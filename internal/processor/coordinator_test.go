package processor

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/store"
)

var errInjected = errors.New("injected write failure")

type fakeStore struct {
	receipts         map[core.DeliveryID]core.ProcessingReceipt
	failStage        string
	commitState      commitState
	commitErr        error
	persistOnUnknown bool
	readbackErr      error
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
	if s.beginCount > 0 && s.readbackErr != nil {
		return core.ProcessingReceipt{}, false, s.readbackErr
	}
	receipt, found := s.receipts[id]
	return receipt, found, nil
}

func (s *fakeStore) beginProcessing(context.Context) (processingUnitOfWork, error) {
	s.beginCount++
	s.last = &fakeUnitOfWork{store: s}
	return s.last, nil
}

type fakeUnitOfWork struct {
	store                  *fakeStore
	stagedReceipt          *core.ProcessingReceipt
	parserOutcomes         []core.ParserTerminalOutcome
	detectionOutcomes      []core.DetectionTerminalOutcome
	detectionContributions []core.DetectionContribution
	alerts                 []core.Alert
	decisions              []core.Decision
	projections            []core.DesiredBanProjection
	audits                 []store.CriticalAudit
	rolledBack             bool
	committed              bool
}

func (tx *fakeUnitOfWork) writeParserOutcome(_ context.Context, outcome core.ParserTerminalOutcome) error {
	if err := tx.fail("parser"); err != nil {
		return err
	}
	tx.parserOutcomes = append(tx.parserOutcomes, outcome)
	return nil
}

func (tx *fakeUnitOfWork) writeDetectionOutcome(_ context.Context, outcome core.DetectionTerminalOutcome) error {
	if err := tx.fail("detection"); err != nil {
		return err
	}
	tx.detectionOutcomes = append(tx.detectionOutcomes, outcome)
	return nil
}

func (tx *fakeUnitOfWork) writeDetectionContribution(_ context.Context, contribution core.DetectionContribution) (bool, error) {
	if err := tx.fail("detection"); err != nil {
		return false, err
	}
	tx.detectionContributions = append(tx.detectionContributions, contribution)
	return true, nil
}

func (tx *fakeUnitOfWork) writeAlert(_ context.Context, alert core.Alert) error {
	if err := tx.fail("alert"); err != nil {
		return err
	}
	tx.alerts = append(tx.alerts, alert)
	return nil
}

func (tx *fakeUnitOfWork) recordAutomaticDecision(_ context.Context, request decision.AutomaticRequest) error {
	if err := tx.fail("decision"); err != nil {
		return err
	}
	if err := tx.fail("projection"); err != nil {
		return err
	}
	tx.decisions = append(tx.decisions, core.Decision{ID: request.DecisionID})
	return nil
}

func (tx *fakeUnitOfWork) writeCriticalAudit(_ context.Context, audit store.CriticalAudit) error {
	if err := tx.fail("audit"); err != nil {
		return err
	}
	tx.audits = append(tx.audits, audit)
	return nil
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
	runner := &zeroOutcomeRunner{}

	if _, err := NewCoordinator(store, runner).Process(context.Background(), testDelivery(t, 1)); !errors.Is(err, ErrCommitRejected) {
		t.Fatalf("Process() error = %v, want ErrCommitRejected", err)
	}
	if !store.last.rolledBack || store.last.committed {
		t.Fatalf("rejected transaction state = rolledBack:%v committed:%v", store.last.rolledBack, store.last.committed)
	}
	if confirmed, aborted := runner.last.lifecycle.state(); confirmed || !aborted {
		t.Fatalf("prepared lifecycle confirmed=%v aborted=%v", confirmed, aborted)
	}
}

func (tx *fakeUnitOfWork) rollback() error {
	tx.rolledBack = true
	tx.stagedReceipt = nil
	return nil
}

type fullRunner struct {
	calls int
	last  *fullPreparedAttempt
}

func (r *fullRunner) prepare(_ context.Context, delivery core.Delivery) (preparedAttempt, error) {
	r.calls++
	r.last = &fullPreparedAttempt{delivery: delivery}
	return r.last, nil
}

type fullPreparedAttempt struct {
	delivery  core.Delivery
	lifecycle attemptLifecycle
}

func (p *fullPreparedAttempt) write(ctx context.Context, writer outcomeWriter) error {
	delivery := p.delivery
	outcome, contribution, alert, automaticDecision, audit := completeOutcomeFixture(delivery)
	if err := writer.writeParserOutcome(ctx, outcome); err != nil {
		return err
	}
	if err := writer.writeDetectionOutcome(ctx, core.DetectionTerminalOutcome{
		DeliveryID: delivery.ID, EventID: contribution.EventID, RuleID: contribution.RuleID,
		RuleVersion: contribution.RuleVersion, Kind: core.DetectionOutcomeSuccess,
		CompletedAt: delivery.Record.ObservedAt,
	}); err != nil {
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
	if err := writer.recordAutomaticDecision(ctx, automaticDecision); err != nil {
		return err
	}
	return writer.writeCriticalAudit(ctx, audit)
}

func (p *fullPreparedAttempt) terminal() (core.ReceiptKind, *core.PermanentFailure) {
	return core.ReceiptSuccess, nil
}

func (p *fullPreparedAttempt) confirm() { p.lifecycle.confirm() }
func (p *fullPreparedAttempt) abort()   { p.lifecycle.abort() }

type zeroOutcomeRunner struct {
	calls int
	last  *zeroPreparedAttempt
}

func (r *zeroOutcomeRunner) prepare(context.Context, core.Delivery) (preparedAttempt, error) {
	r.calls++
	r.last = &zeroPreparedAttempt{kind: core.ReceiptSuccess}
	return r.last, nil
}

type zeroPreparedAttempt struct {
	kind      core.ReceiptKind
	failure   *core.PermanentFailure
	lifecycle attemptLifecycle
}

func (*zeroPreparedAttempt) write(context.Context, outcomeWriter) error { return nil }
func (p *zeroPreparedAttempt) terminal() (core.ReceiptKind, *core.PermanentFailure) {
	return p.kind, p.failure
}
func (p *zeroPreparedAttempt) confirm() { p.lifecycle.confirm() }
func (p *zeroPreparedAttempt) abort()   { p.lifecycle.abort() }

type attemptLifecycle struct {
	mu        sync.Mutex
	confirmed bool
	aborted   bool
}

func (l *attemptLifecycle) confirm() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.aborted {
		return
	}
	l.confirmed = true
}

func (l *attemptLifecycle) abort() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.confirmed {
		return
	}
	l.aborted = true
}

func (l *attemptLifecycle) state() (bool, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.confirmed, l.aborted
}

func TestCoordinator_RollbackAtEveryChildWrite(t *testing.T) {
	for _, stage := range []string{"parser", "detection", "alert", "decision", "projection", "audit", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			store := newFakeStore()
			store.failStage = stage
			runner := &fullRunner{}
			coordinator := NewCoordinator(store, runner)

			if _, err := coordinator.Process(context.Background(), testDelivery(t, 1)); !errors.Is(err, errInjected) {
				t.Fatalf("Process() error = %v, want injected failure", err)
			}
			if store.last == nil || !store.last.rolledBack {
				t.Fatal("failed child write did not roll back the unit of work")
			}
			if len(store.receipts) != 0 {
				t.Fatalf("persisted receipts = %d, want 0", len(store.receipts))
			}
			if confirmed, aborted := runner.last.lifecycle.state(); confirmed || !aborted {
				t.Fatalf("prepared lifecycle confirmed=%v aborted=%v", confirmed, aborted)
			}
		})
	}
}

func completeOutcomeFixture(delivery core.Delivery) (
	core.ParserTerminalOutcome,
	core.DetectionContribution,
	core.Alert,
	decision.AutomaticRequest,
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
	}, decision.AutomaticRequest{
		DecisionID: decisionID, DeliveryID: delivery.ID, EventID: eventID, NodeID: nodeID,
		RuleID: ruleID, RuleVersion: &ruleVersion, AlertID: &alertID,
		Target: target, TriggeredAt: now,
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
	if confirmed, aborted := runner.last.lifecycle.state(); !confirmed || aborted {
		t.Fatalf("prepared lifecycle confirmed=%v aborted=%v", confirmed, aborted)
	}
}

func TestCoordinator_CommitUnknownWithReceiptReadbackCompletes(t *testing.T) {
	store := newFakeStore()
	store.commitState = commitUnknown
	store.persistOnUnknown = true
	delivery := testDelivery(t, 1)

	runner := &zeroOutcomeRunner{}
	completion, err := NewCoordinator(store, runner).Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if completion.DeliveryID != delivery.ID {
		t.Fatalf("completion delivery id = %q, want %q", completion.DeliveryID, delivery.ID)
	}
	if confirmed, aborted := runner.last.lifecycle.state(); !confirmed || aborted {
		t.Fatalf("prepared lifecycle confirmed=%v aborted=%v", confirmed, aborted)
	}
}

func TestCoordinator_CommitUnknownWithoutReceiptDoesNotComplete(t *testing.T) {
	store := newFakeStore()
	store.commitState = commitUnknown
	runner := &zeroOutcomeRunner{}

	if _, err := NewCoordinator(store, runner).Process(context.Background(), testDelivery(t, 1)); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("Process() error = %v, want ErrCommitUnknown", err)
	}
	if store.last.rolledBack {
		t.Fatal("ambiguous commit must not be rolled back as though it were rejected")
	}
	if confirmed, aborted := runner.last.lifecycle.state(); confirmed || !aborted {
		t.Fatalf("prepared lifecycle confirmed=%v aborted=%v", confirmed, aborted)
	}
}

func TestCoordinator_CommitUnknownReadbackErrorResolvesPendingOnReplay(t *testing.T) {
	store := newFakeStore()
	store.commitState = commitUnknown
	store.persistOnUnknown = true
	store.readbackErr = errors.New("readback unavailable")
	runner := &zeroOutcomeRunner{}
	coordinator := NewCoordinator(store, runner)
	delivery := testDelivery(t, 1)

	if _, err := coordinator.Process(context.Background(), delivery); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("first Process() error = %v, want ErrCommitUnknown", err)
	}
	if confirmed, aborted := runner.last.lifecycle.state(); confirmed || aborted {
		t.Fatalf("unresolved lifecycle confirmed=%v aborted=%v", confirmed, aborted)
	}
	store.readbackErr = nil
	if _, err := coordinator.Process(context.Background(), delivery); err != nil {
		t.Fatalf("replay Process(): %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("prepare calls = %d, want 1", runner.calls)
	}
	if confirmed, aborted := runner.last.lifecycle.state(); !confirmed || aborted {
		t.Fatalf("resolved lifecycle confirmed=%v aborted=%v", confirmed, aborted)
	}
}

func TestCoordinator_PrepareFailureDoesNotOpenTransaction(t *testing.T) {
	store := newFakeStore()
	runner := &prepareFailureRunner{err: errInjected}

	if _, err := NewCoordinator(store, runner).Process(context.Background(), testDelivery(t, 1)); !errors.Is(err, errInjected) {
		t.Fatalf("Process() error = %v, want injected failure", err)
	}
	if store.beginCount != 0 || store.last != nil {
		t.Fatalf("prepare failure opened transaction: begins=%d last=%v", store.beginCount, store.last)
	}
}

func TestCoordinator_RecordPermanentWritesPoisonReceipt(t *testing.T) {
	store := newFakeStore()
	failure := &core.PermanentFailure{
		Stage: "parser:parser-1", Code: "malformed_record", SanitizedError: "record rejected",
		Action: "terminal_reject", OccurredAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	runner := &terminalRunner{kind: core.ReceiptRecordPermanent, failure: failure}
	delivery := testDelivery(t, 1)

	if _, err := NewCoordinator(store, runner).Process(context.Background(), delivery); err != nil {
		t.Fatalf("Process(): %v", err)
	}
	receipt := store.receipts[delivery.ID]
	if receipt.Kind != core.ReceiptRecordPermanent || receipt.Failure == nil ||
		receipt.Failure.Code != failure.Code {
		t.Fatalf("poison receipt = %+v", receipt)
	}
}

type prepareFailureRunner struct{ err error }

func (r *prepareFailureRunner) prepare(context.Context, core.Delivery) (preparedAttempt, error) {
	return nil, r.err
}

type terminalRunner struct {
	kind    core.ReceiptKind
	failure *core.PermanentFailure
	last    *zeroPreparedAttempt
}

func (r *terminalRunner) prepare(context.Context, core.Delivery) (preparedAttempt, error) {
	r.last = &zeroPreparedAttempt{kind: r.kind, failure: r.failure}
	return r.last, nil
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

func (r *blockingRunner) prepare(ctx context.Context, _ core.Delivery) (preparedAttempt, error) {
	r.calls++
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return &zeroPreparedAttempt{kind: core.ReceiptSuccess}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
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
