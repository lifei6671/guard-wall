package enforcement

import (
	"net/netip"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestResolveTargetGenerationTracksOnlyFirewallMeaning(t *testing.T) {
	expires := time.Unix(1_700_000_000, 0).UTC()
	projection := core.DesiredBanProjection{
		NodeID:          "00112233445566778899aabbccddeeff",
		CanonicalTarget: netip.MustParsePrefix("192.0.2.4/32"),
		State:           core.BanProjectionPresent,
		ActiveCount:     2,
		EffectiveUntil:  &expires,
		Revision:        1,
	}
	policy := TargetPolicy{
		Coverage:               core.PolicyCoverageNone,
		Scopes:                 core.ScopeInput,
		NativeTimeoutSupported: true,
	}
	first, changed, err := ResolveTarget(projection, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || first.Generation != 1 || first.TimeoutMode != core.TimeoutNative {
		t.Fatalf("unexpected first intent: %+v changed=%v", first, changed)
	}

	projection.ActiveCount = 1
	projection.Revision++
	second, changed, err := ResolveTarget(projection, policy, &first)
	if err != nil {
		t.Fatal(err)
	}
	if changed || second.Generation != first.Generation {
		t.Fatalf("explanatory projection change advanced generation: before=%+v after=%+v", first, second)
	}

	policy.Coverage = core.PolicyCoveragePartial
	policy.RelationDigest = "partial:/32"
	third, changed, err := ResolveTarget(projection, policy, &second)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || third.Generation != second.Generation+1 {
		t.Fatalf("policy meaning change did not advance generation: %+v", third)
	}
}

func TestResolveTargetPermanentIntentHasNoNativeTimeout(t *testing.T) {
	projection := core.DesiredBanProjection{
		NodeID:          "00112233445566778899aabbccddeeff",
		CanonicalTarget: netip.MustParsePrefix("2001:db8::/64"),
		State:           core.BanProjectionPresent,
		ActiveCount:     1,
		Revision:        1,
	}
	intent, _, err := ResolveTarget(projection, TargetPolicy{
		Coverage:               core.PolicyCoverageFull,
		RelationDigest:         "full",
		Scopes:                 core.ScopeInput | core.ScopeForward,
		NativeTimeoutSupported: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if intent.TimeoutMode != core.TimeoutNone || intent.EffectiveUntil != nil {
		t.Fatalf("permanent target must not carry native timeout: %+v", intent)
	}
	if intent.BanMembership != core.BanPresent || intent.PolicyCoverage != core.PolicyCoverageFull {
		t.Fatalf("allowlist coverage must remain orthogonal to ban membership: %+v", intent)
	}
}

func TestNativeExpiryForIntentAddsSafetyGraceOnlyToFiniteNativeTimeout(t *testing.T) {
	effectiveUntil := time.Unix(1_700_000_000, 0).UTC()
	finite := core.NormalizedTargetEnforcementIntent{
		EffectiveUntil: &effectiveUntil,
		TimeoutMode:    core.TimeoutNative,
	}
	nativeExpiry := NativeExpiryForIntent(finite)
	if nativeExpiry == nil || !nativeExpiry.Equal(effectiveUntil.Add(M0SafetyGrace)) {
		t.Fatalf("native expiry = %v", nativeExpiry)
	}
	if !finite.EffectiveUntil.Equal(effectiveUntil) {
		t.Fatalf("effective until was mutated: %v", finite.EffectiveUntil)
	}

	finite.TimeoutMode = core.TimeoutNone
	if expiry := NativeExpiryForIntent(finite); expiry != nil {
		t.Fatalf("non-native timeout received expiry %v", expiry)
	}
	finite.EffectiveUntil = nil
	finite.TimeoutMode = core.TimeoutNative
	if expiry := NativeExpiryForIntent(finite); expiry != nil {
		t.Fatalf("permanent target received expiry %v", expiry)
	}
}

func TestEquivalentDetectsEveryComparedAttribute(t *testing.T) {
	base := core.NormalizedTargetEnforcementIntent{
		NodeID:                  "00112233445566778899aabbccddeeff",
		CanonicalTarget:         netip.MustParsePrefix("192.0.2.4/32"),
		BanMembership:           core.BanPresent,
		Scopes:                  core.ScopeInput,
		AddressFamily:           core.AddressFamilyIPv4,
		PolicyCoverage:          core.PolicyCoverageNone,
		BackendAttributesDigest: "v1",
		Generation:              3,
	}
	other := base
	other.Generation = 99
	if !Equivalent(base, other) {
		t.Fatal("generation is a fence, not part of semantic equality")
	}

	expires := time.Unix(1_700_000_000, 0).UTC()
	tests := map[string]func(*core.NormalizedTargetEnforcementIntent){
		"membership":      func(intent *core.NormalizedTargetEnforcementIntent) { intent.BanMembership = core.BanAbsent },
		"effective until": func(intent *core.NormalizedTargetEnforcementIntent) { intent.EffectiveUntil = &expires },
		"timeout mode":    func(intent *core.NormalizedTargetEnforcementIntent) { intent.TimeoutMode = core.TimeoutNative },
		"scope":           func(intent *core.NormalizedTargetEnforcementIntent) { intent.Scopes = core.ScopeForward },
		"address family":  func(intent *core.NormalizedTargetEnforcementIntent) { intent.AddressFamily = core.AddressFamilyIPv6 },
		"policy coverage": func(intent *core.NormalizedTargetEnforcementIntent) {
			intent.PolicyCoverage = core.PolicyCoveragePartial
		},
		"policy digest":      func(intent *core.NormalizedTargetEnforcementIntent) { intent.PolicyRelationDigest = "partial" },
		"backend attributes": func(intent *core.NormalizedTargetEnforcementIntent) { intent.BackendAttributesDigest = "v2" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if Equivalent(base, changed) {
				t.Fatalf("%s drift was ignored", name)
			}
		})
	}
}

func TestResolveTargetAbsentClearsPolicyCoverage(t *testing.T) {
	projection := core.DesiredBanProjection{
		NodeID:          "00112233445566778899aabbccddeeff",
		CanonicalTarget: netip.MustParsePrefix("192.0.2.4/32"),
		State:           core.BanProjectionAbsent,
		Revision:        1,
	}
	intent, _, err := ResolveTarget(projection, TargetPolicy{
		Coverage:       core.PolicyCoverageFull,
		RelationDigest: "must-not-leak",
		Scopes:         core.ScopeInput,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if intent.PolicyCoverage != core.PolicyCoverageNone || intent.PolicyRelationDigest != "" {
		t.Fatalf("absent intent retained policy relation: %+v", intent)
	}
}

func TestResolveTargetRejectsGenerationOverflow(t *testing.T) {
	target := netip.MustParsePrefix("192.0.2.4/32")
	projection := core.DesiredBanProjection{
		NodeID:          "00112233445566778899aabbccddeeff",
		CanonicalTarget: target,
		State:           core.BanProjectionPresent,
		ActiveCount:     1,
		Revision:        2,
	}
	previous := core.NormalizedTargetEnforcementIntent{
		NodeID:          projection.NodeID,
		CanonicalTarget: target,
		BanMembership:   core.BanAbsent,
		Scopes:          core.ScopeInput,
		AddressFamily:   core.AddressFamilyIPv4,
		Generation:      ^core.TargetEnforcementGeneration(0),
	}
	_, _, err := ResolveTarget(projection, TargetPolicy{Coverage: core.PolicyCoverageNone, Scopes: core.ScopeInput}, &previous)
	if err == nil {
		t.Fatal("expected generation overflow to fail")
	}
}
