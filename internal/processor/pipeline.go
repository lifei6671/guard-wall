package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/detection"
	"github.com/lifei6671/guard-wall/internal/store"
)

// RuleMatch is the rule-owned grouping result for one SecurityEvent. An
// applicable match must provide stable group and distinct keys for the active
// Rule revision.
type RuleMatch struct {
	Applicable  bool
	GroupKey    string
	DistinctKey string
}

// DetectionEffects contains the Detection-owned durable result for one new
// Event/Rule membership. Decision lifecycle and Projection aggregation belong
// to the transaction-aware Decision service and are intentionally absent.
type DetectionEffects struct {
	Alert             *core.Alert
	AutomaticDecision *decision.AutomaticRequest
}

// RuleEvaluator first determines applicability and grouping, then evaluates
// the staged post-contribution Window snapshot. Implementations must not write
// durable state; Coordinator owns every business write and transaction edge.
type RuleEvaluator interface {
	Match(context.Context, RuleSnapshot, core.SecurityEvent) (RuleMatch, error)
	Evaluate(context.Context, RuleSnapshot, core.SecurityEvent, detection.Snapshot) (DetectionEffects, error)
}

// Pipeline turns one frozen Parser/Rule plan into a prepared Coordinator
// attempt. Parser and Detection work finish before SQLite BeginProcessing.
type Pipeline struct {
	nodeID  core.NodeID
	catalog PlanCatalog
	parsers ParserRunner
	rules   RuleEvaluator
	ledger  *detection.Ledger
	clock   func() time.Time
}

// NewPipeline constructs the Phase 1 Parser/Detection attempt runner.
func NewPipeline(
	nodeID core.NodeID,
	catalog PlanCatalog,
	parsers ParserRunner,
	rules RuleEvaluator,
	ledger *detection.Ledger,
) *Pipeline {
	return &Pipeline{
		nodeID:  nodeID,
		catalog: catalog,
		parsers: parsers,
		rules:   rules,
		ledger:  ledger,
		clock:   func() time.Time { return time.Now().UTC() },
	}
}

type matchedRule struct {
	event     core.SecurityEvent
	rule      RuleSnapshot
	candidate detection.Candidate
	order     int
}

type preparedDetection struct {
	contribution core.DetectionContribution
	outcome      core.DetectionTerminalOutcome
	effects      DetectionEffects
}

type detectionPermanentFailure struct {
	eventID core.EventID
	rule    RuleSnapshot
	failure core.PermanentFailure
	order   int
}

type pipelineAttempt struct {
	parserOutcomes    []core.ParserTerminalOutcome
	detections        []preparedDetection
	detectionOutcomes []core.DetectionTerminalOutcome
	audits            []store.CriticalAudit
	kind              core.ReceiptKind
	failure           *core.PermanentFailure
	reservation       *detection.Reservation
}

func (p *Pipeline) prepare(ctx context.Context, delivery core.Delivery) (preparedAttempt, error) {
	if p == nil || p.catalog == nil || p.parsers == nil || p.rules == nil || p.ledger == nil || p.clock == nil {
		return nil, newPlanFailure(PlanFailureBlocked, errors.New("processing pipeline dependencies are incomplete"))
	}
	if ctx == nil {
		return nil, newPlanFailure(PlanFailureBlocked, errors.New("processing context is required"))
	}
	if !validPipelineNodeID(p.nodeID) {
		return nil, newPlanFailure(PlanFailureBlocked, errors.New("processing pipeline node id is invalid"))
	}

	completedAt := p.clock()
	if completedAt.IsZero() {
		return nil, newPlanFailure(PlanFailureBlocked, errors.New("processing clock returned zero time"))
	}
	plan, err := BuildProcessingPlan(ctx, p.catalog)
	if err != nil {
		return nil, err
	}
	result, err := plan.Run(ctx, p.nodeID, delivery, p.parsers, completedAt)
	if err != nil {
		return nil, err
	}
	if !result.Complete {
		return nil, newPlanFailure(PlanFailureBlocked, errors.New("processing plan returned an incomplete result without error"))
	}

	matches, candidates, permanentFailures, err := p.matchRules(ctx, delivery, result, completedAt)
	if err != nil {
		return nil, err
	}
	attempt := &pipelineAttempt{
		parserOutcomes: append([]core.ParserTerminalOutcome(nil), result.ParserOutcomes...),
		kind:           core.ReceiptSuccess,
	}
	reservation, detections, evaluationPermanent, err := p.evaluateRules(
		ctx, delivery.ID, matches, candidates, completedAt,
	)
	if err != nil {
		return nil, err
	}
	attempt.reservation = reservation
	attempt.detections = detections
	permanentFailures = append(permanentFailures, evaluationPermanent...)
	sort.SliceStable(permanentFailures, func(left, right int) bool {
		return permanentFailures[left].order < permanentFailures[right].order
	})
	for _, current := range permanentFailures {
		attempt.detectionOutcomes = append(attempt.detectionOutcomes, core.DetectionTerminalOutcome{
			DeliveryID: delivery.ID, EventID: current.eventID, RuleID: current.rule.RuleID,
			RuleVersion: current.rule.Version, Kind: core.DetectionOutcomeRecordPermanent,
			FailureCode: current.failure.Code, CompletedAt: completedAt,
		})
		audit, err := detectionPoisonAudit(p.nodeID, delivery.ID, current)
		if err != nil {
			attempt.abort()
			return nil, newPlanFailure(PlanFailureBlocked, err)
		}
		attempt.audits = append(attempt.audits, audit)
	}

	for _, permanent := range result.PermanentFailures {
		audit, err := poisonAudit(p.nodeID, delivery.ID, permanent)
		if err != nil {
			attempt.abort()
			return nil, newPlanFailure(PlanFailureBlocked, err)
		}
		attempt.audits = append(attempt.audits, audit)
	}
	if len(result.PermanentFailures) > 0 {
		failure := result.PermanentFailures[0].Failure
		failure.Stage = "parser"
		attempt.kind = core.ReceiptRecordPermanent
		attempt.failure = &failure
	} else if len(permanentFailures) > 0 {
		failure := permanentFailures[0].failure
		failure.Stage = "detection"
		attempt.kind = core.ReceiptRecordPermanent
		attempt.failure = &failure
	}
	return attempt, nil
}

func (p *Pipeline) matchRules(
	ctx context.Context,
	delivery core.Delivery,
	result PlanRunResult,
	completedAt time.Time,
) ([]matchedRule, []detection.Candidate, []detectionPermanentFailure, error) {
	matches := make([]matchedRule, 0, len(result.Events)*len(result.Rules))
	candidates := make([]detection.Candidate, 0, cap(matches))
	permanent := make([]detectionPermanentFailure, 0)
	order := 0
	for _, event := range result.Events {
		for _, rule := range result.Rules {
			currentOrder := order
			order++
			match, err := p.rules.Match(ctx, rule, cloneSecurityEvent(event))
			if err != nil {
				failure := classifyPlanFailure(err, false)
				if failure.Class == PlanFailureRecordPermanent {
					permanent = append(permanent, detectionPermanentFailure{
						eventID: event.ID, rule: rule, order: currentOrder,
						failure: core.PermanentFailure{Code: failure.Code, SanitizedError: failure.SanitizedError,
							Action: failure.Action, OccurredAt: completedAt},
					})
					continue
				}
				return nil, nil, nil, failure
			}
			if !match.Applicable {
				continue
			}
			candidate := detection.Candidate{
				DeliveryID: delivery.ID,
				Key: detection.ContributionKey{
					EventID: event.ID, RuleID: rule.RuleID, RuleVersion: rule.Version,
				},
				Window: detection.WindowKey{
					RuleID: rule.RuleID, RuleVersion: rule.Version, GroupKey: match.GroupKey,
				},
				DistinctKey: match.DistinctKey,
				ObservedAt:  event.ObservedAt,
			}
			matches = append(matches, matchedRule{
				event: cloneSecurityEvent(event), rule: rule, candidate: candidate, order: currentOrder,
			})
			candidates = append(candidates, candidate)
		}
	}
	return matches, candidates, permanent, nil
}

func (p *Pipeline) evaluateRules(
	ctx context.Context,
	deliveryID core.DeliveryID,
	matches []matchedRule,
	candidates []detection.Candidate,
	completedAt time.Time,
) (*detection.Reservation, []preparedDetection, []detectionPermanentFailure, error) {
	excluded := make(map[detection.ContributionKey]struct{})
	permanent := make([]detectionPermanentFailure, 0)
	for {
		activeMatches := make([]matchedRule, 0, len(matches)-len(excluded))
		activeCandidates := make([]detection.Candidate, 0, len(candidates)-len(excluded))
		for index, candidate := range candidates {
			if _, removed := excluded[candidate.Key]; removed {
				continue
			}
			activeMatches = append(activeMatches, matches[index])
			activeCandidates = append(activeCandidates, candidate)
		}
		reservation, previews, err := p.ledger.PrepareBatch(ctx, deliveryID, activeCandidates)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, nil, nil, classifyPlanFailure(err, false)
			}
			return nil, nil, nil, newPlanFailure(PlanFailureBlocked, err)
		}
		prepared := make([]preparedDetection, 0, len(previews))
		retry := false
		for index, preview := range previews {
			if !preview.WillContribute {
				reservation.Abort()
				return nil, nil, nil, newPlanFailure(PlanFailureBlocked, fmt.Errorf(
					"event %q rule %q already has Window membership without a receipt",
					preview.Candidate.Key.EventID, preview.Candidate.Key.RuleID))
			}
			effects, err := p.rules.Evaluate(ctx, activeMatches[index].rule, activeMatches[index].event, preview.Snapshot)
			if err != nil {
				failure := classifyPlanFailure(err, false)
				if failure.Class != PlanFailureRecordPermanent {
					reservation.Abort()
					return nil, nil, nil, failure
				}
				excluded[preview.Candidate.Key] = struct{}{}
				permanent = append(permanent, detectionPermanentFailure{
					eventID: activeMatches[index].event.ID, rule: activeMatches[index].rule,
					order: activeMatches[index].order,
					failure: core.PermanentFailure{Code: failure.Code, SanitizedError: failure.SanitizedError,
						Action: failure.Action, OccurredAt: completedAt},
				})
				retry = true
				break
			}
			if err := validateDetectionEffects(p.nodeID, deliveryID, completedAt, activeMatches[index], effects); err != nil {
				reservation.Abort()
				return nil, nil, nil, newPlanFailure(PlanFailureBlocked, err)
			}
			prepared = append(prepared, preparedDetection{
				contribution: core.DetectionContribution{
					DeliveryID: deliveryID, EventID: activeMatches[index].event.ID,
					RuleID: activeMatches[index].rule.RuleID, RuleVersion: activeMatches[index].rule.Version,
					ContributedAt: completedAt,
				},
				outcome: core.DetectionTerminalOutcome{
					DeliveryID: deliveryID, EventID: activeMatches[index].event.ID,
					RuleID: activeMatches[index].rule.RuleID, RuleVersion: activeMatches[index].rule.Version,
					Kind: core.DetectionOutcomeSuccess, CompletedAt: completedAt,
				},
				effects: cloneDetectionEffects(effects),
			})
		}
		if retry {
			reservation.Abort()
			continue
		}
		return reservation, prepared, permanent, nil
	}
}

func (p *pipelineAttempt) write(ctx context.Context, writer outcomeWriter) error {
	for _, outcome := range p.parserOutcomes {
		if err := writer.writeParserOutcome(ctx, outcome); err != nil {
			return err
		}
	}
	for _, detectionResult := range p.detections {
		if err := writer.writeDetectionOutcome(ctx, detectionResult.outcome); err != nil {
			return err
		}
		inserted, err := writer.writeDetectionContribution(ctx, detectionResult.contribution)
		if err != nil {
			return err
		}
		if !inserted {
			return fmt.Errorf("new processing attempt did not insert detection contribution")
		}
		if effects := detectionResult.effects; effects.Alert != nil {
			if err := writer.writeAlert(ctx, *effects.Alert); err != nil {
				return err
			}
		}
		if effects := detectionResult.effects; effects.AutomaticDecision != nil {
			if err := writer.recordAutomaticDecision(ctx, *effects.AutomaticDecision); err != nil {
				return err
			}
		}
	}
	for _, outcome := range p.detectionOutcomes {
		if err := writer.writeDetectionOutcome(ctx, outcome); err != nil {
			return err
		}
	}
	for _, audit := range p.audits {
		if err := writer.writeCriticalAudit(ctx, audit); err != nil {
			return err
		}
	}
	return nil
}

func (p *pipelineAttempt) terminal() (core.ReceiptKind, *core.PermanentFailure) {
	return p.kind, clonePermanentFailure(p.failure)
}

func (p *pipelineAttempt) confirm() { p.reservation.Confirm() }
func (p *pipelineAttempt) abort()   { p.reservation.Abort() }
func (p *pipelineAttempt) deferResolution() {
	p.reservation.DeferResolution()
}

func (p *Pipeline) resolvePending(deliveryID core.DeliveryID, committed bool) {
	p.ledger.ResolveDeferred(deliveryID, committed)
}

func (p *Pipeline) acquireDelivery(ctx context.Context, deliveryID core.DeliveryID) (func(), error) {
	if p == nil || p.ledger == nil {
		return nil, newPlanFailure(PlanFailureBlocked, errors.New("processing pipeline Ledger is required"))
	}
	return p.ledger.AcquireDelivery(ctx, deliveryID)
}

func validateDetectionEffects(
	nodeID core.NodeID,
	deliveryID core.DeliveryID,
	completedAt time.Time,
	match matchedRule,
	effects DetectionEffects,
) error {
	if effects.Alert != nil {
		if err := effects.Alert.Validate(); err != nil {
			return fmt.Errorf("validate detection alert: %w", err)
		}
		if effects.Alert.NodeID != nodeID || effects.Alert.EventID != match.event.ID ||
			effects.Alert.RuleID != match.rule.RuleID || effects.Alert.RuleVersion != match.rule.Version ||
			effects.Alert.CreatedAt != completedAt {
			return fmt.Errorf("detection alert does not bind the evaluated Event and Rule revision")
		}
	}
	if effects.AutomaticDecision != nil {
		if effects.Alert == nil {
			return fmt.Errorf("automatic decision request requires an alert")
		}
		request := effects.AutomaticDecision
		if request.RuleVersion == nil || request.AlertID == nil ||
			request.DeliveryID != deliveryID || request.EventID != match.event.ID ||
			request.NodeID != nodeID || request.RuleID != match.rule.RuleID ||
			*request.RuleVersion != match.rule.Version || *request.AlertID != effects.Alert.ID ||
			request.Target != effects.Alert.CanonicalTarget || request.TriggeredAt != effects.Alert.CreatedAt {
			return fmt.Errorf("automatic decision request does not bind the evaluated Alert, Event, and Rule revision")
		}
	}
	return nil
}

func detectionPoisonAudit(
	nodeID core.NodeID,
	deliveryID core.DeliveryID,
	permanent detectionPermanentFailure,
) (store.CriticalAudit, error) {
	details, err := json.Marshal(struct {
		EventID     core.EventID     `json:"event_id"`
		RuleID      core.RuleID      `json:"rule_id"`
		RuleVersion core.RuleVersion `json:"rule_version"`
		Action      string           `json:"action"`
	}{permanent.eventID, permanent.rule.RuleID, permanent.rule.Version, permanent.failure.Action})
	if err != nil {
		return store.CriticalAudit{}, fmt.Errorf("marshal detection poison audit details: %w", err)
	}
	identity, err := json.Marshal([]string{
		"poison:detection", string(deliveryID), string(permanent.eventID),
		string(permanent.rule.RuleID), string(permanent.rule.Version),
	})
	if err != nil {
		return store.CriticalAudit{}, fmt.Errorf("marshal detection poison audit identity: %w", err)
	}
	digest := sha256.Sum256(identity)
	hexDigest := hex.EncodeToString(digest[:])
	return store.CriticalAudit{
		ID: "audit-poison-" + hexDigest, IdempotencyKey: "poison:" + hexDigest,
		NodeID: nodeID, Category: "processing", Action: "record_permanent", Result: "rejected",
		Severity: "warning", ActorType: "source", DeliveryID: &deliveryID,
		ErrorCode: permanent.failure.Code, DetailsJSON: details, CreatedAt: permanent.failure.OccurredAt,
	}, nil
}

func poisonAudit(
	nodeID core.NodeID,
	deliveryID core.DeliveryID,
	permanent ParserPermanentFailure,
) (store.CriticalAudit, error) {
	details, err := json.Marshal(struct {
		ParserID      core.ParserID      `json:"parser_id"`
		ParserVersion core.ParserVersion `json:"parser_version"`
		Action        string             `json:"action"`
	}{permanent.Parser.ParserID, permanent.Parser.Version, permanent.Failure.Action})
	if err != nil {
		return store.CriticalAudit{}, fmt.Errorf("marshal poison audit details: %w", err)
	}
	identity, err := json.Marshal([]string{
		"poison:parser", string(deliveryID), string(permanent.Parser.ParserID), string(permanent.Parser.Version),
	})
	if err != nil {
		return store.CriticalAudit{}, fmt.Errorf("marshal parser poison audit identity: %w", err)
	}
	digest := sha256.Sum256(identity)
	key := "poison:" + hex.EncodeToString(digest[:])
	return store.CriticalAudit{
		ID:             "audit-poison-" + hex.EncodeToString(digest[:]),
		IdempotencyKey: key,
		NodeID:         nodeID,
		Category:       "processing",
		Action:         "record_permanent",
		Result:         "rejected",
		Severity:       "warning",
		ActorType:      "source",
		DeliveryID:     &deliveryID,
		ErrorCode:      permanent.Failure.Code,
		DetailsJSON:    details,
		CreatedAt:      permanent.Failure.OccurredAt,
	}, nil
}

func cloneDetectionEffects(effects DetectionEffects) DetectionEffects {
	if effects.Alert != nil {
		alert := *effects.Alert
		effects.Alert = &alert
	}
	if effects.AutomaticDecision != nil {
		request := *effects.AutomaticDecision
		request.RuleVersion = cloneRuleVersion(request.RuleVersion)
		request.AlertID = cloneAlertID(request.AlertID)
		request.ExpiresAt = cloneDecisionTime(request.ExpiresAt)
		effects.AutomaticDecision = &request
	}
	return effects
}

func cloneRuleVersion(value *core.RuleVersion) *core.RuleVersion {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneAlertID(value *core.AlertID) *core.AlertID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneDecisionTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneSecurityEvent(event core.SecurityEvent) core.SecurityEvent {
	if event.Timestamp != nil {
		value := *event.Timestamp
		event.Timestamp = &value
	}
	if event.User != nil {
		value := *event.User
		event.User = &value
	}
	if event.HTTP != nil {
		value := *event.HTTP
		event.HTTP = &value
	}
	if event.Labels != nil {
		labels := make(map[string]string, len(event.Labels))
		for key, value := range event.Labels {
			labels[key] = value
		}
		event.Labels = labels
	}
	if event.Fields != nil {
		fields := make(map[string]any, len(event.Fields))
		for key, value := range event.Fields {
			fields[key] = value
		}
		event.Fields = fields
	}
	return event
}

func validPipelineNodeID(value core.NodeID) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return utf8.ValidString(string(value))
}
