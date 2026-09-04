//go:build linux && integration

package processor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/detection"
	"github.com/lifei6671/guard-wall/internal/source"
	"github.com/lifei6671/guard-wall/internal/store"
)

// 测试专用子进程验证 Source runtime 的真实信号边界；不替代 guard-agent 接线验收。
func TestSourceRuntimeSIGTERMDrainsToSQLite(t *testing.T) {
	base := time.Unix(1_700_000_001, 0).UTC()
	deliveries := []core.Delivery{
		sqliteDeliveryAt(t, 0, 10, base),
		sqliteDeliveryAt(t, 10, 20, base.Add(time.Second)),
	}
	deliveries[0].Record.Content = []byte("poison")
	if mode := os.Getenv("GUARD_SOURCE_SIGTERM_MODE"); mode != "" {
		if mode != "worker" {
			t.Fatalf("unknown child mode %q", mode)
		}
		runSourceSIGTERMChild(t, deliveries)
		return
	}
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "parser-ready")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSourceRuntimeSIGTERMDrainsToSQLite$", "-test.timeout=40s")
	child.Env = append(os.Environ(), "GUARD_SOURCE_SIGTERM_MODE=worker", "GUARD_SOURCE_SIGTERM_DB="+path, "GUARD_SOURCE_SIGTERM_READY="+marker)
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
			t.Fatalf("child exited before blocked Parser boundary: %v\n%s", err, output.String())
		case <-ctx.Done():
			t.Fatal("child did not reach blocked Parser boundary")
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				ready = true
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	// 仅向本测试启动并确认进入屏障的子 PID 发送 SIGTERM。
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := <-done
	reaped = true
	if err != nil {
		t.Fatalf("SIGTERM drain failed: %v\n%s", err, output.String())
	}
	status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Exited() || status.ExitStatus() != 0 {
		t.Fatalf("child did not exit normally: %v", child.ProcessState)
	}
	// 子进程退出后重新 Open，验证落盘状态而非进程内缓存。
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

func runSourceSIGTERMChild(t *testing.T, deliveries []core.Delivery) {
	// NotifyContext 已安装后才可能发布 ready；屏障只由真实 SIGTERM 解除。
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	path := os.Getenv("GUARD_SOURCE_SIGTERM_DB")
	setupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	database, err := store.Open(setupCtx, path, processorMigrationFS())
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	connection := openSQLiteTestConnection(t, path)
	defer func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	}()
	queue, err := source.NewDeliveryQueue(len(deliveries))
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range deliveries {
		if err := queue.Enqueue(runCtx, delivery); err != nil {
			t.Fatal(err)
		}
	}
	parsers := &sourceSIGTERMParser{signal: runCtx, marker: os.Getenv("GUARD_SOURCE_SIGTERM_READY")}
	pipeline := NewPipeline(planNodeID, &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-1", Version: "v1"}},
	}, parsers, statelessRuleEvaluator{}, detection.NewLedger())
	pipeline.clock = func() time.Time { return deliveries[1].Record.ObservedAt.Add(time.Second) }
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), pipeline)
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	closer := &sourceRuntimeValidatingCloser{database: database, validate: func(context.Context) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return validateSourceSIGTERMState(ctx, database, connection, deliveries)
	}}
	if err := RunSourceRuntime(runCtx, 30*time.Second, queue, coordinator, checkpoints, closer); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(runCtx.Err(), context.Canceled) || parsers.calls != 2 || closer.Calls() != 1 {
		t.Fatalf("shutdown state: signal=%v Parser calls=%d Close calls=%d", runCtx.Err(), parsers.calls, closer.Calls())
	}
	if err := queue.Enqueue(context.Background(), deliveries[0]); !errors.Is(err, source.ErrDeliveryQueueSealed) {
		t.Fatalf("queue after shutdown: %v", err)
	}
}

type sourceSIGTERMParser struct {
	signal context.Context
	marker string
	calls  int
}

func (p *sourceSIGTERMParser) RunParser(ctx context.Context, _ ParserSnapshot, record core.RawRecord) (ParserExecution, error) {
	p.calls++
	if p.calls == 1 {
		if err := os.WriteFile(p.marker+".tmp", []byte("accepted=2;parser=1"), 0600); err != nil {
			return ParserExecution{}, err
		}
		if err := os.Rename(p.marker+".tmp", p.marker); err != nil {
			return ParserExecution{}, err
		}
		select {
		case <-p.signal.Done():
		case <-ctx.Done():
			return ParserExecution{}, fmt.Errorf("worker cancelled at signal barrier: %w", ctx.Err())
		case <-time.After(10 * time.Second):
			return ParserExecution{}, errors.New("SIGTERM not received")
		}
	}
	// 两条已接受记录均必须在信号之后使用仍可用的 worker context 完成。
	if p.signal.Err() == nil || ctx.Err() != nil {
		return ParserExecution{}, fmt.Errorf("drain contexts: signal=%v worker=%v", p.signal.Err(), ctx.Err())
	}
	if string(record.Content) == "poison" {
		return ParserExecution{}, &PlanFailure{Class: PlanFailureRecordPermanent, Code: "malformed_record", SanitizedError: "record rejected", Action: "terminal_reject"}
	}
	return ParserExecution{Events: []core.EventFields{{EventType: "auth.login_failed"}}}, nil
}

// Close 前与重新 Open 后复用同一组直接断言，覆盖身份、位置和业务副作用。
func validateSourceSIGTERMState(ctx context.Context, database *store.Store, connection *sql.DB, deliveries []core.Delivery) error {
	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, deliveries[1].Record.SourceID)
	if err != nil || !found || checkpoint.DeliverySequence != 2 || checkpoint.Position != deliveries[1].Record.Position {
		return fmt.Errorf("checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
	for i, delivery := range deliveries {
		receipt, found, err := database.FindProcessingReceipt(ctx, delivery.ID)
		if err != nil || !found || receipt.DeliveryID != delivery.ID || receipt.SourceID != delivery.Record.SourceID || receipt.Position != delivery.Record.Position {
			return fmt.Errorf("receipt[%d]=%+v found=%v err=%v", i, receipt, found, err)
		}
		wantKind, wantEmitted := "success", 1
		if i == 0 {
			wantKind, wantEmitted = "record_permanent", 0
			if receipt.Kind != core.ReceiptRecordPermanent || receipt.Failure == nil || receipt.Failure.Stage != "parser" || receipt.Failure.Code != "malformed_record" {
				return fmt.Errorf("poison receipt=%+v", receipt)
			}
		} else if receipt.Kind != core.ReceiptSuccess || receipt.Failure != nil {
			return fmt.Errorf("success receipt=%+v", receipt)
		}
		var kind string
		var emitted int
		var code sql.NullString
		if err := connection.QueryRowContext(ctx, `SELECT kind, emitted_count, failure_code FROM parser_terminal_outcomes WHERE delivery_id=? AND parser_id='parser-1' AND parser_version='v1'`, delivery.ID).Scan(&kind, &emitted, &code); err != nil {
			return err
		}
		if kind != wantKind || emitted != wantEmitted || (i == 0 && (!code.Valid || code.String != "malformed_record")) || (i == 1 && code.Valid) {
			return fmt.Errorf("Parser outcome[%d]=%s/%d/%v", i, kind, emitted, code)
		}
	}
	var auditDelivery, auditNode, auditCode string
	var critical int
	if err := connection.QueryRowContext(ctx, `SELECT delivery_id, node_id, error_code, critical FROM audit_logs`).Scan(&auditDelivery, &auditNode, &auditCode, &critical); err != nil {
		return err
	}
	if auditDelivery != string(deliveries[0].ID) || auditNode != string(planNodeID) || auditCode != "malformed_record" || critical != 1 {
		return fmt.Errorf("Audit=%s/%s/%s/%d", auditDelivery, auditNode, auditCode, critical)
	}
	eventID, err := core.SecurityEventID(planNodeID, deliveries[1].ID, "parser-1", "v1", 0)
	if err != nil {
		return err
	}
	var contributions int
	if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM detection_contributions WHERE event_id=? AND rule_id='rule-1' AND rule_version='v1'`, eventID).Scan(&contributions); err != nil || contributions != 1 {
		return fmt.Errorf("normal contribution=%d err=%v", contributions, err)
	}
	for table, want := range map[string]int{
		"processing_receipts": 2, "parser_terminal_outcomes": 2, "audit_logs": 1,
		"detection_terminal_outcomes": 1, "detection_contributions": 1,
		"alerts": 0, "decisions": 0, "desired_ban_projections": 0,
	} {
		var count int
		if err := connection.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != want {
			return fmt.Errorf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	return nil
}
