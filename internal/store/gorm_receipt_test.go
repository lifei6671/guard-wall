package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

const (
	gormReceiptSourceID   core.SourceID = "gorm-receipt-source"
	gormReceiptGeneration               = "1234567890abcdef1234567890abcdef"
)

func TestGORMPutReceiptUsesCreateWithExplicitColumnsAndPersistsExactValues(t *testing.T) {
	tests := []struct {
		name         string
		positionKind string
		receiptKind  core.ReceiptKind
		wantKind     string
	}{
		{name: "file success", positionKind: "file", receiptKind: core.ReceiptSuccess, wantKind: "success"},
		{name: "file record permanent", positionKind: "file", receiptKind: core.ReceiptRecordPermanent, wantKind: "record_permanent"},
		{name: "journald success", positionKind: "journald", receiptKind: core.ReceiptSuccess, wantKind: "success"},
		{name: "journald record permanent", positionKind: "journald", receiptKind: core.ReceiptRecordPermanent, wantKind: "record_permanent"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			receipt := newGORMReceipt(t, database, test.positionKind, test.receiptKind)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)

			callbackName := "guard_wall:test_capture_receipt_create_" + strings.ReplaceAll(test.name, " ", "_")
			var (
				callbackCalls   int
				capturedTable   string
				capturedModel   any
				capturedColumns []string
				capturedSQL     string
				capturedVars    []any
				capturedPool    gorm.ConnPool
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
					capturedPool = tx.Statement.ConnPool
				},
			); err != nil {
				t.Fatalf("register GORM create callback: %v", err)
			}
			t.Cleanup(func() {
				_ = uow.transactionORM.Callback().Create().Remove(callbackName)
			})

			if err := uow.PutReceipt(ctx, receipt); err != nil {
				t.Fatalf("PutReceipt(): %v", err)
			}
			if callbackCalls != 1 {
				t.Fatalf("GORM Create callback calls = %d, want 1", callbackCalls)
			}
			if capturedTable != "processing_receipts" {
				t.Fatalf("GORM Create table = %q, want processing_receipts", capturedTable)
			}
			if _, ok := capturedModel.(*processingReceiptRow); !ok {
				t.Fatalf("GORM Create model = %T, want *processingReceiptRow", capturedModel)
			}
			wantColumns := []string{
				"delivery_id", "source_id", "position_kind", "generation", "device_id", "inode",
				"start_offset", "end_offset", "journald_cursor", "kind", "failure_stage",
				"failure_code", "sanitized_error", "terminal_action", "failure_occurred_at_us",
				"committed_at_us",
			}
			if !reflect.DeepEqual(capturedColumns, wantColumns) {
				t.Fatalf("GORM Create columns = %v, want %v", capturedColumns, wantColumns)
			}
			wantSQL := "INSERT INTO `processing_receipts` (`delivery_id`,`source_id`,`position_kind`,`generation`,`device_id`,`inode`,`start_offset`,`end_offset`,`journald_cursor`,`kind`,`failure_stage`,`failure_code`,`sanitized_error`,`terminal_action`,`failure_occurred_at_us`,`committed_at_us`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
			if normalizedSQL := strings.Join(strings.Fields(capturedSQL), " "); normalizedSQL != wantSQL {
				t.Fatalf("GORM Create SQL = %q, want %q", normalizedSQL, wantSQL)
			}
			wantVars := expectedGORMReceiptVars(receipt, test.wantKind)
			if !reflect.DeepEqual(capturedVars, wantVars) {
				t.Fatalf("GORM Create vars = %#v, want %#v", capturedVars, wantVars)
			}
			pool, ok := capturedPool.(*nonFinalizingTxConnPool)
			if !ok {
				t.Fatalf("GORM Create ConnPool = %T, want *nonFinalizingTxConnPool", capturedPool)
			}
			if pool.tx != uow.tx {
				t.Fatal("GORM Create escaped the UnitOfWork raw transaction")
			}
			if err := uow.Commit(); err != nil {
				t.Fatalf("Commit(): %v", err)
			}

			assertGORMReceiptColumns(t, database, receipt, test.wantKind)
		})
	}
}

func TestGORMPutReceiptUsesUnitOfWorkTransaction(t *testing.T) {
	t.Run("rollback discards row", func(t *testing.T) {
		database := openTestStore(t)
		receipt := newGORMReceipt(t, database, "file", core.ReceiptSuccess)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if err := uow.PutReceipt(ctx, receipt); err != nil {
			t.Fatalf("PutReceipt(): %v", err)
		}
		var transactionCount int
		if err := uow.tx.QueryRowContext(
			ctx, "SELECT count(*) FROM processing_receipts WHERE delivery_id = ?", string(receipt.DeliveryID),
		).Scan(&transactionCount); err != nil {
			t.Fatalf("read receipt inside UnitOfWork transaction: %v", err)
		}
		if transactionCount != 1 {
			t.Fatalf("receipt count inside UnitOfWork transaction = %d, want 1", transactionCount)
		}
		if err := uow.Rollback(); err != nil {
			t.Fatalf("Rollback(): %v", err)
		}
		assertProcessingReceiptCount(t, database, receipt.DeliveryID, 0)
	})

	t.Run("commit publishes row", func(t *testing.T) {
		database := openTestStore(t)
		receipt := newGORMReceipt(t, database, "journald", core.ReceiptSuccess)
		commitGORMReceipt(t, database, receipt)
		assertProcessingReceiptCount(t, database, receipt.DeliveryID, 1)
	})
}

func TestGORMPutReceiptRejectsInvalidClosedUnionBeforeCreate(t *testing.T) {
	otherDeliveryID, err := core.JournaldDeliveryID(gormReceiptSourceID, "s=other-cursor")
	if err != nil {
		t.Fatalf("JournaldDeliveryID(other): %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*core.ProcessingReceipt)
	}{
		{
			name: "delivery id does not bind source and position",
			mutate: func(receipt *core.ProcessingReceipt) {
				receipt.DeliveryID = otherDeliveryID
			},
		},
		{name: "position union is empty", mutate: func(receipt *core.ProcessingReceipt) {
			receipt.Position = core.SourcePosition{}
		}},
		{name: "success contains failure", mutate: func(receipt *core.ProcessingReceipt) {
			receipt.Failure = newGORMReceiptFailure(receipt.Committed.Add(-time.Second))
		}},
		{name: "permanent receipt has no failure", mutate: func(receipt *core.ProcessingReceipt) {
			receipt.Kind = core.ReceiptRecordPermanent
			receipt.Failure = nil
		}},
		{name: "permanent failure occurred time is zero", mutate: func(receipt *core.ProcessingReceipt) {
			receipt.Kind = core.ReceiptRecordPermanent
			receipt.Failure = newGORMReceiptFailure(time.Time{})
		}},
		{name: "committed time is zero", mutate: func(receipt *core.ProcessingReceipt) {
			receipt.Committed = time.Time{}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			receipt := newGORMReceipt(t, database, "journald", core.ReceiptSuccess)
			test.mutate(&receipt)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			callbackName := "guard_wall:test_reject_receipt_before_create_" + strings.ReplaceAll(test.name, " ", "_")
			callbackCalls := 0
			if err := uow.transactionORM.Callback().Create().Before("gorm:create").Register(
				callbackName,
				func(*gorm.DB) { callbackCalls++ },
			); err != nil {
				t.Fatalf("register GORM create callback: %v", err)
			}
			t.Cleanup(func() {
				_ = uow.transactionORM.Callback().Create().Remove(callbackName)
			})

			firstErr := uow.PutReceipt(ctx, receipt)
			if firstErr == nil {
				t.Fatal("PutReceipt() invalid closed union error = nil")
			}
			if secondErr := uow.PutReceipt(ctx, receipt); secondErr != firstErr {
				t.Fatalf("sticky PutReceipt() error = %v, want original %v", secondErr, firstErr)
			}
			if callbackCalls != 0 {
				t.Fatalf("GORM Create callback calls = %d, want 0", callbackCalls)
			}
			if commitErr := uow.Commit(); commitErr != firstErr {
				t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
			}
			assertProcessingReceiptCount(t, database, receipt.DeliveryID, 0)
		})
	}
}

func TestGORMPutReceiptEnforcesTerminalTimes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*core.ProcessingReceipt)
	}{
		{
			name: "committed time must be positive",
			mutate: func(receipt *core.ProcessingReceipt) {
				receipt.Committed = time.Unix(-1, 0)
			},
		},
		{
			name: "failure cannot occur after commit",
			mutate: func(receipt *core.ProcessingReceipt) {
				receipt.Kind = core.ReceiptRecordPermanent
				receipt.Failure = newGORMReceiptFailure(receipt.Committed.Add(time.Microsecond))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			receipt := newGORMReceipt(t, database, "journald", core.ReceiptSuccess)
			test.mutate(&receipt)
			assertGORMReceiptCreateFailureIsSticky(t, database, receipt)
		})
	}
}

func TestGORMPutReceiptFilePositionSQLiteRange(t *testing.T) {
	t.Run("maximum SQLite integers persist", func(t *testing.T) {
		database := openTestStore(t)
		prepareGORMReceiptSource(t, database, SourceKindFile, true)
		file := core.FilePosition{
			Generation: gormReceiptGeneration,
			DeviceID:   math.MaxInt64, Inode: math.MaxInt64,
			StartOffset: math.MaxInt64, EndOffset: math.MaxInt64,
		}
		receipt := newGORMFileReceiptFromPosition(t, file)
		commitGORMReceipt(t, database, receipt)
		assertGORMReceiptColumns(t, database, receipt, "success")
	})

	limit := uint64(math.MaxInt64) + 1
	tests := []struct {
		name   string
		mutate func(*core.FilePosition)
	}{
		{name: "device id overflow", mutate: func(position *core.FilePosition) {
			position.DeviceID = limit
		}},
		{name: "inode overflow", mutate: func(position *core.FilePosition) {
			position.Inode = limit
		}},
		{name: "start offset overflow", mutate: func(position *core.FilePosition) {
			position.StartOffset = limit
			position.EndOffset = limit
		}},
		{name: "end offset overflow", mutate: func(position *core.FilePosition) {
			position.EndOffset = limit
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			prepareGORMReceiptSource(t, database, SourceKindFile, true)
			file := core.FilePosition{
				Generation: gormReceiptGeneration, DeviceID: 1, Inode: 2,
				StartOffset: 0, EndOffset: 10,
			}
			test.mutate(&file)
			receipt := newGORMFileReceiptFromPosition(t, file)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			callbackName := "guard_wall:test_reject_receipt_overflow_" + strings.ReplaceAll(test.name, " ", "_")
			callbackCalls := 0
			if err := uow.transactionORM.Callback().Create().Before("gorm:create").Register(
				callbackName,
				func(*gorm.DB) { callbackCalls++ },
			); err != nil {
				t.Fatalf("register GORM create callback: %v", err)
			}
			t.Cleanup(func() {
				_ = uow.transactionORM.Callback().Create().Remove(callbackName)
			})

			firstErr := uow.PutReceipt(ctx, receipt)
			if firstErr == nil {
				t.Fatal("PutReceipt() with file position above SQLite INTEGER error = nil")
			}
			if !strings.Contains(firstErr.Error(), "file position exceeds SQLite INTEGER range") {
				t.Fatalf("PutReceipt() overflow error = %v, want SQLite INTEGER range failure", firstErr)
			}
			if secondErr := uow.PutReceipt(ctx, receipt); secondErr != firstErr {
				t.Fatalf("sticky PutReceipt() overflow error = %v, want original %v", secondErr, firstErr)
			}
			if callbackCalls != 0 {
				t.Fatalf("GORM Create callback calls = %d, want 0", callbackCalls)
			}
			if commitErr := uow.Commit(); commitErr != firstErr {
				t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
			}
			assertProcessingReceiptCount(t, database, receipt.DeliveryID, 0)
		})
	}
}

func TestGORMPutReceiptConstraintFailureIsSticky(t *testing.T) {
	t.Run("delivery primary key", func(t *testing.T) {
		database := openTestStore(t)
		receipt := newGORMReceipt(t, database, "file", core.ReceiptSuccess)
		commitGORMReceipt(t, database, receipt)
		assertGORMReceiptCreateFailureIsSticky(t, database, receipt)
		assertProcessingReceiptCount(t, database, receipt.DeliveryID, 1)
	})

	t.Run("source foreign key", func(t *testing.T) {
		database := openTestStore(t)
		ctx := context.Background()
		seedNodeAndRule(t, ctx, database)
		position, err := core.NewJournaldPosition("s=missing-source")
		if err != nil {
			t.Fatalf("NewJournaldPosition(): %v", err)
		}
		deliveryID, err := core.JournaldDeliveryID("missing-source", "s=missing-source")
		if err != nil {
			t.Fatalf("JournaldDeliveryID(): %v", err)
		}
		receipt := core.ProcessingReceipt{
			DeliveryID: deliveryID, SourceID: "missing-source", Position: position,
			Kind: core.ReceiptSuccess, Committed: time.Unix(500, 0).UTC(),
		}
		assertGORMReceiptCreateFailureIsSticky(t, database, receipt)
		assertProcessingReceiptCount(t, database, receipt.DeliveryID, 0)
	})

	t.Run("source generation foreign key", func(t *testing.T) {
		database := openTestStore(t)
		prepareGORMReceiptSource(t, database, SourceKindFile, false)
		file := core.FilePosition{
			Generation: gormReceiptGeneration, DeviceID: 1, Inode: 2,
			StartOffset: 0, EndOffset: 10,
		}
		receipt := newGORMFileReceiptFromPosition(t, file)
		assertGORMReceiptCreateFailureIsSticky(t, database, receipt)
		assertProcessingReceiptCount(t, database, receipt.DeliveryID, 0)
	})
}

func TestGORMPutReceiptSatisfiesDeferredOutcomeReferences(t *testing.T) {
	t.Run("commit without receipt rejects parser and detection outcomes", func(t *testing.T) {
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
		if err := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
			t.Fatalf("PutDetectionOutcome(): %v", err)
		}
		if err := uow.Commit(); err == nil {
			t.Fatal("Commit() without processing receipt = nil")
		}
		assertParserOutcomeCount(t, database, fixture.outcome.DeliveryID, 0)
		assertDetectionOutcomeCount(t, database, fixture.detectionOutcome.EventID, 0)
	})

	t.Run("receipt written last permits commit", func(t *testing.T) {
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
		if err := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
			t.Fatalf("PutDetectionOutcome(): %v", err)
		}
		if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
			t.Fatalf("PutReceipt(): %v", err)
		}
		if err := uow.Commit(); err != nil {
			t.Fatalf("Commit(): %v", err)
		}
		assertParserOutcomeCount(t, database, fixture.outcome.DeliveryID, 1)
		assertDetectionOutcomeCount(t, database, fixture.detectionOutcome.EventID, 1)
		assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 1)
	})
}

func TestGORMPutReceiptHonorsCanceledContext(t *testing.T) {
	database := openTestStore(t)
	receipt := newGORMReceipt(t, database, "journald", core.ReceiptSuccess)
	uow, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	putErr := uow.PutReceipt(ctx, receipt)
	if !errors.Is(putErr, context.Canceled) {
		t.Fatalf("PutReceipt() error = %v, want context.Canceled", putErr)
	}
	if secondErr := uow.PutReceipt(context.Background(), receipt); secondErr != putErr {
		t.Fatalf("PutReceipt() after canceled write = %v, want original %v", secondErr, putErr)
	}
	if commitErr := uow.Commit(); commitErr != putErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, putErr)
	}
	assertProcessingReceiptCount(t, database, receipt.DeliveryID, 0)
}

func newGORMReceipt(
	t *testing.T,
	database *Store,
	positionKind string,
	kind core.ReceiptKind,
) core.ProcessingReceipt {
	t.Helper()
	prepareGORMReceiptSource(t, database, SourceKind(positionKind), positionKind == "file")
	var (
		position   core.SourcePosition
		deliveryID core.DeliveryID
		err        error
	)
	if positionKind == "file" {
		file := core.FilePosition{
			Generation: gormReceiptGeneration, DeviceID: 7, Inode: 11,
			StartOffset: 13, EndOffset: 17,
		}
		position, err = core.NewFilePosition(file)
		if err == nil {
			deliveryID, err = core.FileDeliveryID(gormReceiptSourceID, file)
		}
	} else {
		const cursor = "s=gorm-receipt"
		position, err = core.NewJournaldPosition(cursor)
		if err == nil {
			deliveryID, err = core.JournaldDeliveryID(gormReceiptSourceID, cursor)
		}
	}
	if err != nil {
		t.Fatalf("construct receipt position: %v", err)
	}
	committed := time.Unix(500, 123_456_000).In(time.FixedZone("receipt-test", 9*60*60))
	receipt := core.ProcessingReceipt{
		DeliveryID: deliveryID, SourceID: gormReceiptSourceID, Position: position,
		Kind: kind, Committed: committed,
	}
	if kind == core.ReceiptRecordPermanent {
		receipt.Failure = newGORMReceiptFailure(
			committed.Add(-time.Second).In(time.FixedZone("failure-test", -5*60*60)),
		)
	}
	return receipt
}

func newGORMFileReceiptFromPosition(t *testing.T, file core.FilePosition) core.ProcessingReceipt {
	t.Helper()
	position, err := core.NewFilePosition(file)
	if err != nil {
		t.Fatalf("NewFilePosition(): %v", err)
	}
	deliveryID, err := core.FileDeliveryID(gormReceiptSourceID, file)
	if err != nil {
		t.Fatalf("FileDeliveryID(): %v", err)
	}
	return core.ProcessingReceipt{
		DeliveryID: deliveryID, SourceID: gormReceiptSourceID, Position: position,
		Kind: core.ReceiptSuccess, Committed: time.Unix(500, 0).UTC(),
	}
}

func newGORMReceiptFailure(occurredAt time.Time) *core.PermanentFailure {
	return &core.PermanentFailure{
		Stage: "parser", Code: "parser.invalid_record", SanitizedError: "safe diagnostic",
		Action: "dead_letter", OccurredAt: occurredAt,
	}
}

func prepareGORMReceiptSource(
	t *testing.T,
	database *Store,
	kind SourceKind,
	registerGeneration bool,
) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(100, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, now); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	if err := database.EnsureSource(ctx, gormReceiptSourceID, testNodeID, kind, now); err != nil {
		t.Fatalf("EnsureSource(): %v", err)
	}
	if registerGeneration {
		if err := database.RegisterFileGeneration(ctx, FileGeneration{
			SourceID: gormReceiptSourceID, Generation: gormReceiptGeneration,
			DeviceID: 7, Inode: 11, Path: "/var/log/gorm-receipt.log",
			ObservedSize: 17, OpenedAt: now,
		}); err != nil {
			t.Fatalf("RegisterFileGeneration(): %v", err)
		}
	}
}

func commitGORMReceipt(t *testing.T, database *Store, receipt core.ProcessingReceipt) {
	t.Helper()
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	if err := uow.PutReceipt(ctx, receipt); err != nil {
		t.Fatalf("PutReceipt(): %v", err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
}

func assertGORMReceiptCreateFailureIsSticky(
	t *testing.T,
	database *Store,
	receipt core.ProcessingReceipt,
) {
	t.Helper()
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	const callbackName = "guard_wall:test_count_failed_receipt_create"
	callbackCalls := 0
	if err := uow.transactionORM.Callback().Create().Before("gorm:create").Register(
		callbackName,
		func(*gorm.DB) { callbackCalls++ },
	); err != nil {
		t.Fatalf("register GORM create callback: %v", err)
	}
	t.Cleanup(func() {
		_ = uow.transactionORM.Callback().Create().Remove(callbackName)
	})

	firstErr := uow.PutReceipt(ctx, receipt)
	if firstErr == nil {
		t.Fatal("PutReceipt() constraint error = nil")
	}
	if secondErr := uow.PutReceipt(ctx, receipt); secondErr != firstErr {
		t.Fatalf("sticky PutReceipt() error = %v, want original %v", secondErr, firstErr)
	}
	if callbackCalls != 1 {
		t.Fatalf("GORM Create callback calls = %d, want 1 after sticky failure", callbackCalls)
	}
	if commitErr := uow.Commit(); commitErr != firstErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
	}
}

func expectedGORMReceiptVars(receipt core.ProcessingReceipt, wantKind string) []any {
	var generation, cursor *string
	var deviceID, inode, startOffset, endOffset *int64
	positionKind := ""
	if file, ok := receipt.Position.File(); ok {
		positionKind = "file"
		generation = gormReceiptStringPointer(file.Generation)
		deviceID = gormReceiptInt64Pointer(int64(file.DeviceID))
		inode = gormReceiptInt64Pointer(int64(file.Inode))
		startOffset = gormReceiptInt64Pointer(int64(file.StartOffset))
		endOffset = gormReceiptInt64Pointer(int64(file.EndOffset))
	} else if journald, ok := receipt.Position.Journald(); ok {
		positionKind = "journald"
		cursor = gormReceiptStringPointer(journald.Cursor)
	}
	var failureStage, failureCode, sanitizedError, action *string
	var occurredAtUS *int64
	if receipt.Failure != nil {
		failureStage = gormReceiptStringPointer(receipt.Failure.Stage)
		failureCode = gormReceiptStringPointer(receipt.Failure.Code)
		sanitizedError = gormReceiptStringPointer(receipt.Failure.SanitizedError)
		action = gormReceiptStringPointer(receipt.Failure.Action)
		occurredAtUS = gormReceiptInt64Pointer(receipt.Failure.OccurredAt.UTC().UnixMicro())
	}
	return []any{
		string(receipt.DeliveryID), string(receipt.SourceID), positionKind,
		generation, deviceID, inode, startOffset, endOffset, cursor, wantKind,
		failureStage, failureCode, sanitizedError, action, occurredAtUS,
		receipt.Committed.UTC().UnixMicro(),
	}
}

func assertGORMReceiptColumns(
	t *testing.T,
	database *Store,
	receipt core.ProcessingReceipt,
	wantKind string,
) {
	t.Helper()
	var (
		deliveryID, sourceID, positionKind, kind                  string
		generation, cursor                                        sql.NullString
		deviceID, inode, startOffset, endOffset                   sql.NullInt64
		failureStage, failureCode, sanitizedError, terminalAction sql.NullString
		failureOccurredAtUS                                       sql.NullInt64
		committedAtUS                                             int64
	)
	err := database.db.QueryRowContext(context.Background(), `
		SELECT delivery_id, source_id, position_kind, generation, device_id, inode,
			start_offset, end_offset, journald_cursor, kind, failure_stage,
			failure_code, sanitized_error, terminal_action, failure_occurred_at_us,
			committed_at_us
		FROM processing_receipts WHERE delivery_id = ?`, string(receipt.DeliveryID),
	).Scan(
		&deliveryID, &sourceID, &positionKind, &generation, &deviceID, &inode,
		&startOffset, &endOffset, &cursor, &kind, &failureStage,
		&failureCode, &sanitizedError, &terminalAction, &failureOccurredAtUS,
		&committedAtUS,
	)
	if err != nil {
		t.Fatalf("read processing receipt: %v", err)
	}
	want := expectedGORMReceiptVars(receipt, wantKind)
	wantGeneration := gormReceiptNullString(want[3].(*string))
	wantDeviceID := gormReceiptNullInt64(want[4].(*int64))
	wantInode := gormReceiptNullInt64(want[5].(*int64))
	wantStartOffset := gormReceiptNullInt64(want[6].(*int64))
	wantEndOffset := gormReceiptNullInt64(want[7].(*int64))
	wantCursor := gormReceiptNullString(want[8].(*string))
	wantFailureStage := gormReceiptNullString(want[10].(*string))
	wantFailureCode := gormReceiptNullString(want[11].(*string))
	wantSanitizedError := gormReceiptNullString(want[12].(*string))
	wantTerminalAction := gormReceiptNullString(want[13].(*string))
	wantFailureOccurredAtUS := gormReceiptNullInt64(want[14].(*int64))
	if deliveryID != string(receipt.DeliveryID) || sourceID != string(receipt.SourceID) ||
		positionKind != want[2].(string) || generation != wantGeneration ||
		deviceID != wantDeviceID || inode != wantInode || startOffset != wantStartOffset ||
		endOffset != wantEndOffset || cursor != wantCursor || kind != wantKind ||
		failureStage != wantFailureStage || failureCode != wantFailureCode ||
		sanitizedError != wantSanitizedError || terminalAction != wantTerminalAction ||
		failureOccurredAtUS != wantFailureOccurredAtUS ||
		committedAtUS != receipt.Committed.UTC().UnixMicro() {
		t.Fatalf(
			"receipt = delivery %q source %q position %q generation %+v device %+v inode %+v start %+v end %+v cursor %+v kind %q stage %+v code %+v error %+v action %+v occurred %+v committed %d",
			deliveryID, sourceID, positionKind, generation, deviceID, inode, startOffset,
			endOffset, cursor, kind, failureStage, failureCode, sanitizedError,
			terminalAction, failureOccurredAtUS, committedAtUS,
		)
	}
}

func gormReceiptStringPointer(value string) *string {
	return &value
}

func gormReceiptInt64Pointer(value int64) *int64 {
	return &value
}

func gormReceiptNullString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func gormReceiptNullInt64(value *int64) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *value, Valid: true}
}
