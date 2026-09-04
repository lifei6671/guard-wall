package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

// RetryStateStore is the durable boundary needed to recover retry budgets and
// mandatory Probe barriers after the process reopens the same database.
type RetryStateStore interface {
	LoadReconcileRecovery(context.Context, core.NodeID) (core.ReconcileRecoverySnapshot, error)
	ApplyReconcileTransition(context.Context, core.ReconcileStateTransition) error
	ApplyReconcileRetryTransition(context.Context, core.ReconcileRetryTransition) error
	ReadReconcileRetryTransition(context.Context, core.ReconcileRetryTransition) (core.ReconcileRetryReadback, error)
}

// ObservedStateStore persists the latest authoritative Firewall observation.
// Callers must still perform a fresh Probe after process restart; this cache is
// never itself proof of current physical state.
type ObservedStateStore interface {
	LoadObservedFirewallSnapshot(context.Context, core.NodeID) (core.ObservedFirewallSnapshot, error)
	ApplyObservedFirewallUpdate(context.Context, core.ObservedFirewallUpdate) error
}

// PersistentStateStore is the complete durable boundary owned by Controller.
type PersistentStateStore interface {
	RetryStateStore
	ObservedStateStore
}

// NewPersistentController constructs a controller and hydrates its durable
// retry ledger before any Desired snapshot or external mutation is published.
func NewPersistentController(
	ctx context.Context,
	nodeID core.NodeID,
	backend Backend,
	clock Clock,
	audit CriticalAuditWriter,
	store PersistentStateStore,
) (*Controller, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if nodeID == "" {
		return nil, fmt.Errorf("node id is required")
	}
	if store == nil {
		return nil, fmt.Errorf("retry state store is required")
	}

	controller, err := NewController(backend, clock, audit)
	if err != nil {
		return nil, err
	}
	recovery, err := store.LoadReconcileRecovery(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("load reconcile recovery: %w", err)
	}
	controller.nodeID = nodeID
	controller.store = store
	if err := controller.hydrateRecovery(recovery); err != nil {
		return nil, fmt.Errorf("hydrate reconcile recovery: %w", err)
	}
	observed, err := store.LoadObservedFirewallSnapshot(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("load Observed firewall snapshot: %w", err)
	}
	if observed.NodeID != "" && observed.NodeID != nodeID {
		return nil, fmt.Errorf("Observed firewall snapshot belongs to node %q", observed.NodeID)
	}
	controller.seedObservedClock(observed)
	// Any recovered durable state requires a fresh physical observation. A new,
	// empty database may begin normally; Dispatcher still probes every Desired
	// domain because DesiredPlanProvider returns the complete startup key set.
	controller.startupProbeRequired = len(recovery.States) != 0 ||
		len(recovery.ProbeRequirements) != 0 || observedSnapshotHasState(observed)
	return controller, nil
}

func (c *Controller) hydrateRecovery(recovery core.ReconcileRecoverySnapshot) error {
	for _, persisted := range recovery.States {
		if persisted.NodeID != c.nodeID {
			return fmt.Errorf("retry state belongs to node %q", persisted.NodeID)
		}
		ref, err := attemptRefFromPersistedState(persisted)
		if err != nil {
			return err
		}
		if err := validateRecoveredRetryState(persisted.RetryState); err != nil {
			return fmt.Errorf("validate %s retry state: %w", persistedDomainName(persisted.Domain), err)
		}
		state := cloneRetryState(persisted.RetryState)
		if state.Status == core.ReconcileApplying {
			if state.AttemptCount >= maxMutationAttempts {
				state.Status = core.ReconcileDegraded
				state.NextAttemptAt = nil
			} else {
				next := state.LastAttemptAt.Add(retryBackoff[state.AttemptCount-1])
				state.Status = core.ReconcileRetryWaiting
				state.NextAttemptAt = &next
			}
		}
		c.putState(ref, state)
	}

	for _, requirement := range recovery.ProbeRequirements {
		if requirement.NodeID != c.nodeID {
			return fmt.Errorf("Probe requirement belongs to node %q", requirement.NodeID)
		}
		key, ref, err := pendingProbeFromPersisted(requirement)
		if err != nil {
			return err
		}
		if _, duplicate := c.pendingProbes[key]; duplicate {
			return fmt.Errorf("duplicate Probe requirement for %s", pendingProbeName(key))
		}
		state, exists := c.stateForRef(ref)
		if exists {
			if state.AttemptCount != requirement.AttemptCount {
				return fmt.Errorf("Probe requirement attempt does not match retry ledger for %s", pendingProbeName(key))
			}
			if state.Status != core.ReconcileApplying && state.Status != core.ReconcileRetryWaiting && state.Status != core.ReconcileDegraded {
				return fmt.Errorf("Probe requirement has incompatible retry status for %s", pendingProbeName(key))
			}
		} else {
			// The infrastructure/policy tables and each target row hold only their
			// newest epoch. A later administrator Retry may therefore overwrite the
			// originating ledger row while its physical ambiguity remains unresolved.
			c.putState(ref, core.RetryState{
				Status:       core.ReconcileApplying,
				AttemptCount: requirement.AttemptCount,
			})
			c.syntheticRefs[ref] = struct{}{}
		}
		c.pendingProbes[key] = ref
	}

	for _, persisted := range recovery.States {
		if persisted.RetryState.Status != core.ReconcileApplying {
			continue
		}
		ref, _ := attemptRefFromPersistedState(persisted)
		if !c.hasPendingRef(ref) {
			return fmt.Errorf("applying retry state is missing its Probe requirement")
		}
	}
	return nil
}

func (c *Controller) activateHydratedStateLocked() {
	c.infrastructureEpoch = 0
	for key := range c.infrastructureStates {
		if key.Revision == c.desired.InfrastructureRevision && key.Epoch > c.infrastructureEpoch {
			c.infrastructureEpoch = key.Epoch
		}
	}
	c.policyEpoch = 0
	for key := range c.policyStates {
		if key.Revision == c.desired.PolicyRevision && key.Epoch > c.policyEpoch {
			c.policyEpoch = key.Epoch
		}
	}

	// A retry ledger is not authoritative physical observation. A fresh runtime
	// Probe must establish confirmedTargets; durable Converged alone cannot.
	c.confirmedTargets = make(map[netip.Prefix]core.TargetEnforcementGeneration)
	for target, intent := range c.desiredTargets {
		var epoch core.RetryEpoch
		for key := range c.targetStates {
			if key.Target == target && key.Generation == intent.Generation && key.Epoch > epoch {
				epoch = key.Epoch
			}
		}
		c.targetEpochs[target] = epoch
	}
}

func (c *Controller) persistApplyingStateLocked(
	ctx context.Context,
	ref attemptRef,
	state core.RetryState,
	key pendingProbeKey,
	previousRef attemptRef,
	replacesPending bool,
) error {
	if c.store == nil {
		return nil
	}
	transition := core.ReconcileStateTransition{
		State:       c.persistedState(ref, state),
		UpsertProbe: pointerTo(c.persistedProbe(key, ref, state.AttemptCount)),
	}
	if replacesPending {
		previousState, ok := c.stateForRef(previousRef)
		if !ok {
			return fmt.Errorf("pending Probe retry state is missing")
		}
		transition.DeleteProbe = pointerTo(c.persistedProbe(key, previousRef, previousState.AttemptCount))
	}
	return c.applyTransitionLocked(ctx, transition)
}

func (c *Controller) persistStateLocked(
	ctx context.Context,
	ref attemptRef,
	state core.RetryState,
	upsertKey *pendingProbeKey,
	deleteKey *pendingProbeKey,
) error {
	return c.persistStateWithObservedLocked(ctx, ref, state, upsertKey, deleteKey, nil)
}

func (c *Controller) persistStateWithObservedLocked(
	ctx context.Context,
	ref attemptRef,
	state core.RetryState,
	upsertKey *pendingProbeKey,
	deleteKey *pendingProbeKey,
	observed *core.ObservedFirewallUpdate,
) error {
	if c.store == nil {
		return nil
	}
	transition := core.ReconcileStateTransition{
		State:    c.persistedState(ref, state),
		Observed: observed,
	}
	if upsertKey != nil {
		transition.UpsertProbe = pointerTo(c.persistedProbe(*upsertKey, ref, state.AttemptCount))
	}
	if deleteKey != nil {
		transition.DeleteProbe = pointerTo(c.persistedProbe(*deleteKey, ref, state.AttemptCount))
	}
	return c.applyTransitionLocked(ctx, transition)
}

func (c *Controller) persistStateAndClearProbeLocked(
	ctx context.Context,
	stateRef attemptRef,
	state core.RetryState,
	key pendingProbeKey,
	pendingRef attemptRef,
) error {
	return c.persistStateAndClearProbeWithObservedLocked(ctx, stateRef, state, key, pendingRef, nil)
}

func (c *Controller) persistStateAndClearProbeWithObservedLocked(
	ctx context.Context,
	stateRef attemptRef,
	state core.RetryState,
	key pendingProbeKey,
	pendingRef attemptRef,
	observed *core.ObservedFirewallUpdate,
) error {
	if c.store == nil {
		return nil
	}
	pendingState, ok := c.stateForRef(pendingRef)
	if !ok {
		return fmt.Errorf("pending Probe retry state is missing")
	}
	return c.applyTransitionLocked(ctx, core.ReconcileStateTransition{
		State:       c.persistedState(stateRef, state),
		DeleteProbe: pointerTo(c.persistedProbe(key, pendingRef, pendingState.AttemptCount)),
		Observed:    observed,
	})
}

func (c *Controller) clearProbeLocked(ctx context.Context, key pendingProbeKey, pendingRef attemptRef) error {
	if c.store == nil {
		return nil
	}
	pendingState, ok := c.stateForRef(pendingRef)
	if !ok {
		return fmt.Errorf("pending Probe retry state is missing")
	}
	return c.applyTransitionLocked(ctx, core.ReconcileStateTransition{
		DeleteProbe: pointerTo(c.persistedProbe(key, pendingRef, pendingState.AttemptCount)),
		DeleteOnly:  true,
	})
}

func (c *Controller) applyTransitionLocked(ctx context.Context, transition core.ReconcileStateTransition) error {
	err := c.store.ApplyReconcileTransition(ctx, transition)
	if !errors.Is(err, core.ErrReconcileCommitUnknown) {
		return err
	}
	recovery, loadErr := c.store.LoadReconcileRecovery(ctx, c.nodeID)
	if loadErr != nil {
		c.recoveryReloadNeeded = true
		return errors.Join(err, fmt.Errorf("read back indeterminate reconcile commit: %w", loadErr))
	}
	applied := reconcileTransitionApplied(recovery, transition)
	if transition.Observed != nil {
		observed, observedErr := c.store.LoadObservedFirewallSnapshot(ctx, c.nodeID)
		if observedErr != nil {
			c.recoveryReloadNeeded = true
			return errors.Join(err, fmt.Errorf("read back indeterminate Observed commit: %w", observedErr))
		}
		applied = applied && observedUpdateApplied(observed, *transition.Observed)
	}
	if replaceErr := c.replaceRecoveryLocked(recovery); replaceErr != nil {
		c.recoveryReloadNeeded = true
		return errors.Join(err, fmt.Errorf("replace state after indeterminate reconcile commit: %w", replaceErr))
	}
	c.recoveryReloadNeeded = false
	if applied {
		return nil
	}
	return err
}

func (c *Controller) reloadRecoveryIfNeeded(ctx context.Context) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.recoveryReloadNeeded {
		return nil
	}
	recovery, err := c.store.LoadReconcileRecovery(ctx, c.nodeID)
	if err != nil {
		return fmt.Errorf("reload indeterminate reconcile state: %w", err)
	}
	if err := c.replaceRecoveryLocked(recovery); err != nil {
		return fmt.Errorf("replace indeterminate reconcile state: %w", err)
	}
	c.recoveryReloadNeeded = false
	return nil
}

func (c *Controller) replaceRecoveryLocked(recovery core.ReconcileRecoverySnapshot) error {
	replacement := &Controller{
		nodeID:               c.nodeID,
		infrastructureStates: make(map[core.InfrastructureRetryKey]core.RetryState),
		policyStates:         make(map[core.PolicyRetryKey]core.RetryState),
		targetStates:         make(map[core.TargetRetryKey]core.RetryState),
		confirmedTargets:     make(map[netip.Prefix]core.TargetEnforcementGeneration),
		pendingProbes:        make(map[pendingProbeKey]attemptRef),
		syntheticRefs:        make(map[attemptRef]struct{}),
	}
	if err := replacement.hydrateRecovery(recovery); err != nil {
		return err
	}
	c.infrastructureStates = replacement.infrastructureStates
	c.policyStates = replacement.policyStates
	c.targetStates = replacement.targetStates
	c.pendingProbes = replacement.pendingProbes
	c.syntheticRefs = replacement.syntheticRefs
	c.startupProbeRequired = len(recovery.States) != 0 || len(recovery.ProbeRequirements) != 0
	if c.hasDesired {
		c.activateHydratedStateLocked()
	}
	return nil
}

func reconcileTransitionApplied(recovery core.ReconcileRecoverySnapshot, transition core.ReconcileStateTransition) bool {
	if !transition.DeleteOnly {
		stateFound := false
		for _, state := range recovery.States {
			if samePersistedState(state, transition.State) {
				stateFound = true
				break
			}
		}
		if !stateFound {
			return false
		}
	}
	if transition.UpsertProbe != nil && !recoveryHasProbe(recovery, *transition.UpsertProbe) {
		return false
	}
	if transition.DeleteProbe != nil && recoveryHasProbe(recovery, *transition.DeleteProbe) {
		return false
	}
	return true
}

func samePersistedState(left, right core.PersistedReconcileState) bool {
	return left.NodeID == right.NodeID && left.Domain == right.Domain &&
		left.InfrastructureRevision == right.InfrastructureRevision &&
		left.PolicyRevision == right.PolicyRevision && left.Target == right.Target &&
		left.TargetGeneration == right.TargetGeneration && left.RetryEpoch == right.RetryEpoch &&
		left.RetryState.Status == right.RetryState.Status &&
		left.RetryState.AttemptCount == right.RetryState.AttemptCount &&
		equalPersistedTime(left.RetryState.LastAttemptAt, right.RetryState.LastAttemptAt) &&
		equalPersistedTime(left.RetryState.NextAttemptAt, right.RetryState.NextAttemptAt) &&
		left.RetryState.LastErrorCode == right.RetryState.LastErrorCode
}

func equalPersistedTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.UTC().UnixMicro() == right.UTC().UnixMicro()
}

func recoveryHasProbe(recovery core.ReconcileRecoverySnapshot, expected core.PersistedProbeRequirement) bool {
	for _, probe := range recovery.ProbeRequirements {
		if probe.NodeID == expected.NodeID && probe.Domain == expected.Domain &&
			probe.InfrastructureRevision == expected.InfrastructureRevision &&
			probe.PolicyRevision == expected.PolicyRevision && probe.Target == expected.Target &&
			probe.TargetGeneration == expected.TargetGeneration &&
			probe.SnapshotRevision == expected.SnapshotRevision &&
			probe.FenceSnapshotRevision == expected.FenceSnapshotRevision &&
			probe.RetryEpoch == expected.RetryEpoch && probe.AttemptCount == expected.AttemptCount {
			return true
		}
	}
	return false
}

func (c *Controller) persistedState(ref attemptRef, state core.RetryState) core.PersistedReconcileState {
	persisted := core.PersistedReconcileState{
		NodeID:     c.nodeID,
		Domain:     coreDomain(ref.domain),
		RetryState: cloneRetryState(state),
		UpdatedAt:  c.clock.Now(),
	}
	switch ref.domain {
	case DomainInfrastructure:
		persisted.InfrastructureRevision = ref.infrastructure.Revision
		persisted.RetryEpoch = ref.infrastructure.Epoch
	case DomainPolicy:
		persisted.PolicyRevision = ref.policy.Revision
		persisted.RetryEpoch = ref.policy.Epoch
	case DomainTarget:
		persisted.Target = ref.target.Target
		persisted.TargetGeneration = ref.target.Generation
		persisted.RetryEpoch = ref.target.Epoch
	}
	return persisted
}

func (c *Controller) persistedProbe(key pendingProbeKey, ref attemptRef, attemptCount uint32) core.PersistedProbeRequirement {
	return core.PersistedProbeRequirement{
		NodeID:                 c.nodeID,
		Domain:                 coreDomain(key.domain),
		InfrastructureRevision: key.infrastructureRevision,
		PolicyRevision:         key.policyRevision,
		Target:                 key.target,
		TargetGeneration:       key.targetGeneration,
		SnapshotRevision:       key.snapshotRevision,
		FenceSnapshotRevision:  key.fenceSnapshotRevision,
		RetryEpoch:             retryEpoch(ref),
		AttemptCount:           attemptCount,
		RecordedAt:             c.clock.Now(),
	}
}

func attemptRefFromPersistedState(persisted core.PersistedReconcileState) (attemptRef, error) {
	switch persisted.Domain {
	case core.ReconcileDomainInfrastructure:
		if persisted.InfrastructureRevision == 0 {
			return attemptRef{}, fmt.Errorf("infrastructure retry state has zero revision")
		}
		return attemptRef{domain: DomainInfrastructure, infrastructure: core.InfrastructureRetryKey{
			Revision: persisted.InfrastructureRevision,
			Epoch:    persisted.RetryEpoch,
		}}, nil
	case core.ReconcileDomainPolicy:
		if persisted.PolicyRevision == 0 {
			return attemptRef{}, fmt.Errorf("policy retry state has zero revision")
		}
		return attemptRef{domain: DomainPolicy, policy: core.PolicyRetryKey{
			Revision: persisted.PolicyRevision,
			Epoch:    persisted.RetryEpoch,
		}}, nil
	case core.ReconcileDomainTarget:
		if !persisted.Target.IsValid() || persisted.Target != persisted.Target.Masked() || persisted.TargetGeneration == 0 {
			return attemptRef{}, fmt.Errorf("target retry state has invalid key")
		}
		return attemptRef{domain: DomainTarget, target: core.TargetRetryKey{
			Target:     persisted.Target,
			Generation: persisted.TargetGeneration,
			Epoch:      persisted.RetryEpoch,
		}}, nil
	default:
		return attemptRef{}, fmt.Errorf("retry state has invalid domain %d", persisted.Domain)
	}
}

func pendingProbeFromPersisted(requirement core.PersistedProbeRequirement) (pendingProbeKey, attemptRef, error) {
	state := core.PersistedReconcileState{
		NodeID:                 requirement.NodeID,
		Domain:                 requirement.Domain,
		InfrastructureRevision: requirement.InfrastructureRevision,
		PolicyRevision:         requirement.PolicyRevision,
		Target:                 requirement.Target,
		TargetGeneration:       requirement.TargetGeneration,
		RetryEpoch:             requirement.RetryEpoch,
	}
	ref, err := attemptRefFromPersistedState(state)
	if err != nil {
		return pendingProbeKey{}, attemptRef{}, fmt.Errorf("invalid Probe requirement: %w", err)
	}
	if requirement.AttemptCount == 0 || requirement.AttemptCount > maxMutationAttempts {
		return pendingProbeKey{}, attemptRef{}, fmt.Errorf("Probe requirement has invalid attempt count")
	}
	key := pendingProbeKey{
		domain:                 fakeDomain(requirement.Domain),
		target:                 requirement.Target,
		infrastructureRevision: requirement.InfrastructureRevision,
		policyRevision:         requirement.PolicyRevision,
		targetGeneration:       requirement.TargetGeneration,
		snapshotRevision:       requirement.SnapshotRevision,
		fenceSnapshotRevision:  requirement.FenceSnapshotRevision,
	}
	return key, ref, nil
}

func validateRecoveredRetryState(state core.RetryState) error {
	if state.Status < core.ReconcilePending || state.Status > core.ReconcileDegraded {
		return fmt.Errorf("invalid status %d", state.Status)
	}
	if state.AttemptCount > maxMutationAttempts {
		return fmt.Errorf("attempt count exceeds %d", maxMutationAttempts)
	}
	if state.AttemptCount == 0 && state.LastAttemptAt != nil {
		return fmt.Errorf("zero attempts have a last-attempt time")
	}
	if state.Status == core.ReconcileApplying && (state.AttemptCount == 0 || state.LastAttemptAt == nil || state.NextAttemptAt != nil) {
		return fmt.Errorf("applying state has inconsistent attempt fields")
	}
	if state.Status == core.ReconcileRetryWaiting && (state.AttemptCount == 0 || state.LastAttemptAt == nil || state.NextAttemptAt == nil) {
		return fmt.Errorf("retry-waiting state has inconsistent attempt fields")
	}
	if state.NextAttemptAt != nil && state.LastAttemptAt != nil && !state.NextAttemptAt.After(*state.LastAttemptAt) {
		return fmt.Errorf("next-attempt time is not after last attempt")
	}
	if state.Status == core.ReconcileConverged || state.Status == core.ReconcileDegraded {
		if state.NextAttemptAt != nil {
			return fmt.Errorf("terminal state has a next-attempt time")
		}
	}
	return nil
}

func (c *Controller) stateForRef(ref attemptRef) (core.RetryState, bool) {
	switch ref.domain {
	case DomainInfrastructure:
		state, ok := c.infrastructureStates[ref.infrastructure]
		return state, ok
	case DomainPolicy:
		state, ok := c.policyStates[ref.policy]
		return state, ok
	case DomainTarget:
		state, ok := c.targetStates[ref.target]
		return state, ok
	default:
		return core.RetryState{}, false
	}
}

func (c *Controller) currentStateForPendingKeyLocked(key pendingProbeKey) (attemptRef, core.RetryState, bool) {
	switch key.domain {
	case DomainInfrastructure:
		ref := attemptRef{domain: DomainInfrastructure, infrastructure: core.InfrastructureRetryKey{
			Revision: c.desired.InfrastructureRevision,
			Epoch:    c.infrastructureEpoch,
		}}
		state, ok := c.stateForRef(ref)
		return ref, state, ok
	case DomainPolicy:
		ref := attemptRef{domain: DomainPolicy, policy: core.PolicyRetryKey{
			Revision: c.desired.PolicyRevision,
			Epoch:    c.policyEpoch,
		}}
		state, ok := c.stateForRef(ref)
		return ref, state, ok
	case DomainTarget:
		intent, desired := c.desiredTargets[key.target]
		if !desired {
			return attemptRef{}, core.RetryState{}, false
		}
		ref := attemptRef{domain: DomainTarget, target: core.TargetRetryKey{
			Target:     key.target,
			Generation: intent.Generation,
			Epoch:      c.targetEpochs[key.target],
		}}
		state, ok := c.stateForRef(ref)
		return ref, state, ok
	default:
		return attemptRef{}, core.RetryState{}, false
	}
}

func (c *Controller) hasPendingRef(ref attemptRef) bool {
	for _, pendingRef := range c.pendingProbes {
		if pendingRef == ref {
			return true
		}
	}
	return false
}

func coreDomain(domain Domain) core.ReconcileDomain {
	switch domain {
	case DomainInfrastructure:
		return core.ReconcileDomainInfrastructure
	case DomainPolicy:
		return core.ReconcileDomainPolicy
	case DomainTarget:
		return core.ReconcileDomainTarget
	default:
		return 0
	}
}

func fakeDomain(domain core.ReconcileDomain) Domain {
	switch domain {
	case core.ReconcileDomainInfrastructure:
		return DomainInfrastructure
	case core.ReconcileDomainPolicy:
		return DomainPolicy
	case core.ReconcileDomainTarget:
		return DomainTarget
	default:
		return 0
	}
}

func retryEpoch(ref attemptRef) core.RetryEpoch {
	switch ref.domain {
	case DomainInfrastructure:
		return ref.infrastructure.Epoch
	case DomainPolicy:
		return ref.policy.Epoch
	case DomainTarget:
		return ref.target.Epoch
	default:
		return 0
	}
}

func persistedDomainName(domain core.ReconcileDomain) string {
	switch domain {
	case core.ReconcileDomainInfrastructure:
		return "infrastructure"
	case core.ReconcileDomainPolicy:
		return "policy"
	case core.ReconcileDomainTarget:
		return "target"
	default:
		return "unknown"
	}
}

func pendingProbeName(key pendingProbeKey) string {
	switch key.domain {
	case DomainInfrastructure:
		return fmt.Sprintf("infrastructure revision %d", key.infrastructureRevision)
	case DomainPolicy:
		return fmt.Sprintf("policy revision %d", key.policyRevision)
	case DomainTarget:
		return fmt.Sprintf("target %s generation %d", key.target, key.targetGeneration)
	default:
		return "unknown domain"
	}
}

func pointerTo[T any](value T) *T { return &value }
