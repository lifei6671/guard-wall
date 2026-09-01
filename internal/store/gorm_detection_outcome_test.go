package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

func TestGORMPutDetectionOutcomeUsesCreateWithExplicitColumns(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)

	const callbackName = "guard_wall:test_capture_detection_outcome_create"
	var (
		callbackCalls   int
		capturedTable   string
		capturedModel   any
		capturedColumns []string
		capturedSQL     string
		capturedVars    []any
	)
	if err := uow.transactionORM.Callback().Create().After("gorm:create").Register(
		callbackName,
		func(tx *gorm.DB) {
			callbackCalls++
			capturedTable = tx.Statement.Table
			capturedModel = tx.Statement.Model
			if tx.Statement.Schema != nil {
				capturedColumns = append([]string(nil), tx.Statement.Schema.DBNames...)
			}
			capturedSQL = tx.Statement.SQL.String()
			capturedVars = append([]any(nil), tx.Statement.Vars...)
		},
	); err != nil {
		t.Fatalf("register GORM create callback: %v", err)
	}
	t.Cleanup(func() {
		_ = uow.transactionORM.Callback().Create().Remove(callbackName)
	})

	if err := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
		t.Fatalf("PutDetectionOutcome(): %v", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("GORM Create callback calls = %d, want 1", callbackCalls)
	}
	if capturedTable != "detection_terminal_outcomes" {
		t.Fatalf("GORM Create table = %q, want detection_terminal_outcomes", capturedTable)
	}
	if _, ok := capturedModel.(*detectionTerminalOutcomeRow); !ok {
		t.Fatalf("GORM Create model = %T, want *detectionTerminalOutcomeRow", capturedModel)
	}
	wantColumns := []string{
		"delivery_id", "event_id", "rule_id", "rule_version", "kind",
		"failure_code", "completed_at_us",
	}
	if !reflect.DeepEqual(capturedColumns, wantColumns) {
		t.Fatalf("GORM Create columns = %v, want %v", capturedColumns, wantColumns)
	}
	wantSQL := "INSERT INTO `detection_terminal_outcomes` (`delivery_id`,`event_id`,`rule_id`,`rule_version`,`kind`,`failure_code`,`completed_at_us`) VALUES (?,?,?,?,?,?,?)"
	if normalizedSQL := strings.Join(strings.Fields(capturedSQL), " "); normalizedSQL != wantSQL {
		t.Fatalf("GORM Create SQL = %q, want %q", normalizedSQL, wantSQL)
	}
	wantVars := []any{
		string(fixture.detectionOutcome.DeliveryID), string(fixture.detectionOutcome.EventID),
		string(fixture.detectionOutcome.RuleID), string(fixture.detectionOutcome.RuleVersion),
		"success", (*string)(nil), fixture.detectionOutcome.CompletedAt.UTC().UnixMicro(),
	}
	if !reflect.DeepEqual(capturedVars, wantVars) {
		t.Fatalf("GORM Create vars = %#v, want %#v", capturedVars, wantVars)
	}

	if err := uow.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
}

func TestGORMPutDetectionOutcomePersistsExactColumns(t *testing.T) {
	tests := []struct {
		name            string
		kind            core.DetectionOutcomeKind
		failureCode     string
		wantKind        string
		wantFailureCode sql.NullString
	}{
		{
			name: "success", kind: core.DetectionOutcomeSuccess,
			wantKind: "success",
		},
		{
			name: "record permanent", kind: core.DetectionOutcomeRecordPermanent,
			failureCode: "rule.invalid_record", wantKind: "record_permanent",
			wantFailureCode: sql.NullString{String: "rule.invalid_record", Valid: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			fixture.detectionOutcome.Kind = test.kind
			fixture.detectionOutcome.FailureCode = test.failureCode
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			if err := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
				t.Fatalf("PutDetectionOutcome(): %v", err)
			}
			if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
				t.Fatalf("PutReceipt(): %v", err)
			}
			if err := uow.Commit(); err != nil {
				t.Fatalf("Commit(): %v", err)
			}

			var (
				deliveryID  string
				eventID     string
				ruleID      string
				ruleVersion string
				kind        string
				failureCode sql.NullString
				completedAt int64
			)
			err = database.db.QueryRowContext(ctx, `
				SELECT delivery_id, event_id, rule_id, rule_version, kind,
					failure_code, completed_at_us
				FROM detection_terminal_outcomes
				WHERE event_id = ? AND rule_id = ? AND rule_version = ?`,
				string(fixture.detectionOutcome.EventID), string(fixture.detectionOutcome.RuleID),
				string(fixture.detectionOutcome.RuleVersion),
			).Scan(
				&deliveryID, &eventID, &ruleID, &ruleVersion, &kind,
				&failureCode, &completedAt,
			)
			if err != nil {
				t.Fatalf("read detection outcome: %v", err)
			}
			if deliveryID != string(fixture.detectionOutcome.DeliveryID) ||
				eventID != string(fixture.detectionOutcome.EventID) ||
				ruleID != string(fixture.detectionOutcome.RuleID) ||
				ruleVersion != string(fixture.detectionOutcome.RuleVersion) ||
				kind != test.wantKind || failureCode != test.wantFailureCode ||
				completedAt != fixture.detectionOutcome.CompletedAt.UTC().UnixMicro() {
				t.Fatalf(
					"detection outcome = delivery %q event %q rule %q version %q kind %q failure %+v completed %d",
					deliveryID, eventID, ruleID, ruleVersion, kind, failureCode, completedAt,
				)
			}
		})
	}
}

func TestGORMPutDetectionOutcomeUsesUnitOfWorkTransaction(t *testing.T) {
	t.Run("rollback discards row", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if err := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
			t.Fatalf("PutDetectionOutcome(): %v", err)
		}
		var transactionCount int
		if err := uow.tx.QueryRowContext(ctx,
			"SELECT count(*) FROM detection_terminal_outcomes WHERE event_id = ?",
			string(fixture.detectionOutcome.EventID),
		).Scan(&transactionCount); err != nil {
			t.Fatalf("read inside UnitOfWork transaction: %v", err)
		}
		if transactionCount != 1 {
			t.Fatalf("row count inside UnitOfWork transaction = %d, want 1", transactionCount)
		}
		if err := uow.Rollback(); err != nil {
			t.Fatalf("Rollback(): %v", err)
		}
		assertDetectionOutcomeCount(t, database, fixture.detectionOutcome.EventID, 0)
	})

	t.Run("commit publishes row", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if err := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
			t.Fatalf("PutDetectionOutcome(): %v", err)
		}
		if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
			t.Fatalf("PutReceipt(): %v", err)
		}
		if err := uow.Commit(); err != nil {
			t.Fatalf("Commit(): %v", err)
		}
		assertDetectionOutcomeCount(t, database, fixture.detectionOutcome.EventID, 1)
	})

	t.Run("deferred receipt foreign key fails commit", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if err := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
			t.Fatalf("PutDetectionOutcome(): %v", err)
		}
		if err := uow.Commit(); err == nil {
			t.Fatal("Commit() without processing receipt = nil")
		}
		assertDetectionOutcomeCount(t, database, fixture.detectionOutcome.EventID, 0)
	})
}

func TestGORMPutDetectionOutcomeDuplicateIsSticky(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()

	committed, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(first): %v", err)
	}
	cleanupOpenUnitOfWork(t, committed)
	if err := committed.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
		t.Fatalf("PutDetectionOutcome(first): %v", err)
	}
	if err := committed.PutReceipt(ctx, fixture.receipt); err != nil {
		t.Fatalf("PutReceipt(first): %v", err)
	}
	if err := committed.Commit(); err != nil {
		t.Fatalf("Commit(first): %v", err)
	}

	duplicate, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(duplicate): %v", err)
	}
	cleanupOpenUnitOfWork(t, duplicate)
	const callbackName = "guard_wall:test_count_duplicate_detection_outcome_create"
	callbackCalls := 0
	if err := duplicate.transactionORM.Callback().Create().Before("gorm:create").Register(
		callbackName,
		func(*gorm.DB) { callbackCalls++ },
	); err != nil {
		t.Fatalf("register GORM create callback: %v", err)
	}
	t.Cleanup(func() {
		_ = duplicate.transactionORM.Callback().Create().Remove(callbackName)
	})

	firstErr := duplicate.PutDetectionOutcome(ctx, fixture.detectionOutcome)
	if firstErr == nil {
		t.Fatal("duplicate PutDetectionOutcome() error = nil")
	}
	secondErr := duplicate.PutDetectionOutcome(ctx, fixture.detectionOutcome)
	if secondErr != firstErr {
		t.Fatalf("sticky PutDetectionOutcome() error = %v, want original %v", secondErr, firstErr)
	}
	if receiptErr := duplicate.PutReceipt(ctx, fixture.receipt); receiptErr != firstErr {
		t.Fatalf("PutReceipt() after GORM failure = %v, want original %v", receiptErr, firstErr)
	}
	if callbackCalls != 1 {
		t.Fatalf("GORM Create callback calls = %d, want 1 after sticky failure", callbackCalls)
	}
	if commitErr := duplicate.Commit(); commitErr != firstErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
	}
	assertDetectionOutcomeCount(t, database, fixture.detectionOutcome.EventID, 1)
	assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 1)
}

func TestGORMPutDetectionOutcomeHonorsCanceledContext(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	uow, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	putErr := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome)
	if !errors.Is(putErr, context.Canceled) {
		t.Fatalf("PutDetectionOutcome() error = %v, want context.Canceled", putErr)
	}
	if receiptErr := uow.PutReceipt(context.Background(), fixture.receipt); receiptErr != putErr {
		t.Fatalf("PutReceipt() after canceled GORM write = %v, want original %v", receiptErr, putErr)
	}
	if commitErr := uow.Commit(); commitErr != putErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, putErr)
	}
	assertDetectionOutcomeCount(t, database, fixture.detectionOutcome.EventID, 0)
	assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 0)
}

func assertDetectionOutcomeCount(
	t *testing.T,
	database *Store,
	eventID core.EventID,
	want int,
) {
	t.Helper()
	var count int
	if err := database.db.QueryRowContext(
		context.Background(),
		"SELECT count(*) FROM detection_terminal_outcomes WHERE event_id = ?",
		string(eventID),
	).Scan(&count); err != nil {
		t.Fatalf("count detection outcomes: %v", err)
	}
	if count != want {
		t.Fatalf("detection outcome count = %d, want %d", count, want)
	}
}
