package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

func sourceDataLossFixture(t *testing.T) (*Store, SourceDataLossAudit) {
	t.Helper()
	database := openTestStore(t)
	base := prepareFileSource(t, database, testSourceID, testGeneration, 20)
	session := beginTestSourceSession(t, database, testSourceID)
	ctx := context.Background()
	if err := database.InitializeFileGenerationCoverage(ctx, session, testGeneration); err != nil {
		t.Fatal(err)
	}
	position := filePosition(t, testGeneration, 0, 10)
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 1, position, []core.FilePosition{coverageSpan(testGeneration, 0, 10)}, base); err != nil {
		t.Fatal(err)
	}
	id, err := core.FileDeliveryID(testSourceID, coverageSpan(testGeneration, 0, 10))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := unit.PutReceipt(ctx, core.ProcessingReceipt{DeliveryID: id, SourceID: testSourceID, Position: position, Kind: core.ReceiptSuccess, Committed: base}); err != nil {
		t.Fatal(errors.Join(err, unit.Rollback()))
	}
	if err := unit.Commit(); err != nil {
		t.Fatal(err)
	}
	return database, SourceDataLossAudit{NodeID: "00112233445566778899aabbccddeeff", SourceID: testSourceID, Generation: testGeneration, DeviceID: 1, Inode: 2, PreviousSize: 20, ReadOffset: 10, ObservedSize: 5, ObservedAt: base.Add(time.Second)}
}

func TestSourceDataLossFirstEvidenceAndRecoveryState(t *testing.T) {
	database, event := sourceDataLossFixture(t)
	before := sourceDataLossRows(t, database)
	if err := database.RecordSourceDataLoss(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	var rows []criticalAuditRow
	if err := database.orm.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit count=%d", len(rows))
	}
	row := rows[0]
	if row.AuditID != row.IdempotencyKey || !strings.HasPrefix(row.AuditID, "source-data-loss:") || len(row.AuditID) != len("source-data-loss:")+64 || row.NodeID != string(event.NodeID) || row.Category != "source" || row.Action != "data_loss_suspected" || row.Result != "failure" || row.Severity != "warning" || row.Critical != 0 || row.ActorType != "source" || row.ErrorCode == nil || *row.ErrorCode != "DataLossSuspected" || row.DeliveryID != nil || row.AlertID != nil || row.DecisionID != nil || row.CreatedAtUS != event.ObservedAt.UnixMicro() {
		t.Fatalf("audit=%+v", row)
	}
	var details SourceDataLossAudit
	if err := json.Unmarshal([]byte(row.DetailsJSON), &details); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(event, details) {
		t.Fatalf("details=%+v, want=%+v", details, event)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(row.DetailsJSON), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 9 {
		t.Fatalf("unexpected details fields=%v", fields)
	}
	after := sourceDataLossRows(t, database)
	delete(after, "audit_logs")
	delete(before, "audit_logs")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("report changed non-audit rows")
	}
	first := sourceDataLossRows(t, database)
	event.ObservedAt = event.ObservedAt.Add(time.Hour)
	event.ObservedSize = 0
	if err := database.RecordSourceDataLoss(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, sourceDataLossRows(t, database)) {
		t.Fatal("duplicate replaced first evidence")
	}
}

func TestSourceDataLossInvalidInputAndIdentity(t *testing.T) {
	database, event := sourceDataLossFixture(t)
	if err := database.EnsureSource(context.Background(), "journal", event.NodeID, SourceKindJournald, event.ObservedAt); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*SourceDataLossAudit){
		"node-format":      func(e *SourceDataLossAudit) { e.NodeID = "bad" },
		"node-owner":       func(e *SourceDataLossAudit) { e.NodeID = testNodeID },
		"source":           func(e *SourceDataLossAudit) { e.SourceID = "missing" },
		"source-empty":     func(e *SourceDataLossAudit) { e.SourceID = "" },
		"source-long":      func(e *SourceDataLossAudit) { e.SourceID = core.SourceID(strings.Repeat("s", 129)) },
		"source-utf8":      func(e *SourceDataLossAudit) { e.SourceID = core.SourceID(string([]byte{255})) },
		"journald":         func(e *SourceDataLossAudit) { e.SourceID = "journal" },
		"generation":       func(e *SourceDataLossAudit) { e.Generation = strings.Repeat("e", 32) },
		"device":           func(e *SourceDataLossAudit) { e.DeviceID++ },
		"inode":            func(e *SourceDataLossAudit) { e.Inode++ },
		"device-range":     func(e *SourceDataLossAudit) { e.DeviceID = math.MaxUint64 },
		"inode-range":      func(e *SourceDataLossAudit) { e.Inode = math.MaxUint64 },
		"previous-range":   func(e *SourceDataLossAudit) { e.PreviousSize = math.MaxUint64 },
		"offset-range":     func(e *SourceDataLossAudit) { e.ReadOffset = math.MaxUint64 },
		"observed-range":   func(e *SourceDataLossAudit) { e.ObservedSize = math.MaxUint64 },
		"no-shrink":        func(e *SourceDataLossAudit) { e.ObservedSize = e.PreviousSize },
		"zero-time":        func(e *SourceDataLossAudit) { e.ObservedAt = time.Time{} },
		"epoch":            func(e *SourceDataLossAudit) { e.ObservedAt = time.Unix(0, 0) },
		"unencodable-time": func(e *SourceDataLossAudit) { e.ObservedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	before := sourceDataLossRows(t, database)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := event
			mutate(&bad)
			if err := database.RecordSourceDataLoss(context.Background(), bad); err == nil {
				t.Fatal("invalid event accepted")
			}
			if !reflect.DeepEqual(before, sourceDataLossRows(t, database)) {
				t.Fatal("rejected event changed rows")
			}
		})
	}
	event.PreviousSize = event.ObservedSize
	if err := database.RecordSourceDataLoss(context.Background(), event); err != nil {
		t.Fatalf("offset-only evidence: %v", err)
	}
}

func TestSourceDataLossFailureAndCancellation(t *testing.T) {
	database, event := sourceDataLossFixture(t)
	before := sourceDataLossRows(t, database)
	injected := errors.New("injected audit failure")
	const callback = "test:source-data-loss-failure"
	if err := database.orm.Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := database.RecordSourceDataLoss(context.Background(), event)
	if removeErr := database.orm.Callback().Create().Remove(callback); removeErr != nil {
		t.Fatal(removeErr)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("write error=%v", err)
	}
	if !reflect.DeepEqual(before, sourceDataLossRows(t, database)) {
		t.Fatal("failed audit survived rollback")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := database.RecordSourceDataLoss(ctx, event); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if !reflect.DeepEqual(before, sourceDataLossRows(t, database)) {
		t.Fatal("cancel changed rows")
	}
	if err := database.RecordSourceDataLoss(nil, event); err == nil {
		t.Fatal("nil context accepted")
	}
	if err := database.RecordSourceDataLoss(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

func TestSourceDataLossConflictingStoredEvidence(t *testing.T) {
	for _, column := range []string{"category", "details_json"} {
		t.Run(column, func(t *testing.T) {
			database, event := sourceDataLossFixture(t)
			if err := database.RecordSourceDataLoss(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			value := "other"
			if column == "details_json" {
				copy := event
				copy.Inode++
				data, err := json.Marshal(copy)
				if err != nil {
					t.Fatal(err)
				}
				value = string(data)
			}
			if _, err := database.db.Exec("UPDATE audit_logs SET "+column+" = ?", value); err != nil {
				t.Fatal(err)
			}
			before := sourceDataLossRows(t, database)
			if err := database.RecordSourceDataLoss(context.Background(), event); err == nil {
				t.Fatal("conflicting row swallowed")
			}
			if !reflect.DeepEqual(before, sourceDataLossRows(t, database)) {
				t.Fatal("conflict changed rows")
			}
		})
	}
}

func TestSourceDataLossConcurrentFirstReport(t *testing.T) {
	database, event := sourceDataLossFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 6)
	for range 6 {
		go func() { <-start; results <- database.RecordSourceDataLoss(ctx, event) }()
	}
	close(start)
	for range 6 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var count int64
	if err := database.orm.Model(&criticalAuditRow{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent audit count=%d", count)
	}
}

// 所有用户表逐行比较，避免仅比较行数而漏掉水位或业务字段更新。
func sourceDataLossRows(t *testing.T, database *Store) map[string][]string {
	t.Helper()
	var tables []string
	if err := database.orm.Raw("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]string)
	for _, table := range tables {
		rows, err := database.db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
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
			result[table] = append(result[table], fmt.Sprintf("%#v", values))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		sort.Strings(result[table])
	}
	return result
}
