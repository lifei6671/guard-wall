package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/detection"
	"github.com/lifei6671/guard-wall/internal/source"
)

func TestSQLiteMultiParserCheckpointAtomicity(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure PlanFailureClass
	}{
		{"permanent_then_sibling_success", PlanFailureRecordPermanent},
		{"success_then_transient", PlanFailureTransient},
		{"success_then_blocked", PlanFailureBlocked},
		{"success_then_cancelled", PlanFailureCancelled},
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
			for _, statement := range []string{
				`INSERT INTO parsers(parser_id, enabled, created_at_us, updated_at_us) SELECT 'parser-2', enabled, created_at_us, updated_at_us FROM parsers WHERE parser_id='parser-1'`,
				`INSERT INTO parser_versions SELECT 'parser-2', version, definition, definition_sha256, created_at_us FROM parser_versions WHERE parser_id='parser-1'`,
				`UPDATE parsers SET active_version='v1' WHERE parser_id='parser-2'`,
			} {
				if _, err := connection.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			base := time.Unix(1_700_000_001, 0).UTC()
			first := sqliteDeliveryAt(t, 0, 10, base)
			second := sqliteDeliveryAt(t, 10, 20, base.Add(time.Second))
			parsers := &multiParserCheckpointRunner{}
			evaluator := &scriptedRuleEvaluator{match: RuleMatch{Applicable: true, GroupKey: "group-a", DistinctKey: "alice"}}
			ledger := detection.NewLedger()
			pipeline := NewPipeline(planNodeID, &mutablePlanCatalog{
				parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1", Priority: 10}, {ParserID: "parser-2", Version: "v1", Priority: 20}},
				rules:   []RuleSnapshot{{RuleID: "rule-1", Version: "v1"}},
			}, parsers, evaluator, ledger)
			pipeline.clock = func() time.Time { return base.Add(2 * time.Second) }
			coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), pipeline)
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
			// 比较全部业务行，防止只检查数量漏掉既有记录被改写。
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
			if len(baseline["parser_terminal_outcomes"]) != 2 || len(baseline["detection_contributions"]) != 2 || len(baseline["processing_receipts"]) != 1 {
				t.Fatalf("baseline rows=%v", baseline)
			}
			parsers.failure = test.failure
			if test.failure != PlanFailureRecordPermanent {
				attemptCtx, attemptCancel := context.WithCancel(ctx)
				parsers.cancel = attemptCancel
				completion, err = coordinator.Process(attemptCtx, second)
				attemptCancel()
				var failure *PlanFailure
				if !errors.As(err, &failure) || failure.Class != test.failure {
					t.Fatalf("failure=%v want class=%v", err, test.failure)
				}
				if test.failure == PlanFailureCancelled && !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation identity: %v", err)
				}
				if !reflect.DeepEqual(completion, core.DurableCompletion{}) {
					t.Fatalf("failed attempt completion=%+v", completion)
				}
				if _, found, err := database.FindProcessingReceipt(ctx, second.ID); err != nil || found {
					t.Fatalf("failed receipt found=%v err=%v", found, err)
				}
				if got := snapshot(); !reflect.DeepEqual(got, baseline) {
					t.Fatalf("failed attempt changed rows: before=%v after=%v", baseline, got)
				}
				if err := manager.Flush(ctx); err != nil {
					t.Fatal(err)
				}
				checkpoint, found, err := database.LoadSourceCheckpoint(ctx, first.Record.SourceID)
				if err != nil || !found || !reflect.DeepEqual(checkpoint, baselineCheckpoint) {
					t.Fatalf("failed checkpoint=%+v want=%+v err=%v", checkpoint, baselineCheckpoint, err)
				}
				if !reflect.DeepEqual(parsers.calls, []core.ParserID{"parser-1", "parser-2", "parser-1", "parser-2"}) || evaluator.matchCalls != 2 || evaluator.evaluateCalls != 2 {
					t.Fatalf("failed attempt calls=%v match=%d evaluate=%d", parsers.calls, evaluator.matchCalls, evaluator.evaluateCalls)
				}
				window, err := ledger.Snapshot(ctx, detection.WindowKey{RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a"})
				if err != nil || window != (detection.Snapshot{Count: 2, DistinctCount: 1}) {
					t.Fatalf("failed attempt window=%+v err=%v", window, err)
				}
				parsers.failure, parsers.cancel = 0, nil
			}
			completion, err = coordinator.Process(ctx, second)
			if err != nil || completion.DeliveryID != second.ID || completion.Sequence != 2 || completion.Position != second.Record.Position {
				t.Fatalf("completion=%+v err=%v", completion, err)
			}
			if err := manager.Complete(ctx, completion); err != nil {
				t.Fatal(err)
			}
			receipt, found, err := database.FindProcessingReceipt(ctx, second.ID)
			if err != nil || !found || receipt.Position != second.Record.Position {
				t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
			}
			permanent := test.failure == PlanFailureRecordPermanent
			wantEvents, wantAudits, wantCalls := 4, 0, 6
			if permanent {
				wantEvents, wantAudits, wantCalls = 3, 1, 4
				if receipt.Kind != core.ReceiptRecordPermanent || receipt.Failure == nil || receipt.Failure.Stage != "parser" || receipt.Failure.Code != "malformed_record" {
					t.Fatalf("mixed permanent receipt=%+v", receipt)
				}
			} else if receipt.Kind != core.ReceiptSuccess || receipt.Failure != nil {
				t.Fatalf("retry receipt=%+v", receipt)
			}
			for _, parserID := range []core.ParserID{"parser-1", "parser-2"} {
				var kind string
				var emitted int
				var code sql.NullString
				if err := connection.QueryRowContext(ctx, `SELECT kind, emitted_count, failure_code FROM parser_terminal_outcomes WHERE delivery_id=? AND parser_id=? AND parser_version='v1'`, second.ID, parserID).Scan(&kind, &emitted, &code); err != nil {
					t.Fatal(err)
				}
				if permanent && parserID == "parser-1" {
					if kind != "record_permanent" || emitted != 0 || !code.Valid || code.String != "malformed_record" {
						t.Fatalf("permanent outcome=%s/%d/%v", kind, emitted, code)
					}
					continue
				}
				if kind != "success" || emitted != 1 || code.Valid {
					t.Fatalf("%s success outcome=%s/%d/%v", parserID, kind, emitted, code)
				}
				eventID, err := core.SecurityEventID(planNodeID, second.ID, parserID, "v1", 0)
				if err != nil {
					t.Fatal(err)
				}
				var contributions int
				if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM detection_contributions WHERE event_id=? AND rule_id='rule-1' AND rule_version='v1'`, eventID).Scan(&contributions); err != nil || contributions != 1 {
					t.Fatalf("%s contribution=%d err=%v", parserID, contributions, err)
				}
			}
			after := snapshot()
			for table, want := range map[string]int{"parser_terminal_outcomes": 4, "detection_terminal_outcomes": wantEvents, "detection_contributions": wantEvents, "alerts": 0, "decisions": 0, "desired_ban_projections": 0, "audit_logs": wantAudits, "processing_receipts": 2} {
				if len(after[table]) != want {
					t.Fatalf("%s count=%d want=%d", table, len(after[table]), want)
				}
			}
			checkpoint, found, err := database.LoadSourceCheckpoint(ctx, first.Record.SourceID)
			if err != nil || !found || checkpoint.DeliverySequence != 2 || checkpoint.Position != second.Record.Position {
				t.Fatalf("final checkpoint=%+v found=%v err=%v", checkpoint, found, err)
			}
			for i := 0; i < 2; i++ {
				replay, err := coordinator.Process(ctx, second)
				if err != nil || !reflect.DeepEqual(replay, completion) {
					t.Fatalf("replay completion=%+v want=%+v err=%v", replay, completion, err)
				}
			}
			if len(parsers.calls) != wantCalls || evaluator.matchCalls != wantEvents || evaluator.evaluateCalls != wantEvents || !reflect.DeepEqual(snapshot(), after) {
				t.Fatalf("replay changed effects: parsers=%v match=%d evaluate=%d", parsers.calls, evaluator.matchCalls, evaluator.evaluateCalls)
			}
			window, err := ledger.Snapshot(ctx, detection.WindowKey{RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a"})
			if err != nil || window != (detection.Snapshot{Count: uint64(wantEvents), DistinctCount: 1}) {
				t.Fatalf("final window=%+v err=%v", window, err)
			}
		})
	}
}

type multiParserCheckpointRunner struct {
	failure PlanFailureClass
	cancel  context.CancelFunc
	calls   []core.ParserID
}

func (p *multiParserCheckpointRunner) RunParser(ctx context.Context, parser ParserSnapshot, _ core.RawRecord) (ParserExecution, error) {
	p.calls = append(p.calls, parser.ParserID)
	// 永久失败在第一个Parser发生，证明后续兄弟继续；系统错误在已有事件后发生。
	if p.failure == PlanFailureRecordPermanent && parser.ParserID == "parser-1" {
		return ParserExecution{}, &PlanFailure{Class: p.failure, Code: "malformed_record", SanitizedError: "record rejected", Action: "terminal_reject"}
	}
	if parser.ParserID == "parser-2" && p.failure != 0 && p.failure != PlanFailureRecordPermanent {
		if p.failure == PlanFailureCancelled {
			p.cancel()
			return ParserExecution{}, ctx.Err()
		}
		return ParserExecution{}, &PlanFailure{Class: p.failure, Cause: errors.New("parser unavailable")}
	}
	return ParserExecution{Events: []core.EventFields{{EventType: "auth.login_failed"}}}, nil
}
