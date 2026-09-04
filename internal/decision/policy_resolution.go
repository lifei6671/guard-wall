package decision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/lifei6671/guard-wall/internal/core"
)

// PolicyInput contains the Policy facts needed to resolve one Target's
// allow/protected coverage. Input order and duplicate prefixes are irrelevant.
type PolicyInput struct {
	Target           netip.Prefix
	Allowlists       []netip.Prefix
	ProtectedTargets []netip.Prefix
}

// PolicyResolution is the deterministic Policy relationship for one Target.
type PolicyResolution struct {
	Coverage       core.PolicyCoverage
	RelationDigest string
}

// ResolvePolicy computes exact union coverage without allowing a single
// partial prefix to overstate a Target's protection relationship.
func ResolvePolicy(input PolicyInput) (PolicyResolution, error) {
	if !input.Target.IsValid() || input.Target != input.Target.Masked() {
		return PolicyResolution{}, fmt.Errorf("policy target must be canonical")
	}
	allowlist, err := canonicalPolicyRelationPrefixes("allowlist", input.Allowlists)
	if err != nil {
		return PolicyResolution{}, err
	}
	protected, err := canonicalPolicyRelationPrefixes("protected targets", input.ProtectedTargets)
	if err != nil {
		return PolicyResolution{}, err
	}
	related := make([]netip.Prefix, 0, len(allowlist)+len(protected))
	related = append(related, allowlist...)
	related = append(related, protected...)
	if len(related) == 0 || !anyPolicyOverlap(input.Target, related) {
		return PolicyResolution{Coverage: core.PolicyCoverageNone}, nil
	}
	resolution := PolicyResolution{Coverage: core.PolicyCoveragePartial}
	if policyUnionCovers(input.Target, related) {
		resolution.Coverage = core.PolicyCoverageFull
	}
	resolution.RelationDigest = policyRelationDigest(input.Target, allowlist, protected)
	return resolution, nil
}

func canonicalPolicyRelationPrefixes(name string, prefixes []netip.Prefix) ([]netip.Prefix, error) {
	prepared := append([]netip.Prefix(nil), prefixes...)
	for _, prefix := range prepared {
		if !prefix.IsValid() || prefix != prefix.Masked() {
			return nil, fmt.Errorf("policy %s contains a non-canonical prefix", name)
		}
	}
	sort.Slice(prepared, func(left, right int) bool { return prepared[left].String() < prepared[right].String() })
	write := 0
	for _, prefix := range prepared {
		if write != 0 && prepared[write-1] == prefix {
			continue
		}
		prepared[write] = prefix
		write++
	}
	return prepared[:write], nil
}

func anyPolicyOverlap(target netip.Prefix, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Addr().BitLen() == target.Addr().BitLen() && prefix.Overlaps(target) {
			return true
		}
	}
	return false
}

func policyUnionCovers(target netip.Prefix, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Addr().BitLen() == target.Addr().BitLen() && prefix.Bits() <= target.Bits() && prefix.Contains(target.Addr()) {
			return true
		}
	}
	if !anyPolicyOverlap(target, prefixes) || target.Bits() == target.Addr().BitLen() {
		return false
	}
	first, second := splitPolicyPrefix(target)
	return policyUnionCovers(first, prefixes) && policyUnionCovers(second, prefixes)
}

func splitPolicyPrefix(prefix netip.Prefix) (netip.Prefix, netip.Prefix) {
	bits := prefix.Bits()
	first := netip.PrefixFrom(prefix.Addr(), bits+1).Masked()
	if prefix.Addr().Is4() {
		v4 := prefix.Addr().As4()
		v4[bits/8] |= 1 << uint(7-bits%8)
		return first, netip.PrefixFrom(netip.AddrFrom4(v4), bits+1).Masked()
	}
	address := prefix.Addr().As16()
	address[bits/8] |= 1 << uint(7-bits%8)
	return first, netip.PrefixFrom(netip.AddrFrom16(address), bits+1).Masked()
}

func policyRelationDigest(target netip.Prefix, allowlist, protected []netip.Prefix) string {
	parts := []string{"target:" + target.String()}
	for _, prefix := range allowlist {
		parts = append(parts, "allow:"+prefix.String())
	}
	for _, prefix := range protected {
		parts = append(parts, "protected:"+prefix.String())
	}
	sort.Strings(parts)
	digest := sha256.Sum256([]byte("guard-policy-relation/v1\n" + strings.Join(parts, "\n")))
	return hex.EncodeToString(digest[:])
}
