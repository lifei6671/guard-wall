package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/enforcement"
)

func TestSQLiteManualCreateDuplicateAndReplaceAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	service := newDecisionLifecycleService(t, database)
	target := netip.MustParsePrefix("192.0.2.10/32")
	createdAt := time.Unix(1_000, 0).UTC()
	firstExpiry := createdAt.Add(time.Hour)

	created, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-1", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt, ExpiresAt: &firstExpiry,
	}, false)
	if err != nil {
		t.Fatalf("BanManual(create): %v", err)
	}
	if created.Replaced || created.Previous != nil || created.Current.ID != "manual-1" {
		t.Fatalf("created result = %+v", created)
	}

	duplicateExpiry := createdAt.Add(24 * time.Hour)
	_, err = service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-duplicate", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt.Add(time.Minute), ExpiresAt: &duplicateExpiry,
	}, false)
	if !errors.Is(err, decision.ErrAlreadyBanned) {
		t.Fatalf("BanManual(duplicate) error = %v", err)
	}
	var alreadyBanned *decision.AlreadyBannedError
	if !errors.As(err, &alreadyBanned) || alreadyBanned.DecisionID != "manual-1" {
		t.Fatalf("typed AlreadyBanned = %#v", alreadyBanned)
	}
	_, err = service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-1", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt, ExpiresAt: &firstExpiry,
	}, false)
	if !errors.Is(err, decision.ErrAlreadyBanned) {
		t.Fatalf("BanManual(same request replay) error = %v", err)
	}
	assertManualLifecycleState(t, database, "manual-1", "active", sql.NullString{}, firstExpiry, 1, 1)

	replacedAt := createdAt.Add(2 * time.Minute)
	replacementExpiry := createdAt.Add(2 * time.Hour)
	replaced, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-2", NodeID: testNodeID, Target: target,
		CreatedAt: replacedAt, ExpiresAt: &replacementExpiry,
	}, true)
	if err != nil {
		t.Fatalf("BanManual(replace): %v", err)
	}
	if !replaced.Replaced || replaced.Previous == nil || replaced.Previous.ID != "manual-1" ||
		replaced.Previous.State != core.DecisionRevoked || replaced.Previous.EndReason == nil ||
		*replaced.Previous.EndReason != core.EndReasonManualReplace || replaced.Current.ID != "manual-2" {
		t.Fatalf("replace result = %+v", replaced)
	}

	var activeManual, totalManual int
	if err := database.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE state = 'active'), count(*)
		FROM decisions WHERE source = 'manual'`).Scan(&activeManual, &totalManual); err != nil {
		t.Fatal(err)
	}
	if activeManual != 1 || totalManual != 2 {
		t.Fatalf("manual counts = active:%d total:%d", activeManual, totalManual)
	}
	var previousState, previousReason string
	if err := database.db.QueryRowContext(ctx, `
		SELECT state, end_reason FROM decisions WHERE decision_id = 'manual-1'`).Scan(
		&previousState, &previousReason); err != nil {
		t.Fatal(err)
	}
	if previousState != "revoked" || previousReason != "manual_replace" {
		t.Fatalf("previous lifecycle = %s/%s", previousState, previousReason)
	}
	assertManualLifecycleState(t, database, "manual-2", "active", sql.NullString{}, replacementExpiry, 2, 2)

	var replaceDetails string
	if err := database.db.QueryRowContext(ctx, `
		SELECT details_json FROM audit_logs WHERE action = 'manual_replace'`).Scan(&replaceDetails); err != nil {
		t.Fatal(err)
	}
	if replaceDetails != `{"replacement_decision_id":"manual-2"}` {
		t.Fatalf("replace audit details = %s", replaceDetails)
	}
}

func TestSQLiteManualReplaceAuditFailureRollsBackEverything(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	service := newDecisionLifecycleService(t, database)
	target := netip.MustParsePrefix("192.0.2.20/32")
	createdAt := time.Unix(2_000, 0).UTC()
	expiresAt := createdAt.Add(time.Hour)
	if _, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-old", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt, ExpiresAt: &expiresAt,
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `
		CREATE TRIGGER fail_manual_replace_audit
		BEFORE INSERT ON audit_logs WHEN NEW.action = 'manual_replace'
		BEGIN SELECT RAISE(ABORT, 'injected manual replace audit failure'); END`); err != nil {
		t.Fatal(err)
	}

	_, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-new", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt.Add(time.Minute), ExpiresAt: &expiresAt,
	}, true)
	if err == nil {
		t.Fatal("BanManual(replace) error = nil")
	}
	assertManualLifecycleState(t, database, "manual-old", "active", sql.NullString{}, expiresAt, 1, 1)
	var replacementCount int
	if err := database.db.QueryRowContext(ctx,
		"SELECT count(*) FROM decisions WHERE decision_id = 'manual-new'").Scan(&replacementCount); err != nil {
		t.Fatal(err)
	}
	if replacementCount != 0 {
		t.Fatal("replacement Decision survived audit rollback")
	}
}

func TestSQLiteManualConcurrentCreateReturnsTypedDuplicates(t *testing.T) {
	database := openTestStore(t)
	database.db.SetMaxOpenConns(8)
	database.db.SetMaxIdleConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seedNodeAndRule(t, ctx, database)
	service := newDecisionLifecycleService(t, database)
	target := netip.MustParsePrefix("192.0.2.30/32")
	createdAt := time.Unix(2_500, 0).UTC()

	const contenders = 8
	start := make(chan struct{})
	results := make(chan error, contenders)
	var workers sync.WaitGroup
	for index := 0; index < contenders; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			_, err := service.BanManual(ctx, decision.ManualRequest{
				DecisionID: core.DecisionID("manual-concurrent-" + string(rune('a'+index))),
				NodeID:     testNodeID, Target: target, CreatedAt: createdAt,
			}, false)
			results <- err
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)

	succeeded, duplicates := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, decision.ErrAlreadyBanned):
			duplicates++
		default:
			t.Fatalf("concurrent BanManual() error = %v", err)
		}
	}
	if succeeded != 1 || duplicates != contenders-1 {
		t.Fatalf("concurrent results = success:%d duplicate:%d", succeeded, duplicates)
	}
	var active, total, audits, revision int
	if err := database.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE state = 'active'), count(*)
		FROM decisions WHERE source = 'manual'`).Scan(&active, &total); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `
		SELECT target_projection_revision FROM desired_ban_projections`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if active != 1 || total != 1 || audits != 1 || revision != 1 {
		t.Fatalf("concurrent durable state = active:%d total:%d audits:%d revision:%d", active, total, audits, revision)
	}
}

func TestSQLiteManualConcurrentReplacePreservesOneActiveHistory(t *testing.T) {
	database := openTestStore(t)
	database.db.SetMaxOpenConns(8)
	database.db.SetMaxIdleConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seedNodeAndRule(t, ctx, database)
	service := newDecisionLifecycleService(t, database)
	target := netip.MustParsePrefix("192.0.2.31/32")
	createdAt := time.Unix(2_600, 0).UTC()
	if _, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-replace-root", NodeID: testNodeID, Target: target, CreatedAt: createdAt,
	}, false); err != nil {
		t.Fatal(err)
	}

	const contenders = 6
	start := make(chan struct{})
	results := make(chan error, contenders)
	var workers sync.WaitGroup
	for index := 0; index < contenders; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			_, err := service.BanManual(ctx, decision.ManualRequest{
				DecisionID: core.DecisionID("manual-replacement-" + string(rune('a'+index))),
				NodeID:     testNodeID, Target: target, CreatedAt: createdAt.Add(time.Minute),
			}, true)
			results <- err
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent replace error = %v", err)
		}
	}

	var active, revoked, total, replaceAudits, revision int
	if err := database.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE state = 'active'),
			count(*) FILTER (WHERE state = 'revoked' AND end_reason = 'manual_replace'), count(*)
		FROM decisions WHERE source = 'manual'`).Scan(&active, &revoked, &total); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx,
		"SELECT count(*) FROM audit_logs WHERE action = 'manual_replace'").Scan(&replaceAudits); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx,
		"SELECT target_projection_revision FROM desired_ban_projections").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if active != 1 || revoked != contenders || total != contenders+1 ||
		replaceAudits != contenders || revision != contenders+1 {
		t.Fatalf("replace history = active:%d revoked:%d total:%d audits:%d revision:%d",
			active, revoked, total, replaceAudits, revision)
	}
	rows, err := database.db.QueryContext(ctx, `
		SELECT decision_id, json_extract(details_json, '$.replacement_decision_id')
		FROM audit_logs WHERE action = 'manual_replace'`)
	if err != nil {
		t.Fatal(err)
	}
	links := make(map[string]string, contenders)
	for rows.Next() {
		var previous, replacement string
		if err := rows.Scan(&previous, &replacement); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		links[previous] = replacement
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	current := "manual-replace-root"
	visited := make(map[string]struct{}, contenders+1)
	for steps := 0; steps < contenders; steps++ {
		if _, duplicate := visited[current]; duplicate {
			t.Fatalf("replace audit chain contains cycle at %s", current)
		}
		visited[current] = struct{}{}
		next, exists := links[current]
		if !exists || next == "" {
			t.Fatalf("replace audit chain breaks at %s: %+v", current, links)
		}
		current = next
	}
	if _, duplicate := visited[current]; duplicate || len(visited) != contenders {
		t.Fatalf("replace audit chain does not cover history: current=%s visited=%+v", current, visited)
	}
	var activeID string
	if err := database.db.QueryRowContext(ctx, `
		SELECT decision_id FROM decisions WHERE source = 'manual' AND state = 'active'`).Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	if activeID != current {
		t.Fatalf("replace audit chain ends at %s, active Decision is %s", current, activeID)
	}
}

func TestSQLiteManualGlobalDecisionIDConflictHasNoSideEffects(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	service := newDecisionLifecycleService(t, database)
	createdAt := time.Unix(2_700, 0).UTC()
	ruleID := core.RuleID("rule-1")
	ruleVersion := core.RuleVersion("v1")
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := uow.PutDecision(ctx, core.Decision{
		ID: "global-conflict", NodeID: testNodeID, Source: core.DecisionSourceAutomatic,
		RuleID: &ruleID, RuleVersion: &ruleVersion,
		CanonicalTarget: netip.MustParsePrefix("198.51.100.50/32"),
		CreatedAt:       createdAt, UpdatedAt: createdAt, LastTriggeredAt: createdAt,
		State: core.DecisionActive,
	}); err != nil {
		t.Fatal(err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatal(err)
	}

	_, err = service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "global-conflict", NodeID: testNodeID,
		Target: netip.MustParsePrefix("203.0.113.50/32"), CreatedAt: createdAt,
	}, false)
	if !errors.Is(err, decision.ErrDecisionIDConflict) {
		t.Fatalf("BanManual(global ID conflict) error = %v", err)
	}
	var decisions, projections, audits int
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM decisions").Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM desired_ban_projections").Scan(&projections); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if decisions != 1 || projections != 0 || audits != 0 {
		t.Fatalf("global ID conflict side effects = decisions:%d projections:%d audits:%d", decisions, projections, audits)
	}
}

func TestSQLiteExpiryBatchRebuildsEachTargetOnceAndIsIdempotent(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	service := newDecisionLifecycleService(t, database)
	target := netip.MustParsePrefix("198.51.100.10/32")
	createdAt := time.Unix(3_000, 0).UTC()
	automaticExpiry := createdAt.Add(time.Minute)
	manualExpiry := createdAt.Add(2 * time.Minute)
	if _, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-expiry", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt, ExpiresAt: &manualExpiry,
	}, false); err != nil {
		t.Fatal(err)
	}
	seedAutomaticDecisionAndProjection(t, database, target, createdAt, automaticExpiry, manualExpiry)
	if _, err := database.db.ExecContext(ctx, `
		UPDATE target_reconcile_state
		SET retry_epoch = 7, status = 'degraded', attempt_count = 6,
			last_attempt_at_us = ?, last_error_code = 'injected', updated_at_us = ?
		WHERE node_id = ? AND canonical_target = ?`,
		createdAt.UnixMicro(), createdAt.UnixMicro(), string(testNodeID), target.String()); err != nil {
		t.Fatal(err)
	}

	first, err := service.Expire(ctx, automaticExpiry)
	if err != nil {
		t.Fatalf("Expire(first): %v", err)
	}
	if len(first.Expired) != 1 || first.Expired[0].ID != "automatic-expiry" ||
		len(first.Projections) != 1 || first.Projections[0].Revision != 3 ||
		len(first.EnforcementChanges) != 0 ||
		first.Projections[0].ActiveCount != 1 || first.Projections[0].EffectiveUntil == nil ||
		!first.Projections[0].EffectiveUntil.Equal(manualExpiry) {
		t.Fatalf("first expiry result = %+v", first)
	}
	assertDecisionState(t, database, "automatic-expiry", "expired", "expired")
	assertDesiredTargetState(t, database, target, "present", 1, 1, "degraded", 7, 6)

	replay, err := service.Expire(ctx, automaticExpiry)
	if err != nil {
		t.Fatalf("Expire(replay): %v", err)
	}
	if len(replay.Expired) != 0 || len(replay.Projections) != 0 {
		t.Fatalf("replayed expiry changed state: %+v", replay)
	}
	assertProjectionAndAuditCounts(t, database, 3, 2)

	last, err := service.Expire(ctx, manualExpiry)
	if err != nil {
		t.Fatalf("Expire(last): %v", err)
	}
	if len(last.Expired) != 1 || last.Expired[0].ID != "manual-expiry" ||
		len(last.Projections) != 1 || last.Projections[0].Revision != 4 ||
		last.Projections[0].State != core.BanProjectionAbsent ||
		len(last.EnforcementChanges) != 1 || last.EnforcementChanges[0].Generation != 2 ||
		last.EnforcementChanges[0].SnapshotRevision != 2 {
		t.Fatalf("last expiry result = %+v", last)
	}
	assertProjectionAndAuditCounts(t, database, 4, 3)
	assertDesiredTargetState(t, database, target, "absent", 2, 2, "pending", 7, 0)
	var relationDigest string
	if err := database.db.QueryRowContext(ctx, `
		SELECT policy_relation_digest FROM enforcement_states
		WHERE node_id = ? AND canonical_target = ?`, string(testNodeID), target.String()).Scan(&relationDigest); err != nil {
		t.Fatal(err)
	}
	if relationDigest != "" {
		t.Fatalf("absent relation digest = %q, want empty", relationDigest)
	}
}

func TestLifecycleServiceWakesOnlyChangedTargetsAfterConfirmedCommit(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	sink := &recordingTargetWakeSink{}
	service, err := decision.NewLifecycleService(database, newTestDesiredStateFinalizer(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	target := netip.MustParsePrefix("198.51.100.70/32")
	createdAt := time.Unix(3_500, 0).UTC()
	expiresAt := createdAt.Add(time.Hour)
	created, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-wake-1", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt, ExpiresAt: &expiresAt,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.EnforcementChanges) != 1 || sink.count() != 1 {
		t.Fatalf("create changes/wakes = %+v/%d", created.EnforcementChanges, sink.count())
	}

	replaced, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-wake-2", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt.Add(time.Minute), ExpiresAt: &expiresAt,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced.EnforcementChanges) != 0 || sink.count() != 1 {
		t.Fatalf("semantic no-op changes/wakes = %+v/%d", replaced.EnforcementChanges, sink.count())
	}
	assertDesiredTargetState(t, database, target, "present", 1, 1, "pending", 0, 0)
}

func TestLifecycleServicePostCommitWakeFailurePreservesCommittedResult(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	wakeFailure := errors.New("injected wake failure")
	service, err := decision.NewLifecycleService(
		database,
		newTestDesiredStateFinalizer(t),
		decision.TargetWakeSinkFunc(func(context.Context, core.NodeID, netip.Prefix) error { return wakeFailure }),
	)
	if err != nil {
		t.Fatal(err)
	}
	target := netip.MustParsePrefix("198.51.100.71/32")
	result, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-wake-failure", NodeID: testNodeID, Target: target,
		CreatedAt: time.Unix(3_600, 0).UTC(),
	}, false)
	if !errors.Is(err, decision.ErrPostCommitWake) || !errors.Is(err, wakeFailure) ||
		result.Current.ID != "manual-wake-failure" || len(result.EnforcementChanges) != 1 {
		t.Fatalf("post-commit wake result = %+v, %v", result, err)
	}
	assertDesiredTargetState(t, database, target, "present", 1, 1, "pending", 0, 0)
}

func TestSQLiteExpiryAuditFailureRollsBackBatch(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	service := newDecisionLifecycleService(t, database)
	target := netip.MustParsePrefix("203.0.113.10/32")
	createdAt := time.Unix(4_000, 0).UTC()
	expiresAt := createdAt.Add(time.Minute)
	if _, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-due", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt, ExpiresAt: &expiresAt,
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `
		CREATE TRIGGER fail_decision_expire_audit
		BEFORE INSERT ON audit_logs WHEN NEW.action = 'decision_expire'
		BEGIN SELECT RAISE(ABORT, 'injected expiration audit failure'); END`); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Expire(ctx, expiresAt); err == nil {
		t.Fatal("Expire() error = nil")
	}
	assertManualLifecycleState(t, database, "manual-due", "active", sql.NullString{}, expiresAt, 1, 1)
}

func TestSQLiteExpirySameTargetBatchRebuildsProjectionOnce(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	service := newDecisionLifecycleService(t, database)
	target := netip.MustParsePrefix("203.0.113.11/32")
	createdAt := time.Unix(4_100, 0).UTC()
	expiresAt := createdAt.Add(time.Minute)
	if _, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-same-batch", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt, ExpiresAt: &expiresAt,
	}, false); err != nil {
		t.Fatal(err)
	}
	seedAutomaticDecisionAndProjection(t, database, target, createdAt, expiresAt, expiresAt)

	result, err := service.Expire(ctx, expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Expired) != 2 || len(result.Projections) != 1 ||
		result.Projections[0].Revision != 3 || result.Projections[0].State != core.BanProjectionAbsent {
		t.Fatalf("same-target expiry batch = %+v", result)
	}
	assertProjectionAndAuditCounts(t, database, 3, 3)
}

func TestSQLiteExpiryMultipleTargetsAdvancesSnapshotOnce(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	sink := &recordingTargetWakeSink{}
	service, err := decision.NewLifecycleService(database, newTestDesiredStateFinalizer(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Unix(4_150, 0).UTC()
	expiresAt := createdAt.Add(time.Minute)
	targets := []netip.Prefix{
		netip.MustParsePrefix("203.0.113.21/32"),
		netip.MustParsePrefix("203.0.113.22/32"),
	}
	for index, target := range targets {
		if _, err := service.BanManual(ctx, decision.ManualRequest{
			DecisionID: core.DecisionID(fmt.Sprintf("manual-multi-%d", index)),
			NodeID:     testNodeID, Target: target, CreatedAt: createdAt, ExpiresAt: &expiresAt,
		}, false); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.Expire(ctx, expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.EnforcementChanges) != 2 ||
		result.EnforcementChanges[0].SnapshotRevision != 3 ||
		result.EnforcementChanges[1].SnapshotRevision != 3 || sink.count() != 4 {
		t.Fatalf("multi-target expiry changes/wakes = %+v/%d", result.EnforcementChanges, sink.count())
	}
	for _, target := range targets {
		assertDesiredTargetState(t, database, target, "absent", 2, 3, "pending", 0, 0)
	}
}

func TestSQLiteIntentWriteFailureRollsBackDecisionProjectionAndAudit(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	if _, err := database.db.ExecContext(ctx, `
		CREATE TRIGGER fail_target_intent
		BEFORE INSERT ON enforcement_states
		BEGIN SELECT RAISE(ABORT, 'injected target intent failure'); END`); err != nil {
		t.Fatal(err)
	}
	sink := &recordingTargetWakeSink{}
	service, err := decision.NewLifecycleService(database, newTestDesiredStateFinalizer(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-intent-rollback", NodeID: testNodeID,
		Target: netip.MustParsePrefix("203.0.113.30/32"), CreatedAt: time.Unix(4_180, 0).UTC(),
	}, false)
	if err == nil {
		t.Fatal("BanManual() error = nil")
	}
	if result.Current.ID != "" || len(result.EnforcementChanges) != 0 {
		t.Fatalf("known rollback leaked expected result = %+v", result)
	}
	for _, table := range []string{"decisions", "desired_ban_projections", "enforcement_states", "audit_logs"} {
		var count int
		if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count after rollback = %d", table, count)
		}
	}
	if sink.count() != 0 {
		t.Fatalf("rollback wakes = %d", sink.count())
	}
}

func TestSQLiteSnapshotRevisionExhaustionRollsBackEntireDecisionTransaction(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	sink := &recordingTargetWakeSink{}
	service, err := decision.NewLifecycleService(database, newTestDesiredStateFinalizer(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	target := netip.MustParsePrefix("203.0.113.31/32")
	createdAt := time.Unix(4_190, 0).UTC()
	firstExpiry := createdAt.Add(time.Hour)
	if _, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-snapshot-root", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt, ExpiresAt: &firstExpiry,
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `
		UPDATE desired_firewall_state SET snapshot_revision = 9223372036854775807
		WHERE singleton = 1`); err != nil {
		t.Fatal(err)
	}
	secondExpiry := createdAt.Add(2 * time.Hour)
	result, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-snapshot-replacement", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt.Add(time.Minute), ExpiresAt: &secondExpiry,
	}, true)
	if err == nil || !strings.Contains(err.Error(), "revision is exhausted") {
		t.Fatalf("snapshot exhaustion error = %v", err)
	}
	if result.Current.ID != "" || len(result.EnforcementChanges) != 0 {
		t.Fatalf("snapshot rollback leaked expected result = %+v", result)
	}
	var decisions, projectionRevision, audits int64
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM decisions").Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx,
		"SELECT target_projection_revision FROM desired_ban_projections").Scan(&projectionRevision); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if decisions != 1 || projectionRevision != 1 || audits != 1 || sink.count() != 1 {
		t.Fatalf("snapshot rollback state = decisions:%d projection:%d audits:%d wakes:%d",
			decisions, projectionRevision, audits, sink.count())
	}
	assertDesiredTargetState(t, database, target, "present", 1, math.MaxInt64, "pending", 0, 0)
}

func TestLifecycleServicePreservesExpectedResultOnCommitUnknown(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	runner := commitUnknownAfterSuccessRunner{Store: database}
	sink := &recordingTargetWakeSink{}
	service, err := decision.NewLifecycleService(runner, newTestDesiredStateFinalizer(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	target := netip.MustParsePrefix("203.0.113.12/32")
	createdAt := time.Unix(4_200, 0).UTC()
	expiresAt := createdAt.Add(time.Minute)
	manual, err := service.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-commit-unknown", NodeID: testNodeID, Target: target,
		CreatedAt: createdAt, ExpiresAt: &expiresAt,
	}, false)
	if !errors.Is(err, decision.ErrCommitUnknown) || manual.Current.ID != "manual-commit-unknown" {
		t.Fatalf("manual commit-unknown result = %+v, %v", manual, err)
	}
	if sink.count() != 0 {
		t.Fatalf("commit-unknown manual wakes = %d, want 0", sink.count())
	}

	expired, err := service.Expire(ctx, expiresAt)
	if !errors.Is(err, decision.ErrCommitUnknown) || len(expired.Expired) != 1 ||
		expired.Expired[0].ID != "manual-commit-unknown" || len(expired.Projections) != 1 {
		t.Fatalf("expiry commit-unknown result = %+v, %v", expired, err)
	}
	if sink.count() != 0 {
		t.Fatalf("commit-unknown expiry wakes = %d, want 0", sink.count())
	}
}

type commitUnknownAfterSuccessRunner struct {
	*Store
}

func (r commitUnknownAfterSuccessRunner) RunDecisionTransaction(
	ctx context.Context,
	operation func(decision.LifecycleTransaction) error,
) error {
	if err := r.Store.RunDecisionTransaction(ctx, operation); err != nil {
		return err
	}
	return decision.NewCommitUnknownError(errors.New("injected lost commit acknowledgement"))
}

func TestSQLiteExpirationSchedulerStartsWithDueSweepBeforePendingRecovery(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	target := netip.MustParsePrefix("198.51.100.95/32")
	now := time.Now().UTC()
	expiresAt := now.Add(-time.Minute)
	seedAutomaticDecisionAndProjection(t, database, target, expiresAt.Add(-time.Minute), expiresAt, expiresAt)

	firstWake := make(chan struct{})
	releaseWake := make(chan struct{})
	var wakeMu sync.Mutex
	wakeCount := 0
	sink := decision.TargetWakeSinkFunc(func(ctx context.Context, _ core.NodeID, got netip.Prefix) error {
		wakeMu.Lock()
		wakeCount++
		call := wakeCount
		wakeMu.Unlock()
		if got != target {
			return fmt.Errorf("unexpected expiration wake target %s", got)
		}
		if call == 1 {
			close(firstWake)
			select {
			case <-releaseWake:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	runner := &observedPendingRunner{Store: database, read: make(chan struct{})}
	service, err := decision.NewLifecycleService(runner, newTestDesiredStateFinalizer(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- service.RunExpirationScheduler(runCtx) }()

	select {
	case <-firstWake:
	case <-time.After(time.Second):
		t.Fatal("startup expiration did not wake the due Target")
	}
	assertDecisionState(t, database, "automatic-expiry", "expired", "expired")
	assertDesiredTargetState(t, database, target, "absent", 1, 1, "pending", 0, 0)
	close(releaseWake)
	select {
	case <-runner.read:
	case <-time.After(time.Second):
		t.Fatal("startup expiration did not reach durable pending recovery")
	}
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunExpirationScheduler() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expiration scheduler did not stop")
	}
	wakeMu.Lock()
	defer wakeMu.Unlock()
	if wakeCount != 1 {
		t.Fatalf("startup expiration wakes = %d, want 1", wakeCount)
	}
}

type observedPendingRunner struct {
	*Store
	once sync.Once
	read chan struct{}
}

func (r *observedPendingRunner) PendingTargetEnforcementChanges(
	ctx context.Context,
) ([]decision.TargetEnforcementChange, error) {
	changes, err := r.Store.PendingTargetEnforcementChanges(ctx)
	r.once.Do(func() { close(r.read) })
	return changes, err
}

func newDecisionLifecycleService(t *testing.T, database *Store) *decision.LifecycleService {
	t.Helper()
	service, err := decision.NewLifecycleService(database, newTestDesiredStateFinalizer(t), noOpTargetWakeSink())
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newTestDesiredStateFinalizer(t *testing.T) *decision.DesiredStateFinalizer {
	t.Helper()
	finalizer, err := decision.NewDesiredStateFinalizer(decision.TargetPolicyResolverFunc(
		func(context.Context, decision.DesiredStateTransaction, core.DesiredBanProjection) (enforcement.TargetPolicy, error) {
			return enforcement.TargetPolicy{
				Coverage: core.PolicyCoverageNone, Scopes: core.ScopeInput,
				NativeTimeoutSupported: true, BackendAttributesDigest: strings.Repeat("a", 64),
			}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	return finalizer
}

func noOpTargetWakeSink() decision.TargetWakeSink {
	return decision.TargetWakeSinkFunc(func(context.Context, core.NodeID, netip.Prefix) error { return nil })
}

type recordingTargetWakeSink struct {
	mu    sync.Mutex
	calls []netip.Prefix
}

func (s *recordingTargetWakeSink) WakeTarget(_ context.Context, _ core.NodeID, target netip.Prefix) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, target)
	return nil
}

func (s *recordingTargetWakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func assertDesiredTargetState(
	t *testing.T,
	database *Store,
	target netip.Prefix,
	wantMembership string,
	wantGeneration, wantSnapshot int64,
	wantStatus string,
	wantRetryEpoch, wantAttempts int64,
) {
	t.Helper()
	var membership, status string
	var generation, snapshot, retryEpoch, attempts int64
	if err := database.db.QueryRowContext(context.Background(), `
		SELECT e.desired_membership, e.target_enforcement_generation,
			d.snapshot_revision, r.status, r.retry_epoch, r.attempt_count
		FROM enforcement_states e
		JOIN desired_firewall_state d ON d.singleton = 1
		JOIN target_reconcile_state r
			ON r.node_id = e.node_id AND r.canonical_target = e.canonical_target
		WHERE e.node_id = ? AND e.canonical_target = ?`,
		string(testNodeID), target.String()).Scan(
		&membership, &generation, &snapshot, &status, &retryEpoch, &attempts,
	); err != nil {
		t.Fatal(err)
	}
	if membership != wantMembership || generation != wantGeneration || snapshot != wantSnapshot ||
		status != wantStatus || retryEpoch != wantRetryEpoch || attempts != wantAttempts {
		t.Fatalf("desired target = membership:%s generation:%d snapshot:%d status:%s retry:%d attempts:%d",
			membership, generation, snapshot, status, retryEpoch, attempts)
	}
}

func assertManualLifecycleState(
	t *testing.T,
	database *Store,
	decisionID string,
	wantState string,
	wantReason sql.NullString,
	wantExpiry time.Time,
	wantProjectionRevision int64,
	wantAuditCount int,
) {
	t.Helper()
	ctx := context.Background()
	var state string
	var reason sql.NullString
	var expiry int64
	if err := database.db.QueryRowContext(ctx, `
		SELECT state, end_reason, expires_at_us FROM decisions WHERE decision_id = ?`, decisionID).Scan(
		&state, &reason, &expiry); err != nil {
		t.Fatal(err)
	}
	if state != wantState || reason != wantReason || expiry != wantExpiry.UnixMicro() {
		t.Fatalf("decision %s = state:%s reason:%+v expiry:%d", decisionID, state, reason, expiry)
	}
	assertProjectionAndAuditCounts(t, database, wantProjectionRevision, wantAuditCount)
}

func assertProjectionAndAuditCounts(t *testing.T, database *Store, wantRevision int64, wantAudits int) {
	t.Helper()
	ctx := context.Background()
	var revision int64
	if err := database.db.QueryRowContext(ctx,
		"SELECT target_projection_revision FROM desired_ban_projections").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	var auditCount int
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if revision != wantRevision || auditCount != wantAudits {
		t.Fatalf("projection/audit = revision:%d audits:%d, want %d/%d", revision, auditCount, wantRevision, wantAudits)
	}
}

func assertDecisionState(t *testing.T, database *Store, id, wantState, wantReason string) {
	t.Helper()
	var state, reason string
	if err := database.db.QueryRowContext(context.Background(), `
		SELECT state, end_reason FROM decisions WHERE decision_id = ?`, id).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != wantState || reason != wantReason {
		t.Fatalf("decision %s = %s/%s", id, state, reason)
	}
}

func seedAutomaticDecisionAndProjection(
	t *testing.T,
	database *Store,
	target netip.Prefix,
	createdAt time.Time,
	automaticExpiry time.Time,
	projectionExpiry time.Time,
) {
	t.Helper()
	ruleID := core.RuleID("rule-1")
	ruleVersion := core.RuleVersion("v1")
	uow, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := uow.PutDecision(context.Background(), core.Decision{
		ID: "automatic-expiry", NodeID: testNodeID, Source: core.DecisionSourceAutomatic,
		RuleID: &ruleID, RuleVersion: &ruleVersion, CanonicalTarget: target,
		CreatedAt: createdAt, UpdatedAt: createdAt, LastTriggeredAt: createdAt,
		ExpiresAt: &automaticExpiry, State: core.DecisionActive,
	}); err != nil {
		t.Fatal(err)
	}
	if err := uow.PutProjection(context.Background(), core.DesiredBanProjection{
		NodeID: testNodeID, CanonicalTarget: target, State: core.BanProjectionPresent,
		ActiveCount: 2, EffectiveUntil: &projectionExpiry, Revision: 2,
	}, createdAt); err != nil {
		t.Fatal(err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatal(err)
	}
}
