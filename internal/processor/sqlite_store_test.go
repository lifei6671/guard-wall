package processor

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/detection"
	"github.com/lifei6671/guard-wall/internal/enforcement"
	"github.com/lifei6671/guard-wall/internal/store"
	"modernc.org/sqlite"
)

func TestSQLiteCoordinatorReceiptReplaySkipsSecondAttempt(t *testing.T) {
	database, _ := openSQLiteProcessingStore(t)
	runner := &zeroOutcomeRunner{}
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), runner)
	delivery := testDelivery(t, 1)

	if _, err := coordinator.Process(context.Background(), delivery); err != nil {
		t.Fatalf("first Process(): %v", err)
	}
	delivery.Sequence = 7
	completion, err := coordinator.Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("replay Process(): %v", err)
	}
	if runner.calls != 1 || completion.Sequence != 7 {
		t.Fatalf("runner calls/completion = %d/%+v", runner.calls, completion)
	}
	receipt, found, err := database.FindProcessingReceipt(context.Background(), delivery.ID)
	if err != nil || !found || receipt.DeliveryID != delivery.ID {
		t.Fatalf("FindProcessingReceipt() = %+v,%v,%v", receipt, found, err)
	}
}

func TestSQLiteCoordinatorCommitUnknownUsesIndependentReceiptReadback(t *testing.T) {
	database, _ := openSQLiteProcessingStore(t)
	adapter := newEnforcingSQLiteStoreAdapter(t, database)
	commitResultLost := errors.New("injected connection loss after commit")
	adapter.commit = func(unit *store.UnitOfWork) error {
		if err := unit.Commit(); err != nil {
			return err
		}
		return commitResultLost
	}
	delivery := testDelivery(t, 1)

	completion, err := NewCoordinator(adapter, &zeroOutcomeRunner{}).Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("Process(): %v", err)
	}
	if completion.DeliveryID != delivery.ID {
		t.Fatalf("completion = %+v", completion)
	}
}

func TestSQLiteCommitCanceledBeforeCommitRollsBackAndClosesTransaction(t *testing.T) {
	database, _ := openSQLiteProcessingStore(t)
	adapter := newEnforcingSQLiteStoreAdapter(t, database)
	ctx, cancel := context.WithCancel(context.Background())
	unit, err := adapter.beginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	delivery := testDelivery(t, 1)
	receipt := core.ProcessingReceipt{
		DeliveryID: delivery.ID, SourceID: delivery.Record.SourceID,
		Position: delivery.Record.Position, Kind: core.ReceiptSuccess,
		Committed: time.Unix(1_700_000_001, 0).UTC(),
	}
	if err := unit.writeReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	cancel()
	state, err := unit.commit(ctx)
	if state != commitRejected || !errors.Is(err, context.Canceled) {
		t.Fatalf("commit() = %v,%v, want Rejected/context.Canceled", state, err)
	}
	if err := unit.rollback(); err == nil {
		t.Fatal("rejected transaction remained open")
	}
	if _, found, err := database.FindProcessingReceipt(context.Background(), delivery.ID); err != nil || found {
		t.Fatalf("canceled receipt readback found=%v err=%v", found, err)
	}
	fresh, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatalf("begin after rejected transaction: %v", err)
	}
	if err := fresh.Rollback(); err != nil {
		t.Fatalf("rollback fresh transaction: %v", err)
	}
}

func TestSQLiteCoordinatorCommitsTypedOutcomesWithReceipt(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	delivery := testDelivery(t, 1)

	if _, err := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), &fullRunner{}).
		Process(context.Background(), delivery); err != nil {
		t.Fatalf("Process(): %v", err)
	}

	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	for _, table := range []string{
		"parser_terminal_outcomes", "detection_terminal_outcomes", "detection_contributions",
		"alerts", "decisions", "desired_ban_projections", "processing_receipts",
	} {
		var count int
		if err := connection.QueryRowContext(
			context.Background(), "SELECT count(*) FROM "+table,
		).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("%s count = %d, want 1", table, count)
		}
	}
	var auditCount int
	if err := connection.QueryRowContext(context.Background(), "SELECT count(*) FROM audit_logs").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Errorf("audit_logs count = %d, want 2", auditCount)
	}
}

func TestSQLitePipelineCommitsPlanDetectionEffectsReceiptAndWindow(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	delivery := testDelivery(t, 1)
	ledger := detection.NewLedger()
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-1", Version: "v1"}},
	}
	parsers := &scriptedParserRunner{events: map[core.ParserID][]core.EventFields{
		"parser-1": {{EventType: "auth.login_failed"}},
	}}
	evaluator := &scriptedRuleEvaluator{
		match:   RuleMatch{Applicable: true, GroupKey: "group-a", DistinctKey: "alice"},
		effects: fullDetectionEffects(delivery.Record.ObservedAt),
	}
	pipeline := NewPipeline(planNodeID, catalog, parsers, evaluator, ledger)
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }

	completion, err := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), pipeline).
		Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("Process(): %v", err)
	}
	if completion.DeliveryID != delivery.ID {
		t.Fatalf("completion = %+v", completion)
	}

	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	counts := map[string]int{
		"parser_terminal_outcomes":    1,
		"detection_terminal_outcomes": 1,
		"detection_contributions":     1,
		"alerts":                      1,
		"decisions":                   0,
		"desired_ban_projections":     0,
		"audit_logs":                  0,
		"processing_receipts":         1,
	}
	for table, want := range counts {
		var count int
		if err := connection.QueryRowContext(
			context.Background(), "SELECT count(*) FROM "+table,
		).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != want {
			t.Errorf("%s count = %d, want %d", table, count, want)
		}
	}
	window, err := ledger.Snapshot(context.Background(), detection.WindowKey{
		RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a",
	})
	if err != nil || window != (detection.Snapshot{Count: 1, DistinctCount: 1}) {
		t.Fatalf("Window Snapshot() = %+v,%v", window, err)
	}
}

func TestSQLitePipelineAutomaticDecisionCreateAndDuplicateSuppression(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	connection := openSQLiteTestConnection(t, path)
	if _, err := connection.ExecContext(context.Background(), `
		INSERT INTO rule_versions(rule_id, version, definition, definition_sha256, created_at_us)
		VALUES ('rule-1', 'v2', '{}', ?, ?)`, strings.Repeat("2", 64), time.Unix(1_700_000_000, 0).UTC().UnixMicro()); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	connection.Close()
	first := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_000_000, 0).UTC())
	second := sqliteDeliveryAt(t, 10, 20, time.Unix(1_700_000_060, 0).UTC())
	ledger := detection.NewLedger()
	firstCatalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-1", Version: "v1"}},
	}
	secondCatalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-1", Version: "v2"}},
	}
	firstPipeline := NewPipeline(planNodeID, firstCatalog, statelessParserRunner{}, automaticRuleEvaluator{}, ledger)
	firstPipeline.clock = func() time.Time { return first.Record.ObservedAt.Add(time.Second) }
	wakes := &processorRecordingWakeSink{}
	if _, err := NewCoordinator(newEnforcingSQLiteStoreAdapterWithWake(t, database, wakes), firstPipeline).Process(context.Background(), first); err != nil {
		t.Fatalf("first Process(): %v", err)
	}
	secondPipeline := NewPipeline(planNodeID, secondCatalog, statelessParserRunner{}, automaticRuleEvaluator{}, ledger)
	secondPipeline.clock = func() time.Time { return second.Record.ObservedAt.Add(time.Second) }
	if _, err := NewCoordinator(newEnforcingSQLiteStoreAdapterWithWake(t, database, wakes), secondPipeline).Process(context.Background(), second); err != nil {
		t.Fatalf("duplicate Process(): %v", err)
	}
	if wakes.count() != 1 {
		t.Fatalf("automatic create/duplicate wakes = %d, want 1", wakes.count())
	}

	connection = openSQLiteTestConnection(t, path)
	defer connection.Close()
	var (
		decisionID, ruleVersion, alertID                                  string
		createdAt, updatedAt, lastTriggeredAt, expiresAt, suppressedCount int64
	)
	if err := connection.QueryRowContext(context.Background(), `
		SELECT decision_id, rule_version, alert_id, created_at_us, updated_at_us,
			last_triggered_at_us, expires_at_us, suppressed_count
		FROM decisions WHERE source = 'automatic' AND state = 'active'`).Scan(
		&decisionID, &ruleVersion, &alertID, &createdAt, &updatedAt,
		&lastTriggeredAt, &expiresAt, &suppressedCount,
	); err != nil {
		t.Fatal(err)
	}
	firstEventID, err := core.SecurityEventID(planNodeID, first.ID, "parser-1", "v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	firstTriggeredAt := first.Record.ObservedAt.Add(time.Second)
	if decisionID != "decision-"+string(firstEventID) || ruleVersion != "v1" ||
		alertID != "alert-"+string(firstEventID) {
		t.Fatalf("duplicate replaced frozen identity: id=%q rule=%q alert=%q", decisionID, ruleVersion, alertID)
	}
	if createdAt != firstTriggeredAt.UnixMicro() || updatedAt != firstTriggeredAt.UnixMicro() ||
		lastTriggeredAt != second.Record.ObservedAt.Add(time.Second).UnixMicro() ||
		expiresAt != firstTriggeredAt.Add(10*time.Minute).UnixMicro() || suppressedCount != 1 {
		t.Fatalf("unexpected automatic decision times/count: created=%d updated=%d last=%d expires=%d suppressed=%d",
			createdAt, updatedAt, lastTriggeredAt, expiresAt, suppressedCount)
	}
	var projectionState, projectionTarget string
	var projectionRevision, projectionUpdatedAt, activeCount, effectiveUntil int64
	if err := connection.QueryRowContext(context.Background(), `
		SELECT state, canonical_target, target_projection_revision, updated_at_us,
			active_count, effective_until_us
		FROM desired_ban_projections`).Scan(
		&projectionState, &projectionTarget, &projectionRevision, &projectionUpdatedAt,
		&activeCount, &effectiveUntil,
	); err != nil {
		t.Fatal(err)
	}
	if projectionState != "present" || projectionTarget != "192.0.2.10/32" ||
		projectionRevision != 1 || projectionUpdatedAt != firstTriggeredAt.UnixMicro() ||
		activeCount != 1 || effectiveUntil != firstTriggeredAt.Add(10*time.Minute).UnixMicro() {
		t.Fatalf("suppression changed projection: state=%s target=%s revision=%d updated=%d active=%d until=%d",
			projectionState, projectionTarget, projectionRevision, projectionUpdatedAt, activeCount, effectiveUntil)
	}
	var membership string
	var generation, snapshotRevision int64
	if err := connection.QueryRowContext(context.Background(), `
		SELECT e.desired_membership, e.target_enforcement_generation, d.snapshot_revision
		FROM enforcement_states e JOIN desired_firewall_state d ON d.singleton = 1`).Scan(
		&membership, &generation, &snapshotRevision,
	); err != nil {
		t.Fatal(err)
	}
	if membership != "present" || generation != 1 || snapshotRevision != 1 {
		t.Fatalf("automatic desired state = %s/%d/%d", membership, generation, snapshotRevision)
	}
	for table, want := range map[string]int{
		"detection_terminal_outcomes": 2,
		"detection_contributions":     2,
		"alerts":                      2,
		"decisions":                   1,
		"desired_ban_projections":     1,
		"audit_logs":                  2,
		"processing_receipts":         2,
	} {
		var count int
		if err := connection.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("%s count = %d, want %d", table, count, want)
		}
	}
	var createAudits, suppressionAudits, correctlyBound int
	secondEventID, err := core.SecurityEventID(planNodeID, second.ID, "parser-1", "v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(context.Background(), `
		SELECT sum(action = 'automatic_create'), sum(action = 'automatic_suppress'),
			sum(critical = 1 AND (
				(action = 'automatic_create' AND delivery_id = ? AND alert_id = ? AND decision_id = ?)
				OR
				(action = 'automatic_suppress' AND delivery_id = ? AND alert_id = ? AND decision_id = ?
					AND json_extract(details_json, '$.event_id') = ?
					AND json_extract(details_json, '$.rule_id') = 'rule-1'
					AND json_extract(details_json, '$.rule_version') = 'v2')
			))
		FROM audit_logs`,
		string(first.ID), "alert-"+string(firstEventID), decisionID,
		string(second.ID), "alert-"+string(secondEventID), decisionID, string(secondEventID),
	).Scan(&createAudits, &suppressionAudits, &correctlyBound); err != nil {
		t.Fatal(err)
	}
	if createAudits != 1 || suppressionAudits != 1 || correctlyBound != 2 {
		t.Fatalf("decision audits = create:%d suppress:%d correctly-bound:%d",
			createAudits, suppressionAudits, correctlyBound)
	}
	for _, version := range []core.RuleVersion{"v1", "v2"} {
		snapshot, err := ledger.Snapshot(context.Background(), detection.WindowKey{
			RuleID: "rule-1", RuleVersion: version, GroupKey: "group-a",
		})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Count != 1 {
			t.Fatalf("Window %s count = %d, want 1", version, snapshot.Count)
		}
	}
}

func TestSQLiteAutomaticCommitUnknownReadbackWakesProvenCommit(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	delivery := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_010_000, 0).UTC())
	wakes := &processorRecordingWakeSink{}
	adapter := newEnforcingSQLiteStoreAdapterWithWake(t, database, wakes)
	adapter.commit = func(unit *store.UnitOfWork) error {
		if err := unit.Commit(); err != nil {
			return err
		}
		return errors.New("injected lost commit acknowledgement")
	}
	completion, err := NewCoordinator(
		adapter, automaticPipeline(delivery, "v1", detection.NewLedger()),
	).Process(context.Background(), delivery)
	if err != nil || completion.DeliveryID != delivery.ID || wakes.count() != 1 {
		t.Fatalf("commit-unknown proof = %+v/%v wakes=%d", completion, err, wakes.count())
	}
}

func TestSQLiteAutomaticPostCommitWakeFailurePreservesCompletion(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	delivery := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_020_000, 0).UTC())
	wakeFailure := errors.New("injected automatic wake failure")
	wakes := &processorFailOnceWakeSink{failure: wakeFailure}
	adapter := newEnforcingSQLiteStoreAdapterWithWake(t, database, wakes)
	completion, err := NewCoordinator(
		adapter, automaticPipeline(delivery, "v1", detection.NewLedger()),
	).Process(context.Background(), delivery)
	if !errors.Is(err, decision.ErrPostCommitWake) || !errors.Is(err, wakeFailure) ||
		completion.DeliveryID != delivery.ID {
		t.Fatalf("post-commit automatic wake = %+v/%v", completion, err)
	}
	if _, found, readErr := database.FindProcessingReceipt(context.Background(), delivery.ID); readErr != nil || !found {
		t.Fatalf("committed receipt after wake failure = found:%v err:%v", found, readErr)
	}
	replayed, err := NewCoordinator(adapter, &zeroOutcomeRunner{}).Process(context.Background(), delivery)
	if err != nil || replayed.DeliveryID != delivery.ID || wakes.count() != 2 {
		t.Fatalf("receipt replay wake recovery = %+v/%v calls=%d", replayed, err, wakes.count())
	}
}

func TestSQLiteBaseAdapterReceiptReplayIgnoresUnrelatedPendingTarget(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	pendingDelivery := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_025_000, 0).UTC())
	if _, err := NewCoordinator(
		newEnforcingSQLiteStoreAdapter(t, database),
		automaticPipeline(pendingDelivery, "v1", detection.NewLedger()),
	).Process(context.Background(), pendingDelivery); err != nil {
		t.Fatalf("create pending Target: %v", err)
	}
	pending, err := database.PendingTargetEnforcementChanges(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending Target changes = %+v, %v", pending, err)
	}

	delivery := sqliteDeliveryAt(t, 10, 20, time.Unix(1_700_025_001, 0).UTC())
	runner := &zeroOutcomeRunner{}
	coordinator := NewCoordinator(NewSQLiteStoreAdapter(database), runner)
	if _, err := coordinator.Process(context.Background(), delivery); err != nil {
		t.Fatalf("first Process(): %v", err)
	}
	delivery.Sequence = 9
	completion, err := coordinator.Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("receipt replay: %v", err)
	}
	if runner.calls != 1 || completion.Sequence != 9 {
		t.Fatalf("runner calls/completion = %d/%+v", runner.calls, completion)
	}
}

func TestSQLiteBaseAdapterRejectsAutomaticDecisionWithoutDesiredStateDependencies(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	delivery := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_030_000, 0).UTC())
	_, err := NewCoordinator(
		NewSQLiteStoreAdapter(database), automaticPipeline(delivery, "v1", detection.NewLedger()),
	).Process(context.Background(), delivery)
	if err == nil || !strings.Contains(err.Error(), "desired-state dependencies are required") {
		t.Fatalf("base adapter automatic error = %v", err)
	}
	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	for _, table := range []string{
		"decisions", "desired_ban_projections", "enforcement_states", "processing_receipts",
	} {
		var count int
		if err := connection.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count after rejected base adapter = %d", table, count)
		}
	}
}

func TestSQLitePipelineRulePermanentCommitsTerminalOutcomeAndSuccessSibling(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	connection := openSQLiteTestConnection(t, path)
	now := time.Unix(1_700_000_000, 0).UTC().UnixMicro()
	if _, err := connection.ExecContext(context.Background(), `
		INSERT INTO rules(rule_id, enabled, created_at_us, updated_at_us)
		VALUES ('rule-b', 1, ?, ?)`, now, now); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `
		INSERT INTO rule_versions(rule_id, version, definition, definition_sha256, created_at_us)
		VALUES ('rule-b', 'v1', '{}', ?, ?)`, strings.Repeat("2", 64), now); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `
		UPDATE rules SET active_version = 'v1' WHERE rule_id = 'rule-b'`); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `
		INSERT INTO rules(rule_id, enabled, created_at_us, updated_at_us)
		VALUES ('rule-c', 1, ?, ?)`, now, now); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `
		INSERT INTO rule_versions(rule_id, version, definition, definition_sha256, created_at_us)
		VALUES ('rule-c', 'v1', '{}', ?, ?)`, strings.Repeat("3", 64), now); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `
		UPDATE rules SET active_version = 'v1' WHERE rule_id = 'rule-c'`); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	connection.Close()
	delivery := testDelivery(t, 1)
	ledger := detection.NewLedger()
	pipeline := NewPipeline(
		planNodeID,
		&mutablePlanCatalog{
			parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
			rules: []RuleSnapshot{
				{RuleID: "rule-1", Version: "v1"},
				{RuleID: "rule-b", Version: "v1"},
				{RuleID: "rule-c", Version: "v1"},
			},
		},
		statelessParserRunner{},
		&middlePermanentRuleEvaluator{},
		ledger,
	)
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
	if _, err := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), pipeline).Process(context.Background(), delivery); err != nil {
		t.Fatalf("Process(): %v", err)
	}
	receipt, found, err := database.FindProcessingReceipt(context.Background(), delivery.ID)
	if err != nil || !found {
		t.Fatalf("FindProcessingReceipt() = %+v,%v,%v", receipt, found, err)
	}
	completedAt := delivery.Record.ObservedAt.Add(time.Second)
	if receipt.Kind != core.ReceiptRecordPermanent || receipt.Failure == nil ||
		receipt.Failure.Stage != "detection" || receipt.Failure.Code != "invalid_rule_input" ||
		receipt.Failure.SanitizedError != "rule input is invalid" ||
		receipt.Failure.Action != "skip_rule" || receipt.Failure.OccurredAt != completedAt {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	connection = openSQLiteTestConnection(t, path)
	defer connection.Close()
	rows, err := connection.QueryContext(context.Background(), `
		SELECT rule_id, kind, coalesce(failure_code, '')
		FROM detection_terminal_outcomes ORDER BY rule_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var outcomes []string
	for rows.Next() {
		var ruleID, kind, code string
		if err := rows.Scan(&ruleID, &kind, &code); err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, ruleID+":"+kind+":"+code)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantOutcomes := []string{
		"rule-1:success:",
		"rule-b:record_permanent:invalid_rule_input",
		"rule-c:success:",
	}
	if strings.Join(outcomes, "|") != strings.Join(wantOutcomes, "|") {
		t.Fatalf("terminal outcomes = %v, want %v", outcomes, wantOutcomes)
	}
	for query, want := range map[string]int{
		"SELECT count(*) FROM detection_contributions WHERE rule_id = 'rule-1'": 1,
		"SELECT count(*) FROM detection_contributions WHERE rule_id = 'rule-b'": 0,
		"SELECT count(*) FROM detection_contributions WHERE rule_id = 'rule-c'": 1,
		"SELECT count(*) FROM audit_logs WHERE action = 'record_permanent'":     1,
	} {
		var count int
		if err := connection.QueryRowContext(context.Background(), query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("query %q count = %d, want %d", query, count, want)
		}
	}
	var boundAudit int
	if err := connection.QueryRowContext(context.Background(), `
		SELECT count(*) FROM audit_logs
		WHERE critical = 1 AND category = 'processing' AND action = 'record_permanent'
			AND result = 'rejected' AND delivery_id = ? AND error_code = 'invalid_rule_input'
			AND created_at_us = ?
			AND json_extract(details_json, '$.event_id') IS NOT NULL
			AND json_extract(details_json, '$.rule_id') = 'rule-b'
			AND json_extract(details_json, '$.rule_version') = 'v1'
			AND json_extract(details_json, '$.action') = 'skip_rule'`,
		string(delivery.ID), completedAt.UnixMicro()).Scan(&boundAudit); err != nil {
		t.Fatal(err)
	}
	if boundAudit != 1 {
		t.Fatalf("bound detection poison audit count = %d, want 1", boundAudit)
	}
	for _, ruleID := range []core.RuleID{"rule-1", "rule-b", "rule-c"} {
		snapshot, err := ledger.Snapshot(context.Background(), detection.WindowKey{
			RuleID: ruleID, RuleVersion: "v1", GroupKey: "group-" + string(ruleID),
		})
		if err != nil {
			t.Fatal(err)
		}
		want := uint64(1)
		if ruleID == "rule-b" {
			want = 0
		}
		if snapshot.Count != want {
			t.Fatalf("%s Window count = %d, want %d", ruleID, snapshot.Count, want)
		}
	}
}

func TestSQLiteAutomaticSuppressionReceiptFailureRollsBackAndRetryAppliesOnce(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	first := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_000_000, 0).UTC())
	duplicate := sqliteDeliveryAt(t, 10, 20, time.Unix(1_700_000_060, 0).UTC())
	ledger := detection.NewLedger()
	if _, err := NewCoordinator(
		newEnforcingSQLiteStoreAdapter(t, database), automaticPipeline(first, "v1", ledger),
	).Process(context.Background(), first); err != nil {
		t.Fatalf("seed Process(): %v", err)
	}
	failing := &receiptFailingSQLiteStore{adapter: newEnforcingSQLiteStoreAdapter(t, database)}
	if _, err := NewCoordinator(
		failing, automaticPipeline(duplicate, "v1", ledger),
	).Process(context.Background(), duplicate); !errors.Is(err, errInjected) {
		t.Fatalf("duplicate Process() error = %v, want injected receipt failure", err)
	}
	assertAutomaticSuppressionState(t, path, first, duplicate, 0, 1)
	snapshot, err := ledger.Snapshot(context.Background(), detection.WindowKey{
		RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Count != 1 {
		t.Fatalf("Window after rollback = %d, want 1", snapshot.Count)
	}
	if _, err := NewCoordinator(
		newEnforcingSQLiteStoreAdapter(t, database), automaticPipeline(duplicate, "v1", ledger),
	).Process(context.Background(), duplicate); err != nil {
		t.Fatalf("retry Process(): %v", err)
	}
	assertAutomaticSuppressionState(t, path, first, duplicate, 1, 2)
	snapshot, err = ledger.Snapshot(context.Background(), detection.WindowKey{
		RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Count != 2 {
		t.Fatalf("Window after retry = %d, want 2", snapshot.Count)
	}
}

func TestSQLiteRulePermanentReceiptFailureRollsBackAndRetryAppliesOnce(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	seedSQLiteRule(t, path, "rule-b", "v1", "4")
	delivery := testDelivery(t, 1)
	ledger := detection.NewLedger()
	newPipeline := func() *Pipeline {
		pipeline := NewPipeline(
			planNodeID,
			&mutablePlanCatalog{
				parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
				rules: []RuleSnapshot{
					{RuleID: "rule-1", Version: "v1"},
					{RuleID: "rule-b", Version: "v1"},
				},
			},
			statelessParserRunner{}, &middlePermanentRuleEvaluator{}, ledger,
		)
		pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
		return pipeline
	}
	if _, err := NewCoordinator(
		&receiptFailingSQLiteStore{adapter: newEnforcingSQLiteStoreAdapter(t, database)}, newPipeline(),
	).Process(context.Background(), delivery); !errors.Is(err, errInjected) {
		t.Fatalf("Process() error = %v, want injected receipt failure", err)
	}
	connection := openSQLiteTestConnection(t, path)
	for _, table := range []string{
		"parser_terminal_outcomes", "detection_terminal_outcomes", "detection_contributions",
		"audit_logs", "processing_receipts",
	} {
		var count int
		if err := connection.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			connection.Close()
			t.Fatal(err)
		}
		if count != 0 {
			connection.Close()
			t.Fatalf("%s count after rollback = %d, want 0", table, count)
		}
	}
	connection.Close()
	for _, ruleID := range []core.RuleID{"rule-1", "rule-b"} {
		snapshot, err := ledger.Snapshot(context.Background(), detection.WindowKey{
			RuleID: ruleID, RuleVersion: "v1", GroupKey: "group-" + string(ruleID),
		})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Count != 0 {
			t.Fatalf("%s Window after rollback = %d, want 0", ruleID, snapshot.Count)
		}
	}
	if _, err := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), newPipeline()).
		Process(context.Background(), delivery); err != nil {
		t.Fatalf("retry Process(): %v", err)
	}
	connection = openSQLiteTestConnection(t, path)
	defer connection.Close()
	for table, want := range map[string]int{
		"parser_terminal_outcomes":    1,
		"detection_terminal_outcomes": 2,
		"detection_contributions":     1,
		"audit_logs":                  1,
		"processing_receipts":         1,
	} {
		var count int
		if err := connection.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count after retry = %d, want %d", table, count, want)
		}
	}
}

func TestSQLiteAutomaticCandidateIDConflictDoesNotSuppressAnotherDecision(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	first := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_000_000, 0).UTC())
	duplicate := sqliteDeliveryAt(t, 10, 20, time.Unix(1_700_000_060, 0).UTC())
	ledger := detection.NewLedger()
	if _, err := NewCoordinator(
		newEnforcingSQLiteStoreAdapter(t, database), automaticPipeline(first, "v1", ledger),
	).Process(context.Background(), first); err != nil {
		t.Fatalf("seed Process(): %v", err)
	}
	duplicateEventID, err := core.SecurityEventID(planNodeID, duplicate.ID, "parser-1", "v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	conflictingID := "decision-" + string(duplicateEventID)
	connection := openSQLiteTestConnection(t, path)
	seededAt := first.Record.ObservedAt.Add(2 * time.Second).UnixMicro()
	if _, err := connection.ExecContext(context.Background(), `
		INSERT INTO decisions(
			decision_id, node_id, source, canonical_target, created_at_us, updated_at_us,
			last_triggered_at_us, state, suppressed_count
		) VALUES (?, ?, 'manual', '198.51.100.5/32', ?, ?, ?, 'active', 0)`,
		conflictingID, string(planNodeID), seededAt, seededAt, seededAt); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	connection.Close()
	if _, err := NewCoordinator(
		newEnforcingSQLiteStoreAdapter(t, database), automaticPipeline(duplicate, "v1", ledger),
	).Process(context.Background(), duplicate); !errors.Is(err, decision.ErrDecisionIDConflict) {
		t.Fatalf("duplicate Process() error = %v, want DecisionID conflict", err)
	}
	connection = openSQLiteTestConnection(t, path)
	defer connection.Close()
	var suppressed int64
	if err := connection.QueryRowContext(context.Background(), `
		SELECT suppressed_count FROM decisions WHERE source = 'automatic'`).Scan(&suppressed); err != nil {
		t.Fatal(err)
	}
	if suppressed != 0 {
		t.Fatalf("conflicting candidate changed suppression to %d", suppressed)
	}
	for table, want := range map[string]int{
		"alerts":                      1,
		"detection_contributions":     1,
		"detection_terminal_outcomes": 1,
		"audit_logs":                  1,
		"processing_receipts":         1,
	} {
		var count int
		if err := connection.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
	snapshot, err := ledger.Snapshot(context.Background(), detection.WindowKey{
		RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Count != 1 {
		t.Fatalf("Window after conflict = %d, want 1", snapshot.Count)
	}
}

func TestSQLiteConcurrentAutomaticDuplicatesAllSuppressSuccessfully(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	base := time.Unix(1_700_000_000, 0).UTC()
	first := sqliteDeliveryAt(t, 0, 10, base)
	ledger := detection.NewLedger()
	if _, err := NewCoordinator(
		newEnforcingSQLiteStoreAdapter(t, database), automaticPipeline(first, "v1", ledger),
	).Process(context.Background(), first); err != nil {
		t.Fatalf("seed Process(): %v", err)
	}
	const duplicateCount = 4
	deliveries := make([]core.Delivery, 0, duplicateCount)
	for index := 0; index < duplicateCount; index++ {
		start := uint64((index + 1) * 10)
		deliveries = append(deliveries, sqliteDeliveryAt(
			t, start, start+10, base.Add(time.Duration(index+1)*time.Minute),
		))
	}
	errorsByAttempt := make(chan error, duplicateCount)
	barrierStore := newBeginBarrierSQLiteStore(newEnforcingSQLiteStoreAdapter(t, database), duplicateCount)
	var wait sync.WaitGroup
	for index, delivery := range deliveries {
		index := index
		delivery := delivery
		wait.Add(1)
		go func() {
			defer wait.Done()
			pipeline := automaticPipeline(delivery, "v1", ledger)
			pipeline.rules = automaticRuleEvaluator{groupKey: "concurrent-group-" + string(rune('a'+index))}
			_, err := NewCoordinator(
				barrierStore, pipeline,
			).Process(context.Background(), delivery)
			errorsByAttempt <- err
		}()
	}
	wait.Wait()
	close(errorsByAttempt)
	for err := range errorsByAttempt {
		if err != nil {
			t.Fatalf("concurrent duplicate Process(): %v", err)
		}
	}
	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	var decisions, suppressed, lastTriggered, revision int64
	if err := connection.QueryRowContext(context.Background(), `
		SELECT count(*), max(suppressed_count), max(last_triggered_at_us)
		FROM decisions WHERE source = 'automatic' AND state = 'active'`).Scan(
		&decisions, &suppressed, &lastTriggered,
	); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRowContext(context.Background(), `
		SELECT target_projection_revision FROM desired_ban_projections`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	wantLast := deliveries[len(deliveries)-1].Record.ObservedAt.Add(time.Second).UnixMicro()
	if decisions != 1 || suppressed != duplicateCount || lastTriggered != wantLast || revision != 1 {
		t.Fatalf("concurrent state = decisions:%d suppressed:%d last:%d revision:%d",
			decisions, suppressed, lastTriggered, revision)
	}
	firstEventID, err := core.SecurityEventID(planNodeID, first.ID, "parser-1", "v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var decisionID, ruleVersion, alertID string
	var expiresAt, updatedAt int64
	if err := connection.QueryRowContext(context.Background(), `
		SELECT decision_id, rule_version, alert_id, expires_at_us, updated_at_us
		FROM decisions WHERE source = 'automatic'`).Scan(
		&decisionID, &ruleVersion, &alertID, &expiresAt, &updatedAt,
	); err != nil {
		t.Fatal(err)
	}
	firstTriggeredAt := first.Record.ObservedAt.Add(time.Second)
	if decisionID != "decision-"+string(firstEventID) || ruleVersion != "v1" ||
		alertID != "alert-"+string(firstEventID) ||
		expiresAt != firstTriggeredAt.Add(10*time.Minute).UnixMicro() ||
		updatedAt != firstTriggeredAt.UnixMicro() {
		t.Fatalf("concurrent duplicate replaced frozen Decision fields")
	}
	for table, want := range map[string]int{
		"alerts":                  duplicateCount + 1,
		"detection_contributions": duplicateCount + 1,
		"processing_receipts":     duplicateCount + 1,
		"audit_logs":              duplicateCount + 1,
	} {
		var count int
		if err := connection.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
	groups := []string{"group-a", "concurrent-group-a", "concurrent-group-b", "concurrent-group-c", "concurrent-group-d"}
	for _, groupKey := range groups {
		snapshot, err := ledger.Snapshot(context.Background(), detection.WindowKey{
			RuleID: "rule-1", RuleVersion: "v1", GroupKey: groupKey,
		})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Count != 1 {
			t.Fatalf("Window %q count = %d, want 1", groupKey, snapshot.Count)
		}
	}
}

func TestSQLiteAutomaticDuplicateCommitUnknownAndReplay(t *testing.T) {
	t.Run("commit persisted", func(t *testing.T) {
		database, path := openSQLiteProcessingStore(t)
		seedSQLiteProcessingCatalog(t, path)
		first := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_000_000, 0).UTC())
		duplicate := sqliteDeliveryAt(t, 10, 20, time.Unix(1_700_000_060, 0).UTC())
		ledger := detection.NewLedger()
		if _, err := NewCoordinator(
			newEnforcingSQLiteStoreAdapter(t, database), automaticPipeline(first, "v1", ledger),
		).Process(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		adapter := newEnforcingSQLiteStoreAdapter(t, database)
		adapter.commit = func(unit *store.UnitOfWork) error {
			if err := unit.Commit(); err != nil {
				return err
			}
			return errors.New("commit result lost")
		}
		if _, err := NewCoordinator(adapter, automaticPipeline(duplicate, "v1", ledger)).
			Process(context.Background(), duplicate); err != nil {
			t.Fatalf("unknown commit readback: %v", err)
		}
		assertAutomaticSuppressionState(t, path, first, duplicate, 1, 2)
		replayLedger := detection.NewLedger()
		if _, err := NewCoordinator(
			newEnforcingSQLiteStoreAdapter(t, database), automaticPipeline(duplicate, "v1", replayLedger),
		).Process(context.Background(), duplicate); err != nil {
			t.Fatalf("receipt replay: %v", err)
		}
		assertAutomaticSuppressionState(t, path, first, duplicate, 1, 2)
		snapshot, err := replayLedger.Snapshot(context.Background(), detection.WindowKey{
			RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a",
		})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Count != 0 {
			t.Fatalf("replay entered fresh Window: %d", snapshot.Count)
		}
	})

	t.Run("commit rolled back", func(t *testing.T) {
		database, path := openSQLiteProcessingStore(t)
		seedSQLiteProcessingCatalog(t, path)
		first := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_000_000, 0).UTC())
		duplicate := sqliteDeliveryAt(t, 10, 20, time.Unix(1_700_000_060, 0).UTC())
		ledger := detection.NewLedger()
		if _, err := NewCoordinator(
			newEnforcingSQLiteStoreAdapter(t, database), automaticPipeline(first, "v1", ledger),
		).Process(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		adapter := newEnforcingSQLiteStoreAdapter(t, database)
		adapter.commit = func(unit *store.UnitOfWork) error {
			if err := unit.Rollback(); err != nil {
				return err
			}
			return errors.New("commit outcome unknown after rollback")
		}
		if _, err := NewCoordinator(adapter, automaticPipeline(duplicate, "v1", ledger)).
			Process(context.Background(), duplicate); !errors.Is(err, ErrCommitUnknown) {
			t.Fatalf("unknown rollback error = %v", err)
		}
		assertAutomaticSuppressionState(t, path, first, duplicate, 0, 1)
		if _, err := NewCoordinator(
			newEnforcingSQLiteStoreAdapter(t, database), automaticPipeline(duplicate, "v1", ledger),
		).Process(context.Background(), duplicate); err != nil {
			t.Fatalf("retry after unknown rollback: %v", err)
		}
		assertAutomaticSuppressionState(t, path, first, duplicate, 1, 2)
	})
}

type receiptFailingSQLiteStore struct {
	adapter *SQLiteStoreAdapter
}

type beginBarrierSQLiteStore struct {
	adapter *SQLiteStoreAdapter
	total   int
	ready   chan struct{}
	mu      sync.Mutex
	arrived int
}

func newBeginBarrierSQLiteStore(adapter *SQLiteStoreAdapter, total int) *beginBarrierSQLiteStore {
	return &beginBarrierSQLiteStore{adapter: adapter, total: total, ready: make(chan struct{})}
}

func (s *beginBarrierSQLiteStore) findReceipt(
	ctx context.Context,
	id core.DeliveryID,
) (core.ProcessingReceipt, bool, error) {
	return s.adapter.findReceipt(ctx, id)
}

func (s *beginBarrierSQLiteStore) notifyReceiptReplay(ctx context.Context) error {
	return s.adapter.notifyReceiptReplay(ctx)
}

func (s *beginBarrierSQLiteStore) beginProcessing(ctx context.Context) (processingUnitOfWork, error) {
	s.mu.Lock()
	s.arrived++
	if s.arrived == s.total {
		close(s.ready)
	}
	s.mu.Unlock()
	select {
	case <-s.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.adapter.beginProcessing(ctx)
}

func (s *receiptFailingSQLiteStore) findReceipt(
	ctx context.Context,
	id core.DeliveryID,
) (core.ProcessingReceipt, bool, error) {
	return s.adapter.findReceipt(ctx, id)
}

func (s *receiptFailingSQLiteStore) notifyReceiptReplay(ctx context.Context) error {
	return s.adapter.notifyReceiptReplay(ctx)
}

func (s *receiptFailingSQLiteStore) beginProcessing(ctx context.Context) (processingUnitOfWork, error) {
	unit, err := s.adapter.beginProcessing(ctx)
	if err != nil {
		return nil, err
	}
	return &receiptFailingUnitOfWork{processingUnitOfWork: unit}, nil
}

type receiptFailingUnitOfWork struct {
	processingUnitOfWork
}

func (*receiptFailingUnitOfWork) writeReceipt(context.Context, core.ProcessingReceipt) error {
	return errInjected
}

func automaticPipeline(
	delivery core.Delivery,
	ruleVersion core.RuleVersion,
	ledger *detection.Ledger,
) *Pipeline {
	pipeline := NewPipeline(
		planNodeID,
		&mutablePlanCatalog{
			parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
			rules:   []RuleSnapshot{{RuleID: "rule-1", Version: ruleVersion}},
		},
		statelessParserRunner{}, automaticRuleEvaluator{}, ledger,
	)
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
	return pipeline
}

func assertAutomaticSuppressionState(
	t *testing.T,
	path string,
	first core.Delivery,
	duplicate core.Delivery,
	wantSuppressed int64,
	wantCommitted int,
) {
	t.Helper()
	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	var suppressed, lastTriggered, expiresAt, updatedAt int64
	if err := connection.QueryRowContext(context.Background(), `
		SELECT suppressed_count, last_triggered_at_us, expires_at_us, updated_at_us
		FROM decisions WHERE source = 'automatic'`).Scan(
		&suppressed, &lastTriggered, &expiresAt, &updatedAt,
	); err != nil {
		t.Fatal(err)
	}
	firstTriggeredAt := first.Record.ObservedAt.Add(time.Second)
	wantLast := firstTriggeredAt
	if wantSuppressed > 0 {
		wantLast = duplicate.Record.ObservedAt.Add(time.Second)
	}
	if suppressed != wantSuppressed || lastTriggered != wantLast.UnixMicro() ||
		expiresAt != firstTriggeredAt.Add(10*time.Minute).UnixMicro() ||
		updatedAt != firstTriggeredAt.UnixMicro() {
		t.Fatalf("automatic state = suppressed:%d last:%d expires:%d updated:%d",
			suppressed, lastTriggered, expiresAt, updatedAt)
	}
	for table, want := range map[string]int{
		"alerts":                      wantCommitted,
		"detection_contributions":     wantCommitted,
		"detection_terminal_outcomes": wantCommitted,
		"processing_receipts":         wantCommitted,
		"audit_logs":                  wantCommitted,
		"desired_ban_projections":     1,
	} {
		var count int
		if err := connection.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
	var revision, projectionUpdatedAt int64
	if err := connection.QueryRowContext(context.Background(), `
		SELECT target_projection_revision, updated_at_us FROM desired_ban_projections`).Scan(
		&revision, &projectionUpdatedAt,
	); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || projectionUpdatedAt != firstTriggeredAt.UnixMicro() {
		t.Fatalf("projection changed: revision=%d updated=%d", revision, projectionUpdatedAt)
	}
}

type automaticRuleEvaluator struct {
	groupKey string
}

func (e automaticRuleEvaluator) Match(context.Context, RuleSnapshot, core.SecurityEvent) (RuleMatch, error) {
	groupKey := e.groupKey
	if groupKey == "" {
		groupKey = "group-a"
	}
	return RuleMatch{Applicable: true, GroupKey: groupKey, DistinctKey: "alice"}, nil
}

func (automaticRuleEvaluator) Evaluate(
	_ context.Context,
	rule RuleSnapshot,
	event core.SecurityEvent,
	_ detection.Snapshot,
) (DetectionEffects, error) {
	file, ok := event.SourcePosition.File()
	if !ok {
		return DetectionEffects{}, errors.New("expected file source position")
	}
	deliveryID, err := core.FileDeliveryID(event.SourceID, file)
	if err != nil {
		return DetectionEffects{}, err
	}
	createdAt := event.ObservedAt.Add(time.Second)
	expiresAt := createdAt.Add(10 * time.Minute)
	if file.StartOffset > 0 {
		expiresAt = createdAt.Add(2 * time.Hour)
	}
	target := netip.MustParsePrefix("192.0.2.10/32")
	alertID := core.AlertID("alert-" + string(event.ID))
	alert := core.Alert{
		ID: alertID, NodeID: event.NodeID, EventID: event.ID,
		RuleID: rule.RuleID, RuleVersion: rule.Version, CanonicalTarget: target,
		ObservedAt: event.ObservedAt, CreatedAt: createdAt,
	}
	ruleVersion := rule.Version
	request := decision.AutomaticRequest{
		DecisionID: core.DecisionID("decision-" + string(event.ID)), DeliveryID: deliveryID,
		EventID: event.ID, NodeID: event.NodeID, RuleID: rule.RuleID,
		RuleVersion: &ruleVersion, AlertID: &alertID, Target: target,
		TriggeredAt: createdAt, ExpiresAt: &expiresAt,
	}
	return DetectionEffects{Alert: &alert, AutomaticDecision: &request}, nil
}

func sqliteDeliveryAt(t *testing.T, start, end uint64, observedAt time.Time) core.Delivery {
	t.Helper()
	file := core.FilePosition{
		Generation: "00112233445566778899aabbccddeeff", DeviceID: 1, Inode: 2,
		StartOffset: start, EndOffset: end,
	}
	position, err := core.NewFilePosition(file)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := core.FileDeliveryID("source-1", file)
	if err != nil {
		t.Fatal(err)
	}
	return core.Delivery{
		ID: deliveryID, Sequence: core.DeliverySequence(start/10 + 1),
		Record: core.RawRecord{
			SourceID: "source-1", ObservedAt: observedAt, Position: position, Content: []byte("failed login"),
		},
	}
}

func TestSQLitePipelineCommitUnknownReadbackConfirmsWindowOnce(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	delivery := testDelivery(t, 1)
	ledger := detection.NewLedger()
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-1", Version: "v1"}},
	}
	parsers := &scriptedParserRunner{events: map[core.ParserID][]core.EventFields{
		"parser-1": {{EventType: "auth.login_failed"}},
	}}
	evaluator := &scriptedRuleEvaluator{match: RuleMatch{
		Applicable: true, GroupKey: "group-a", DistinctKey: "alice",
	}}
	pipeline := NewPipeline(planNodeID, catalog, parsers, evaluator, ledger)
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }
	adapter := newEnforcingSQLiteStoreAdapter(t, database)
	adapter.commit = func(unit *store.UnitOfWork) error {
		if err := unit.Commit(); err != nil {
			return err
		}
		return errors.New("injected connection loss after commit")
	}
	coordinator := NewCoordinator(adapter, pipeline)

	if _, err := coordinator.Process(context.Background(), delivery); err != nil {
		t.Fatalf("Process(): %v", err)
	}
	if _, err := coordinator.Process(context.Background(), delivery); err != nil {
		t.Fatalf("replay Process(): %v", err)
	}
	if evaluator.evaluateCalls != 1 {
		t.Fatalf("evaluate calls = %d, want 1", evaluator.evaluateCalls)
	}
	window, err := ledger.Snapshot(context.Background(), detection.WindowKey{
		RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a",
	})
	if err != nil || window != (detection.Snapshot{Count: 1, DistinctCount: 1}) {
		t.Fatalf("Window Snapshot() = %+v,%v", window, err)
	}
}

func TestSQLitePipelineAlertFailureRollsBackThenRetryCommitsOnce(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	firstDelivery := testDelivery(t, 1)
	secondDelivery := testSQLiteDeliveryAt(t, 2, 20, 30)

	ledger := detection.NewLedger()
	catalog := &mutablePlanCatalog{
		parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}},
		rules:   []RuleSnapshot{{RuleID: "rule-1", Version: "v1"}},
	}
	parsers := &scriptedParserRunner{events: map[core.ParserID][]core.EventFields{
		"parser-1": {{EventType: "auth.login_failed"}},
	}}
	alertID := core.AlertID("alert-conflict")
	evaluator := &scriptedRuleEvaluator{
		match: RuleMatch{Applicable: true, GroupKey: "group-a", DistinctKey: "alice"},
		effects: func(rule RuleSnapshot, event core.SecurityEvent, _ detection.Snapshot) DetectionEffects {
			alert := core.Alert{
				ID: alertID, NodeID: event.NodeID, EventID: event.ID,
				RuleID: rule.RuleID, RuleVersion: rule.Version,
				CanonicalTarget: netip.MustParsePrefix("192.0.2.10/32"),
				ObservedAt:      event.ObservedAt, CreatedAt: event.ObservedAt.Add(time.Second),
			}
			return DetectionEffects{Alert: &alert}
		},
	}
	pipeline := NewPipeline(planNodeID, catalog, parsers, evaluator, ledger)
	pipeline.clock = func() time.Time { return firstDelivery.Record.ObservedAt.Add(time.Second) }
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), pipeline)

	if _, err := coordinator.Process(context.Background(), firstDelivery); err != nil {
		t.Fatalf("baseline Process(): %v", err)
	}
	if _, err := coordinator.Process(context.Background(), secondDelivery); err == nil {
		t.Fatal("expected conflicting alert insert to fail")
	}
	assertSQLiteCounts(t, path, map[string]int{
		"parser_terminal_outcomes": 1, "detection_contributions": 1,
		"alerts": 1, "processing_receipts": 1,
	})
	window, err := ledger.Snapshot(context.Background(), detection.WindowKey{
		RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a",
	})
	if err != nil || window != (detection.Snapshot{Count: 1, DistinctCount: 1}) {
		t.Fatalf("failed Window Snapshot() = %+v,%v", window, err)
	}

	alertID = "alert-retry-success"
	if _, err := coordinator.Process(context.Background(), secondDelivery); err != nil {
		t.Fatalf("retry Process(): %v", err)
	}
	assertSQLiteCounts(t, path, map[string]int{
		"parser_terminal_outcomes": 2, "detection_contributions": 2,
		"alerts": 2, "processing_receipts": 2,
	})
	window, err = ledger.Snapshot(context.Background(), detection.WindowKey{
		RuleID: "rule-1", RuleVersion: "v1", GroupKey: "group-a",
	})
	if err != nil || window != (detection.Snapshot{Count: 2, DistinctCount: 1}) {
		t.Fatalf("retry Window Snapshot() = %+v,%v", window, err)
	}
}

func testSQLiteDeliveryAt(t *testing.T, sequence core.DeliverySequence, start, end uint64) core.Delivery {
	t.Helper()
	file := core.FilePosition{
		Generation: "00112233445566778899aabbccddeeff", DeviceID: 1, Inode: 2,
		StartOffset: start, EndOffset: end,
	}
	position, err := core.NewFilePosition(file)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := core.FileDeliveryID("source-1", file)
	if err != nil {
		t.Fatal(err)
	}
	return core.Delivery{
		ID: deliveryID, Sequence: sequence,
		Record: core.RawRecord{
			SourceID: "source-1", ObservedAt: time.Unix(1_700_000_000, 0).UTC(),
			Position: position, Content: []byte("record"),
		},
	}
}

func TestSQLitePipelineParserPoisonCommitsOutcomeAuditAndReceipt(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	delivery := testDelivery(t, 1)
	catalog := &mutablePlanCatalog{parsers: []ParserSnapshot{{ParserID: "parser-1", Version: "v1"}}}
	parsers := &scriptedParserRunner{failures: map[core.ParserID]error{
		"parser-1": &PlanFailure{
			Class: PlanFailureRecordPermanent, Code: "malformed_record",
			SanitizedError: "record rejected", Action: "terminal_reject",
			Cause: errors.New("secret parser cause"),
		},
	}}
	pipeline := NewPipeline(planNodeID, catalog, parsers, &scriptedRuleEvaluator{}, detection.NewLedger())
	pipeline.clock = func() time.Time { return delivery.Record.ObservedAt.Add(time.Second) }

	if _, err := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), pipeline).
		Process(context.Background(), delivery); err != nil {
		t.Fatalf("Process(): %v", err)
	}
	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	var parserKind, parserCode string
	if err := connection.QueryRow(`SELECT kind, failure_code FROM parser_terminal_outcomes`).
		Scan(&parserKind, &parserCode); err != nil {
		t.Fatal(err)
	}
	var receiptKind, stage, receiptCode, sanitized, action string
	if err := connection.QueryRow(`
		SELECT kind, failure_stage, failure_code, sanitized_error, terminal_action
		FROM processing_receipts`).Scan(&receiptKind, &stage, &receiptCode, &sanitized, &action); err != nil {
		t.Fatal(err)
	}
	var auditCode, details string
	if err := connection.QueryRow(`SELECT error_code, details_json FROM audit_logs`).
		Scan(&auditCode, &details); err != nil {
		t.Fatal(err)
	}
	if parserKind != "record_permanent" || parserCode != "malformed_record" ||
		receiptKind != "record_permanent" || stage != "parser" || receiptCode != parserCode ||
		sanitized != "record rejected" || action != "terminal_reject" || auditCode != parserCode {
		t.Fatalf("poison rows = parser:%s/%s receipt:%s/%s/%s/%s/%s audit:%s",
			parserKind, parserCode, receiptKind, stage, receiptCode, sanitized, action, auditCode)
	}
	if strings.Contains(details, "secret") || strings.Contains(sanitized, "secret") {
		t.Fatal("poison persistence leaked the internal parser cause")
	}
}

func assertSQLiteCounts(t *testing.T, path string, wants map[string]int) {
	t.Helper()
	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	for table, want := range wants {
		var got int
		if err := connection.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}

func newEnforcingSQLiteStoreAdapter(t *testing.T, database *store.Store) *SQLiteStoreAdapter {
	return newEnforcingSQLiteStoreAdapterWithWake(t, database, decision.TargetWakeSinkFunc(
		func(context.Context, core.NodeID, netip.Prefix) error { return nil },
	))
}

func newEnforcingSQLiteStoreAdapterWithWake(
	t *testing.T,
	database *store.Store,
	wake decision.TargetWakeSink,
) *SQLiteStoreAdapter {
	t.Helper()
	finalizer, err := decision.NewDesiredStateFinalizer(decision.TargetPolicyResolverFunc(
		func(context.Context, decision.DesiredStateTransaction, core.DesiredBanProjection) (enforcement.TargetPolicy, error) {
			return enforcement.TargetPolicy{
				Coverage: core.PolicyCoverageNone, Scopes: core.ScopeInput,
				NativeTimeoutSupported: true, BackendAttributesDigest: strings.Repeat("b", 64),
			}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewEnforcingSQLiteStoreAdapter(
		database,
		finalizer,
		wake,
	)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

type processorRecordingWakeSink struct {
	mu    sync.Mutex
	calls []netip.Prefix
}

type processorFailOnceWakeSink struct {
	mu      sync.Mutex
	calls   int
	failure error
}

func (s *processorFailOnceWakeSink) WakeTarget(
	context.Context,
	core.NodeID,
	netip.Prefix,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls == 1 {
		return s.failure
	}
	return nil
}

func (s *processorFailOnceWakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *processorRecordingWakeSink) WakeTarget(
	_ context.Context,
	_ core.NodeID,
	target netip.Prefix,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, target)
	return nil
}

func (s *processorRecordingWakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func openSQLiteProcessingStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guard.db")
	database, err := store.Open(context.Background(), path, processorMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	base := time.Unix(1_700_000_000, 0).UTC()
	nodeID := core.NodeID("00112233445566778899aabbccddeeff")
	if err := database.EnsureNodeIdentity(context.Background(), nodeID, base); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(context.Background(), "source-1", nodeID, store.SourceKindFile, base); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterFileGeneration(context.Background(), store.FileGeneration{
		SourceID: "source-1", Generation: "00112233445566778899aabbccddeeff",
		DeviceID: 1, Inode: 2, Path: "/var/log/guard.log", ObservedSize: 20, OpenedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	return database, path
}

func seedSQLiteProcessingCatalog(t *testing.T, path string) {
	t.Helper()
	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	now := time.Unix(1_700_000_000, 0).UTC().UnixMicro()
	hash := strings.Repeat("1", 64)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO parsers(parser_id, enabled, created_at_us, updated_at_us) VALUES (?, 1, ?, ?)`, []any{"parser-1", now, now}},
		{`INSERT INTO parser_versions(parser_id, version, definition, definition_sha256, created_at_us) VALUES (?, ?, '{}', ?, ?)`, []any{"parser-1", "v1", hash, now}},
		{`UPDATE parsers SET active_version = ? WHERE parser_id = ?`, []any{"v1", "parser-1"}},
		{`INSERT INTO rules(rule_id, enabled, created_at_us, updated_at_us) VALUES (?, 1, ?, ?)`, []any{"rule-1", now, now}},
		{`INSERT INTO rule_versions(rule_id, version, definition, definition_sha256, created_at_us) VALUES (?, ?, '{}', ?, ?)`, []any{"rule-1", "v1", hash, now}},
		{`UPDATE rules SET active_version = ? WHERE rule_id = ?`, []any{"v1", "rule-1"}},
	}
	for _, statement := range statements {
		if _, err := connection.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatalf("seed processing catalog: %v", err)
		}
	}
}

func seedSQLiteRule(t *testing.T, path, ruleID, version, hashCharacter string) {
	t.Helper()
	connection := openSQLiteTestConnection(t, path)
	defer connection.Close()
	now := time.Unix(1_700_000_000, 0).UTC().UnixMicro()
	if _, err := connection.ExecContext(context.Background(), `
		INSERT INTO rules(rule_id, enabled, created_at_us, updated_at_us)
		VALUES (?, 1, ?, ?)`, ruleID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `
		INSERT INTO rule_versions(rule_id, version, definition, definition_sha256, created_at_us)
		VALUES (?, ?, '{}', ?, ?)`, ruleID, version, strings.Repeat(hashCharacter, 64), now); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), `
		UPDATE rules SET active_version = ? WHERE rule_id = ?`, version, ruleID); err != nil {
		t.Fatal(err)
	}
}

func openSQLiteTestConnection(t *testing.T, path string) *sql.DB {
	t.Helper()
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	uriPath := filepath.ToSlash(absolutePath)
	if filepath.VolumeName(absolutePath) != "" {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "rwc")
	query.Set("_busy_timeout", "5000")
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "FULL")
	query.Add("_pragma", "wal_autocheckpoint(1000)")
	dsn.RawQuery = query.Encode()
	connector, err := sqlite.NewConnector(dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	connection := sql.OpenDB(connector)
	if err := connection.PingContext(context.Background()); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	return connection
}

func processorMigrationFS() fs.FS {
	return os.DirFS(filepath.Join("..", "..", "migrations"))
}
