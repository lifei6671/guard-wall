package store

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestReconcileRecoveryRoundTripAcrossCloseAndReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "guard.db")
	store, err := Open(ctx, databasePath, migrationFileSystem())
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 123000000, time.UTC)
	if err := store.EnsureNodeIdentity(ctx, testNodeID, now); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}

	target := netip.MustParsePrefix("192.0.2.44/32")
	lastAttempt := now.Add(time.Second)
	nextAttempt := now.Add(6 * time.Second)
	tests := []struct {
		name  string
		state core.PersistedReconcileState
		probe core.PersistedProbeRequirement
	}{
		{
			name:  "infrastructure",
			state: persistedRetryState(core.ReconcileDomainInfrastructure, target, lastAttempt, nextAttempt),
			probe: core.PersistedProbeRequirement{
				NodeID: testNodeID, Domain: core.ReconcileDomainInfrastructure,
				InfrastructureRevision: 11, SnapshotRevision: 19, FenceSnapshotRevision: true,
				RetryEpoch: 2, AttemptCount: 2, RecordedAt: now.Add(2 * time.Second),
			},
		},
		{
			name:  "policy",
			state: persistedRetryState(core.ReconcileDomainPolicy, target, lastAttempt, nextAttempt),
			probe: core.PersistedProbeRequirement{
				NodeID: testNodeID, Domain: core.ReconcileDomainPolicy, PolicyRevision: 12,
				RetryEpoch: 2, AttemptCount: 2, RecordedAt: now.Add(3 * time.Second),
			},
		},
		{
			name:  "target",
			state: persistedRetryState(core.ReconcileDomainTarget, target, lastAttempt, nextAttempt),
			probe: core.PersistedProbeRequirement{
				NodeID: testNodeID, Domain: core.ReconcileDomainTarget, Target: target,
				TargetGeneration: 13, RetryEpoch: 2, AttemptCount: 2,
				RecordedAt: now.Add(4 * time.Second),
			},
		},
	}

	want := core.ReconcileRecoverySnapshot{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{
				State: test.state, UpsertProbe: &test.probe,
			}); err != nil {
				t.Fatalf("ApplyReconcileTransition(): %v", err)
			}
		})
		want.States = append(want.States, test.state)
		want.ProbeRequirements = append(want.ProbeRequirements, test.probe)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	reopened, err := Open(ctx, databasePath, migrationFileSystem())
	if err != nil {
		t.Fatalf("Open() after close: %v", err)
	}
	defer closeStore(t, reopened)
	got, err := reopened.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadReconcileRecovery(): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recovery snapshot = %#v, want %#v", got, want)
	}
}

func TestApplyReconcileTransitionDeletesOnlyExactProbeAtomically(t *testing.T) {
	store := openReconcileTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC)
	target := netip.MustParsePrefix("198.51.100.8/32")
	state := persistedRetryState(core.ReconcileDomainTarget, target, now, now.Add(time.Second))
	probe := core.PersistedProbeRequirement{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget, Target: target,
		TargetGeneration: 13, RetryEpoch: 2, AttemptCount: 2, RecordedAt: now,
	}
	if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: state, UpsertProbe: &probe}); err != nil {
		t.Fatalf("upsert transition: %v", err)
	}

	missing := probe
	missing.AttemptCount = 1
	advanced := state
	advanced.RetryState.Status = core.ReconcileDegraded
	advanced.RetryState.AttemptCount = 3
	advanced.RetryState.NextAttemptAt = nil
	advanced.UpdatedAt = now.Add(2 * time.Second)
	if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: advanced, DeleteProbe: &missing}); err == nil {
		t.Fatal("delete missing exact probe succeeded")
	}
	got, err := store.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadReconcileRecovery() after rollback: %v", err)
	}
	if len(got.States) != 1 || got.States[0].RetryState.AttemptCount != 2 || len(got.ProbeRequirements) != 1 {
		t.Fatalf("rollback snapshot = %#v", got)
	}

	replacement := probe
	replacement.AttemptCount = 3
	replacement.RecordedAt = now.Add(3 * time.Second)
	if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{
		State: advanced, UpsertProbe: &replacement,
	}); err == nil {
		t.Fatal("replacement without exact delete succeeded")
	}
	got, err = store.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadReconcileRecovery() after rejected replacement: %v", err)
	}
	if len(got.ProbeRequirements) != 1 || got.ProbeRequirements[0].AttemptCount != 2 ||
		len(got.States) != 1 || got.States[0].RetryState.AttemptCount != 2 {
		t.Fatalf("rejected replacement snapshot = %#v", got)
	}

	if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{
		State: advanced, UpsertProbe: &replacement, DeleteProbe: &probe,
	}); err != nil {
		t.Fatalf("replace probe transition: %v", err)
	}
	got, err = store.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadReconcileRecovery() after replace: %v", err)
	}
	if len(got.ProbeRequirements) != 1 || got.ProbeRequirements[0].AttemptCount != 3 ||
		len(got.States) != 1 || got.States[0].RetryState.AttemptCount != 3 {
		t.Fatalf("replacement snapshot = %#v", got)
	}

	converged := advanced
	converged.RetryState.Status = core.ReconcileConverged
	converged.RetryState.NextAttemptAt = nil
	converged.UpdatedAt = now.Add(4 * time.Second)
	if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: converged, DeleteProbe: &replacement}); err != nil {
		t.Fatalf("delete transition: %v", err)
	}
	got, err = store.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadReconcileRecovery() after delete: %v", err)
	}
	if len(got.ProbeRequirements) != 0 || len(got.States) != 1 || got.States[0].RetryState.Status != core.ReconcileConverged {
		t.Fatalf("deleted snapshot = %#v", got)
	}
}

func TestApplyReconcileTransitionDeleteOnlyPreservesNewerLedger(t *testing.T) {
	store := openReconcileTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Date(2026, 8, 30, 13, 20, 0, 0, time.UTC)
	target := netip.MustParsePrefix("198.51.100.9/32")
	lastAttempt := now
	nextAttempt := lastAttempt.Add(time.Second)
	oldState := core.PersistedReconcileState{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget,
		Target: target, TargetGeneration: 13,
		RetryState: core.RetryState{
			Status: core.ReconcileRetryWaiting, AttemptCount: 1,
			LastAttemptAt: &lastAttempt, NextAttemptAt: &nextAttempt,
		},
		UpdatedAt: now,
	}
	oldProbe := core.PersistedProbeRequirement{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget,
		Target: target, TargetGeneration: 13,
		AttemptCount: 1, RecordedAt: now,
	}
	if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: oldState, UpsertProbe: &oldProbe}); err != nil {
		t.Fatalf("seed old requirement: %v", err)
	}
	newState := oldState
	newState.RetryEpoch = 1
	newState.RetryState = core.RetryState{Status: core.ReconcilePending}
	newState.UpdatedAt = now.Add(time.Second)
	if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: newState}); err != nil {
		t.Fatalf("write newer ledger: %v", err)
	}
	if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{
		DeleteProbe: &oldProbe,
		DeleteOnly:  true,
	}); err != nil {
		t.Fatalf("delete stale old requirement: %v", err)
	}

	got, err := store.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ProbeRequirements) != 0 || len(got.States) != 1 ||
		got.States[0].RetryEpoch != 1 || got.States[0].RetryState.Status != core.ReconcilePending {
		t.Fatalf("delete-only transition changed newer ledger: %#v", got)
	}
}

func TestApplyReconcileTransitionPersistsApplyingProbeBeforeBackend(t *testing.T) {
	store := openReconcileTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Date(2026, 8, 30, 13, 30, 0, 0, time.UTC)
	target := netip.MustParsePrefix("198.51.100.18/32")
	state := persistedRetryState(core.ReconcileDomainTarget, target, now, now.Add(time.Second))
	state.RetryState.Status = core.ReconcileApplying
	state.RetryState.NextAttemptAt = nil
	probe := core.PersistedProbeRequirement{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget, Target: target,
		TargetGeneration: 13, RetryEpoch: 2, AttemptCount: 2, RecordedAt: now,
	}
	if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{
		State: state, UpsertProbe: &probe,
	}); err != nil {
		t.Fatalf("ApplyReconcileTransition(): %v", err)
	}
	got, err := store.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadReconcileRecovery(): %v", err)
	}
	if len(got.States) != 1 || got.States[0].RetryState.Status != core.ReconcileApplying ||
		len(got.ProbeRequirements) != 1 || got.ProbeRequirements[0].AttemptCount != 2 {
		t.Fatalf("applying recovery snapshot = %#v", got)
	}
}

func TestApplyReconcileTransitionRejectsStaleVersionComponents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target := netip.MustParsePrefix("203.0.113.9/32")
	now := time.Date(2026, 8, 30, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*core.PersistedReconcileState)
	}{
		{name: "old key", mutate: func(state *core.PersistedReconcileState) { state.TargetGeneration = 12 }},
		{name: "old epoch", mutate: func(state *core.PersistedReconcileState) { state.RetryEpoch = 1 }},
		{name: "old attempt", mutate: func(state *core.PersistedReconcileState) { state.RetryState.AttemptCount = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openReconcileTestStore(t)
			current := persistedRetryState(core.ReconcileDomainTarget, target, now, now.Add(time.Second))
			if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: current}); err != nil {
				t.Fatalf("seed state: %v", err)
			}
			stale := current
			stale.UpdatedAt = now.Add(time.Second)
			test.mutate(&stale)
			if err := store.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: stale}); !errors.Is(err, ErrReconcileStateRegression) {
				t.Fatalf("ApplyReconcileTransition() error = %v, want regression", err)
			}
			got, err := store.LoadReconcileRecovery(ctx, testNodeID)
			if err != nil {
				t.Fatalf("LoadReconcileRecovery(): %v", err)
			}
			if len(got.States) != 1 || got.States[0].TargetGeneration != current.TargetGeneration ||
				got.States[0].RetryEpoch != current.RetryEpoch ||
				got.States[0].RetryState.AttemptCount != current.RetryState.AttemptCount {
				t.Fatalf("state changed after regression: %#v", got)
			}
		})
	}
}

func TestApplyReconcileTransitionRejectsInvalidInput(t *testing.T) {
	store := openReconcileTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Date(2026, 8, 30, 15, 0, 0, 0, time.UTC)
	target := netip.MustParsePrefix("192.0.2.9/32")
	valid := persistedRetryState(core.ReconcileDomainTarget, target, now, now.Add(time.Second))
	validProbe := core.PersistedProbeRequirement{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget, Target: target,
		TargetGeneration: 13, RetryEpoch: 2, AttemptCount: 2, RecordedAt: now,
	}
	tests := []struct {
		name       string
		transition core.ReconcileStateTransition
	}{
		{name: "missing node", transition: func() core.ReconcileStateTransition {
			item := valid
			item.NodeID = ""
			return core.ReconcileStateTransition{State: item}
		}()},
		{name: "mixed domain key", transition: func() core.ReconcileStateTransition {
			item := valid
			item.PolicyRevision = 1
			return core.ReconcileStateTransition{State: item}
		}()},
		{name: "uncanonical target", transition: func() core.ReconcileStateTransition {
			item := valid
			item.Target = netip.MustParsePrefix("192.0.2.9/24")
			return core.ReconcileStateTransition{State: item}
		}()},
		{name: "sqlite overflow", transition: func() core.ReconcileStateTransition {
			item := valid
			item.TargetGeneration = core.TargetEnforcementGeneration(uint64(math.MaxInt64) + 1)
			return core.ReconcileStateTransition{State: item}
		}()},
		{name: "invalid status", transition: func() core.ReconcileStateTransition {
			item := valid
			item.RetryState.Status = 99
			return core.ReconcileStateTransition{State: item}
		}()},
		{name: "state attempt exceeds budget", transition: func() core.ReconcileStateTransition {
			item := valid
			item.RetryState.AttemptCount = 7
			return core.ReconcileStateTransition{State: item}
		}()},
		{name: "retry waiting without next time", transition: func() core.ReconcileStateTransition {
			item := valid
			item.RetryState.NextAttemptAt = nil
			return core.ReconcileStateTransition{State: item}
		}()},
		{name: "probe attempt mismatch", transition: func() core.ReconcileStateTransition {
			probe := validProbe
			probe.AttemptCount = 1
			return core.ReconcileStateTransition{State: valid, UpsertProbe: &probe}
		}()},
		{name: "probe attempt exceeds budget", transition: func() core.ReconcileStateTransition {
			probe := validProbe
			probe.AttemptCount = 7
			return core.ReconcileStateTransition{State: valid, UpsertProbe: &probe}
		}()},
		{name: "next attempt does not follow last attempt", transition: func() core.ReconcileStateTransition {
			item := valid
			item.RetryState.NextAttemptAt = item.RetryState.LastAttemptAt
			return core.ReconcileStateTransition{State: item}
		}()},
		{name: "snapshot without fence", transition: func() core.ReconcileStateTransition {
			state := valid
			state.Domain = core.ReconcileDomainInfrastructure
			state.Target = netip.Prefix{}
			state.TargetGeneration = 0
			state.InfrastructureRevision = 13
			probe := validProbe
			probe.Domain = core.ReconcileDomainInfrastructure
			probe.Target = netip.Prefix{}
			probe.TargetGeneration = 0
			probe.InfrastructureRevision = 13
			probe.SnapshotRevision = 2
			return core.ReconcileStateTransition{State: state, UpsertProbe: &probe}
		}()},
		{name: "snapshot fence without revision", transition: func() core.ReconcileStateTransition {
			state := valid
			state.Domain = core.ReconcileDomainInfrastructure
			state.Target = netip.Prefix{}
			state.TargetGeneration = 0
			state.InfrastructureRevision = 13
			probe := validProbe
			probe.Domain = core.ReconcileDomainInfrastructure
			probe.Target = netip.Prefix{}
			probe.TargetGeneration = 0
			probe.InfrastructureRevision = 13
			probe.FenceSnapshotRevision = true
			return core.ReconcileStateTransition{State: state, UpsertProbe: &probe}
		}()},
		{name: "replacement snapshot mismatch", transition: func() core.ReconcileStateTransition {
			state := valid
			state.Domain = core.ReconcileDomainInfrastructure
			state.Target = netip.Prefix{}
			state.TargetGeneration = 0
			state.InfrastructureRevision = 13
			upsert := validProbe
			upsert.Domain = core.ReconcileDomainInfrastructure
			upsert.Target = netip.Prefix{}
			upsert.TargetGeneration = 0
			upsert.InfrastructureRevision = 13
			upsert.SnapshotRevision = 2
			upsert.FenceSnapshotRevision = true
			deleted := upsert
			deleted.SnapshotRevision = 3
			return core.ReconcileStateTransition{State: state, UpsertProbe: &upsert, DeleteProbe: &deleted}
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := store.ApplyReconcileTransition(ctx, test.transition); err == nil {
				t.Fatal("ApplyReconcileTransition() succeeded")
			}
		})
	}
	if err := store.ApplyReconcileTransition(nil, core.ReconcileStateTransition{State: valid}); err == nil {
		t.Fatal("ApplyReconcileTransition(nil) succeeded")
	}
	if _, err := store.LoadReconcileRecovery(ctx, "bad-node"); err == nil {
		t.Fatal("LoadReconcileRecovery() accepted invalid node")
	}
}

func persistedRetryState(domain core.ReconcileDomain, target netip.Prefix, lastAttempt, nextAttempt time.Time) core.PersistedReconcileState {
	state := core.PersistedReconcileState{
		NodeID: testNodeID, Domain: domain, RetryEpoch: 2,
		RetryState: core.RetryState{
			Status: core.ReconcileRetryWaiting, AttemptCount: 2,
			LastAttemptAt: &lastAttempt, NextAttemptAt: &nextAttempt, LastErrorCode: "backend_unavailable",
		},
		UpdatedAt: lastAttempt,
	}
	switch domain {
	case core.ReconcileDomainInfrastructure:
		state.InfrastructureRevision = 11
	case core.ReconcileDomainPolicy:
		state.PolicyRevision = 12
	case core.ReconcileDomainTarget:
		state.Target = target
		state.TargetGeneration = 13
	}
	return state
}

func openReconcileTestStore(t *testing.T) *Store {
	t.Helper()
	store := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.EnsureNodeIdentity(ctx, testNodeID, time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	return store
}
