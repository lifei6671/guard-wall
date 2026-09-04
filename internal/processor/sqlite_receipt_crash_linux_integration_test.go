//go:build linux && integration

package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/detection"
	"github.com/lifei6671/guard-wall/internal/source"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestSQLiteReceiptCommitSIGKILLReplayPreservesEffects(t *testing.T) {
	if mode := os.Getenv("GUARD_RECEIPT_CRASH_MODE"); mode != "" {
		runReceiptCrashAttempt(t, mode)
		return
	}
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "committed.json")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	writer := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteReceiptCommitSIGKILLReplayPreservesEffects$", "-test.timeout=30s")
	writer.Env = append(os.Environ(), "GUARD_RECEIPT_CRASH_MODE=write", "GUARD_RECEIPT_CRASH_DB="+path, "GUARD_RECEIPT_CRASH_MARKER="+marker)
	var output bytes.Buffer
	writer.Stdout, writer.Stderr = &output, &output
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- writer.Wait() }()
	reaped := false
	defer func() {
		if !reaped {
			if err := writer.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("cleanup writer: %v", err)
			}
			if err := <-done; err != nil {
				t.Logf("cleanup writer exit: %v", err)
			}
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := true
	for waiting {
		select {
		case err := <-done:
			reaped = true
			t.Fatalf("writer exited before committed boundary: %v\n%s", err, output.String())
		case <-ctx.Done():
			t.Fatal("writer did not reach committed boundary")
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				waiting = false
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := <-done
	reaped = true
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("writer exit: %v", err)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("writer status: %v", exitError.Sys())
	}
	reader := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteReceiptCommitSIGKILLReplayPreservesEffects$", "-test.timeout=30s")
	reader.Env = append(os.Environ(), "GUARD_RECEIPT_CRASH_MODE=read", "GUARD_RECEIPT_CRASH_DB="+path, "GUARD_RECEIPT_CRASH_MARKER="+marker)
	if output, err := reader.CombinedOutput(); err != nil {
		t.Fatalf("fresh-process receipt replay: %v\n%s", err, output)
	}
	// 再次打开数据库确认 reader 写入的 checkpoint 已持久化。
	database, err = store.Open(ctx, path, processorMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	delivery := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_000_000, 0).UTC())
	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, delivery.Record.SourceID)
	if err != nil || !found || checkpoint.DeliverySequence != delivery.Sequence || checkpoint.Position != delivery.Record.Position {
		t.Fatalf("reopened checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
	generations, err := database.LoadRecoverableFileGenerations(ctx, delivery.Record.SourceID)
	if err != nil || len(generations) != 1 || generations[0].DurableEndOffset == nil || *generations[0].DurableEndOffset != 10 || generations[0].CoverageSessionID == nil || *generations[0].CoverageSessionID != checkpoint.SessionID {
		t.Fatalf("reopened coverage=%+v err=%v checkpoint=%+v", generations, err, checkpoint)
	}
}

type receiptCrashSnapshot struct {
	DeliveryID core.DeliveryID     `json:"delivery_id"`
	Rows       map[string][]string `json:"rows"`
}

func runReceiptCrashAttempt(t *testing.T, mode string) {
	if mode != "write" && mode != "read" {
		t.Fatalf("unknown mode %q", mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	path := os.Getenv("GUARD_RECEIPT_CRASH_DB")
	marker := os.Getenv("GUARD_RECEIPT_CRASH_MARKER")
	database, err := store.Open(ctx, path, processorMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	connection := openSQLiteTestConnection(t, path)
	defer func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	}()
	delivery := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_000_000, 0).UTC())
	session := beginProcessorSourceSession(t, database, delivery.Record.SourceID)
	if mode == "write" {
		if err := database.InitializeFileGenerationCoverage(ctx, session, "00112233445566778899aabbccddeeff"); err != nil {
			t.Fatal(err)
		}
	}
	generations, err := database.LoadRecoverableFileGenerations(ctx, delivery.Record.SourceID)
	if err != nil || len(generations) != 1 || generations[0].DurableEndOffset == nil || *generations[0].DurableEndOffset != 0 || generations[0].CoverageSessionID == nil {
		t.Fatalf("receipt replay recovery coverage=%+v err=%v", generations, err)
	}
	if mode == "read" && *generations[0].CoverageSessionID == session.ID() {
		t.Fatal("restart replaced historical coverage owner before replay")
	}
	// 恢复入口使用持久字节水位，新会话仍从序号 1 开始。
	delivery = sqliteDeliveryAt(t, *generations[0].DurableEndOffset, 10, delivery.Record.ObservedAt)
	snapshot := func() receiptCrashSnapshot {
		t.Helper()
		result := receiptCrashSnapshot{DeliveryID: delivery.ID, Rows: make(map[string][]string)}
		for table, want := range map[string]int{
			"parser_terminal_outcomes": 1, "detection_terminal_outcomes": 1, "detection_contributions": 1, "alerts": 1, "processing_receipts": 1,
			"decisions": 0, "desired_ban_projections": 0, "audit_logs": 0,
		} {
			rows, err := connection.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY 1,2,3")
			if err != nil {
				t.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				t.Fatal(errors.Join(err, rows.Close()))
			}
			result.Rows[table] = []string{}
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err := rows.Scan(pointers...); err != nil {
					t.Fatal(errors.Join(err, rows.Close()))
				}
				result.Rows[table] = append(result.Rows[table], fmt.Sprintf("%#v", values))
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				t.Fatal(err)
			}
			if len(result.Rows[table]) != want {
				t.Fatalf("%s rows=%d want=%d", table, len(result.Rows[table]), want)
			}
		}
		return result
	}
	parsers := &scriptedParserRunner{events: map[core.ParserID][]core.EventFields{"parser-1": {{EventType: "auth.login_failed"}}}}
	evaluator := &scriptedRuleEvaluator{match: RuleMatch{Applicable: true, GroupKey: "group-a", DistinctKey: "alice"}, effects: fullDetectionEffects(delivery.Record.ObservedAt)}
	if mode == "read" {
		parsers.failures = map[core.ParserID]error{"parser-1": errors.New("receipt replay must skip Parser")}
		evaluator.matchErr = errors.New("receipt replay must skip Rule")
	}
	pipeline := NewPipeline(planNodeID, &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}}, rules: []RuleSnapshot{{RuleID: "rule-1", Version: "v1"}},
	}, parsers, evaluator, detection.NewLedger())
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), pipeline)
	var persisted receiptCrashSnapshot
	if mode == "read" {
		contents, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(contents, &persisted); err != nil {
			t.Fatal(err)
		}
		if got := snapshot(); !reflect.DeepEqual(got, persisted) {
			t.Fatalf("SIGKILL changed committed snapshot: got=%v want=%v", got, persisted)
		}
	}
	if _, found, err := database.LoadSourceCheckpoint(ctx, delivery.Record.SourceID); err != nil || found {
		t.Fatalf("checkpoint before replay found=%v err=%v", found, err)
	}
	completion, err := coordinator.Process(ctx, delivery)
	if err != nil || completion.DeliveryID != delivery.ID || completion.Sequence != delivery.Sequence || completion.Position != delivery.Record.Position {
		t.Fatalf("completion=%+v err=%v", completion, err)
	}
	receipt, found, err := database.FindProcessingReceipt(ctx, delivery.ID)
	if err != nil || !found || receipt.DeliveryID != delivery.ID || receipt.SourceID != delivery.Record.SourceID || receipt.Position != delivery.Record.Position || receipt.Kind != core.ReceiptSuccess || receipt.Failure != nil {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
	if mode == "write" {
		if len(parsers.calls) != 1 || evaluator.matchCalls != 1 || evaluator.evaluateCalls != 1 {
			t.Fatal("writer did not evaluate Parser/Rule once")
		}
		persisted = snapshot()
		if _, found, err := database.LoadSourceCheckpoint(ctx, delivery.Record.SourceID); err != nil || found {
			t.Fatalf("writer checkpoint found=%v err=%v", found, err)
		}
		contents, err := json.Marshal(persisted)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker+".tmp", contents, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(marker+".tmp", marker); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		t.Fatal("writer was not SIGKILLed after committed marker")
	}
	tracker, err := source.NewCompletionTracker(delivery.Record.SourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	manager := source.NewCheckpointManager(tracker, source.NewSQLiteStateStore(database, session))
	if err := manager.Complete(ctx, completion); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := coordinator.Process(ctx, delivery); err != nil {
			t.Fatal(err)
		}
	}
	if len(parsers.calls) != 0 || evaluator.matchCalls != 0 || evaluator.evaluateCalls != 0 {
		t.Fatalf("receipt replay evaluated: parser=%d match=%d evaluate=%d", len(parsers.calls), evaluator.matchCalls, evaluator.evaluateCalls)
	}
	if got := snapshot(); !reflect.DeepEqual(got, persisted) {
		t.Fatalf("receipt replay changed committed rows: got=%v want=%v", got, persisted)
	}
	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, delivery.Record.SourceID)
	if err != nil || !found || checkpoint.DeliverySequence != delivery.Sequence || checkpoint.Position != delivery.Record.Position {
		t.Fatalf("replay checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
}
