package decision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"

	"github.com/lifei6671/guard-wall/internal/core"
)

// PolicyInput contains the independent Allowlist and Protected Target facts applied after the
// Decision projection is built. These facts never create, revoke, or expire a Decision.
type PolicyInput struct {
	Target           netip.Prefix
	Allowlists       []netip.Prefix
	ProtectedTargets []netip.Prefix
}

// PolicyResolution is the stable range relationship for one projected target.
type PolicyResolution struct {
	Coverage       core.PolicyCoverage
	RelationDigest string
}

type policyRange struct {
	kind   string
	prefix netip.Prefix
}

// ResolvePolicy calculates the union coverage and an order-independent digest of all overlapping
// policy ranges. Multiple narrower CIDRs may together fully cover a target.
func ResolvePolicy(input PolicyInput) (PolicyResolution, error) {
	if !input.Target.IsValid() || input.Target != input.Target.Masked() {
		return PolicyResolution{}, fmt.Errorf("policy target must be canonical")
	}
	relevant := make([]policyRange, 0, len(input.Allowlists)+len(input.ProtectedTargets))
	for _, group := range []struct {
		kind   string
		ranges []netip.Prefix
	}{
		{kind: "allowlist", ranges: input.Allowlists},
		{kind: "protected", ranges: input.ProtectedTargets},
	} {
		for _, current := range group.ranges {
			if !current.IsValid() || current != current.Masked() {
				return PolicyResolution{}, fmt.Errorf("%s range must be canonical", group.kind)
			}
			if prefixesOverlap(input.Target, current) {
				relevant = append(relevant, policyRange{kind: group.kind, prefix: current})
			}
		}
	}
	if len(relevant) == 0 {
		return PolicyResolution{Coverage: core.PolicyCoverageNone}, nil
	}
	relevant = canonicalPolicyRanges(relevant)
	union := make([]netip.Prefix, len(relevant))
	for index, current := range relevant {
		union[index] = current.prefix
	}
	coverage := core.PolicyCoveragePartial
	if prefixFullyCovered(input.Target, union) {
		coverage = core.PolicyCoverageFull
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "target:%s\ncoverage:%d\n", input.Target, coverage)
	for _, current := range relevant {
		fmt.Fprintf(hash, "%s:%s\n", current.kind, current.prefix)
	}
	return PolicyResolution{Coverage: coverage, RelationDigest: hex.EncodeToString(hash.Sum(nil))}, nil
}

func canonicalPolicyRanges(ranges []policyRange) []policyRange {
	sort.Slice(ranges, func(left, right int) bool {
		if ranges[left].kind != ranges[right].kind {
			return ranges[left].kind < ranges[right].kind
		}
		return ranges[left].prefix.String() < ranges[right].prefix.String()
	})
	result := ranges[:0]
	for _, current := range ranges {
		if len(result) != 0 && result[len(result)-1] == current {
			continue
		}
		result = append(result, current)
	}
	return result
}

func prefixFullyCovered(target netip.Prefix, ranges []netip.Prefix) bool {
	for _, current := range ranges {
		if current.Addr().BitLen() == target.Addr().BitLen() && current.Bits() <= target.Bits() && current.Contains(target.Addr()) {
			return true
		}
	}
	if target.Bits() == target.Addr().BitLen() {
		return false
	}
	left, right := splitPrefix(target)
	return branchHasPolicy(left, ranges) && branchHasPolicy(right, ranges) &&
		prefixFullyCovered(left, ranges) && prefixFullyCovered(right, ranges)
}

func branchHasPolicy(branch netip.Prefix, ranges []netip.Prefix) bool {
	for _, current := range ranges {
		if prefixesOverlap(branch, current) {
			return true
		}
	}
	return false
}

func prefixesOverlap(left, right netip.Prefix) bool {
	if left.Addr().BitLen() != right.Addr().BitLen() {
		return false
	}
	return left.Contains(right.Addr()) || right.Contains(left.Addr())
}

func splitPrefix(prefix netip.Prefix) (netip.Prefix, netip.Prefix) {
	nextBits := prefix.Bits() + 1
	left := netip.PrefixFrom(prefix.Addr(), nextBits).Masked()
	bytes := append([]byte(nil), prefix.Addr().AsSlice()...)
	bit := prefix.Bits()
	bytes[bit/8] |= byte(1 << (7 - bit%8))
	rightAddress, ok := netip.AddrFromSlice(bytes)
	if !ok {
		panic("validated address could not be split")
	}
	right := netip.PrefixFrom(rightAddress, nextBits).Masked()
	return left, right
}
