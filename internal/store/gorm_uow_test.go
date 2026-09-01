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

func TestGORMPutParserOutcomeUsesCreateWithExplicitColumns(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)

	const callbackName = "guard_wall:test_capture_parser_outcome_create"
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

	if err := uow.PutParserOutcome(ctx, fixture.outcome); err != nil {
		t.Fatalf("PutParserOutcome(): %v", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("GORM Create callback calls = %d, want 1", callbackCalls)
	}
	if capturedTable != "parser_terminal_outcomes" {
		t.Fatalf("GORM Create table = %q, want parser_terminal_outcomes", capturedTable)
	}
	if _, ok := capturedModel.(*parserTerminalOutcomeRow); !ok {
		t.Fatalf("GORM Create model = %T, want *parserTerminalOutcomeRow", capturedModel)
	}
	wantColumns := []string{
		"delivery_id", "parser_id", "parser_version", "kind", "emitted_count",
		"failure_code", "completed_at_us",
	}
	if !reflect.DeepEqual(capturedColumns, wantColumns) {
		t.Fatalf("GORM Create columns = %v, want %v", capturedColumns, wantColumns)
	}
	wantSQL := "INSERT INTO `parser_terminal_outcomes` (`delivery_id`,`parser_id`,`parser_version`,`kind`,`emitted_count`,`failure_code`,`completed_at_us`) VALUES (?,?,?,?,?,?,?)"
	if normalizedSQL := strings.Join(strings.Fields(capturedSQL), " "); normalizedSQL != wantSQL {
		t.Fatalf("GORM Create SQL = %q, want %q", normalizedSQL, wantSQL)
	}
	wantVars := []any{
		string(fixture.outcome.DeliveryID), string(fixture.outcome.ParserID),
		string(fixture.outcome.ParserVersion), "success", int64(1), (*string)(nil),
		fixture.outcome.CompletedAt.UTC().UnixMicro(),
	}
	if !reflect.DeepEqual(capturedVars, wantVars) {
		t.Fatalf("GORM Create vars = %#v, want %#v", capturedVars, wantVars)
	}

	if err := uow.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
}

func TestGORMUnitOfWorkTransactionPoolIsCapabilityIsolated(t *testing.T) {
	database := openTestStore(t)
	rootConfigPool := database.orm.Config.ConnPool
	rootStatementPool := database.orm.Statement.ConnPool
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)

	configPool, ok := uow.transactionORM.Config.ConnPool.(*nonFinalizingTxConnPool)
	if !ok {
		t.Fatalf(
			"transaction GORM config ConnPool = %T, want *nonFinalizingTxConnPool",
			uow.transactionORM.Config.ConnPool,
		)
	}
	statementPool, ok := uow.transactionORM.Statement.ConnPool.(*nonFinalizingTxConnPool)
	if !ok {
		t.Fatalf(
			"transaction GORM statement ConnPool = %T, want *nonFinalizingTxConnPool",
			uow.transactionORM.Statement.ConnPool,
		)
	}
	if configPool != statementPool {
		t.Fatal("transaction GORM config and statement use different wrappers")
	}
	if configPool.tx != uow.tx {
		t.Fatal("transaction GORM wrapper does not delegate to UnitOfWork transaction")
	}

	if _, ok := any(configPool).(gorm.TxBeginner); ok {
		t.Fatal("transaction wrapper must not implement gorm.TxBeginner")
	}
	if _, ok := any(configPool).(gorm.ConnPoolBeginner); ok {
		t.Fatal("transaction wrapper must not implement gorm.ConnPoolBeginner")
	}
	if _, ok := any(configPool).(gorm.TxCommitter); ok {
		t.Fatal("transaction wrapper must not implement gorm.TxCommitter")
	}
	if _, ok := any(configPool).(gorm.Tx); ok {
		t.Fatal("transaction wrapper must not implement gorm.Tx")
	}
	if _, ok := any(configPool).(gorm.GetDBConnector); ok {
		t.Fatal("transaction wrapper must not implement gorm.GetDBConnector")
	}
	if _, ok := any(configPool).(interface{ Close() error }); ok {
		t.Fatal("transaction wrapper must not expose Close")
	}
	if _, err := uow.transactionORM.DB(); !errors.Is(err, gorm.ErrInvalidDB) {
		t.Fatalf("transaction GORM DB() error = %v, want gorm.ErrInvalidDB", err)
	}

	if database.orm.Config.ConnPool != rootConfigPool {
		t.Fatal("creating UnitOfWork replaced Store root config ConnPool")
	}
	if database.orm.Statement.ConnPool != rootStatementPool {
		t.Fatal("creating UnitOfWork replaced Store root statement ConnPool")
	}
}

func TestGORMPutParserOutcomePersistsExactColumns(t *testing.T) {
	tests := []struct {
		name            string
		kind            core.ParserOutcomeKind
		emittedCount    uint32
		failureCode     string
		wantKind        string
		wantFailureCode sql.NullString
	}{
		{
			name: "success", kind: core.ParserOutcomeSuccess, emittedCount: 1,
			wantKind: "success",
		},
		{
			name: "no match", kind: core.ParserOutcomeNoMatch,
			wantKind: "no_match",
		},
		{
			name: "record permanent", kind: core.ParserOutcomeRecordPermanent,
			failureCode: "parser.invalid_record", wantKind: "record_permanent",
			wantFailureCode: sql.NullString{String: "parser.invalid_record", Valid: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			fixture.outcome.Kind = test.kind
			fixture.outcome.EmittedCount = test.emittedCount
			fixture.outcome.FailureCode = test.failureCode
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			if err := uow.PutParserOutcome(ctx, fixture.outcome); err != nil {
				t.Fatalf("PutParserOutcome(): %v", err)
			}
			if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
				t.Fatalf("PutReceipt(): %v", err)
			}
			if err := uow.Commit(); err != nil {
				t.Fatalf("Commit(): %v", err)
			}

			var (
				deliveryID    string
				parserID      string
				parserVersion string
				kind          string
				emittedCount  int64
				failureCode   sql.NullString
				completedAtUS int64
			)
			err = database.db.QueryRowContext(ctx, `
				SELECT delivery_id, parser_id, parser_version, kind, emitted_count,
					failure_code, completed_at_us
				FROM parser_terminal_outcomes
				WHERE delivery_id = ? AND parser_id = ? AND parser_version = ?`,
				string(fixture.outcome.DeliveryID), string(fixture.outcome.ParserID),
				string(fixture.outcome.ParserVersion),
			).Scan(
				&deliveryID, &parserID, &parserVersion, &kind, &emittedCount,
				&failureCode, &completedAtUS,
			)
			if err != nil {
				t.Fatalf("read parser outcome: %v", err)
			}
			if deliveryID != string(fixture.outcome.DeliveryID) ||
				parserID != string(fixture.outcome.ParserID) ||
				parserVersion != string(fixture.outcome.ParserVersion) ||
				kind != test.wantKind || emittedCount != int64(test.emittedCount) ||
				failureCode != test.wantFailureCode ||
				completedAtUS != fixture.outcome.CompletedAt.UTC().UnixMicro() {
				t.Fatalf(
					"parser outcome = delivery %q parser %q version %q kind %q emitted %d failure %+v completed %d",
					deliveryID, parserID, parserVersion, kind, emittedCount,
					failureCode, completedAtUS,
				)
			}
		})
	}
}

func TestGORMPutParserOutcomeUsesUnitOfWorkTransaction(t *testing.T) {
	t.Run("rollback discards row", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if err := uow.PutParserOutcome(ctx, fixture.outcome); err != nil {
			t.Fatalf("PutParserOutcome(): %v", err)
		}
		var transactionCount int
		if err := uow.tx.QueryRowContext(ctx,
			"SELECT count(*) FROM parser_terminal_outcomes WHERE delivery_id = ?",
			string(fixture.outcome.DeliveryID),
		).Scan(&transactionCount); err != nil {
			t.Fatalf("read inside UnitOfWork transaction: %v", err)
		}
		if transactionCount != 1 {
			t.Fatalf("row count inside UnitOfWork transaction = %d, want 1", transactionCount)
		}
		if err := uow.Rollback(); err != nil {
			t.Fatalf("Rollback(): %v", err)
		}
		assertParserOutcomeCount(t, database, fixture.outcome.DeliveryID, 0)
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
		if err := uow.PutParserOutcome(ctx, fixture.outcome); err != nil {
			t.Fatalf("PutParserOutcome(): %v", err)
		}
		if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
			t.Fatalf("PutReceipt(): %v", err)
		}
		if err := uow.Commit(); err != nil {
			t.Fatalf("Commit(): %v", err)
		}
		assertParserOutcomeCount(t, database, fixture.outcome.DeliveryID, 1)
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
		if err := uow.PutParserOutcome(ctx, fixture.outcome); err != nil {
			t.Fatalf("PutParserOutcome(): %v", err)
		}
		if err := uow.Commit(); err == nil {
			t.Fatal("Commit() without processing receipt = nil")
		}
		assertParserOutcomeCount(t, database, fixture.outcome.DeliveryID, 0)
	})
}

func TestGORMPutParserOutcomeDuplicateIsSticky(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()

	committed, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(first): %v", err)
	}
	cleanupOpenUnitOfWork(t, committed)
	if err := committed.PutParserOutcome(ctx, fixture.outcome); err != nil {
		t.Fatalf("PutParserOutcome(first): %v", err)
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
	const callbackName = "guard_wall:test_count_duplicate_parser_outcome_create"
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

	firstErr := duplicate.PutParserOutcome(ctx, fixture.outcome)
	if firstErr == nil {
		t.Fatal("duplicate PutParserOutcome() error = nil")
	}
	secondErr := duplicate.PutParserOutcome(ctx, fixture.outcome)
	if secondErr != firstErr {
		t.Fatalf("sticky PutParserOutcome() error = %v, want original %v", secondErr, firstErr)
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
	assertParserOutcomeCount(t, database, fixture.outcome.DeliveryID, 1)
	assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 1)
}

func TestGORMPutParserOutcomeHonorsCanceledContext(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	uow, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	putErr := uow.PutParserOutcome(ctx, fixture.outcome)
	if !errors.Is(putErr, context.Canceled) {
		t.Fatalf("PutParserOutcome() error = %v, want context.Canceled", putErr)
	}
	if receiptErr := uow.PutReceipt(context.Background(), fixture.receipt); receiptErr != putErr {
		t.Fatalf("PutReceipt() after canceled GORM write = %v, want original %v", receiptErr, putErr)
	}
	if commitErr := uow.Commit(); commitErr != putErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, putErr)
	}
	assertParserOutcomeCount(t, database, fixture.outcome.DeliveryID, 0)
	assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 0)
}

func assertParserOutcomeCount(
	t *testing.T,
	database *Store,
	deliveryID core.DeliveryID,
	want int,
) {
	t.Helper()
	var count int
	if err := database.db.QueryRowContext(
		context.Background(),
		"SELECT count(*) FROM parser_terminal_outcomes WHERE delivery_id = ?",
		string(deliveryID),
	).Scan(&count); err != nil {
		t.Fatalf("count parser outcomes: %v", err)
	}
	if count != want {
		t.Fatalf("parser outcome count = %d, want %d", count, want)
	}
}

func assertProcessingReceiptCount(
	t *testing.T,
	database *Store,
	deliveryID core.DeliveryID,
	want int,
) {
	t.Helper()
	var count int
	if err := database.db.QueryRowContext(
		context.Background(),
		"SELECT count(*) FROM processing_receipts WHERE delivery_id = ?",
		string(deliveryID),
	).Scan(&count); err != nil {
		t.Fatalf("count processing receipts: %v", err)
	}
	if count != want {
		t.Fatalf("processing receipt count = %d, want %d", count, want)
	}
}

func cleanupOpenUnitOfWork(t *testing.T, uow *UnitOfWork) {
	t.Helper()
	t.Cleanup(func() {
		if uow == nil || uow.done {
			return
		}
		if err := uow.Rollback(); err != nil {
			t.Errorf("cleanup UnitOfWork rollback: %v", err)
		}
	})
}
