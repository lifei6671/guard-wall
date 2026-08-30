// Package enforcement resolves business projections into normalized Firewall intent.
package enforcement

import (
	"fmt"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

// M0SafetyGrace is the frozen Fake Slice failsafe added to finite native
// Firewall timeouts. Decision expiry remains authoritative.
const M0SafetyGrace = 5 * time.Minute

// TargetPolicy contains the Firewall-significant policy and backend attributes for one target.
type TargetPolicy struct {
	Coverage                core.PolicyCoverage
	RelationDigest          string
	Scopes                  core.EnforcementScope
	NativeTimeoutSupported  bool
	BackendAttributesDigest string
}

// ResolveTarget calculates normalized intent and advances generation only when its physical
// Firewall meaning changes. The first materialized intent starts at generation one.
func ResolveTarget(
	projection core.DesiredBanProjection,
	policy TargetPolicy,
	previous *core.NormalizedTargetEnforcementIntent,
) (core.NormalizedTargetEnforcementIntent, bool, error) {
	if err := projection.Validate(); err != nil {
		return core.NormalizedTargetEnforcementIntent{}, false, fmt.Errorf("validate projection: %w", err)
	}
	if policy.Scopes == 0 || policy.Scopes&^(core.ScopeInput|core.ScopeForward) != 0 {
		return core.NormalizedTargetEnforcementIntent{}, false, fmt.Errorf("enforcement scopes are invalid")
	}
	if policy.Coverage != core.PolicyCoverageNone && policy.Coverage != core.PolicyCoveragePartial && policy.Coverage != core.PolicyCoverageFull {
		return core.NormalizedTargetEnforcementIntent{}, false, fmt.Errorf("policy coverage is invalid")
	}

	intent := core.NormalizedTargetEnforcementIntent{
		NodeID:                  projection.NodeID,
		CanonicalTarget:         projection.CanonicalTarget,
		BanMembership:           core.BanAbsent,
		TimeoutMode:             core.TimeoutNone,
		Scopes:                  policy.Scopes,
		AddressFamily:           addressFamily(projection.CanonicalTarget.Addr().Is4()),
		PolicyCoverage:          policy.Coverage,
		PolicyRelationDigest:    policy.RelationDigest,
		BackendAttributesDigest: policy.BackendAttributesDigest,
	}
	if projection.State == core.BanProjectionPresent {
		intent.BanMembership = core.BanPresent
		intent.EffectiveUntil = cloneTime(projection.EffectiveUntil)
		if projection.EffectiveUntil != nil && policy.NativeTimeoutSupported {
			intent.TimeoutMode = core.TimeoutNative
		}
	} else {
		intent.PolicyCoverage = core.PolicyCoverageNone
		intent.PolicyRelationDigest = ""
	}

	if previous == nil {
		intent.Generation = 1
		if err := intent.Validate(); err != nil {
			return core.NormalizedTargetEnforcementIntent{}, false, fmt.Errorf("validate resolved intent: %w", err)
		}
		return intent, true, nil
	}
	if err := previous.Validate(); err != nil {
		return core.NormalizedTargetEnforcementIntent{}, false, fmt.Errorf("validate previous intent: %w", err)
	}
	if previous.NodeID != intent.NodeID || previous.CanonicalTarget != intent.CanonicalTarget {
		return core.NormalizedTargetEnforcementIntent{}, false, fmt.Errorf("previous intent belongs to another target")
	}
	intent.Generation = previous.Generation
	changed := !Equivalent(*previous, intent)
	if changed {
		if previous.Generation == ^core.TargetEnforcementGeneration(0) {
			return core.NormalizedTargetEnforcementIntent{}, false, fmt.Errorf("target generation is exhausted")
		}
		intent.Generation++
	}
	if err := intent.Validate(); err != nil {
		return core.NormalizedTargetEnforcementIntent{}, false, fmt.Errorf("validate resolved intent: %w", err)
	}
	return intent, changed, nil
}

// Equivalent compares every Firewall-significant target attribute and ignores generation.
func Equivalent(left, right core.NormalizedTargetEnforcementIntent) bool {
	return left.NodeID == right.NodeID &&
		left.CanonicalTarget == right.CanonicalTarget &&
		left.BanMembership == right.BanMembership &&
		equalTime(left.EffectiveUntil, right.EffectiveUntil) &&
		left.TimeoutMode == right.TimeoutMode &&
		left.Scopes == right.Scopes &&
		left.AddressFamily == right.AddressFamily &&
		left.PolicyCoverage == right.PolicyCoverage &&
		left.PolicyRelationDigest == right.PolicyRelationDigest &&
		left.BackendAttributesDigest == right.BackendAttributesDigest
}

// NativeExpiryForIntent derives the physical native timeout without changing
// the Decision-owned EffectiveUntil value.
func NativeExpiryForIntent(intent core.NormalizedTargetEnforcementIntent) *time.Time {
	if intent.TimeoutMode != core.TimeoutNative || intent.EffectiveUntil == nil {
		return nil
	}
	expiry := intent.EffectiveUntil.Add(M0SafetyGrace)
	return &expiry
}

func addressFamily(ipv4 bool) core.AddressFamily {
	if ipv4 {
		return core.AddressFamilyIPv4
	}
	return core.AddressFamilyIPv6
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
