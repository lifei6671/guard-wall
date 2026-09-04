package decision

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"

	appclock "github.com/lifei6671/guard-wall/internal/clock"
	"github.com/lifei6671/guard-wall/internal/core"
)

// ErrCommitUnknown means SQLite returned an error without proving whether the
// transaction crossed its durability point. Callers must read back stable IDs.
var ErrCommitUnknown = errors.New("decision transaction commit outcome is unknown")

// CommitUnknownError preserves the physical commit error while classifying it
// separately from a transaction that is known to have rolled back.
type CommitUnknownError struct {
	Cause error
}

func (e *CommitUnknownError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrCommitUnknown.Error()
	}
	return fmt.Sprintf("%v: %v", ErrCommitUnknown, e.Cause)
}

func (e *CommitUnknownError) Unwrap() error { return ErrCommitUnknown }

// NewCommitUnknownError marks an indeterminate transaction commit.
func NewCommitUnknownError(cause error) error {
	return &CommitUnknownError{Cause: cause}
}

// LifecycleAudit is the Decision-owned Critical Audit payload for Manual and
// expiration lifecycle mutations.
type LifecycleAudit struct {
	ID             string
	IdempotencyKey string
	NodeID         core.NodeID
	Action         string
	ActorType      string
	DecisionID     core.DecisionID
	ReplacementID  *core.DecisionID
	CreatedAt      time.Time
}

// LifecycleTransaction is the narrow SQLite transaction port used by Manual
// and expiration application-service operations.
type LifecycleTransaction interface {
	DesiredStateTransaction
	RequireNodeIdentity(context.Context, core.NodeID) error
	InsertManualDecision(context.Context, core.Decision) (bool, error)
	FindDecisionByID(context.Context, core.DecisionID) (core.Decision, bool, error)
	FindActiveManualDecision(context.Context, core.NodeID, netip.Prefix) (core.Decision, bool, error)
	RevokeActiveManualDecision(context.Context, core.DecisionID, time.Time) (core.Decision, error)
	ExpireDueActiveDecisions(context.Context, core.NodeID, time.Time) ([]core.Decision, error)
	AppendDecisionLifecycleAudit(context.Context, LifecycleAudit) error
}

// TransactionRunner owns one short Decision lifecycle transaction.
type TransactionRunner interface {
	RunDecisionTransaction(context.Context, func(LifecycleTransaction) error) error
}

// LifecycleService owns one node's Manual and expiry Decision, Projection,
// normalized Intent, SnapshotRevision, retry reset, Audit, and post-commit
// Target wake. Callers never receive a transaction handle.
type LifecycleService struct {
	nodeID          core.NodeID
	runner          TransactionRunner
	finalizer       *DesiredStateFinalizer
	wake            TargetWakeSink
	expirationClock appclock.Clock
}

// NewLifecycleService constructs a Decision lifecycle application service.
func NewLifecycleService(
	nodeID core.NodeID,
	runner TransactionRunner,
	finalizer *DesiredStateFinalizer,
	wake TargetWakeSink,
) (*LifecycleService, error) {
	return NewLifecycleServiceWithClock(nodeID, runner, finalizer, wake, appclock.NewWallClock())
}

// NewLifecycleServiceWithClock constructs a Decision lifecycle application
// service using schedulerClock for expiration scheduling.
func NewLifecycleServiceWithClock(
	nodeID core.NodeID,
	runner TransactionRunner,
	finalizer *DesiredStateFinalizer,
	wake TargetWakeSink,
	schedulerClock appclock.Clock,
) (*LifecycleService, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("decision lifecycle node id is required")
	}
	if runner == nil {
		return nil, fmt.Errorf("decision transaction runner is required")
	}
	if finalizer == nil {
		return nil, fmt.Errorf("desired state finalizer is required")
	}
	if wake == nil {
		return nil, fmt.Errorf("target wake sink is required")
	}
	if schedulerClock == nil {
		return nil, fmt.Errorf("expiration scheduler clock is required")
	}
	return &LifecycleService{
		nodeID: nodeID, runner: runner, finalizer: finalizer, wake: wake, expirationClock: schedulerClock,
	}, nil
}

// BanManual creates a Manual Decision or atomically replaces the current
// active Manual Decision when replace is explicit.
func (s *LifecycleService) BanManual(
	ctx context.Context,
	request ManualRequest,
	replace bool,
) (ManualResult, error) {
	if s == nil || s.runner == nil || s.finalizer == nil || s.wake == nil {
		return ManualResult{}, fmt.Errorf("decision lifecycle service is not initialized")
	}
	if ctx == nil {
		return ManualResult{}, fmt.Errorf("manual decision context is required")
	}
	if request.NodeID != s.nodeID {
		return ManualResult{}, fmt.Errorf("manual decision node id does not match lifecycle node id")
	}

	var result ManualResult
	err := s.runner.RunDecisionTransaction(ctx, func(tx LifecycleTransaction) error {
		if err := tx.RequireNodeIdentity(ctx, s.nodeID); err != nil {
			return err
		}
		var err error
		result, err = RecordManualInTransaction(ctx, tx, request, replace)
		if err != nil {
			return err
		}
		projection, found, err := tx.FindDecisionProjection(ctx, request.NodeID, request.Target)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("manual decision projection was not materialized")
		}
		result.EnforcementChanges, err = s.finalizer.FinalizeTargets(
			ctx, tx, []core.DesiredBanProjection{projection}, request.CreatedAt,
		)
		return err
	})
	if err != nil {
		if !errors.Is(err, ErrCommitUnknown) {
			return ManualResult{}, err
		}
		return result, err
	}
	if err := WakeCommittedTargets(ctx, s.wake, result.EnforcementChanges); err != nil {
		return result, err
	}
	return result, nil
}

// ExpirationResult reports every Decision terminated by one transaction and
// the once-per-target Projections rebuilt from the remaining Active set.
type ExpirationResult struct {
	Expired            []core.Decision
	Projections        []core.DesiredBanProjection
	EnforcementChanges []TargetEnforcementChange
}

// Expire atomically terminates this service's Active Decisions due at now.
func (s *LifecycleService) Expire(ctx context.Context, now time.Time) (ExpirationResult, error) {
	return s.expire(ctx, now, true)
}

func (s *LifecycleService) expire(
	ctx context.Context,
	now time.Time,
	wake bool,
) (ExpirationResult, error) {
	if s == nil || s.runner == nil || s.finalizer == nil || s.wake == nil {
		return ExpirationResult{}, fmt.Errorf("decision lifecycle service is not initialized")
	}
	if ctx == nil {
		return ExpirationResult{}, fmt.Errorf("expiration context is required")
	}

	var result ExpirationResult
	err := s.runner.RunDecisionTransaction(ctx, func(tx LifecycleTransaction) error {
		var err error
		result, err = ExpireInTransaction(ctx, tx, s.nodeID, now)
		if err != nil {
			return err
		}
		result.EnforcementChanges, err = s.finalizer.FinalizeTargets(ctx, tx, result.Projections, now)
		return err
	})
	if err != nil {
		if !errors.Is(err, ErrCommitUnknown) {
			return ExpirationResult{}, err
		}
		return result, err
	}
	if wake {
		if err := WakeCommittedTargets(ctx, s.wake, result.EnforcementChanges); err != nil {
			return result, err
		}
	}
	return result, nil
}

// RecordManualInTransaction applies Manual duplicate/replace semantics and
// writes the Decision, Projection, and Critical Audit through one transaction.
func RecordManualInTransaction(
	ctx context.Context,
	tx LifecycleTransaction,
	request ManualRequest,
	replace bool,
) (ManualResult, error) {
	if ctx == nil || tx == nil {
		return ManualResult{}, fmt.Errorf("manual decision transaction and context are required")
	}
	candidate := core.Decision{
		ID: request.DecisionID, NodeID: request.NodeID, Source: core.DecisionSourceManual,
		CanonicalTarget: request.Target, CreatedAt: request.CreatedAt, UpdatedAt: request.CreatedAt,
		LastTriggeredAt: request.CreatedAt, ExpiresAt: cloneDecisionTime(request.ExpiresAt), State: core.DecisionActive,
	}
	if err := candidate.Validate(); err != nil {
		return ManualResult{}, fmt.Errorf("validate manual decision: %w", err)
	}

	inserted, err := tx.InsertManualDecision(ctx, candidate)
	if err != nil {
		return ManualResult{}, err
	}
	if inserted {
		_, _, err := rebuildProjection(ctx, tx, request.NodeID, request.Target, request.CreatedAt, true)
		if err != nil {
			return ManualResult{}, err
		}
		if err := tx.AppendDecisionLifecycleAudit(ctx, manualCreatedAudit(request)); err != nil {
			return ManualResult{}, err
		}
		return ManualResult{Current: cloneDecision(candidate)}, nil
	}

	current, found, err := tx.FindActiveManualDecision(ctx, request.NodeID, request.Target)
	if err != nil {
		return ManualResult{}, err
	}
	if found && !replace {
		return ManualResult{}, &AlreadyBannedError{DecisionID: current.ID}
	}
	if _, idFound, err := tx.FindDecisionByID(ctx, request.DecisionID); err != nil {
		return ManualResult{}, err
	} else if idFound {
		return ManualResult{}, fmt.Errorf("%w: %s", ErrDecisionIDConflict, request.DecisionID)
	}
	if !found {
		return ManualResult{}, fmt.Errorf("active manual decision conflict was not found")
	}
	if request.CreatedAt.Before(current.CreatedAt) {
		return ManualResult{}, fmt.Errorf("manual replacement precedes active decision")
	}

	previous, err := tx.RevokeActiveManualDecision(ctx, current.ID, request.CreatedAt)
	if err != nil {
		return ManualResult{}, err
	}
	if err := tx.AppendDecisionLifecycleAudit(ctx, manualReplacedAudit(request, previous.ID)); err != nil {
		return ManualResult{}, err
	}
	inserted, err = tx.InsertManualDecision(ctx, candidate)
	if err != nil {
		return ManualResult{}, err
	}
	if !inserted {
		return ManualResult{}, fmt.Errorf("%w: %s", ErrDecisionIDConflict, request.DecisionID)
	}
	_, _, err = rebuildProjection(ctx, tx, request.NodeID, request.Target, request.CreatedAt, true)
	if err != nil {
		return ManualResult{}, err
	}
	previous = cloneDecision(previous)
	return ManualResult{
		Previous: &previous, Current: cloneDecision(candidate), Replaced: true,
	}, nil
}

// ExpireInTransaction ends each due Active Decision for nodeID and rebuilds
// every affected target Projection exactly once.
func ExpireInTransaction(
	ctx context.Context,
	tx LifecycleTransaction,
	nodeID core.NodeID,
	now time.Time,
) (ExpirationResult, error) {
	if ctx == nil || tx == nil {
		return ExpirationResult{}, fmt.Errorf("expiration transaction and context are required")
	}
	if now.IsZero() {
		return ExpirationResult{}, fmt.Errorf("expiration time is required")
	}
	if nodeID == "" {
		return ExpirationResult{}, fmt.Errorf("expiration node id is required")
	}
	due, err := tx.ExpireDueActiveDecisions(ctx, nodeID, now)
	if err != nil {
		return ExpirationResult{}, err
	}
	sort.Slice(due, func(left, right int) bool {
		if due[left].NodeID != due[right].NodeID {
			return due[left].NodeID < due[right].NodeID
		}
		if due[left].CanonicalTarget != due[right].CanonicalTarget {
			return due[left].CanonicalTarget.String() < due[right].CanonicalTarget.String()
		}
		return due[left].ID < due[right].ID
	})
	result := ExpirationResult{Expired: make([]core.Decision, 0, len(due))}
	type targetKey struct {
		nodeID core.NodeID
		target netip.Prefix
	}
	affected := make([]targetKey, 0, len(due))
	seen := make(map[targetKey]struct{}, len(due))
	for _, expired := range due {
		if err := tx.AppendDecisionLifecycleAudit(ctx, expiredAudit(expired, now)); err != nil {
			return ExpirationResult{}, err
		}
		result.Expired = append(result.Expired, cloneDecision(expired))
		key := targetKey{nodeID: expired.NodeID, target: expired.CanonicalTarget}
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			affected = append(affected, key)
		}
	}
	result.Projections = make([]core.DesiredBanProjection, 0, len(affected))
	for _, key := range affected {
		projection, _, err := rebuildProjection(ctx, tx, key.nodeID, key.target, now, true)
		if err != nil {
			return ExpirationResult{}, err
		}
		result.Projections = append(result.Projections, projection)
	}
	return result, nil
}

func manualCreatedAudit(request ManualRequest) LifecycleAudit {
	digest := decisionIdentityDigest("decision:manual:create", string(request.DecisionID))
	hexDigest := hex.EncodeToString(digest[:])
	return LifecycleAudit{
		ID: "audit-manual-create-" + hexDigest, IdempotencyKey: "manual-create:" + hexDigest,
		NodeID: request.NodeID, Action: "manual_create", ActorType: "administrator",
		DecisionID: request.DecisionID, CreatedAt: request.CreatedAt,
	}
}

func manualReplacedAudit(request ManualRequest, previousID core.DecisionID) LifecycleAudit {
	digest := decisionIdentityDigest("decision:manual:replace", string(previousID), string(request.DecisionID))
	hexDigest := hex.EncodeToString(digest[:])
	return LifecycleAudit{
		ID: "audit-manual-replace-" + hexDigest, IdempotencyKey: "manual-replace:" + hexDigest,
		NodeID: request.NodeID, Action: "manual_replace", ActorType: "administrator",
		DecisionID: previousID, ReplacementID: &request.DecisionID, CreatedAt: request.CreatedAt,
	}
}

func expiredAudit(value core.Decision, now time.Time) LifecycleAudit {
	digest := decisionIdentityDigest("decision:expire", string(value.ID))
	hexDigest := hex.EncodeToString(digest[:])
	return LifecycleAudit{
		ID: "audit-decision-expire-" + hexDigest, IdempotencyKey: "decision-expire:" + hexDigest,
		NodeID: value.NodeID, Action: "decision_expire", ActorType: "system",
		DecisionID: value.ID, CreatedAt: now,
	}
}
