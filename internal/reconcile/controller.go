// Package reconcile implements the in-memory M0 C2 retry, fencing, and mutation rules.
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
	return nil
}

// Execute validates a plan against current authoritative Desired state and applies it under the
// single external mutation lock.
func (c *Controller) Execute(ctx context.Context, plan fake.OperationPlan) (ExecutionResult, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()

	if err := fake.ValidatePlan(plan); err != nil {
		c.markInvalidPlan(plan)
		return ExecutionResult{Apply: fake.ApplyResult{
			Kind: fake.ResultRejected, Domain: plan.Domain, Target: plan.Target, Digest: plan.Digest, ErrorCode: "invalid_plan",
		}}, fmt.Errorf("%w: %v", ErrInvalidPlan, err)
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
		attempt, recovered, err = c.beginAfterRequiredProbe(plan, snapshot, probeKey)
		if err != nil {
			return ExecutionResult{}, err
		}
		if recovered {
			return ExecutionResult{RecoveredByProbe: true}, nil
		}
	} else {
		var err error
		attempt, err = c.beginAttempt(plan)
		if err != nil {
			return ExecutionResult{}, err
		}
	}
	result, err := c.backend.Apply(ctx, plan)
	if err != nil {
		c.finishFailureAndRequireProbe(attempt, probeKey, "backend_error")
		return ExecutionResult{}, fmt.Errorf("apply plan: %w", err)
	}
	execution := ExecutionResult{Apply: result}

	switch result.Kind {
	case fake.ResultUnknown:
		code := result.ErrorCode
		if code == "" {
			code = "unknown_result"
		}
		c.finishFailureAndRequireProbe(attempt, probeKey, code)
		return execution, nil
	case fake.ResultRejected:
		code := result.ErrorCode
		if code == "" {
			code = "rejected"
		}
		c.finishFailure(attempt, code, retryable(code))
		return execution, nil
	case fake.ResultConfirmed:
		snapshot, probeErr := c.backend.Probe(ctx)
		if probeErr != nil {
			c.finishFailureAndRequireProbe(attempt, probeKey, "confirm_probe_failed")
			return execution, fmt.Errorf("confirm probe: %w", probeErr)
		}
		return execution, c.commitConfirmed(attempt, plan, snapshot, probeKey)
	default:
		c.finishFailure(attempt, "invalid_apply_result", false)
		return execution, fmt.Errorf("unsupported apply result %d", result.Kind)
	}
}

// RetryInfrastructure publishes a new epoch only after Critical Audit succeeds.
func (c *Controller) RetryInfrastructure(ctx context.Context) (core.InfrastructureRetryKey, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
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
	c.infrastructureEpoch = next
	key := core.InfrastructureRetryKey{Revision: c.desired.InfrastructureRevision, Epoch: next}
	c.infrastructureStates[key] = core.RetryState{Status: core.ReconcilePending}
	return key, nil
}

// RetryPolicy publishes a new epoch only after Critical Audit succeeds.
func (c *Controller) RetryPolicy(ctx context.Context) (core.PolicyRetryKey, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
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
	c.policyEpoch = next
	key := core.PolicyRetryKey{Revision: c.desired.PolicyRevision, Epoch: next}
	c.policyStates[key] = core.RetryState{Status: core.ReconcilePending}
	return key, nil
}

// RetryTarget publishes a new epoch only for one target after Critical Audit succeeds.
func (c *Controller) RetryTarget(ctx context.Context, target netip.Prefix) (core.TargetRetryKey, error) {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
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
	c.targetEpochs[target] = next
	key := core.TargetRetryKey{Target: target, Generation: intent.Generation, Epoch: next}
	c.targetStates[key] = core.RetryState{Status: core.ReconcilePending}
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

func (c *Controller) beginAttempt(plan fake.OperationPlan) (attemptRef, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.planMatchesCurrentDesiredLocked(plan) {
		return attemptRef{}, ErrStalePlan
	}
	return c.beginAttemptLocked(plan)
}

func (c *Controller) beginAttemptLocked(plan fake.OperationPlan) (attemptRef, error) {
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
	c.putState(ref, state)
	return ref, nil
}

func (c *Controller) beginAfterRequiredProbe(plan fake.OperationPlan, snapshot fake.Snapshot, key pendingProbeKey) (attemptRef, bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.planMatchesCurrentDesiredLocked(plan) {
		return attemptRef{}, false, ErrStalePlan
	}
	_, currentWasPending := c.pendingProbes[key]
	currentRecovered := c.resolvePendingProbesLocked(snapshot, key, plan)
	if currentRecovered {
		return attemptRef{}, true, nil
	}
	attempt, err := c.beginAttemptLocked(plan)
	if err != nil {
		// A premature retry must retain its exact pending Probe requirement. Unrelated
		// pending keys do not consume this plan's retry budget.
		return attemptRef{}, false, err
	}
	if currentWasPending {
		delete(c.pendingProbes, key)
	}
	return attempt, false, nil
}

func (c *Controller) resolvePendingProbesLocked(snapshot fake.Snapshot, currentKey pendingProbeKey, currentPlan fake.OperationPlan) bool {
	currentRecovered := false
	for key, ref := range c.pendingProbes {
		if !c.pendingKeyMatchesCurrentDesiredLocked(key) {
			c.finishStaleLocked(ref)
			delete(c.pendingProbes, key)
			continue
		}
		if !c.snapshotMatchesCurrentDesiredLocked(snapshot, key.domain, key.target) {
			continue
		}
		if key == currentKey {
			c.finishRecoveredByProbeLocked(currentPlan)
			currentRecovered = true
		} else {
			c.finishRecoveredPendingLocked(ref, key)
		}
		delete(c.pendingProbes, key)
	}
	return currentRecovered
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

func (c *Controller) finishRecoveredPendingLocked(ref attemptRef, key pendingProbeKey) {
	state := c.getState(ref)
	state.Status = core.ReconcileConverged
	state.NextAttemptAt = nil
	state.LastErrorCode = ""
	c.putState(ref, state)
	if key.domain == fake.DomainTarget {
		c.confirmedTargets[key.target] = key.targetGeneration
	}
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

func (c *Controller) finishFailure(ref attemptRef, code string, canRetry bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.finishFailureLocked(ref, code, canRetry)
}

func (c *Controller) finishFailureLocked(ref attemptRef, code string, canRetry bool) {
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
	c.putState(ref, state)
}

func (c *Controller) finishFailureAndRequireProbe(ref attemptRef, key pendingProbeKey, code string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.finishFailureLocked(ref, code, true)
	c.pendingProbes[key] = ref
}

func (c *Controller) finishStaleLocked(ref attemptRef) {
	state := c.getState(ref)
	state.Status = core.ReconcilePending
	state.NextAttemptAt = nil
	state.LastErrorCode = "stale_completion"
	c.putState(ref, state)
}

func (c *Controller) finishConvergedLocked(ref attemptRef, plan fake.OperationPlan) {
	state := c.getState(ref)
	state.Status = core.ReconcileConverged
	state.NextAttemptAt = nil
	state.LastErrorCode = ""
	c.putState(ref, state)
	if plan.Domain == fake.DomainTarget {
		c.confirmedTargets[plan.Target] = plan.ExpectedTargetGeneration
	}
}

func (c *Controller) finishRecoveredByProbeLocked(plan fake.OperationPlan) {
	ref, state, ok := c.attemptStateLocked(plan)
	if !ok {
		state = core.RetryState{Status: core.ReconcilePending}
	}
	state.Status = core.ReconcileConverged
	state.NextAttemptAt = nil
	state.LastErrorCode = ""
	c.putState(ref, state)
	if plan.Domain == fake.DomainTarget {
		c.confirmedTargets[plan.Target] = plan.ExpectedTargetGeneration
	}
}

func (c *Controller) commitConfirmed(ref attemptRef, plan fake.OperationPlan, snapshot fake.Snapshot, key pendingProbeKey) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	// The current fence, observed physical postcondition, and durable in-memory writeback are one
	// state transition. SetDesiredSnapshot cannot publish a new fence between these checks.
	if !c.planMatchesCurrentDesiredLocked(plan) {
		c.finishStaleLocked(ref)
		return ErrStaleCompletion
	}
	if !c.snapshotMatchesCurrentDesiredLocked(snapshot, plan.Domain, plan.Target) {
		c.finishFailureLocked(ref, "postcondition_mismatch", true)
		return nil
	}
	c.finishConvergedLocked(ref, plan)
	delete(c.pendingProbes, key)
	return nil
}

func (c *Controller) markInvalidPlan(plan fake.OperationPlan) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.hasDesired {
		return
	}
	ref, state, ok := c.attemptStateLocked(plan)
	if ref.domain != fake.DomainInfrastructure && ref.domain != fake.DomainPolicy && ref.domain != fake.DomainTarget {
		return
	}
	if !ok {
		state = core.RetryState{}
	}
	state.Status = core.ReconcileDegraded
	state.NextAttemptAt = nil
	state.LastErrorCode = "invalid_plan"
	c.putState(ref, state)
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
		return equalTime(observed.NativeExpiry, intent.EffectiveUntil)
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
