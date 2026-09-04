package decision

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

// ErrPostCommitPolicyWake means the authoritative Policy transaction is
// durable, but the Policy reconciliation key could not be queued.
var ErrPostCommitPolicyWake = errors.New("post-commit policy wake failed")

// PolicyWriteRequest is one compare-and-swap authoritative replacement of the
// complete managed Policy payload. ExpectedPolicyRevision zero is reserved for
// the first write to a node with no persisted Policy rows.
type PolicyWriteRequest struct {
	NodeID                 core.NodeID
	ExpectedPolicyRevision core.PolicyRevision
	Policy                 core.ManagedPolicyIntent
	AuditID                string
	AuditIdempotencyKey    string
	ActorType              string
	UpdatedAt              time.Time
}

// PolicyWriteAudit is the transaction-owned Critical Audit payload persisted
// only when a Policy semantic change is committed.
type PolicyWriteAudit struct {
	ID             string
	IdempotencyKey string
	NodeID         core.NodeID
	ActorType      string
	PolicyRevision core.PolicyRevision
	RelationDigest string
	CreatedAt      time.Time
}

// PolicyChange identifies the committed Policy revision and shared Desired
// snapshot fence. TargetChanges lists only Target intents with new generation.
type PolicyChange struct {
	NodeID           core.NodeID
	PolicyRevision   core.PolicyRevision
	SnapshotRevision core.SnapshotRevision
	TargetChanges    []TargetEnforcementChange
	Changed          bool
}

// PolicyTransaction is the narrow persistence port for an authoritative
// Policy replacement and its dependent Target re-materialization.
type PolicyTransaction interface {
	PolicyDesiredStateTransaction
	ReplaceManagedPolicy(context.Context, core.NodeID, core.PolicyRevision, core.ManagedPolicyIntent, time.Time) (core.PolicyRevision, core.SnapshotRevision, bool, error)
	ResetPolicyReconcileState(context.Context, core.PolicyRevision, time.Time) error
	AppendPolicyWriteAudit(context.Context, PolicyWriteAudit) error
}

// PolicyTransactionRunner owns one short authoritative Policy transaction.
type PolicyTransactionRunner interface {
	RunPolicyTransaction(context.Context, func(PolicyTransaction) error) error
}

// PolicyStateReader proves a commit-unknown Policy transaction before any
// post-commit wake may be sent.
type PolicyStateReader interface {
	LoadDesiredFirewallState(context.Context, core.NodeID) (core.DesiredFirewallState, error)
}

// PolicyWakeSink queues the Policy domain for one node after commit.
type PolicyWakeSink interface {
	WakePolicy(context.Context, core.NodeID) error
}

// PolicyWakeSinkFunc adapts a function to PolicyWakeSink.
type PolicyWakeSinkFunc func(context.Context, core.NodeID) error

func (f PolicyWakeSinkFunc) WakePolicy(ctx context.Context, nodeID core.NodeID) error {
	if f == nil {
		return fmt.Errorf("policy wake sink function is required")
	}
	return f(ctx, nodeID)
}

// PostCommitPolicyWakeError proves the Policy write committed. Callers must
// not replay the write; the durable pending retry state remains recoverable.
type PostCommitPolicyWakeError struct {
	Change PolicyChange
	Cause  error
}

func (e *PostCommitPolicyWakeError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrPostCommitPolicyWake.Error()
	}
	return fmt.Sprintf("%v for revision %d: %v", ErrPostCommitPolicyWake, e.Change.PolicyRevision, e.Cause)
}

func (e *PostCommitPolicyWakeError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrPostCommitPolicyWake
	}
	return errors.Join(ErrPostCommitPolicyWake, e.Cause)
}

// PolicyService owns the atomic Policy, dependent Target, retry, audit, and
// post-commit wake protocol. It never returns a transaction handle.
type PolicyService struct {
	runner     PolicyTransactionRunner
	reader     PolicyStateReader
	finalizer  *DesiredStateFinalizer
	policyWake PolicyWakeSink
	targetWake TargetWakeSink
}

// NewPolicyService constructs the authoritative Policy application service.
func NewPolicyService(
	runner PolicyTransactionRunner,
	reader PolicyStateReader,
	finalizer *DesiredStateFinalizer,
	policyWake PolicyWakeSink,
	targetWake TargetWakeSink,
) (*PolicyService, error) {
	if runner == nil {
		return nil, fmt.Errorf("policy transaction runner is required")
	}
	if reader == nil {
		return nil, fmt.Errorf("policy state reader is required")
	}
	if finalizer == nil {
		return nil, fmt.Errorf("desired state finalizer is required")
	}
	if policyWake == nil {
		return nil, fmt.Errorf("policy wake sink is required")
	}
	if targetWake == nil {
		return nil, fmt.Errorf("target wake sink is required")
	}
	return &PolicyService{
		runner: runner, reader: reader, finalizer: finalizer,
		policyWake: policyWake, targetWake: targetWake,
	}, nil
}

// Replace atomically writes complete Policy facts, re-materializes affected
// Targets, resets new retry keys, appends Critical Audit, and advances the
// global Desired snapshot exactly once. Wakes happen only after durability is
// confirmed directly or by exact authoritative readback.
func (s *PolicyService) Replace(ctx context.Context, request PolicyWriteRequest) (PolicyChange, error) {
	if s == nil || s.runner == nil || s.reader == nil || s.finalizer == nil || s.policyWake == nil || s.targetWake == nil {
		return PolicyChange{}, fmt.Errorf("policy service is not initialized")
	}
	result, err := s.replace(ctx, request)
	if err != nil {
		return result, err
	}
	if !result.Changed {
		return result, nil
	}
	if wakeErr := s.policyWake.WakePolicy(ctx, result.NodeID); wakeErr != nil {
		return result, &PostCommitPolicyWakeError{Change: result, Cause: wakeErr}
	}
	if wakeErr := WakeCommittedTargets(ctx, s.targetWake, result.TargetChanges); wakeErr != nil {
		return result, wakeErr
	}
	return result, nil
}

// BootstrapInitialManagedPolicy persists the contract-defined initial Policy
// only when no complete Policy exists. It deliberately sends no post-commit
// wakes: the caller must invoke it before Dispatcher startup, whose complete
// startup key set performs the initial full reconcile.
func (s *PolicyService) BootstrapInitialManagedPolicy(
	ctx context.Context,
	nodeID core.NodeID,
	updatedAt time.Time,
) (PolicyChange, error) {
	if s == nil || s.runner == nil || s.reader == nil || s.finalizer == nil {
		return PolicyChange{}, fmt.Errorf("policy bootstrap service is not initialized")
	}
	if ctx == nil {
		return PolicyChange{}, fmt.Errorf("policy bootstrap context is required")
	}
	if nodeID == "" || updatedAt.IsZero() || updatedAt.UTC().UnixMicro() <= 0 {
		return PolicyChange{}, fmt.Errorf("policy bootstrap node id and update time are required")
	}
	state, err := s.reader.LoadDesiredFirewallState(ctx, nodeID)
	if err == nil {
		return PolicyChange{NodeID: nodeID, PolicyRevision: state.PolicyRevision, SnapshotRevision: state.SnapshotRevision}, nil
	}
	if !errors.Is(err, core.ErrManagedPolicyUninitialized) {
		return PolicyChange{}, fmt.Errorf("read managed policy bootstrap state: %w", err)
	}
	policy, err := core.NewInitialManagedPolicyIntent()
	if err != nil {
		return PolicyChange{}, fmt.Errorf("construct initial managed policy: %w", err)
	}
	result, replaceErr := s.replace(ctx, PolicyWriteRequest{
		NodeID:                 nodeID,
		ExpectedPolicyRevision: 0,
		Policy:                 policy,
		AuditID:                "audit-policy-bootstrap-" + string(nodeID),
		AuditIdempotencyKey:    "policy-bootstrap:" + string(nodeID),
		ActorType:              "system",
		UpdatedAt:              updatedAt,
	})
	if replaceErr == nil {
		return result, nil
	}
	// A concurrent bootstrap may have committed a complete state after the
	// initial read. Only a fresh authoritative read may prove that outcome.
	state, readErr := s.reader.LoadDesiredFirewallState(context.WithoutCancel(ctx), nodeID)
	if readErr == nil {
		return PolicyChange{NodeID: nodeID, PolicyRevision: state.PolicyRevision, SnapshotRevision: state.SnapshotRevision}, nil
	}
	return result, errors.Join(replaceErr, fmt.Errorf("read managed policy bootstrap outcome: %w", readErr))
}

func (s *PolicyService) replace(ctx context.Context, request PolicyWriteRequest) (PolicyChange, error) {
	if s == nil || s.runner == nil || s.reader == nil || s.finalizer == nil {
		return PolicyChange{}, fmt.Errorf("policy service is not initialized")
	}
	if ctx == nil {
		return PolicyChange{}, fmt.Errorf("policy write context is required")
	}
	if err := validatePolicyWriteRequest(request); err != nil {
		return PolicyChange{}, err
	}

	var result PolicyChange
	err := s.runner.RunPolicyTransaction(ctx, func(tx PolicyTransaction) error {
		revision, snapshotRevision, changed, err := tx.ReplaceManagedPolicy(
			ctx, request.NodeID, request.ExpectedPolicyRevision, request.Policy, request.UpdatedAt,
		)
		if err != nil {
			return err
		}
		result = PolicyChange{NodeID: request.NodeID, PolicyRevision: revision, SnapshotRevision: snapshotRevision, Changed: changed}
		if !changed {
			return nil
		}
		changes, err := s.finalizer.MaterializeNodeTargets(ctx, tx, request.NodeID, request.UpdatedAt)
		if err != nil {
			return err
		}
		if err := tx.ResetPolicyReconcileState(ctx, revision, request.UpdatedAt); err != nil {
			return err
		}
		if err := tx.AppendPolicyWriteAudit(ctx, PolicyWriteAudit{
			ID: request.AuditID, IdempotencyKey: request.AuditIdempotencyKey,
			NodeID: request.NodeID, ActorType: request.ActorType, PolicyRevision: revision,
			RelationDigest: request.Policy.RelationDigest, CreatedAt: request.UpdatedAt,
		}); err != nil {
			return err
		}
		for index := range changes {
			changes[index].SnapshotRevision = snapshotRevision
		}
		result.TargetChanges = changes
		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrCommitUnknown) || !result.Changed {
			return PolicyChange{}, err
		}
		if readbackErr := s.proveCommitted(ctx, request, result); readbackErr != nil {
			return result, errors.Join(err, readbackErr)
		}
	}
	return result, nil
}

func validatePolicyWriteRequest(request PolicyWriteRequest) error {
	if request.NodeID == "" {
		return fmt.Errorf("policy write node id is required")
	}
	if err := request.Policy.ValidateComplete(); err != nil {
		return fmt.Errorf("validate policy write: %w", err)
	}
	if request.AuditID == "" || request.AuditIdempotencyKey == "" || request.ActorType == "" {
		return fmt.Errorf("policy write audit identity and actor type are required")
	}
	if request.UpdatedAt.IsZero() || request.UpdatedAt.UTC().UnixMicro() <= 0 {
		return fmt.Errorf("policy write update time is required")
	}
	return nil
}

func (s *PolicyService) proveCommitted(ctx context.Context, request PolicyWriteRequest, result PolicyChange) error {
	readbackContext := context.WithoutCancel(ctx)
	state, err := s.reader.LoadDesiredFirewallState(readbackContext, request.NodeID)
	if err != nil {
		return fmt.Errorf("read back policy commit: %w", err)
	}
	if state.PolicyRevision != result.PolicyRevision || state.SnapshotRevision != result.SnapshotRevision ||
		!sameManagedPolicy(state.Policy, request.Policy) {
		return fmt.Errorf("read back policy commit does not prove requested desired state")
	}
	for _, change := range result.TargetChanges {
		if !containsTargetGeneration(state.Targets, change.Target, change.Generation) {
			return fmt.Errorf("read back policy commit does not prove target %s generation %d", change.Target, change.Generation)
		}
	}
	return nil
}

func sameManagedPolicy(left, right core.ManagedPolicyIntent) bool {
	if left.RelationDigest != right.RelationDigest || len(left.Allowlist) != len(right.Allowlist) || len(left.ProtectedTargets) != len(right.ProtectedTargets) {
		return false
	}
	for index := range left.Allowlist {
		if left.Allowlist[index] != right.Allowlist[index] {
			return false
		}
	}
	for index := range left.ProtectedTargets {
		if left.ProtectedTargets[index] != right.ProtectedTargets[index] {
			return false
		}
	}
	return true
}

func containsTargetGeneration(intents []core.NormalizedTargetEnforcementIntent, target netip.Prefix, generation core.TargetEnforcementGeneration) bool {
	for _, intent := range intents {
		if intent.CanonicalTarget == target && intent.Generation == generation {
			return true
		}
	}
	return false
}
