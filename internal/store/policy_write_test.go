package store

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/enforcement"
)

func TestPolicyServiceReplaceAtomicallyRematerializesTargetsAndWakes(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, now); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	target := netip.MustParsePrefix("192.0.2.0/24")
	seedPolicyProjection(t, database, target, now)

	policyWakes := 0
	targetWakes := make([]netip.Prefix, 0)
	service := newPolicyWriteService(t, database,
		decision.PolicyWakeSinkFunc(func(context.Context, core.NodeID) error {
			policyWakes++
			return nil
		}),
		decision.TargetWakeSinkFunc(func(_ context.Context, _ core.NodeID, value netip.Prefix) error {
			targetWakes = append(targetWakes, value)
			return nil
		}),
	)
	firstPolicy := completePolicy(t, []string{"192.0.2.0/24"})
	first, err := service.Replace(ctx, policyWriteRequest(firstPolicy, 0, now))
	if err != nil {
		t.Fatalf("Replace(first): %v", err)
	}
	if !first.Changed || first.PolicyRevision != 1 || first.SnapshotRevision != 1 || len(first.TargetChanges) != 1 ||
		first.TargetChanges[0].Generation != 1 || policyWakes != 1 || len(targetWakes) != 1 {
		t.Fatalf("first replace = %+v policy wakes=%d target wakes=%v", first, policyWakes, targetWakes)
	}

	secondPolicy := completePolicy(t, nil)
	secondRequest := policyWriteRequest(secondPolicy, first.PolicyRevision, now.Add(time.Second))
	secondRequest.AuditID = "policy-audit-2"
	secondRequest.AuditIdempotencyKey = "policy-write-2"
	second, err := service.Replace(ctx, secondRequest)
	if err != nil {
		t.Fatalf("Replace(second): %v", err)
	}
	if !second.Changed || second.PolicyRevision != 2 || second.SnapshotRevision != 2 || len(second.TargetChanges) != 1 ||
		second.TargetChanges[0].Generation != 2 || policyWakes != 2 || len(targetWakes) != 2 {
		t.Fatalf("second replace = %+v policy wakes=%d target wakes=%v", second, policyWakes, targetWakes)
	}

	noOpRequest := policyWriteRequest(secondPolicy, second.PolicyRevision, now.Add(2*time.Second))
	noOpRequest.AuditID = "policy-audit-3"
	noOpRequest.AuditIdempotencyKey = "policy-write-3"
	noOp, err := service.Replace(ctx, noOpRequest)
	if err != nil {
		t.Fatalf("Replace(no-op): %v", err)
	}
	if noOp.Changed || noOp.PolicyRevision != 2 || noOp.SnapshotRevision != 0 || policyWakes != 2 || len(targetWakes) != 2 {
		t.Fatalf("no-op replace = %+v policy wakes=%d target wakes=%v", noOp, policyWakes, targetWakes)
	}

	state, err := database.LoadDesiredFirewallState(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadDesiredFirewallState(): %v", err)
	}
	if state.PolicyRevision != 2 || state.SnapshotRevision != 2 || !samePrefixes(state.Policy.Allowlist, nil) ||
		len(state.Targets) != 1 || state.Targets[0].Generation != 2 || state.Targets[0].PolicyCoverage != core.PolicyCoverageNone {
		t.Fatalf("persisted desired state = %+v", state)
	}
	var auditCount int
	if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs WHERE category = 'policy' AND action = 'replace'").Scan(&auditCount); err != nil {
		t.Fatalf("count policy audit: %v", err)
	}
	if auditCount != 2 {
		t.Fatalf("policy audit count = %d, want 2", auditCount)
	}
	recovery, err := database.LoadReconcileRecovery(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadReconcileRecovery(): %v", err)
	}
	if !containsPolicyRetryState(recovery.States, 2) {
		t.Fatalf("policy pending retry state is missing: %+v", recovery.States)
	}
}

func TestPolicyServiceRejectsStaleRevisionWithoutWritesOrWake(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, now); err != nil {
		t.Fatal(err)
	}
	policyWakes := 0
	service := newPolicyWriteService(t, database,
		decision.PolicyWakeSinkFunc(func(context.Context, core.NodeID) error { policyWakes++; return nil }),
		decision.TargetWakeSinkFunc(func(context.Context, core.NodeID, netip.Prefix) error { return nil }),
	)
	firstPolicy := completePolicy(t, nil)
	if _, err := service.Replace(ctx, policyWriteRequest(firstPolicy, 0, now)); err != nil {
		t.Fatal(err)
	}
	before, err := database.LoadDesiredFirewallState(ctx, testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	stale := policyWriteRequest(completePolicy(t, []string{"192.0.2.0/24"}), 0, now.Add(time.Second))
	stale.AuditID = "policy-audit-stale"
	stale.AuditIdempotencyKey = "policy-write-stale"
	if _, err := service.Replace(ctx, stale); err == nil || !strings.Contains(err.Error(), "expected revision") {
		t.Fatalf("Replace(stale) error = %v", err)
	}
	after, err := database.LoadDesiredFirewallState(ctx, testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PolicyRevision != before.PolicyRevision || after.SnapshotRevision != before.SnapshotRevision ||
		after.Policy.RelationDigest != before.Policy.RelationDigest || policyWakes != 1 {
		t.Fatalf("stale write changed desired state: before=%+v after=%+v wakes=%d", before, after, policyWakes)
	}
}

func TestPolicyServiceCanonicalizesDisabledLegacyRows(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, now); err != nil {
		t.Fatal(err)
	}
	seedDesiredFirewallPolicyRows(t, database, []desiredFirewallPolicyRow{
		{table: "allowlists", target: "192.0.2.0/24", enabled: 1, revision: 5},
		{table: "allowlists", target: "198.51.100.0/24", enabled: 0, revision: 5},
		{table: "protected_targets", target: "127.0.0.0/8", enabled: 1, revision: 5},
		{table: "protected_targets", target: "::1/128", enabled: 1, revision: 5},
	})
	service := newPolicyWriteService(t, database,
		decision.PolicyWakeSinkFunc(func(context.Context, core.NodeID) error { return nil }),
		decision.TargetWakeSinkFunc(func(context.Context, core.NodeID, netip.Prefix) error { return nil }),
	)
	result, err := service.Replace(ctx, policyWriteRequest(completePolicy(t, []string{"192.0.2.0/24"}), 5, now))
	if err != nil || !result.Changed || result.PolicyRevision != 6 {
		t.Fatalf("Replace() = %+v, %v", result, err)
	}
	var disabled int
	if err := database.db.QueryRowContext(ctx, `
		SELECT count(*) FROM allowlists WHERE node_id = ? AND enabled = 0`, string(testNodeID)).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled != 0 {
		t.Fatalf("disabled rows remain after authoritative replace: %d", disabled)
	}
}

func TestPolicyServiceReturnsPostCommitWakeErrorWithoutReplay(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, now); err != nil {
		t.Fatal(err)
	}
	service := newPolicyWriteService(t, database,
		decision.PolicyWakeSinkFunc(func(context.Context, core.NodeID) error { return errors.New("queue unavailable") }),
		decision.TargetWakeSinkFunc(func(context.Context, core.NodeID, netip.Prefix) error {
			t.Fatal("target wake after policy wake failure")
			return nil
		}),
	)
	result, err := service.Replace(ctx, policyWriteRequest(completePolicy(t, nil), 0, now))
	if !errors.Is(err, decision.ErrPostCommitPolicyWake) || result.PolicyRevision != 1 || result.SnapshotRevision != 1 {
		t.Fatalf("Replace() = %+v, %v", result, err)
	}
	state, readErr := database.LoadDesiredFirewallState(ctx, testNodeID)
	if readErr != nil || state.PolicyRevision != 1 || state.SnapshotRevision != 1 {
		t.Fatalf("durable desired state = %+v, %v", state, readErr)
	}
}

func TestPolicyServiceCommitUnknownReadbackProvesAndWakes(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, now); err != nil {
		t.Fatal(err)
	}
	resolver, err := decision.NewManagedPolicyTargetResolver(core.ScopeInput, false, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := decision.NewDesiredStateFinalizer(resolver)
	if err != nil {
		t.Fatal(err)
	}
	policyWakes := 0
	service, err := decision.NewPolicyService(
		policyCommitUnknownRunner{database: database}, database, finalizer,
		decision.PolicyWakeSinkFunc(func(context.Context, core.NodeID) error { policyWakes++; return nil }),
		decision.TargetWakeSinkFunc(func(context.Context, core.NodeID, netip.Prefix) error { return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Replace(ctx, policyWriteRequest(completePolicy(t, nil), 0, now))
	if err != nil || !result.Changed || result.PolicyRevision != 1 || result.SnapshotRevision != 1 || policyWakes != 1 {
		t.Fatalf("Replace() = %+v, %v, wakes=%d", result, err, policyWakes)
	}
}

func TestPolicyServiceRollbackDoesNotReturnCommittedChange(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, now); err != nil {
		t.Fatal(err)
	}
	seedPolicyProjection(t, database, netip.MustParsePrefix("192.0.2.0/24"), now)
	finalizer, err := decision.NewDesiredStateFinalizer(decision.TargetPolicyResolverFunc(
		func(context.Context, decision.DesiredStateTransaction, core.DesiredBanProjection) (enforcement.TargetPolicy, error) {
			return enforcement.TargetPolicy{}, errors.New("resolver unavailable")
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	service, err := decision.NewPolicyService(
		database, database, finalizer,
		decision.PolicyWakeSinkFunc(func(context.Context, core.NodeID) error { t.Fatal("policy wake after rollback"); return nil }),
		decision.TargetWakeSinkFunc(func(context.Context, core.NodeID, netip.Prefix) error {
			t.Fatal("target wake after rollback")
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Replace(ctx, policyWriteRequest(completePolicy(t, nil), 0, now))
	if err == nil || result.Changed || result.NodeID != "" || result.PolicyRevision != 0 ||
		result.SnapshotRevision != 0 || len(result.TargetChanges) != 0 {
		t.Fatalf("Replace() = %+v, %v; want zero result and rollback error", result, err)
	}
	if _, err := database.LoadDesiredFirewallState(ctx, testNodeID); err == nil || !strings.Contains(err.Error(), "policy rows are missing") {
		t.Fatalf("policy rows persisted after rollback: %v", err)
	}
}

func newPolicyWriteService(t *testing.T, database *Store, policyWake decision.PolicyWakeSink, targetWake decision.TargetWakeSink) *decision.PolicyService {
	t.Helper()
	resolver, err := decision.NewManagedPolicyTargetResolver(core.ScopeInput, false, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := decision.NewDesiredStateFinalizer(resolver)
	if err != nil {
		t.Fatal(err)
	}
	service, err := decision.NewPolicyService(database, database, finalizer, policyWake, targetWake)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func policyWriteRequest(policy core.ManagedPolicyIntent, expected core.PolicyRevision, now time.Time) decision.PolicyWriteRequest {
	return decision.PolicyWriteRequest{
		NodeID: testNodeID, ExpectedPolicyRevision: expected, Policy: policy,
		AuditID: "policy-audit-1", AuditIdempotencyKey: "policy-write-1", ActorType: "system", UpdatedAt: now,
	}
}

func completePolicy(t *testing.T, allowlist []string) core.ManagedPolicyIntent {
	t.Helper()
	prepared := make([]netip.Prefix, 0, len(allowlist))
	for _, item := range allowlist {
		prepared = append(prepared, netip.MustParsePrefix(item))
	}
	policy, err := core.NewManagedPolicyIntent(prepared, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func seedPolicyProjection(t *testing.T, database *Store, target netip.Prefix, now time.Time) {
	t.Helper()
	uow, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projection := core.DesiredBanProjection{
		NodeID: testNodeID, CanonicalTarget: target, State: core.BanProjectionPresent,
		ActiveCount: 1, Revision: 1,
	}
	if err := uow.PutDecisionProjection(context.Background(), projection, now); err != nil {
		t.Fatal(err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatal(err)
	}
}

func containsPolicyRetryState(states []core.PersistedReconcileState, revision core.PolicyRevision) bool {
	for _, state := range states {
		if state.Domain == core.ReconcileDomainPolicy && state.PolicyRevision == revision &&
			state.RetryState.Status == core.ReconcilePending && state.RetryState.AttemptCount == 0 {
			return true
		}
	}
	return false
}

type policyCommitUnknownRunner struct{ database *Store }

func (r policyCommitUnknownRunner) RunPolicyTransaction(ctx context.Context, operation func(decision.PolicyTransaction) error) error {
	if err := r.database.RunPolicyTransaction(ctx, operation); err != nil {
		return err
	}
	return decision.NewCommitUnknownError(errors.New("commit acknowledgement unavailable"))
}
