package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appclock "github.com/lifei6671/guard-wall/internal/clock"
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

func TestDispatcherBackendHealthyRecoversByProbeWithoutAnotherMutation(t *testing.T) {
	clock := newDispatcherManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	plan := targetPlan(desired, target)
	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, Mutate: true, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}

	provider := &staticPlanProvider{plans: map[ReconcileKey]fake.OperationPlan{targetKey(target): plan}}
	dispatcher := newTestDispatcher(t, controller, provider, clock, 4)
	ctx, cancel, runErr := runDispatcher(t, dispatcher)
	defer cancel()
	if err := dispatcher.Wake(ctx, targetKey(target)); err != nil {
		t.Fatal(err)
	}
	waitForRetryState(t, controller, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileRetryWaiting && state.AttemptCount == 1
	})
	waitForTimers(t, clock, 1)

	resolved, err := dispatcher.BackendHealthy(ctx)
	if err != nil || resolved != 1 {
		t.Fatalf("BackendHealthy(): resolved=%d err=%v", resolved, err)
	}
	waitForRetryState(t, controller, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 1
	})
	clock.Advance(time.Second)
	assertNoRunError(t, runErr)
	probes, applies := backend.Counts()
	if probes != 1 || applies != 1 {
		t.Fatalf("observation-only recovery calls: probes=%d applies=%d", probes, applies)
	}
	if controller.ProbeRequired() {
		t.Fatal("health recovery left a stale Probe requirement")
	}
}

func TestDispatcherBackendHealthyClassifiesRecoverableBackendUnavailability(t *testing.T) {
	clock := newDispatcherManualClock()
	backend := newHealthFlapBackend()
	backend.healthy.Store(false)
	controller := newTestController(t, backend, clock, &memoryAudit{})
	dispatcher := newTestDispatcher(t, controller, &staticPlanProvider{}, clock, 1)
	_, err := dispatcher.BackendHealthy(context.Background())
	var unavailable *backendHealthUnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, errBackendUnavailable) {
		t.Fatalf("BackendHealthy() error = %T %v, want recoverable Backend unavailability", err, err)
	}
	if status := dispatcher.BackendHealthStatus(); status.State != BackendHealthDegraded || status.TotalFailures != 1 {
		t.Fatalf("Backend health status = %+v, want one recoverable failure", status)
	}
}

func TestDispatcherUsesAbsoluteRetryDeadlineAfterHealthProbe(t *testing.T) {
	clock := newDispatcherManualClock()
	backend := newHealthFlapBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	provider := &staticPlanProvider{plans: map[ReconcileKey]fake.OperationPlan{targetKey(target): targetPlan(desired, target)}}
	dispatcher := newTestDispatcher(t, controller, provider, clock, 4)
	ctx, cancel, runErr := runDispatcher(t, dispatcher)
	defer cancel()

	backend.healthy.Store(false)
	if err := dispatcher.Wake(ctx, targetKey(target)); err != nil {
		t.Fatal(err)
	}
	waitForRetryState(t, controller, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileRetryWaiting && state.AttemptCount == 1
	})
	waitForTimers(t, clock, 1)
	retryKey, waiting, _ := controller.TargetState(target)
	wantDeadline := clock.Now().Add(time.Second)
	if waiting.NextAttemptAt == nil || !waiting.NextAttemptAt.Equal(wantDeadline) {
		t.Fatalf("NextAttemptAt=%v, want %v", waiting.NextAttemptAt, wantDeadline)
	}

	backend.healthy.Store(true)
	clock.Advance(500 * time.Millisecond)
	resolved, err := dispatcher.BackendHealthy(ctx)
	if err != nil || resolved != 0 {
		t.Fatalf("BackendHealthy(): resolved=%d err=%v", resolved, err)
	}
	if backend.applyAttempts.Load() != 1 {
		t.Fatalf("health event triggered Apply before deadline: %d", backend.applyAttempts.Load())
	}
	unchangedKey, unchanged, _ := controller.TargetState(target)
	if unchangedKey != retryKey || unchanged.AttemptCount != 1 || unchanged.NextAttemptAt == nil || !unchanged.NextAttemptAt.Equal(wantDeadline) {
		t.Fatalf("health event changed retry budget/deadline: %+v", unchanged)
	}

	clock.Advance(499 * time.Millisecond)
	if backend.applyAttempts.Load() != 1 {
		t.Fatalf("retry ran before absolute deadline: %d", backend.applyAttempts.Load())
	}
	clock.Advance(time.Millisecond)
	waitForRetryState(t, controller, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 2
	})
	assertNoRunError(t, runErr)
	if backend.applyAttempts.Load() != 2 {
		t.Fatalf("retry Apply count=%d, want 2", backend.applyAttempts.Load())
	}
}

func TestDispatcherQueueCoalescesAndBackpressureIsCancelable(t *testing.T) {
	clock := newDispatcherManualClock()
	controller := newTestController(t, fake.NewBackend(), clock, &memoryAudit{})
	targetA := netip.MustParsePrefix("192.0.2.4/32")
	targetB := netip.MustParsePrefix("192.0.2.5/32")
	desired := desiredSnapshot(targetIntent(targetA, 1), targetIntent(targetB, 1))
	setDesired(t, controller, desired)
	provider := &staticPlanProvider{plans: map[ReconcileKey]fake.OperationPlan{
		targetKey(targetA): targetPlan(desired, targetA),
		targetKey(targetB): targetPlan(desired, targetB),
	}}
	dispatcher := newTestDispatcher(t, controller, provider, clock, 1)
	if err := dispatcher.Wake(context.Background(), targetKey(targetA)); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Wake(context.Background(), targetKey(targetA)); err != nil {
		t.Fatalf("duplicate wake: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := dispatcher.Wake(canceled, targetKey(targetB)); !errors.Is(err, context.Canceled) {
		t.Fatalf("full queue wake error=%v, want context canceled", err)
	}

	ctx, stop, runErr := runDispatcher(t, dispatcher)
	defer stop()
	waitForRetryState(t, controller, targetA, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 1
	})
	if provider.CallCount(targetKey(targetA)) != 1 {
		t.Fatalf("duplicate queued key loaded %d Plans", provider.CallCount(targetKey(targetA)))
	}
	if err := dispatcher.Wake(ctx, targetKey(targetB)); err != nil {
		t.Fatal(err)
	}
	waitForRetryState(t, controller, targetB, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 1
	})
	assertNoRunError(t, runErr)
}

func TestDispatcherTreatsExpiredTargetAsNoOpAndContinues(t *testing.T) {
	clock := newDispatcherManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	expiredTarget := netip.MustParsePrefix("192.0.2.4/32")
	liveTarget := netip.MustParsePrefix("192.0.2.5/32")
	expiredIntent := targetIntent(expiredTarget, 1)
	expiresAt := clock.Now()
	expiredIntent.EffectiveUntil = &expiresAt
	expiredIntent.TimeoutMode = core.TimeoutNative
	desired := desiredSnapshot(expiredIntent, targetIntent(liveTarget, 1))
	setDesired(t, controller, desired)
	provider := &staticPlanProvider{plans: map[ReconcileKey]fake.OperationPlan{
		targetKey(expiredTarget): targetPlan(desired, expiredTarget),
		targetKey(liveTarget):    targetPlan(desired, liveTarget),
	}}
	dispatcher := newTestDispatcher(t, controller, provider, clock, 2)
	if err := dispatcher.Wake(context.Background(), targetKey(expiredTarget)); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Wake(context.Background(), targetKey(liveTarget)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel, runErr := runDispatcher(t, dispatcher)
	defer cancel()

	waitForRetryState(t, controller, liveTarget, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 1
	})
	assertNoRunError(t, runErr)
	if _, _, exists := controller.TargetState(expiredTarget); exists {
		t.Fatal("expired target consumed retry budget")
	}
	if provider.CallCount(targetKey(expiredTarget)) != 1 || provider.CallCount(targetKey(liveTarget)) != 1 {
		t.Fatalf("Plan reads: expired=%d live=%d, want 1/1",
			provider.CallCount(targetKey(expiredTarget)), provider.CallCount(targetKey(liveTarget)))
	}
	probes, applies := backend.Counts()
	if probes != 1 || applies != 1 {
		t.Fatalf("dispatcher crossed expired mutation boundary: probes=%d applies=%d, want 1/1 from live target", probes, applies)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("dispatcher context ended while processing expired target: %v", err)
	}
}

func TestDispatcherRefreshesExpiredStalePlanBeforeNoOp(t *testing.T) {
	clock := newDispatcherManualClock()
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
	provider := &refreshingPlanProvider{
		first: targetPlan(stale, target),
		fresh: targetPlan(current, target),
	}
	dispatcher := newTestDispatcher(t, controller, provider, clock, 1)
	ctx, cancel, runErr := runDispatcher(t, dispatcher)
	defer cancel()
	if err := dispatcher.Wake(ctx, targetKey(target)); err != nil {
		t.Fatal(err)
	}

	waitForRetryState(t, controller, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 1
	})
	assertNoRunError(t, runErr)
	if provider.CallCount() != 2 {
		t.Fatalf("Plan reads=%d, want stale plus fresh", provider.CallCount())
	}
	probes, applies := backend.Counts()
	if probes != 1 || applies != 1 {
		t.Fatalf("fresh current Plan did not converge: probes=%d applies=%d", probes, applies)
	}
}

func TestDispatcherBlockedReservationDoesNotDefeatDuplicateCancellation(t *testing.T) {
	clock := newDispatcherManualClock()
	controller := newTestController(t, fake.NewBackend(), clock, &memoryAudit{})
	targetA := netip.MustParsePrefix("192.0.2.4/32")
	targetB := netip.MustParsePrefix("192.0.2.5/32")
	dispatcher := newTestDispatcher(t, controller, &staticPlanProvider{}, clock, 1)
	if err := dispatcher.Wake(context.Background(), targetKey(targetA)); err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancelBlocked := context.WithCancel(context.Background())
	blockedDone := make(chan error, 1)
	go func() { blockedDone <- dispatcher.Wake(blockedCtx, targetKey(targetB)) }()
	waitForQueuedKey(t, dispatcher, targetKey(targetB))

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := dispatcher.Wake(canceled, targetKey(targetB)); !errors.Is(err, context.Canceled) {
		t.Fatalf("duplicate canceled wake error=%v", err)
	}
	cancelBlocked()
	select {
	case err := <-blockedDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked wake error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked reservation did not observe cancellation")
	}
}

func TestDispatcherRestoresPersistedAbsoluteDeadlineOnRun(t *testing.T) {
	ctx := context.Background()
	clock := newDispatcherManualClock()
	backend := newHealthFlapBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	plan := targetPlan(desired, target)
	path := filepath.Join(t.TempDir(), "guard.db")

	firstStore := openRestartStore(t, ctx, path, clock.Now())
	first := newPersistentTestController(t, ctx, firstStore, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, firstStore, first, desired)
	backend.healthy.Store(false)
	if _, err := first.Execute(ctx, plan); !errors.Is(err, errBackendUnavailable) {
		t.Fatalf("first Execute error=%v", err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	backend.healthy.Store(true)
	secondStore := openRestartStore(t, ctx, path, clock.Now())
	defer secondStore.Close()
	second := newPersistentTestController(t, ctx, secondStore, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, secondStore, second, desired)
	provider := &staticPlanProvider{
		keys:  []ReconcileKey{targetKey(target)},
		plans: map[ReconcileKey]fake.OperationPlan{targetKey(target): plan},
	}
	dispatcher := newTestDispatcher(t, second, provider, clock, 2)
	runCtx, cancel, runErr := runDispatcher(t, dispatcher)
	waitForTimers(t, clock, 1)
	if backend.probeAttempts.Load() != 1 || backend.applyAttempts.Load() != 1 {
		t.Fatalf("startup recovery calls before deadline: probes=%d applies=%d, want 1/1", backend.probeAttempts.Load(), backend.applyAttempts.Load())
	}
	clock.Advance(999 * time.Millisecond)
	if backend.applyAttempts.Load() != 1 {
		t.Fatalf("persisted retry ran before absolute deadline: applies=%d", backend.applyAttempts.Load())
	}
	clock.Advance(time.Millisecond)
	waitForRetryState(t, second, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 2
	})
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run(): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not stop")
	}
	if err := dispatcher.Wake(runCtx, targetKey(target)); !errors.Is(err, ErrDispatcherStopped) {
		t.Fatalf("wake after stop error=%v", err)
	}
}

func TestDispatcherRestartRetainsProbeBarrierUntilFutureDeadline(t *testing.T) {
	ctx := context.Background()
	clock := newDispatcherManualClock()
	backend := newHealthFlapBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	plan := targetPlan(desired, target)
	path := filepath.Join(t.TempDir(), "guard.db")

	firstStore := openRestartStore(t, ctx, path, clock.Now())
	first := newPersistentTestController(t, ctx, firstStore, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, firstStore, first, desired)
	backend.healthy.Store(false)
	if _, err := first.Execute(ctx, plan); !errors.Is(err, errBackendUnavailable) {
		t.Fatalf("first Execute error=%v", err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	backend.healthy.Store(true)
	secondStore := openRestartStore(t, ctx, path, clock.Now())
	defer secondStore.Close()
	second := newPersistentTestController(t, ctx, secondStore, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, secondStore, second, desired)
	provider := &staticPlanProvider{
		keys:  []ReconcileKey{targetKey(target)},
		plans: map[ReconcileKey]fake.OperationPlan{targetKey(target): plan},
	}
	dispatcher := newTestDispatcher(t, second, provider, clock, 2)
	_, cancel, runErr := runDispatcher(t, dispatcher)
	defer cancel()
	waitForTimers(t, clock, 1)
	if !second.ProbeRequired() {
		t.Fatal("startup mismatch cleared the required Probe before the retry deadline")
	}
	if backend.probeAttempts.Load() != 1 || backend.applyAttempts.Load() != 1 {
		t.Fatalf("startup calls before deadline: probes=%d applies=%d", backend.probeAttempts.Load(), backend.applyAttempts.Load())
	}

	clock.Advance(999 * time.Millisecond)
	if _, err := backend.Backend.Apply(ctx, plan); err != nil {
		t.Fatalf("external physical convergence: %v", err)
	}
	clock.Advance(time.Millisecond)
	waitForRetryState(t, second, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 1
	})
	if second.ProbeRequired() {
		t.Fatal("deadline Probe recovery retained a stale barrier")
	}
	if backend.probeAttempts.Load() != 2 || backend.applyAttempts.Load() != 1 {
		t.Fatalf("deadline recovery was not observation-only: probes=%d applies=%d", backend.probeAttempts.Load(), backend.applyAttempts.Load())
	}
	assertNoRunError(t, runErr)
}

func TestDispatcherStartupProbeConvergesPersistedAmbiguousMutation(t *testing.T) {
	ctx := context.Background()
	clock := newDispatcherManualClock()
	backend := fake.NewBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	plan := targetPlan(desired, target)
	path := filepath.Join(t.TempDir(), "guard.db")
	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, Mutate: true, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}

	firstStore := openRestartStore(t, ctx, path, clock.Now())
	first := newPersistentTestController(t, ctx, firstStore, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, firstStore, first, desired)
	result, err := first.Execute(ctx, plan)
	if err != nil || result.Apply.Kind != fake.ResultUnknown {
		t.Fatalf("first Execute: result=%+v err=%v", result, err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore := openRestartStore(t, ctx, path, clock.Now())
	defer secondStore.Close()
	second := newPersistentTestController(t, ctx, secondStore, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, secondStore, second, desired)
	provider := &staticPlanProvider{
		keys:  []ReconcileKey{targetKey(target)},
		plans: map[ReconcileKey]fake.OperationPlan{targetKey(target): plan},
	}
	dispatcher := newTestDispatcher(t, second, provider, clock, 1)
	_, cancel, runErr := runDispatcher(t, dispatcher)
	waitForRetryState(t, second, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 1
	})
	probes, applies := backend.Counts()
	if probes != 1 || applies != 1 {
		t.Fatalf("startup observation recovery calls: probes=%d applies=%d", probes, applies)
	}
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run(): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not stop")
	}
}

func TestDispatcherStartupProbeConfirmsMultipleConvergedKeysWithoutMutation(t *testing.T) {
	ctx := context.Background()
	clock := newDispatcherManualClock()
	backend := fake.NewBackend()
	targetA := netip.MustParsePrefix("192.0.2.4/32")
	targetB := netip.MustParsePrefix("192.0.2.5/32")
	desired := desiredSnapshot(targetIntent(targetA, 1), targetIntent(targetB, 1))
	plans := map[ReconcileKey]fake.OperationPlan{
		targetKey(targetA): targetPlan(desired, targetA),
		targetKey(targetB): targetPlan(desired, targetB),
	}
	path := filepath.Join(t.TempDir(), "guard.db")

	firstStore := openRestartStore(t, ctx, path, clock.Now())
	first := newPersistentTestController(t, ctx, firstStore, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, firstStore, first, desired)
	for _, target := range []netip.Prefix{targetA, targetB} {
		if _, err := first.Execute(ctx, plans[targetKey(target)]); err != nil {
			t.Fatalf("converge %s: %v", target, err)
		}
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	probesBefore, appliesBefore := backend.Counts()

	secondStore := openRestartStore(t, ctx, path, clock.Now())
	defer secondStore.Close()
	second := newPersistentTestController(t, ctx, secondStore, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, secondStore, second, desired)
	provider := &staticPlanProvider{
		keys:  []ReconcileKey{targetKey(targetA), targetKey(targetB)},
		plans: plans,
	}
	dispatcher := newTestDispatcher(t, second, provider, clock, 2)
	_, cancel, runErr := runDispatcher(t, dispatcher)
	defer cancel()

	for _, target := range []netip.Prefix{targetA, targetB} {
		waitForConfirmedTarget(t, second, target, 1)
		_, state, _ := second.TargetState(target)
		if state.Status != core.ReconcileConverged || state.AttemptCount != 1 {
			t.Fatalf("startup state for %s: %+v", target, state)
		}
	}
	probesAfter, appliesAfter := backend.Counts()
	if probesAfter != probesBefore+1 || appliesAfter != appliesBefore {
		t.Fatalf("multi-key startup recovery calls: probes %d->%d applies %d->%d", probesBefore, probesAfter, appliesBefore, appliesAfter)
	}
	for _, key := range []ReconcileKey{targetKey(targetA), targetKey(targetB)} {
		if provider.CallCount(key) != 0 {
			t.Fatalf("matching startup key %s loaded %d mutation plans", reconcileKeyName(key), provider.CallCount(key))
		}
	}
	assertNoRunError(t, runErr)
}

func TestDispatcherWakeDuringPlanLoadTriggersASecondFreshRead(t *testing.T) {
	clock := newDispatcherManualClock()
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	provider := newBlockingPlanProvider(targetPlan(desired, target))
	dispatcher := newTestDispatcher(t, controller, provider, clock, 2)
	ctx, cancel, runErr := runDispatcher(t, dispatcher)
	defer cancel()

	if err := dispatcher.Wake(ctx, targetKey(target)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.firstRead:
	case <-time.After(2 * time.Second):
		t.Fatal("first Plan read did not start")
	}
	if err := dispatcher.Wake(ctx, targetKey(target)); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	waitForRetryState(t, controller, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 1
	})
	if provider.CallCount() != 2 {
		t.Fatalf("Plan reads=%d, want 2", provider.CallCount())
	}
	assertNoRunError(t, runErr)
}

func TestDispatcherExpiredDeadlineProbeFailureUsesHealthBackoffWithoutHotLoop(t *testing.T) {
	clock := newDispatcherManualClock()
	backend := newHealthFlapBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setDesired(t, controller, desired)
	provider := &staticPlanProvider{plans: map[ReconcileKey]fake.OperationPlan{targetKey(target): targetPlan(desired, target)}}
	dispatcher := newTestDispatcher(t, controller, provider, clock, 2)
	ctx, cancel, runErr := runDispatcher(t, dispatcher)
	defer cancel()

	backend.healthy.Store(false)
	if err := dispatcher.Wake(ctx, targetKey(target)); err != nil {
		t.Fatal(err)
	}
	waitForRetryState(t, controller, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileRetryWaiting && state.AttemptCount == 1
	})
	waitForTimers(t, clock, 1)
	clock.Advance(time.Second)
	waitForCounter(t, &backend.probeAttempts, 1)
	if backend.applyAttempts.Load() != 1 {
		t.Fatalf("expired deadline crossed failed Probe: applies=%d", backend.applyAttempts.Load())
	}
	clock.Advance(time.Hour)
	time.Sleep(10 * time.Millisecond)
	if backend.probeAttempts.Load() != 2 {
		t.Fatalf("health backoff Probe count=%d, want 2", backend.probeAttempts.Load())
	}
	_, state, _ := controller.TargetState(target)
	if state.AttemptCount != 1 || state.Status != core.ReconcileRetryWaiting {
		t.Fatalf("failed Probe changed retry ledger: %+v", state)
	}
	backend.healthy.Store(true)
	resolved, err := dispatcher.BackendHealthy(ctx)
	if err != nil || resolved != 0 {
		t.Fatalf("BackendHealthy(): resolved=%d err=%v", resolved, err)
	}
	waitForRetryState(t, controller, target, func(state core.RetryState) bool {
		return state.Status == core.ReconcileConverged && state.AttemptCount == 2
	})
	if backend.applyAttempts.Load() != 2 {
		t.Fatalf("healthy event did not resume remaining budget: applies=%d", backend.applyAttempts.Load())
	}
	assertNoRunError(t, runErr)
}

func TestDispatcherContinuousStalePlanFailsFast(t *testing.T) {
	clock := newDispatcherManualClock()
	controller := newTestController(t, fake.NewBackend(), clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	current := desiredSnapshot(targetIntent(target, 2))
	current.SnapshotRevision = 2
	setDesired(t, controller, current)
	stale := desiredSnapshot(targetIntent(target, 1))
	provider := &staticPlanProvider{plans: map[ReconcileKey]fake.OperationPlan{targetKey(target): targetPlan(stale, target)}}
	dispatcher := newTestDispatcher(t, controller, provider, clock, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	if err := dispatcher.Wake(ctx, targetKey(target)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("continuous stale Plan did not fail fast")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("continuous stale Plan entered a hot loop")
	}
	if provider.CallCount(targetKey(target)) != 2 {
		t.Fatalf("stale Plan reads=%d, want 2", provider.CallCount(targetKey(target)))
	}
}

func TestDispatcherCancellationStopsWorkerAndRejectsLaterWake(t *testing.T) {
	clock := newDispatcherManualClock()
	controller := newTestController(t, fake.NewBackend(), clock, &memoryAudit{})
	dispatcher := newTestDispatcher(t, controller, &staticPlanProvider{}, clock, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run(): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not stop after cancellation")
	}
	if err := dispatcher.Wake(context.Background(), ReconcileKey{Domain: fake.DomainInfrastructure}); !errors.Is(err, ErrDispatcherStopped) {
		t.Fatalf("wake after stop error=%v", err)
	}
}

func targetKey(target netip.Prefix) ReconcileKey {
	return ReconcileKey{Domain: fake.DomainTarget, Target: target}
}

type staticPlanProvider struct {
	mu    sync.Mutex
	keys  []ReconcileKey
	plans map[ReconcileKey]fake.OperationPlan
	calls map[ReconcileKey]int
}

func (p *staticPlanProvider) ReconcileKeys(_ context.Context) ([]ReconcileKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ReconcileKey(nil), p.keys...), nil
}

type blockingPlanProvider struct {
	mu        sync.Mutex
	plan      fake.OperationPlan
	calls     int
	firstRead chan struct{}
	release   chan struct{}
}

type refreshingPlanProvider struct {
	mu    sync.Mutex
	first fake.OperationPlan
	fresh fake.OperationPlan
	calls int
}

func (p *refreshingPlanProvider) ReconcileKeys(context.Context) ([]ReconcileKey, error) {
	return nil, nil
}

func (p *refreshingPlanProvider) CurrentPlan(context.Context, ReconcileKey) (fake.OperationPlan, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return p.first, true, nil
	}
	return p.fresh, true, nil
}

func (p *refreshingPlanProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func newBlockingPlanProvider(plan fake.OperationPlan) *blockingPlanProvider {
	return &blockingPlanProvider{plan: plan, firstRead: make(chan struct{}), release: make(chan struct{})}
}

func (p *blockingPlanProvider) CurrentPlan(ctx context.Context, _ ReconcileKey) (fake.OperationPlan, bool, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		close(p.firstRead)
		select {
		case <-p.release:
		case <-ctx.Done():
			return fake.OperationPlan{}, false, ctx.Err()
		}
		return fake.OperationPlan{}, false, nil
	}
	return p.plan, true, nil
}

func (p *blockingPlanProvider) ReconcileKeys(context.Context) ([]ReconcileKey, error) {
	return nil, nil
}

func (p *blockingPlanProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *staticPlanProvider) CurrentPlan(_ context.Context, key ReconcileKey) (fake.OperationPlan, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = make(map[ReconcileKey]int)
	}
	p.calls[key]++
	plan, ok := p.plans[key]
	return plan, ok, nil
}

func (p *staticPlanProvider) CallCount(key ReconcileKey) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[key]
}

func newTestDispatcher(t *testing.T, controller *Controller, plans PlanProvider, schedulerClock appclock.Clock, capacity int) *Dispatcher {
	t.Helper()
	dispatcher, err := NewDispatcherWithClock(controller, plans, capacity, schedulerClock)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func runDispatcher(t *testing.T, dispatcher *Dispatcher) (context.Context, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	return ctx, cancel, done
}

func assertNoRunError(t *testing.T, runErr <-chan error) {
	t.Helper()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("dispatcher stopped: %v", err)
		}
	default:
	}
}

func waitForRetryState(t *testing.T, controller *Controller, target netip.Prefix, accept func(core.RetryState) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, state, ok := controller.TargetState(target)
		if ok && accept(state) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	_, state, ok := controller.TargetState(target)
	t.Fatalf("retry state did not converge: ok=%t state=%+v", ok, state)
}

func waitForConfirmedTarget(t *testing.T, controller *Controller, target netip.Prefix, want core.TargetEnforcementGeneration) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if generation, confirmed := controller.ConfirmedTarget(target); confirmed && generation == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("target %s was not confirmed at generation %d", target, want)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func waitForTimers(t *testing.T, clock *dispatcherManualClock, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if clock.TimerCount() >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active timer count=%d, want at least %d", clock.TimerCount(), count)
}

func waitForCounter(t *testing.T, counter interface{ Load() uint64 }, want uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if counter.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("counter=%d, want at least %d", counter.Load(), want)
}

func waitForQueuedKey(t *testing.T, dispatcher *Dispatcher, key ReconcileKey) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dispatcher.queueMu.Lock()
		_, ok := dispatcher.queued[key]
		dispatcher.queueMu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("wake reservation was not created")
}

type dispatcherManualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*dispatcherManualTimer]time.Time
}

func newDispatcherManualClock() *dispatcherManualClock {
	return &dispatcherManualClock{
		now:    time.Unix(1_700_000_000, 0).UTC(),
		timers: make(map[*dispatcherManualTimer]time.Time),
	}
}

func (c *dispatcherManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *dispatcherManualClock) NewTimer(delay time.Duration) appclock.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &dispatcherManualTimer{clock: c, ch: make(chan time.Time, 1)}
	if delay <= 0 {
		timer.ch <- c.now
		return timer
	}
	timer.active = true
	c.timers[timer] = c.now.Add(delay)
	return timer
}

func (c *dispatcherManualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	now := c.now
	ready := make([]*dispatcherManualTimer, 0)
	for timer, deadline := range c.timers {
		if timer.active && !deadline.After(now) {
			timer.active = false
			delete(c.timers, timer)
			ready = append(ready, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range ready {
		timer.ch <- now
	}
}

func (c *dispatcherManualClock) TimerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

type dispatcherManualTimer struct {
	clock  *dispatcherManualClock
	ch     chan time.Time
	active bool
}

func (t *dispatcherManualTimer) C() <-chan time.Time { return t.ch }

func (t *dispatcherManualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	delete(t.clock.timers, t)
	return wasActive
}
