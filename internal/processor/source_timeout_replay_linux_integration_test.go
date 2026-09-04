//go:build linux && integration

package processor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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

func TestSourceRuntimeTimeoutRestartReplaysOnce(t *testing.T) {
	base := time.Unix(1_700_000_001, 0).UTC()
	deliveries := []core.Delivery{sqliteDeliveryAt(t, 0, 10, base), sqliteDeliveryAt(t, 10, 20, base.Add(time.Second))}
	deliveries[0].Record.Content = []byte("poison")
	if mode := os.Getenv("GUARD_SOURCE_TIMEOUT_MODE"); mode != "" {
		runSourceTimeoutChild(t, mode, deliveries)
		return
	}
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "baseline.json")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSourceRuntimeTimeoutRestartReplaysOnce$", "-test.timeout=20s")
	child.Env = append(os.Environ(), "GUARD_SOURCE_TIMEOUT_MODE=timeout", "GUARD_SOURCE_TIMEOUT_DB="+path, "GUARD_SOURCE_TIMEOUT_MARKER="+marker)
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	reaped := false
	defer func() {
		if !reaped {
			if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("cleanup child: %v", err)
			}
			if err := <-done; err != nil {
				t.Logf("cleanup child exit: %v", err)
			}
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	ready := false
	for !ready {
		select {
		case err := <-done:
			reaped = true
			t.Fatalf("child exited before timeout boundary: %v\n%s", err, output.String())
		case <-ctx.Done():
			t.Fatal("child did not reach timeout boundary")
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				ready = true
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := <-done
	reaped = true
	if err != nil {
		t.Fatalf("timeout child: %v\n%s", err, output.String())
	}
	status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Exited() || status.ExitStatus() != 0 {
		t.Fatalf("timeout child status: %v", child.ProcessState)
	}
	proof, err := os.ReadFile(marker + ".timeout")
	if err != nil || string(proof) != "deadline_exceeded;worker_cancelled;close_calls=0" {
		t.Fatalf("timeout proof=%q err=%v", proof, err)
	}
	reader := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSourceRuntimeTimeoutRestartReplaysOnce$", "-test.timeout=20s")
	reader.Env = append(os.Environ(), "GUARD_SOURCE_TIMEOUT_MODE=replay", "GUARD_SOURCE_TIMEOUT_DB="+path, "GUARD_SOURCE_TIMEOUT_MARKER="+marker)
	if output, err := reader.CombinedOutput(); err != nil {
		t.Fatalf("fresh-process replay: %v\n%s", err, output)
	}
	reopened, err := store.Open(ctx, path, processorMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	connection := openSQLiteTestConnection(t, path)
	defer func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := validateSourceSIGTERMState(ctx, reopened, connection, deliveries); err != nil {
		t.Fatal(err)
	}
}

func runSourceTimeoutChild(t *testing.T, mode string, deliveries []core.Delivery) {
	if mode != "timeout" && mode != "replay" {
		t.Fatalf("unknown mode %q", mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path, marker := os.Getenv("GUARD_SOURCE_TIMEOUT_DB"), os.Getenv("GUARD_SOURCE_TIMEOUT_MARKER")
	database, err := store.Open(ctx, path, processorMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	connection := openSQLiteTestConnection(t, path)
	// timeout 分支由进程 owner 直接退出，不能关闭仍由 worker 持有的 Store。
	if mode == "replay" {
		defer func() {
			if err := errors.Join(connection.Close(), database.Close()); err != nil {
				t.Error(err)
			}
		}()
	}
	parsers := &sourceTimeoutParser{cancelled: make(chan struct{})}
	pipeline := NewPipeline(planNodeID, &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-1", Version: "v1"}},
	}, parsers, statelessRuleEvaluator{}, detection.NewLedger())
	pipeline.clock = func() time.Time { return deliveries[1].Record.ObservedAt.Add(time.Second) }
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), pipeline)
	if mode == "timeout" {
		checkpoints := newSourceRuntimeCheckpoints(t, database)
		parsers.block = func() error {
			checkpoint, found, err := database.LoadSourceCheckpoint(ctx, deliveries[0].Record.SourceID)
			if err != nil || !found || checkpoint.DeliverySequence != 1 || checkpoint.Position != deliveries[0].Record.Position {
				return fmt.Errorf("baseline checkpoint=%+v found=%v err=%v", checkpoint, found, err)
			}
			rows, err := sourceTimeoutRows(ctx, connection)
			if err != nil {
				return err
			}
			contents, err := json.Marshal(rows)
			if err != nil {
				return err
			}
			if err := os.WriteFile(marker+".tmp", contents, 0600); err != nil {
				return err
			}
			return os.Rename(marker+".tmp", marker)
		}
		// 只接真实 SIGTERM，不继承辅助查询的 deadline，避免信号假阳性。
		runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		queue, err := source.NewDeliveryQueue(len(deliveries))
		if err != nil {
			t.Fatal(err)
		}
		for _, delivery := range deliveries {
			if err := queue.Enqueue(runCtx, delivery); err != nil {
				t.Fatal(err)
			}
		}
		closer := &sourceRuntimeValidatingCloser{database: database}
		// 100ms 是此测试的加速 deadline；产品 shutdown_timeout 默认仍为 30s。
		err = RunSourceRuntime(runCtx, 100*time.Millisecond, queue, coordinator, checkpoints, closer)
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(runCtx.Err(), context.Canceled) {
			t.Fatalf("timeout=%v signal=%v", err, runCtx.Err())
		}
		select {
		case <-parsers.cancelled:
		case <-ctx.Done():
			t.Fatal("worker did not observe cancellation")
		}
		if closer.Calls() != 0 || parsers.calls != 2 {
			t.Fatalf("timeout Close=%d Parser=%d", closer.Calls(), parsers.calls)
		}
		if err := os.WriteFile(marker+".timeout", []byte("deadline_exceeded;worker_cancelled;close_calls=0"), 0600); err != nil {
			t.Fatal(err)
		}
		// worker 永不释放；timeout 后不重用或关闭 DB，终止整个测试专用进程。
		os.Exit(0)
	}
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var baseline map[string][]string
	if err := json.Unmarshal(contents, &baseline); err != nil {
		t.Fatal(err)
	}
	before, err := sourceTimeoutRows(ctx, connection)
	if err != nil || !reflect.DeepEqual(before, baseline) {
		t.Fatalf("timeout changed durable baseline: before=%v baseline=%v err=%v", before, baseline, err)
	}
	if _, found, err := database.FindProcessingReceipt(ctx, deliveries[1].ID); err != nil || found {
		t.Fatalf("unfinished receipt found=%v err=%v", found, err)
	}
	// 先完整核对 crash 前快照，再建立新 session；旧 checkpoint 全行仍保持。
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	for _, delivery := range deliveries {
		completion, err := coordinator.Process(ctx, delivery)
		if err != nil || completion.DeliveryID != delivery.ID || completion.Sequence != delivery.Sequence || completion.Position != delivery.Record.Position {
			t.Fatalf("replay completion=%+v err=%v", completion, err)
		}
		if err := checkpoints.Complete(ctx, completion); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateSourceSIGTERMState(ctx, database, connection, deliveries); err != nil {
		t.Fatal(err)
	}
	after, err := sourceTimeoutRows(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		for _, delivery := range deliveries {
			if _, err := coordinator.Process(ctx, delivery); err != nil {
				t.Fatal(err)
			}
		}
	}
	repeated, err := sourceTimeoutRows(ctx, connection)
	if err != nil || !reflect.DeepEqual(after, repeated) || parsers.calls != 1 {
		t.Fatalf("repeated replay changed effects: Parser=%d after=%v repeated=%v err=%v", parsers.calls, after, repeated, err)
	}
}

type sourceTimeoutParser struct {
	block     func() error
	cancelled chan struct{}
	calls     int
}

func (p *sourceTimeoutParser) RunParser(ctx context.Context, _ ParserSnapshot, record core.RawRecord) (ParserExecution, error) {
	p.calls++
	if string(record.Content) == "poison" {
		return ParserExecution{}, &PlanFailure{Class: PlanFailureRecordPermanent, Code: "malformed_record", SanitizedError: "record rejected", Action: "terminal_reject"}
	}
	if p.block != nil {
		if err := p.block(); err != nil {
			return ParserExecution{}, err
		}
		<-ctx.Done()
		close(p.cancelled)
		select {} // 模拟不会及时退出的 worker，只有进程退出可以回收。
	}
	return ParserExecution{Events: []core.EventFields{{EventType: "auth.login_failed"}}}, nil
}

// 保留完整行，证明 timeout 及重复投递均不改写任何既有业务状态。
func sourceTimeoutRows(ctx context.Context, connection *sql.DB) (map[string][]string, error) {
	result := make(map[string][]string)
	for _, table := range []string{"sources", "source_checkpoints", "processing_receipts", "parser_terminal_outcomes", "detection_terminal_outcomes", "detection_contributions", "audit_logs", "alerts", "decisions", "desired_ban_projections"} {
		rows, err := connection.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY 1,2,3")
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		result[table] = []string{}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			result[table] = append(result[table], fmt.Sprintf("%#v", values))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return result, nil
}
