//go:build linux

package nftables

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
)

func TestFixedManagedPolicyTargetResolverUsesNativeLayoutAuthority(t *testing.T) {
	resolver, err := NewFixedManagedPolicyTargetResolver()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := core.NewManagedPolicyIntent([]netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	})
	if err != nil {
		t.Fatal(err)
	}
	projection := core.DesiredBanProjection{
		NodeID: "00112233445566778899aabbccddeeff", CanonicalTarget: netip.MustParsePrefix("198.51.100.4/32"),
		State: core.BanProjectionPresent, ActiveCount: 1, Revision: 1,
	}
	resolved, err := resolver.ResolveTargetPolicy(context.Background(), &policyResolverTransaction{policy: policy}, projection)
	if err != nil {
		t.Fatal(err)
	}
	_, infrastructure := FixedDesiredInfrastructure()
	if resolved.Scopes != core.ScopeInput|core.ScopeForward || !resolved.NativeTimeoutSupported ||
		resolved.BackendAttributesDigest != infrastructure.Digest || resolved.RelationDigest == "" {
		t.Fatalf("resolved Policy = %#v", resolved)
	}
}

type policyResolverTransaction struct{ policy core.ManagedPolicyIntent }

func (t *policyResolverTransaction) ReadManagedPolicy(context.Context, core.NodeID) (core.PolicyRevision, core.ManagedPolicyIntent, error) {
	return 1, t.policy, nil
}
func (*policyResolverTransaction) ListActiveDecisions(context.Context, core.NodeID, netip.Prefix) ([]core.Decision, error) {
	return nil, nil
}
func (*policyResolverTransaction) FindDecisionProjection(context.Context, core.NodeID, netip.Prefix) (core.DesiredBanProjection, bool, error) {
	return core.DesiredBanProjection{}, false, nil
}
func (*policyResolverTransaction) PutDecisionProjection(context.Context, core.DesiredBanProjection, time.Time) error {
	return nil
}
func (*policyResolverTransaction) FindTargetEnforcementIntent(context.Context, core.NodeID, netip.Prefix) (core.NormalizedTargetEnforcementIntent, bool, error) {
	return core.NormalizedTargetEnforcementIntent{}, false, nil
}
func (*policyResolverTransaction) TargetEnforcementGenerationFloor(context.Context, core.NodeID, netip.Prefix) (core.TargetEnforcementGeneration, bool, error) {
	return 0, false, nil
}
func (*policyResolverTransaction) PutTargetEnforcementIntent(context.Context, core.NormalizedTargetEnforcementIntent) error {
	return nil
}
func (*policyResolverTransaction) ResetTargetReconcileState(context.Context, core.NodeID, netip.Prefix, core.TargetEnforcementGeneration, time.Time) error {
	return nil
}
func (*policyResolverTransaction) AdvanceSnapshotRevision(context.Context) (core.SnapshotRevision, error) {
	return 0, nil
}

var _ decision.DesiredStateTransaction = (*policyResolverTransaction)(nil)
