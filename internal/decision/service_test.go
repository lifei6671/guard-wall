package decision

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestAutomaticDuplicateLifecycle(t *testing.T) {
	target := netip.MustParsePrefix("192.0.2.4/32")
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	expiresAt := createdAt.Add(time.Hour)
	tests := []struct {
		name     string
		triggers int
	}{
		{name: "five failures produce one active decision", triggers: 5},
		{name: "one trigger creates without suppression", triggers: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewMemoryService()
			var originalID core.DecisionID
			for index := 0; index < test.triggers; index++ {
				request := automaticRequest(core.DecisionID(fmt.Sprintf("decision-%d", index)), target, createdAt.Add(time.Duration(index)*time.Second), &expiresAt)
				result, err := service.RecordAutomatic(request)
				if err != nil {
					t.Fatal(err)
				}
				if index == 0 {
					originalID = result.Decision.ID
				} else if result.Decision.ID != originalID {
					t.Fatalf("duplicate changed DecisionID from %s to %s", originalID, result.Decision.ID)
				}
			}
			decisions := service.Decisions()
			if len(decisions) != 1 || decisions[0].State != core.DecisionActive {
				t.Fatalf("unexpected decision history: %+v", decisions)
			}
			if decisions[0].SuppressedCount != uint64(test.triggers-1) {
				t.Fatalf("suppressed count = %d", decisions[0].SuppressedCount)
			}
			if decisions[0].ExpiresAt == nil || !decisions[0].ExpiresAt.Equal(expiresAt) {
				t.Fatalf("duplicate refreshed expiry: %v", decisions[0].ExpiresAt)
			}
		})
	}
}

func TestConcurrentAutomaticTriggersUseOneActiveIdentity(t *testing.T) {
	service := NewMemoryService()
	target := netip.MustParsePrefix("192.0.2.4/32")
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	const triggers = 32
	start := make(chan struct{})
	errs := make(chan error, triggers)
	var wait sync.WaitGroup
	for index := 0; index < triggers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			request := automaticRequest(core.DecisionID(fmt.Sprintf("concurrent-%d", index)), target, createdAt, nil)
			_, err := service.RecordAutomatic(request)
			errs <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	decisions := service.Decisions()
	if len(decisions) != 1 || decisions[0].SuppressedCount != triggers-1 {
		t.Fatalf("concurrent identity was not atomic: %+v", decisions)
	}
}

func TestAutomaticRequiresAndFreezesRuleVersionWithOutOfOrderSuppression(t *testing.T) {
	service := NewMemoryService()
	target := netip.MustParsePrefix("192.0.2.4/32")
	now := time.Unix(1_700_000_000, 0).UTC()
	missing := automaticRequest("missing-version", target, now, nil)
	missing.RuleVersion = nil
	if _, err := service.RecordAutomatic(missing); err == nil {
		t.Fatal("automatic decision without RuleVersion was accepted")
	}
	initial := automaticRequest("automatic-v1", target, now.Add(time.Minute), nil)
	if _, err := service.RecordAutomatic(initial); err != nil {
		t.Fatal(err)
	}
	v2 := core.RuleVersion("v2")
	outOfOrder := automaticRequest("ignored-duplicate-id", target, now, nil)
	outOfOrder.RuleVersion = &v2
	result, err := service.RecordAutomatic(outOfOrder)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.SuppressedCount != 1 || !result.Decision.LastTriggeredAt.Equal(initial.TriggeredAt) {
		t.Fatalf("out-of-order suppression result = %+v", result.Decision)
	}
	if result.Decision.RuleVersion == nil || *result.Decision.RuleVersion != "v1" {
		t.Fatalf("duplicate refreshed RuleVersion: %v", result.Decision.RuleVersion)
	}
}

func TestDecisionIDIsUniqueAcrossHistorySourceAndReplace(t *testing.T) {
	service := NewMemoryService()
	targetA := netip.MustParsePrefix("192.0.2.4/32")
	targetB := netip.MustParsePrefix("192.0.2.5/32")
	now := time.Unix(1_700_000_000, 0).UTC()
	if _, err := service.RecordAutomatic(automaticRequest("global-id", targetA, now, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Revoke("global-id", now.Add(time.Minute), core.EndReasonRuleDisabled); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BanManual(ManualRequest{DecisionID: "global-id", NodeID: testNodeID, Target: targetB, CreatedAt: now.Add(2 * time.Minute)}, false); !errors.Is(err, ErrDecisionIDConflict) {
		t.Fatalf("cross-source history collision error = %v", err)
	}
	if _, err := service.BanManual(ManualRequest{DecisionID: "manual-active", NodeID: testNodeID, Target: targetB, CreatedAt: now.Add(2 * time.Minute)}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BanManual(ManualRequest{DecisionID: "global-id", NodeID: testNodeID, Target: targetB, CreatedAt: now.Add(3 * time.Minute)}, true); !errors.Is(err, ErrDecisionIDConflict) {
		t.Fatalf("replace collision error = %v", err)
	}
	decisions := service.Decisions()
	if len(decisions) != 2 || decisions[1].State != core.DecisionActive {
		t.Fatalf("ID conflict partially mutated history: %+v", decisions)
	}
}

func TestExpirationAndProjectionFollowLastActiveDecision(t *testing.T) {
	service := NewMemoryService()
	target := netip.MustParsePrefix("192.0.2.4/32")
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	firstExpiry := createdAt.Add(time.Minute)
	secondExpiry := createdAt.Add(2 * time.Minute)
	first := automaticRequest("first", target, createdAt, &firstExpiry)
	second := automaticRequest("second", target, createdAt, &secondExpiry)
	second.RuleID = "rule-2"
	if _, err := service.RecordAutomatic(first); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordAutomatic(second); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Expire(firstExpiry); err != nil {
		t.Fatal(err)
	}
	projection, err := AggregateProjection(testNodeID, target, 1, service.Decisions())
	if err != nil {
		t.Fatal(err)
	}
	if projection.State != core.BanProjectionPresent || projection.ActiveCount != 1 {
		t.Fatalf("first expiry removed remaining ban: %+v", projection)
	}
	if _, err := service.Expire(secondExpiry); err != nil {
		t.Fatal(err)
	}
	projection, err = AggregateProjection(testNodeID, target, 2, service.Decisions())
	if err != nil {
		t.Fatal(err)
	}
	if projection.State != core.BanProjectionAbsent || projection.ActiveCount != 0 {
		t.Fatalf("last expiry did not remove projection: %+v", projection)
	}
}

func TestPolicyResolverUnionCoverageAndStableDigest(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		allow     []string
		protected []string
		want      core.PolicyCoverage
	}{
		{name: "none", target: "192.0.2.0/24", allow: []string{"198.51.100.1/32"}, want: core.PolicyCoverageNone},
		{name: "partial hole", target: "192.0.2.0/24", allow: []string{"192.0.2.0/26", "192.0.2.128/25"}, want: core.PolicyCoveragePartial},
		{name: "IPv4 child union full", target: "192.0.2.0/24", allow: []string{"192.0.2.128/25", "192.0.2.0/25"}, want: core.PolicyCoverageFull},
		{name: "IPv6 child union full", target: "2001:db8::/64", allow: []string{"2001:db8:0:0:8000::/65"}, protected: []string{"2001:db8::/65"}, want: core.PolicyCoverageFull},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := PolicyInput{Target: netip.MustParsePrefix(test.target), Allowlists: prefixes(test.allow), ProtectedTargets: prefixes(test.protected)}
			resolution, err := ResolvePolicy(input)
			if err != nil {
				t.Fatal(err)
			}
			if resolution.Coverage != test.want {
				t.Fatalf("coverage = %d, want %d", resolution.Coverage, test.want)
			}
			if test.want == core.PolicyCoverageNone && resolution.RelationDigest != "" {
				t.Fatalf("none relation digest = %q", resolution.RelationDigest)
			}
			if test.want != core.PolicyCoverageNone && resolution.RelationDigest == "" {
				t.Fatal("overlap relation digest is empty")
			}
			input.Allowlists = reversePrefixes(input.Allowlists)
			input.ProtectedTargets = append(input.ProtectedTargets, input.ProtectedTargets...)
			reordered, err := ResolvePolicy(input)
			if err != nil || reordered != resolution {
				t.Fatalf("relation is not stable: first=%+v second=%+v err=%v", resolution, reordered, err)
			}
		})
	}
}

func TestPolicyFactsNeverMutateDecisionLifecycle(t *testing.T) {
	service := NewMemoryService()
	target := netip.MustParsePrefix("192.0.2.0/24")
	if _, err := service.RecordAutomatic(automaticRequest("policy-decision", target, time.Unix(1_700_000_000, 0).UTC(), nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePolicy(PolicyInput{
		Target: target, Allowlists: []netip.Prefix{netip.MustParsePrefix("192.0.2.4/32")},
		ProtectedTargets: []netip.Prefix{netip.MustParsePrefix("192.0.2.8/32")},
	}); err != nil {
		t.Fatal(err)
	}
	if decisions := service.Decisions(); len(decisions) != 1 || decisions[0].State != core.DecisionActive {
		t.Fatalf("orthogonal policy changed Decision lifecycle: %+v", decisions)
	}
}

func TestManualDuplicateAndAtomicReplace(t *testing.T) {
	service := NewMemoryService()
	target := netip.MustParsePrefix("192.0.2.4/32")
	now := time.Unix(1_700_000_000, 0).UTC()
	first := ManualRequest{DecisionID: "manual-1", NodeID: testNodeID, Target: target, CreatedAt: now}
	if _, err := service.BanManual(first, false); err != nil {
		t.Fatal(err)
	}
	duplicate := ManualRequest{DecisionID: "manual-duplicate", NodeID: testNodeID, Target: target, CreatedAt: now.Add(time.Minute)}
	if _, err := service.BanManual(duplicate, false); !errors.Is(err, ErrAlreadyBanned) {
		t.Fatalf("duplicate error = %v", err)
	} else {
		var typed *AlreadyBannedError
		if !errors.As(err, &typed) || typed.DecisionID != first.DecisionID {
			t.Fatalf("typed AlreadyBanned = %#v", typed)
		}
	}
	replacement := ManualRequest{DecisionID: "manual-2", NodeID: testNodeID, Target: target, CreatedAt: now.Add(2 * time.Minute)}
	result, err := service.BanManual(replacement, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replaced || result.Previous == nil || result.Previous.State != core.DecisionRevoked ||
		result.Previous.EndReason == nil || *result.Previous.EndReason != core.EndReasonManualReplace ||
		result.Current.State != core.DecisionActive {
		t.Fatalf("invalid replace result: %+v", result)
	}
	decisions := service.Decisions()
	if len(decisions) != 2 || decisions[0].State != core.DecisionRevoked || decisions[1].State != core.DecisionActive {
		t.Fatalf("replace was not atomic in history: %+v", decisions)
	}
}

func TestRevokedDecisionMakesProjectionAbsentIndependentOfFirewallOutcome(t *testing.T) {
	service := NewMemoryService()
	target := netip.MustParsePrefix("192.0.2.4/32")
	now := time.Unix(1_700_000_000, 0).UTC()
	if _, err := service.RecordAutomatic(automaticRequest("revoke-me", target, now, nil)); err != nil {
		t.Fatal(err)
	}
	revoked, err := service.Revoke("revoke-me", now.Add(time.Minute), core.EndReasonRuleDisabled)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.State != core.DecisionRevoked || revoked.EndReason == nil || *revoked.EndReason != core.EndReasonRuleDisabled {
		t.Fatalf("unexpected revoked decision: %+v", revoked)
	}
	projection, err := AggregateProjection(testNodeID, target, 1, service.Decisions())
	if err != nil {
		t.Fatal(err)
	}
	if projection.State != core.BanProjectionAbsent {
		t.Fatalf("revoked decision remained in desired projection: %+v", projection)
	}
}

func TestTerminalConflictIsTypedAndPreservesOriginalOutcome(t *testing.T) {
	service := NewMemoryService()
	target := netip.MustParsePrefix("192.0.2.4/32")
	now := time.Unix(1_700_000_000, 0).UTC()
	if _, err := service.RecordAutomatic(automaticRequest("terminal", target, now, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Revoke("terminal", now.Add(time.Minute), core.EndReasonRuleDisabled); err != nil {
		t.Fatal(err)
	}
	_, err := service.Revoke("terminal", now.Add(2*time.Minute), core.EndReasonSystemCleanup)
	if !errors.Is(err, ErrTerminalConflict) {
		t.Fatalf("terminal conflict error = %v", err)
	}
	var typed *TerminalConflictError
	if !errors.As(err, &typed) || typed.ExistingReason == nil || *typed.ExistingReason != core.EndReasonRuleDisabled {
		t.Fatalf("typed terminal conflict = %#v", typed)
	}
	decision := service.Decisions()[0]
	if decision.EndReason == nil || *decision.EndReason != core.EndReasonRuleDisabled {
		t.Fatalf("terminal outcome was overwritten: %+v", decision)
	}
}

func TestDecisionResultsAndSnapshotsAreImmutableClones(t *testing.T) {
	service := NewMemoryService()
	target := netip.MustParsePrefix("192.0.2.4/32")
	now := time.Unix(1_700_000_000, 0).UTC()
	expires := now.Add(time.Hour)
	result, err := service.RecordAutomatic(automaticRequest("clone", target, now, &expires))
	if err != nil {
		t.Fatal(err)
	}
	*result.Decision.ExpiresAt = now
	*result.Decision.RuleVersion = "mutated"
	snapshot := service.Decisions()
	*snapshot[0].ExpiresAt = now.Add(2 * time.Hour)
	*snapshot[0].RuleVersion = "also-mutated"
	persisted := service.Decisions()[0]
	if persisted.ExpiresAt == nil || !persisted.ExpiresAt.Equal(expires) || persisted.RuleVersion == nil || *persisted.RuleVersion != "v1" {
		t.Fatalf("caller mutated stored Decision through clone: %+v", persisted)
	}
}

func automaticRequest(id core.DecisionID, target netip.Prefix, triggeredAt time.Time, expiresAt *time.Time) AutomaticRequest {
	version := core.RuleVersion("v1")
	return AutomaticRequest{
		DecisionID: id, NodeID: testNodeID, RuleID: "rule-1", RuleVersion: &version,
		Target: target, TriggeredAt: triggeredAt, ExpiresAt: expiresAt,
	}
}

func prefixes(values []string) []netip.Prefix {
	result := make([]netip.Prefix, len(values))
	for index, value := range values {
		result[index] = netip.MustParsePrefix(value)
	}
	return result
}

func reversePrefixes(values []netip.Prefix) []netip.Prefix {
	result := append([]netip.Prefix(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

const testNodeID core.NodeID = "00112233445566778899aabbccddeeff"
