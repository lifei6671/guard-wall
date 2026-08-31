package store

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

const (
	processingSourceID      core.SourceID      = "processing-source"
	processingParserID      core.ParserID      = "parser-1"
	processingParserVersion core.ParserVersion = "v1"
	processingGeneration                       = "abcdef0123456789abcdef0123456789"
)

type processingFixture struct {
	now              time.Time
	deliveryID       core.DeliveryID
	eventID          core.EventID
	position         core.SourcePosition
	outcome          core.ParserTerminalOutcome
	detectionOutcome core.DetectionTerminalOutcome
	contribution     core.DetectionContribution
	alert            core.Alert
	decision         core.Decision
	projection       core.DesiredBanProjection
	audit            CriticalAudit
	receipt          core.ProcessingReceipt
}

func TestProcessingSemanticWritersCommitTogether(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	uow, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeCompleteProcessingOutcome(t, uow, fixture)
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
	assertProcessingCounts(t, database, 1)
}

func TestDetectionContributionDuplicateDoesNotContributeTwice(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution)
	if err != nil || !inserted {
		t.Fatalf("first PutDetectionContribution() = %v,%v", inserted, err)
	}
	inserted, err = uow.PutDetectionContribution(ctx, fixture.contribution)
	if err != nil || inserted {
		t.Fatalf("duplicate PutDetectionContribution() = %v,%v", inserted, err)
	}
	if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
		t.Fatal(err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatal(err)
	}

	replay, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err = replay.PutDetectionContribution(ctx, fixture.contribution)
	if err != nil || inserted {
		t.Fatalf("committed duplicate PutDetectionContribution() = %v,%v", inserted, err)
	}
	if err := replay.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM detection_contributions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("detection contribution count = %d, want 1", count)
	}
}

func TestProcessingSemanticWriterFailureRollsBackEverything(t *testing.T) {
	for _, failStage := range []string{"parser", "detection", "alert", "decision", "projection", "audit", "receipt"} {
		t.Run(failStage, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeUntilInjectedFailure(ctx, uow, fixture, failStage); err == nil {
				t.Fatalf("%s write error = nil", failStage)
			}
			if err := uow.Commit(); err == nil {
				t.Fatalf("Commit() after %s failure = nil", failStage)
			}
			assertProcessingCounts(t, database, 0)
		})
	}
}

func TestProcessingSemanticForeignKeysAndStableIdentity(t *testing.T) {
	t.Run("parser outcome requires receipt at commit", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		uow, err := database.BeginProcessing(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := uow.PutParserOutcome(context.Background(), fixture.outcome); err != nil {
			t.Fatal(err)
		}
		if err := uow.Commit(); err == nil {
			t.Fatal("Commit() without terminal receipt = nil")
		}
		assertProcessingCounts(t, database, 0)
	})

	t.Run("alert requires detection membership", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		uow, err := database.BeginProcessing(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := uow.PutAlert(context.Background(), fixture.alert); err == nil {
			t.Fatal("PutAlert() without contribution = nil")
		}
		if err := uow.Commit(); err == nil {
			t.Fatal("Commit() after missing membership = nil")
		}
	})

	t.Run("membership key cannot change delivery identity", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
			t.Fatalf("first contribution = %v,%v", inserted, err)
		}
		conflicting := fixture.contribution
		conflicting.DeliveryID, err = core.FileDeliveryID(processingSourceID, core.FilePosition{
			Generation: processingGeneration, StartOffset: 10, EndOffset: 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := uow.PutDetectionContribution(ctx, conflicting); err == nil {
			t.Fatal("conflicting delivery identity was accepted")
		}
		if err := uow.Commit(); err == nil {
			t.Fatal("Commit() after identity conflict = nil")
		}
		assertProcessingCounts(t, database, 0)
	})
}

func prepareProcessingFixture(t *testing.T, database *Store) processingFixture {
	t.Helper()
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	now := time.Unix(200, 0).UTC()
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO parsers(parser_id, enabled, created_at_us, updated_at_us)
		VALUES (?, 1, ?, ?)`, string(processingParserID), now.UnixMicro(), now.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO parser_versions(parser_id, version, definition, definition_sha256, created_at_us)
		VALUES (?, ?, '{}', ?, ?)`, string(processingParserID), string(processingParserVersion), strings.Repeat("1", 64), now.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE parsers SET active_version = ? WHERE parser_id = ?`,
		string(processingParserVersion), string(processingParserID)); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(ctx, processingSourceID, testNodeID, SourceKindFile, now); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterFileGeneration(ctx, FileGeneration{
		SourceID: processingSourceID, Generation: processingGeneration, DeviceID: 1, Inode: 2,
		Path: "/var/log/processing.log", ObservedSize: 10, OpenedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return processingFixtureValues(t)
}

func processingFixtureValues(t *testing.T) processingFixture {
	t.Helper()
	now := time.Unix(200, 0).UTC()
	position, err := core.NewFilePosition(core.FilePosition{
		Generation: processingGeneration, DeviceID: 1, Inode: 2, StartOffset: 0, EndOffset: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := core.FileDeliveryID(processingSourceID, core.FilePosition{
		Generation: processingGeneration, StartOffset: 0, EndOffset: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := core.SecurityEventID(testNodeID, deliveryID, processingParserID, processingParserVersion, 0)
	if err != nil {
		t.Fatal(err)
	}
	ruleID := core.RuleID("rule-1")
	ruleVersion := core.RuleVersion("v1")
	alertID := core.AlertID("alert-1")
	decisionID := core.DecisionID("decision-1")
	target := netip.MustParsePrefix("192.0.2.1/32")
	return processingFixture{
		now: now, deliveryID: deliveryID, eventID: eventID, position: position,
		outcome: core.ParserTerminalOutcome{
			DeliveryID: deliveryID, ParserID: processingParserID, ParserVersion: processingParserVersion,
			Kind: core.ParserOutcomeSuccess, EmittedCount: 1, CompletedAt: now,
		},
		contribution: core.DetectionContribution{
			DeliveryID: deliveryID, EventID: eventID, RuleID: ruleID, RuleVersion: ruleVersion, ContributedAt: now,
		},
		detectionOutcome: core.DetectionTerminalOutcome{
			DeliveryID: deliveryID, EventID: eventID, RuleID: ruleID, RuleVersion: ruleVersion,
			Kind: core.DetectionOutcomeSuccess, CompletedAt: now,
		},
		alert: core.Alert{
			ID: alertID, NodeID: testNodeID, EventID: eventID, RuleID: ruleID,
			RuleVersion: ruleVersion, CanonicalTarget: target, ObservedAt: now, CreatedAt: now,
		},
		decision: core.Decision{
			ID: decisionID, NodeID: testNodeID, Source: core.DecisionSourceAutomatic,
			RuleID: &ruleID, RuleVersion: &ruleVersion, AlertID: &alertID, CanonicalTarget: target,
			CreatedAt: now, UpdatedAt: now, LastTriggeredAt: now, State: core.DecisionActive,
		},
		projection: core.DesiredBanProjection{
			NodeID: testNodeID, CanonicalTarget: target, State: core.BanProjectionPresent,
			ActiveCount: 1, Revision: 1,
		},
		audit: CriticalAudit{
			ID: "audit-1", IdempotencyKey: "delivery-1", NodeID: testNodeID,
			Category: "processing", Action: "completed", Result: "success", Severity: "info",
			ActorType: "source", DeliveryID: &deliveryID, AlertID: &alertID, DecisionID: &decisionID,
			CreatedAt: now,
		},
		receipt: core.ProcessingReceipt{
			DeliveryID: deliveryID, SourceID: processingSourceID, Position: position,
			Kind: core.ReceiptSuccess, Committed: now.Add(time.Second),
		},
	}
}

func writeCompleteProcessingOutcome(t *testing.T, uow *UnitOfWork, fixture processingFixture) {
	t.Helper()
	ctx := context.Background()
	if err := uow.PutParserOutcome(ctx, fixture.outcome); err != nil {
		t.Fatal(err)
	}
	if err := uow.PutDetectionOutcome(ctx, fixture.detectionOutcome); err != nil {
		t.Fatal(err)
	}
	if inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution); err != nil || !inserted {
		t.Fatalf("PutDetectionContribution() = %v,%v", inserted, err)
	}
	if err := uow.PutAlert(ctx, fixture.alert); err != nil {
		t.Fatal(err)
	}
	if err := uow.PutDecision(ctx, fixture.decision); err != nil {
		t.Fatal(err)
	}
	if err := uow.PutProjection(ctx, fixture.projection, fixture.now); err != nil {
		t.Fatal(err)
	}
	if err := uow.AppendCriticalAudit(ctx, fixture.audit); err != nil {
		t.Fatal(err)
	}
	if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
		t.Fatal(err)
	}
}

func writeUntilInjectedFailure(ctx context.Context, uow *UnitOfWork, fixture processingFixture, failStage string) error {
	outcome := fixture.outcome
	if failStage == "parser" {
		outcome.ParserVersion = "missing"
	}
	if err := uow.PutParserOutcome(ctx, outcome); err != nil {
		return err
	}
	detectionOutcome := fixture.detectionOutcome
	if failStage == "detection" {
		detectionOutcome.RuleVersion = "missing"
	}
	if err := uow.PutDetectionOutcome(ctx, detectionOutcome); err != nil {
		return err
	}
	contribution := fixture.contribution
	if _, err := uow.PutDetectionContribution(ctx, contribution); err != nil {
		return err
	}
	alert := fixture.alert
	if failStage == "alert" {
		alert.NodeID = "11111111111111111111111111111111"
	}
	if err := uow.PutAlert(ctx, alert); err != nil {
		return err
	}
	decision := fixture.decision
	if failStage == "decision" {
		decision.NodeID = "11111111111111111111111111111111"
	}
	if err := uow.PutDecision(ctx, decision); err != nil {
		return err
	}
	projection := fixture.projection
	if failStage == "projection" {
		projection.NodeID = "11111111111111111111111111111111"
	}
	if err := uow.PutProjection(ctx, projection, fixture.now); err != nil {
		return err
	}
	audit := fixture.audit
	if failStage == "audit" {
		audit.NodeID = "11111111111111111111111111111111"
	}
	if err := uow.AppendCriticalAudit(ctx, audit); err != nil {
		return err
	}
	receipt := fixture.receipt
	if failStage == "receipt" {
		receipt.SourceID = "missing-source"
		missingDeliveryID, err := core.FileDeliveryID(receipt.SourceID, core.FilePosition{
			Generation: processingGeneration, StartOffset: 0, EndOffset: 10,
		})
		if err != nil {
			return err
		}
		receipt.DeliveryID = missingDeliveryID
	}
	return uow.PutReceipt(ctx, receipt)
}

func assertProcessingCounts(t *testing.T, database *Store, want int) {
	t.Helper()
	for _, table := range []string{
		"parser_terminal_outcomes", "detection_terminal_outcomes", "detection_contributions", "alerts", "decisions",
		"desired_ban_projections", "audit_logs", "processing_receipts",
	} {
		var count int
		if err := database.db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != want {
			t.Errorf("%s count = %d, want %d", table, count, want)
		}
	}
}
