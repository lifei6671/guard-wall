package processor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// PlanFailureClass distinguishes a deterministic parser rejection from failures
// that must abort the whole processing attempt without producing completion.
type PlanFailureClass uint8

const (
	PlanFailureRecordPermanent PlanFailureClass = iota + 1
	PlanFailureTransient
	PlanFailureBlocked
	PlanFailureCancelled
)

// PlanFailure is a classified failure returned by a catalog or ParserRunner.
type PlanFailure struct {
	Class PlanFailureClass
	Cause error
}

func (e *PlanFailure) Error() string {
	if e == nil {
		return "processing plan failure"
	}
	if e.Cause == nil {
		return fmt.Sprintf("processing plan failure class %d", e.Class)
	}
	return fmt.Sprintf("processing plan failure class %d: %v", e.Class, e.Cause)
}

func (e *PlanFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ParserSnapshot is one immutable Active Parser revision selected at ingress.
type ParserSnapshot struct {
	ParserID string
	Version  string
	Priority int
}

// RuleSnapshot is one immutable Active Detection Rule revision.
type RuleSnapshot struct {
	RuleID  string
	Version string
}

// PlanCatalog provides atomic Active-version snapshots. Rules are intentionally
// loaded separately because they are not needed until a Parser emits an event.
type PlanCatalog interface {
	SnapshotParsers(context.Context) ([]ParserSnapshot, error)
	SnapshotRules(context.Context) ([]RuleSnapshot, error)
}

// ParserExecution contains only the fact needed by this slice to decide whether
// the lazy Rule Catalog snapshot is required.
type ParserExecution struct {
	EmittedEvents uint32
}

// ParserRunner executes one already-frozen Parser revision.
type ParserRunner interface {
	RunParser(context.Context, ParserSnapshot) (ParserExecution, error)
}

// ParserTerminalState is a terminal result for one Parser inside an attempt.
type ParserTerminalState uint8

const (
	ParserSucceeded ParserTerminalState = iota + 1
	ParserRejectedPermanent
)

// ParserTerminalOutcome records only Parser-local terminal results. Transient,
// blocked, and cancelled work never appears here as if it were terminal.
type ParserTerminalOutcome struct {
	Parser ParserSnapshot
	State  ParserTerminalState
}

// PlanRunResult is attempt-local state. Complete does not imply a committed
// receipt, SourceDurable, or Coordinator completion.
type PlanRunResult struct {
	Outcomes []ParserTerminalOutcome
	Complete bool
}

// ProcessingPlan freezes the Parser Set eagerly and the Rule Catalog lazily.
// Returned snapshots and results are cloned so callers cannot mutate the plan.
type ProcessingPlan struct {
	parsers []ParserSnapshot
	catalog PlanCatalog
	
	rulesMu     sync.Mutex
	rulesFrozen bool
	rules       []RuleSnapshot
	rulesErr    error
}

// BuildProcessingPlan freezes the current enabled Parser Set in deterministic
// priority + parser_id order.
func BuildProcessingPlan(ctx context.Context, catalog PlanCatalog) (*ProcessingPlan, error) {
	if ctx == nil || catalog == nil {
		return nil, newPlanFailure(PlanFailureBlocked, errors.New("processing plan catalog and context are required"))
	}
	parsers, err := catalog.SnapshotParsers(ctx)
	if err != nil {
		return nil, classifyPlanFailure(err, true)
	}
	parsers, err = freezeParsers(parsers)
	if err != nil {
		return nil, newPlanFailure(PlanFailureBlocked, err)
	}
	return &ProcessingPlan{parsers: parsers, catalog: catalog}, nil
}

// Parsers returns a clone of the immutable Parser Set snapshot.
func (p *ProcessingPlan) Parsers() []ParserSnapshot {
	if p == nil {
		return nil
	}
	return append([]ParserSnapshot(nil), p.parsers...)
}

// Rules freezes the current Active Rule Catalog on first use and returns clones
// of that same snapshot for the rest of this attempt.
func (p *ProcessingPlan) Rules(ctx context.Context) ([]RuleSnapshot, error) {
	if p == nil {
		return nil, newPlanFailure(PlanFailureBlocked, errors.New("processing plan is required"))
	}
	p.rulesMu.Lock()
	defer p.rulesMu.Unlock()
	if !p.rulesFrozen {
		p.rulesFrozen = true
		if ctx == nil {
			p.rulesErr = newPlanFailure(PlanFailureBlocked, errors.New("rule snapshot context is required"))
		} else {
			rules, err := p.catalog.SnapshotRules(ctx)
			if err != nil {
				p.rulesErr = classifyPlanFailure(err, true)
			} else if p.rules, err = freezeRules(rules); err != nil {
				p.rulesErr = newPlanFailure(PlanFailureBlocked, err)
			}
		}
	}
	if p.rulesErr != nil {
		return nil, p.rulesErr
	}
	return append([]RuleSnapshot(nil), p.rules...), nil
}

// Run executes the all-match Parser Set. RecordPermanent rejects only that
// Parser; every non-terminal failure aborts the attempt and leaves Complete false.
func (p *ProcessingPlan) Run(ctx context.Context, runner ParserRunner) (PlanRunResult, error) {
	if p == nil || runner == nil {
		return PlanRunResult{}, newPlanFailure(PlanFailureBlocked, errors.New("processing plan and parser runner are required"))
	}
	if ctx == nil {
		return PlanRunResult{}, newPlanFailure(PlanFailureBlocked, errors.New("processing context is required"))
	}
	result := PlanRunResult{Outcomes: make([]ParserTerminalOutcome, 0, len(p.parsers))}
	for _, parser := range p.parsers {
		if err := ctx.Err(); err != nil {
			return clonePlanResult(result), newPlanFailure(PlanFailureCancelled, err)
		}
		execution, err := runner.RunParser(ctx, parser)
		if err != nil {
			failure := classifyPlanFailure(err, false)
			if failure.Class == PlanFailureRecordPermanent {
				result.Outcomes = append(result.Outcomes, ParserTerminalOutcome{
					Parser: parser,
					State:  ParserRejectedPermanent,
				})
				continue
			}
			return clonePlanResult(result), failure
		}
		if execution.EmittedEvents > 0 {
			if _, err := p.Rules(ctx); err != nil {
				return clonePlanResult(result), err
			}
		}
		result.Outcomes = append(result.Outcomes, ParserTerminalOutcome{Parser: parser, State: ParserSucceeded})
	}
	result.Complete = true
	return clonePlanResult(result), nil
}

func freezeParsers(parsers []ParserSnapshot) ([]ParserSnapshot, error) {
	frozen := append([]ParserSnapshot(nil), parsers...)
	sort.Slice(frozen, func(left, right int) bool {
		if frozen[left].Priority != frozen[right].Priority {
			return frozen[left].Priority < frozen[right].Priority
		}
		return frozen[left].ParserID < frozen[right].ParserID
	})
	seen := make(map[string]struct{}, len(frozen))
	for _, parser := range frozen {
		if parser.ParserID == "" || parser.Version == "" {
			return nil, fmt.Errorf("parser snapshot identity is incomplete")
		}
		if _, exists := seen[parser.ParserID]; exists {
			return nil, fmt.Errorf("duplicate active parser %q", parser.ParserID)
		}
		seen[parser.ParserID] = struct{}{}
	}
	return frozen, nil
}

func freezeRules(rules []RuleSnapshot) ([]RuleSnapshot, error) {
	frozen := append([]RuleSnapshot(nil), rules...)
	sort.Slice(frozen, func(left, right int) bool {
		return frozen[left].RuleID < frozen[right].RuleID
	})
	for index, rule := range frozen {
		if rule.RuleID == "" || rule.Version == "" {
			return nil, fmt.Errorf("rule snapshot identity is incomplete")
		}
		if index > 0 && frozen[index-1].RuleID == rule.RuleID {
			return nil, fmt.Errorf("duplicate active rule %q", rule.RuleID)
		}
	}
	return frozen, nil
}

func classifyPlanFailure(err error, snapshot bool) *PlanFailure {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return newPlanFailure(PlanFailureCancelled, err)
	}
	if classified, ok := errors.AsType[*PlanFailure](err); ok {
		class := classified.Class
		if snapshot && class == PlanFailureRecordPermanent {
			class = PlanFailureBlocked
		}
		switch class {
		case PlanFailureRecordPermanent, PlanFailureTransient, PlanFailureBlocked, PlanFailureCancelled:
			return newPlanFailure(class, err)
		}
	}
	return newPlanFailure(PlanFailureTransient, err)
}

func newPlanFailure(class PlanFailureClass, cause error) *PlanFailure {
	return &PlanFailure{Class: class, Cause: cause}
}

func clonePlanResult(result PlanRunResult) PlanRunResult {
	result.Outcomes = append([]ParserTerminalOutcome(nil), result.Outcomes...)
	return result
}
