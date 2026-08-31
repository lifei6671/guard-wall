package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
	storepkg "github.com/lifei6671/guard-wall/internal/store"
)

func TestPersistentControllerRecoversUnknownTargetAcrossSQLiteReopen(t *testing.T) {
	tests := []struct {
		name           string
		domain         fake.Domain
		mutate         bool
		wantRecovered  bool
		wantAttempts   uint32
		wantApplyCalls uint64
		wantProbeCalls uint64
	}{
		{
			name:           "authoritative Probe observes ambiguous mutation",
			domain:         fake.DomainTarget,
			mutate:         true,
			wantRecovered:  true,
			wantAttempts:   1,
			wantApplyCalls: 1,
			wantProbeCalls: 1,
		},
		{
			name:           "authoritative Probe disproves ambiguous mutation",
			domain:         fake.DomainTarget,
			mutate:         false,
			wantRecovered:  false,
			wantAttempts:   2,
			wantApplyCalls: 2,
			wantProbeCalls: 2,
		},
		{
			name:           "infrastructure Probe key survives reopen",
			domain:         fake.DomainInfrastructure,
			mutate:         true,
			wantRecovered:  true,
			wantAttempts:   1,
			wantApplyCalls: 1,
			wantProbeCalls: 1,
		},
		{
			name:           "policy Probe key survives reopen",
			domain:         fake.DomainPolicy,
			mutate:         true,
			wantRecovered:  true,
			wantAttempts:   1,
			wantApplyCalls: 1,
			wantProbeCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newManualClock()
			backend := fake.NewBackend()
			audit := &memoryAudit{}
			desired := desiredSnapshot(targetIntent(netip.MustParsePrefix("192.0.2.4/32"), 1))
			plan := persistentRecoveryPlan(desired, test.domain)
			databasePath := filepath.Join(t.TempDir(), "guard.db")

			database := openRestartStore(t, ctx, databasePath, clock.Now())
			controller := newPersistentTestController(t, ctx, database, backend, clock, audit)
			setPersistentDesired(t, ctx, database, controller, desired)
			if err := backend.QueueOutcome(test.domain, fake.QueuedOutcome{
				Kind: fake.ResultUnknown, Mutate: test.mutate, ErrorCode: "timeout",
			}); err != nil {
				t.Fatal(err)
			}
			first, err := controller.Execute(ctx, plan)
			if err != nil || first.Apply.Kind != fake.ResultUnknown {
				t.Fatalf("first Execute(): result=%+v err=%v", first, err)
			}
			if !controller.ProbeRequired() {
				t.Fatal("ambiguous mutation did not persist a Probe barrier")
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			clock.Advance(time.Second)
			database = openRestartStore(t, ctx, databasePath, clock.Now())
			controller = newPersistentTestController(t, ctx, database, backend, clock, audit)
			setPersistentDesired(t, ctx, database, controller, desired)
			if !controller.ProbeRequired() {
				t.Fatal("SQLite reopen lost the Probe barrier")
			}
			result, err := controller.Execute(ctx, plan)
			if err != nil {
				t.Fatalf("recovery Execute(): %v", err)
			}
			if result.RecoveredByProbe != test.wantRecovered {
				t.Fatalf("RecoveredByProbe = %v, want %v", result.RecoveredByProbe, test.wantRecovered)
			}
			state, ok := retryStateForPlan(controller, plan)
			if !ok || state.Status != core.ReconcileConverged || state.AttemptCount != test.wantAttempts {
				t.Fatalf("recovered target ledger = %+v, exists=%v", state, ok)
			}
			if controller.ProbeRequired() {
				t.Fatal("successful recovery retained a stale Probe barrier")
			}
			probes, applies := backend.Counts()
			if probes != test.wantProbeCalls || applies != test.wantApplyCalls {
				t.Fatalf("backend calls: probes=%d applies=%d, want probes=%d applies=%d", probes, applies, test.wantProbeCalls, test.wantApplyCalls)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			// A second reopen proves both the consumed budget and Probe deletion are durable.
			database = openRestartStore(t, ctx, databasePath, clock.Now())
			defer database.Close()
			controller = newPersistentTestController(t, ctx, database, backend, clock, audit)
			setPersistentDesired(t, ctx, database, controller, desired)
			durable, ok := retryStateForPlan(controller, plan)
			if !ok || durable.Status != core.ReconcileConverged || durable.AttemptCount != test.wantAttempts {
				t.Fatalf("second reopen ledger = %+v, exists=%v", durable, ok)
			}
			if controller.ProbeRequired() {
				t.Fatal("second reopen restored a deleted Probe barrier")
			}
			if plan.Domain == fake.DomainTarget {
				if generation, confirmed := controller.ConfirmedTarget(plan.Target); confirmed {
					t.Fatalf("durable retry ledger was treated as physical confirmation at generation %d", generation)
				}
				probesBefore, appliesBefore := backend.Counts()
				startup, startupErr := controller.Execute(ctx, plan)
				if startupErr != nil || !startup.RecoveredByProbe {
					t.Fatalf("startup Probe recovery: result=%+v err=%v", startup, startupErr)
				}
				probesAfter, appliesAfter := backend.Counts()
				if probesAfter != probesBefore+1 || appliesAfter != appliesBefore {
					t.Fatalf("startup recovery order: probes %d->%d applies %d->%d", probesBefore, probesAfter, appliesBefore, appliesAfter)
				}
			}
		})
	}
}

func TestPersistentControllerKeepsAmbiguousProbeAcrossAdministratorRetry(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := fake.NewBackend()
	audit := &memoryAudit{}
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	plan := targetPlan(desired, target)
	databasePath := filepath.Join(t.TempDir(), "guard.db")

	database := openRestartStore(t, ctx, databasePath, clock.Now())
	controller := newPersistentTestController(t, ctx, database, backend, clock, audit)
	setPersistentDesired(t, ctx, database, controller, desired)
	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(ctx, plan); err != nil {
		t.Fatal(err)
	}
	retryKey, err := controller.RetryTarget(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if retryKey.Epoch != 1 || !controller.ProbeRequired() {
		t.Fatalf("administrator retry did not preserve old ambiguity: key=%+v probe=%v", retryKey, controller.ProbeRequired())
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = openRestartStore(t, ctx, databasePath, clock.Now())
	defer database.Close()
	controller = newPersistentTestController(t, ctx, database, backend, clock, audit)
	setPersistentDesired(t, ctx, database, controller, desired)
	key, pending, ok := controller.TargetState(target)
	if !ok || key.Epoch != 1 || pending.Status != core.ReconcilePending || pending.AttemptCount != 0 {
		t.Fatalf("reopened administrator retry ledger: key=%+v state=%+v exists=%v", key, pending, ok)
	}
	if !controller.ProbeRequired() {
		t.Fatal("reopen discarded the older epoch's physical ambiguity")
	}
	result, err := controller.Execute(ctx, plan)
	if err != nil || result.Apply.Kind != fake.ResultConfirmed || result.RecoveredByProbe {
		t.Fatalf("retry Execute(): result=%+v err=%v", result, err)
	}
	key, converged, ok := controller.TargetState(target)
	if !ok || key.Epoch != 1 || converged.Status != core.ReconcileConverged || converged.AttemptCount != 1 {
		t.Fatalf("converged administrator retry ledger: key=%+v state=%+v exists=%v", key, converged, ok)
	}
	if controller.ProbeRequired() {
		t.Fatal("successful administrator retry retained a stale Probe barrier")
	}
	if audit.Count() != 1 {
		t.Fatalf("administrator Retry audit count = %d, want 1", audit.Count())
	}
}

func TestPersistentControllerDoesNotResetExhaustedBudgetAcrossSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := fake.NewBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	plan := targetPlan(desired, target)
	databasePath := filepath.Join(t.TempDir(), "guard.db")

	database := openRestartStore(t, ctx, databasePath, clock.Now())
	controller := newPersistentTestController(t, ctx, database, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, database, controller, desired)
	for attempt := uint32(1); attempt <= maxMutationAttempts; attempt++ {
		if attempt > 1 {
			clock.Advance(retryBackoff[attempt-2])
		}
		if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{
			Kind: fake.ResultUnknown, ErrorCode: "timeout",
		}); err != nil {
			t.Fatal(err)
		}
		result, err := controller.Execute(ctx, plan)
		if err != nil || result.Apply.Kind != fake.ResultUnknown {
			t.Fatalf("attempt %d: result=%+v err=%v", attempt, result, err)
		}
	}
	_, exhausted, ok := controller.TargetState(target)
	if !ok || exhausted.Status != core.ReconcileDegraded || exhausted.AttemptCount != maxMutationAttempts {
		t.Fatalf("pre-reopen exhausted ledger = %+v, exists=%v", exhausted, ok)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = openRestartStore(t, ctx, databasePath, clock.Now())
	defer database.Close()
	controller = newPersistentTestController(t, ctx, database, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, database, controller, desired)
	if _, err := controller.Execute(ctx, plan); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("post-reopen Execute() error = %v, want budget exhausted", err)
	}
	_, durable, ok := controller.TargetState(target)
	if !ok || durable.Status != core.ReconcileDegraded || durable.AttemptCount != maxMutationAttempts {
		t.Fatalf("post-reopen exhausted ledger = %+v, exists=%v", durable, ok)
	}
	probes, applies := backend.Counts()
	if probes != uint64(maxMutationAttempts) || applies != uint64(maxMutationAttempts) {
		t.Fatalf("exhausted recovery crossed seventh mutation: probes=%d applies=%d", probes, applies)
	}
}

func TestPersistentControllerClearsOlderEpochProbeAfterDesiredGenerationAdvances(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := fake.NewBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	desiredV1 := desiredSnapshot(targetIntent(target, 1))
	databasePath := filepath.Join(t.TempDir(), "guard.db")

	database := openRestartStore(t, ctx, databasePath, clock.Now())
	controller := newPersistentTestController(t, ctx, database, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, database, controller, desiredV1)
	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(ctx, targetPlan(desiredV1, target)); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.RetryTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	desiredV2 := desiredV1
	desiredV2.SnapshotRevision = 2
	desiredV2.Targets = []core.NormalizedTargetEnforcementIntent{
		targetIntentWithScope(target, 2, core.ScopeForward),
	}
	database = openRestartStore(t, ctx, databasePath, clock.Now())
	defer database.Close()
	controller = newPersistentTestController(t, ctx, database, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, database, controller, desiredV2)
	result, err := controller.Execute(ctx, targetPlan(desiredV2, target))
	if err != nil || result.Apply.Kind != fake.ResultConfirmed {
		t.Fatalf("generation-advanced Execute(): result=%+v err=%v", result, err)
	}
	key, state, ok := controller.TargetState(target)
	if !ok || key.Generation != 2 || key.Epoch != 0 || state.Status != core.ReconcileConverged || state.AttemptCount != 1 {
		t.Fatalf("generation-advanced ledger: key=%+v state=%+v exists=%v", key, state, ok)
	}
	if controller.ProbeRequired() {
		t.Fatal("stale older-epoch Probe requirement permanently blocked the new generation")
	}
}

func TestPersistentControllerClearsSupersededProbeAfterGenerationAdvancesWithoutReopen(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := fake.NewBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	desiredV1 := desiredSnapshot(targetIntent(target, 1))
	database := openRestartStore(t, ctx, filepath.Join(t.TempDir(), "guard.db"), clock.Now())
	defer database.Close()
	controller := newPersistentTestController(t, ctx, database, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, database, controller, desiredV1)
	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{Kind: fake.ResultUnknown, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(ctx, targetPlan(desiredV1, target)); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.RetryTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	desiredV2 := desiredV1
	desiredV2.SnapshotRevision = 2
	desiredV2.Targets = []core.NormalizedTargetEnforcementIntent{
		targetIntentWithScope(target, 2, core.ScopeForward),
	}
	setPersistentDesired(t, ctx, database, controller, desiredV2)

	result, err := controller.Execute(ctx, targetPlan(desiredV2, target))
	if err != nil || result.Apply.Kind != fake.ResultConfirmed {
		t.Fatalf("same-process generation advance: result=%+v err=%v", result, err)
	}
	key, state, ok := controller.TargetState(target)
	if !ok || key.Generation != 2 || state.Status != core.ReconcileConverged || state.AttemptCount != 1 {
		t.Fatalf("same-process generation ledger: key=%+v state=%+v exists=%v", key, state, ok)
	}
	if controller.ProbeRequired() {
		t.Fatal("same-process superseded Probe permanently blocked the new generation")
	}
}

func TestPersistentControllerResolvesPostApplyCommitUnknownByReadback(t *testing.T) {
	tests := []struct {
		name           string
		commitFinal    bool
		wantFirstError bool
		wantRecovered  bool
	}{
		{name: "commit persisted", commitFinal: true},
		{name: "commit rolled back", commitFinal: false, wantFirstError: true, wantRecovered: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newManualClock()
			clock.Advance(123 * time.Nanosecond)
			backend := fake.NewBackend()
			database := openRestartStore(t, ctx, filepath.Join(t.TempDir(), "guard.db"), clock.Now())
			defer database.Close()
			unknown := &commitUnknownTransitionStore{
				PersistentStateStore: database,
				unknownOnCall:        2,
				commit:               test.commitFinal,
			}
			controller := newPersistentTestController(t, ctx, unknown, backend, clock, &memoryAudit{})
			target := netip.MustParsePrefix("192.0.2.4/32")
			desired := desiredSnapshot(targetIntent(target, 1))
			setPersistentDesired(t, ctx, database, controller, desired)

			first, err := controller.Execute(ctx, targetPlan(desired, target))
			if test.wantFirstError {
				if !errors.Is(err, core.ErrReconcileCommitUnknown) {
					t.Fatalf("first Execute() error = %v, want commit unknown", err)
				}
			} else if err != nil || first.Apply.Kind != fake.ResultConfirmed {
				t.Fatalf("first Execute(): result=%+v err=%v", first, err)
			}
			if test.wantRecovered {
				second, secondErr := controller.Execute(ctx, targetPlan(desired, target))
				if secondErr != nil || !second.RecoveredByProbe {
					t.Fatalf("readback recovery Execute(): result=%+v err=%v", second, secondErr)
				}
			}
			_, state, ok := controller.TargetState(target)
			if !ok || state.Status != core.ReconcileConverged || state.AttemptCount != 1 {
				t.Fatalf("commit-unknown ledger = %+v, exists=%v", state, ok)
			}
			if controller.ProbeRequired() {
				t.Fatal("commit-unknown readback left a stale Probe barrier")
			}
			_, applies := backend.Counts()
			if applies != 1 {
				t.Fatalf("commit-unknown recovery duplicated Apply: %d", applies)
			}
		})
	}
}

func TestPersistentControllerReloadsCommitUnknownWhenImmediateReadbackFails(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := fake.NewBackend()
	database := openRestartStore(t, ctx, filepath.Join(t.TempDir(), "guard.db"), clock.Now())
	defer database.Close()
	unknown := &commitUnknownTransitionStore{
		PersistentStateStore: database,
		unknownOnCall:        2,
		commit:               true,
		failLoadOnCall:       2,
		loadErr:              context.Canceled,
	}
	controller := newPersistentTestController(t, ctx, unknown, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setPersistentDesired(t, ctx, database, controller, desired)

	if _, err := controller.Execute(ctx, targetPlan(desired, target)); !errors.Is(err, core.ErrReconcileCommitUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("first Execute() error = %v, want commit unknown plus canceled readback", err)
	}
	result, err := controller.Execute(ctx, targetPlan(desired, target))
	if err != nil || !result.RecoveredByProbe {
		t.Fatalf("deferred readback Execute(): result=%+v err=%v", result, err)
	}
	_, state, ok := controller.TargetState(target)
	if !ok || state.Status != core.ReconcileConverged || state.AttemptCount != 1 {
		t.Fatalf("deferred readback ledger = %+v, exists=%v", state, ok)
	}
	_, applies := backend.Counts()
	if applies != 1 {
		t.Fatalf("deferred readback duplicated Apply: %d", applies)
	}
}

func TestPersistentControllerRestoresBackoffForPreApplyCrash(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := fake.NewBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	databasePath := filepath.Join(t.TempDir(), "guard.db")
	database := openRestartStore(t, ctx, databasePath, clock.Now())
	lastAttempt := clock.Now()
	state := core.PersistedReconcileState{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget,
		Target: target, TargetGeneration: 1,
		RetryState: core.RetryState{
			Status: core.ReconcileApplying, AttemptCount: 1, LastAttemptAt: &lastAttempt,
		},
		UpdatedAt: lastAttempt,
	}
	probe := core.PersistedProbeRequirement{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget,
		Target: target, TargetGeneration: 1,
		AttemptCount: 1, RecordedAt: lastAttempt,
	}
	if err := database.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: state, UpsertProbe: &probe}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = openRestartStore(t, ctx, databasePath, clock.Now())
	defer database.Close()
	controller := newPersistentTestController(t, ctx, database, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, database, controller, desired)
	if _, err := controller.Execute(ctx, targetPlan(desired, target)); !errors.Is(err, ErrRetryNotReady) {
		t.Fatalf("immediate recovery error = %v, want retry not ready", err)
	}
	_, waiting, ok := controller.TargetState(target)
	wantNext := lastAttempt.Add(time.Second)
	if !ok || waiting.Status != core.ReconcileRetryWaiting || waiting.AttemptCount != 1 ||
		waiting.NextAttemptAt == nil || !waiting.NextAttemptAt.Equal(wantNext) {
		t.Fatalf("restored Applying backoff = %+v, exists=%v, want next=%v", waiting, ok, wantNext)
	}
	_, applies := backend.Counts()
	if applies != 0 {
		t.Fatalf("immediate recovery crossed backoff with %d Apply calls", applies)
	}

	clock.Advance(time.Second)
	result, err := controller.Execute(ctx, targetPlan(desired, target))
	if err != nil || result.Apply.Kind != fake.ResultConfirmed {
		t.Fatalf("elapsed-backoff Execute(): result=%+v err=%v", result, err)
	}
	_, converged, ok := controller.TargetState(target)
	if !ok || converged.Status != core.ReconcileConverged || converged.AttemptCount != 2 {
		t.Fatalf("elapsed-backoff ledger = %+v, exists=%v", converged, ok)
	}
}

func TestPersistentControllerRestoresExhaustedPreApplyCrashAsDegraded(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := fake.NewBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	databasePath := filepath.Join(t.TempDir(), "guard.db")
	database := openRestartStore(t, ctx, databasePath, clock.Now())
	lastAttempt := clock.Now()
	state := core.PersistedReconcileState{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget,
		Target: target, TargetGeneration: 1,
		RetryState: core.RetryState{
			Status: core.ReconcileApplying, AttemptCount: maxMutationAttempts, LastAttemptAt: &lastAttempt,
		},
		UpdatedAt: lastAttempt,
	}
	probe := core.PersistedProbeRequirement{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget,
		Target: target, TargetGeneration: 1,
		AttemptCount: maxMutationAttempts, RecordedAt: lastAttempt,
	}
	if err := database.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: state, UpsertProbe: &probe}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = openRestartStore(t, ctx, databasePath, clock.Now())
	defer database.Close()
	controller := newPersistentTestController(t, ctx, database, backend, clock, &memoryAudit{})
	setPersistentDesired(t, ctx, database, controller, desired)
	if _, err := controller.Execute(ctx, targetPlan(desired, target)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("exhausted Applying recovery error = %v", err)
	}
	_, degraded, ok := controller.TargetState(target)
	if !ok || degraded.Status != core.ReconcileDegraded || degraded.AttemptCount != maxMutationAttempts {
		t.Fatalf("exhausted Applying recovery ledger = %+v, exists=%v", degraded, ok)
	}
	_, applies := backend.Counts()
	if applies != 0 {
		t.Fatalf("exhausted Applying recovery made %d mutation calls", applies)
	}
}

func TestPersistentControllerDoesNotApplyWhenPreMutationPersistenceFails(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := fake.NewBackend()
	database := openRestartStore(t, ctx, filepath.Join(t.TempDir(), "guard.db"), clock.Now())
	defer database.Close()
	failing := &failingTransitionStore{PersistentStateStore: database, err: errors.New("disk unavailable")}
	controller := newPersistentTestController(t, ctx, failing, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.4/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setPersistentDesired(t, ctx, database, controller, desired)

	if _, err := controller.Execute(ctx, targetPlan(desired, target)); err == nil {
		t.Fatal("Execute() succeeded despite failed pre-mutation persistence")
	}
	probes, applies := backend.Counts()
	if probes != 0 || applies != 0 {
		t.Fatalf("persistence failure crossed external boundary: probes=%d applies=%d", probes, applies)
	}
}

func TestPersistentControllerPersistsCompleteObservedAndProbeFailureUnknown(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := newHealthFlapBackend()
	database := openRestartStore(t, ctx, filepath.Join(t.TempDir(), "guard.db"), clock.Now())
	defer database.Close()
	controller := newPersistentTestController(t, ctx, database, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("192.0.2.40/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setPersistentDesired(t, ctx, database, controller, desired)

	for _, plan := range []fake.OperationPlan{
		infrastructurePlan(desired),
		policyPlan(desired),
		targetPlan(desired, target),
	} {
		result, err := controller.Execute(ctx, plan)
		if err != nil || result.Apply.Kind != fake.ResultConfirmed {
			t.Fatalf("converge domain %d: result=%+v err=%v", plan.Domain, result, err)
		}
	}

	observed, err := database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Infrastructure == nil || observed.Infrastructure.Presence != core.ObservedPresencePresent ||
		observed.Infrastructure.ConfirmedRevision != desired.InfrastructureRevision {
		t.Fatalf("infrastructure Observed = %+v", observed.Infrastructure)
	}
	if observed.Policy == nil || observed.Policy.Presence != core.ObservedPresencePresent ||
		observed.Policy.ConfirmedRevision != desired.PolicyRevision {
		t.Fatalf("policy Observed = %+v", observed.Policy)
	}
	if len(observed.Targets) != 1 || observed.Targets[0].BanMembership != core.ObservedMembershipPresent ||
		observed.Targets[0].ConfirmedGeneration != desired.Targets[0].Generation {
		t.Fatalf("target Observed = %+v", observed.Targets)
	}

	backend.healthy.Store(false)
	outcome, err := controller.probeRecovery(ctx, backendHealthProbeTimeout)
	if err != nil || !errors.Is(outcome.backendErr, errBackendUnavailable) {
		t.Fatalf("failed recovery Probe outcome = %+v, error = %v", outcome, err)
	}
	observed, err = database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Infrastructure == nil || observed.Infrastructure.Presence != core.ObservedPresenceUnknown ||
		observed.Infrastructure.ConfirmedRevision != 0 || observed.Infrastructure.LastErrorCode != probeFailureErrorCode {
		t.Fatalf("failed-Probe infrastructure Observed = %+v", observed.Infrastructure)
	}
	if observed.Policy == nil || observed.Policy.Presence != core.ObservedPresenceUnknown ||
		observed.Policy.ConfirmedRevision != 0 || observed.Policy.LastErrorCode != probeFailureErrorCode {
		t.Fatalf("failed-Probe policy Observed = %+v", observed.Policy)
	}
	if len(observed.Targets) != 1 || observed.Targets[0].BanMembership != core.ObservedMembershipUnknown ||
		observed.Targets[0].ConfirmedGeneration != 0 || observed.Targets[0].LastErrorCode != probeFailureErrorCode {
		t.Fatalf("failed-Probe target Observed = %+v", observed.Targets)
	}
}

func TestPersistentControllerPersistsAmbiguousApplyAndScopedUnknownAtomically(t *testing.T) {
	ctx := context.Background()
	clock := newManualClock()
	backend := fake.NewBackend()
	database := openRestartStore(t, ctx, filepath.Join(t.TempDir(), "guard.db"), clock.Now())
	defer database.Close()
	controller := newPersistentTestController(t, ctx, database, backend, clock, &memoryAudit{})
	target := netip.MustParsePrefix("198.51.100.40/32")
	desired := desiredSnapshot(targetIntent(target, 1))
	setPersistentDesired(t, ctx, database, controller, desired)
	if err := backend.QueueOutcome(fake.DomainTarget, fake.QueuedOutcome{
		Kind: fake.ResultUnknown, ErrorCode: "timeout_after_dispatch",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := controller.Execute(ctx, targetPlan(desired, target))
	if err != nil || result.Apply.Kind != fake.ResultUnknown {
		t.Fatalf("ambiguous Execute: result=%+v err=%v", result, err)
	}
	observed, err := database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Infrastructure != nil || observed.Policy != nil {
		t.Fatalf("domain-scoped ambiguity overwrote unrelated Observed state: %+v", observed)
	}
	if len(observed.Targets) != 1 || observed.Targets[0].CanonicalTarget != target ||
		observed.Targets[0].BanMembership != core.ObservedMembershipUnknown ||
		observed.Targets[0].LastErrorCode != "timeout_after_dispatch" {
		t.Fatalf("ambiguous target Observed = %+v", observed.Targets)
	}
	recovery, err := database.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery.States) != 1 || recovery.States[0].RetryState.Status != core.ReconcileRetryWaiting ||
		len(recovery.ProbeRequirements) != 1 {
		t.Fatalf("ambiguous transition recovery = %+v", recovery)
	}
}

type failingTransitionStore struct {
	PersistentStateStore
	err error
}

type commitUnknownTransitionStore struct {
	PersistentStateStore
	unknownOnCall  int
	commit         bool
	calls          int
	failLoadOnCall int
	loadErr        error
	loadCalls      int
}

func (s *commitUnknownTransitionStore) ApplyReconcileTransition(ctx context.Context, transition core.ReconcileStateTransition) error {
	s.calls++
	if s.calls != s.unknownOnCall {
		return s.PersistentStateStore.ApplyReconcileTransition(ctx, transition)
	}
	if s.commit {
		if err := s.PersistentStateStore.ApplyReconcileTransition(ctx, transition); err != nil {
			return err
		}
	}
	return core.NewReconcileCommitUnknownError(errors.New("injected lost commit acknowledgement"))
}

func (s *commitUnknownTransitionStore) LoadReconcileRecovery(ctx context.Context, nodeID core.NodeID) (core.ReconcileRecoverySnapshot, error) {
	s.loadCalls++
	if s.loadCalls == s.failLoadOnCall {
		return core.ReconcileRecoverySnapshot{}, s.loadErr
	}
	return s.PersistentStateStore.LoadReconcileRecovery(ctx, nodeID)
}

func (s *failingTransitionStore) ApplyReconcileTransition(context.Context, core.ReconcileStateTransition) error {
	return s.err
}

func openRestartStore(t *testing.T, ctx context.Context, path string, createdAt time.Time) *storepkg.Store {
	t.Helper()
	database, err := storepkg.Open(ctx, path, os.DirFS(filepath.Join("..", "..", "migrations")))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureNodeIdentity(ctx, testNodeID, createdAt); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database
}

func setPersistentDesired(
	t *testing.T,
	ctx context.Context,
	database *storepkg.Store,
	controller *Controller,
	desired core.DesiredFirewallSnapshot,
) {
	t.Helper()
	if len(desired.Targets) != 0 {
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, intent := range desired.Targets {
			if err := uow.PutTargetEnforcementIntent(ctx, intent); err != nil {
				_ = uow.Rollback()
				t.Fatal(err)
			}
		}
		if err := uow.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	setDesired(t, controller, desired)
}

func newPersistentTestController(
	t *testing.T,
	ctx context.Context,
	persistence PersistentStateStore,
	backend Backend,
	clock Clock,
	audit CriticalAuditWriter,
) *Controller {
	t.Helper()
	controller, err := NewPersistentController(ctx, testNodeID, backend, clock, audit, persistence)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func persistentRecoveryPlan(desired core.DesiredFirewallSnapshot, domain fake.Domain) fake.OperationPlan {
	switch domain {
	case fake.DomainInfrastructure:
		return infrastructurePlan(desired)
	case fake.DomainPolicy:
		return policyPlan(desired)
	case fake.DomainTarget:
		return targetPlan(desired, desired.Targets[0].CanonicalTarget)
	default:
		panic("unsupported recovery domain")
	}
}

func retryStateForPlan(controller *Controller, plan fake.OperationPlan) (core.RetryState, bool) {
	switch plan.Domain {
	case fake.DomainInfrastructure:
		_, state, ok := controller.InfrastructureState()
		return state, ok
	case fake.DomainPolicy:
		_, state, ok := controller.PolicyState()
		return state, ok
	case fake.DomainTarget:
		_, state, ok := controller.TargetState(plan.Target)
		return state, ok
	default:
		return core.RetryState{}, false
	}
}
