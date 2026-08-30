package processor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lifei6671/guard-wall/internal/core"
)

// PlanFailureClass distinguishes a deterministic record rejection from failures
// that must abort the whole processing attempt without producing completion.
type PlanFailureClass uint8

const (
	PlanFailureRecordPermanent PlanFailureClass = iota + 1
	PlanFailureTransient
	PlanFailureBlocked
	PlanFailureCancelled
)

// PlanFailure is a classified failure returned by a catalog or ParserRunner.
// A RecordPermanent failure must carry the stable, sanitized diagnostic that is
// safe to persist in the terminal receipt and Critical Audit.
type PlanFailure struct {
	Class          PlanFailureClass
	Code           string
	SanitizedError string
	Action         string
	Cause          error
}

func (e *PlanFailure) Error() string {
	if e == nil {
		return "processing plan failure"
	}
	if e.Class == PlanFailureRecordPermanent && e.SanitizedError != "" {
		return fmt.Sprintf("processing plan failure class %d: %s", e.Class, e.SanitizedError)
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
	ParserID core.ParserID
	Version  core.ParserVersion
	Priority int
}

// RuleSnapshot is one immutable Active Detection Rule revision.
type RuleSnapshot struct {
	RuleID  core.RuleID
	Version core.RuleVersion
}

// PlanCatalog provides atomic Active-version snapshots. Rules are intentionally
// loaded separately because they are not needed until a Parser emits an event.
type PlanCatalog interface {
	SnapshotParsers(context.Context) ([]ParserSnapshot, error)
	SnapshotRules(context.Context) ([]RuleSnapshot, error)
}

// ParserExecution contains only Parser-owned fields. The plan constructs every
// system-owned SecurityEvent field from the frozen snapshot and Delivery.
type ParserExecution struct {
	Events []core.EventFields
}

// ParserRunner executes one already-frozen Parser revision against a RawRecord.
type ParserRunner interface {
	RunParser(context.Context, ParserSnapshot, core.RawRecord) (ParserExecution, error)
}

// ParserPermanentFailure binds one durable poison diagnostic to the Parser
// revision that rejected the record.
type ParserPermanentFailure struct {
	Parser  ParserSnapshot
	Failure core.PermanentFailure
}

// PlanRunResult is attempt-local state. Complete does not imply a committed
// receipt, SourceDurable, or Coordinator completion.
type PlanRunResult struct {
	ParserOutcomes    []core.ParserTerminalOutcome
	Events            []core.SecurityEvent
	Rules             []RuleSnapshot
	PermanentFailures []ParserPermanentFailure
	Complete          bool
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
func (p *ProcessingPlan) Run(
	ctx context.Context,
	nodeID core.NodeID,
	delivery core.Delivery,
	runner ParserRunner,
	completedAt time.Time,
) (PlanRunResult, error) {
	if p == nil || runner == nil {
		return PlanRunResult{}, newPlanFailure(PlanFailureBlocked, errors.New("processing plan and parser runner are required"))
	}
	if ctx == nil {
		return PlanRunResult{}, newPlanFailure(PlanFailureBlocked, errors.New("processing context is required"))
	}
	if err := delivery.Validate(); err != nil {
		return PlanRunResult{}, newPlanFailure(PlanFailureBlocked, fmt.Errorf("validate delivery: %w", err))
	}
	if completedAt.IsZero() {
		return PlanRunResult{}, newPlanFailure(PlanFailureBlocked, errors.New("parser completion time is required"))
	}

	result := PlanRunResult{
		ParserOutcomes: make([]core.ParserTerminalOutcome, 0, len(p.parsers)),
	}
	for _, parser := range p.parsers {
		if err := ctx.Err(); err != nil {
			return clonePlanResult(result), newPlanFailure(PlanFailureCancelled, err)
		}
		execution, err := runner.RunParser(ctx, parser, cloneRawRecord(delivery.Record))
		if err != nil {
			failure := classifyPlanFailure(err, false)
			if failure.Class == PlanFailureRecordPermanent {
				result.ParserOutcomes = append(result.ParserOutcomes, core.ParserTerminalOutcome{
					DeliveryID: delivery.ID, ParserID: parser.ParserID, ParserVersion: parser.Version,
					Kind: core.ParserOutcomeRecordPermanent, FailureCode: failure.Code,
					CompletedAt: completedAt,
				})
				result.PermanentFailures = append(result.PermanentFailures, ParserPermanentFailure{
					Parser: parser,
					Failure: core.PermanentFailure{
						Stage: "parser", Code: failure.Code, SanitizedError: failure.SanitizedError,
						Action: failure.Action, OccurredAt: completedAt,
					},
				})
				continue
			}
			return clonePlanResult(result), failure
		}

		if len(execution.Events) == 0 {
			result.ParserOutcomes = append(result.ParserOutcomes, core.ParserTerminalOutcome{
				DeliveryID: delivery.ID, ParserID: parser.ParserID, ParserVersion: parser.Version,
				Kind: core.ParserOutcomeNoMatch, CompletedAt: completedAt,
			})
			continue
		}
		if uint64(len(execution.Events)) > math.MaxUint32 {
			return clonePlanResult(result), newPlanFailure(
				PlanFailureBlocked, errors.New("parser emitted more events than the stable index supports"))
		}

		parserEvents := make([]core.SecurityEvent, 0, len(execution.Events))
		for index, fields := range execution.Events {
			event, err := core.NewSecurityEvent(
				nodeID, delivery, parser.ParserID, parser.Version, uint32(index), fields)
			if err != nil {
				return clonePlanResult(result), newPlanFailure(
					PlanFailureBlocked, fmt.Errorf("parser %q emitted invalid event: %w", parser.ParserID, err))
			}
			parserEvents = append(parserEvents, event)
		}
		rules, err := p.Rules(ctx)
		if err != nil {
			return clonePlanResult(result), err
		}
		result.Rules = rules
		result.Events = append(result.Events, parserEvents...)
		result.ParserOutcomes = append(result.ParserOutcomes, core.ParserTerminalOutcome{
			DeliveryID: delivery.ID, ParserID: parser.ParserID, ParserVersion: parser.Version,
			Kind: core.ParserOutcomeSuccess, EmittedCount: uint32(len(parserEvents)),
			CompletedAt: completedAt,
		})
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
	seen := make(map[core.ParserID]struct{}, len(frozen))
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
		case PlanFailureRecordPermanent:
			if !validPermanentDiagnostic(classified.Code, classified.SanitizedError, classified.Action) {
				return newPlanFailure(PlanFailureBlocked, errors.New("record-permanent failure diagnostic is incomplete"))
			}
			return &PlanFailure{
				Class: class, Code: classified.Code, SanitizedError: classified.SanitizedError,
				Action: classified.Action, Cause: err,
			}
		case PlanFailureTransient, PlanFailureBlocked, PlanFailureCancelled:
			return &PlanFailure{Class: class, Cause: err}
		}
	}
	return newPlanFailure(PlanFailureTransient, err)
}

func newPlanFailure(class PlanFailureClass, cause error) *PlanFailure {
	return &PlanFailure{Class: class, Cause: cause}
}

func validPermanentDiagnostic(code, sanitizedError, action string) bool {
	return len(code) >= 1 && len(code) <= 128 && utf8.ValidString(code) &&
		len(sanitizedError) >= 1 && len(sanitizedError) <= 2048 && utf8.ValidString(sanitizedError) &&
		len(action) >= 1 && len(action) <= 64 && utf8.ValidString(action)
}

func cloneRawRecord(record core.RawRecord) core.RawRecord {
	record.Content = append([]byte(nil), record.Content...)
	if record.Metadata != nil {
		metadata := make(map[string]string, len(record.Metadata))
		for key, value := range record.Metadata {
			metadata[key] = value
		}
		record.Metadata = metadata
	}
	return record
}

func clonePlanResult(result PlanRunResult) PlanRunResult {
	result.ParserOutcomes = append([]core.ParserTerminalOutcome(nil), result.ParserOutcomes...)
	result.Events = append([]core.SecurityEvent(nil), result.Events...)
	result.Rules = append([]RuleSnapshot(nil), result.Rules...)
	result.PermanentFailures = append([]ParserPermanentFailure(nil), result.PermanentFailures...)
	return result
}
