package core

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// ErrReconcileCommitUnknown means SQLite did not prove whether a reconcile
// transition committed. Callers must resolve it by authoritative readback.
var ErrReconcileCommitUnknown = errors.New("reconcile transition commit outcome is unknown")

// ReconcileCommitUnknownError preserves the physical commit error while
// exposing a stable classification for readback recovery.
type ReconcileCommitUnknownError struct {
	Cause error
}

func (e *ReconcileCommitUnknownError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrReconcileCommitUnknown.Error()
	}
	return fmt.Sprintf("%v: %v", ErrReconcileCommitUnknown, e.Cause)
}

func (e *ReconcileCommitUnknownError) Unwrap() error { return ErrReconcileCommitUnknown }

// NewReconcileCommitUnknownError marks an indeterminate SQLite commit.
func NewReconcileCommitUnknownError(cause error) error {
	return &ReconcileCommitUnknownError{Cause: cause}
}

// ReconcileDomain identifies one independently fenced retry ledger.
type ReconcileDomain uint8

const (
	ReconcileDomainInfrastructure ReconcileDomain = iota + 1
	ReconcileDomainPolicy
	ReconcileDomainTarget
)

// PersistedReconcileState is the durable retry state for one typed domain key.
// Fields not owned by Domain must remain at their zero value.
type PersistedReconcileState struct {
	NodeID                 NodeID
	Domain                 ReconcileDomain
	InfrastructureRevision InfrastructureRevision
	PolicyRevision         PolicyRevision
	Target                 netip.Prefix
	TargetGeneration       TargetEnforcementGeneration
	RetryEpoch             RetryEpoch
	RetryState             RetryState
	UpdatedAt              time.Time
}

// PersistedProbeRequirement preserves an ambiguous mutation that must be
// authoritatively probed before another mutation is allowed.
type PersistedProbeRequirement struct {
	NodeID                 NodeID
	Domain                 ReconcileDomain
	InfrastructureRevision InfrastructureRevision
	PolicyRevision         PolicyRevision
	Target                 netip.Prefix
	TargetGeneration       TargetEnforcementGeneration
	SnapshotRevision       SnapshotRevision
	FenceSnapshotRevision  bool
	RetryEpoch             RetryEpoch
	AttemptCount           uint32
	RecordedAt             time.Time
}

// ReconcileStateTransition atomically changes a retry ledger row and its exact
// pending-Probe requirements. UpsertProbe and DeleteProbe may be combined to
// replace an older ambiguous attempt without exposing an empty barrier.
type ReconcileStateTransition struct {
	State       PersistedReconcileState
	UpsertProbe *PersistedProbeRequirement
	DeleteProbe *PersistedProbeRequirement
	// DeleteOnly clears a stale exact Probe requirement without rewriting a
	// singleton/current ledger row that may already contain a newer key.
	DeleteOnly bool
}

// ReconcileRecoverySnapshot is the flat durable state required to hydrate one
// node's in-memory reconcile controller.
type ReconcileRecoverySnapshot struct {
	States            []PersistedReconcileState
	ProbeRequirements []PersistedProbeRequirement
}
