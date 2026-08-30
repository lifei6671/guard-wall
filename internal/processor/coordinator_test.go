package processor

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

var errInjected = errors.New("injected write failure")

type fakeStore struct {
	receipts         map[core.DeliveryID]core.ProcessingReceipt
	failStage        string
	commitState      commitState
	commitErr        error
	persistOnUnknown bool
	beginCount       int
	last             *fakeUnitOfWork
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		receipts:    make(map[core.DeliveryID]core.ProcessingReceipt),
		commitState: commitConfirmed,
	}
}

func (s *fakeStore) findReceipt(_ context.Context, id core.DeliveryID) (core.ProcessingReceipt, bool, error) {
	receipt, found := s.receipts[id]
	return receipt, found, nil
}

func (s *fakeStore) beginProcessing(context.Context) (processingUnitOfWork, error) {
	s.beginCount++
	s.last = &fakeUnitOfWork{store: s}
	return s.last, nil
}

type fakeUnitOfWork struct {
	store         *fakeStore
	stagedReceipt *core.ProcessingReceipt
	rolledBack    bool
	committed     bool
}

func (tx *fakeUnitOfWork) writeParserOutcome(context.Context, core.ParserTerminalOutcome) error {
	return tx.fail("parser")
}

func (tx *fakeUnitOfWork) writeDetectionContribution(context.Context, core.DetectionContribution) (bool, error) {
	if err := tx.fail("detection"); err != nil {
		return false, err
	}
	return true, nil
}

func (tx *fakeUnitOfWork) writeAlert(context.Context, core.Alert) error {
	return tx.fail("alert")
}

func (tx *fakeUnitOfWork) writeDecision(context.Context, core.Decision) error {
	return tx.fail("decision")
}

func (tx *fakeUnitOfWork) writeProjection(context.Context, core.DesiredBanProjection, time.Time) error {
	return tx.fail("projection")
}

func (tx *fakeUnitOfWork) writeCriticalAudit(context.Context, store.CriticalAudit) error {
	return tx.fail("audit")
}

func (tx *fakeUnitOfWork) writeReceipt(_ context.Context, receipt core.ProcessingReceipt) error {
	if err := tx.fail("receipt"); err != nil {
		return err
	}
	tx.stagedReceipt = &receipt
	return nil
}

func (tx *fakeUnitOfWork) fail(stage string) error {
	if tx.store.failStage == stage {
		return errInjected
	}
	return nil
}

func (tx *fakeUnitOfWork) commit(context.Context) (commitState, error) {
	tx.committed = tx.store.commitState == commitConfirmed
	if tx.store.commitState == commitRejected {
		tx.rolledBack = true
		tx.stagedReceipt = nil
	}
	if tx.stagedReceipt != nil && (tx.store.commitState == commitConfirmed || tx.store.persistOnUnknown) {
		tx.store.receipts[tx.stagedReceipt.DeliveryID] = *tx.stagedReceipt
	}
	return tx.store.commitState, tx.store.commitErr
}

func TestCoordinator_CommitRejectedIsAlreadyRolledBack(t *testing.T) {
	store := newFakeStore()
	store.commitState = commitRejected
	store.commitErr = context.Canceled

	if _, err := NewCoordinator(store, &zeroOutcomeRunner{}).Process(context.Background(), testDelivery(t, 1)); !errors.Is(err, ErrCommitRejected) {
		t.Fatalf("Process() error = %v, want ErrCommitRejected", err)
	}
	if !store.last.rolledBack || store.last.committed {
		t.Fatalf("rejected transaction state = rolledBack:%v committed:%v", store.last.rolledBack, store.last.committed)
	}
}

func (tx *fakeUnitOfWork) rollback() error {
	tx.rolledBack = true
	tx.stagedReceipt = nil
	return nil
}

type fullRunner struct {
	calls int
}

func (r *fullRunner) run(ctx context.Context, writer outcomeWriter, delivery core.Delivery) error {
	r.calls++
	outcome, contribution, alert, decision, projection, audit := completeOutcomeFixture(delivery)
	if err := writer.writeParserOutcome(ctx, outcome); err != nil {
		return err
	}
	inserted, err := writer.writeDetectionContribution(ctx, contribution)
	if err != nil {
		return err
	}
	if !inserted {
		return errors.New("new processing attempt did not insert detection contribution")
	}
	if err := writer.writeAlert(ctx, alert); err != nil {
		return err
	}
	if err := writer.writeDecision(ctx, decision); err != nil {
		return err
	}
	if err := writer.writeProjection(ctx, projection, delivery.Record.ObservedAt); err != nil {
		return err
	}
	return writer.writeCriticalAudit(ctx, audit)
}

type zeroOutcomeRunner struct {
	calls int
}

func (r *zeroOutcomeRunner) run(context.Context, outcomeWriter, core.Delivery) error {
	r.calls++
	return nil
}

func TestCoordinator_RollbackAtEveryChildWrite(t *testing.T) {
	for _, stage := range []string{"parser", "detection", "alert", "decision", "projection", "audit", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			store := newFakeStore()
			store.failStage = stage
			coordinator := NewCoordinator(store, &fullRunner{})

			if _, err := coordinator.Process(context.Background(), testDelivery(t, 1)); !errors.Is(err, errInjected) {
				t.Fatalf("Process() error = %v, want injected failure", err)
			}
			if store.last == nil || !store.last.rolledBack {
				t.Fatal("failed child write did not roll back the unit of work")
			}
			if len(store.receipts) != 0 {
				t.Fatalf("persisted receipts = %d, want 0", len(store.receipts))
			}
		})
	}
}

func completeOutcomeFixture(delivery core.Delivery) (
	core.ParserTerminalOutcome,
	core.DetectionContribution,
	core.Alert,
	core.Decision,
	core.DesiredBanProjection,
	store.CriticalAudit,
) {
	nodeID := core.NodeID("00112233445566778899aabbccddeeff")
	parserID := core.ParserID("parser-1")
	parserVersion := core.ParserVersion("v1")
	ruleID := core.RuleID("rule-1")
	ruleVersion := core.RuleVersion("v1")
	eventID, err := core.SecurityEventID(nodeID, delivery.ID, parserID, parserVersion, 0)
	if err != nil {
		panic(err)
	}
	now := delivery.Record.ObservedAt
	target := netip.MustParsePrefix("192.0.2.1/32")
	alertID := core.AlertID("alert-" + string(delivery.ID))
	decisionID := core.DecisionID("decision-" + string(delivery.ID))
	return core.ParserTerminalOutcome{
		DeliveryID: delivery.ID, ParserID: parserID, ParserVersion: parserVersion,
		Kind: core.ParserOutcomeSuccess, EmittedCount: 1, CompletedAt: now,
	}, core.DetectionContribution{
		DeliveryID: delivery.ID, EventID: eventID, RuleID: ruleID,
		RuleVersion: ruleVersion, ContributedAt: now,
	}, core.Alert{
		ID: alertID, NodeID: nodeID, EventID: eventID, RuleID: ruleID,
		RuleVersion: ruleVersion, CanonicalTarget: target, ObservedAt: now, CreatedAt: now,
	}, core.Decision{
		ID: decisionID, NodeID: nodeID, Source: core.DecisionSourceAutomatic,
		RuleID: &ruleID, RuleVersion: &ruleVersion, AlertID: &alertID,
		CanonicalTarget: target, CreatedAt: now, UpdatedAt: now,
		LastTriggeredAt: now, State: core.DecisionActive,
	}, core.DesiredBanProjection{
		NodeID: nodeID, CanonicalTarget: target, State: core.BanProjectionPresent,
		ActiveCount: 1, Revision: 1,
	}, store.CriticalAudit{
		ID: "audit-" + string(delivery.ID), IdempotencyKey: "processing-" + string(delivery.ID),
		NodeID: nodeID, Category: "processing", Action: "completed", Result: "success",
		Severity: "info", ActorType: "source", DeliveryID: &delivery.ID,
		AlertID: &alertID, DecisionID: &decisionID, CreatedAt: now,
	}
}

func TestCoordinator_CommittedReceiptReplaySkipsAttempt(t *testing.T) {
	store := newFakeStore()
	runner := &fullRunner{}
	coordinator := NewCoordinator(store, runner)
	delivery := testDelivery(t, 1)

	first, err := coordinator.Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("first Process() error = %v", err)
	}
	delivery.Sequence = 7
	second, err := coordinator.Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("replay Process() error = %v", err)
	}
	if runner.calls != 1 || store.beginCount != 1 {
		t.Fatalf("runner calls/begins = %d/%d, want 1/1", runner.calls, store.beginCount)
	}
	if first.DeliveryID != second.DeliveryID || second.Sequence != 7 {
		t.Fatalf("unexpected replay completion: %#v", second)
	}
}

func TestCoordinator_ZeroOutcomeStillWritesReceipt(t *testing.T) {
	store := newFakeStore()
	runner := &zeroOutcomeRunner{}
	delivery := testDelivery(t, 1)

	completion, err := NewCoordinator(store, runner).Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if completion.DeliveryID != delivery.ID {
		t.Fatalf("completion delivery id = %q, want %q", completion.DeliveryID, delivery.ID)
	}
	if _, found := store.receipts[delivery.ID]; !found {
		t.Fatal("zero-outcome attempt did not persist a receipt")
	}
}

func TestCoordinator_CommitUnknownWithReceiptReadbackCompletes(t *testing.T) {
	store := newFakeStore()
	store.commitState = commitUnknown
	store.persistOnUnknown = true
	delivery := testDelivery(t, 1)

	completion, err := NewCoordinator(store, &zeroOutcomeRunner{}).Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if completion.DeliveryID != delivery.ID {
		t.Fatalf("completion delivery id = %q, want %q", completion.DeliveryID, delivery.ID)
	}
}

func TestCoordinator_CommitUnknownWithoutReceiptDoesNotComplete(t *testing.T) {
	store := newFakeStore()
	store.commitState = commitUnknown

	if _, err := NewCoordinator(store, &zeroOutcomeRunner{}).Process(context.Background(), testDelivery(t, 1)); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("Process() error = %v, want ErrCommitUnknown", err)
	}
	if store.last.rolledBack {
		t.Fatal("ambiguous commit must not be rolled back as though it were rejected")
	}
}

func TestCoordinator_ConcurrentDuplicateRunsAttemptOnce(t *testing.T) {
	store := newFakeStore()
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	coordinator := NewCoordinator(store, runner)
	delivery := testDelivery(t, 1)

	results := make(chan error, 2)
	go func() {
		_, err := coordinator.Process(context.Background(), delivery)
		results <- err
	}()
	<-runner.started
	go func() {
		_, err := coordinator.Process(context.Background(), delivery)
		results <- err
	}()
	close(runner.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if runner.calls != 1 || store.beginCount != 1 {
		t.Fatalf("runner calls/begins = %d/%d, want 1/1", runner.calls, store.beginCount)
	}
}

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   int
}

func (r *blockingRunner) run(ctx context.Context, _ outcomeWriter, _ core.Delivery) error {
	r.calls++
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func testDelivery(t *testing.T, sequence core.DeliverySequence) core.Delivery {
	t.Helper()
	position, err := core.NewFilePosition(core.FilePosition{
		Generation:  "00112233445566778899aabbccddeeff",
		DeviceID:    1,
		Inode:       2,
		StartOffset: 10,
		EndOffset:   20,
	})
	if err != nil {
		t.Fatalf("NewFilePosition() error = %v", err)
	}
	deliveryID, err := core.FileDeliveryID("source-1", core.FilePosition{
		Generation:  "00112233445566778899aabbccddeeff",
		StartOffset: 10,
		EndOffset:   20,
	})
	if err != nil {
		t.Fatalf("FileDeliveryID() error = %v", err)
	}
	return core.Delivery{
		ID:       deliveryID,
		Sequence: sequence,
		Record: core.RawRecord{
			SourceID:   core.SourceID("source-1"),
			ObservedAt: time.Unix(1_700_000_000, 0).UTC(),
			Position:   position,
			Content:    []byte("record"),
		},
	}
}
