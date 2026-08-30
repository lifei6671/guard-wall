// Package reconcile implements the M0 C2 retry, fencing, persistence, and mutation rules.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/enforcement"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

var (
	ErrInvalidPlan     = errors.New("operation plan is invalid")
	ErrStalePlan       = errors.New("operation plan does not match current desired state")
	ErrStaleCompletion = errors.New("operation completed for a stale fence")
	ErrRetryNotReady   = errors.New("retry backoff has not elapsed")
	ErrBudgetExhausted = errors.New("retry budget is exhausted")
)

const maxMutationAttempts uint32 = 6

var retryBackoff = [...]time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
	5 * time.Minute,
	15 * time.Minute,
}

// Clock makes retry and audit tests deterministic.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Backend is the minimum physical boundary required by the fake slice.
type Backend interface {
	Probe(context.Context) (fake.Snapshot, error)
	Apply(context.Context, fake.OperationPlan) (fake.ApplyResult, error)
}

// CriticalAuditEvent records an explicit administrator retry request.
type CriticalAuditEvent struct {
	Action        string
	Domain        fake.Domain
	Target        netip.Prefix
	PreviousEpoch core.RetryEpoch
	NewEpoch      core.RetryEpoch
	OccurredAt    time.Time
}

// CriticalAuditWriter is the fail-able boundary that must succeed before a retry epoch is visible.
// The M0 in-memory slice guarantees function-level atomic publication; SQLite transaction atomicity
// remains a store responsibility.
type CriticalAuditWriter interface {
	AppendCriticalAudit(context.Context, CriticalAuditEvent) error
}

// ExecutionResult records one attempted mutation or a convergence recovered by authoritative Probe.
type ExecutionResult struct {
	Apply            fake.ApplyResult
	RecoveredByProbe bool
}

// Controller serializes mutations while allowing immutable Desired snapshots to advance.
type Controller struct {
	backend Backend
	clock   Clock
	audit   CriticalAuditWriter
	nodeID  core.NodeID
	store   RetryStateStore

	mutationMu sync.Mutex
	stateMu    sync.Mutex

	hasDesired     bool
	desired        core.DesiredFirewallSnapshot
	desiredTargets map[netip.Prefix]core.NormalizedTargetEnforcementIntent

	infrastructureEpoch core.RetryEpoch
	policyEpoch         core.RetryEpoch
	targetEpochs        map[netip.Prefix]core.RetryEpoch

	infrastructureStates map[core.InfrastructureRetryKey]core.RetryState
	policyStates         map[core.PolicyRetryKey]core.RetryState
	targetStates         map[core.TargetRetryKey]core.RetryState
	confirmedTargets     map[netip.Prefix]core.TargetEnforcementGeneration
	pendingProbes        map[pendingProbeKey]attemptRef
	syntheticRefs        map[attemptRef]struct{}
	startupProbeRequired bool
	recoveryReloadNeeded bool
}

// NewController constructs an empty retry ledger around a backend and mandatory audit boundary.
func NewController(backend Backend, clock Clock, audit CriticalAuditWriter) (*Controller, error) {
	if backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if audit == nil {
		return nil, fmt.Errorf("critical audit writer is required")
	}
	if clock == nil {
		clock = systemClock{}
	}
	return &Controller{
		backend:              backend,
		clock:                clock,
		audit:                audit,
		desiredTargets:       make(map[netip.Prefix]core.NormalizedTargetEnforcementIntent),
		targetEpochs:         make(map[netip.Prefix]core.RetryEpoch),
		infrastructureStates: make(map[core.InfrastructureRetryKey]core.RetryState),
		policyStates:         make(map[core.PolicyRetryKey]core.RetryState),
		targetStates:         make(map[core.TargetRetryKey]core.RetryState),
		confirmedTargets:     make(map[netip.Prefix]core.TargetEnforcementGeneration),
		pendingProbes:        make(map[pendingProbeKey]attemptRef),
		syntheticRefs:        make(map[attemptRef]struct{}),
	}, nil
}

// SetDesiredSnapshot atomically publishes an immutable complete Desired snapshot. Revisions and
// target generations may only move forward and must change exactly with their owned semantics.
func (c *Controller) SetDesiredSnapshot(snapshot core.DesiredFirewallSnapshot) error {
	prepared, targets, err := prepareDesired(snapshot)
	if err != nil {
		return err
	}

	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.hasDesired {
		if err := validateDesiredTransition(c.desired, c.desiredTargets, prepared, targets); err != nil {
			return err
		}
	}
	c.desired = prepared
	c.desiredTargets = targets
	c.hasDesired = true
	c.activateHydratedStateLocked()
	return nil
}

// Execute validates a plan against current authoritative Desired state and applies it under the
// single external mutation lock.
func (c *Controller) Execute(ctx context.Context, plan fake.OperationPlan) (ExecutionResult, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	if err := c.reloadRecoveryIfNeeded(ctx); err != nil {
		return ExecutionResult{}, err
	}

	if err := fake.ValidatePlan(plan); err != nil {
		persistErr := c.markInvalidPlan(ctx, plan)
		return ExecutionResult{Apply: fake.ApplyResult{
			Kind: fake.ResultRejected, Domain: plan.Domain, Target: plan.Target, Digest: plan.Digest, ErrorCode: "invalid_plan",
		}}, errors.Join(fmt.Errorf("%w: %v", ErrInvalidPlan, err), persistErr)
	}
	probeKey := pendingProbeKeyForPlan(plan)
	var attempt attemptRef
	if c.hasPendingProbes() {
		snapshot, err := c.backend.Probe(ctx)
		if err != nil {
			return ExecutionResult{}, fmt.Errorf("required probe: %w", err)
		}
		// The ambiguous result did not establish the desired postcondition. Bind a fresh fake plan
		// to the just-observed physical state before another attempt is persisted.
		plan.BasisSnapshotDigest = snapshot.Digest()
		plan.Digest = fake.PlanDigest(plan)
		var recovered bool
		attempt, recovered, err = c.beginAfterRequiredProbe(ctx, plan, snapshot, probeKey)
		if err != nil {
			return ExecutionResult{}, err
		}
		if recovered {
			return ExecutionResult{RecoveredByProbe: true}, nil
		}
	} else if c.requiresStartupProbe() {
		snapshot, err := c.backend.Probe(ctx)
		if err != nil {
			return ExecutionResult{}, fmt.Errorf("startup recovery probe: %w", err)
		}
		plan.BasisSnapshotDigest = snapshot.Digest()
		plan.Digest = fake.PlanDigest(plan)
		var recovered bool
		attempt, recovered, err = c.beginAfterStartupProbe(ctx, plan, snapshot)
		if err != nil {
			return ExecutionResult{}, err
		}
		if recovered {
			return ExecutionResult{RecoveredByProbe: true}, nil
		}
	} else {
		var err error
		attempt, err = c.beginAttempt(ctx, plan)
		if err != nil {
			return ExecutionResult{}, err
		}
	}
	result, err := c.backend.Apply(ctx, plan)
	if err != nil {
		persistErr := c.finishFailureAndRequireProbe(ctx, attempt, probeKey, "backend_error")
		return ExecutionResult{}, errors.Join(fmt.Errorf("apply plan: %w", err), persistErr)
	}
	execution := ExecutionResult{Apply: result}

	switch result.Kind {
	case fake.ResultUnknown:
		code := result.ErrorCode
		if code == "" {
			code = "unknown_result"
		}
		if err := c.finishFailureAndRequireProbe(ctx, attempt, probeKey, code); err != nil {
			return execution, err
		}
		return execution, nil
	case fake.ResultRejected:
		code := result.ErrorCode
		if code == "" {
			code = "rejected"
		}
		if err := c.finishFailure(ctx, attempt, probeKey, code, retryable(code)); err != nil {
			return execution, err
		}
		return execution, nil
	case fake.ResultConfirmed:
		snapshot, probeErr := c.backend.Probe(ctx)
		if probeErr != nil {
			persistErr := c.finishFailureAndRequireProbe(ctx, attempt, probeKey, "confirm_probe_failed")
			return execution, errors.Join(fmt.Errorf("confirm probe: %w", probeErr), persistErr)
		}
		return execution, c.commitConfirmed(ctx, attempt, plan, snapshot, probeKey)
	default:
		persistErr := c.finishFailure(ctx, attempt, probeKey, "invalid_apply_result", false)
		return execution, errors.Join(fmt.Errorf("unsupported apply result %d", result.Kind), persistErr)
	}
}

// probeRecovery performs the observation-only half of a Backend healthy event.
// It may confirm or retire durable Probe requirements, but never begins an attempt.
func (c *Controller) probeRecovery(ctx context.Context) (int, []ReconcileKey, error) {
	if ctx == nil {
		return 0, nil, fmt.Errorf("context is required")
	}
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	if err := c.reloadRecoveryIfNeeded(ctx); err != nil {
		return 0, nil, err
	}
	snapshot, err := c.backend.Probe(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("backend recovery probe: %w", err)
	}

	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.hasDesired {
		return 0, nil, nil
	}
	resolved := 0
	unresolved := make([]ReconcileKey, 0)
	seenUnresolved := make(map[ReconcileKey]struct{})
	for key, ref := range c.pendingProbes {
		if !c.pendingKeyMatchesCurrentDesiredLocked(key) {
			if err := c.finishStaleLocked(ctx, ref, key); err != nil {
				return resolved, nil, err
			}
			delete(c.pendingProbes, key)
			continue
		}
		if !c.snapshotMatchesCurrentDesiredLocked(snapshot, key.domain, key.target) {
			dispatchKey := ReconcileKey{Domain: key.domain, Target: key.target}
			if _, exists := seenUnresolved[dispatchKey]; !exists {
				seenUnresolved[dispatchKey] = struct{}{}
				unresolved = append(unresolved, dispatchKey)
			}
			continue
		}
		if err := c.finishRecoveredPendingLocked(ctx, ref, key); err != nil {
			return resolved, nil, err
		}
		delete(c.pendingProbes, key)
		resolved++
	}
	return resolved, unresolved, nil
}

// probeStartupRecovery compares all persisted startup work with one authoritative
// snapshot. Matching keys are confirmed without consuming an attempt; only drifted
// keys are returned to the dispatcher for mutation.
func (c *Controller) probeStartupRecovery(ctx context.Context, keys []ReconcileKey) ([]ReconcileKey, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	if err := c.reloadRecoveryIfNeeded(ctx); err != nil {
		return nil, err
	}
	snapshot, err := c.backend.Probe(ctx)
	if err != nil {
		return nil, fmt.Errorf("startup recovery probe: %w", err)
	}

	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.hasDesired {
		c.startupProbeRequired = false
		return nil, nil
	}
	unresolved := make([]ReconcileKey, 0, len(keys))
	seenUnresolved := make(map[ReconcileKey]struct{}, len(keys))
	resolved := make(map[ReconcileKey]struct{}, len(keys))
	addUnresolved := func(key ReconcileKey) {
		if _, exists := seenUnresolved[key]; exists {
			return
		}
		seenUnresolved[key] = struct{}{}
		unresolved = append(unresolved, key)
	}

	for key, ref := range c.pendingProbes {
		dispatchKey := ReconcileKey{Domain: key.domain, Target: key.target}
		if !c.pendingKeyMatchesCurrentDesiredLocked(key) {
			if err := c.finishStaleLocked(ctx, ref, key); err != nil {
				return nil, err
			}
			delete(c.pendingProbes, key)
			continue
		}
		if !c.snapshotMatchesCurrentDesiredLocked(snapshot, key.domain, key.target) {
			addUnresolved(dispatchKey)
			continue
		}
		if err := c.finishRecoveredPendingLocked(ctx, ref, key); err != nil {
			return nil, err
		}
		delete(c.pendingProbes, key)
		resolved[dispatchKey] = struct{}{}
	}

	for _, key := range keys {
		if _, alreadyResolved := resolved[key]; alreadyResolved {
			continue
		}
		if !c.snapshotMatchesCurrentDesiredLocked(snapshot, key.Domain, key.Target) {
			addUnresolved(key)
			continue
		}
		ref, ok := c.currentAttemptRefForKeyLocked(key)
		if !ok {
			continue
		}
		state := c.getState(ref)
		state.Status = core.ReconcileConverged
		state.NextAttemptAt = nil
		state.LastErrorCode = ""
		if err := c.persistStateLocked(ctx, ref, state, nil, nil); err != nil {
			return nil, fmt.Errorf("persist startup Probe recovery: %w", err)
		}
		c.putState(ref, state)
		if key.Domain == fake.DomainTarget {
			c.confirmedTargets[key.Target] = ref.target.Generation
		}
	}
	c.startupProbeRequired = false
	return unresolved, nil
}

// RetryInfrastructure publishes a new epoch only after Critical Audit succeeds.
func (c *Controller) RetryInfrastructure(ctx context.Context) (core.InfrastructureRetryKey, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	if err := c.reloadRecoveryIfNeeded(ctx); err != nil {
		return core.InfrastructureRetryKey{}, err
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.hasDesired {
		return core.InfrastructureRetryKey{}, fmt.Errorf("desired snapshot is not initialized")
	}
	previous := c.infrastructureEpoch
	next := previous + 1
	if next == 0 {
		return core.InfrastructureRetryKey{}, fmt.Errorf("infrastructure retry epoch overflow")
	}
	event := CriticalAuditEvent{Action: "reconcile_retry", Domain: fake.DomainInfrastructure, PreviousEpoch: previous, NewEpoch: next, OccurredAt: c.clock.Now()}
	if err := c.audit.AppendCriticalAudit(ctx, event); err != nil {
		return core.InfrastructureRetryKey{}, err
	}
	key := core.InfrastructureRetryKey{Revision: c.desired.InfrastructureRevision, Epoch: next}
	state := core.RetryState{Status: core.ReconcilePending}
	ref := attemptRef{domain: fake.DomainInfrastructure, infrastructure: key}
	if err := c.persistStateLocked(ctx, ref, state, nil, nil); err != nil {
		return core.InfrastructureRetryKey{}, fmt.Errorf("persist infrastructure retry epoch: %w", err)
	}
	c.markPendingRefsSupersededLocked(ref)
	c.infrastructureEpoch = next
	c.infrastructureStates[key] = state
	return key, nil
}

// RetryPolicy publishes a new epoch only after Critical Audit succeeds.
func (c *Controller) RetryPolicy(ctx context.Context) (core.PolicyRetryKey, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	if err := c.reloadRecoveryIfNeeded(ctx); err != nil {
		return core.PolicyRetryKey{}, err
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.hasDesired {
		return core.PolicyRetryKey{}, fmt.Errorf("desired snapshot is not initialized")
	}
	previous := c.policyEpoch
	next := previous + 1
	if next == 0 {
		return core.PolicyRetryKey{}, fmt.Errorf("policy retry epoch overflow")
	}
	event := CriticalAuditEvent{Action: "reconcile_retry", Domain: fake.DomainPolicy, PreviousEpoch: previous, NewEpoch: next, OccurredAt: c.clock.Now()}
	if err := c.audit.AppendCriticalAudit(ctx, event); err != nil {
		return core.PolicyRetryKey{}, err
	}
	key := core.PolicyRetryKey{Revision: c.desired.PolicyRevision, Epoch: next}
	state := core.RetryState{Status: core.ReconcilePending}
	ref := attemptRef{domain: fake.DomainPolicy, policy: key}
	if err := c.persistStateLocked(ctx, ref, state, nil, nil); err != nil {
		return core.PolicyRetryKey{}, fmt.Errorf("persist policy retry epoch: %w", err)
	}
	c.markPendingRefsSupersededLocked(ref)
	c.policyEpoch = next
	c.policyStates[key] = state
	return key, nil
}

// RetryTarget publishes a new epoch only for one target after Critical Audit succeeds.
func (c *Controller) RetryTarget(ctx context.Context, target netip.Prefix) (core.TargetRetryKey, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	if err := c.reloadRecoveryIfNeeded(ctx); err != nil {
		return core.TargetRetryKey{}, err
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	intent, ok := c.desiredTargets[target]
	if !ok {
		return core.TargetRetryKey{}, fmt.Errorf("target has no current desired intent")
	}
	previous := c.targetEpochs[target]
	next := previous + 1
	if next == 0 {
		return core.TargetRetryKey{}, fmt.Errorf("target retry epoch overflow")
	}
	event := CriticalAuditEvent{Action: "reconcile_retry", Domain: fake.DomainTarget, Target: target, PreviousEpoch: previous, NewEpoch: next, OccurredAt: c.clock.Now()}
	if err := c.audit.AppendCriticalAudit(ctx, event); err != nil {
		return core.TargetRetryKey{}, err
	}
	key := core.TargetRetryKey{Target: target, Generation: intent.Generation, Epoch: next}
	state := core.RetryState{Status: core.ReconcilePending}
	ref := attemptRef{domain: fake.DomainTarget, target: key}
	if err := c.persistStateLocked(ctx, ref, state, nil, nil); err != nil {
		return core.TargetRetryKey{}, fmt.Errorf("persist target retry epoch: %w", err)
	}
	c.markPendingRefsSupersededLocked(ref)
	c.targetEpochs[target] = next
	c.targetStates[key] = state
	return key, nil
}

// InfrastructureState returns the current infrastructure ledger entry.
func (c *Controller) InfrastructureState() (core.InfrastructureRetryKey, core.RetryState, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	key := core.InfrastructureRetryKey{Revision: c.desired.InfrastructureRevision, Epoch: c.infrastructureEpoch}
	state, ok := c.infrastructureStates[key]
	return key, cloneRetryState(state), ok
}

// PolicyState returns the current policy ledger entry.
func (c *Controller) PolicyState() (core.PolicyRetryKey, core.RetryState, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	key := core.PolicyRetryKey{Revision: c.desired.PolicyRevision, Epoch: c.policyEpoch}
	state, ok := c.policyStates[key]
	return key, cloneRetryState(state), ok
}

// TargetState returns one target's current ledger entry.
func (c *Controller) TargetState(target netip.Prefix) (core.TargetRetryKey, core.RetryState, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	intent, exists := c.desiredTargets[target]
	if !exists {
		return core.TargetRetryKey{}, core.RetryState{}, false
	}
	key := core.TargetRetryKey{Target: target, Generation: intent.Generation, Epoch: c.targetEpochs[target]}
	state, ok := c.targetStates[key]
	return key, cloneRetryState(state), ok
}

// ConfirmedTarget returns the last generation written after authoritative confirmation and fencing.
func (c *Controller) ConfirmedTarget(target netip.Prefix) (core.TargetEnforcementGeneration, bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	generation, ok := c.confirmedTargets[target]
	return generation, ok
}

// ProbeRequired reports whether an ambiguous result requires a read before any next write.
func (c *Controller) ProbeRequired() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return len(c.pendingProbes) != 0
}

// pendingProbeKey binds an ambiguous physical outcome to its failure domain and operation fence.
// A result for one domain, target, or generation must never force or satisfy a Probe for another.
type pendingProbeKey struct {
	domain                 fake.Domain
	target                 netip.Prefix
	infrastructureRevision core.InfrastructureRevision
	policyRevision         core.PolicyRevision
	targetGeneration       core.TargetEnforcementGeneration
	snapshotRevision       core.SnapshotRevision
	fenceSnapshotRevision  bool
}

type attemptRef struct {
	domain         fake.Domain
	infrastructure core.InfrastructureRetryKey
	policy         core.PolicyRetryKey
	target         core.TargetRetryKey
}

func (c *Controller) beginAttempt(ctx context.Context, plan fake.OperationPlan) (attemptRef, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.planMatchesCurrentDesiredLocked(plan) {
		return attemptRef{}, ErrStalePlan
	}
	return c.beginAttemptLocked(ctx, plan)
}

func (c *Controller) beginAttemptLocked(ctx context.Context, plan fake.OperationPlan) (attemptRef, error) {
	ref, state, ok := c.attemptStateLocked(plan)
	if !ok {
		state = core.RetryState{Status: core.ReconcilePending}
	}
	now := c.clock.Now()
	if state.Status == core.ReconcileDegraded || state.AttemptCount >= maxMutationAttempts {
		return attemptRef{}, ErrBudgetExhausted
	}
	if state.Status == core.ReconcileRetryWaiting && state.NextAttemptAt != nil && now.Before(*state.NextAttemptAt) {
		return attemptRef{}, ErrRetryNotReady
	}
	state.AttemptCount++
	state.Status = core.ReconcileApplying
	state.LastAttemptAt = timePointer(now)
	state.NextAttemptAt = nil
	state.LastErrorCode = ""
	probeKey := pendingProbeKeyForPlan(plan)
	previousPending, replacesPending := c.pendingProbes[probeKey]
	if err := c.persistApplyingStateLocked(ctx, ref, state, probeKey, previousPending, replacesPending); err != nil {
		return attemptRef{}, fmt.Errorf("persist applying attempt: %w", err)
	}
	c.putState(ref, state)
	c.pendingProbes[probeKey] = ref
	if replacesPending {
		delete(c.syntheticRefs, previousPending)
	}
	return ref, nil
}

func (c *Controller) beginAfterRequiredProbe(ctx context.Context, plan fake.OperationPlan, snapshot fake.Snapshot, key pendingProbeKey) (attemptRef, bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.planMatchesCurrentDesiredLocked(plan) {
		return attemptRef{}, false, ErrStalePlan
	}
	currentRecovered, err := c.resolvePendingProbesLocked(ctx, snapshot, key, plan)
	if err != nil {
		return attemptRef{}, false, err
	}
	if currentRecovered {
		return attemptRef{}, true, nil
	}
	attempt, err := c.beginAttemptLocked(ctx, plan)
	if err != nil {
		// A premature retry must retain its exact pending Probe requirement. Unrelated
		// pending keys do not consume this plan's retry budget.
		return attemptRef{}, false, err
	}
	return attempt, false, nil
}

func (c *Controller) beginAfterStartupProbe(ctx context.Context, plan fake.OperationPlan, snapshot fake.Snapshot) (attemptRef, bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.planMatchesCurrentDesiredLocked(plan) {
		return attemptRef{}, false, ErrStalePlan
	}
	if !c.snapshotMatchesCurrentDesiredLocked(snapshot, plan.Domain, plan.Target) {
		attempt, err := c.beginAttemptLocked(ctx, plan)
		if err == nil {
			c.startupProbeRequired = false
		}
		return attempt, false, err
	}
	ref, state, ok := c.attemptStateLocked(plan)
	if !ok {
		state = core.RetryState{Status: core.ReconcilePending}
	}
	state.Status = core.ReconcileConverged
	state.NextAttemptAt = nil
	state.LastErrorCode = ""
	if err := c.persistStateLocked(ctx, ref, state, nil, nil); err != nil {
		return attemptRef{}, false, fmt.Errorf("persist startup Probe recovery: %w", err)
	}
	c.putState(ref, state)
	c.startupProbeRequired = false
	if plan.Domain == fake.DomainTarget {
		c.confirmedTargets[plan.Target] = plan.ExpectedTargetGeneration
	}
	return attemptRef{}, true, nil
}

func (c *Controller) resolvePendingProbesLocked(ctx context.Context, snapshot fake.Snapshot, currentKey pendingProbeKey, currentPlan fake.OperationPlan) (bool, error) {
	currentRecovered := false
	for key, ref := range c.pendingProbes {
		if !c.pendingKeyMatchesCurrentDesiredLocked(key) {
			if err := c.finishStaleLocked(ctx, ref, key); err != nil {
				return false, err
			}
			delete(c.pendingProbes, key)
			continue
		}
		if !c.snapshotMatchesCurrentDesiredLocked(snapshot, key.domain, key.target) {
			continue
		}
		if key == currentKey {
			if err := c.finishRecoveredByProbeLocked(ctx, currentPlan, key, ref); err != nil {
				return false, err
			}
			currentRecovered = true
		} else {
			if err := c.finishRecoveredPendingLocked(ctx, ref, key); err != nil {
				return false, err
			}
		}
		delete(c.pendingProbes, key)
	}
	return currentRecovered, nil
}

func (c *Controller) pendingKeyMatchesCurrentDesiredLocked(key pendingProbeKey) bool {
	switch key.domain {
	case fake.DomainInfrastructure:
		return key.infrastructureRevision == c.desired.InfrastructureRevision &&
			(!key.fenceSnapshotRevision || key.snapshotRevision == c.desired.SnapshotRevision)
	case fake.DomainPolicy:
		return key.policyRevision == c.desired.PolicyRevision
	case fake.DomainTarget:
		intent, ok := c.desiredTargets[key.target]
		return ok && key.targetGeneration == intent.Generation
	default:
		return false
	}
}

func (c *Controller) finishRecoveredPendingLocked(ctx context.Context, ref attemptRef, key pendingProbeKey) error {
	stateRef, state, ok := c.currentStateForPendingKeyLocked(key)
	if !ok {
		stateRef = ref
		state = c.getState(ref)
	}
	state.Status = core.ReconcileConverged
	state.NextAttemptAt = nil
	state.LastErrorCode = ""
	if err := c.persistStateAndClearProbeLocked(ctx, stateRef, state, key, ref); err != nil {
		return fmt.Errorf("persist Probe recovery: %w", err)
	}
	c.putState(stateRef, state)
	delete(c.syntheticRefs, ref)
	if key.domain == fake.DomainTarget {
		c.confirmedTargets[key.target] = key.targetGeneration
	}
	return nil
}

func (c *Controller) attemptStateLocked(plan fake.OperationPlan) (attemptRef, core.RetryState, bool) {
	ref := attemptRef{domain: plan.Domain}
	switch plan.Domain {
	case fake.DomainInfrastructure:
		ref.infrastructure = core.InfrastructureRetryKey{Revision: plan.ExpectedInfrastructureRevision, Epoch: c.infrastructureEpoch}
		state, ok := c.infrastructureStates[ref.infrastructure]
		return ref, state, ok
	case fake.DomainPolicy:
		ref.policy = core.PolicyRetryKey{Revision: plan.ExpectedPolicyRevision, Epoch: c.policyEpoch}
		state, ok := c.policyStates[ref.policy]
		return ref, state, ok
	case fake.DomainTarget:
		ref.target = core.TargetRetryKey{Target: plan.Target, Generation: plan.ExpectedTargetGeneration, Epoch: c.targetEpochs[plan.Target]}
		state, ok := c.targetStates[ref.target]
		return ref, state, ok
	default:
		return ref, core.RetryState{}, false
	}
}

func (c *Controller) currentAttemptRefForKeyLocked(key ReconcileKey) (attemptRef, bool) {
	ref := attemptRef{domain: key.Domain}
	switch key.Domain {
	case fake.DomainInfrastructure:
		ref.infrastructure = core.InfrastructureRetryKey{Revision: c.desired.InfrastructureRevision, Epoch: c.infrastructureEpoch}
		return ref, true
	case fake.DomainPolicy:
		ref.policy = core.PolicyRetryKey{Revision: c.desired.PolicyRevision, Epoch: c.policyEpoch}
		return ref, true
	case fake.DomainTarget:
		intent, exists := c.desiredTargets[key.Target]
		if !exists {
			return ref, false
		}
		ref.target = core.TargetRetryKey{Target: key.Target, Generation: intent.Generation, Epoch: c.targetEpochs[key.Target]}
		return ref, true
	default:
		return ref, false
	}
}

func (c *Controller) finishFailure(ctx context.Context, ref attemptRef, key pendingProbeKey, code string, canRetry bool) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.finishFailureLocked(ctx, ref, key, code, canRetry, false)
}

func (c *Controller) finishFailureLocked(
	ctx context.Context,
	ref attemptRef,
	key pendingProbeKey,
	code string,
	canRetry bool,
	requireProbe bool,
) error {
	state := c.getState(ref)
	state.LastErrorCode = code
	if !canRetry || state.AttemptCount >= maxMutationAttempts {
		state.Status = core.ReconcileDegraded
		state.NextAttemptAt = nil
	} else {
		state.Status = core.ReconcileRetryWaiting
		next := c.clock.Now().Add(retryBackoff[state.AttemptCount-1])
		state.NextAttemptAt = &next
	}
	var upsertProbe, deleteProbe *pendingProbeKey
	if requireProbe {
		upsertProbe = &key
	} else {
		deleteProbe = &key
	}
	if err := c.persistStateLocked(ctx, ref, state, upsertProbe, deleteProbe); err != nil {
		// The pre-mutation Applying transition is already durable and requires Probe.
		// Retain the in-memory barrier until the final transition can be persisted.
		c.pendingProbes[key] = ref
		return fmt.Errorf("persist reconcile failure: %w", err)
	}
	c.putState(ref, state)
	if requireProbe {
		c.pendingProbes[key] = ref
	} else {
		delete(c.pendingProbes, key)
	}
	return nil
}

func (c *Controller) finishFailureAndRequireProbe(ctx context.Context, ref attemptRef, key pendingProbeKey, code string) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.finishFailureLocked(ctx, ref, key, code, true, true)
}

func (c *Controller) finishStaleLocked(ctx context.Context, ref attemptRef, key pendingProbeKey) error {
	state := c.getState(ref)
	state.Status = core.ReconcilePending
	state.NextAttemptAt = nil
	state.LastErrorCode = "stale_completion"
	stateRef := ref
	persistedState := state
	if currentRef, currentState, ok := c.currentStateForPendingKeyLocked(key); ok && currentRef != ref {
		stateRef = currentRef
		persistedState = currentState
	}
	var err error
	if _, synthetic := c.syntheticRefs[ref]; synthetic && stateRef == ref {
		err = c.clearProbeLocked(ctx, key, ref)
	} else {
		err = c.persistStateAndClearProbeLocked(ctx, stateRef, persistedState, key, ref)
	}
	if err != nil {
		return fmt.Errorf("persist stale completion: %w", err)
	}
	c.putState(ref, state)
	delete(c.syntheticRefs, ref)
	return nil
}

func (c *Controller) finishConvergedLocked(ctx context.Context, ref attemptRef, plan fake.OperationPlan, key pendingProbeKey) error {
	state := c.getState(ref)
	state.Status = core.ReconcileConverged
	state.NextAttemptAt = nil
	state.LastErrorCode = ""
	if err := c.persistStateLocked(ctx, ref, state, nil, &key); err != nil {
		return fmt.Errorf("persist converged state: %w", err)
	}
	c.putState(ref, state)
	if plan.Domain == fake.DomainTarget {
		c.confirmedTargets[plan.Target] = plan.ExpectedTargetGeneration
	}
	return nil
}

func (c *Controller) finishRecoveredByProbeLocked(
	ctx context.Context,
	plan fake.OperationPlan,
	key pendingProbeKey,
	pendingRef attemptRef,
) error {
	ref, state, ok := c.attemptStateLocked(plan)
	if !ok {
		state = core.RetryState{Status: core.ReconcilePending}
	}
	state.Status = core.ReconcileConverged
	state.NextAttemptAt = nil
	state.LastErrorCode = ""
	if err := c.persistStateAndClearProbeLocked(ctx, ref, state, key, pendingRef); err != nil {
		return fmt.Errorf("persist observation-only convergence: %w", err)
	}
	c.putState(ref, state)
	delete(c.syntheticRefs, pendingRef)
	if plan.Domain == fake.DomainTarget {
		c.confirmedTargets[plan.Target] = plan.ExpectedTargetGeneration
	}
	return nil
}

func (c *Controller) commitConfirmed(ctx context.Context, ref attemptRef, plan fake.OperationPlan, snapshot fake.Snapshot, key pendingProbeKey) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	// The current fence, observed physical postcondition, and durable in-memory writeback are one
	// state transition. SetDesiredSnapshot cannot publish a new fence between these checks.
	if !c.planMatchesCurrentDesiredLocked(plan) {
		if err := c.finishStaleLocked(ctx, ref, key); err != nil {
			c.pendingProbes[key] = ref
			return errors.Join(ErrStaleCompletion, err)
		}
		return ErrStaleCompletion
	}
	if !c.snapshotMatchesCurrentDesiredLocked(snapshot, plan.Domain, plan.Target) {
		return c.finishFailureLocked(ctx, ref, key, "postcondition_mismatch", true, false)
	}
	if err := c.finishConvergedLocked(ctx, ref, plan, key); err != nil {
		c.pendingProbes[key] = ref
		return err
	}
	delete(c.pendingProbes, key)
	return nil
}

func (c *Controller) markInvalidPlan(ctx context.Context, plan fake.OperationPlan) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.hasDesired {
		return nil
	}
	ref, state, ok := c.attemptStateLocked(plan)
	if ref.domain != fake.DomainInfrastructure && ref.domain != fake.DomainPolicy && ref.domain != fake.DomainTarget {
		return nil
	}
	if !ok {
		state = core.RetryState{}
	}
	state.Status = core.ReconcileDegraded
	state.NextAttemptAt = nil
	state.LastErrorCode = "invalid_plan"
	if err := c.persistStateLocked(ctx, ref, state, nil, nil); err != nil {
		return fmt.Errorf("persist invalid plan: %w", err)
	}
	c.putState(ref, state)
	return nil
}

func (c *Controller) getState(ref attemptRef) core.RetryState {
	switch ref.domain {
	case fake.DomainInfrastructure:
		return c.infrastructureStates[ref.infrastructure]
	case fake.DomainPolicy:
		return c.policyStates[ref.policy]
	case fake.DomainTarget:
		return c.targetStates[ref.target]
	default:
		panic("invalid attempt domain")
	}
}

func (c *Controller) putState(ref attemptRef, state core.RetryState) {
	switch ref.domain {
	case fake.DomainInfrastructure:
		c.infrastructureStates[ref.infrastructure] = state
	case fake.DomainPolicy:
		c.policyStates[ref.policy] = state
	case fake.DomainTarget:
		c.targetStates[ref.target] = state
	default:
		panic("invalid attempt domain")
	}
}

func (c *Controller) planMatchesCurrentDesired(plan fake.OperationPlan) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.planMatchesCurrentDesiredLocked(plan)
}

func (c *Controller) planMatchesCurrentDesiredLocked(plan fake.OperationPlan) bool {
	if !c.hasDesired {
		return false
	}
	switch plan.Domain {
	case fake.DomainInfrastructure:
		return plan.ExpectedInfrastructureRevision == c.desired.InfrastructureRevision &&
			(!plan.FenceSnapshotRevision || plan.ExpectedSnapshotRevision == c.desired.SnapshotRevision) &&
			plan.DesiredInfrastructure == c.desired.Infrastructure
	case fake.DomainPolicy:
		return plan.ExpectedPolicyRevision == c.desired.PolicyRevision && plan.DesiredPolicy == c.desired.Policy
	case fake.DomainTarget:
		intent, ok := c.desiredTargets[plan.Target]
		return ok && plan.ExpectedTargetGeneration == intent.Generation && plan.DesiredTarget.Generation == intent.Generation &&
			enforcement.Equivalent(plan.DesiredTarget, intent)
	default:
		return false
	}
}

func (c *Controller) snapshotMatchesCurrentDesired(snapshot fake.Snapshot, domain fake.Domain, target netip.Prefix) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.snapshotMatchesCurrentDesiredLocked(snapshot, domain, target)
}

func (c *Controller) snapshotMatchesCurrentDesiredLocked(snapshot fake.Snapshot, domain fake.Domain, target netip.Prefix) bool {
	if !c.hasDesired {
		return false
	}
	switch domain {
	case fake.DomainInfrastructure:
		return snapshot.Infrastructure != nil && snapshot.Infrastructure.Backend == c.desired.Infrastructure.Backend &&
			snapshot.Infrastructure.OwnerVersion == c.desired.Infrastructure.OwnerVersion && snapshot.Infrastructure.Digest == c.desired.Infrastructure.Digest
	case fake.DomainPolicy:
		return snapshot.Policy != nil && snapshot.Policy.RelationDigest == c.desired.Policy.RelationDigest
	case fake.DomainTarget:
		intent, ok := c.desiredTargets[target]
		return ok && physicalTargetMatches(snapshot.Targets, intent)
	default:
		return false
	}
}

func (c *Controller) hasPendingProbes() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return len(c.pendingProbes) != 0
}

func (c *Controller) requiresStartupProbe() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.startupProbeRequired
}

func (c *Controller) markPendingRefsSupersededLocked(current attemptRef) {
	for _, pending := range c.pendingProbes {
		if pending == current || pending.domain != current.domain {
			continue
		}
		samePhysicalKey := false
		switch current.domain {
		case fake.DomainInfrastructure:
			samePhysicalKey = pending.infrastructure.Revision == current.infrastructure.Revision
		case fake.DomainPolicy:
			samePhysicalKey = pending.policy.Revision == current.policy.Revision
		case fake.DomainTarget:
			samePhysicalKey = pending.target.Target == current.target.Target &&
				pending.target.Generation == current.target.Generation
		}
		if samePhysicalKey {
			c.syntheticRefs[pending] = struct{}{}
		}
	}
}

func pendingProbeKeyForPlan(plan fake.OperationPlan) pendingProbeKey {
	return pendingProbeKey{
		domain:                 plan.Domain,
		target:                 plan.Target,
		infrastructureRevision: plan.ExpectedInfrastructureRevision,
		policyRevision:         plan.ExpectedPolicyRevision,
		targetGeneration:       plan.ExpectedTargetGeneration,
		snapshotRevision:       plan.ExpectedSnapshotRevision,
		fenceSnapshotRevision:  plan.FenceSnapshotRevision,
	}
}

func prepareDesired(snapshot core.DesiredFirewallSnapshot) (core.DesiredFirewallSnapshot, map[netip.Prefix]core.NormalizedTargetEnforcementIntent, error) {
	if snapshot.SnapshotRevision == 0 || snapshot.InfrastructureRevision == 0 || snapshot.PolicyRevision == 0 {
		return core.DesiredFirewallSnapshot{}, nil, fmt.Errorf("desired revisions must be positive")
	}
	if snapshot.Infrastructure.Backend == "" || snapshot.Infrastructure.OwnerVersion == "" || snapshot.Infrastructure.Digest == "" {
		return core.DesiredFirewallSnapshot{}, nil, fmt.Errorf("desired infrastructure is incomplete")
	}
	if snapshot.Policy.RelationDigest == "" {
		return core.DesiredFirewallSnapshot{}, nil, fmt.Errorf("desired policy digest is required")
	}
	targets := make(map[netip.Prefix]core.NormalizedTargetEnforcementIntent, len(snapshot.Targets))
	prepared := snapshot
	prepared.Targets = make([]core.NormalizedTargetEnforcementIntent, 0, len(snapshot.Targets))
	for _, intent := range snapshot.Targets {
		if err := intent.Validate(); err != nil {
			return core.DesiredFirewallSnapshot{}, nil, fmt.Errorf("validate desired target: %w", err)
		}
		if _, duplicate := targets[intent.CanonicalTarget]; duplicate {
			return core.DesiredFirewallSnapshot{}, nil, fmt.Errorf("duplicate desired target %s", intent.CanonicalTarget)
		}
		cloned := cloneIntent(intent)
		targets[intent.CanonicalTarget] = cloned
		prepared.Targets = append(prepared.Targets, cloned)
	}
	return prepared, targets, nil
}

func validateDesiredTransition(
	previous core.DesiredFirewallSnapshot,
	previousTargets map[netip.Prefix]core.NormalizedTargetEnforcementIntent,
	next core.DesiredFirewallSnapshot,
	nextTargets map[netip.Prefix]core.NormalizedTargetEnforcementIntent,
) error {
	if next.SnapshotRevision < previous.SnapshotRevision || next.InfrastructureRevision < previous.InfrastructureRevision || next.PolicyRevision < previous.PolicyRevision {
		return fmt.Errorf("desired revisions cannot move backward")
	}
	infraChanged := previous.Infrastructure != next.Infrastructure
	policyChanged := previous.Policy != next.Policy
	if infraChanged != (next.InfrastructureRevision > previous.InfrastructureRevision) {
		return fmt.Errorf("infrastructure revision must change exactly with infrastructure intent")
	}
	if policyChanged != (next.PolicyRevision > previous.PolicyRevision) {
		return fmt.Errorf("policy revision must change exactly with policy intent")
	}
	externalChanged := infraChanged || policyChanged
	for target, oldIntent := range previousTargets {
		newIntent, ok := nextTargets[target]
		if !ok {
			return fmt.Errorf("target %s cannot disappear; publish an Absent intent generation", target)
		}
		semanticChanged := !enforcement.Equivalent(oldIntent, newIntent)
		if semanticChanged {
			if newIntent.Generation <= oldIntent.Generation {
				return fmt.Errorf("changed target %s must advance generation", target)
			}
			externalChanged = true
		} else if newIntent.Generation != oldIntent.Generation {
			return fmt.Errorf("unchanged target %s cannot advance generation", target)
		}
	}
	for target := range nextTargets {
		if _, exists := previousTargets[target]; !exists {
			externalChanged = true
		}
	}
	if externalChanged != (next.SnapshotRevision > previous.SnapshotRevision) {
		return fmt.Errorf("snapshot revision must change exactly with external desired state")
	}
	return nil
}

func physicalTargetMatches(targets map[netip.Prefix]core.PhysicalTargetObserved, intent core.NormalizedTargetEnforcementIntent) bool {
	observed, exists := targets[intent.CanonicalTarget]
	if intent.BanMembership == core.BanAbsent {
		if !exists {
			return true
		}
		// An explicit Absent observation is canonical only when it carries no stale physical
		// attributes. CanonicalTarget and BanMembership identify the observation itself.
		return observed.CanonicalTarget == intent.CanonicalTarget &&
			observed.ObservedAt.IsZero() && observed.Backend == "" &&
			observed.BanMembership == core.ObservedMembershipAbsent &&
			observed.PolicyCoverage == core.ObservedPolicyUnknown && observed.PolicyRelationDigest == "" &&
			observed.TimeoutMode == core.TimeoutNone && observed.NativeExpiry == nil && observed.Scopes == 0 &&
			observed.AddressFamily == 0 && observed.OwnerVersion == "" && observed.LastErrorCode == ""
	}
	if !exists || observed.BanMembership != core.ObservedMembershipPresent || observed.TimeoutMode != intent.TimeoutMode ||
		observed.Scopes != intent.Scopes || observed.AddressFamily != intent.AddressFamily ||
		observed.PolicyRelationDigest != intent.PolicyRelationDigest || observed.OwnerVersion != intent.BackendAttributesDigest {
		return false
	}
	wantCoverage := core.ObservedPolicyNone
	switch intent.PolicyCoverage {
	case core.PolicyCoveragePartial:
		wantCoverage = core.ObservedPolicyPartial
	case core.PolicyCoverageFull:
		wantCoverage = core.ObservedPolicyFull
	}
	if observed.PolicyCoverage != wantCoverage {
		return false
	}
	if intent.TimeoutMode == core.TimeoutNative {
		return equalTime(observed.NativeExpiry, enforcement.NativeExpiryForIntent(intent))
	}
	return observed.NativeExpiry == nil
}

func retryable(code string) bool {
	switch code {
	case "ownership_conflict", "unsupported", "invalid_plan":
		return false
	default:
		return true
	}
}

func cloneIntent(intent core.NormalizedTargetEnforcementIntent) core.NormalizedTargetEnforcementIntent {
	intent.EffectiveUntil = cloneTime(intent.EffectiveUntil)
	return intent
}

func cloneRetryState(state core.RetryState) core.RetryState {
	state.LastAttemptAt = cloneTime(state.LastAttemptAt)
	state.NextAttemptAt = cloneTime(state.NextAttemptAt)
	return state
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func timePointer(value time.Time) *time.Time {
	return &value
}
