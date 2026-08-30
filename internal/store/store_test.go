package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestPragmasOnEveryPhysicalConnection(t *testing.T) {
	store := openTestStore(t)
	store.db.SetMaxOpenConns(4)
	store.db.SetMaxIdleConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connections := make([]*sql.Conn, 0, 4)
	for index := 0; index < 4; index++ {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			closeSQLConnections(t, connections)
			t.Fatalf("acquire physical connection %d: %v", index, err)
		}
		connections = append(connections, conn)
	}
	defer closeSQLConnections(t, connections)

	for index, conn := range connections {
		readback, err := readSQLPragmas(ctx, conn)
		if err != nil {
			t.Fatalf("read physical connection %d pragmas: %v", index, err)
		}
		if err := readback.validate(); err != nil {
			t.Fatalf("physical connection %d: %v", index, err)
		}
	}
}

func TestMigrationEmptyAndIdempotent(t *testing.T) {
	tests := []struct {
		name  string
		opens int
	}{
		{name: "empty database", opens: 1},
		{name: "repeated startup", opens: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "guard.db")
			for attempt := 0; attempt < test.opens; attempt++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				store, err := Open(ctx, databasePath, migrationFileSystem())
				cancel()
				if err != nil {
					t.Fatalf("Open() attempt %d: %v", attempt+1, err)
				}
				if err := store.Close(); err != nil {
					t.Fatalf("Close() attempt %d: %v", attempt+1, err)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store, err := Open(ctx, databasePath, migrationFileSystem())
			if err != nil {
				t.Fatalf("Open() for verification: %v", err)
			}
			defer closeStore(t, store)

			var migrationCount int
			var checksum string
			if err := store.db.QueryRowContext(ctx, `
				SELECT count(*), min(checksum_sha256) FROM schema_migrations`).Scan(
				&migrationCount, &checksum); err != nil {
				t.Fatalf("read migration ledger: %v", err)
			}
			if migrationCount != 1 || len(checksum) != 64 {
				t.Fatalf("migration ledger = count %d checksum %q", migrationCount, checksum)
			}

			for _, table := range m0Tables {
				var count int
				if err := store.db.QueryRowContext(ctx, `
					SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`,
					table).Scan(&count); err != nil {
					t.Fatalf("query table %s: %v", table, err)
				}
				if count != 1 {
					t.Errorf("table %s count = %d, want 1", table, count)
				}
			}

			rows, err := store.db.QueryContext(ctx, "PRAGMA foreign_key_check")
			if err != nil {
				t.Fatalf("foreign_key_check: %v", err)
			}
			if rows.Next() {
				if closeErr := rows.Close(); closeErr != nil {
					t.Errorf("close foreign_key_check rows: %v", closeErr)
				}
				t.Fatal("foreign_key_check returned a violation")
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate foreign_key_check: %v", err)
			}
			if err := rows.Close(); err != nil {
				t.Fatalf("close foreign_key_check rows: %v", err)
			}
		})
	}
}

func TestMigrationRejectsNonPrefixHistory(t *testing.T) {
	first := newTestMigration(1, "0001_first", `CREATE TABLE migration_one(id INTEGER PRIMARY KEY) STRICT;`)
	second := newTestMigration(2, "0002_second", `CREATE TABLE migration_two(id INTEGER PRIMARY KEY) STRICT;`)
	tests := []struct {
		name        string
		persisted   []migration
		available   []migration
		wantError   string
		absentTable string
	}{
		{
			name:      "future version rejects older binary",
			persisted: []migration{first, second},
			available: []migration{first},
			wantError: "downgrade is not supported",
		},
		{
			name:        "missing historical version rejects non-prefix ledger",
			persisted:   []migration{second},
			available:   []migration{first, second},
			wantError:   "database migration history is not a prefix",
			absentTable: "migration_one",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			db, err := openDatabase(ctx, filepath.Join(t.TempDir(), "guard.db"))
			if err != nil {
				t.Fatalf("openDatabase(): %v", err)
			}
			defer func() {
				if err := db.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			}()

			if err := applyMigrations(ctx, db, test.persisted); err != nil {
				t.Fatalf("seed migration history: %v", err)
			}
			err = applyMigrations(ctx, db, test.available)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("applyMigrations() error = %v, want substring %q", err, test.wantError)
			}

			if test.absentTable != "" {
				var count int
				if err := db.QueryRowContext(ctx, `
					SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`,
					test.absentTable).Scan(&count); err != nil {
					t.Fatalf("query table %s: %v", test.absentTable, err)
				}
				if count != 0 {
					t.Fatalf("table %s was created before history rejection", test.absentTable)
				}
			}
		})
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "guard.db")
	db, err := openDatabase(ctx, databasePath)
	if err != nil {
		t.Fatalf("openDatabase(): %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()

	sqlText := `
		CREATE TABLE should_rollback(id INTEGER PRIMARY KEY) STRICT;
		INSERT INTO missing_table(id) VALUES (1);`
	digest := sha256.Sum256([]byte(sqlText))
	err = applyMigrations(ctx, db, []migration{{
		version: 1, name: "0001_broken", checksum: hex.EncodeToString(digest[:]), sql: sqlText,
	}})
	if err == nil {
		t.Fatal("applyMigrations() error = nil, want failure")
	}

	for _, table := range []string{"schema_migrations", "should_rollback"} {
		var count int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`,
			table).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("table %s survived failed migration", table)
		}
	}
}

func TestConcurrentActiveDecisionUniqueness(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		ruleID      any
		ruleVersion any
	}{
		{name: "automatic", source: "automatic", ruleID: "rule-1", ruleVersion: "v1"},
		{name: "manual", source: "manual"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			store.db.SetMaxOpenConns(8)
			store.db.SetMaxIdleConns(8)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			seedNodeAndRule(t, ctx, store)

			const contenders = 8
			start := make(chan struct{})
			results := make(chan error, contenders)
			var workers sync.WaitGroup
			for index := 0; index < contenders; index++ {
				workers.Add(1)
				go func(index int) {
					defer workers.Done()
					<-start
					now := time.Unix(100, 0).UTC().UnixMicro()
					_, err := store.db.ExecContext(ctx, `
						INSERT INTO decisions(
							decision_id, node_id, source, rule_id, rule_version, canonical_target,
							created_at_us, updated_at_us, last_triggered_at_us, state, suppressed_count
						) VALUES (?, ?, ?, ?, ?, '192.0.2.1/32', ?, ?, ?, 'active', 0)`,
						fmt.Sprintf("decision-%d", index), testNodeID, test.source, test.ruleID,
						test.ruleVersion, now, now, now)
					results <- err
				}(index)
			}
			close(start)
			workers.Wait()
			close(results)

			successes := 0
			for err := range results {
				if err == nil {
					successes++
					continue
				}
				if !strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
					t.Errorf("concurrent insert error = %v, want unique constraint", err)
				}
			}
			if successes != 1 {
				t.Fatalf("successful inserts = %d, want 1", successes)
			}

			var activeCount int
			if err := store.db.QueryRowContext(ctx, `
				SELECT count(*) FROM decisions
				WHERE node_id = ? AND source = ? AND state = 'active'
					AND canonical_target = '192.0.2.1/32'`, testNodeID, test.source).Scan(&activeCount); err != nil {
				t.Fatalf("count active decisions: %v", err)
			}
			if activeCount != 1 {
				t.Fatalf("active decision count = %d, want 1", activeCount)
			}
		})
	}
}

func TestDecisionProjectionAuditRollBackOnRequiredWriteFailure(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seedNodeAndRule(t, ctx, store)

	now := time.Unix(100, 0).UTC()
	ruleID := core.RuleID("rule-1")
	ruleVersion := core.RuleVersion("v1")
	decisionID := core.DecisionID("decision-atomic")
	target := netip.MustParsePrefix("192.0.2.1/32")
	uow, err := store.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	if err := uow.PutDecision(ctx, core.Decision{
		ID: decisionID, NodeID: testNodeID, Source: core.DecisionSourceAutomatic,
		RuleID: &ruleID, RuleVersion: &ruleVersion, CanonicalTarget: target,
		CreatedAt: now, UpdatedAt: now, LastTriggeredAt: now, State: core.DecisionActive,
	}); err != nil {
		t.Fatalf("PutDecision(): %v", err)
	}
	if err := uow.PutProjection(ctx, core.DesiredBanProjection{
		NodeID: testNodeID, CanonicalTarget: target, State: core.BanProjectionPresent,
		ActiveCount: 1, Revision: 1,
	}, now); err != nil {
		t.Fatalf("PutProjection(): %v", err)
	}
	if err := uow.AppendCriticalAudit(ctx, CriticalAudit{
		ID: "audit-atomic", IdempotencyKey: "decision-atomic:activated", NodeID: testNodeID,
		Category: "decision", Action: "decision_activated", Result: "success",
		Severity: "info", ActorType: "system", DecisionID: &decisionID,
		DetailsJSON: []byte(`{"target":"192.0.2.1/32"}`), CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendCriticalAudit(): %v", err)
	}
	position, err := core.NewJournaldPosition("s=cursor")
	if err != nil {
		t.Fatalf("NewJournaldPosition(): %v", err)
	}
	deliveryID, err := core.JournaldDeliveryID("missing-source", "s=cursor")
	if err != nil {
		t.Fatalf("JournaldDeliveryID(): %v", err)
	}
	writeErr := uow.PutReceipt(ctx, core.ProcessingReceipt{
		DeliveryID: deliveryID, SourceID: "missing-source",
		Position: position, Kind: core.ReceiptSuccess, Committed: now,
	})
	if writeErr == nil {
		t.Fatal("PutReceipt() error = nil, want foreign-key failure")
	}
	if err := uow.Commit(); err == nil {
		t.Fatal("Commit() error = nil after required write failure")
	}

	checks := []struct {
		name  string
		query string
	}{
		{name: "decision", query: "SELECT count(*) FROM decisions WHERE decision_id = 'decision-atomic'"},
		{name: "projection", query: "SELECT count(*) FROM desired_ban_projections WHERE canonical_target = '192.0.2.1/32'"},
		{name: "audit", query: "SELECT count(*) FROM audit_logs WHERE audit_id = 'audit-atomic'"},
		{name: "receipt", query: "SELECT count(*) FROM processing_receipts"},
	}
	for _, check := range checks {
		var count int
		if err := store.db.QueryRowContext(ctx, check.query).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", check.name, err)
		}
		if count != 0 {
			t.Errorf("%s row count = %d after rollback, want 0", check.name, count)
		}
	}
}

func TestPutProjectionRevisionFencing(t *testing.T) {
	tests := []struct {
		name              string
		candidateRevision core.TargetProjectionRevision
		candidateCount    uint64
		wantWriteError    bool
		wantRevision      int64
		wantCount         int64
		wantUpdatedAt     time.Time
	}{
		{
			name:              "equal revision with identical content is idempotent",
			candidateRevision: 2, candidateCount: 1,
			wantRevision: 2, wantCount: 1,
		},
		{
			name:              "equal revision with different content is rejected",
			candidateRevision: 2, candidateCount: 2, wantWriteError: true,
			wantRevision: 2, wantCount: 1,
		},
		{
			name:              "lower revision is rejected",
			candidateRevision: 1, candidateCount: 2, wantWriteError: true,
			wantRevision: 2, wantCount: 1,
		},
		{
			name:              "higher revision replaces content",
			candidateRevision: 3, candidateCount: 2,
			wantRevision: 3, wantCount: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			baseUpdatedAt := time.Unix(100, 0).UTC()
			candidateUpdatedAt := time.Unix(200, 0).UTC()
			if test.wantUpdatedAt.IsZero() {
				if test.candidateRevision > 2 {
					test.wantUpdatedAt = candidateUpdatedAt
				} else {
					test.wantUpdatedAt = baseUpdatedAt
				}
			}
			if err := store.EnsureNodeIdentity(ctx, testNodeID, baseUpdatedAt); err != nil {
				t.Fatalf("EnsureNodeIdentity(): %v", err)
			}

			target := netip.MustParsePrefix("192.0.2.1/32")
			baseUOW, err := store.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing() base: %v", err)
			}
			if err := baseUOW.PutProjection(ctx, core.DesiredBanProjection{
				NodeID: testNodeID, CanonicalTarget: target, State: core.BanProjectionPresent,
				ActiveCount: 1, Revision: 2,
			}, baseUpdatedAt); err != nil {
				t.Fatalf("PutProjection() base: %v", err)
			}
			if err := baseUOW.Commit(); err != nil {
				t.Fatalf("Commit() base: %v", err)
			}

			candidateUOW, err := store.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing() candidate: %v", err)
			}
			writeErr := candidateUOW.PutProjection(ctx, core.DesiredBanProjection{
				NodeID: testNodeID, CanonicalTarget: target, State: core.BanProjectionPresent,
				ActiveCount: test.candidateCount, Revision: test.candidateRevision,
			}, candidateUpdatedAt)
			if test.wantWriteError {
				if writeErr == nil {
					t.Fatal("PutProjection() error = nil, want revision conflict")
				}
				if err := candidateUOW.Commit(); err == nil {
					t.Fatal("Commit() error = nil after revision conflict")
				}
			} else {
				if writeErr != nil {
					t.Fatalf("PutProjection(): %v", writeErr)
				}
				if err := candidateUOW.Commit(); err != nil {
					t.Fatalf("Commit(): %v", err)
				}
			}

			var state string
			var activeCount int64
			var effectiveUntil sql.NullInt64
			var revision int64
			var updatedAt int64
			if err := store.db.QueryRowContext(ctx, `
				SELECT state, active_count, effective_until_us,
					target_projection_revision, updated_at_us
				FROM desired_ban_projections
				WHERE node_id = ? AND canonical_target = ?`,
				testNodeID, target.String()).Scan(
				&state, &activeCount, &effectiveUntil, &revision, &updatedAt); err != nil {
				t.Fatalf("read projection: %v", err)
			}
			if state != "present" || effectiveUntil.Valid ||
				activeCount != test.wantCount || revision != test.wantRevision ||
				updatedAt != test.wantUpdatedAt.UnixMicro() {
				t.Fatalf(
					"projection = state %q count %d until %+v revision %d updated %d",
					state, activeCount, effectiveUntil, revision, updatedAt)
			}
		})
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "guard.db"), migrationFileSystem())
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		closeStore(t, store)
	})
	return store
}

func migrationFileSystem() fs.FS {
	return os.DirFS(filepath.Join("..", "..", "migrations"))
}

func newTestMigration(version int64, name, sqlText string) migration {
	digest := sha256.Sum256([]byte(sqlText))
	return migration{
		version: version, name: name, checksum: hex.EncodeToString(digest[:]), sql: sqlText,
	}
}

func closeStore(t *testing.T, store *Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Errorf("Close(): %v", err)
	}
}

func closeSQLConnections(t *testing.T, connections []*sql.Conn) {
	t.Helper()
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Errorf("close SQL connection: %v", err)
		}
	}
}

func seedNodeAndRule(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	now := time.Unix(100, 0).UTC()
	if err := store.EnsureNodeIdentity(ctx, testNodeID, now); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO rules(rule_id, enabled, created_at_us, updated_at_us)
		VALUES ('rule-1', 1, ?, ?)`, now.UnixMicro(), now.UnixMicro()); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO rule_versions(rule_id, version, definition, definition_sha256, created_at_us)
		VALUES ('rule-1', 'v1', '{}', ?, ?)`, strings.Repeat("0", 64), now.UnixMicro()); err != nil {
		t.Fatalf("insert rule version: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE rules SET active_version = 'v1' WHERE rule_id = 'rule-1'`); err != nil {
		t.Fatalf("activate rule version: %v", err)
	}
}

var m0Tables = []string{
	"schema_migrations",
	"node_identity",
	"sources",
	"parsers",
	"parser_versions",
	"rules",
	"rule_versions",
	"allowlists",
	"protected_targets",
	"source_file_generations",
	"source_checkpoints",
	"processing_receipts",
	"parser_terminal_outcomes",
	"detection_contributions",
	"alerts",
	"decisions",
	"desired_ban_projections",
	"enforcement_states",
	"infrastructure_reconcile_state",
	"policy_reconcile_state",
	"target_reconcile_state",
	"audit_logs",
}

const testNodeID core.NodeID = "0123456789abcdef0123456789abcdef"
