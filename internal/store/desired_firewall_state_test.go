package store

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestSQLiteLoadDesiredFirewallStateReturnsCompleteSingleSnapshot(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}

	target := core.NormalizedTargetEnforcementIntent{
		NodeID: testNodeID, CanonicalTarget: netip.MustParsePrefix("192.0.2.1/32"),
		BanMembership: core.BanPresent, TimeoutMode: core.TimeoutNative,
		EffectiveUntil: desiredFirewallTimePointer(time.Unix(5_000, 0).UTC()), Scopes: core.ScopeInput,
		AddressFamily: core.AddressFamilyIPv4, PolicyCoverage: core.PolicyCoverageFull,
		PolicyRelationDigest: strings.Repeat("a", 64), BackendAttributesDigest: strings.Repeat("b", 64),
		Generation: 3,
	}
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	if err := uow.PutTargetEnforcementIntent(ctx, target); err != nil {
		t.Fatalf("PutTargetEnforcementIntent(): %v", err)
	}
	if revision, err := uow.AdvanceSnapshotRevision(ctx); err != nil || revision != 1 {
		t.Fatalf("AdvanceSnapshotRevision() = %d, %v; want 1, nil", revision, err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}

	seedDesiredFirewallPolicyRows(t, database, []desiredFirewallPolicyRow{
		{table: "allowlists", target: "2001:db8:1::/48", enabled: 1, revision: 7},
		{table: "allowlists", target: "198.51.100.0/24", enabled: 1, revision: 7},
		{table: "allowlists", target: "203.0.113.0/24", enabled: 0, revision: 7},
		{table: "protected_targets", target: "::1/128", enabled: 1, revision: 7},
		{table: "protected_targets", target: "127.0.0.0/8", enabled: 1, revision: 7},
		{table: "protected_targets", target: "192.0.2.0/24", enabled: 0, revision: 7},
	})

	state, err := database.LoadDesiredFirewallState(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadDesiredFirewallState(): %v", err)
	}
	if state.SnapshotRevision != 7 || state.PolicyRevision != 7 {
		t.Fatalf("revisions = snapshot %d policy %d, want 7 and 7", state.SnapshotRevision, state.PolicyRevision)
	}
	if err := state.Policy.ValidateComplete(); err != nil {
		t.Fatalf("Policy.ValidateComplete(): %v", err)
	}
	wantAllowlist := []netip.Prefix{
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("2001:db8:1::/48"),
	}
	wantProtected := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
	if !samePrefixes(state.Policy.Allowlist, wantAllowlist) ||
		!samePrefixes(state.Policy.ProtectedTargets, wantProtected) {
		t.Fatalf("policy = %+v, want allowlist %v protected %v", state.Policy, wantAllowlist, wantProtected)
	}
	if len(state.Targets) != 1 || !sameDesiredTargetIntent(state.Targets[0], target) {
		t.Fatalf("targets = %+v, want %+v", state.Targets, target)
	}
}

func TestSQLiteLoadDesiredFirewallStateAllowsEmptyAllowlistAndTargets(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	seedDesiredFirewallPolicyRows(t, database, []desiredFirewallPolicyRow{
		{table: "protected_targets", target: "127.0.0.0/8", enabled: 1, revision: 7},
		{table: "protected_targets", target: "::1/128", enabled: 1, revision: 7},
	})

	state, err := database.LoadDesiredFirewallState(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadDesiredFirewallState(): %v", err)
	}
	if state.SnapshotRevision != 2 || state.PolicyRevision != 7 || len(state.Policy.Allowlist) != 0 ||
		len(state.Targets) != 0 {
		t.Fatalf("state = %+v, want revision 2 policy 7 with empty allowlist and targets", state)
	}
	if err := state.Policy.ValidateComplete(); err != nil {
		t.Fatalf("Policy.ValidateComplete(): %v", err)
	}
}

func TestSQLitePolicyMutationAdvancesDesiredSnapshotRevision(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	seedDesiredFirewallPolicyRows(t, database, []desiredFirewallPolicyRow{
		{table: "protected_targets", target: "127.0.0.0/8", enabled: 1, revision: 7},
		{table: "protected_targets", target: "::1/128", enabled: 1, revision: 7},
	})
	before, err := database.SnapshotRevision(ctx)
	if err != nil {
		t.Fatalf("SnapshotRevision(before): %v", err)
	}
	if _, err := database.db.ExecContext(ctx, `
		UPDATE protected_targets
		SET enabled = 0, policy_revision = 8, updated_at_us = 101
		WHERE node_id = ? AND canonical_target = '::1/128'`, string(testNodeID)); err != nil {
		t.Fatalf("update policy row: %v", err)
	}
	after, err := database.SnapshotRevision(ctx)
	if err != nil {
		t.Fatalf("SnapshotRevision(after): %v", err)
	}
	if after != before+1 {
		t.Fatalf("snapshot revision = %d, want %d after policy update", after, before+1)
	}
	if _, err := database.db.ExecContext(ctx, `
		UPDATE protected_targets
		SET enabled = enabled, policy_revision = policy_revision, updated_at_us = 102
		WHERE node_id = ? AND canonical_target = '::1/128'`, string(testNodeID)); err != nil {
		t.Fatalf("no-op policy update: %v", err)
	}
	stable, err := database.SnapshotRevision(ctx)
	if err != nil {
		t.Fatalf("SnapshotRevision(no-op): %v", err)
	}
	if stable != after {
		t.Fatalf("snapshot revision = %d after no-op, want %d", stable, after)
	}
}

func TestSQLiteLoadDesiredFirewallStateRejectsIncompleteOrInconsistentPolicy(t *testing.T) {
	tests := []struct {
		name      string
		rows      []desiredFirewallPolicyRow
		wantError string
	}{
		{
			name: "missing policy rows", wantError: "policy rows are missing",
		},
		{
			name: "inconsistent revision including disabled row",
			rows: []desiredFirewallPolicyRow{
				{table: "allowlists", target: "198.51.100.0/24", enabled: 1, revision: 7},
				{table: "allowlists", target: "203.0.113.0/24", enabled: 0, revision: 8},
				{table: "protected_targets", target: "127.0.0.0/8", enabled: 1, revision: 7},
				{table: "protected_targets", target: "::1/128", enabled: 1, revision: 7},
			},
			wantError: "inconsistent revisions",
		},
		{
			name: "missing mandatory protected loopback",
			rows: []desiredFirewallPolicyRow{
				{table: "allowlists", target: "198.51.100.0/24", enabled: 1, revision: 7},
				{table: "protected_targets", target: "127.0.0.0/8", enabled: 1, revision: 7},
				{table: "protected_targets", target: "::1/128", enabled: 0, revision: 7},
			},
			wantError: "missing mandatory protected targets",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			ctx := context.Background()
			if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
				t.Fatalf("EnsureNodeIdentity(): %v", err)
			}
			seedDesiredFirewallPolicyRows(t, database, test.rows)

			_, err := database.LoadDesiredFirewallState(ctx, testNodeID)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("LoadDesiredFirewallState() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestSQLiteLoadDesiredFirewallStateRejectsNonCanonicalPolicyRows(t *testing.T) {
	tests := []struct {
		name      string
		rows      []desiredFirewallPolicyRow
		wantError string
	}{
		{
			name: "non-canonical prefix",
			rows: []desiredFirewallPolicyRow{
				{table: "allowlists", target: "198.51.100.1/24", enabled: 1, revision: 7},
				{table: "protected_targets", target: "127.0.0.0/8", enabled: 1, revision: 7},
				{table: "protected_targets", target: "::1/128", enabled: 1, revision: 7},
			},
			wantError: "non-canonical target",
		},
		{
			name: "non-boolean enabled flag",
			rows: []desiredFirewallPolicyRow{
				{table: "allowlists", target: "198.51.100.0/24", enabled: 2, revision: 7},
				{table: "protected_targets", target: "127.0.0.0/8", enabled: 1, revision: 7},
				{table: "protected_targets", target: "::1/128", enabled: 1, revision: 7},
			},
			wantError: "invalid enabled flag",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			ctx := context.Background()
			if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
				t.Fatalf("EnsureNodeIdentity(): %v", err)
			}
			seedDesiredFirewallPolicyRowsUnchecked(t, database, test.rows)

			_, err := database.LoadDesiredFirewallState(ctx, testNodeID)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("LoadDesiredFirewallState() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

type desiredFirewallPolicyRow struct {
	table    string
	target   string
	enabled  int64
	revision int64
}

func seedDesiredFirewallPolicyRows(t *testing.T, database *Store, rows []desiredFirewallPolicyRow) {
	t.Helper()
	ctx := context.Background()
	for _, row := range rows {
		if row.table != "allowlists" && row.table != "protected_targets" {
			t.Fatalf("unsupported policy table %q", row.table)
		}
		if _, err := database.db.ExecContext(ctx, `
			INSERT INTO `+row.table+`(
				node_id, canonical_target, enabled, policy_revision, created_at_us, updated_at_us
			) VALUES (?, ?, ?, ?, ?, ?)`,
			string(testNodeID), row.target, row.enabled, row.revision, 100, 100); err != nil {
			t.Fatalf("seed %s %s: %v", row.table, row.target, err)
		}
	}
}

func seedDesiredFirewallPolicyRowsUnchecked(t *testing.T, database *Store, rows []desiredFirewallPolicyRow) {
	t.Helper()
	ctx := context.Background()
	connection, err := database.db.Conn(ctx)
	if err != nil {
		t.Fatalf("open policy seed connection: %v", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "PRAGMA ignore_check_constraints = ON"); err != nil {
		t.Fatalf("enable unchecked policy seed: %v", err)
	}
	defer func() {
		if _, err := connection.ExecContext(ctx, "PRAGMA ignore_check_constraints = OFF"); err != nil {
			t.Errorf("restore policy constraint checks: %v", err)
		}
	}()
	for _, row := range rows {
		if row.table != "allowlists" && row.table != "protected_targets" {
			t.Fatalf("unsupported policy table %q", row.table)
		}
		if _, err := connection.ExecContext(ctx, `
			INSERT INTO `+row.table+`(
				node_id, canonical_target, enabled, policy_revision, created_at_us, updated_at_us
			) VALUES (?, ?, ?, ?, ?, ?)`,
			string(testNodeID), row.target, row.enabled, row.revision, 100, 100); err != nil {
			t.Fatalf("unchecked seed %s %s: %v", row.table, row.target, err)
		}
	}
}

func samePrefixes(left, right []netip.Prefix) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func desiredFirewallTimePointer(value time.Time) *time.Time {
	return &value
}
