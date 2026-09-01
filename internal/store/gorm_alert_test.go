package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

func TestGORMPutAlertUsesCreateWithExplicitColumns(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	if inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
		t.Fatalf("PutDetectionContribution() = %v, %v", inserted, err)
	}

	const callbackName = "guard_wall:test_capture_alert_create"
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

	if err := uow.PutAlert(ctx, fixture.alert); err != nil {
		t.Fatalf("PutAlert(): %v", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("GORM Create callback calls = %d, want 1", callbackCalls)
	}
	if capturedTable != "alerts" {
		t.Fatalf("GORM Create table = %q, want alerts", capturedTable)
	}
	if _, ok := capturedModel.(*alertRow); !ok {
		t.Fatalf("GORM Create model = %T, want *alertRow", capturedModel)
	}
	wantColumns := []string{
		"alert_id", "node_id", "event_id", "rule_id", "rule_version",
		"canonical_target", "observed_at_us", "created_at_us",
	}
	if !reflect.DeepEqual(capturedColumns, wantColumns) {
		t.Fatalf("GORM Create columns = %v, want %v", capturedColumns, wantColumns)
	}
	wantSQL := "INSERT INTO `alerts` (`alert_id`,`node_id`,`event_id`,`rule_id`,`rule_version`,`canonical_target`,`observed_at_us`,`created_at_us`) VALUES (?,?,?,?,?,?,?,?)"
	if normalizedSQL := strings.Join(strings.Fields(capturedSQL), " "); normalizedSQL != wantSQL {
		t.Fatalf("GORM Create SQL = %q, want %q", normalizedSQL, wantSQL)
	}
	wantVars := []any{
		string(fixture.alert.ID), string(fixture.alert.NodeID), string(fixture.alert.EventID),
		string(fixture.alert.RuleID), string(fixture.alert.RuleVersion),
		fixture.alert.CanonicalTarget.String(), fixture.alert.ObservedAt.UTC().UnixMicro(),
		fixture.alert.CreatedAt.UTC().UnixMicro(),
	}
	if !reflect.DeepEqual(capturedVars, wantVars) {
		t.Fatalf("GORM Create vars = %#v, want %#v", capturedVars, wantVars)
	}

	if err := uow.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
}

func TestGORMPutAlertPersistsExactColumns(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	if inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
		t.Fatalf("PutDetectionContribution() = %v, %v", inserted, err)
	}
	if err := uow.PutAlert(ctx, fixture.alert); err != nil {
		t.Fatalf("PutAlert(): %v", err)
	}
	if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
		t.Fatalf("PutReceipt(): %v", err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}

	var (
		alertID         string
		nodeID          string
		eventID         string
		ruleID          string
		ruleVersion     string
		canonicalTarget string
		observedAtUS    int64
		createdAtUS     int64
	)
	err = database.db.QueryRowContext(ctx, `
		SELECT alert_id, node_id, event_id, rule_id, rule_version,
			canonical_target, observed_at_us, created_at_us
		FROM alerts WHERE alert_id = ?`, string(fixture.alert.ID),
	).Scan(
		&alertID, &nodeID, &eventID, &ruleID, &ruleVersion,
		&canonicalTarget, &observedAtUS, &createdAtUS,
	)
	if err != nil {
		t.Fatalf("read alert: %v", err)
	}
	if alertID != string(fixture.alert.ID) || nodeID != string(fixture.alert.NodeID) ||
		eventID != string(fixture.alert.EventID) || ruleID != string(fixture.alert.RuleID) ||
		ruleVersion != string(fixture.alert.RuleVersion) ||
		canonicalTarget != fixture.alert.CanonicalTarget.String() ||
		observedAtUS != fixture.alert.ObservedAt.UTC().UnixMicro() ||
		createdAtUS != fixture.alert.CreatedAt.UTC().UnixMicro() {
		t.Fatalf(
			"alert = id %q node %q event %q rule %q version %q target %q observed %d created %d",
			alertID, nodeID, eventID, ruleID, ruleVersion, canonicalTarget,
			observedAtUS, createdAtUS,
		)
	}
}

func TestGORMPutAlertUsesUnitOfWorkTransaction(t *testing.T) {
	t.Run("rollback discards row", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
			t.Fatalf("PutDetectionContribution() = %v, %v", inserted, err)
		}
		if err := uow.PutAlert(ctx, fixture.alert); err != nil {
			t.Fatalf("PutAlert(): %v", err)
		}
		var transactionCount int
		if err := uow.tx.QueryRowContext(
			ctx, "SELECT count(*) FROM alerts WHERE alert_id = ?", string(fixture.alert.ID),
		).Scan(&transactionCount); err != nil {
			t.Fatalf("read alert inside UnitOfWork transaction: %v", err)
		}
		if transactionCount != 1 {
			t.Fatalf("alert count inside UnitOfWork transaction = %d, want 1", transactionCount)
		}
		if err := uow.Rollback(); err != nil {
			t.Fatalf("Rollback(): %v", err)
		}
		assertAlertCount(t, database, fixture.alert.ID, 0)
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
		if inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
			t.Fatalf("PutDetectionContribution() = %v, %v", inserted, err)
		}
		if err := uow.PutAlert(ctx, fixture.alert); err != nil {
			t.Fatalf("PutAlert(): %v", err)
		}
		if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
			t.Fatalf("PutReceipt(): %v", err)
		}
		if err := uow.Commit(); err != nil {
			t.Fatalf("Commit(): %v", err)
		}
		assertAlertCount(t, database, fixture.alert.ID, 1)
	})
}

func TestGORMPutAlertRequiresDetectionContributionImmediately(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)

	putErr := uow.PutAlert(ctx, fixture.alert)
	if putErr == nil {
		t.Fatal("PutAlert() without detection contribution error = nil")
	}
	if commitErr := uow.Commit(); commitErr != putErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, putErr)
	}
	assertAlertCount(t, database, fixture.alert.ID, 0)
}

func TestGORMPutAlertRequiresNodeIdentityImmediately(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	if inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
		t.Fatalf("PutDetectionContribution() = %v, %v", inserted, err)
	}

	missingNodeAlert := fixture.alert
	missingNodeAlert.NodeID = "11111111111111111111111111111111"
	putErr := uow.PutAlert(ctx, missingNodeAlert)
	if putErr == nil {
		t.Fatal("PutAlert() without node identity error = nil")
	}
	if receiptErr := uow.PutReceipt(ctx, fixture.receipt); receiptErr != putErr {
		t.Fatalf("PutReceipt() after missing node identity = %v, want original %v", receiptErr, putErr)
	}
	if commitErr := uow.Commit(); commitErr != putErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, putErr)
	}
	assertAlertCount(t, database, missingNodeAlert.ID, 0)
	assertDetectionContributionCount(t, database, fixture.contribution, 0)
	assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 0)
}

func TestGORMPutAlertRequiresFrozenRuleRevisionImmediately(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()
	orphanContribution := fixture.contribution
	orphanContribution.RuleVersion = "missing-v1"
	seedOrphanDetectionContribution(t, ctx, database, orphanContribution)

	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	if err := uow.PutParserOutcome(ctx, fixture.outcome); err != nil {
		t.Fatalf("PutParserOutcome(): %v", err)
	}

	missingRuleAlert := fixture.alert
	missingRuleAlert.RuleVersion = orphanContribution.RuleVersion
	putErr := uow.PutAlert(ctx, missingRuleAlert)
	if putErr == nil {
		t.Fatal("PutAlert() without frozen rule revision error = nil")
	}
	if receiptErr := uow.PutReceipt(ctx, fixture.receipt); receiptErr != putErr {
		t.Fatalf("PutReceipt() after missing frozen rule revision = %v, want original %v", receiptErr, putErr)
	}
	if commitErr := uow.Commit(); commitErr != putErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, putErr)
	}
	assertAlertCount(t, database, missingRuleAlert.ID, 0)
	assertParserOutcomeCount(t, database, fixture.outcome.DeliveryID, 0)
	assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 0)
}

func TestGORMPutAlertDetectionMembershipUniqueIsSticky(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()

	committed, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(first): %v", err)
	}
	cleanupOpenUnitOfWork(t, committed)
	if inserted, err := committed.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
		t.Fatalf("PutDetectionContribution(first) = %v, %v", inserted, err)
	}
	if err := committed.PutAlert(ctx, fixture.alert); err != nil {
		t.Fatalf("PutAlert(first): %v", err)
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
	if err := duplicate.PutParserOutcome(ctx, fixture.outcome); err != nil {
		t.Fatalf("PutParserOutcome(duplicate transaction): %v", err)
	}
	const callbackName = "guard_wall:test_count_unique_alert_create"
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

	conflictingAlert := fixture.alert
	conflictingAlert.ID = "alert-same-membership"
	firstErr := duplicate.PutAlert(ctx, conflictingAlert)
	if firstErr == nil {
		t.Fatal("PutAlert() with duplicate detection membership error = nil")
	}
	secondErr := duplicate.PutAlert(ctx, conflictingAlert)
	if secondErr != firstErr {
		t.Fatalf("sticky PutAlert() error = %v, want original %v", secondErr, firstErr)
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
	assertAlertCount(t, database, fixture.alert.ID, 1)
	assertAlertCount(t, database, conflictingAlert.ID, 0)
	assertParserOutcomeCount(t, database, fixture.outcome.DeliveryID, 0)
	assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 1)
}

func TestGORMPutAlertPrimaryKeyIsSticky(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()

	committed, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(first): %v", err)
	}
	cleanupOpenUnitOfWork(t, committed)
	if inserted, err := committed.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
		t.Fatalf("PutDetectionContribution(first) = %v, %v", inserted, err)
	}
	if err := committed.PutAlert(ctx, fixture.alert); err != nil {
		t.Fatalf("PutAlert(first): %v", err)
	}
	if err := committed.PutReceipt(ctx, fixture.receipt); err != nil {
		t.Fatalf("PutReceipt(first): %v", err)
	}
	if err := committed.Commit(); err != nil {
		t.Fatalf("Commit(first): %v", err)
	}

	alternateEventID, err := core.SecurityEventID(
		testNodeID, fixture.deliveryID, processingParserID, processingParserVersion, 1,
	)
	if err != nil {
		t.Fatalf("SecurityEventID(alternate): %v", err)
	}
	alternateContribution := fixture.contribution
	alternateContribution.EventID = alternateEventID
	duplicate, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(duplicate): %v", err)
	}
	cleanupOpenUnitOfWork(t, duplicate)
	if inserted, err := duplicate.PutDetectionContribution(ctx, alternateContribution); err != nil || !inserted {
		t.Fatalf("PutDetectionContribution(alternate) = %v, %v", inserted, err)
	}
	const callbackName = "guard_wall:test_count_primary_key_alert_create"
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

	primaryKeyConflict := fixture.alert
	primaryKeyConflict.EventID = alternateEventID
	firstErr := duplicate.PutAlert(ctx, primaryKeyConflict)
	if firstErr == nil {
		t.Fatal("PutAlert() with duplicate alert ID error = nil")
	}
	secondErr := duplicate.PutAlert(ctx, primaryKeyConflict)
	if secondErr != firstErr {
		t.Fatalf("sticky PutAlert() error = %v, want original %v", secondErr, firstErr)
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
	assertAlertCount(t, database, fixture.alert.ID, 1)
	assertDetectionContributionCount(t, database, alternateContribution, 0)
	assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 1)
}

func TestGORMPutAlertHonorsCanceledContext(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	uow, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	putErr := uow.PutAlert(ctx, fixture.alert)
	if !errors.Is(putErr, context.Canceled) {
		t.Fatalf("PutAlert() error = %v, want context.Canceled", putErr)
	}
	if receiptErr := uow.PutReceipt(context.Background(), fixture.receipt); receiptErr != putErr {
		t.Fatalf("PutReceipt() after canceled GORM write = %v, want original %v", receiptErr, putErr)
	}
	if commitErr := uow.Commit(); commitErr != putErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, putErr)
	}
	assertAlertCount(t, database, fixture.alert.ID, 0)
	assertProcessingReceiptCount(t, database, fixture.receipt.DeliveryID, 0)
}

func assertAlertCount(t *testing.T, database *Store, alertID core.AlertID, want int) {
	t.Helper()
	var count int
	if err := database.db.QueryRowContext(
		context.Background(), "SELECT count(*) FROM alerts WHERE alert_id = ?", string(alertID),
	).Scan(&count); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if count != want {
		t.Fatalf("alert count = %d, want %d", count, want)
	}
}

func assertDetectionContributionCount(
	t *testing.T,
	database *Store,
	contribution core.DetectionContribution,
	want int,
) {
	t.Helper()
	var count int
	if err := database.db.QueryRowContext(
		context.Background(), `
			SELECT count(*) FROM detection_contributions
			WHERE event_id = ? AND rule_id = ? AND rule_version = ?`,
		string(contribution.EventID), string(contribution.RuleID), string(contribution.RuleVersion),
	).Scan(&count); err != nil {
		t.Fatalf("count detection contributions: %v", err)
	}
	if count != want {
		t.Fatalf("detection contribution count = %d, want %d", count, want)
	}
}

func seedOrphanDetectionContribution(
	t *testing.T,
	ctx context.Context,
	database *Store,
	contribution core.DetectionContribution,
) {
	t.Helper()
	conn, err := database.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire seed connection: %v", err)
	}
	foreignKeysDisabled := false
	connectionClosed := false
	t.Cleanup(func() {
		if connectionClosed {
			return
		}
		if foreignKeysDisabled {
			if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON"); err != nil {
				t.Errorf("restore seed connection foreign keys: %v", err)
			}
		}
		if err := conn.Close(); err != nil {
			t.Errorf("close seed connection: %v", err)
		}
	})
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("disable foreign keys for orphan seed: %v", err)
	}
	foreignKeysDisabled = true
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO detection_contributions(
			event_id, rule_id, rule_version, delivery_id, contributed_at_us
		) VALUES (?, ?, ?, ?, ?)`,
		string(contribution.EventID), string(contribution.RuleID), string(contribution.RuleVersion),
		string(contribution.DeliveryID), contribution.ContributedAt.UTC().UnixMicro(),
	); err != nil {
		t.Fatalf("insert orphan detection contribution: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("restore foreign keys after orphan seed: %v", err)
	}
	foreignKeysDisabled = false
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read back foreign keys after orphan seed: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys after orphan seed = %d, want 1", foreignKeys)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("release seed connection: %v", err)
	}
	connectionClosed = true
}
