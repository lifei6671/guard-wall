package store

import (
	"context"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestSQLiteObservedFirewallSchemaRejectsClaimWithoutObservedTime(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "guard.db"), migrationFileSystem())
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer database.Close()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	target := netip.MustParsePrefix("192.0.2.99/32")
	seedObservedDesiredTargets(t, database, map[netip.Prefix]core.TargetEnforcementGeneration{target: 1})

	tests := []struct {
		name       string
		membership string
		backend    string
		coverage   string
		scopes     int
		family     int
		owner      string
	}{
		{name: "absent", membership: "absent", coverage: "unknown"},
		{name: "present", membership: "present", backend: "fake", coverage: "none", scopes: 1, family: 4, owner: "v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := database.db.ExecContext(ctx, `
				UPDATE enforcement_states
				SET observed_membership = ?, observed_at_us = NULL,
					observed_backend = ?, observed_policy_coverage = ?,
					observed_scopes = ?, observed_address_family = ?,
					observed_owner_version = ?
				WHERE node_id = ? AND canonical_target = ?`,
				test.membership, test.backend, test.coverage, test.scopes,
				test.family, test.owner, string(testNodeID), target.String())
			if err == nil {
				t.Fatal("schema accepted an Observed claim without observed_at_us")
			}
		})
	}
}

func TestSQLiteObservedFirewallRoundTripAcrossReopen(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "guard.db")
	database, err := Open(ctx, databasePath, migrationFileSystem())
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close(cleanup): %v", err)
		}
	})
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}

	unknownTarget := netip.MustParsePrefix("192.0.2.1/32")
	absentTarget := netip.MustParsePrefix("198.51.100.1/32")
	presentTarget := netip.MustParsePrefix("2001:db8::1/128")
	seedObservedDesiredTargets(t, database, map[netip.Prefix]core.TargetEnforcementGeneration{
		unknownTarget: 1,
		absentTarget:  2,
		presentTarget: 3,
	})
	now := time.Unix(2_000, 123_000).UTC()
	nativeExpiry := now.Add(30 * time.Minute)
	want := core.ObservedFirewallSnapshot{
		NodeID: testNodeID,
		Infrastructure: &core.InfrastructureObservedState{
			Presence: core.ObservedPresencePresent, ObservedAt: now,
			Backend: "fake", OwnerVersion: "v1", Digest: "infra-digest", ConfirmedRevision: 7,
		},
		Policy: &core.PolicyObservedState{
			Presence: core.ObservedPresenceAbsent, ObservedAt: now,
		},
		Targets: []core.TargetObservedState{
			{
				PhysicalTargetObserved: core.PhysicalTargetObserved{
					CanonicalTarget: unknownTarget, ObservedAt: now,
					BanMembership:  core.ObservedMembershipUnknown,
					PolicyCoverage: core.ObservedPolicyUnknown,
					TimeoutMode:    core.TimeoutNone, LastErrorCode: "probe_failed",
				},
			},
			{
				PhysicalTargetObserved: core.PhysicalTargetObserved{
					CanonicalTarget: absentTarget, ObservedAt: now,
					BanMembership:  core.ObservedMembershipAbsent,
					PolicyCoverage: core.ObservedPolicyUnknown,
					TimeoutMode:    core.TimeoutNone,
				},
				ConfirmedGeneration: 2,
			},
			{
				PhysicalTargetObserved: core.PhysicalTargetObserved{
					CanonicalTarget: presentTarget, ObservedAt: now, Backend: "fake",
					BanMembership:        core.ObservedMembershipPresent,
					PolicyCoverage:       core.ObservedPolicyPartial,
					PolicyRelationDigest: "policy-digest", TimeoutMode: core.TimeoutNative,
					NativeExpiry: &nativeExpiry, Scopes: core.ScopeInput | core.ScopeForward,
					AddressFamily: core.AddressFamilyIPv6, OwnerVersion: "v1",
				},
				ConfirmedGeneration: 3,
			},
		},
	}
	update := core.ObservedFirewallUpdate{
		NodeID: testNodeID, Infrastructure: want.Infrastructure, Policy: want.Policy,
		// Deliberately reverse the input to prove stable load ordering.
		Targets: []core.TargetObservedState{want.Targets[2], want.Targets[1], want.Targets[0]},
	}
	if err := database.ApplyObservedFirewallUpdate(ctx, update); err != nil {
		t.Fatalf("ApplyObservedFirewallUpdate(): %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	database, err = Open(ctx, databasePath, migrationFileSystem())
	if err != nil {
		t.Fatalf("Open(reopen): %v", err)
	}
	got, err := database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadObservedFirewallSnapshot(): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %#v, want %#v", got, want)
	}
}

func TestSQLiteObservedFirewallRejectsTimeRegressionAndSameTimeConflict(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	target := netip.MustParsePrefix("192.0.2.8/32")
	seedObservedDesiredTargets(t, database, map[netip.Prefix]core.TargetEnforcementGeneration{target: 4})
	now := time.Unix(3_000, 0).UTC()
	baseline := core.ObservedFirewallUpdate{
		NodeID: testNodeID,
		Infrastructure: &core.InfrastructureObservedState{
			Presence: core.ObservedPresencePresent, ObservedAt: now,
			Backend: "fake", OwnerVersion: "v1", Digest: "infra", ConfirmedRevision: 2,
		},
		Policy: &core.PolicyObservedState{
			Presence: core.ObservedPresencePresent, ObservedAt: now,
			RelationDigest: "policy", ConfirmedRevision: 3,
		},
		Targets: []core.TargetObservedState{{
			PhysicalTargetObserved: core.PhysicalTargetObserved{
				CanonicalTarget: target, ObservedAt: now,
				BanMembership:  core.ObservedMembershipAbsent,
				PolicyCoverage: core.ObservedPolicyUnknown, TimeoutMode: core.TimeoutNone,
			},
			ConfirmedGeneration: 4,
		}},
	}
	if err := database.ApplyObservedFirewallUpdate(ctx, baseline); err != nil {
		t.Fatalf("ApplyObservedFirewallUpdate(baseline): %v", err)
	}
	if err := database.ApplyObservedFirewallUpdate(ctx, baseline); err != nil {
		t.Fatalf("ApplyObservedFirewallUpdate(exact replay): %v", err)
	}
	want, err := database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadObservedFirewallSnapshot(): %v", err)
	}

	tests := []struct {
		name      string
		update    core.ObservedFirewallUpdate
		wantError string
	}{
		{
			name: "infrastructure older",
			update: core.ObservedFirewallUpdate{NodeID: testNodeID, Infrastructure: &core.InfrastructureObservedState{
				Presence: core.ObservedPresenceAbsent, ObservedAt: now.Add(-time.Second),
			}},
			wantError: "observed time would regress",
		},
		{
			name: "infrastructure same time differs",
			update: core.ObservedFirewallUpdate{NodeID: testNodeID, Infrastructure: &core.InfrastructureObservedState{
				Presence: core.ObservedPresencePresent, ObservedAt: now,
				Backend: "fake", OwnerVersion: "v1", Digest: "different", ConfirmedRevision: 2,
			}},
			wantError: "same observed time",
		},
		{
			name: "policy older",
			update: core.ObservedFirewallUpdate{NodeID: testNodeID, Policy: &core.PolicyObservedState{
				Presence: core.ObservedPresenceAbsent, ObservedAt: now.Add(-time.Second),
			}},
			wantError: "observed time would regress",
		},
		{
			name: "policy same time differs",
			update: core.ObservedFirewallUpdate{NodeID: testNodeID, Policy: &core.PolicyObservedState{
				Presence: core.ObservedPresencePresent, ObservedAt: now,
				RelationDigest: "different", ConfirmedRevision: 3,
			}},
			wantError: "same observed time",
		},
		{
			name: "target older",
			update: core.ObservedFirewallUpdate{NodeID: testNodeID, Targets: []core.TargetObservedState{{
				PhysicalTargetObserved: core.PhysicalTargetObserved{
					CanonicalTarget: target, ObservedAt: now.Add(-time.Second),
					BanMembership:  core.ObservedMembershipAbsent,
					PolicyCoverage: core.ObservedPolicyUnknown, TimeoutMode: core.TimeoutNone,
				},
				ConfirmedGeneration: 4,
			}}},
			wantError: "observed time would regress",
		},
		{
			name: "target same time differs",
			update: core.ObservedFirewallUpdate{NodeID: testNodeID, Targets: []core.TargetObservedState{{
				PhysicalTargetObserved: core.PhysicalTargetObserved{
					CanonicalTarget: target, ObservedAt: now,
					BanMembership:  core.ObservedMembershipAbsent,
					PolicyCoverage: core.ObservedPolicyUnknown, TimeoutMode: core.TimeoutNone,
				},
			}}},
			wantError: "same observed time",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := database.ApplyObservedFirewallUpdate(ctx, test.update)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ApplyObservedFirewallUpdate() error = %v, want containing %q", err, test.wantError)
			}
			got, loadErr := database.LoadObservedFirewallSnapshot(ctx, testNodeID)
			if loadErr != nil {
				t.Fatalf("LoadObservedFirewallSnapshot(): %v", loadErr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("snapshot changed after rejected write: %#v", got)
			}
		})
	}
}

func TestSQLiteObservedFirewallAcceptsStrictlyNewerObservations(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	target := netip.MustParsePrefix("192.0.2.18/32")
	seedObservedDesiredTargets(t, database, map[netip.Prefix]core.TargetEnforcementGeneration{target: 6})
	firstTime := time.Unix(3_500, 0).UTC()
	first := core.ObservedFirewallUpdate{
		NodeID: testNodeID,
		Infrastructure: &core.InfrastructureObservedState{
			Presence: core.ObservedPresenceAbsent, ObservedAt: firstTime,
		},
		Policy: &core.PolicyObservedState{
			Presence: core.ObservedPresenceAbsent, ObservedAt: firstTime,
		},
		Targets: []core.TargetObservedState{{
			PhysicalTargetObserved: core.PhysicalTargetObserved{
				CanonicalTarget: target, ObservedAt: firstTime,
				BanMembership:  core.ObservedMembershipAbsent,
				PolicyCoverage: core.ObservedPolicyUnknown, TimeoutMode: core.TimeoutNone,
			},
			ConfirmedGeneration: 6,
		}},
	}
	if err := database.ApplyObservedFirewallUpdate(ctx, first); err != nil {
		t.Fatalf("ApplyObservedFirewallUpdate(first): %v", err)
	}

	newerTime := firstTime.Add(time.Microsecond)
	newer := core.ObservedFirewallUpdate{
		NodeID: testNodeID,
		Infrastructure: &core.InfrastructureObservedState{
			Presence: core.ObservedPresencePresent, ObservedAt: newerTime,
			Backend: "fake", OwnerVersion: "v2", Digest: "infra-new", ConfirmedRevision: 8,
		},
		Policy: &core.PolicyObservedState{
			Presence: core.ObservedPresenceUnknown, ObservedAt: newerTime,
			LastErrorCode: "policy_probe_failed",
		},
		Targets: []core.TargetObservedState{{
			PhysicalTargetObserved: core.PhysicalTargetObserved{
				CanonicalTarget: target, ObservedAt: newerTime,
				BanMembership:  core.ObservedMembershipUnknown,
				PolicyCoverage: core.ObservedPolicyUnknown, TimeoutMode: core.TimeoutNone,
				LastErrorCode: "target_probe_failed",
			},
		}},
	}
	if err := database.ApplyObservedFirewallUpdate(ctx, newer); err != nil {
		t.Fatalf("ApplyObservedFirewallUpdate(newer): %v", err)
	}
	got, err := database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadObservedFirewallSnapshot(): %v", err)
	}
	want := core.ObservedFirewallSnapshot{
		NodeID: testNodeID, Infrastructure: newer.Infrastructure, Policy: newer.Policy,
		Targets: newer.Targets,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("newer snapshot = %#v, want %#v", got, want)
	}
}

func TestSQLiteObservedFirewallStaleTargetFenceRollsBackStandaloneUpdate(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	target := netip.MustParsePrefix("192.0.2.9/32")
	seedObservedDesiredTargets(t, database, map[netip.Prefix]core.TargetEnforcementGeneration{target: 2})
	initialTime := time.Unix(4_000, 0).UTC()
	initial := core.ObservedFirewallUpdate{NodeID: testNodeID, Infrastructure: &core.InfrastructureObservedState{
		Presence: core.ObservedPresencePresent, ObservedAt: initialTime,
		Backend: "fake", OwnerVersion: "v1", Digest: "initial",
	}}
	if err := database.ApplyObservedFirewallUpdate(ctx, initial); err != nil {
		t.Fatalf("ApplyObservedFirewallUpdate(initial): %v", err)
	}

	err := database.ApplyObservedFirewallUpdate(ctx, core.ObservedFirewallUpdate{
		NodeID: testNodeID,
		Infrastructure: &core.InfrastructureObservedState{
			Presence: core.ObservedPresencePresent, ObservedAt: initialTime.Add(time.Second),
			Backend: "fake", OwnerVersion: "v1", Digest: "must-rollback",
		},
		Targets: []core.TargetObservedState{{
			PhysicalTargetObserved: core.PhysicalTargetObserved{
				CanonicalTarget: target, ObservedAt: initialTime.Add(time.Second),
				BanMembership:  core.ObservedMembershipAbsent,
				PolicyCoverage: core.ObservedPolicyUnknown, TimeoutMode: core.TimeoutNone,
			},
			ConfirmedGeneration: 1,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "differs from current Desired generation 2") {
		t.Fatalf("ApplyObservedFirewallUpdate(stale) error = %v", err)
	}
	got, err := database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadObservedFirewallSnapshot(): %v", err)
	}
	want := core.ObservedFirewallSnapshot{NodeID: testNodeID, Infrastructure: initial.Infrastructure, Targets: []core.TargetObservedState{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot after rollback = %#v, want %#v", got, want)
	}
}

func TestSQLiteReconcileTransitionObservedStateIsAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	target := netip.MustParsePrefix("2001:db8::8/128")
	seedObservedDesiredTargets(t, database, map[netip.Prefix]core.TargetEnforcementGeneration{target: 5})
	now := time.Unix(5_000, 0).UTC()
	state := core.PersistedReconcileState{
		NodeID: testNodeID, Domain: core.ReconcileDomainTarget,
		Target: target, TargetGeneration: 5, RetryEpoch: 1,
		RetryState: core.RetryState{Status: core.ReconcilePending}, UpdatedAt: now,
	}
	staleObserved := core.ObservedFirewallUpdate{NodeID: testNodeID, Targets: []core.TargetObservedState{{
		PhysicalTargetObserved: core.PhysicalTargetObserved{
			CanonicalTarget: target, ObservedAt: now,
			BanMembership:  core.ObservedMembershipAbsent,
			PolicyCoverage: core.ObservedPolicyUnknown, TimeoutMode: core.TimeoutNone,
		},
		ConfirmedGeneration: 4,
	}}}
	err := database.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: state, Observed: &staleObserved})
	if err == nil || !strings.Contains(err.Error(), "differs from current Desired generation 5") {
		t.Fatalf("ApplyReconcileTransition(stale) error = %v", err)
	}
	recovery, err := database.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadReconcileRecovery(): %v", err)
	}
	if len(recovery.States) != 0 || len(recovery.ProbeRequirements) != 0 {
		t.Fatalf("recovery changed after rollback: %+v", recovery)
	}
	snapshot, err := database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadObservedFirewallSnapshot(): %v", err)
	}
	if snapshot.Infrastructure != nil || snapshot.Policy != nil || len(snapshot.Targets) != 0 {
		t.Fatalf("Observed changed after rollback: %+v", snapshot)
	}

	confirmed := staleObserved
	confirmed.Targets[0].ConfirmedGeneration = 5
	if err := database.ApplyReconcileTransition(ctx, core.ReconcileStateTransition{State: state, Observed: &confirmed}); err != nil {
		t.Fatalf("ApplyReconcileTransition(confirmed): %v", err)
	}
	recovery, err = database.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadReconcileRecovery(after success): %v", err)
	}
	if len(recovery.States) != 1 || recovery.States[0] != state {
		t.Fatalf("recovery after success = %+v, want state %+v", recovery, state)
	}
	snapshot, err = database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadObservedFirewallSnapshot(after success): %v", err)
	}
	if len(snapshot.Targets) != 1 || !reflect.DeepEqual(snapshot.Targets[0], confirmed.Targets[0]) {
		t.Fatalf("Observed after success = %+v, want %+v", snapshot, confirmed.Targets[0])
	}
}

func seedObservedDesiredTargets(
	t *testing.T,
	database *Store,
	targets map[netip.Prefix]core.TargetEnforcementGeneration,
) {
	t.Helper()
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	for target, generation := range targets {
		family := core.AddressFamilyIPv6
		if target.Addr().Is4() {
			family = core.AddressFamilyIPv4
		}
		intent := core.NormalizedTargetEnforcementIntent{
			NodeID: testNodeID, CanonicalTarget: target,
			BanMembership: core.BanAbsent, TimeoutMode: core.TimeoutNone,
			Scopes: core.ScopeInput, AddressFamily: family,
			PolicyCoverage:          core.PolicyCoverageNone,
			BackendAttributesDigest: strings.Repeat("a", 64), Generation: generation,
		}
		if err := uow.PutTargetEnforcementIntent(ctx, intent); err != nil {
			_ = uow.Rollback()
			t.Fatalf("PutTargetEnforcementIntent(%s): %v", target, err)
		}
	}
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
}
