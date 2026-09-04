package decision

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/enforcement"
)

// ErrPostCommitWake means authoritative Desired state committed but at least
// one affected Target could not be queued for reconciliation.
var ErrPostCommitWake = errors.New("post-commit target wake failed")

// DesiredStateTransaction is the Decision-owned persistence port for the
// normalized Target intents and the global Desired snapshot revision.
type DesiredStateTransaction interface {
	ProjectionTransaction
	FindTargetEnforcementIntent(context.Context, core.NodeID, netip.Prefix) (core.NormalizedTargetEnforcementIntent, bool, error)
	TargetEnforcementGenerationFloor(context.Context, core.NodeID, netip.Prefix) (core.TargetEnforcementGeneration, bool, error)
	PutTargetEnforcementIntent(context.Context, core.NormalizedTargetEnforcementIntent) error
	ResetTargetReconcileState(context.Context, core.NodeID, netip.Prefix, core.TargetEnforcementGeneration, time.Time) error
	AdvanceSnapshotRevision(context.Context) (core.SnapshotRevision, error)
}

// PolicyDesiredStateTransaction adds the complete Projection view needed when
// an authoritative Policy change must re-materialize every stored Target.
type PolicyDesiredStateTransaction interface {
	DesiredStateTransaction
	ListDecisionProjections(context.Context, core.NodeID) ([]core.DesiredBanProjection, error)
}

// TargetPolicyResolver returns Firewall-significant inputs from the same
// transaction snapshot, or from immutable process configuration.
type TargetPolicyResolver interface {
	ResolveTargetPolicy(context.Context, DesiredStateTransaction, core.DesiredBanProjection) (enforcement.TargetPolicy, error)
}

// TargetPolicyResolverFunc adapts a function to TargetPolicyResolver.
type TargetPolicyResolverFunc func(context.Context, DesiredStateTransaction, core.DesiredBanProjection) (enforcement.TargetPolicy, error)

func (f TargetPolicyResolverFunc) ResolveTargetPolicy(
	ctx context.Context,
	tx DesiredStateTransaction,
	projection core.DesiredBanProjection,
) (enforcement.TargetPolicy, error) {
	if f == nil {
		return enforcement.TargetPolicy{}, fmt.Errorf("target policy resolver function is required")
	}
	return f(ctx, tx, projection)
}

// DesiredStateFinalizer materializes final Projection meaning once per Target
// immediately before the owning transaction commits.
type DesiredStateFinalizer struct {
	policies TargetPolicyResolver
}

// NewDesiredStateFinalizer constructs the transaction-local finalizer.
func NewDesiredStateFinalizer(policies TargetPolicyResolver) (*DesiredStateFinalizer, error) {
	if policies == nil {
		return nil, fmt.Errorf("target policy resolver is required")
	}
	return &DesiredStateFinalizer{policies: policies}, nil
}

// TargetEnforcementChange identifies one committed Target generation and the
// global SnapshotRevision assigned to its transaction.
type TargetEnforcementChange struct {
	NodeID           core.NodeID
	Target           netip.Prefix
	Generation       core.TargetEnforcementGeneration
	SnapshotRevision core.SnapshotRevision
}

// FinalizeTargets compares the transaction-final Projection for each Target,
// persists only semantic changes, resets only changed Target retry state, and
// advances the global SnapshotRevision once when any Target changed.
func (f *DesiredStateFinalizer) FinalizeTargets(
	ctx context.Context,
	tx DesiredStateTransaction,
	projections []core.DesiredBanProjection,
	updatedAt time.Time,
) ([]TargetEnforcementChange, error) {
	changes, err := f.MaterializeTargets(ctx, tx, projections, updatedAt)
	if err != nil || len(changes) == 0 {
		return changes, err
	}
	revision, err := tx.AdvanceSnapshotRevision(ctx)
	if err != nil {
		return nil, err
	}
	for index := range changes {
		changes[index].SnapshotRevision = revision
	}
	return changes, nil
}

// MaterializeTargets compares final Projections and persists only semantic
// Target changes. The caller owns SnapshotRevision advancement so a Policy
// replacement can commit Policy and Target changes behind one snapshot fence.
func (f *DesiredStateFinalizer) MaterializeTargets(
	ctx context.Context,
	tx DesiredStateTransaction,
	projections []core.DesiredBanProjection,
	updatedAt time.Time,
) ([]TargetEnforcementChange, error) {
	if f == nil || f.policies == nil {
		return nil, fmt.Errorf("desired state finalizer is not initialized")
	}
	if ctx == nil || tx == nil {
		return nil, fmt.Errorf("desired state transaction and context are required")
	}
	if updatedAt.IsZero() {
		return nil, fmt.Errorf("desired state update time is required")
	}

	type targetKey struct {
		nodeID core.NodeID
		target netip.Prefix
	}
	final := make(map[targetKey]core.DesiredBanProjection, len(projections))
	for _, projection := range projections {
		if err := projection.Validate(); err != nil {
			return nil, fmt.Errorf("validate affected projection: %w", err)
		}
		final[targetKey{nodeID: projection.NodeID, target: projection.CanonicalTarget}] = projection
	}
	keys := make([]targetKey, 0, len(final))
	for key := range final {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].nodeID != keys[right].nodeID {
			return keys[left].nodeID < keys[right].nodeID
		}
		return keys[left].target.String() < keys[right].target.String()
	})

	changes := make([]TargetEnforcementChange, 0, len(keys))
	for _, key := range keys {
		projection, found, err := tx.FindDecisionProjection(ctx, key.nodeID, key.target)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("affected projection for %s was not materialized", key.target)
		}
		previous, found, err := tx.FindTargetEnforcementIntent(ctx, key.nodeID, key.target)
		if err != nil {
			return nil, err
		}
		policy, err := f.policies.ResolveTargetPolicy(ctx, tx, projection)
		if err != nil {
			return nil, fmt.Errorf("resolve target policy for %s: %w", key.target, err)
		}
		var previousPointer *core.NormalizedTargetEnforcementIntent
		if found {
			previousPointer = &previous
		}
		intent, changed, err := enforcement.ResolveTarget(projection, policy, previousPointer)
		if err != nil {
			return nil, fmt.Errorf("resolve target intent for %s: %w", key.target, err)
		}
		if !changed {
			continue
		}
		if !found {
			floor, floorFound, err := tx.TargetEnforcementGenerationFloor(ctx, key.nodeID, key.target)
			if err != nil {
				return nil, err
			}
			if floorFound {
				if uint64(floor) >= math.MaxInt64 {
					return nil, fmt.Errorf("target generation is exhausted for %s", key.target)
				}
				intent.Generation = floor + 1
			}
		}
		if uint64(intent.Generation) > math.MaxInt64 {
			return nil, fmt.Errorf("target generation is exhausted for %s", key.target)
		}
		if err := tx.PutTargetEnforcementIntent(ctx, intent); err != nil {
			return nil, err
		}
		if err := tx.ResetTargetReconcileState(ctx, key.nodeID, key.target, intent.Generation, updatedAt); err != nil {
			return nil, err
		}
		changes = append(changes, TargetEnforcementChange{
			NodeID: key.nodeID, Target: key.target, Generation: intent.Generation,
		})
	}
	return changes, nil
}

// MaterializeNodeTargets re-materializes every stored Projection for one
// node without advancing SnapshotRevision. It is reserved for a Policy
// transaction, which advances the shared snapshot exactly once after Policy,
// retry, audit, and Target writes all succeed.
func (f *DesiredStateFinalizer) MaterializeNodeTargets(
	ctx context.Context,
	tx PolicyDesiredStateTransaction,
	nodeID core.NodeID,
	updatedAt time.Time,
) ([]TargetEnforcementChange, error) {
	if tx == nil {
		return nil, fmt.Errorf("policy desired state transaction is required")
	}
	projections, err := tx.ListDecisionProjections(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list policy projections: %w", err)
	}
	return f.MaterializeTargets(ctx, tx, projections, updatedAt)
}

// TargetWakeSink queues one committed Target for reconciliation.
type TargetWakeSink interface {
	WakeTarget(context.Context, core.NodeID, netip.Prefix) error
}

// TargetWakeSinkFunc adapts a function to TargetWakeSink.
type TargetWakeSinkFunc func(context.Context, core.NodeID, netip.Prefix) error

func (f TargetWakeSinkFunc) WakeTarget(ctx context.Context, nodeID core.NodeID, target netip.Prefix) error {
	if f == nil {
		return fmt.Errorf("target wake sink function is required")
	}
	return f(ctx, nodeID, target)
}

// PostCommitWakeError proves the Decision transaction is durable. Callers
// must not replay the mutation blindly; Pending contains the not-yet-attempted
// suffix after Failed.
type PostCommitWakeError struct {
	Failed  TargetEnforcementChange
	Pending []TargetEnforcementChange
	Cause   error
}

func (e *PostCommitWakeError) Error() string {
	if e == nil {
		return ErrPostCommitWake.Error()
	}
	return fmt.Sprintf("%v for %s: %v", ErrPostCommitWake, e.Failed.Target, e.Cause)
}

func (e *PostCommitWakeError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrPostCommitWake
	}
	return errors.Join(ErrPostCommitWake, e.Cause)
}

// WakeCommittedTargets queues a stable, de-duplicated committed change set.
func WakeCommittedTargets(ctx context.Context, sink TargetWakeSink, changes []TargetEnforcementChange) error {
	if len(changes) == 0 {
		return nil
	}
	if ctx == nil || sink == nil {
		return fmt.Errorf("wake committed targets: context and sink are required")
	}
	for index, change := range changes {
		if err := sink.WakeTarget(ctx, change.NodeID, change.Target); err != nil {
			pending := append([]TargetEnforcementChange(nil), changes[index+1:]...)
			return &PostCommitWakeError{Failed: change, Pending: pending, Cause: err}
		}
	}
	return nil
}
