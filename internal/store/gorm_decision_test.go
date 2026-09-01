package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

func TestGORMPutDecisionUsesCreateWithExplicitColumns(t *testing.T) {
	tests := []struct {
		name       string
		decision   func(processingFixture) core.Decision
		writeAlert bool
		wantSource string
		wantState  string
	}{
		{
			name: "automatic",
			decision: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				decision.CreatedAt = fixture.now.In(time.FixedZone("decision-test", 9*60*60))
				decision.UpdatedAt = decision.CreatedAt.Add(time.Second)
				decision.LastTriggeredAt = decision.CreatedAt.Add(2 * time.Second)
				expiresAt := decision.CreatedAt.Add(time.Hour)
				decision.ExpiresAt = &expiresAt
				decision.SuppressedCount = 3
				return decision
			},
			writeAlert: true,
			wantSource: "automatic",
			wantState:  "active",
		},
		{
			name: "manual",
			decision: func(fixture processingFixture) core.Decision {
				return newGORMManualDecision(fixture, "decision-manual", "198.51.100.8/32")
			},
			wantSource: "manual", wantState: "active",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			decision := test.decision(fixture)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			if test.writeAlert {
				writeGORMDecisionAlertReference(t, uow, fixture)
			}

			callbackName := "guard_wall:test_capture_decision_create_" + test.name
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

			if err := uow.PutDecision(ctx, decision); err != nil {
				t.Fatalf("PutDecision(): %v", err)
			}
			if callbackCalls != 1 {
				t.Fatalf("GORM Create callback calls = %d, want 1", callbackCalls)
			}
			if capturedTable != "decisions" {
				t.Fatalf("GORM Create table = %q, want decisions", capturedTable)
			}
			if _, ok := capturedModel.(*decisionRow); !ok {
				t.Fatalf("GORM Create model = %T, want *decisionRow", capturedModel)
			}
			wantColumns := []string{
				"decision_id", "node_id", "source", "rule_id", "rule_version", "alert_id",
				"canonical_target", "created_at_us", "updated_at_us", "last_triggered_at_us",
				"expires_at_us", "ended_at_us", "state", "end_reason", "suppressed_count",
			}
			if !reflect.DeepEqual(capturedColumns, wantColumns) {
				t.Fatalf("GORM Create columns = %v, want %v", capturedColumns, wantColumns)
			}
			wantSQL := "INSERT INTO `decisions` (`decision_id`,`node_id`,`source`,`rule_id`,`rule_version`,`alert_id`,`canonical_target`,`created_at_us`,`updated_at_us`,`last_triggered_at_us`,`expires_at_us`,`ended_at_us`,`state`,`end_reason`,`suppressed_count`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
			if normalizedSQL := strings.Join(strings.Fields(capturedSQL), " "); normalizedSQL != wantSQL {
				t.Fatalf("GORM Create SQL = %q, want %q", normalizedSQL, wantSQL)
			}
			wantRuleID := decisionStringPointer(decision.RuleID)
			wantRuleVersion := decisionStringPointer(decision.RuleVersion)
			wantAlertID := decisionStringPointer(decision.AlertID)
			wantExpiresAtUS := decisionTimePointer(decision.ExpiresAt)
			wantEndedAtUS := decisionTimePointer(decision.EndedAt)
			wantEndReason := decisionStringPointer(decision.EndReason)
			wantVars := []any{
				string(decision.ID), string(decision.NodeID), test.wantSource,
				wantRuleID, wantRuleVersion, wantAlertID,
				decision.CanonicalTarget.String(), decision.CreatedAt.UTC().UnixMicro(),
				decision.UpdatedAt.UTC().UnixMicro(), decision.LastTriggeredAt.UTC().UnixMicro(),
				wantExpiresAtUS, wantEndedAtUS, test.wantState, wantEndReason,
				decision.SuppressedCount,
			}
			if !reflect.DeepEqual(capturedVars, wantVars) {
				t.Fatalf("GORM Create vars = %#v, want %#v", capturedVars, wantVars)
			}

			if err := uow.Rollback(); err != nil {
				t.Fatalf("Rollback(): %v", err)
			}
		})
	}
}

func TestGORMPutDecisionPersistsExactColumns(t *testing.T) {
	tests := []struct {
		name       string
		decision   func(processingFixture) core.Decision
		writeAlert bool
		wantSource string
		wantState  string
	}{
		{
			name: "automatic active with references and expiry",
			decision: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				decision.CreatedAt = fixture.now.In(time.FixedZone("decision-test", -7*60*60))
				decision.UpdatedAt = decision.CreatedAt.Add(time.Second)
				decision.LastTriggeredAt = decision.CreatedAt.Add(2 * time.Second)
				expiresAt := decision.CreatedAt.Add(time.Hour)
				decision.ExpiresAt = &expiresAt
				decision.SuppressedCount = 7
				return decision
			},
			writeAlert: true, wantSource: "automatic", wantState: "active",
		},
		{
			name: "manual active with nullable fields",
			decision: func(fixture processingFixture) core.Decision {
				return newGORMManualDecision(fixture, "decision-manual-active", "198.51.100.8/32")
			},
			wantSource: "manual", wantState: "active",
		},
		{
			name: "automatic expired",
			decision: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				decision.ID = "decision-automatic-expired"
				decision.AlertID = nil
				decision.CreatedAt = fixture.now.In(time.FixedZone("decision-test", 5*60*60+30*60))
				decision.UpdatedAt = decision.CreatedAt.Add(time.Second)
				decision.LastTriggeredAt = decision.CreatedAt.Add(2 * time.Second)
				expiresAt := decision.CreatedAt.Add(time.Hour)
				endedAt := expiresAt.Add(time.Second)
				reason := core.EndReasonExpired
				decision.ExpiresAt = &expiresAt
				decision.EndedAt = &endedAt
				decision.State = core.DecisionExpired
				decision.EndReason = &reason
				decision.SuppressedCount = 11
				return decision
			},
			wantSource: "automatic", wantState: "expired",
		},
		{
			name: "manual revoked",
			decision: func(fixture processingFixture) core.Decision {
				decision := newGORMManualDecision(
					fixture, "decision-manual-revoked", "203.0.113.9/32",
				)
				endedAt := decision.CreatedAt.Add(time.Minute)
				reason := core.EndReasonManualReplace
				decision.EndedAt = &endedAt
				decision.State = core.DecisionRevoked
				decision.EndReason = &reason
				decision.SuppressedCount = 13
				return decision
			},
			wantSource: "manual", wantState: "revoked",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			decision := test.decision(fixture)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			if test.writeAlert {
				writeGORMDecisionAlertReference(t, uow, fixture)
			}
			if err := uow.PutDecision(ctx, decision); err != nil {
				t.Fatalf("PutDecision(): %v", err)
			}
			if test.writeAlert {
				if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
					t.Fatalf("PutReceipt(): %v", err)
				}
			}
			if err := uow.Commit(); err != nil {
				t.Fatalf("Commit(): %v", err)
			}

			assertGORMDecisionColumns(
				t, database, decision, test.wantSource, test.wantState,
			)
		})
	}
}

func TestGORMPutDecisionUsesUnitOfWorkTransaction(t *testing.T) {
	t.Run("rollback discards row", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		decision := newGORMManualDecision(fixture, "decision-rollback", "198.51.100.10/32")
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		if err := uow.PutDecision(ctx, decision); err != nil {
			t.Fatalf("PutDecision(): %v", err)
		}
		var transactionCount int
		if err := uow.tx.QueryRowContext(
			ctx, "SELECT count(*) FROM decisions WHERE decision_id = ?", string(decision.ID),
		).Scan(&transactionCount); err != nil {
			t.Fatalf("read decision inside UnitOfWork transaction: %v", err)
		}
		if transactionCount != 1 {
			t.Fatalf("decision count inside UnitOfWork transaction = %d, want 1", transactionCount)
		}
		if err := uow.Rollback(); err != nil {
			t.Fatalf("Rollback(): %v", err)
		}
		assertGORMDecisionCount(t, database, decision.ID, 0)
	})

	t.Run("commit publishes row", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		decision := newGORMManualDecision(fixture, "decision-commit", "198.51.100.11/32")
		commitGORMDecision(t, database, decision)
		assertGORMDecisionCount(t, database, decision.ID, 1)
	})
}

func TestGORMPutDecisionConstraintFailureIsSticky(t *testing.T) {
	tests := []struct {
		name              string
		seed              func(processingFixture) core.Decision
		candidate         func(processingFixture) core.Decision
		wantCandidateRows int
		wantStoredTarget  string
	}{
		{
			name: "decision primary key",
			seed: func(fixture processingFixture) core.Decision {
				decision := newGORMManualDecision(fixture, "decision-existing", "198.51.100.20/32")
				endedAt := decision.CreatedAt.Add(time.Minute)
				reason := core.EndReasonManual
				decision.EndedAt = &endedAt
				decision.State = core.DecisionRevoked
				decision.EndReason = &reason
				return decision
			},
			candidate: func(fixture processingFixture) core.Decision {
				decision := newGORMManualDecision(fixture, "decision-existing", "198.51.100.21/32")
				endedAt := decision.CreatedAt.Add(2 * time.Minute)
				reason := core.EndReasonManual
				decision.EndedAt = &endedAt
				decision.State = core.DecisionRevoked
				decision.EndReason = &reason
				return decision
			},
			wantCandidateRows: 1, wantStoredTarget: "198.51.100.20/32",
		},
		{
			name: "active automatic partial unique",
			seed: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				decision.ID = "decision-automatic-existing"
				decision.AlertID = nil
				return decision
			},
			candidate: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				decision.ID = "decision-automatic-conflict"
				decision.AlertID = nil
				return decision
			},
		},
		{
			name: "active manual partial unique",
			seed: func(fixture processingFixture) core.Decision {
				return newGORMManualDecision(fixture, "decision-manual-existing", "203.0.113.20/32")
			},
			candidate: func(fixture processingFixture) core.Decision {
				return newGORMManualDecision(fixture, "decision-manual-conflict", "203.0.113.20/32")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			seed := test.seed(fixture)
			commitGORMDecision(t, database, seed)
			candidate := test.candidate(fixture)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			callbackName := "guard_wall:test_count_failed_decision_create_" + strings.ReplaceAll(test.name, " ", "_")
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

			firstErr := uow.PutDecision(ctx, candidate)
			if firstErr == nil {
				t.Fatal("PutDecision() constraint error = nil")
			}
			if secondErr := uow.PutDecision(ctx, candidate); secondErr != firstErr {
				t.Fatalf("sticky PutDecision() error = %v, want original %v", secondErr, firstErr)
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
			assertGORMDecisionCount(t, database, candidate.ID, test.wantCandidateRows)
			if test.wantStoredTarget != "" {
				var storedTarget string
				if err := database.db.QueryRowContext(
					ctx, "SELECT canonical_target FROM decisions WHERE decision_id = ?", string(candidate.ID),
				).Scan(&storedTarget); err != nil {
					t.Fatalf("read primary-key winner: %v", err)
				}
				if storedTarget != test.wantStoredTarget {
					t.Fatalf("primary-key winner target = %q, want %q", storedTarget, test.wantStoredTarget)
				}
			}
		})
	}
}

func TestGORMPutDecisionTerminalRowsDoNotConflictWithActivePartialUnique(t *testing.T) {
	tests := []struct {
		name       string
		decision   func(processingFixture, core.DecisionID) core.Decision
		wantSource string
	}{
		{
			name: "automatic expired",
			decision: func(fixture processingFixture, id core.DecisionID) core.Decision {
				decision := fixture.decision
				decision.ID = id
				decision.AlertID = nil
				expiresAt := decision.CreatedAt.Add(time.Hour)
				endedAt := expiresAt.Add(time.Second)
				reason := core.EndReasonExpired
				decision.ExpiresAt = &expiresAt
				decision.EndedAt = &endedAt
				decision.State = core.DecisionExpired
				decision.EndReason = &reason
				return decision
			},
			wantSource: "automatic",
		},
		{
			name: "manual revoked",
			decision: func(fixture processingFixture, id core.DecisionID) core.Decision {
				decision := newGORMManualDecision(fixture, id, "203.0.113.24/32")
				endedAt := decision.CreatedAt.Add(time.Minute)
				reason := core.EndReasonManual
				decision.EndedAt = &endedAt
				decision.State = core.DecisionRevoked
				decision.EndReason = &reason
				return decision
			},
			wantSource: "manual",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			first := test.decision(fixture, "decision-terminal-first")
			second := test.decision(fixture, "decision-terminal-second")

			commitGORMDecision(t, database, first)
			commitGORMDecision(t, database, second)

			assertGORMDecisionCount(t, database, first.ID, 1)
			assertGORMDecisionCount(t, database, second.ID, 1)
			var sameScopeRows int
			if err := database.db.QueryRowContext(context.Background(), `
				SELECT count(*) FROM decisions
				WHERE node_id = ? AND source = ? AND canonical_target = ?`,
				string(first.NodeID), test.wantSource, first.CanonicalTarget.String(),
			).Scan(&sameScopeRows); err != nil {
				t.Fatalf("count terminal decisions in same partial-unique scope: %v", err)
			}
			if sameScopeRows != 2 {
				t.Fatalf("terminal decisions in same partial-unique scope = %d, want 2", sameScopeRows)
			}
		})
	}
}

func TestGORMPutDecisionSuppressedCountSQLiteRange(t *testing.T) {
	t.Run("maximum SQLite integer persists", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		decision := newGORMManualDecision(fixture, "decision-max-suppressed", "198.51.100.50/32")
		decision.SuppressedCount = math.MaxInt64

		commitGORMDecision(t, database, decision)
		assertGORMDecisionColumns(t, database, decision, "manual", "active")
	})

	t.Run("value above SQLite integer fails sticky", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		decision := newGORMManualDecision(fixture, "decision-overflow-suppressed", "198.51.100.51/32")
		decision.SuppressedCount = uint64(math.MaxInt64) + 1
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)

		firstErr := uow.PutDecision(ctx, decision)
		if firstErr == nil {
			t.Fatal("PutDecision() with suppressed count above SQLite INTEGER error = nil")
		}
		if !strings.Contains(firstErr.Error(), "uint64 values with high bit set") {
			t.Fatalf("PutDecision() overflow error = %v, want database/sql uint64 range failure", firstErr)
		}
		if secondErr := uow.PutDecision(ctx, decision); secondErr != firstErr {
			t.Fatalf("sticky PutDecision() overflow error = %v, want original %v", secondErr, firstErr)
		}
		if commitErr := uow.Commit(); commitErr != firstErr {
			t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
		}
		assertGORMDecisionCount(t, database, decision.ID, 0)
	})
}

func TestGORMPutDecisionRequiresReferencesImmediately(t *testing.T) {
	tests := []struct {
		name     string
		decision func(processingFixture) core.Decision
	}{
		{
			name: "node identity",
			decision: func(fixture processingFixture) core.Decision {
				decision := newGORMManualDecision(fixture, "decision-missing-node", "198.51.100.30/32")
				decision.NodeID = "11111111111111111111111111111111"
				return decision
			},
		},
		{
			name: "frozen rule revision",
			decision: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				decision.ID = "decision-missing-rule-version"
				missing := core.RuleVersion("missing-v1")
				decision.RuleVersion = &missing
				decision.AlertID = nil
				return decision
			},
		},
		{
			name: "alert",
			decision: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				decision.ID = "decision-missing-alert"
				missing := core.AlertID("missing-alert")
				decision.AlertID = &missing
				return decision
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			decision := test.decision(fixture)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			callbackName := "guard_wall:test_count_missing_decision_reference_" + strings.ReplaceAll(test.name, " ", "_")
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

			firstErr := uow.PutDecision(ctx, decision)
			if firstErr == nil {
				t.Fatal("PutDecision() without immediate reference error = nil")
			}
			if secondErr := uow.PutDecision(ctx, decision); secondErr != firstErr {
				t.Fatalf("sticky PutDecision() error = %v, want original %v", secondErr, firstErr)
			}
			if callbackCalls != 1 {
				t.Fatalf("GORM Create callback calls = %d, want 1 after sticky failure", callbackCalls)
			}
			if commitErr := uow.Commit(); commitErr != firstErr {
				t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
			}
			assertGORMDecisionCount(t, database, decision.ID, 0)
		})
	}
}

func TestGORMPutDecisionHonorsCanceledContext(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	decision := newGORMManualDecision(fixture, "decision-canceled", "198.51.100.40/32")
	uow, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	putErr := uow.PutDecision(ctx, decision)
	if !errors.Is(putErr, context.Canceled) {
		t.Fatalf("PutDecision() error = %v, want context.Canceled", putErr)
	}
	if secondErr := uow.PutDecision(context.Background(), decision); secondErr != putErr {
		t.Fatalf("PutDecision() after canceled write = %v, want original %v", secondErr, putErr)
	}
	if commitErr := uow.Commit(); commitErr != putErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, putErr)
	}
	assertGORMDecisionCount(t, database, decision.ID, 0)
}

func TestGORMPutDecisionRejectsInvalidInputBeforeCreate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(processingFixture) core.Decision
	}{
		{
			name: "automatic_missing_rule_version",
			mutate: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				decision.RuleVersion = nil
				return decision
			},
		},
		{
			name: "manual_with_rule_id",
			mutate: func(fixture processingFixture) core.Decision {
				decision := newGORMManualDecision(fixture, "invalid-manual-rule-id", "198.51.100.51/32")
				decision.RuleID = fixture.decision.RuleID
				return decision
			},
		},
		{
			name: "manual_with_rule_version",
			mutate: func(fixture processingFixture) core.Decision {
				decision := newGORMManualDecision(fixture, "invalid-manual-rule-version", "198.51.100.52/32")
				decision.RuleVersion = fixture.decision.RuleVersion
				return decision
			},
		},
		{
			name: "manual_with_alert",
			mutate: func(fixture processingFixture) core.Decision {
				decision := newGORMManualDecision(fixture, "invalid-manual-alert", "198.51.100.53/32")
				decision.AlertID = fixture.decision.AlertID
				return decision
			},
		},
		{
			name: "active_with_terminal_fields",
			mutate: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				endedAt := fixture.now.Add(time.Minute)
				reason := core.EndReasonManual
				decision.EndedAt = &endedAt
				decision.EndReason = &reason
				return decision
			},
		},
		{
			name: "expired_without_expiry",
			mutate: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				endedAt := fixture.now.Add(time.Minute)
				reason := core.EndReasonExpired
				decision.State = core.DecisionExpired
				decision.EndedAt = &endedAt
				decision.EndReason = &reason
				return decision
			},
		},
		{
			name: "expired_with_wrong_reason",
			mutate: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				expiresAt := fixture.now.Add(time.Minute)
				endedAt := expiresAt.Add(time.Minute)
				reason := core.EndReasonManual
				decision.State = core.DecisionExpired
				decision.ExpiresAt = &expiresAt
				decision.EndedAt = &endedAt
				decision.EndReason = &reason
				return decision
			},
		},
		{
			name: "automatic_with_manual_replace",
			mutate: func(fixture processingFixture) core.Decision {
				decision := fixture.decision
				endedAt := fixture.now.Add(time.Minute)
				reason := core.EndReasonManualReplace
				decision.State = core.DecisionRevoked
				decision.EndedAt = &endedAt
				decision.EndReason = &reason
				return decision
			},
		},
		{
			name: "manual_with_rule_disabled",
			mutate: func(fixture processingFixture) core.Decision {
				decision := newGORMManualDecision(fixture, "invalid-manual-rule-disabled", "198.51.100.54/32")
				endedAt := fixture.now.Add(time.Minute)
				reason := core.EndReasonRuleDisabled
				decision.State = core.DecisionRevoked
				decision.EndedAt = &endedAt
				decision.EndReason = &reason
				return decision
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			decision := test.mutate(fixture)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)

			callbackName := "guard_wall:test_reject_decision_before_create_" + test.name
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

			firstErr := uow.PutDecision(ctx, decision)
			if firstErr == nil {
				t.Fatal("PutDecision() invalid input error = nil")
			}
			if callbackCalls != 0 {
				t.Fatalf("GORM Create callback calls = %d, want 0", callbackCalls)
			}

			followup := newGORMManualDecision(fixture, core.DecisionID("followup-"+test.name), "198.51.100.60/32")
			if secondErr := uow.PutDecision(ctx, followup); secondErr != firstErr {
				t.Fatalf("sticky PutDecision() error = %v, want original %v", secondErr, firstErr)
			}
			if callbackCalls != 0 {
				t.Fatalf("GORM Create callback calls after sticky failure = %d, want 0", callbackCalls)
			}
			if commitErr := uow.Commit(); commitErr != firstErr {
				t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
			}
			assertGORMDecisionCount(t, database, decision.ID, 0)
			assertGORMDecisionCount(t, database, followup.ID, 0)
		})
	}
}

func newGORMManualDecision(
	fixture processingFixture,
	id core.DecisionID,
	target string,
) core.Decision {
	return core.Decision{
		ID: id, NodeID: testNodeID, Source: core.DecisionSourceManual,
		CanonicalTarget: netip.MustParsePrefix(target),
		CreatedAt:       fixture.now, UpdatedAt: fixture.now, LastTriggeredAt: fixture.now,
		State: core.DecisionActive,
	}
}

func decisionStringPointer[T ~string](value *T) *string {
	if value == nil {
		return nil
	}
	result := string(*value)
	return &result
}

func decisionTimePointer(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	result := value.UTC().UnixMicro()
	return &result
}

func writeGORMDecisionAlertReference(t *testing.T, uow *UnitOfWork, fixture processingFixture) {
	t.Helper()
	ctx := context.Background()
	if inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
		t.Fatalf("PutDetectionContribution() = %v, %v", inserted, err)
	}
	if err := uow.PutAlert(ctx, fixture.alert); err != nil {
		t.Fatalf("PutAlert(): %v", err)
	}
}

func commitGORMDecision(t *testing.T, database *Store, decision core.Decision) {
	t.Helper()
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	if err := uow.PutDecision(ctx, decision); err != nil {
		t.Fatalf("PutDecision(): %v", err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
}

func assertGORMDecisionColumns(
	t *testing.T,
	database *Store,
	decision core.Decision,
	wantSource string,
	wantState string,
) {
	t.Helper()
	var (
		decisionID, nodeID, source, canonicalTarget, state string
		ruleID, ruleVersion, alertID, endReason            sql.NullString
		createdAtUS, updatedAtUS, lastTriggeredAtUS        int64
		expiresAtUS, endedAtUS                             sql.NullInt64
		suppressedCount                                    int64
	)
	err := database.db.QueryRowContext(context.Background(), `
		SELECT decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		FROM decisions WHERE decision_id = ?`, string(decision.ID),
	).Scan(
		&decisionID, &nodeID, &source, &ruleID, &ruleVersion, &alertID,
		&canonicalTarget, &createdAtUS, &updatedAtUS, &lastTriggeredAtUS,
		&expiresAtUS, &endedAtUS, &state, &endReason, &suppressedCount,
	)
	if err != nil {
		t.Fatalf("read decision: %v", err)
	}
	wantRuleID := sql.NullString{}
	if decision.RuleID != nil {
		wantRuleID = sql.NullString{String: string(*decision.RuleID), Valid: true}
	}
	wantRuleVersion := sql.NullString{}
	if decision.RuleVersion != nil {
		wantRuleVersion = sql.NullString{String: string(*decision.RuleVersion), Valid: true}
	}
	wantAlertID := sql.NullString{}
	if decision.AlertID != nil {
		wantAlertID = sql.NullString{String: string(*decision.AlertID), Valid: true}
	}
	wantExpiresAtUS := sql.NullInt64{}
	if decision.ExpiresAt != nil {
		wantExpiresAtUS = sql.NullInt64{Int64: decision.ExpiresAt.UTC().UnixMicro(), Valid: true}
	}
	wantEndedAtUS := sql.NullInt64{}
	if decision.EndedAt != nil {
		wantEndedAtUS = sql.NullInt64{Int64: decision.EndedAt.UTC().UnixMicro(), Valid: true}
	}
	wantEndReason := sql.NullString{}
	if decision.EndReason != nil {
		wantEndReason = sql.NullString{String: string(*decision.EndReason), Valid: true}
	}
	if decisionID != string(decision.ID) || nodeID != string(decision.NodeID) ||
		source != wantSource || ruleID != wantRuleID || ruleVersion != wantRuleVersion ||
		alertID != wantAlertID || canonicalTarget != decision.CanonicalTarget.String() ||
		createdAtUS != decision.CreatedAt.UTC().UnixMicro() ||
		updatedAtUS != decision.UpdatedAt.UTC().UnixMicro() ||
		lastTriggeredAtUS != decision.LastTriggeredAt.UTC().UnixMicro() ||
		expiresAtUS != wantExpiresAtUS || endedAtUS != wantEndedAtUS ||
		state != wantState || endReason != wantEndReason ||
		suppressedCount != int64(decision.SuppressedCount) {
		t.Fatalf(
			"decision = id %q node %q source %q rule %+v version %+v alert %+v target %q created %d updated %d triggered %d expires %+v ended %+v state %q reason %+v suppressed %d",
			decisionID, nodeID, source, ruleID, ruleVersion, alertID, canonicalTarget,
			createdAtUS, updatedAtUS, lastTriggeredAtUS, expiresAtUS, endedAtUS,
			state, endReason, suppressedCount,
		)
	}
}

func assertGORMDecisionCount(
	t *testing.T,
	database *Store,
	decisionID core.DecisionID,
	want int,
) {
	t.Helper()
	var count int
	if err := database.db.QueryRowContext(
		context.Background(), "SELECT count(*) FROM decisions WHERE decision_id = ?", string(decisionID),
	).Scan(&count); err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if count != want {
		t.Fatalf("decision count = %d, want %d", count, want)
	}
}
