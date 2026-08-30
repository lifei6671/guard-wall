package processor

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestProcessingPlanRecordPermanentRejectsOnlyOneParser(t *testing.T) {
	catalog := &mutablePlanCatalog{parsers: []ParserSnapshot{
		{ParserID: "parser-c", Version: "v1", Priority: 20},
		{ParserID: "parser-b", Version: "v1", Priority: 10},
		{ParserID: "parser-a", Version: "v1", Priority: 10},
	}}
	plan, err := BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedParserRunner{failures: map[string]error{
		"parser-b": &PlanFailure{Class: PlanFailureRecordPermanent, Cause: errors.New("deterministic parse error")},
	}}

	result, err := plan.Run(context.Background(), runner)
	if err != nil {
		t.Fatalf("Run(): %v", err)
	}
	if !result.Complete {
		t.Fatal("all terminal parser outcomes did not complete the plan")
	}
	if !reflect.DeepEqual(runner.calls, []string{"parser-a", "parser-b", "parser-c"}) {
		t.Fatalf("parser calls = %v", runner.calls)
	}
	wantStates := []ParserTerminalState{ParserSucceeded, ParserRejectedPermanent, ParserSucceeded}
	for index, want := range wantStates {
		if result.Outcomes[index].State != want {
			t.Fatalf("outcome[%d] = %+v, want state %d", index, result.Outcomes[index], want)
		}
	}
}

func TestProcessingPlanNonTerminalFailureAbortsWithoutCompletion(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class PlanFailureClass
	}{
		{name: "transient", err: &PlanFailure{Class: PlanFailureTransient, Cause: errors.New("database busy")}, class: PlanFailureTransient},
		{name: "plan blocked", err: &PlanFailure{Class: PlanFailureBlocked, Cause: errors.New("revision missing")}, class: PlanFailureBlocked},
		{name: "cancelled", err: context.Canceled, class: PlanFailureCancelled},
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
			runner := &scriptedParserRunner{failures: map[string]error{"parser-b": test.err}}

			result, err := plan.Run(context.Background(), runner)
			if err == nil || result.Complete {
				t.Fatalf("Run() = %+v,%v, want incomplete failure", result, err)
			}
			var failure *PlanFailure
			if !errors.As(err, &failure) || failure.Class != test.class {
				t.Fatalf("Run() failure = %v, want class %d", err, test.class)
			}
			if !reflect.DeepEqual(runner.calls, []string{"parser-a", "parser-b"}) {
				t.Fatalf("parser calls = %v; later parser must not run", runner.calls)
			}
			if len(result.Outcomes) != 1 || result.Outcomes[0].Parser.ParserID != "parser-a" {
				t.Fatalf("non-terminal parser leaked into terminal outcomes: %+v", result.Outcomes)
			}
		})
	}
}

func TestProcessingPlanSnapshotsSurviveActiveVersionSwitchAndRebuild(t *testing.T) {
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-a", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-a", Version: "v1"}},
	}
	first, err := BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	catalog.parsers[0].Version = "v2"
	if got := first.Parsers()[0].Version; got != "v1" {
		t.Fatalf("existing parser snapshot changed to %q", got)
	}

	rules, err := first.Rules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rules[0].Version != "v1" {
		t.Fatalf("first lazy rule snapshot version = %q", rules[0].Version)
	}
	catalog.rules[0].Version = "v2"
	rules[0].Version = "caller-mutation"
	rulesAgain, err := first.Rules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rulesAgain[0].Version != "v1" || catalog.ruleSnapshots != 1 {
		t.Fatalf("frozen rules = %+v, snapshot calls = %d", rulesAgain, catalog.ruleSnapshots)
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
	if _, err := plan.Run(context.Background(), &scriptedParserRunner{}); err != nil {
		t.Fatal(err)
	}
	if catalog.ruleSnapshots != 0 {
		t.Fatalf("zero-event run loaded Rule Catalog %d times", catalog.ruleSnapshots)
	}

	plan, err = BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedParserRunner{events: map[string]uint32{"parser-a": 1}}
	if _, err := plan.Run(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	if catalog.ruleSnapshots != 1 {
		t.Fatalf("event-producing run loaded Rule Catalog %d times", catalog.ruleSnapshots)
	}
}

func TestProcessingPlanReturnsImmutableClones(t *testing.T) {
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-a", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-a", Version: "v1"}},
	}
	plan, err := BuildProcessingPlan(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	parsers := plan.Parsers()
	parsers[0].Version = "mutated"
	if plan.Parsers()[0].Version != "v1" {
		t.Fatal("caller mutated Parser Set snapshot")
	}
	runner := &scriptedParserRunner{}
	result, err := plan.Run(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	result.Outcomes[0].Parser.Version = "mutated"
	second, err := plan.Run(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcomes[0].Parser.Version != "v1" {
		t.Fatal("caller mutated plan outcome state")
	}
}

type mutablePlanCatalog struct {
	parsers         []ParserSnapshot
	rules           []RuleSnapshot
	parserErr       error
	ruleErr         error
	parserSnapshots int
	ruleSnapshots   int
}

func (c *mutablePlanCatalog) SnapshotParsers(context.Context) ([]ParserSnapshot, error) {
	c.parserSnapshots++
	return c.parsers, c.parserErr
}

func (c *mutablePlanCatalog) SnapshotRules(context.Context) ([]RuleSnapshot, error) {
	c.ruleSnapshots++
	return c.rules, c.ruleErr
}

type scriptedParserRunner struct {
	failures map[string]error
	events   map[string]uint32
	calls    []string
}

func (r *scriptedParserRunner) RunParser(_ context.Context, parser ParserSnapshot) (ParserExecution, error) {
	r.calls = append(r.calls, parser.ParserID)
	if err := r.failures[parser.ParserID]; err != nil {
		return ParserExecution{}, err
	}
	return ParserExecution{EmittedEvents: r.events[parser.ParserID]}, nil
}
