package decision

import (
	"context"
	"fmt"
	"strings"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/enforcement"
)

// ManagedPolicyReader is the optional transaction capability required by the
// standard resolver. Keeping it narrow lets Decision lifecycle callers retain
// their immutable/static policy resolver where appropriate.
type ManagedPolicyReader interface {
	ReadManagedPolicy(context.Context, core.NodeID) (core.PolicyRevision, core.ManagedPolicyIntent, error)
}

// ManagedPolicyTargetResolver derives Target Policy attributes from complete
// persisted Policy facts and immutable backend capability inputs.
type ManagedPolicyTargetResolver struct {
	scopes                  core.EnforcementScope
	nativeTimeoutSupported  bool
	backendAttributesDigest string
}

// NewManagedPolicyTargetResolver constructs the standard transaction-local
// resolver used by authoritative Policy writes.
func NewManagedPolicyTargetResolver(
	scopes core.EnforcementScope,
	nativeTimeoutSupported bool,
	backendAttributesDigest string,
) (*ManagedPolicyTargetResolver, error) {
	if scopes == 0 || scopes&^core.EnforcementScope(3) != 0 {
		return nil, fmt.Errorf("managed policy resolver scopes are invalid")
	}
	if len(backendAttributesDigest) != 64 || strings.Trim(backendAttributesDigest, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("managed policy resolver backend attributes digest is invalid")
	}
	return &ManagedPolicyTargetResolver{
		scopes: scopes, nativeTimeoutSupported: nativeTimeoutSupported,
		backendAttributesDigest: backendAttributesDigest,
	}, nil
}

// ResolveTargetPolicy reads Policy from the same transaction as the Target
// materialization and computes exact union coverage for that Target.
func (r *ManagedPolicyTargetResolver) ResolveTargetPolicy(
	ctx context.Context,
	tx DesiredStateTransaction,
	projection core.DesiredBanProjection,
) (enforcement.TargetPolicy, error) {
	if r == nil {
		return enforcement.TargetPolicy{}, fmt.Errorf("managed policy resolver is not initialized")
	}
	reader, ok := tx.(ManagedPolicyReader)
	if !ok {
		return enforcement.TargetPolicy{}, fmt.Errorf("desired state transaction cannot read managed policy")
	}
	_, policy, err := reader.ReadManagedPolicy(ctx, projection.NodeID)
	if err != nil {
		return enforcement.TargetPolicy{}, fmt.Errorf("read managed policy: %w", err)
	}
	resolution, err := ResolvePolicy(PolicyInput{
		Target: projection.CanonicalTarget, Allowlists: policy.Allowlist, ProtectedTargets: policy.ProtectedTargets,
	})
	if err != nil {
		return enforcement.TargetPolicy{}, fmt.Errorf("resolve managed policy relation: %w", err)
	}
	return enforcement.TargetPolicy{
		Coverage: resolution.Coverage, RelationDigest: resolution.RelationDigest,
		Scopes: r.scopes, NativeTimeoutSupported: r.nativeTimeoutSupported,
		BackendAttributesDigest: r.backendAttributesDigest,
	}, nil
}

var _ TargetPolicyResolver = (*ManagedPolicyTargetResolver)(nil)
