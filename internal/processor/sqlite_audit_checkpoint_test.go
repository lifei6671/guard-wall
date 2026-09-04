package processor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/detection"
	"github.com/lifei6671/guard-wall/internal/source"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestSQLiteAuditFailurePreservesCheckpointAndRetriesOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	connection := openSQLiteTestConnection(t, path)
	defer func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	}()
	base := time.Unix(1_700_000_001, 0).UTC()
	first := sqliteDeliveryAt(t, 0, 10, base)
	second := sqliteDeliveryAt(t, 10, 20, base.Add(time.Second))
	parsers := &scriptedParserRunner{failures: map[core.ParserID]error{
		"parser-1": &PlanFailure{Class: PlanFailureRecordPermanent, Code: "malformed_record", SanitizedError: "record rejected", Action: "terminal_reject"},
	}}
	pipeline := NewPipeline(planNodeID, &mutablePlanCatalog{parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}}}, parsers, &scriptedRuleEvaluator{}, detection.NewLedger())
	pipeline.clock = func() time.Time { return base.Add(2 * time.Second) }
	adapter := &auditConstraintStore{SQLiteStoreAdapter: newEnforcingSQLiteStoreAdapter(t, database)}
	coordinator := NewCoordinator(adapter, pipeline)
	tracker, err := source.NewCompletionTracker(first.Record.SourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	manager := source.NewCheckpointManager(tracker, newProcessorCoverageState(t, database))
	completion, err := coordinator.Process(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Complete(ctx, completion); err != nil {
		t.Fatal(err)
	}
	baselineCheckpoint, found, err := database.LoadSourceCheckpoint(ctx, first.Record.SourceID)
	if err != nil || !found || baselineCheckpoint.DeliverySequence != 1 || baselineCheckpoint.Position != first.Record.Position {
		t.Fatalf("baseline checkpoint=%+v found=%v err=%v", baselineCheckpoint, found, err)
	}
	// 比较完整行而非只比较数量，避免既有业务记录被修改而漏检。
	tables := []string{"parser_terminal_outcomes", "detection_terminal_outcomes", "detection_contributions", "alerts", "decisions", "desired_ban_projections", "audit_logs", "processing_receipts"}
	snapshot := func() map[string][]string {
		t.Helper()
		result := make(map[string][]string)
		for _, table := range tables {
			rows, err := connection.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY 1,2,3")
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
		}
		return result
	}
	baseline := snapshot()
	for _, table := range []string{"parser_terminal_outcomes", "audit_logs", "processing_receipts"} {
		if len(baseline[table]) != 1 {
			t.Fatalf("baseline %s=%v", table, baseline[table])
		}
	}
	adapter.failAudit = true
	completion, err = coordinator.Process(ctx, second)
	if err == nil || adapter.auditError == nil || !errors.Is(err, adapter.auditError) || !strings.Contains(err.Error(), "append critical audit") || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected real Audit FK failure, got %v (writer=%v)", err, adapter.auditError)
	}
	if !reflect.DeepEqual(completion, core.DurableCompletion{}) {
		t.Fatalf("failed Audit emitted completion: %+v", completion)
	}
	if _, found, err := database.FindProcessingReceipt(ctx, second.ID); err != nil || found {
		t.Fatalf("failed receipt found=%v err=%v", found, err)
	}
	if got := snapshot(); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("failed Audit changed rows:\nbefore=%v\nafter=%v", baseline, got)
	}
	if err := manager.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, first.Record.SourceID)
	if err != nil || !found || !reflect.DeepEqual(checkpoint, baselineCheckpoint) {
		t.Fatalf("failed Audit advanced checkpoint: %+v, baseline=%+v err=%v", checkpoint, baselineCheckpoint, err)
	}
	adapter.failAudit = false
	completion, err = coordinator.Process(ctx, second)
	if err != nil || completion.DeliveryID != second.ID || completion.Sequence != 2 || completion.Position != second.Record.Position {
		t.Fatalf("retry completion=%+v err=%v", completion, err)
	}
	if err := manager.Complete(ctx, completion); err != nil {
		t.Fatal(err)
	}
	afterRetry := snapshot()
	for _, table := range tables {
		want := 0
		if table == "parser_terminal_outcomes" || table == "audit_logs" || table == "processing_receipts" {
			want = 2
		}
		if len(afterRetry[table]) != want {
			t.Fatalf("retry %s count=%d want=%d", table, len(afterRetry[table]), want)
		}
	}
	receipt, found, err := database.FindProcessingReceipt(ctx, second.ID)
	if err != nil || !found || receipt.Kind != core.ReceiptRecordPermanent || receipt.Position != second.Record.Position || receipt.Failure == nil || receipt.Failure.Code != "malformed_record" {
		t.Fatalf("retry receipt=%+v found=%v err=%v", receipt, found, err)
	}
	checkpoint, found, err = database.LoadSourceCheckpoint(ctx, first.Record.SourceID)
	if err != nil || !found || checkpoint.DeliverySequence != 2 || checkpoint.Position != second.Record.Position {
		t.Fatalf("retry checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := coordinator.Process(ctx, second); err != nil {
			t.Fatal(err)
		}
	}
	if len(parsers.calls) != 3 {
		t.Fatalf("parser calls=%d want baseline+failure+retry=3", len(parsers.calls))
	}
	if got := snapshot(); !reflect.DeepEqual(got, afterRetry) {
		t.Fatalf("receipt replay changed rows: %v", got)
	}
}

type auditConstraintStore struct {
	*SQLiteStoreAdapter
	failAudit  bool
	auditError error
}

func (s *auditConstraintStore) beginProcessing(ctx context.Context) (processingUnitOfWork, error) {
	unit, err := s.SQLiteStoreAdapter.beginProcessing(ctx)
	if err != nil {
		return nil, err
	}
	return &auditConstraintUnit{processingUnitOfWork: unit, owner: s}, nil
}

type auditConstraintUnit struct {
	processingUnitOfWork
	owner *auditConstraintStore
}

func (u *auditConstraintUnit) writeCriticalAudit(ctx context.Context, audit store.CriticalAudit) error {
	// 只改变测试输入；错误来自真实SQLite Audit写入的外键约束。
	if u.owner.failAudit {
		audit.NodeID = "11111111111111111111111111111111"
	}
	err := u.processingUnitOfWork.writeCriticalAudit(ctx, audit)
	u.owner.auditError = err
	return err
}
