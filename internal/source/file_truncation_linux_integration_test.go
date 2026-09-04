//go:build linux && integration

package source

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
	"modernc.org/sqlite"
)

// 这些测试驱动有限观测器，不代表生产 File reader 或跨重启 Health 恢复。
func TestFileTruncationNativeObservations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, mode := range []string{"append", "truncate", "rename", "fast-regrow"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "access.log")
			file := openTruncationFile(t, path)
			if _, err := file.WriteString("0123456789"); err != nil {
				t.Fatal(err)
			}
			base := nativeTruncationObservation(t, file, 8)
			var reports []store.SourceDataLossAudit
			observer, err := NewFileTruncationObserver(sourceRestartNodeID, sourceRestartSourceID, sourceRestartOldGeneration, base, func(_ context.Context, event store.SourceDataLossAudit) error {
				reports = append(reports, event)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			want := FileNoTruncationEvidence
			switch mode {
			case "append":
				if _, err := file.WriteString("abc"); err != nil {
					t.Fatal(err)
				}
			case "truncate":
				if err := file.Truncate(2); err != nil {
					t.Fatal(err)
				}
				want = FileDataLossSuspected
			case "rename":
				if err := os.Rename(path, path+".1"); err != nil {
					t.Fatal(err)
				}
				replacement := openTruncationFile(t, path)
				if _, err := replacement.WriteString("new"); err != nil {
					t.Fatal(err)
				}
				if got, err := observer.Observe(ctx, nativeTruncationObservation(t, replacement, 0)); err != nil || got != FileIdentityChanged {
					t.Fatalf("replacement identity=%s err=%v", got, err)
				}
				// rename 不改变仍打开的旧 fd 身份，也不重置其观测基线。
			case "fast-regrow":
				if err := file.Truncate(0); err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteAt([]byte("replacement-data"), 0); err != nil {
					t.Fatal(err)
				}
			}
			current := nativeTruncationObservation(t, file, 8)
			got, err := observer.Observe(ctx, current)
			if err != nil || got != want {
				t.Fatalf("Observe=%s err=%v want=%s", got, err, want)
			}
			if mode == "truncate" {
				wantEvent := store.SourceDataLossAudit{NodeID: sourceRestartNodeID, SourceID: sourceRestartSourceID, Generation: sourceRestartOldGeneration, DeviceID: base.DeviceID, Inode: base.Inode, PreviousSize: 10, ReadOffset: 8, ObservedSize: 2, ObservedAt: current.ObservedAt}
				if len(reports) != 1 || reports[0] != wantEvent || observer.Health() != (FileTruncationHealth{Degraded: true, StopReading: true, AuditRecorded: true}) {
					t.Fatalf("reports=%+v health=%+v", reports, observer.Health())
				}
			} else if len(reports) != 0 || observer.Health() != (FileTruncationHealth{}) {
				t.Fatalf("unexpected report=%+v health=%+v", reports, observer.Health())
			}
		})
	}
}

func TestFileTruncationSQLiteAuditRetryAndReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	file := openTruncationFile(t, path)
	if _, err := file.WriteString("0123456789abcdefghij"); err != nil {
		t.Fatal(err)
	}
	base := nativeTruncationObservation(t, file, 10)
	databasePath := filepath.Join(dir, "guard.db")
	database, err := store.Open(ctx, databasePath, sourceMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := database.EnsureNodeIdentity(ctx, sourceRestartNodeID, base.ObservedAt); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(ctx, sourceRestartSourceID, sourceRestartNodeID, store.SourceKindFile, base.ObservedAt); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterFileGeneration(ctx, store.FileGeneration{SourceID: sourceRestartSourceID, Generation: sourceRestartOldGeneration, DeviceID: base.DeviceID, Inode: base.Inode, Path: path, ObservedSize: base.Size, OpenedAt: base.ObservedAt}); err != nil {
		t.Fatal(err)
	}
	session := beginSQLiteSourceSession(t, database, sourceRestartSourceID)
	if err := database.InitializeFileGenerationCoverage(ctx, session, sourceRestartOldGeneration); err != nil {
		t.Fatal(err)
	}
	position := sourceRestartPosition(t, sourceRestartOldGeneration, base.DeviceID, base.Inode)
	putSourceRestartReceipt(t, ctx, database, sourceRestartDeliveryID(t, position), position, base.ObservedAt)
	span, _ := position.File()
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 1, position, []core.FilePosition{span}, base.ObservedAt); err != nil {
		t.Fatal(err)
	}

	// 与 Store 使用相同连接约束；独立连接只用于测试故障注入及完整行读取。
	dsn := &url.URL{Scheme: "file", Path: databasePath}
	query := dsn.Query()
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
	inspection := sql.OpenDB(connector)
	defer func() {
		if err := inspection.Close(); err != nil {
			t.Error(err)
		}
	}()
	before := truncationProtectedRows(t, ctx, inspection)
	if _, err := inspection.ExecContext(ctx, `CREATE TRIGGER reject_source_loss BEFORE INSERT ON audit_logs WHEN NEW.action = 'data_loss_suspected' BEGIN SELECT RAISE(ABORT, 'test source audit rejected'); END`); err != nil {
		t.Fatal(err)
	}
	observer, err := NewFileTruncationObserver(sourceRestartNodeID, sourceRestartSourceID, sourceRestartOldGeneration, base, database.RecordSourceDataLoss)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(2); err != nil {
		t.Fatal(err)
	}
	first := nativeTruncationObservation(t, file, 10)
	if got, err := observer.Observe(ctx, first); got != FileDataLossSuspected || err == nil || !strings.Contains(err.Error(), "test source audit rejected") {
		t.Fatalf("rejected report=%s err=%v", got, err)
	}
	if observer.Health() != (FileTruncationHealth{Degraded: true, StopReading: true}) {
		t.Fatalf("failed health=%+v", observer.Health())
	}
	var count int
	if err := inspection.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed audit count=%d err=%v", count, err)
	}
	if after := truncationProtectedRows(t, ctx, inspection); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed report changed protected rows: before=%v after=%v", before, after)
	}
	if _, err := inspection.ExecContext(ctx, "DROP TRIGGER reject_source_loss"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("regrown-data-after-failure"), 0); err != nil {
		t.Fatal(err)
	}
	if got, err := observer.Observe(ctx, nativeTruncationObservation(t, file, 10)); err != nil || got != FileDataLossSuspected {
		t.Fatalf("retry=%s err=%v", got, err)
	}
	if observer.Health() != (FileTruncationHealth{Degraded: true, StopReading: true, AuditRecorded: true}) {
		t.Fatalf("retry health=%+v", observer.Health())
	}
	want := store.SourceDataLossAudit{NodeID: sourceRestartNodeID, SourceID: sourceRestartSourceID, Generation: sourceRestartOldGeneration, DeviceID: base.DeviceID, Inode: base.Inode, PreviousSize: base.Size, ReadOffset: 10, ObservedSize: 2, ObservedAt: first.ObservedAt}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = store.Open(ctx, databasePath, sourceMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	inspection = sql.OpenDB(connector)
	// 重开后显式重复提交不同观测，首次完整证据不得改变。
	later := want
	later.ObservedSize = 1
	later.ObservedAt = later.ObservedAt.Add(time.Second)
	if err := database.RecordSourceDataLoss(ctx, later); err != nil {
		t.Fatal(err)
	}
	var details, category, action, result, severity, actor, code string
	var critical int
	var created int64
	if err := inspection.QueryRowContext(ctx, `SELECT details_json, category, action, result, severity, actor_type, error_code, critical, created_at_us FROM audit_logs`).Scan(&details, &category, &action, &result, &severity, &actor, &code, &critical, &created); err != nil {
		t.Fatal(err)
	}
	var recorded store.SourceDataLossAudit
	if err := json.Unmarshal([]byte(details), &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != want || category != "source" || action != "data_loss_suspected" || result != "failure" || severity != "warning" || actor != "source" || code != "DataLossSuspected" || critical != 0 || created != want.ObservedAt.UnixMicro() {
		t.Fatalf("audit=%+v metadata=%s/%s/%s/%s/%s/%s/%d/%d", recorded, category, action, result, severity, actor, code, critical, created)
	}
	if err := inspection.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	if after := truncationProtectedRows(t, ctx, inspection); !reflect.DeepEqual(after, before) {
		t.Fatalf("report changed protected rows: before=%v after=%v", before, after)
	}
}

func openTruncationFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	return file
}

func nativeTruncationObservation(t *testing.T, file *os.File, offset uint64) FileObservation {
	t.Helper()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("Linux stat identity unavailable")
	}
	return FileObservation{DeviceID: uint64(stat.Dev), Inode: stat.Ino, Size: uint64(info.Size()), ReadOffset: offset, ObservedAt: time.Now().UTC()}
}

func truncationProtectedRows(t *testing.T, ctx context.Context, database *sql.DB) map[string][][]any {
	t.Helper()
	snapshot := make(map[string][][]any)
	for _, table := range []string{"sources", "source_file_generations", "processing_receipts"} {
		rows, err := database.QueryContext(ctx, "SELECT * FROM "+table)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(errors.Join(err, rows.Close()))
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(errors.Join(err, rows.Close()))
			}
			for i, value := range values {
				if bytes, ok := value.([]byte); ok {
					values[i] = string(bytes)
				}
			}
			snapshot[table] = append(snapshot[table], values)
		}
		iterationErr := rows.Err()
		closeErr := rows.Close()
		if err := errors.Join(iterationErr, closeErr); err != nil {
			t.Fatal(err)
		}
		if len(snapshot[table]) != 1 {
			t.Fatalf("expected nonempty singleton %s, got %v", table, snapshot[table])
		}
	}
	return snapshot
}
