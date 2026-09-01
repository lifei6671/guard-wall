package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
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
			if migrationCount != 5 || len(checksum) != 64 {
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

func TestMigrationV4UpgradesLegacyDesiredStateWithoutRevisionOrRetryRegression(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	migrations, err := loadMigrations(migrationFileSystem())
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 5 {
		t.Fatalf("migration count = %d, want 5", len(migrations))
	}
	db, err := openDatabase(ctx, filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(ctx, db, migrations[:3]); err != nil {
		t.Fatalf("apply legacy migrations: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC().UnixMicro()
	target := "192.0.2.90/32"
	orphanTarget := "192.0.2.91/32"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO node_identity(singleton, node_id, created_at_us) VALUES (1, ?, ?)`,
		testNodeID, now); err != nil {
		t.Fatalf("seed legacy node: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO enforcement_states(
			node_id, canonical_target, desired_membership, observed_membership,
			timeout_mode, scopes, address_family, policy_coverage,
			policy_relation_digest, backend_attributes_digest,
			target_enforcement_generation, confirmed_snapshot_revision
		) VALUES (?, ?, 'absent', 'unknown', 'none', 1, 4, 'none', ?, ?, 0, 7)`,
		testNodeID, target, strings.Repeat("1", 64), strings.Repeat("2", 64)); err != nil {
		t.Fatalf("seed legacy intent: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO target_reconcile_state(
			node_id, canonical_target, target_enforcement_generation, retry_epoch,
			status, attempt_count, last_error_code, updated_at_us
		) VALUES (?, ?, 9, 3, 'degraded', 6, 'legacy', ?)`,
		testNodeID, target, now); err != nil {
		t.Fatalf("seed legacy target retry: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO target_reconcile_state(
			node_id, canonical_target, target_enforcement_generation, retry_epoch,
			status, attempt_count, last_error_code, updated_at_us
		) VALUES (?, ?, 9, 4, 'degraded', 6, 'orphan', ?)`,
		testNodeID, orphanTarget, now); err != nil {
		t.Fatalf("seed legacy orphan target retry: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO reconcile_probe_requirements(
			node_id, domain, canonical_target, infrastructure_revision,
			policy_revision, target_enforcement_generation, snapshot_revision,
			fence_snapshot_revision, retry_epoch, attempt_count, recorded_at_us
		) VALUES (?, 'infrastructure', '', 1, 0, 0, 9, 1, 0, 1, ?)`,
		testNodeID, now); err != nil {
		t.Fatalf("seed legacy probe: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO reconcile_probe_requirements(
			node_id, domain, canonical_target, infrastructure_revision,
			policy_revision, target_enforcement_generation, snapshot_revision,
			fence_snapshot_revision, retry_epoch, attempt_count, recorded_at_us
		) VALUES (?, 'target', ?, 0, 0, 9, 0, 0, 3, 6, ?)`,
		testNodeID, target, now); err != nil {
		t.Fatalf("seed legacy matched target probe: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO reconcile_probe_requirements(
			node_id, domain, canonical_target, infrastructure_revision,
			policy_revision, target_enforcement_generation, snapshot_revision,
			fence_snapshot_revision, retry_epoch, attempt_count, recorded_at_us
		) VALUES (?, 'target', ?, 0, 0, 9, 0, 0, 4, 6, ?)`,
		testNodeID, orphanTarget, now); err != nil {
		t.Fatalf("seed legacy orphan probe: %v", err)
	}
	if err := applyMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("upgrade through migration v5: %v", err)
	}
	var snapshot, generation, retryGeneration, retryEpoch, attempts int64
	var relationDigest, status string
	if err := db.QueryRowContext(ctx, `
		SELECT d.snapshot_revision, e.target_enforcement_generation,
			e.policy_relation_digest, r.target_enforcement_generation,
			r.retry_epoch, r.attempt_count, r.status
		FROM desired_firewall_state d
		JOIN enforcement_states e ON e.node_id = ? AND e.canonical_target = ?
		JOIN target_reconcile_state r
			ON r.node_id = e.node_id AND r.canonical_target = e.canonical_target
		WHERE d.singleton = 1`, testNodeID, target).Scan(
		&snapshot, &generation, &relationDigest, &retryGeneration, &retryEpoch, &attempts, &status,
	); err != nil {
		t.Fatal(err)
	}
	if snapshot != 9 || generation != 9 || relationDigest != "" || retryGeneration != 9 ||
		retryEpoch != 3 || attempts != 6 || status != "degraded" {
		t.Fatalf("upgraded desired state = snapshot:%d generation:%d digest:%q retry-generation:%d retry:%d attempts:%d status:%s",
			snapshot, generation, relationDigest, retryGeneration, retryEpoch, attempts, status)
	}
	var orphanCount int
	var orphanGeneration, orphanRetryEpoch, orphanAttempts int64
	var orphanStatus string
	if err := db.QueryRowContext(ctx, `
		SELECT count(*), max(target_enforcement_generation), max(retry_epoch),
			max(attempt_count), max(status)
		FROM target_reconcile_state
		WHERE node_id = ? AND canonical_target = ?`, testNodeID, orphanTarget).Scan(
		&orphanCount, &orphanGeneration, &orphanRetryEpoch, &orphanAttempts, &orphanStatus,
	); err != nil {
		t.Fatal(err)
	}
	if orphanCount != 1 || orphanGeneration != 9 || orphanRetryEpoch != 4 ||
		orphanAttempts != 6 || orphanStatus != "degraded" {
		t.Fatalf("legacy unmaterialized retry = count:%d generation:%d retry:%d attempts:%d status:%s",
			orphanCount, orphanGeneration, orphanRetryEpoch, orphanAttempts, orphanStatus)
	}
	orm, err := newGORMAdapter(ctx, db)
	if err != nil {
		t.Fatalf("initialize GORM adapter for migrated store: %v", err)
	}
	database := &Store{db: db, orm: orm}
	service := newDecisionLifecycleService(t, database)
	if _, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-after-v4-matched", NodeID: testNodeID,
		Target: netip.MustParsePrefix(target), CreatedAt: time.Unix(1_700_000_050, 0).UTC(),
	}, false); err != nil {
		t.Fatalf("first Decision after matched migration: %v", err)
	}
	assertDesiredTargetState(
		t, database, netip.MustParsePrefix(target), "present", 10, 10, "pending", 3, 0,
	)
	if _, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-after-v4-orphan", NodeID: testNodeID,
		Target: netip.MustParsePrefix(orphanTarget), CreatedAt: time.Unix(1_700_000_100, 0).UTC(),
	}, false); err != nil {
		t.Fatalf("first Decision after orphan migration: %v", err)
	}
	assertDesiredTargetState(
		t, database, netip.MustParsePrefix(orphanTarget), "present", 10, 11, "pending", 4, 0,
	)
	var retainedProbeGeneration, retainedProbeAttempts int64
	if err := db.QueryRowContext(ctx, `
		SELECT target_enforcement_generation, attempt_count
		FROM reconcile_probe_requirements
		WHERE node_id = ? AND domain = 'target' AND canonical_target = ?`,
		testNodeID, orphanTarget).Scan(&retainedProbeGeneration, &retainedProbeAttempts); err != nil {
		t.Fatal(err)
	}
	if retainedProbeGeneration != 9 || retainedProbeAttempts != 6 {
		t.Fatalf("legacy probe = generation:%d attempts:%d", retainedProbeGeneration, retainedProbeAttempts)
	}
}

func TestPutTargetEnforcementIntentRejectsGenerationBeyondSQLiteInteger(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := netip.MustParsePrefix("192.0.2.92/32")
	err = uow.PutTargetEnforcementIntent(ctx, core.NormalizedTargetEnforcementIntent{
		NodeID: testNodeID, CanonicalTarget: target, BanMembership: core.BanPresent,
		TimeoutMode: core.TimeoutNone, Scopes: core.ScopeInput, AddressFamily: core.AddressFamilyIPv4,
		PolicyCoverage: core.PolicyCoverageNone, BackendAttributesDigest: strings.Repeat("f", 64),
		Generation: core.TargetEnforcementGeneration(uint64(math.MaxInt64) + 1),
	})
	if err == nil || !strings.Contains(err.Error(), "generation is exhausted") {
		t.Fatalf("PutTargetEnforcementIntent() error = %v", err)
	}
	if rollbackErr := uow.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
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
	"detection_terminal_outcomes",
	"alerts",
	"decisions",
	"desired_ban_projections",
	"desired_firewall_state",
	"enforcement_states",
	"infrastructure_observed_state",
	"policy_observed_state",
	"infrastructure_reconcile_state",
	"policy_reconcile_state",
	"target_reconcile_state",
	"reconcile_probe_requirements",
	"audit_logs",
}

const testNodeID core.NodeID = "0123456789abcdef0123456789abcdef"
