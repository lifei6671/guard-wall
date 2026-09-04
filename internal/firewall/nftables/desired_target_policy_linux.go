//go:build linux

package nftables

import (
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
)

// NewFixedManagedPolicyTargetResolver constructs the immutable TargetPolicy
// authority for the fixed native nftables layout without probing the host.
func NewFixedManagedPolicyTargetResolver() (*decision.ManagedPolicyTargetResolver, error) {
	_, infrastructure := FixedDesiredInfrastructure()
	return decision.NewManagedPolicyTargetResolver(
		core.ScopeInput|core.ScopeForward,
		true,
		infrastructure.Digest,
	)
}
