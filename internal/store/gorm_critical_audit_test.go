package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

func TestGORMAppendCriticalAuditUsesCreateWithExplicitColumns(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	audit := newGORMCriticalAudit(fixture)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)

	const callbackName = "guard_wall:test_capture_critical_audit_create"
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

	if err := uow.AppendCriticalAudit(ctx, audit); err != nil {
		t.Fatalf("AppendCriticalAudit(): %v", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("GORM Create callback calls = %d, want 1", callbackCalls)
	}
	if capturedTable != "audit_logs" {
		t.Fatalf("GORM Create table = %q, want audit_logs", capturedTable)
	}
	if _, ok := capturedModel.(*criticalAuditRow); !ok {
		t.Fatalf("GORM Create model = %T, want *criticalAuditRow", capturedModel)
	}
	wantColumns := []string{
		"audit_id", "idempotency_key", "node_id", "category", "action", "result", "severity",
		"critical", "actor_type", "delivery_id", "alert_id", "decision_id", "error_code",
		"details_json", "created_at_us",
	}
	if !reflect.DeepEqual(capturedColumns, wantColumns) {
		t.Fatalf("GORM Create columns = %v, want %v", capturedColumns, wantColumns)
	}
	wantSQL := "INSERT INTO `audit_logs` (`audit_id`,`idempotency_key`,`node_id`,`category`,`action`,`result`,`severity`,`critical`,`actor_type`,`delivery_id`,`alert_id`,`decision_id`,`error_code`,`details_json`,`created_at_us`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
	if normalizedSQL := strings.Join(strings.Fields(capturedSQL), " "); normalizedSQL != wantSQL {
		t.Fatalf("GORM Create SQL = %q, want %q", normalizedSQL, wantSQL)
	}
	wantVars := []any{
		audit.ID, audit.IdempotencyKey, string(audit.NodeID), audit.Category, audit.Action,
		audit.Result, audit.Severity, int64(1), audit.ActorType,
		(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
		"{}", audit.CreatedAt.UTC().UnixMicro(),
	}
	if !reflect.DeepEqual(capturedVars, wantVars) {
		t.Fatalf("GORM Create vars = %#v, want %#v", capturedVars, wantVars)
	}

	if err := uow.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
}

func TestGORMAppendCriticalAuditPersistsExactColumns(t *testing.T) {
	t.Run("nullable fields are null and empty details normalize", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		audit := newGORMCriticalAudit(fixture)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if err := uow.AppendCriticalAudit(ctx, audit); err != nil {
			t.Fatalf("AppendCriticalAudit(): %v", err)
		}
		if err := uow.Commit(); err != nil {
			t.Fatalf("Commit(): %v", err)
		}
		assertCriticalAuditColumns(t, database, audit, "{}")
	})

	t.Run("references error and JSON remain exact", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		audit := fixture.audit
		audit.ID = "audit-gorm-references"
		audit.IdempotencyKey = "audit-gorm:references"
		audit.ErrorCode = "processing.example"
		audit.DetailsJSON = []byte(`{"z":1, "a":"exact"}`)
		audit.CreatedAt = audit.CreatedAt.In(time.FixedZone("audit-test", 9*60*60))
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		writeGORMCriticalAuditReferences(t, uow, fixture)
		if err := uow.AppendCriticalAudit(ctx, audit); err != nil {
			t.Fatalf("AppendCriticalAudit(): %v", err)
		}
		if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
			t.Fatalf("PutReceipt(): %v", err)
		}
		if err := uow.Commit(); err != nil {
			t.Fatalf("Commit(): %v", err)
		}
		assertCriticalAuditColumns(t, database, audit, string(audit.DetailsJSON))
	})
}

func TestGORMAppendCriticalAuditNormalizesEmptyDetails(t *testing.T) {
	for _, test := range []struct {
		name    string
		details []byte
	}{
		{name: "nil"},
		{name: "zero length", details: []byte{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			audit := newGORMCriticalAudit(fixture)
			audit.DetailsJSON = test.details
			commitGORMCriticalAudit(t, database, audit)
			assertCriticalAuditColumns(t, database, audit, "{}")
		})
	}
}

func TestGORMAppendCriticalAuditUsesUnitOfWorkTransaction(t *testing.T) {
	t.Run("rollback discards row", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		audit := newGORMCriticalAudit(fixture)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if err := uow.AppendCriticalAudit(ctx, audit); err != nil {
			t.Fatalf("AppendCriticalAudit(): %v", err)
		}
		var transactionCount int
		if err := uow.tx.QueryRowContext(
			ctx, "SELECT count(*) FROM audit_logs WHERE audit_id = ?", audit.ID,
		).Scan(&transactionCount); err != nil {
			t.Fatalf("read audit inside UnitOfWork transaction: %v", err)
		}
		if transactionCount != 1 {
			t.Fatalf("audit count inside UnitOfWork transaction = %d, want 1", transactionCount)
		}
		if err := uow.Rollback(); err != nil {
			t.Fatalf("Rollback(): %v", err)
		}
		assertCriticalAuditCount(t, database, audit.ID, 0)
	})

	t.Run("commit publishes row", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		audit := newGORMCriticalAudit(fixture)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if err := uow.AppendCriticalAudit(ctx, audit); err != nil {
			t.Fatalf("AppendCriticalAudit(): %v", err)
		}
		if err := uow.Commit(); err != nil {
			t.Fatalf("Commit(): %v", err)
		}
		assertCriticalAuditCount(t, database, audit.ID, 1)
	})
}

func TestGORMAppendCriticalAuditConstraintFailureIsSticky(t *testing.T) {
	tests := []struct {
		name          string
		seedExisting  bool
		mutate        func(*CriticalAudit)
		wantAuditID   string
		wantFinalRows int
	}{
		{
			name:         "audit primary key",
			seedExisting: true,
			mutate: func(audit *CriticalAudit) {
				audit.ID = "audit-existing"
				audit.IdempotencyKey = "audit-gorm:new-key"
			},
			wantAuditID: "audit-existing", wantFinalRows: 1,
		},
		{
			name:         "idempotency unique",
			seedExisting: true,
			mutate: func(audit *CriticalAudit) {
				audit.ID = "audit-new-id"
				audit.IdempotencyKey = "audit-existing:key"
			},
			wantAuditID: "audit-new-id", wantFinalRows: 0,
		},
		{
			name: "node foreign key",
			mutate: func(audit *CriticalAudit) {
				audit.NodeID = "11111111111111111111111111111111"
			},
			wantAuditID: "audit-gorm", wantFinalRows: 0,
		},
		{
			name: "alert foreign key",
			mutate: func(audit *CriticalAudit) {
				missing := core.AlertID("missing-alert")
				audit.AlertID = &missing
			},
			wantAuditID: "audit-gorm", wantFinalRows: 0,
		},
		{
			name: "decision foreign key",
			mutate: func(audit *CriticalAudit) {
				missing := core.DecisionID("missing-decision")
				audit.DecisionID = &missing
			},
			wantAuditID: "audit-gorm", wantFinalRows: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			ctx := context.Background()
			if test.seedExisting {
				seed := newGORMCriticalAudit(fixture)
				seed.ID = "audit-existing"
				seed.IdempotencyKey = "audit-existing:key"
				commitGORMCriticalAudit(t, database, seed)
			}

			audit := newGORMCriticalAudit(fixture)
			test.mutate(&audit)
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			const callbackName = "guard_wall:test_count_failed_critical_audit_create"
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

			firstErr := uow.AppendCriticalAudit(ctx, audit)
			if firstErr == nil {
				t.Fatal("AppendCriticalAudit() constraint error = nil")
			}
			if secondErr := uow.AppendCriticalAudit(ctx, audit); secondErr != firstErr {
				t.Fatalf("sticky AppendCriticalAudit() error = %v, want original %v", secondErr, firstErr)
			}
			if receiptErr := uow.PutReceipt(ctx, fixture.receipt); receiptErr != firstErr {
				t.Fatalf("PutReceipt() after GORM failure = %v, want original %v", receiptErr, firstErr)
			}
			if callbackCalls != 1 {
				t.Fatalf("GORM Create callback calls = %d, want 1 after sticky failure", callbackCalls)
			}
			if commitErr := uow.Commit(); commitErr != firstErr {
				t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
			}
			assertCriticalAuditCount(t, database, test.wantAuditID, test.wantFinalRows)
			assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 0)
		})
	}
}

func TestGORMAppendCriticalAuditHonorsCanceledContext(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	audit := newGORMCriticalAudit(fixture)
	uow, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	putErr := uow.AppendCriticalAudit(ctx, audit)
	if !errors.Is(putErr, context.Canceled) {
		t.Fatalf("AppendCriticalAudit() error = %v, want context.Canceled", putErr)
	}
	if secondErr := uow.AppendCriticalAudit(context.Background(), audit); secondErr != putErr {
		t.Fatalf("AppendCriticalAudit() after canceled write = %v, want original %v", secondErr, putErr)
	}
	if commitErr := uow.Commit(); commitErr != putErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, putErr)
	}
	assertCriticalAuditCount(t, database, audit.ID, 0)
}

func TestGORMAppendCriticalAuditRejectsInvalidJSONBeforeCreate(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	audit := newGORMCriticalAudit(fixture)
	audit.DetailsJSON = []byte(`{"broken"`)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	const callbackName = "guard_wall:test_reject_invalid_critical_audit_json"
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

	firstErr := uow.AppendCriticalAudit(ctx, audit)
	if firstErr == nil {
		t.Fatal("AppendCriticalAudit() invalid JSON error = nil")
	}
	if callbackCalls != 0 {
		t.Fatalf("GORM Create callback calls = %d, want 0", callbackCalls)
	}
	if secondErr := uow.AppendCriticalAudit(ctx, audit); secondErr != firstErr {
		t.Fatalf("sticky AppendCriticalAudit() error = %v, want original %v", secondErr, firstErr)
	}
	if commitErr := uow.Commit(); commitErr != firstErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
	}
	assertCriticalAuditCount(t, database, audit.ID, 0)
}

func newGORMCriticalAudit(fixture processingFixture) CriticalAudit {
	return CriticalAudit{
		ID: "audit-gorm", IdempotencyKey: "audit-gorm:key", NodeID: testNodeID,
		Category: "processing", Action: "gorm_audit", Result: "success", Severity: "info",
		ActorType: "system", CreatedAt: fixture.now,
	}
}

func writeGORMCriticalAuditReferences(t *testing.T, uow *UnitOfWork, fixture processingFixture) {
	t.Helper()
	ctx := context.Background()
	if err := uow.PutParserOutcome(ctx, fixture.outcome); err != nil {
		t.Fatalf("PutParserOutcome(): %v", err)
	}
	if err := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
		t.Fatalf("PutDetectionOutcome(): %v", err)
	}
	if inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
		t.Fatalf("PutDetectionContribution() = %v, %v", inserted, err)
	}
	if err := uow.PutAlert(ctx, fixture.alert); err != nil {
		t.Fatalf("PutAlert(): %v", err)
	}
	if err := uow.PutDecision(ctx, fixture.decision); err != nil {
		t.Fatalf("PutDecision(): %v", err)
	}
}

func commitGORMCriticalAudit(t *testing.T, database *Store, audit CriticalAudit) {
	t.Helper()
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	if err := uow.AppendCriticalAudit(ctx, audit); err != nil {
		t.Fatalf("AppendCriticalAudit(): %v", err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
}

func assertCriticalAuditColumns(
	t *testing.T,
	database *Store,
	audit CriticalAudit,
	wantDetails string,
) {
	t.Helper()
	var (
		auditID, idempotencyKey, nodeID, category, action string
		result, severity, actorType, detailsJSON          string
		critical, createdAtUS                             int64
		deliveryID, alertID, decisionID, errorCode        sql.NullString
	)
	err := database.db.QueryRowContext(context.Background(), `
		SELECT audit_id, idempotency_key, node_id, category, action, result, severity,
			critical, actor_type, delivery_id, alert_id, decision_id, error_code,
			details_json, created_at_us
		FROM audit_logs WHERE audit_id = ?`, audit.ID,
	).Scan(
		&auditID, &idempotencyKey, &nodeID, &category, &action, &result, &severity,
		&critical, &actorType, &deliveryID, &alertID, &decisionID, &errorCode,
		&detailsJSON, &createdAtUS,
	)
	if err != nil {
		t.Fatalf("read critical audit: %v", err)
	}
	wantDeliveryID := nullableSQLStringFromDeliveryID(audit.DeliveryID)
	wantAlertID := nullableSQLStringFromAlertID(audit.AlertID)
	wantDecisionID := nullableSQLStringFromDecisionID(audit.DecisionID)
	wantErrorCode := sql.NullString{String: audit.ErrorCode, Valid: audit.ErrorCode != ""}
	if auditID != audit.ID || idempotencyKey != audit.IdempotencyKey || nodeID != string(audit.NodeID) ||
		category != audit.Category || action != audit.Action || result != audit.Result ||
		severity != audit.Severity || critical != 1 || actorType != audit.ActorType ||
		deliveryID != wantDeliveryID || alertID != wantAlertID || decisionID != wantDecisionID ||
		errorCode != wantErrorCode || detailsJSON != wantDetails ||
		createdAtUS != audit.CreatedAt.UTC().UnixMicro() {
		t.Fatalf(
			"critical audit = id %q key %q node %q category %q action %q result %q severity %q critical %d actor %q delivery %+v alert %+v decision %+v error %+v details %q created %d",
			auditID, idempotencyKey, nodeID, category, action, result, severity, critical,
			actorType, deliveryID, alertID, decisionID, errorCode, detailsJSON, createdAtUS,
		)
	}
}

func nullableSQLStringFromDeliveryID(value *core.DeliveryID) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*value), Valid: true}
}

func nullableSQLStringFromAlertID(value *core.AlertID) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*value), Valid: true}
}

func nullableSQLStringFromDecisionID(value *core.DecisionID) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*value), Valid: true}
}

func assertCriticalAuditCount(t *testing.T, database *Store, auditID string, want int) {
	t.Helper()
	var count int
	if err := database.db.QueryRowContext(
		context.Background(), "SELECT count(*) FROM audit_logs WHERE audit_id = ?", auditID,
	).Scan(&count); err != nil {
		t.Fatalf("count critical audits: %v", err)
	}
	if count != want {
		t.Fatalf("critical audit count = %d, want %d", count, want)
	}
}
