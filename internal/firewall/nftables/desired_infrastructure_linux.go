//go:build linux

package nftables

import (
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall"
)

const fixedInfrastructureRevision core.InfrastructureRevision = 1

// FixedDesiredInfrastructure returns the only Desired identity accepted by
// the fixed native nftables layout.
func FixedDesiredInfrastructure() (core.InfrastructureRevision, core.ManagedInfrastructureIntent) {
	return fixedInfrastructureRevision, core.ManagedInfrastructureIntent{
		Backend:      string(firewall.BackendKindNftablesNative),
		OwnerVersion: firewall.ManagedOwnerVersionV1,
		Digest:       sha256hex([]byte(infrastructureV)),
	}
}

// MatchesFixedDesiredInfrastructure reports whether a Reconcile Infrastructure
// plan names this exact native layout and layout revision.
func MatchesFixedDesiredInfrastructure(
	revision core.InfrastructureRevision,
	intent core.ManagedInfrastructureIntent,
) bool {
	fixedRevision, fixedIntent := FixedDesiredInfrastructure()
	return revision == fixedRevision && intent == fixedIntent
}

// MatchesFixedInfrastructureObservation reports whether present managed
// infrastructure was observed as this exact native layout.
func MatchesFixedInfrastructureObservation(observation firewall.InfrastructureObservation) bool {
	_, intent := FixedDesiredInfrastructure()
	return observation.Backend() == firewall.BackendKindNftablesNative &&
		observation.OwnerVersion() == intent.OwnerVersion &&
		observation.SchemaVersion() == firewall.ManagedInfrastructureSchemaVersionV1 &&
		observation.Digest() == intent.Digest
}
