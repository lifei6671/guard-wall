package processor

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
	"modernc.org/sqlite"
)

func TestSQLiteCoordinatorReceiptReplaySkipsSecondAttempt(t *testing.T) {
	database, _ := openSQLiteProcessingStore(t)
	runner := &zeroOutcomeRunner{}
	coordinator := NewCoordinator(NewSQLiteStoreAdapter(database), runner)
	delivery := testDelivery(t, 1)

	if _, err := coordinator.Process(context.Background(), delivery); err != nil {
		t.Fatalf("first Process(): %v", err)
	}
	delivery.Sequence = 7
	completion, err := coordinator.Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("replay Process(): %v", err)
	}
	if runner.calls != 1 || completion.Sequence != 7 {
		t.Fatalf("runner calls/completion = %d/%+v", runner.calls, completion)
	}
	receipt, found, err := database.FindProcessingReceipt(context.Background(), delivery.ID)
	if err != nil || !found || receipt.DeliveryID != delivery.ID {
		t.Fatalf("FindProcessingReceipt() = %+v,%v,%v", receipt, found, err)
	}
}

func TestSQLiteCoordinatorCommitUnknownUsesIndependentReceiptReadback(t *testing.T) {
	database, _ := openSQLiteProcessingStore(t)
	adapter := NewSQLiteStoreAdapter(database)
	commitResultLost := errors.New("injected connection loss after commit")
	adapter.commit = func(unit *store.UnitOfWork) error {
		if err := unit.Commit(); err != nil {
			return err
		}
		return commitResultLost
	}
	delivery := testDelivery(t, 1)

	completion, err := NewCoordinator(adapter, &zeroOutcomeRunner{}).Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("Process(): %v", err)
	}
	if completion.DeliveryID != delivery.ID {
		t.Fatalf("completion = %+v", completion)
	}
}

func TestSQLiteCommitCanceledBeforeCommitRollsBackAndClosesTransaction(t *testing.T) {
	database, _ := openSQLiteProcessingStore(t)
	adapter := NewSQLiteStoreAdapter(database)
	ctx, cancel := context.WithCancel(context.Background())
	unit, err := adapter.beginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	delivery := testDelivery(t, 1)
	receipt := core.ProcessingReceipt{
		DeliveryID: delivery.ID, SourceID: delivery.Record.SourceID,
		Position: delivery.Record.Position, Kind: core.ReceiptSuccess,
		Committed: time.Unix(1_700_000_001, 0).UTC(),
	}
	if err := unit.writeReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	cancel()
	state, err := unit.commit(ctx)
	if state != commitRejected || !errors.Is(err, context.Canceled) {
		t.Fatalf("commit() = %v,%v, want Rejected/context.Canceled", state, err)
	}
	if err := unit.rollback(); err == nil {
		t.Fatal("rejected transaction remained open")
	}
	if _, found, err := database.FindProcessingReceipt(context.Background(), delivery.ID); err != nil || found {
		t.Fatalf("canceled receipt readback found=%v err=%v", found, err)
	}
	fresh, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatalf("begin after rejected transaction: %v", err)
	}
	if err := fresh.Rollback(); err != nil {
		t.Fatalf("rollback fresh transaction: %v", err)
	}
}

func TestSQLiteCoordinatorCommitsTypedOutcomesWithReceipt(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	delivery := testDelivery(t, 1)

	if _, err := NewCoordinator(NewSQLiteStoreAdapter(database), &fullRunner{}).
		Process(context.Background(), delivery); err != nil {
		t.Fatalf("Process(): %v", err)
	}

	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	for _, table := range []string{
		"parser_terminal_outcomes", "detection_contributions", "alerts", "decisions",
		"desired_ban_projections", "audit_logs", "processing_receipts",
	} {
		var count int
		if err := connection.QueryRowContext(
			context.Background(), "SELECT count(*) FROM "+table,
		).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("%s count = %d, want 1", table, count)
		}
	}
}

func openSQLiteProcessingStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guard.db")
	database, err := store.Open(context.Background(), path, processorMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	base := time.Unix(1_700_000_000, 0).UTC()
	nodeID := core.NodeID("00112233445566778899aabbccddeeff")
	if err := database.EnsureNodeIdentity(context.Background(), nodeID, base); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(context.Background(), "source-1", nodeID, store.SourceKindFile, base); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterFileGeneration(context.Background(), store.FileGeneration{
		SourceID: "source-1", Generation: "00112233445566778899aabbccddeeff",
		DeviceID: 1, Inode: 2, Path: "/var/log/guard.log", ObservedSize: 20, OpenedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	return database, path
}

func seedSQLiteProcessingCatalog(t *testing.T, path string) {
	t.Helper()
	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	now := time.Unix(1_700_000_000, 0).UTC().UnixMicro()
	hash := strings.Repeat("1", 64)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO parsers(parser_id, enabled, created_at_us, updated_at_us) VALUES (?, 1, ?, ?)`, []any{"parser-1", now, now}},
		{`INSERT INTO parser_versions(parser_id, version, definition, definition_sha256, created_at_us) VALUES (?, ?, '{}', ?, ?)`, []any{"parser-1", "v1", hash, now}},
		{`UPDATE parsers SET active_version = ? WHERE parser_id = ?`, []any{"v1", "parser-1"}},
		{`INSERT INTO rules(rule_id, enabled, created_at_us, updated_at_us) VALUES (?, 1, ?, ?)`, []any{"rule-1", now, now}},
		{`INSERT INTO rule_versions(rule_id, version, definition, definition_sha256, created_at_us) VALUES (?, ?, '{}', ?, ?)`, []any{"rule-1", "v1", hash, now}},
		{`UPDATE rules SET active_version = ? WHERE rule_id = ?`, []any{"v1", "rule-1"}},
	}
	for _, statement := range statements {
		if _, err := connection.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatalf("seed processing catalog: %v", err)
		}
	}
}

func openSQLiteTestConnection(t *testing.T, path string) *sql.DB {
	t.Helper()
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	uriPath := filepath.ToSlash(absolutePath)
	if filepath.VolumeName(absolutePath) != "" {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "rwc")
	query.Set("_busy_timeout", "5000")
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "FULL")
	query.Add("_pragma", "wal_autocheckpoint(1000)")
	dsn.RawQuery = query.Encode()
	connector, err := sqlite.NewConnector(dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	connection := sql.OpenDB(connector)
	if err := connection.PingContext(context.Background()); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	return connection
}

func processorMigrationFS() fs.FS {
	return os.DirFS(filepath.Join("..", "..", "migrations"))
}
