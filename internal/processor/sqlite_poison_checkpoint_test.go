package processor

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/detection"
	"github.com/lifei6671/guard-wall/internal/source"
)

func TestSQLitePoisonCheckpointAndNonTerminalRetry(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure PlanFailureClass
	}{
		{"poison_then_success", 0},
		{"transient", PlanFailureTransient},
		{"blocked", PlanFailureBlocked},
		{"cancelled", PlanFailureCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			poison := sqliteDeliveryAt(t, 0, 10, base)
			poison.Record.Content = []byte("poison")
			normal := sqliteDeliveryAt(t, 10, 20, base.Add(time.Second))
			parsers := &poisonCheckpointParser{}
			pipeline := NewPipeline(planNodeID, &mutablePlanCatalog{
				parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
				rules:   []RuleSnapshot{{RuleID: "rule-1", Version: "v1"}},
			}, parsers, statelessRuleEvaluator{}, detection.NewLedger())
			pipeline.clock = func() time.Time { return base.Add(2 * time.Second) }
			coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), pipeline)
			tracker, err := source.NewCompletionTracker(poison.Record.SourceID, 1)
			if err != nil {
				t.Fatal(err)
			}
			manager := source.NewCheckpointManager(tracker, newProcessorCoverageState(t, database))
			completion, err := coordinator.Process(ctx, poison)
			if err != nil || completion.DeliveryID != poison.ID || completion.Sequence != 1 || completion.Position != poison.Record.Position {
				t.Fatalf("poison completion=%+v err=%v", completion, err)
			}
			if err := manager.Complete(ctx, completion); err != nil {
				t.Fatal(err)
			}
			receipt, found, err := database.FindProcessingReceipt(ctx, poison.ID)
			if err != nil || !found || receipt.Kind != core.ReceiptRecordPermanent || receipt.Position != poison.Record.Position || receipt.Failure == nil || receipt.Failure.Stage != "parser" || receipt.Failure.Code != "malformed_record" {
				t.Fatalf("poison receipt=%+v found=%v err=%v", receipt, found, err)
			}
			var kind, code string
			if err := connection.QueryRowContext(ctx, `SELECT kind, failure_code FROM parser_terminal_outcomes WHERE delivery_id=?`, string(poison.ID)).Scan(&kind, &code); err != nil {
				t.Fatal(err)
			}
			if kind != "record_permanent" || code != "malformed_record" {
				t.Fatalf("poison outcome=%s/%s", kind, code)
			}
			if err := connection.QueryRowContext(ctx, `SELECT error_code FROM audit_logs`).Scan(&code); err != nil {
				t.Fatal(err)
			}
			if code != "malformed_record" {
				t.Fatalf("poison audit code=%s", code)
			}
			baseline, found, err := database.LoadSourceCheckpoint(ctx, poison.Record.SourceID)
			if err != nil || !found || baseline.DeliverySequence != 1 || baseline.Position != poison.Record.Position {
				t.Fatalf("baseline checkpoint=%+v found=%v err=%v", baseline, found, err)
			}
			assertCounts := func(success bool) {
				t.Helper()
				normalCount := 0
				if success {
					normalCount = 1
				}
				for table, want := range map[string]int{
					"parser_terminal_outcomes": 1 + normalCount, "audit_logs": 1, "processing_receipts": 1 + normalCount,
					"detection_terminal_outcomes": normalCount, "detection_contributions": normalCount,
					"alerts": 0, "decisions": 0, "desired_ban_projections": 0,
				} {
					var got int
					if err := connection.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
						t.Fatal(err)
					}
					if got != want {
						t.Fatalf("%s count=%d want=%d", table, got, want)
					}
				}
			}
			assertCounts(false)
			if test.failure != 0 {
				attemptCtx, attemptCancel := context.WithCancel(ctx)
				parsers.failure, parsers.cancel = test.failure, attemptCancel
				completion, err = coordinator.Process(attemptCtx, normal)
				attemptCancel()
				var failure *PlanFailure
				if !errors.As(err, &failure) || failure.Class != test.failure {
					t.Fatalf("non-terminal error=%v want class=%v", err, test.failure)
				}
				if test.failure == PlanFailureCancelled && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation identity lost: %v", err)
				}
				if !reflect.DeepEqual(completion, core.DurableCompletion{}) {
					t.Fatalf("non-terminal completion=%+v", completion)
				}
				if _, found, err := database.FindProcessingReceipt(ctx, normal.ID); err != nil || found {
					t.Fatalf("non-terminal receipt found=%v err=%v", found, err)
				}
				var outcomes int
				if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM parser_terminal_outcomes WHERE delivery_id=?`, string(normal.ID)).Scan(&outcomes); err != nil {
					t.Fatal(err)
				}
				if outcomes != 0 {
					t.Fatalf("non-terminal outcomes=%d", outcomes)
				}
				assertCounts(false)
				if err := manager.Flush(ctx); err != nil {
					t.Fatal(err)
				}
				checkpoint, found, err := database.LoadSourceCheckpoint(ctx, poison.Record.SourceID)
				if err != nil || !found || !reflect.DeepEqual(checkpoint, baseline) {
					t.Fatalf("non-terminal checkpoint=%+v want=%+v err=%v", checkpoint, baseline, err)
				}
				parsers.failure, parsers.cancel = 0, nil
			}
			completion, err = coordinator.Process(ctx, normal)
			if err != nil || completion.DeliveryID != normal.ID || completion.Sequence != 2 || completion.Position != normal.Record.Position {
				t.Fatalf("normal completion=%+v err=%v", completion, err)
			}
			if err := manager.Complete(ctx, completion); err != nil {
				t.Fatal(err)
			}
			receipt, found, err = database.FindProcessingReceipt(ctx, normal.ID)
			if err != nil || !found || receipt.Kind != core.ReceiptSuccess || receipt.Failure != nil || receipt.Position != normal.Record.Position {
				t.Fatalf("normal receipt=%+v found=%v err=%v", receipt, found, err)
			}
			checkpoint, found, err := database.LoadSourceCheckpoint(ctx, normal.Record.SourceID)
			if err != nil || !found || checkpoint.DeliverySequence != 2 || checkpoint.Position != normal.Record.Position {
				t.Fatalf("normal checkpoint=%+v found=%v err=%v", checkpoint, found, err)
			}
			assertCounts(true)
			for i := 0; i < 2; i++ {
				if _, err := coordinator.Process(ctx, normal); err != nil {
					t.Fatal(err)
				}
			}
			wantCalls := 2
			if test.failure != 0 {
				wantCalls = 3
			}
			if parsers.calls != wantCalls {
				t.Fatalf("parser calls=%d want=%d", parsers.calls, wantCalls)
			}
			assertCounts(true)
		})
	}
}

type poisonCheckpointParser struct {
	failure PlanFailureClass
	cancel  context.CancelFunc
	calls   int
}

func (p *poisonCheckpointParser) RunParser(ctx context.Context, _ ParserSnapshot, record core.RawRecord) (ParserExecution, error) {
	p.calls++
	if string(record.Content) == "poison" {
		return ParserExecution{}, &PlanFailure{Class: PlanFailureRecordPermanent, Code: "malformed_record", SanitizedError: "record rejected", Action: "terminal_reject"}
	}
	if p.failure == PlanFailureCancelled {
		// 在实际 Parser 调用期间取消本次 attempt，传播该 context 的取消身份。
		p.cancel()
		return ParserExecution{}, ctx.Err()
	}
	if p.failure != 0 {
		return ParserExecution{}, &PlanFailure{Class: p.failure, Cause: errors.New("parser attempt unavailable")}
	}
	return ParserExecution{Events: []core.EventFields{{EventType: "auth.login_failed"}}}, nil
}
