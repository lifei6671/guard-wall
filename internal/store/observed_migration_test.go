package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestSQLiteObservedFirewallMigrationDowngradesIncompleteV4Cache(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "guard.db")
	v4Migrations := fstest.MapFS{}
	for _, name := range []string{
		"0001_m0.sql",
		"0002_detection_terminal_outcomes.sql",
		"0003_reconcile_restart_recovery.sql",
		"0004_desired_firewall_authority.sql",
	} {
		content, err := fs.ReadFile(migrationFileSystem(), name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		v4Migrations[name] = &fstest.MapFile{Data: content}
	}

	database, err := Open(ctx, databasePath, v4Migrations)
	if err != nil {
		t.Fatalf("Open(v4): %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close(cleanup): %v", err)
		}
	})
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO enforcement_states(
			node_id, canonical_target, desired_membership, observed_membership,
			effective_until_us, timeout_mode, scopes, address_family,
			policy_coverage, policy_relation_digest, backend_attributes_digest,
			target_enforcement_generation, confirmed_target_enforcement_generation,
			confirmed_snapshot_revision, observed_at_us)
		VALUES (?, '192.0.2.44/32', 'present', 'present', NULL, 'none', 1, 4,
			'none', '', ?, 5, 5, 3, ?)`,
		string(testNodeID), strings.Repeat("a", 64), time.Unix(200, 0).UTC().UnixMicro()); err != nil {
		t.Fatalf("insert v4 enforcement cache: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close(v4): %v", err)
	}

	database, err = Open(ctx, databasePath, migrationFileSystem())
	if err != nil {
		t.Fatalf("Open(v5): %v", err)
	}
	snapshot, err := database.LoadObservedFirewallSnapshot(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadObservedFirewallSnapshot(): %v", err)
	}
	if snapshot.Infrastructure != nil || snapshot.Policy != nil || snapshot.Targets == nil || len(snapshot.Targets) != 0 {
		t.Fatalf("migrated Observed snapshot = %+v", snapshot)
	}

	var membership string
	var desiredGeneration int64
	var confirmedGeneration, confirmedSnapshot, observedAt sql.NullInt64
	if err := database.db.QueryRowContext(ctx, `
		SELECT observed_membership, target_enforcement_generation,
			confirmed_target_enforcement_generation, confirmed_snapshot_revision,
			observed_at_us
		FROM enforcement_states
		WHERE node_id = ? AND canonical_target = '192.0.2.44/32'`, string(testNodeID)).Scan(
		&membership, &desiredGeneration, &confirmedGeneration, &confirmedSnapshot, &observedAt); err != nil {
		t.Fatalf("read migrated enforcement row: %v", err)
	}
	if membership != "unknown" || desiredGeneration != 5 || confirmedGeneration.Valid ||
		confirmedSnapshot.Valid || observedAt.Valid {
		t.Fatalf("migrated row = membership %q desired %d confirmed %+v/%+v observed %+v",
			membership, desiredGeneration, confirmedGeneration, confirmedSnapshot, observedAt)
	}

	var migrationCount int
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("read migration ledger: %v", err)
	}
	if migrationCount != 5 {
		t.Fatalf("migration count = %d, want 5", migrationCount)
	}
}
