package processor

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

const planNodeID core.NodeID = "00112233445566778899aabbccddeeff"

func TestProcessingPlanBuildsStableEventsAndTerminalOutcomes(t *testing.T) {
	delivery := testPlanDelivery(t)
	completedAt := delivery.Record.ObservedAt.Add(time.Second)
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{
			{ParserID: "parser-c", Version: "v1", Priority: 20},
			{ParserID: "parser-b", Version: "v1", Priority: 10},
			{ParserID: "parser-a", Version: "v1", Priority: 10},
		},
		rules: []RuleSnapshot{{RuleID: "rule-b", Version: "v1"}, {RuleID: "rule-a", Version: "v2"}},
	}
	plan, err := BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedParserRunner{
		failures: map[core.ParserID]error{
			"parser-b": &PlanFailure{
				Class: PlanFailureRecordPermanent, Code: "malformed", SanitizedError: "record is malformed",
				Action: "skip_parser", Cause: errors.New("sensitive parser detail"),
			},
		},
		events: map[core.ParserID][]core.EventFields{
			"parser-c": {
				{EventType: "auth.login_failed", Service: "ssh"},
				{EventType: "auth.login_failed", Service: "ssh"},
			},
		},
	}

	result, err := plan.Run(context.Background(), planNodeID, delivery, runner, completedAt)
	if err != nil {
		t.Fatalf("Run(): %v", err)
	}
	if !result.Complete {
		t.Fatal("all terminal parser outcomes did not complete the plan")
	}
	if !reflect.DeepEqual(runner.calls, []core.ParserID{"parser-a", "parser-b", "parser-c"}) {
		t.Fatalf("parser calls = %v", runner.calls)
	}
	wantKinds := []core.ParserOutcomeKind{
		core.ParserOutcomeNoMatch,
		core.ParserOutcomeRecordPermanent,
		core.ParserOutcomeSuccess,
	}
	for index, want := range wantKinds {
		if result.ParserOutcomes[index].Kind != want {
			t.Fatalf("outcome[%d] = %+v, want kind %d", index, result.ParserOutcomes[index], want)
		}
		if result.ParserOutcomes[index].CompletedAt != completedAt {
			t.Fatalf("outcome[%d] completed time = %v", index, result.ParserOutcomes[index].CompletedAt)
		}
	}
	if result.ParserOutcomes[2].EmittedCount != 2 {
		t.Fatalf("successful emitted count = %d", result.ParserOutcomes[2].EmittedCount)
	}
	if len(result.PermanentFailures) != 1 ||
		result.PermanentFailures[0].Failure.Code != "malformed" ||
		result.PermanentFailures[0].Failure.SanitizedError != "record is malformed" {
		t.Fatalf("permanent failures = %+v", result.PermanentFailures)
	}
	if len(result.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(result.Events))
	}
	for index, event := range result.Events {
		wantID, err := core.SecurityEventID(planNodeID, delivery.ID, "parser-c", "v1", uint32(index))
		if err != nil {
			t.Fatal(err)
		}
		if event.ID != wantID || event.EmittedIndex != uint32(index) ||
			event.NodeID != planNodeID || event.SourceID != delivery.Record.SourceID ||
			event.ParserID != "parser-c" || event.ParserVersion != "v1" ||
			event.ObservedAt != delivery.Record.ObservedAt || event.SourcePosition != delivery.Record.Position {
			t.Fatalf("event[%d] system fields = %+v", index, event)
		}
	}
	if got := result.Rules; !reflect.DeepEqual(got, []RuleSnapshot{
		{RuleID: "rule-a", Version: "v2"}, {RuleID: "rule-b", Version: "v1"},
	}) {
		t.Fatalf("rules = %+v", got)
	}
	if catalog.ruleSnapshotCount() != 1 {
		t.Fatalf("rule snapshot calls = %d, want 1", catalog.ruleSnapshotCount())
	}
}

func TestProcessingPlanNonTerminalFailureAbortsWithoutCompletion(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class PlanFailureClass
	}{
		{name: "transient", err: &PlanFailure{Class: PlanFailureTransient, Cause: errors.New("worker unavailable")}, class: PlanFailureTransient},
		{name: "plan blocked", err: &PlanFailure{Class: PlanFailureBlocked, Cause: errors.New("revision missing")}, class: PlanFailureBlocked},
		{name: "cancelled", err: context.Canceled, class: PlanFailureCancelled},
		{name: "incomplete permanent is blocked", err: &PlanFailure{Class: PlanFailureRecordPermanent, Code: "missing-details"}, class: PlanFailureBlocked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := &mutablePlanCatalog{parsers: []ParserSnapshot{
				{ParserID: "parser-a", Version: "v1"},
				{ParserID: "parser-b", Version: "v1"},
				{ParserID: "parser-c", Version: "v1"},
			}}
			plan, err := BuildProcessingPlan(context.Background(), catalog)
			if err != nil {
				t.Fatal(err)
			}
			runner := &scriptedParserRunner{failures: map[core.ParserID]error{"parser-b": test.err}}

			result, err := plan.Run(
				context.Background(), planNodeID, testPlanDelivery(t), runner, time.Unix(200, 0).UTC())
			if err == nil || result.Complete {
				t.Fatalf("Run() = %+v,%v, want incomplete failure", result, err)
			}
			var failure *PlanFailure
			if !errors.As(err, &failure) || failure.Class != test.class {
				t.Fatalf("Run() failure = %v, want class %d", err, test.class)
			}
			if !reflect.DeepEqual(runner.calls, []core.ParserID{"parser-a", "parser-b"}) {
				t.Fatalf("parser calls = %v; later parser must not run", runner.calls)
			}
			if len(result.ParserOutcomes) != 1 || result.ParserOutcomes[0].ParserID != "parser-a" ||
				result.ParserOutcomes[0].Kind != core.ParserOutcomeNoMatch {
				t.Fatalf("non-terminal parser leaked into terminal outcomes: %+v", result.ParserOutcomes)
			}
		})
	}
}

func TestProcessingPlanSnapshotsSurviveActiveVersionSwitchAndClone(t *testing.T) {
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-a", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-a", Version: "v1"}},
	}
	first, err := BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	catalog.setParserVersion(0, "v2")
	parsers := first.Parsers()
	parsers[0].Version = "caller-mutation"
	if got := first.Parsers()[0].Version; got != "v1" {
		t.Fatalf("existing parser snapshot changed to %q", got)
	}

	rules, err := first.Rules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	catalog.setRuleVersion(0, "v2")
	rules[0].Version = "caller-mutation"
	rulesAgain, err := first.Rules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rulesAgain[0].Version != "v1" || catalog.ruleSnapshotCount() != 1 {
		t.Fatalf("frozen rules = %+v, snapshot calls = %d", rulesAgain, catalog.ruleSnapshotCount())
	}

	second, err := BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	secondRules, err := second.Rules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Parsers()[0].Version != "v2" || secondRules[0].Version != "v2" {
		t.Fatalf("rebuilt plan did not read active versions: parsers=%+v rules=%+v", second.Parsers(), secondRules)
	}
}

func TestProcessingPlanLazilyFreezesRulesOnlyAfterAnEvent(t *testing.T) {
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-a", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-a", Version: "v1"}},
	}
	plan, err := BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	delivery := testPlanDelivery(t)
	if _, err := plan.Run(context.Background(), planNodeID, delivery, &scriptedParserRunner{}, delivery.Record.ObservedAt); err != nil {
		t.Fatal(err)
	}
	if catalog.ruleSnapshotCount() != 0 {
		t.Fatalf("zero-event run loaded Rule Catalog %d times", catalog.ruleSnapshotCount())
	}

	plan, err = BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedParserRunner{events: map[core.ParserID][]core.EventFields{
		"parser-a": {{EventType: "auth.login_failed"}},
	}}
	result, err := plan.Run(context.Background(), planNodeID, delivery, runner, delivery.Record.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.ruleSnapshotCount() != 1 || len(result.Rules) != 1 {
		t.Fatalf("event-producing run snapshot calls/rules = %d/%d", catalog.ruleSnapshotCount(), len(result.Rules))
	}
}

func TestProcessingPlanIsolatesRawRecordBetweenParsers(t *testing.T) {
	delivery := testPlanDelivery(t)
	delivery.Record.Metadata = map[string]string{"format": "original"}
	catalog := &mutablePlanCatalog{parsers: []ParserSnapshot{
		{ParserID: "parser-a", Version: "v1", Priority: 10},
		{ParserID: "parser-b", Version: "v1", Priority: 20},
	}}
	plan, err := BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordIsolationRunner{}

	if _, err := plan.Run(context.Background(), planNodeID, delivery, runner, delivery.Record.ObservedAt); err != nil {
		t.Fatalf("Run(): %v", err)
	}
	if string(delivery.Record.Content) != "failed login" || delivery.Record.Metadata["format"] != "original" {
		t.Fatalf("Parser mutated caller-owned RawRecord: %+v", delivery.Record)
	}
}

type recordIsolationRunner struct{}

func (*recordIsolationRunner) RunParser(
	_ context.Context,
	parser ParserSnapshot,
	record core.RawRecord,
) (ParserExecution, error) {
	if parser.ParserID == "parser-a" {
		record.Content[0] = 'X'
		record.Metadata["format"] = "mutated"
		return ParserExecution{}, nil
	}
	if string(record.Content) != "failed login" || record.Metadata["format"] != "original" {
		return ParserExecution{}, errors.New("RawRecord leaked mutations between Parsers")
	}
	return ParserExecution{}, nil
}

func TestProcessingPlanRulesConcurrentFirstUseFreezesOnce(t *testing.T) {
	catalog := &mutablePlanCatalog{rules: []RuleSnapshot{{RuleID: "rule-a", Version: "v1"}}}
	plan, err := BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	const callers = 32
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			rules, err := plan.Rules(context.Background())
			if err == nil && (len(rules) != 1 || rules[0].Version != "v1") {
				err = errors.New("unexpected rule snapshot")
			}
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if catalog.ruleSnapshotCount() != 1 {
		t.Fatalf("concurrent rule snapshot calls = %d, want 1", catalog.ruleSnapshotCount())
	}
}

type mutablePlanCatalog struct {
	mu              sync.Mutex
	parsers         []ParserSnapshot
	rules           []RuleSnapshot
	parserErr       error
	ruleErr         error
	parserSnapshots int
	ruleSnapshots   int
}

func (c *mutablePlanCatalog) SnapshotParsers(context.Context) ([]ParserSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.parserSnapshots++
	return append([]ParserSnapshot(nil), c.parsers...), c.parserErr
}

func (c *mutablePlanCatalog) SnapshotRules(context.Context) ([]RuleSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ruleSnapshots++
	return append([]RuleSnapshot(nil), c.rules...), c.ruleErr
}

func (c *mutablePlanCatalog) setParserVersion(index int, version core.ParserVersion) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.parsers[index].Version = version
}

func (c *mutablePlanCatalog) setRuleVersion(index int, version core.RuleVersion) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules[index].Version = version
}

func (c *mutablePlanCatalog) ruleSnapshotCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ruleSnapshots
}

type scriptedParserRunner struct {
	failures map[core.ParserID]error
	events   map[core.ParserID][]core.EventFields
	calls    []core.ParserID
}

func (r *scriptedParserRunner) RunParser(
	_ context.Context,
	parser ParserSnapshot,
	_ core.RawRecord,
) (ParserExecution, error) {
	r.calls = append(r.calls, parser.ParserID)
	if err := r.failures[parser.ParserID]; err != nil {
		return ParserExecution{}, err
	}
	return ParserExecution{Events: append([]core.EventFields(nil), r.events[parser.ParserID]...)}, nil
}

func testPlanDelivery(t *testing.T) core.Delivery {
	t.Helper()
	file := core.FilePosition{
		Generation: "00112233445566778899aabbccddeeff", DeviceID: 1, Inode: 2,
		StartOffset: 10, EndOffset: 20,
	}
	position, err := core.NewFilePosition(file)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := core.FileDeliveryID("source-1", file)
	if err != nil {
		t.Fatal(err)
	}
	return core.Delivery{
		ID: deliveryID, Sequence: 1,
		Record: core.RawRecord{
			SourceID: "source-1", ObservedAt: time.Unix(100, 0).UTC(), Position: position,
			Content: []byte("failed login"),
		},
	}
}
