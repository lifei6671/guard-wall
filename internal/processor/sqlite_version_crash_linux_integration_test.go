//go:build linux && integration

package processor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/detection"
	"github.com/lifei6671/guard-wall/internal/source"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestSQLiteVersionCrashReplayUsesCurrentSnapshots(t *testing.T) {
	if mode := os.Getenv("GUARD_VERSION_CRASH_MODE"); mode != "" {
		runVersionCrashAttempt(t, mode)
		return
	}
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "before-commit")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteVersionCrashReplayUsesCurrentSnapshots$", "-test.timeout=30s")
	command.Env = append(os.Environ(), "GUARD_VERSION_CRASH_MODE=write", "GUARD_VERSION_CRASH_DB="+path, "GUARD_VERSION_CRASH_MARKER="+marker)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	reaped := false
	defer func() {
		if !reaped {
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
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
			t.Fatalf("writer exited before commit boundary: %v\n%s", err, output.String())
		case <-ctx.Done():
			t.Fatal("writer did not reach commit boundary")
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				waiting = false
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	if err := command.Process.Kill(); err != nil {
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
	// 版本切换在旧进程死亡之后提交，恢复进程只能读取新的 Active 指针。
	connection := openSQLiteTestConnection(t, path)
	for _, statement := range []string{
		`INSERT INTO parser_versions SELECT parser_id, 'v2', definition, definition_sha256, created_at_us FROM parser_versions WHERE version='v1'`,
		`INSERT INTO rule_versions SELECT rule_id, 'v2', definition, definition_sha256, created_at_us FROM rule_versions WHERE version='v1'`,
		`UPDATE parsers SET active_version='v2'`,
		`UPDATE rules SET active_version='v2'`,
	} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			t.Fatal(errors.Join(err, connection.Close()))
		}
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	reader := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteVersionCrashReplayUsesCurrentSnapshots$", "-test.timeout=30s")
	reader.Env = append(os.Environ(), "GUARD_VERSION_CRASH_MODE=read", "GUARD_VERSION_CRASH_DB="+path, "GUARD_VERSION_CRASH_MARKER="+marker)
	if output, err := reader.CombinedOutput(); err != nil {
		t.Fatalf("fresh-process replay: %v\n%s", err, output)
	}
}

func runVersionCrashAttempt(t *testing.T, mode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	path := os.Getenv("GUARD_VERSION_CRASH_DB")
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
	var parserVersion core.ParserVersion
	var ruleVersion core.RuleVersion
	if err := connection.QueryRowContext(ctx, `SELECT active_version FROM parsers WHERE parser_id='parser-1'`).Scan(&parserVersion); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(ctx, `SELECT active_version FROM rules WHERE rule_id='rule-1'`).Scan(&ruleVersion); err != nil {
		t.Fatal(err)
	}
	wantVersion := "v1"
	if mode == "read" {
		wantVersion = "v2"
	} else if mode != "write" {
		t.Fatalf("unknown mode %q", mode)
	}
	if string(parserVersion) != wantVersion || string(ruleVersion) != wantVersion {
		t.Fatalf("active snapshots = %s/%s", parserVersion, ruleVersion)
	}
	delivery := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_000_000, 0).UTC())
	session := beginProcessorSourceSession(t, database, delivery.Record.SourceID)
	if mode == "write" {
		if err := database.InitializeFileGenerationCoverage(ctx, session, "00112233445566778899aabbccddeeff"); err != nil {
			t.Fatal(err)
		}
	}
	assertCounts := func(want int) {
		t.Helper()
		for table, expected := range map[string]int{
			"parser_terminal_outcomes": want, "detection_terminal_outcomes": want,
			"detection_contributions": want, "alerts": want, "processing_receipts": want,
			"decisions": 0, "desired_ban_projections": 0, "audit_logs": 0,
		} {
			var count int
			if err := connection.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != expected {
				t.Fatalf("%s count=%d, want %d", table, count, expected)
			}
		}
	}
	assertCounts(0)
	if _, found, err := database.LoadSourceCheckpoint(ctx, delivery.Record.SourceID); err != nil || found {
		t.Fatalf("pre-replay checkpoint found=%v err=%v", found, err)
	}
	catalog := &mutablePlanCatalog{parsers: []ParserSnapshot{{ParserID: "parser-1", Version: parserVersion}}, rules: []RuleSnapshot{{RuleID: "rule-1", Version: ruleVersion}}}
	parsers := &scriptedParserRunner{events: map[core.ParserID][]core.EventFields{"parser-1": {{EventType: "auth.login_failed"}}}}
	evaluator := &scriptedRuleEvaluator{match: RuleMatch{Applicable: true, GroupKey: "group-a", DistinctKey: "alice"}, effects: fullDetectionEffects(delivery.Record.ObservedAt)}
	pipeline := NewPipeline(planNodeID, catalog, parsers, evaluator, detection.NewLedger())
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
	adapter := newEnforcingSQLiteStoreAdapter(t, database)
	if mode == "write" {
		// 现有测试 seam 在所有真实业务写入与 receipt 写入之后、SQLite Commit 之前执行。
		adapter.commit = func(_ *store.UnitOfWork) error {
			if len(parsers.calls) != 1 || evaluator.evaluateCalls != 1 {
				t.Fatal("v1 evaluation did not execute")
			}
			marker := os.Getenv("GUARD_VERSION_CRASH_MARKER")
			if err := os.WriteFile(marker+".tmp", []byte(delivery.ID), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(marker+".tmp", marker); err != nil {
				t.Fatal(err)
			}
			<-ctx.Done()
			return ctx.Err()
		}
	}
	coordinator := NewCoordinator(adapter, pipeline)
	completion, err := coordinator.Process(ctx, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "write" {
		t.Fatal("writer committed before SIGKILL")
	}
	identity, err := os.ReadFile(os.Getenv("GUARD_VERSION_CRASH_MARKER"))
	if err != nil || string(identity) != string(delivery.ID) {
		t.Fatalf("replay DeliveryID changed: %q/%s err=%v", identity, delivery.ID, err)
	}
	if completion.DeliveryID != delivery.ID || completion.Position != delivery.Record.Position || completion.Sequence != delivery.Sequence {
		t.Fatalf("completion=%+v", completion)
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
	if len(parsers.calls) != 1 || evaluator.matchCalls != 1 || evaluator.evaluateCalls != 1 {
		t.Fatalf("replay reevaluated: parser=%d match=%d evaluate=%d", len(parsers.calls), evaluator.matchCalls, evaluator.evaluateCalls)
	}
	assertCounts(1)
	for _, query := range []string{
		`SELECT count(*) FROM parser_terminal_outcomes WHERE parser_version='v2'`,
		`SELECT count(*) FROM detection_terminal_outcomes WHERE rule_version='v2'`,
		`SELECT count(*) FROM detection_contributions WHERE rule_version='v2'`,
	} {
		var count int
		if err := connection.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("current version row missing: %s", query)
		}
	}
	wantEvent, err := core.SecurityEventID(planNodeID, delivery.ID, "parser-1", "v2", 0)
	if err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := connection.QueryRowContext(ctx, `SELECT event_id FROM detection_contributions`).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if eventID != string(wantEvent) {
		t.Fatalf("replayed event=%s want %s", eventID, wantEvent)
	}
	receipt, found, err := database.FindProcessingReceipt(ctx, delivery.ID)
	if err != nil || !found || receipt.DeliveryID != delivery.ID || receipt.Position != delivery.Record.Position || receipt.Kind != core.ReceiptSuccess {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, delivery.Record.SourceID)
	if err != nil || !found || checkpoint.DeliverySequence != delivery.Sequence || checkpoint.Position != delivery.Record.Position {
		t.Fatalf("checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
}
