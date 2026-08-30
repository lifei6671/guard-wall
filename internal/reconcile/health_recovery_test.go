package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

func TestBackendHealthRecoveryProbesBeforeRetryWithoutResettingBudget(t *testing.T) {
	clock := newManualClock()
	backend := newHealthFlapBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)

	backend.healthy.Store(false)
	if _, err := controller.Execute(context.Background(), plan); !errors.Is(err, errBackendUnavailable) {
		t.Fatalf("unhealthy apply error = %v", err)
	}
	_, state, ok := controller.TargetState(target)
	if !ok || state.AttemptCount != 1 || state.Status != core.ReconcileRetryWaiting || state.LastErrorCode != "backend_error" {
		t.Fatalf("unhealthy apply did not preserve the consumed attempt: %+v", state)
	}
	if !controller.ProbeRequired() {
		t.Fatal("backend error did not require an authoritative Probe before another mutation")
	}

	if _, err := controller.Execute(context.Background(), plan); !errors.Is(err, errBackendUnavailable) {
		t.Fatalf("unhealthy recovery Probe error = %v", err)
	}
	_, unchanged, _ := controller.TargetState(target)
	if unchanged.AttemptCount != 1 || unchanged.Status != core.ReconcileRetryWaiting {
		t.Fatalf("failed health Probe changed retry budget: %+v", unchanged)
	}
	if backend.applyAttempts.Load() != 1 || backend.probeAttempts.Load() != 1 {
		t.Fatalf("unhealthy retry crossed the Probe barrier: probes=%d applies=%d", backend.probeAttempts.Load(), backend.applyAttempts.Load())
	}

	backend.healthy.Store(true)
	clock.Advance(time.Second)
	result, err := controller.Execute(context.Background(), plan)
	if err != nil || result.Apply.Kind != fake.ResultConfirmed {
		t.Fatalf("healthy retry: result=%+v err=%v", result, err)
	}
	_, recovered, _ := controller.TargetState(target)
	if recovered.AttemptCount != 2 || recovered.Status != core.ReconcileConverged {
		t.Fatalf("health recovery reset or miscounted the retry budget: %+v", recovered)
	}
	if controller.ProbeRequired() {
		t.Fatal("successful health recovery left a stale Probe requirement")
	}
	if backend.applyAttempts.Load() != 2 || backend.probeAttempts.Load() != 3 {
		t.Fatalf("unexpected recovery order counts: probes=%d applies=%d", backend.probeAttempts.Load(), backend.applyAttempts.Load())
	}
	probes, applies := backend.Backend.Counts()
	if probes != 2 || applies != 1 {
		t.Fatalf("unexpected healthy backend calls: probes=%d applies=%d", probes, applies)
	}
}

func TestRepeatedApplyUnavailabilityDoesNotPermitSeventhMutationAfterBudgetExhaustion(t *testing.T) {
	clock := newManualClock()
	backend := newHealthFlapBackend()
	audit := &memoryAudit{}
	controller := newTestController(t, backend, clock, audit)
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)

	backend.healthy.Store(false)
	if _, err := controller.Execute(context.Background(), plan); !errors.Is(err, errBackendUnavailable) {
		t.Fatalf("attempt 1 error = %v", err)
	}
	oldKey, first, ok := controller.TargetState(target)
	if !ok || first.AttemptCount != 1 || first.Status != core.ReconcileRetryWaiting || first.LastErrorCode != "backend_error" {
		t.Fatalf("attempt 1 ledger: key=%+v state=%+v", oldKey, first)
	}
	firstRetryAt := clock.Now().Add(retryBackoff[0])
	if first.NextAttemptAt == nil || !first.NextAttemptAt.Equal(firstRetryAt) {
		t.Fatalf("attempt 1 retry time = %v, want %v", first.NextAttemptAt, firstRetryAt)
	}
	backend.healthy.Store(true)
	backend.applyUnavailable.Store(true)
	for attempt := uint32(2); attempt <= maxMutationAttempts; attempt++ {
		clock.Advance(retryBackoff[attempt-2])
		if _, err := controller.Execute(context.Background(), plan); !errors.Is(err, errBackendUnavailable) {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
		key, state, ok := controller.TargetState(target)
		expectedStatus := core.ReconcileRetryWaiting
		if attempt == maxMutationAttempts {
			expectedStatus = core.ReconcileDegraded
		}
		if !ok || key != oldKey || state.AttemptCount != attempt || state.Status != expectedStatus || state.LastErrorCode != "backend_error" {
			t.Fatalf("attempt %d ledger: key=%+v state=%+v", attempt, key, state)
		}
		if attempt < maxMutationAttempts {
			nextRetryAt := clock.Now().Add(retryBackoff[attempt-1])
			if state.NextAttemptAt == nil || !state.NextAttemptAt.Equal(nextRetryAt) {
				t.Fatalf("attempt %d retry time = %v, want %v", attempt, state.NextAttemptAt, nextRetryAt)
			}
		} else if state.NextAttemptAt != nil {
			t.Fatalf("attempt %d retained retry time %v after exhaustion", attempt, state.NextAttemptAt)
		}
		if !controller.ProbeRequired() {
			t.Fatalf("attempt %d lost its Probe requirement", attempt)
		}
	}

	_, exhausted, ok := controller.TargetState(target)
	if !ok || exhausted.AttemptCount != maxMutationAttempts || exhausted.Status != core.ReconcileDegraded || exhausted.NextAttemptAt != nil {
		t.Fatalf("unexpected exhausted state: key=%+v state=%+v", oldKey, exhausted)
	}
	if !controller.ProbeRequired() {
		t.Fatal("exhausted ambiguous mutation lost its Probe requirement")
	}
	if audit.Count() != 0 {
		t.Fatalf("automatic recovery wrote administrator Retry audit: %d", audit.Count())
	}

	backend.applyUnavailable.Store(false)
	if _, err := controller.Execute(context.Background(), plan); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("recovery after exhaustion error = %v", err)
	}
	newKey, unchanged, _ := controller.TargetState(target)
	if newKey != oldKey || unchanged.AttemptCount != maxMutationAttempts || unchanged.Status != core.ReconcileDegraded {
		t.Fatalf("health recovery revived exhausted budget: old=%+v new=%+v state=%+v", oldKey, newKey, unchanged)
	}
	if backend.applyAttempts.Load() != uint64(maxMutationAttempts) || backend.probeAttempts.Load() != uint64(maxMutationAttempts) {
		t.Fatalf("unexpected exhausted recovery calls: probes=%d applies=%d", backend.probeAttempts.Load(), backend.applyAttempts.Load())
	}
	probes, applies := backend.Backend.Counts()
	if probes != uint64(maxMutationAttempts) || applies != 0 {
		t.Fatalf("unavailable Apply reached physical fake: probes=%d applies=%d", probes, applies)
	}
	if generation, confirmed := controller.ConfirmedTarget(target); confirmed {
		t.Fatalf("unapplied target was confirmed at generation %d", generation)
	}
	if audit.Count() != 0 {
		t.Fatalf("health recovery created Retry audit: %d", audit.Count())
	}
}

func TestExhaustedUnknownCanConvergeByProbeWithoutSeventhMutation(t *testing.T) {
	clock := newManualClock()
	backend := fake.NewBackend()
	audit := &memoryAudit{}
	controller := newTestController(t, backend, clock, audit)
	target := netip.MustParsePrefix("192.0.2.4/32")
	intent := targetIntent(target, 1)
	desired := desiredSnapshot(intent)
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)

	for attempt := uint32(1); attempt <= maxMutationAttempts; attempt++ {
		if attempt > 1 {
			clock.Advance(retryBackoff[attempt-2])
		}
		if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, ErrorCode: "timeout"}); err != nil {
			t.Fatal(err)
		}
		result, err := controller.Execute(context.Background(), plan)
		if err != nil || result.Apply.Kind != fake.ResultUnknown {
			t.Fatalf("attempt %d: result=%+v err=%v", attempt, result, err)
		}
	}

	oldKey, exhausted, ok := controller.TargetState(target)
	if !ok || exhausted.AttemptCount != maxMutationAttempts || exhausted.Status != core.ReconcileDegraded || exhausted.NextAttemptAt != nil {
		t.Fatalf("unexpected exhausted state: key=%+v state=%+v", oldKey, exhausted)
	}
	if err := backend.SetPhysicalTarget(core.PhysicalTargetObserved{
		CanonicalTarget:      target,
		ObservedAt:           clock.Now(),
		Backend:              "fake",
		BanMembership:        core.ObservedMembershipPresent,
		PolicyCoverage:       core.ObservedPolicyNone,
		PolicyRelationDigest: intent.PolicyRelationDigest,
		TimeoutMode:          intent.TimeoutMode,
		Scopes:               intent.Scopes,
		AddressFamily:        intent.AddressFamily,
		OwnerVersion:         intent.BackendAttributesDigest,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := controller.Execute(context.Background(), plan)
	if err != nil || !result.RecoveredByProbe {
		t.Fatalf("observation-only recovery: result=%+v err=%v", result, err)
	}
	newKey, recovered, _ := controller.TargetState(target)
	if newKey != oldKey || recovered.AttemptCount != maxMutationAttempts || recovered.Status != core.ReconcileConverged || recovered.NextAttemptAt != nil || recovered.LastErrorCode != "" {
		t.Fatalf("Probe recovery changed the budget instead of confirming it: old=%+v new=%+v state=%+v", oldKey, newKey, recovered)
	}
	probes, applies := backend.Counts()
	if probes != uint64(maxMutationAttempts) || applies != uint64(maxMutationAttempts) {
		t.Fatalf("observation-only recovery mutated again: probes=%d applies=%d", probes, applies)
	}
	if generation, confirmed := controller.ConfirmedTarget(target); !confirmed || generation != intent.Generation {
		t.Fatalf("confirmed generation = %d,%t; want %d,true", generation, confirmed, intent.Generation)
	}
	if controller.ProbeRequired() {
		t.Fatal("successful observation-only recovery left a stale Probe requirement")
	}
	if audit.Count() != 0 {
		t.Fatalf("observation-only recovery created Retry audit: %d", audit.Count())
	}
}

var errBackendUnavailable = errors.New("backend unavailable")

type healthFlapBackend struct {
	*fake.Backend
	healthy          atomic.Bool
	applyUnavailable atomic.Bool
	probeAttempts    atomic.Uint64
	applyAttempts    atomic.Uint64
}

func newHealthFlapBackend() *healthFlapBackend {
	backend := &healthFlapBackend{Backend: fake.NewBackend()}
	backend.healthy.Store(true)
	return backend
}

func (b *healthFlapBackend) Probe(ctx context.Context) (fake.Snapshot, error) {
	b.probeAttempts.Add(1)
	if !b.healthy.Load() {
		return fake.Snapshot{}, errBackendUnavailable
	}
	return b.Backend.Probe(ctx)
}

func (b *healthFlapBackend) Apply(ctx context.Context, plan fake.OperationPlan) (fake.ApplyResult, error) {
	b.applyAttempts.Add(1)
	if !b.healthy.Load() || b.applyUnavailable.Load() {
		return fake.ApplyResult{}, errBackendUnavailable
	}
	return b.Backend.Apply(ctx, plan)
}
