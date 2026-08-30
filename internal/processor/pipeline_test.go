package processor

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/detection"
)

type scriptedRuleEvaluator struct {
	match         RuleMatch
	matchErr      error
	evaluateErr   error
	effects       func(RuleSnapshot, core.SecurityEvent, detection.Snapshot) DetectionEffects
	matchCalls    int
	evaluateCalls int
	lastSnapshot  detection.Snapshot
}

type statelessParserRunner struct{}

func (statelessParserRunner) RunParser(
	context.Context,
	ParserSnapshot,
	core.RawRecord,
) (ParserExecution, error) {
	return ParserExecution{Events: []core.EventFields{{EventType: "auth.login_failed"}}}, nil
}

type statelessRuleEvaluator struct{}

func (statelessRuleEvaluator) Match(
	context.Context,
	RuleSnapshot,
	core.SecurityEvent,
) (RuleMatch, error) {
	return RuleMatch{Applicable: true, GroupKey: "group-a", DistinctKey: "alice"}, nil
}

func (statelessRuleEvaluator) Evaluate(
	context.Context,
	RuleSnapshot,
	core.SecurityEvent,
	detection.Snapshot,
) (DetectionEffects, error) {
	return DetectionEffects{}, nil
}

type synchronizedReceiptStore struct {
	*fakeStore
	mu           sync.Mutex
	reads        int
	firstEntered chan struct{}
	releaseFirst chan struct{}
}

func (s *synchronizedReceiptStore) findReceipt(
	ctx context.Context,
	id core.DeliveryID,
) (core.ProcessingReceipt, bool, error) {
	s.mu.Lock()
	s.reads++
	first := s.reads == 1
	s.mu.Unlock()
	if first {
		close(s.firstEntered)
		<-s.releaseFirst
	}
	return s.fakeStore.findReceipt(ctx, id)
}

func (e *scriptedRuleEvaluator) Match(
	context.Context,
	RuleSnapshot,
	core.SecurityEvent,
) (RuleMatch, error) {
	e.matchCalls++
	return e.match, e.matchErr
}

func (e *scriptedRuleEvaluator) Evaluate(
	_ context.Context,
	rule RuleSnapshot,
	event core.SecurityEvent,
	snapshot detection.Snapshot,
) (DetectionEffects, error) {
	e.evaluateCalls++
	e.lastSnapshot = snapshot
	if e.evaluateErr != nil {
		return DetectionEffects{}, e.evaluateErr
	}
	if e.effects == nil {
		return DetectionEffects{}, nil
	}
	return e.effects(rule, event, snapshot), nil
}

func TestPipelineFullSuccessConfirmsWindowAfterDurableCommit(t *testing.T) {
	delivery := testPlanDelivery(t)
	ledger := detection.NewLedger()
	evaluator := &scriptedRuleEvaluator{
		match:   RuleMatch{Applicable: true, GroupKey: "group-a", DistinctKey: "alice"},
		effects: fullDetectionEffects(delivery.Record.ObservedAt),
	}
	pipeline := testPipeline(t, ledger, evaluator, nil)
	store := newFakeStore()

	completion, err := NewCoordinator(store, pipeline).Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("Process(): %v", err)
	}
	if completion.DeliveryID != delivery.ID || evaluator.lastSnapshot != (detection.Snapshot{Count: 1, DistinctCount: 1}) {
		t.Fatalf("completion/snapshot = %+v/%+v", completion, evaluator.lastSnapshot)
	}
	if got := len(store.last.parserOutcomes); got != 1 {
		t.Fatalf("parser outcomes = %d, want 1", got)
	}
	if got := len(store.last.detectionContributions); got != 1 {
		t.Fatalf("detection contributions = %d, want 1", got)
	}
	if len(store.last.alerts) != 1 || len(store.last.decisions) != 0 ||
		len(store.last.projections) != 0 || len(store.last.audits) != 0 {
		t.Fatalf("effects = alerts:%d decisions:%d projections:%d audits:%d",
			len(store.last.alerts), len(store.last.decisions), len(store.last.projections), len(store.last.audits))
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{Count: 1, DistinctCount: 1})

	if _, err := NewCoordinator(store, pipeline).Process(context.Background(), delivery); err != nil {
		t.Fatalf("replay Process(): %v", err)
	}
	if evaluator.matchCalls != 1 || evaluator.evaluateCalls != 1 || store.beginCount != 1 {
		t.Fatalf("replay re-entered pipeline: match=%d evaluate=%d begin=%d",
			evaluator.matchCalls, evaluator.evaluateCalls, store.beginCount)
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{Count: 1, DistinctCount: 1})
}

func TestPipelineRollbackAbortsWindowAndRetryContributesOnce(t *testing.T) {
	delivery := testPlanDelivery(t)
	ledger := detection.NewLedger()
	evaluator := &scriptedRuleEvaluator{
		match:   RuleMatch{Applicable: true, GroupKey: "group-a", DistinctKey: "alice"},
		effects: fullDetectionEffects(delivery.Record.ObservedAt),
	}
	pipeline := testPipeline(t, ledger, evaluator, nil)
	fake := newFakeStore()
	fake.failStage = "alert"
	coordinator := NewCoordinator(fake, pipeline)

	if _, err := coordinator.Process(context.Background(), delivery); !errors.Is(err, errInjected) {
		t.Fatalf("failed Process() error = %v", err)
	}
	if !fake.last.rolledBack {
		t.Fatal("failed detection attempt did not roll back")
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{})

	fake.failStage = ""
	if _, err := coordinator.Process(context.Background(), delivery); err != nil {
		t.Fatalf("retry Process(): %v", err)
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{Count: 1, DistinctCount: 1})
	if evaluator.evaluateCalls != 2 || fake.beginCount != 2 {
		t.Fatalf("retry calls/begins = %d/%d, want 2/2", evaluator.evaluateCalls, fake.beginCount)
	}
}

func TestPipelineEvaluateFailureReleasesReservationBeforeTransaction(t *testing.T) {
	delivery := testPlanDelivery(t)
	ledger := detection.NewLedger()
	evaluator := &scriptedRuleEvaluator{
		match:       RuleMatch{Applicable: true, GroupKey: "group-a", DistinctKey: "alice"},
		evaluateErr: &PlanFailure{Class: PlanFailureTransient, Cause: errors.New("rule worker unavailable")},
	}
	pipeline := testPipeline(t, ledger, evaluator, nil)
	fake := newFakeStore()

	if _, err := NewCoordinator(fake, pipeline).Process(context.Background(), delivery); err == nil {
		t.Fatal("expected evaluation failure")
	}
	if fake.beginCount != 0 {
		t.Fatalf("evaluation failure opened %d transactions", fake.beginCount)
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{})
}

func TestPipelineCommitUnknownControlsWindowLifecycle(t *testing.T) {
	tests := []struct {
		name             string
		persistOnUnknown bool
		readbackErr      error
		wantError        bool
		wantSnapshot     detection.Snapshot
		resolveReplay    bool
	}{
		{name: "receipt found confirms", persistOnUnknown: true, wantSnapshot: detection.Snapshot{Count: 1, DistinctCount: 1}},
		{name: "receipt absent aborts", wantError: true},
		{name: "readback failure stays pending then confirms", persistOnUnknown: true, readbackErr: errors.New("readback unavailable"), wantError: true, resolveReplay: true, wantSnapshot: detection.Snapshot{Count: 1, DistinctCount: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delivery := testPlanDelivery(t)
			ledger := detection.NewLedger()
			evaluator := &scriptedRuleEvaluator{match: RuleMatch{
				Applicable: true, GroupKey: "group-a", DistinctKey: "alice",
			}}
			pipeline := testPipeline(t, ledger, evaluator, nil)
			fake := newFakeStore()
			fake.commitState = commitUnknown
			fake.persistOnUnknown = test.persistOnUnknown
			fake.readbackErr = test.readbackErr
			coordinator := NewCoordinator(fake, pipeline)

			_, err := coordinator.Process(context.Background(), delivery)
			if test.wantError != (err != nil) {
				t.Fatalf("Process() error = %v, wantError=%v", err, test.wantError)
			}
			if test.resolveReplay {
				fake.readbackErr = nil
				if _, err := coordinator.Process(context.Background(), delivery); err != nil {
					t.Fatalf("replay Process(): %v", err)
				}
			}
			assertWindowSnapshot(t, ledger, test.wantSnapshot)
		})
	}
}

func TestPipelineConfirmedCommitErrorStillConfirmsWindow(t *testing.T) {
	delivery := testPlanDelivery(t)
	ledger := detection.NewLedger()
	evaluator := &scriptedRuleEvaluator{match: RuleMatch{
		Applicable: true, GroupKey: "group-a", DistinctKey: "alice",
	}}
	pipeline := testPipeline(t, ledger, evaluator, nil)
	fake := newFakeStore()
	fake.commitErr = errors.New("adapter invariant failure")

	if _, err := NewCoordinator(fake, pipeline).Process(context.Background(), delivery); err == nil {
		t.Fatal("expected confirmed commit adapter error")
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{Count: 1, DistinctCount: 1})
}

func TestPipelineDeferredReservationResolvesAcrossCoordinatorReconstruction(t *testing.T) {
	delivery := testPlanDelivery(t)
	ledger := detection.NewLedger()
	evaluator := &scriptedRuleEvaluator{match: RuleMatch{
		Applicable: true, GroupKey: "group-a", DistinctKey: "alice",
	}}
	pipeline := testPipeline(t, ledger, evaluator, nil)
	fake := newFakeStore()
	fake.commitState = commitUnknown
	fake.persistOnUnknown = true
	fake.readbackErr = errors.New("readback unavailable")

	if _, err := NewCoordinator(fake, pipeline).Process(context.Background(), delivery); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("first Process() error = %v", err)
	}
	fake.readbackErr = nil
	if _, err := NewCoordinator(fake, pipeline).Process(context.Background(), delivery); err != nil {
		t.Fatalf("reconstructed Coordinator Process(): %v", err)
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{Count: 1, DistinctCount: 1})
	if evaluator.evaluateCalls != 1 || fake.beginCount != 1 {
		t.Fatalf("resolved attempt re-ran: evaluate=%d begin=%d", evaluator.evaluateCalls, fake.beginCount)
	}
}

func TestPipelineDeferredAbsentReceiptAbortsThenReprepares(t *testing.T) {
	delivery := testPlanDelivery(t)
	ledger := detection.NewLedger()
	evaluator := &scriptedRuleEvaluator{match: RuleMatch{
		Applicable: true, GroupKey: "group-a", DistinctKey: "alice",
	}}
	pipeline := testPipeline(t, ledger, evaluator, nil)
	fake := newFakeStore()
	fake.commitState = commitUnknown
	fake.readbackErr = errors.New("readback unavailable")

	if _, err := NewCoordinator(fake, pipeline).Process(context.Background(), delivery); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("first Process() error = %v", err)
	}
	fake.readbackErr = nil
	fake.commitState = commitConfirmed
	if _, err := NewCoordinator(fake, pipeline).Process(context.Background(), delivery); err != nil {
		t.Fatalf("reconstructed Coordinator retry: %v", err)
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{Count: 1, DistinctCount: 1})
	if evaluator.evaluateCalls != 2 || fake.beginCount != 2 {
		t.Fatalf("absent retry calls/begins = %d/%d, want 2/2", evaluator.evaluateCalls, fake.beginCount)
	}
}

func TestPipelineSharedLedgerSerializesOverlappingCoordinatorsForSameDelivery(t *testing.T) {
	delivery := testPlanDelivery(t)
	ledger := detection.NewLedger()
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-a", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-a", Version: "v1"}},
	}
	pipeline := NewPipeline(planNodeID, catalog, statelessParserRunner{}, statelessRuleEvaluator{}, ledger)
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
	sharedStore := &synchronizedReceiptStore{
		fakeStore: newFakeStore(), firstEntered: make(chan struct{}), releaseFirst: make(chan struct{}),
	}

	errorsFound := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := NewCoordinator(sharedStore, pipeline).Process(context.Background(), delivery)
			errorsFound <- err
		}()
	}
	<-sharedStore.firstEntered
	close(sharedStore.releaseFirst)
	var successes, failures int
	for range 2 {
		if err := <-errorsFound; err != nil {
			failures++
		} else {
			successes++
		}
	}
	if successes != 2 || failures != 0 || sharedStore.beginCount != 1 {
		t.Fatalf("overlap results success=%d failure=%d begins=%d", successes, failures, sharedStore.beginCount)
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{Count: 1, DistinctCount: 1})
}

func TestPipelineMixedParserPermanentWritesPoisonAndContinuesDetection(t *testing.T) {
	delivery := testPlanDelivery(t)
	ledger := detection.NewLedger()
	evaluator := &scriptedRuleEvaluator{match: RuleMatch{
		Applicable: true, GroupKey: "group-a", DistinctKey: "alice",
	}}
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{
			{ParserID: "parser-a", Version: "v1", Priority: 10},
			{ParserID: "parser-b", Version: "v1", Priority: 20},
		},
		rules: []RuleSnapshot{{RuleID: "rule-a", Version: "v1"}},
	}
	runner := &scriptedParserRunner{
		failures: map[core.ParserID]error{"parser-a": &PlanFailure{
			Class: PlanFailureRecordPermanent, Code: "malformed_record",
			SanitizedError: "record rejected", Action: "terminal_reject",
			Cause: errors.New("secret raw record content"),
		}},
		events: map[core.ParserID][]core.EventFields{"parser-b": {{EventType: "auth.login_failed"}}},
	}
	pipeline := NewPipeline(planNodeID, catalog, runner, evaluator, ledger)
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
	fake := newFakeStore()

	if _, err := NewCoordinator(fake, pipeline).Process(context.Background(), delivery); err != nil {
		t.Fatalf("Process(): %v", err)
	}
	receipt := fake.receipts[delivery.ID]
	if receipt.Kind != core.ReceiptRecordPermanent || receipt.Failure == nil ||
		receipt.Failure.Stage != "parser" || receipt.Failure.Code != "malformed_record" {
		t.Fatalf("poison receipt = %+v", receipt)
	}
	if len(fake.last.parserOutcomes) != 2 || len(fake.last.detectionContributions) != 1 || len(fake.last.audits) != 1 {
		t.Fatalf("mixed outcomes = parsers:%d detections:%d audits:%d",
			len(fake.last.parserOutcomes), len(fake.last.detectionContributions), len(fake.last.audits))
	}
	if strings.Contains(string(fake.last.audits[0].DetailsJSON), "secret") ||
		strings.Contains(receipt.Failure.SanitizedError, "secret") {
		t.Fatal("poison persistence leaked the internal parser cause")
	}
	assertWindowSnapshot(t, ledger, detection.Snapshot{Count: 1, DistinctCount: 1})
}

func TestPipelineRuleRecordPermanentContinuesOtherApplicableRules(t *testing.T) {
	delivery := testPlanDelivery(t)
	ledger := detection.NewLedger()
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-a", Version: "v1"}},
		rules: []RuleSnapshot{
			{RuleID: "rule-a", Version: "v1"},
			{RuleID: "rule-b", Version: "v1"},
			{RuleID: "rule-c", Version: "v1"},
		},
	}
	runner := &scriptedParserRunner{events: map[core.ParserID][]core.EventFields{
		"parser-a": {{EventType: "auth.login_failed"}},
	}}
	evaluator := &middlePermanentRuleEvaluator{}
	pipeline := NewPipeline(planNodeID, catalog, runner, evaluator, ledger)
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
	database := newFakeStore()
	_, err := NewCoordinator(database, pipeline).Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("Process(): %v", err)
	}
	receipt := database.receipts[delivery.ID]
	if receipt.Kind != core.ReceiptRecordPermanent || receipt.Failure == nil || receipt.Failure.Stage != "detection" ||
		receipt.Failure.Code != "invalid_rule_input" || receipt.Failure.SanitizedError != "rule input is invalid" {
		t.Fatalf("unexpected detection poison receipt: %+v", receipt)
	}
	if len(database.last.detectionOutcomes) != 3 || len(database.last.detectionContributions) != 2 {
		t.Fatalf("outcomes/contributions = %d/%d, want 3/2",
			len(database.last.detectionOutcomes), len(database.last.detectionContributions))
	}
	permanentCount := 0
	for _, outcome := range database.last.detectionOutcomes {
		if outcome.Kind == core.DetectionOutcomeRecordPermanent {
			permanentCount++
			if outcome.RuleID != "rule-b" || outcome.FailureCode != "invalid_rule_input" {
				t.Fatalf("unexpected permanent outcome: %+v", outcome)
			}
		}
	}
	if permanentCount != 1 || len(database.last.audits) != 1 {
		t.Fatalf("permanent outcomes/audits = %d/%d", permanentCount, len(database.last.audits))
	}
	if strings.Contains(string(database.last.audits[0].DetailsJSON), "raw-secret") {
		t.Fatalf("Critical Audit leaked raw cause: %s", database.last.audits[0].DetailsJSON)
	}
	if evaluator.calls["rule-c"] == 0 {
		t.Fatal("rule-c was not evaluated after rule-b terminal rejection")
	}
	for _, ruleID := range []core.RuleID{"rule-a", "rule-b", "rule-c"} {
		snapshot, err := ledger.Snapshot(context.Background(), detection.WindowKey{
			RuleID: ruleID, RuleVersion: "v1", GroupKey: "group-" + string(ruleID),
		})
		if err != nil {
			t.Fatal(err)
		}
		want := uint64(1)
		if ruleID == "rule-b" {
			want = 0
		}
		if snapshot.Count != want {
			t.Fatalf("%s Window count = %d, want %d", ruleID, snapshot.Count, want)
		}
	}
}

type middlePermanentRuleEvaluator struct {
	calls map[core.RuleID]int
}

func TestDetectionPoisonAuditIdentityIsUnambiguous(t *testing.T) {
	delivery := testPlanDelivery(t)
	eventID, err := core.SecurityEventID(planNodeID, delivery.ID, "parser-a", "v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	failure := core.PermanentFailure{
		Code: "invalid", SanitizedError: "invalid input", Action: "skip_rule",
		OccurredAt: delivery.Record.ObservedAt,
	}
	first, err := detectionPoisonAudit(planNodeID, delivery.ID, detectionPermanentFailure{
		eventID: eventID, rule: RuleSnapshot{RuleID: "a:b", Version: "c"}, failure: failure,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := detectionPoisonAudit(planNodeID, delivery.ID, detectionPermanentFailure{
		eventID: eventID, rule: RuleSnapshot{RuleID: "a", Version: "b:c"}, failure: failure,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.IdempotencyKey == second.IdempotencyKey {
		t.Fatalf("ambiguous poison audit identities: %+v / %+v", first, second)
	}
}

func (e *middlePermanentRuleEvaluator) Match(
	_ context.Context,
	rule RuleSnapshot,
	_ core.SecurityEvent,
) (RuleMatch, error) {
	return RuleMatch{
		Applicable: true, GroupKey: "group-" + string(rule.RuleID), DistinctKey: "alice",
	}, nil
}

func (e *middlePermanentRuleEvaluator) Evaluate(
	_ context.Context,
	rule RuleSnapshot,
	_ core.SecurityEvent,
	_ detection.Snapshot,
) (DetectionEffects, error) {
	if e.calls == nil {
		e.calls = make(map[core.RuleID]int)
	}
	e.calls[rule.RuleID]++
	if rule.RuleID == "rule-b" {
		return DetectionEffects{}, &PlanFailure{
			Class: PlanFailureRecordPermanent, Code: "invalid_rule_input",
			SanitizedError: "rule input is invalid", Action: "skip_rule",
			Cause: errors.New("raw-secret record fragment"),
		}
	}
	return DetectionEffects{}, nil
}

func testPipeline(
	t *testing.T,
	ledger *detection.Ledger,
	evaluator RuleEvaluator,
	parserFailures map[core.ParserID]error,
) *Pipeline {
	t.Helper()
	delivery := testPlanDelivery(t)
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-a", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-a", Version: "v1"}},
	}
	runner := &scriptedParserRunner{
		failures: parserFailures,
		events: map[core.ParserID][]core.EventFields{
			"parser-a": {{EventType: "auth.login_failed"}},
		},
	}
	pipeline := NewPipeline(planNodeID, catalog, runner, evaluator, ledger)
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
	return pipeline
}

func fullDetectionEffects(now time.Time) func(RuleSnapshot, core.SecurityEvent, detection.Snapshot) DetectionEffects {
	return func(rule RuleSnapshot, event core.SecurityEvent, _ detection.Snapshot) DetectionEffects {
		target := netip.MustParsePrefix("192.0.2.10/32")
		alertID := core.AlertID("alert-pipeline")
		alert := core.Alert{
			ID: alertID, NodeID: event.NodeID, EventID: event.ID,
			RuleID: rule.RuleID, RuleVersion: rule.Version, CanonicalTarget: target,
			ObservedAt: event.ObservedAt, CreatedAt: now.Add(time.Second),
		}
		return DetectionEffects{Alert: &alert}
	}
}

func assertWindowSnapshot(t *testing.T, ledger *detection.Ledger, want detection.Snapshot) {
	t.Helper()
	got, err := ledger.Snapshot(context.Background(), detection.WindowKey{
		RuleID: "rule-a", RuleVersion: "v1", GroupKey: "group-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Window snapshot = %+v, want %+v", got, want)
	}
}
