package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/enforcement"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

func TestRetryBudgetsAreIsolatedByDomain(t *testing.T) {
	clock := newManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)

	if err := backend.QueueOutcome(fake.DomainInfrastructure, fake.QueuedOutcome{Kind: fake.ResultRejected, ErrorCode: "transient"}); err != nil {
		t.Fatal(err)
	}
	result, err := controller.Execute(context.Background(), infrastructurePlan(desired))
	if err != nil || result.Apply.Kind != fake.ResultRejected {
		t.Fatalf("infrastructure failure: result=%+v err=%v", result, err)
	}
	_, infrastructure, ok := controller.InfrastructureState()
	if !ok || infrastructure.AttemptCount != 1 || infrastructure.Status != core.ReconcileRetryWaiting {
		t.Fatalf("unexpected infrastructure ledger: %+v", infrastructure)
	}
	if _, _, ok := controller.PolicyState(); ok {
		t.Fatal("infrastructure failure created policy attempt")
	}
	if _, _, ok := controller.TargetState(target); ok {
		t.Fatal("infrastructure failure created target attempt")
	}

	result, err = controller.Execute(context.Background(), targetPlan(desired, target))
	if err != nil || result.Apply.Kind != fake.ResultConfirmed {
		t.Fatalf("target apply: result=%+v err=%v", result, err)
	}
	_, targetState, ok := controller.TargetState(target)
	if !ok || targetState.AttemptCount != 1 || targetState.Status != core.ReconcileConverged {
		t.Fatalf("unexpected target ledger: %+v", targetState)
	}
	_, infrastructureAfter, _ := controller.InfrastructureState()
	if infrastructureAfter.AttemptCount != 1 {
		t.Fatal("target success changed infrastructure budget")
	}
}

func TestInfrastructureAndPolicyRequirePhysicalPostcondition(t *testing.T) {
	controller := newTestController(t, fake.NewBackend(), newManualClock(), &memoryAudit{})
	desired := desiredSnapshot()
	setDesired(t, controller, desired)

	if _, err := controller.Execute(context.Background(), infrastructurePlan(desired)); err != nil {
		t.Fatal(err)
	}
	_, infrastructure, ok := controller.InfrastructureState()
	if !ok || infrastructure.Status != core.ReconcileConverged {
		t.Fatalf("infrastructure did not confirm physical postcondition: %+v", infrastructure)
	}
	if _, err := controller.Execute(context.Background(), policyPlan(desired)); err != nil {
		t.Fatal(err)
	}
	_, policy, ok := controller.PolicyState()
	if !ok || policy.Status != core.ReconcileConverged {
		t.Fatalf("policy did not confirm physical postcondition: %+v", policy)
	}
}

func TestExpiredPresentTargetIsFencedBeforeProbeOrAttempt(t *testing.T) {
	tests := []struct {
		name        string
		expiryDelta time.Duration
		wantExpired bool
	}{
		{name: "before expiry", expiryDelta: time.Nanosecond},
		{name: "at expiry", wantExpired: true},
		{name: "after expiry", expiryDelta: -time.Nanosecond, wantExpired: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := newManualClock()
			backend := fake.NewBackend()
			controller := newTestController(t, backend, clock, &memoryAudit{})
			target := netip.MustParsePrefix("192.0.2.4/32")
			intent := targetIntent(target, 1)
			expiresAt := clock.Now().Add(test.expiryDelta)
			intent.EffectiveUntil = &expiresAt
			intent.TimeoutMode = core.TimeoutNative
			desired := desiredSnapshot(intent)
			setDesired(t, controller, desired)

			result, err := controller.Execute(context.Background(), targetPlan(desired, target))
			if test.wantExpired {
				if !errors.Is(err, errTargetIntentExpired) {
					t.Fatalf("Execute() error=%v, want expired target sentinel", err)
				}
				if result != (ExecutionResult{}) {
					t.Fatalf("Execute() result=%+v, want empty result", result)
				}
				probes, applies := backend.Counts()
				if probes != 0 || applies != 0 {
					t.Fatalf("expired target crossed mutation boundary: probes=%d applies=%d", probes, applies)
				}
				if _, _, exists := controller.TargetState(target); exists {
					t.Fatal("expired target consumed retry budget")
				}
				return
			}

			if err != nil || result.Apply.Kind != fake.ResultConfirmed {
				t.Fatalf("Execute() before expiry: result=%+v err=%v", result, err)
			}
			probes, applies := backend.Counts()
			if probes != 1 || applies != 1 {
				t.Fatalf("allowed target calls: probes=%d applies=%d, want 1/1", probes, applies)
			}
			_, state, exists := controller.TargetState(target)
			if !exists || state.AttemptCount != 1 || state.Status != core.ReconcileConverged {
				t.Fatalf("allowed target retry state=%+v exists=%t", state, exists)
			}
		})
	}
}

func TestExpiredStaleTargetReturnsStalePlan(t *testing.T) {
	clock := newManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	current := desiredSnapshot(targetIntent(target, 2))
	setDesired(t, controller, current)
	staleIntent := targetIntent(target, 1)
	expiresAt := clock.Now()
	staleIntent.EffectiveUntil = &expiresAt
	staleIntent.TimeoutMode = core.TimeoutNative
	stale := desiredSnapshot(staleIntent)

	if _, err := controller.Execute(context.Background(), targetPlan(stale, target)); !errors.Is(err, ErrStalePlan) {
		t.Fatalf("Execute() error=%v, want stale Plan", err)
	}
	probes, applies := backend.Counts()
	if probes != 0 || applies != 0 {
		t.Fatalf("stale expired target crossed mutation boundary: probes=%d applies=%d", probes, applies)
	}
}

func TestTargetExpiringAfterAttemptPersistenceDoesNotApply(t *testing.T) {
	clock := newExpirationStepClock(4)
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	intent := targetIntent(target, 1)
	expiresAt := clock.base.Add(time.Second)
	intent.EffectiveUntil = &expiresAt
	intent.TimeoutMode = core.TimeoutNative
	desired := desiredSnapshot(intent)
	setDesired(t, controller, desired)

	if _, err := controller.Execute(context.Background(), targetPlan(desired, target)); !errors.Is(err, errTargetIntentExpired) {
		t.Fatalf("Execute() error=%v, want expired target sentinel", err)
	}
	probes, applies := backend.Counts()
	if probes != 0 || applies != 0 {
		t.Fatalf("post-persistence expiry crossed mutation boundary: probes=%d applies=%d", probes, applies)
	}
	_, state, exists := controller.TargetState(target)
	if !exists || state.AttemptCount != 1 || state.Status != core.ReconcileDegraded || state.LastErrorCode != "expired_before_apply" {
		t.Fatalf("post-persistence expiry state=%+v exists=%t", state, exists)
	}
	if controller.ProbeRequired() {
		t.Fatal("expiry before Apply retained a false Probe requirement")
	}
}

func TestConfirmedWithoutPhysicalPostconditionDoesNotConverge(t *testing.T) {
	backend := &confirmedWithoutMutationBackend{Backend: fake.NewBackend()}
	controller := newTestController(t, backend, newManualClock(), &memoryAudit{})
	desired := desiredSnapshot()
	setDesired(t, controller, desired)
	result, err := controller.Execute(context.Background(), infrastructurePlan(desired))
	if err != nil || result.Apply.Kind != fake.ResultConfirmed {
		t.Fatalf("lying backend result=%+v err=%v", result, err)
	}
	_, state, ok := controller.InfrastructureState()
	if !ok || state.Status != core.ReconcileRetryWaiting || state.LastErrorCode != "postcondition_mismatch" {
		t.Fatalf("missing postcondition was accepted: %+v", state)
	}
}

func TestPolicyOwnershipConflictDegradesOnlyPolicyDomain(t *testing.T) {
	backend := fake.NewBackend()
	controller := newTestController(t, backend, newManualClock(), &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)

	if err := backend.QueueOutcome(fake.DomainPolicy, fake.QueuedOutcome{Kind: fake.ResultRejected, ErrorCode: "ownership_conflict"}); err != nil {
		t.Fatal(err)
	}
	result, err := controller.Execute(context.Background(), policyPlan(desired))
	if err != nil || result.Apply.Kind != fake.ResultRejected {
		t.Fatalf("policy conflict: result=%+v err=%v", result, err)
	}
	_, policy, ok := controller.PolicyState()
	if !ok || policy.AttemptCount != 1 || policy.Status != core.ReconcileDegraded {
		t.Fatalf("unexpected policy ledger: %+v", policy)
	}
	if _, _, ok := controller.InfrastructureState(); ok {
		t.Fatal("policy conflict created infrastructure attempt")
	}
	if _, _, ok := controller.TargetState(target); ok {
		t.Fatal("policy conflict consumed target budget")
	}
}

func TestUnknownAppliedResultIsConfirmedByProbeWithoutAnotherMutation(t *testing.T) {
	clock := newManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)

	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, Mutate: true, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	result, err := controller.Execute(context.Background(), plan)
	if err != nil || result.Apply.Kind != fake.ResultUnknown {
		t.Fatalf("unknown apply: result=%+v err=%v", result, err)
	}
	result, err = controller.Execute(context.Background(), plan)
	if err != nil || !result.RecoveredByProbe {
		t.Fatalf("probe recovery: result=%+v err=%v", result, err)
	}
	probes, applies := backend.Counts()
	if probes != 1 || applies != 1 {
		t.Fatalf("already-applied unknown was replayed: probes=%d applies=%d", probes, applies)
	}
	_, state, _ := controller.TargetState(target)
	if state.AttemptCount != 1 || state.Status != core.ReconcileConverged {
		t.Fatalf("probe recovery changed attempt accounting: %+v", state)
	}
}

func TestUnknownNotAppliedResultBuildsFreshPlanAfterProbe(t *testing.T) {
	clock := newManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)

	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	result, err := controller.Execute(context.Background(), plan)
	if err != nil || result.Apply.Kind != fake.ResultConfirmed {
		t.Fatalf("fresh retry: result=%+v err=%v", result, err)
	}
	if result.Apply.Digest == plan.Digest {
		t.Fatal("retry plan was not rebound to the fresh physical snapshot")
	}
	probes, applies := backend.Counts()
	if probes != 2 || applies != 2 {
		t.Fatalf("unexpected retry sequence: probes=%d applies=%d", probes, applies)
	}
}

func TestTargetExpiringDuringRequiredProbeDoesNotBeginAnotherAttempt(t *testing.T) {
	clock := newManualClock()
	backend := newBlockingProbeBackend(fake.NewBackend())
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	intent := targetIntent(target, 1)
	expiresAt := clock.Now().Add(2 * time.Second)
	intent.EffectiveUntil = &expiresAt
	intent.TimeoutMode = core.TimeoutNative
	desired := desiredSnapshot(intent)
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)
	if err := backend.Backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	done := make(chan error, 1)
	go func() {
		_, err := controller.Execute(context.Background(), plan)
		done <- err
	}()
	<-backend.entered
	clock.Advance(time.Second)
	close(backend.release)
	if err := <-done; !errors.Is(err, errTargetIntentExpired) {
		t.Fatalf("Execute() error=%v, want expired target sentinel", err)
	}
	probes, applies := backend.Backend.Counts()
	if probes != 1 || applies != 1 {
		t.Fatalf("expiry during Probe calls: probes=%d applies=%d, want 1/1", probes, applies)
	}
	_, state, exists := controller.TargetState(target)
	if !exists || state.AttemptCount != 1 || state.Status != core.ReconcileRetryWaiting {
		t.Fatalf("expiry during Probe changed retry budget: state=%+v exists=%t", state, exists)
	}
	if !controller.ProbeRequired() {
		t.Fatal("expiry during Probe cleared the existing ambiguous-result barrier")
	}
}

func TestEarlyRetryDoesNotConsumeRequiredProbe(t *testing.T) {
	clock := newManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)
	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(context.Background(), plan); !errors.Is(err, ErrRetryNotReady) {
		t.Fatalf("early retry error = %v", err)
	}
	if !controller.ProbeRequired() {
		t.Fatal("early retry cleared the required probe")
	}
	probes, _ := backend.Counts()
	if probes != 1 {
		t.Fatalf("early retry must still inspect an ambiguous physical result: %d", probes)
	}
}

func TestUnknownProbeRequirementIsScopedToItsDomainAndFence(t *testing.T) {
	clock := newManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	desired := desiredSnapshot()
	setDesired(t, controller, desired)
	if err := backend.QueueOutcome(fake.DomainInfrastructure, fake.QueuedOutcome{Kind: fake.ResultUnknown, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(context.Background(), infrastructurePlan(desired)); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(context.Background(), policyPlan(desired)); err != nil {
		t.Fatal(err)
	}
	probes, applies := backend.Counts()
	if probes != 2 || applies != 2 {
		t.Fatalf("unrelated policy did not honor global Probe barrier: probes=%d applies=%d", probes, applies)
	}
	if !controller.ProbeRequired() {
		t.Fatal("unrelated policy incorrectly cleared unresolved infrastructure Unknown")
	}
	_, infrastructure, ok := controller.InfrastructureState()
	if !ok || infrastructure.Status != core.ReconcileRetryWaiting || infrastructure.AttemptCount != 1 {
		t.Fatalf("unrelated policy changed infrastructure pending state: %+v", infrastructure)
	}
}

func TestAbsentProbeRejectsExplicitObservationWithResidualAttributes(t *testing.T) {
	clock := newManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	absent := targetIntent(target, 1)
	absent.BanMembership = core.BanAbsent
	desired := desiredSnapshot(absent)
	setDesired(t, controller, desired)
	if err := backend.SetPhysicalTarget(core.PhysicalTargetObserved{
		CanonicalTarget: target,
		BanMembership:   core.ObservedMembershipAbsent,
		Scopes:          core.ScopeInput,
	}); err != nil {
		t.Fatal(err)
	}
	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(context.Background(), targetPlan(desired, target)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	result, err := controller.Execute(context.Background(), targetPlan(desired, target))
	if err != nil {
		t.Fatal(err)
	}
	if result.RecoveredByProbe {
		t.Fatal("explicit Absent with residual attributes was accepted as converged")
	}
	_, applies := backend.Counts()
	if applies != 2 {
		t.Fatalf("residual Absent skipped corrective mutation: applies=%d", applies)
	}
}

func TestDesiredAdvanceDuringConfirmationProbeCannotCommitStaleFence(t *testing.T) {
	base := fake.NewBackend()
	backend := newBlockingProbeBackend(base)
	controller := newTestController(t, backend, newManualClock(), &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	done := make(chan error, 1)
	go func() {
		_, err := controller.Execute(context.Background(), targetPlan(desired, target))
		done <- err
	}()
	<-backend.entered
	next := desiredSnapshot(targetIntentWithScope(target, 2, core.ScopeForward))
	next.SnapshotRevision = 2
	setDesired(t, controller, next)
	close(backend.release)
	if err := <-done; !errors.Is(err, ErrStaleCompletion) {
		t.Fatalf("completion error = %v, want stale completion", err)
	}
	if generation, ok := controller.ConfirmedTarget(target); ok {
		t.Fatalf("stale final commit published generation %d", generation)
	}
}

func TestStaleTargetCompletionCannotOverwriteNewGeneration(t *testing.T) {
	base := fake.NewBackend()
	backend := newBlockingBackend(base)
	controller := newTestController(t, backend, newManualClock(), &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)

	done := make(chan error, 1)
	go func() {
		_, err := controller.Execute(context.Background(), targetPlan(desired, target))
		done <- err
	}()
	<-backend.entered
	next := desiredSnapshot(targetIntentWithScope(target, 2, core.ScopeForward))
	next.SnapshotRevision = 2
	if err := controller.SetDesiredSnapshot(next); err != nil {
		t.Fatal(err)
	}
	close(backend.release)
	if err := <-done; !errors.Is(err, ErrStaleCompletion) {
		t.Fatalf("completion error = %v, want stale completion", err)
	}
	if generation, ok := controller.ConfirmedTarget(target); ok {
		t.Fatalf("stale completion confirmed generation %d", generation)
	}
}

func TestUnrelatedSnapshotRevisionDoesNotInvalidateTargetFence(t *testing.T) {
	base := fake.NewBackend()
	backend := newBlockingBackend(base)
	controller := newTestController(t, backend, newManualClock(), &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)

	done := make(chan error, 1)
	go func() {
		_, err := controller.Execute(context.Background(), targetPlan(desired, target))
		done <- err
	}()
	<-backend.entered
	next := desired
	next.Targets = append([]core.NormalizedTargetEnforcementIntent(nil), desired.Targets...)
	next.PolicyRevision = 2
	next.SnapshotRevision = 2
	next.Policy.RelationDigest = "policy-unrelated-v2"
	if err := controller.SetDesiredSnapshot(next); err != nil {
		t.Fatal(err)
	}
	close(backend.release)
	if err := <-done; err != nil {
		t.Fatalf("unrelated snapshot revision rejected target completion: %v", err)
	}
	if generation, ok := controller.ConfirmedTarget(target); !ok || generation != 1 {
		t.Fatalf("confirmed generation = %d,%v; want 1,true", generation, ok)
	}
}

func TestPhysicalTargetMatchIncludesSafetyGraceExpiry(t *testing.T) {
	target := netip.MustParsePrefix("192.0.2.41/32")
	effectiveUntil := time.Unix(1_700_000_000, 0).UTC()
	intent := targetIntent(target, 1)
	intent.EffectiveUntil = &effectiveUntil
	intent.TimeoutMode = core.TimeoutNative
	observed := core.PhysicalTargetObserved{
		CanonicalTarget: target, BanMembership: core.ObservedMembershipPresent,
		PolicyCoverage: core.ObservedPolicyNone, TimeoutMode: core.TimeoutNative,
		NativeExpiry: enforcement.NativeExpiryForIntent(intent), Scopes: intent.Scopes,
		AddressFamily: intent.AddressFamily, OwnerVersion: intent.BackendAttributesDigest,
	}
	if !physicalTargetMatches(map[netip.Prefix]core.PhysicalTargetObserved{target: observed}, intent) {
		t.Fatal("SafetyGrace-adjusted native expiry did not converge")
	}
	stale := effectiveUntil
	observed.NativeExpiry = &stale
	if physicalTargetMatches(map[netip.Prefix]core.PhysicalTargetObserved{target: observed}, intent) {
		t.Fatal("unadjusted EffectiveUntil was accepted as native expiry")
	}
}

func TestMutationExecutorSerializesConcurrentPlans(t *testing.T) {
	backend := &trackingBackend{Backend: fake.NewBackend()}
	controller := newTestController(t, backend, newManualClock(), &memoryAudit{})
	targetA := netip.MustParsePrefix("192.0.2.4/32")
	targetB := netip.MustParsePrefix("192.0.2.5/32")
	desired := desiredSnapshot(targetIntent(targetA, 1), targetIntent(targetB, 1))
	setDesired(t, controller, desired)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, target := range []netip.Prefix{targetA, targetB} {
		wait.Add(1)
		go func(target netip.Prefix) {
			defer wait.Done()
			<-start
			_, err := controller.Execute(context.Background(), targetPlan(desired, target))
			errs <- err
		}(target)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if backend.maxActive.Load() != 1 {
		t.Fatalf("maximum concurrent mutations = %d, want 1", backend.maxActive.Load())
	}
}

func TestRetryBudgetIsBoundedAndAuditFailureDoesNotPublishEpoch(t *testing.T) {
	clock := newManualClock()
	backend := fake.NewBackend()
	audit := &memoryAudit{}
	controller := newTestController(t, backend, clock, audit)
	desired := desiredSnapshot()
	setDesired(t, controller, desired)
	plan := infrastructurePlan(desired)

	for attempt := uint32(1); attempt <= maxMutationAttempts; attempt++ {
		if err := backend.QueueOutcome(fake.DomainInfrastructure, fake.QueuedOutcome{Kind: fake.ResultRejected, ErrorCode: "transient"}); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Execute(context.Background(), plan); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if attempt < maxMutationAttempts {
			clock.Advance(retryBackoff[attempt-1])
		}
	}
	oldKey, state, ok := controller.InfrastructureState()
	if !ok || state.Status != core.ReconcileDegraded || state.AttemptCount != maxMutationAttempts {
		t.Fatalf("unexpected exhausted state: key=%+v state=%+v", oldKey, state)
	}

	audit.FailNext(errors.New("audit unavailable"))
	if _, err := controller.RetryInfrastructure(context.Background()); err == nil {
		t.Fatal("expected audit failure")
	}
	unchangedKey, unchangedState, _ := controller.InfrastructureState()
	if unchangedKey != oldKey || unchangedState.Status != core.ReconcileDegraded {
		t.Fatalf("audit failure published retry epoch: key=%+v state=%+v", unchangedKey, unchangedState)
	}

	newKey, err := controller.RetryInfrastructure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if newKey.Epoch != oldKey.Epoch+1 || audit.Count() != 1 {
		t.Fatalf("retry/audit mismatch: old=%+v new=%+v audit=%d", oldKey, newKey, audit.Count())
	}
}

func TestInvalidPlanIsNonRetryableAndNeverApplied(t *testing.T) {
	backend := fake.NewBackend()
	controller := newTestController(t, backend, newManualClock(), &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)
	plan.Digest = "tampered"

	result, err := controller.Execute(context.Background(), plan)
	if !errors.Is(err, ErrInvalidPlan) || result.Apply.ErrorCode != "invalid_plan" {
		t.Fatalf("invalid plan result=%+v err=%v", result, err)
	}
	_, state, ok := controller.TargetState(target)
	if !ok || state.Status != core.ReconcileDegraded || state.AttemptCount != 0 {
		t.Fatalf("invalid plan was not stable non-retryable: %+v", state)
	}
	_, applies := backend.Counts()
	if applies != 0 {
		t.Fatalf("invalid plan reached backend mutation: %d", applies)
	}
}

func TestValidDigestCannotBindPlanToNonAuthoritativeIntent(t *testing.T) {
	backend := fake.NewBackend()
	controller := newTestController(t, backend, newManualClock(), &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)
	plan.DesiredTarget.Scopes = core.ScopeForward
	plan.Digest = fake.PlanDigest(plan)

	if _, err := controller.Execute(context.Background(), plan); !errors.Is(err, ErrStalePlan) {
		t.Fatalf("non-authoritative payload error = %v", err)
	}
	_, applies := backend.Counts()
	if applies != 0 {
		t.Fatalf("non-authoritative payload reached mutation: %d", applies)
	}
}

func TestDesiredSnapshotRejectsVersionAndGenerationRegression(t *testing.T) {
	controller := newTestController(t, fake.NewBackend(), newManualClock(), &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 2))
	desired.SnapshotRevision = 2
	setDesired(t, controller, desired)

	regressed := desiredSnapshot(targetIntent(target, 1))
	if err := controller.SetDesiredSnapshot(regressed); err == nil {
		t.Fatal("generation/revision regression was accepted")
	}
	sameGenerationChangedMeaning := desired
	sameGenerationChangedMeaning.Targets = []core.NormalizedTargetEnforcementIntent{targetIntentWithScope(target, 2, core.ScopeForward)}
	sameGenerationChangedMeaning.SnapshotRevision = 3
	if err := controller.SetDesiredSnapshot(sameGenerationChangedMeaning); err == nil {
		t.Fatal("semantic change without generation advance was accepted")
	}
	removed := desired
	removed.Targets = nil
	removed.SnapshotRevision = 3
	if err := controller.SetDesiredSnapshot(removed); err == nil {
		t.Fatal("target disappearance without absent generation was accepted")
	}
}

func TestSetDesiredSnapshotClonesCompletePolicyPayload(t *testing.T) {
	controller := newTestController(t, fake.NewBackend(), newManualClock(), &memoryAudit{})
	policy, err := core.NewManagedPolicyIntent(
		[]netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")},
		[]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
	)
	if err != nil {
		t.Fatal(err)
	}
	desired := desiredSnapshot()
	desired.Policy = policy
	setDesired(t, controller, desired)

	desired.Policy.Allowlist[0] = netip.MustParsePrefix("203.0.113.0/24")
	if got := controller.desired.Policy.Allowlist[0]; got != netip.MustParsePrefix("198.51.100.0/24") {
		t.Fatalf("published policy changed through caller slice: %s", got)
	}
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

type expirationStepClock struct {
	mu           sync.Mutex
	base         time.Time
	calls        int
	expireAtCall int
}

func newExpirationStepClock(expireAtCall int) *expirationStepClock {
	return &expirationStepClock{
		base:         time.Unix(1_700_000_000, 0).UTC(),
		expireAtCall: expireAtCall,
	}
}

func (c *expirationStepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls >= c.expireAtCall {
		return c.base.Add(time.Second)
	}
	return c.base
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1_700_000_000, 0).UTC()}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

type memoryAudit struct {
	mu       sync.Mutex
	events   []CriticalAuditEvent
	nextFail error
}

func (a *memoryAudit) AppendCriticalAudit(_ context.Context, event CriticalAuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.nextFail != nil {
		err := a.nextFail
		a.nextFail = nil
		return err
	}
	a.events = append(a.events, event)
	return nil
}

func (a *memoryAudit) FailNext(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextFail = err
}

func (a *memoryAudit) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.events)
}

type blockingBackend struct {
	*fake.Backend
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingProbeBackend struct {
	*fake.Backend
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingProbeBackend(backend *fake.Backend) *blockingProbeBackend {
	return &blockingProbeBackend{Backend: backend, entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingProbeBackend) Probe(ctx context.Context) (fake.Snapshot, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return fake.Snapshot{}, ctx.Err()
	}
	return b.Backend.Probe(ctx)
}

func newBlockingBackend(backend *fake.Backend) *blockingBackend {
	return &blockingBackend{Backend: backend, entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingBackend) Apply(ctx context.Context, plan fake.OperationPlan) (fake.ApplyResult, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return fake.ApplyResult{}, ctx.Err()
	}
	return b.Backend.Apply(ctx, plan)
}

type trackingBackend struct {
	*fake.Backend
	active    atomic.Int32
	maxActive atomic.Int32
}

type confirmedWithoutMutationBackend struct {
	*fake.Backend
}

func (b *confirmedWithoutMutationBackend) Apply(_ context.Context, plan fake.OperationPlan) (fake.ApplyResult, error) {
	return fake.ApplyResult{Kind: fake.ResultConfirmed, Domain: plan.Domain, Target: plan.Target, Digest: plan.Digest}, nil
}

func (b *trackingBackend) Apply(ctx context.Context, plan fake.OperationPlan) (fake.ApplyResult, error) {
	active := b.active.Add(1)
	for {
		maximum := b.maxActive.Load()
		if active <= maximum || b.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	result, err := b.Backend.Apply(ctx, plan)
	b.active.Add(-1)
	return result, err
}

func newTestController(t *testing.T, backend Backend, clock Clock, audit CriticalAuditWriter) *Controller {
	t.Helper()
	controller, err := NewController(backend, clock, audit)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func setDesired(t *testing.T, controller *Controller, desired core.DesiredFirewallSnapshot) {
	t.Helper()
	if err := controller.SetDesiredSnapshot(desired); err != nil {
		t.Fatal(err)
	}
}

func desiredSnapshot(targets ...core.NormalizedTargetEnforcementIntent) core.DesiredFirewallSnapshot {
	return core.DesiredFirewallSnapshot{
		SnapshotRevision:       1,
		InfrastructureRevision: 1,
		PolicyRevision:         1,
		Infrastructure:         core.ManagedInfrastructureIntent{Backend: "fake", OwnerVersion: "v1", Digest: "infra-v1"},
		Policy:                 core.ManagedPolicyIntent{RelationDigest: "policy-v1"},
		Targets:                targets,
	}
}

func infrastructurePlan(desired core.DesiredFirewallSnapshot) fake.OperationPlan {
	plan := fake.OperationPlan{
		Domain:                         fake.DomainInfrastructure,
		DesiredInfrastructure:          desired.Infrastructure,
		ExpectedInfrastructureRevision: desired.InfrastructureRevision,
		ExpectedSnapshotRevision:       desired.SnapshotRevision,
		FenceSnapshotRevision:          true,
	}
	plan.Digest = fake.PlanDigest(plan)
	return plan
}

func policyPlan(desired core.DesiredFirewallSnapshot) fake.OperationPlan {
	plan := fake.OperationPlan{
		Domain:                 fake.DomainPolicy,
		DesiredPolicy:          desired.Policy,
		ExpectedPolicyRevision: desired.PolicyRevision,
	}
	plan.Digest = fake.PlanDigest(plan)
	return plan
}

func targetPlan(desired core.DesiredFirewallSnapshot, target netip.Prefix) fake.OperationPlan {
	var intent core.NormalizedTargetEnforcementIntent
	for _, candidate := range desired.Targets {
		if candidate.CanonicalTarget == target {
			intent = candidate
			break
		}
	}
	plan := fake.OperationPlan{
		Domain:                   fake.DomainTarget,
		Target:                   target,
		DesiredTarget:            intent,
		ExpectedTargetGeneration: intent.Generation,
	}
	plan.Digest = fake.PlanDigest(plan)
	return plan
}

func targetIntent(target netip.Prefix, generation core.TargetEnforcementGeneration) core.NormalizedTargetEnforcementIntent {
	return targetIntentWithScope(target, generation, core.ScopeInput)
}

func targetIntentWithScope(target netip.Prefix, generation core.TargetEnforcementGeneration, scope core.EnforcementScope) core.NormalizedTargetEnforcementIntent {
	return core.NormalizedTargetEnforcementIntent{
		NodeID:                  testNodeID,
		CanonicalTarget:         target,
		BanMembership:           core.BanPresent,
		Scopes:                  scope,
		AddressFamily:           core.AddressFamilyIPv4,
		BackendAttributesDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Generation:              generation,
	}
}

const testNodeID core.NodeID = "00112233445566778899aabbccddeeff"
