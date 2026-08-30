package decision

import (
	"net/netip"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestAggregateProjectionLastActiveDecisionControlsAbsence(t *testing.T) {
	target := netip.MustParsePrefix("192.0.2.4/32")
	now := time.Unix(1_700_000_000, 0).UTC()
	expiredAt := now.Add(time.Minute)
	reason := core.EndReasonExpired
	rule := core.RuleID("rule-1")
	active := decision("active", target, &rule, nil)
	terminal := decision("expired", target, &rule, &expiredAt)
	terminal.State = core.DecisionExpired
	terminal.EndedAt = &expiredAt
	terminal.EndReason = &reason

	projection, err := AggregateProjection("00112233445566778899aabbccddeeff", target, 3, []core.Decision{active, terminal})
	if err != nil {
		t.Fatal(err)
	}
	if projection.State != core.BanProjectionPresent || projection.ActiveCount != 1 {
		t.Fatalf("unexpected projection: %+v", projection)
	}

	projection, err = AggregateProjection("00112233445566778899aabbccddeeff", target, 4, []core.Decision{terminal})
	if err != nil {
		t.Fatal(err)
	}
	if projection.State != core.BanProjectionAbsent || projection.ActiveCount != 0 {
		t.Fatalf("terminal decision must not keep projection present: %+v", projection)
	}
}

func TestAggregateProjectionPermanentDecisionWinsOverFiniteExpiry(t *testing.T) {
	target := netip.MustParsePrefix("2001:db8::/64")
	rule := core.RuleID("rule-1")
	expiresSoon := time.Unix(1_700_000_000, 0).UTC()
	expiresLater := expiresSoon.Add(time.Hour)

	finiteA := decision("finite-a", target, &rule, &expiresSoon)
	finiteB := decision("finite-b", target, &rule, &expiresLater)
	projection, err := AggregateProjection("00112233445566778899aabbccddeeff", target, 1, []core.Decision{finiteA, finiteB})
	if err != nil {
		t.Fatal(err)
	}
	if projection.EffectiveUntil == nil || !projection.EffectiveUntil.Equal(expiresLater) {
		t.Fatalf("expected latest finite expiry, got %v", projection.EffectiveUntil)
	}

	permanent := decision("permanent", target, &rule, nil)
	projection, err = AggregateProjection("00112233445566778899aabbccddeeff", target, 2, []core.Decision{finiteA, permanent})
	if err != nil {
		t.Fatal(err)
	}
	if projection.EffectiveUntil != nil {
		t.Fatalf("permanent decision must clear effective expiry, got %v", projection.EffectiveUntil)
	}
}

func TestAggregateProjectionRejectsMixedTargets(t *testing.T) {
	target := netip.MustParsePrefix("192.0.2.4/32")
	other := netip.MustParsePrefix("192.0.2.5/32")
	rule := core.RuleID("rule-1")
	_, err := AggregateProjection("00112233445566778899aabbccddeeff", target, 1, []core.Decision{decision("other", other, &rule, nil)})
	if err == nil {
		t.Fatal("expected mixed projection input to fail")
	}
}

func decision(id string, target netip.Prefix, rule *core.RuleID, expiresAt *time.Time) core.Decision {
	now := time.Unix(1_700_000_000, 0).UTC()
	return core.Decision{
		ID:              core.DecisionID(id),
		NodeID:          "00112233445566778899aabbccddeeff",
		Source:          core.DecisionSourceAutomatic,
		RuleID:          rule,
		CanonicalTarget: target,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastTriggeredAt: now,
		ExpiresAt:       expiresAt,
		State:           core.DecisionActive,
	}
}
