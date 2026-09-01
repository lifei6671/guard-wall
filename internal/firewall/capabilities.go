// Package firewall defines platform-neutral Firewall domain contracts.
package firewall

import "strings"

const maxToolVersionBytes = 128

// BackendKind identifies the selected physical Firewall implementation.
type BackendKind string

const (
	// BackendKindNftablesNative identifies direct nftables control.
	BackendKindNftablesNative BackendKind = "nftables-native"
	// BackendKindIptablesNFT identifies iptables using the nf_tables backend.
	BackendKindIptablesNFT BackendKind = "iptables-nft"
	// BackendKindIptablesLegacy identifies the legacy iptables backend.
	BackendKindIptablesLegacy BackendKind = "iptables-legacy"
)

// FirewallCapabilitiesSpec is the validated-constructor input for one Probe result.
// It is not a validated capability authority and must not be persisted or sent
// across a runtime or wire boundary.
// UFWIntegrationProven and DockerIntegrationProven mean that safe coexistence
// was proved for the current topology, not merely that a process was detected.
type FirewallCapabilitiesSpec struct {
	Backend                 BackendKind
	ToolVersion             string
	IPv4                    bool
	IPv6                    bool
	CIDR                    bool
	NativeSet               bool
	NativeTimeout           bool
	CrashSafeExpiry         bool
	AtomicBatch             bool
	HostInput               bool
	Forward                 bool
	UFWIntegrationProven    bool
	DockerIntegrationProven bool
	OwnershipProven         bool
	MutationReady           bool
}

// FirewallCapabilities is an immutable, platform-neutral Firewall Probe result.
// Its zero value is invalid and values must be constructed with
// NewFirewallCapabilities.
type FirewallCapabilities struct {
	backend                 BackendKind
	toolVersion             string
	ipv4                    bool
	ipv6                    bool
	cidr                    bool
	nativeSet               bool
	nativeTimeout           bool
	crashSafeExpiry         bool
	atomicBatch             bool
	hostInput               bool
	forward                 bool
	ufwIntegrationProven    bool
	dockerIntegrationProven bool
	ownershipProven         bool
	mutationReady           bool
}

// NewFirewallCapabilities validates and owns one capability snapshot.
func NewFirewallCapabilities(spec FirewallCapabilitiesSpec) (FirewallCapabilities, error) {
	capabilities := FirewallCapabilities{
		backend:                 spec.Backend,
		toolVersion:             spec.ToolVersion,
		ipv4:                    spec.IPv4,
		ipv6:                    spec.IPv6,
		cidr:                    spec.CIDR,
		nativeSet:               spec.NativeSet,
		nativeTimeout:           spec.NativeTimeout,
		crashSafeExpiry:         spec.CrashSafeExpiry,
		atomicBatch:             spec.AtomicBatch,
		hostInput:               spec.HostInput,
		forward:                 spec.Forward,
		ufwIntegrationProven:    spec.UFWIntegrationProven,
		dockerIntegrationProven: spec.DockerIntegrationProven,
		ownershipProven:         spec.OwnershipProven,
		mutationReady:           spec.MutationReady,
	}
	if err := capabilities.Validate(); err != nil {
		return FirewallCapabilities{}, err
	}
	return capabilities, nil
}

// Validate rejects incomplete or internally contradictory capability snapshots.
func (c FirewallCapabilities) Validate() error {
	if !validBackendKind(c.backend) || !validToolVersion(c.toolVersion) ||
		(!c.ipv4 && !c.ipv6) || (!c.hostInput && !c.forward) ||
		(c.nativeTimeout && !c.nativeSet) ||
		(c.crashSafeExpiry && !c.nativeTimeout) ||
		(c.dockerIntegrationProven && !c.forward) ||
		(c.mutationReady && (!c.ownershipProven || !c.cidr)) ||
		(c.backend == BackendKindNftablesNative && c.mutationReady && (!c.nativeSet || !c.atomicBatch)) {
		return invalidCapabilitiesError{}
	}
	return nil
}

// Backend returns the selected physical Firewall implementation.
func (c FirewallCapabilities) Backend() BackendKind { return c.backend }

// ToolVersion returns the bounded, printable version identity reported by Probe.
func (c FirewallCapabilities) ToolVersion() string { return c.toolVersion }

// SupportsIPv4 reports whether IPv4 rules are supported.
func (c FirewallCapabilities) SupportsIPv4() bool { return c.ipv4 }

// SupportsIPv6 reports whether IPv6 rules are supported.
func (c FirewallCapabilities) SupportsIPv6() bool { return c.ipv6 }

// SupportsCIDR reports whether CIDR targets are supported.
func (c FirewallCapabilities) SupportsCIDR() bool { return c.cidr }

// SupportsNativeSet reports whether the backend has a native set primitive.
func (c FirewallCapabilities) SupportsNativeSet() bool { return c.nativeSet }

// SupportsNativeTimeout reports whether native set elements can expire.
func (c FirewallCapabilities) SupportsNativeTimeout() bool { return c.nativeTimeout }

// SupportsCrashSafeExpiry reports whether expiry survives process failure.
func (c FirewallCapabilities) SupportsCrashSafeExpiry() bool { return c.crashSafeExpiry }

// SupportsAtomicBatch reports whether one mutation batch is atomic.
func (c FirewallCapabilities) SupportsAtomicBatch() bool { return c.atomicBatch }

// SupportsHostInput reports whether host INPUT policy is supported.
func (c FirewallCapabilities) SupportsHostInput() bool { return c.hostInput }

// SupportsForward reports whether FORWARD policy is supported.
func (c FirewallCapabilities) SupportsForward() bool { return c.forward }

// UFWIntegrationProven reports whether safe UFW coexistence was proved.
func (c FirewallCapabilities) UFWIntegrationProven() bool { return c.ufwIntegrationProven }

// DockerIntegrationProven reports whether safe Docker coexistence was proved.
func (c FirewallCapabilities) DockerIntegrationProven() bool { return c.dockerIntegrationProven }

// OwnershipProven reports whether Guard mutation ownership was proved.
func (c FirewallCapabilities) OwnershipProven() bool { return c.ownershipProven }

// MutationReady reports whether the validated snapshot permits mutation.
func (c FirewallCapabilities) MutationReady() bool { return c.mutationReady }

func validBackendKind(kind BackendKind) bool {
	switch kind {
	case BackendKindNftablesNative, BackendKindIptablesNFT, BackendKindIptablesLegacy:
		return true
	default:
		return false
	}
}

func validToolVersion(version string) bool {
	if len(version) == 0 || len(version) > maxToolVersionBytes || strings.TrimSpace(version) != version {
		return false
	}
	for index := 0; index < len(version); index++ {
		if version[index] < 0x20 || version[index] > 0x7e {
			return false
		}
	}
	return true
}

type invalidCapabilitiesError struct{}

func (invalidCapabilitiesError) Error() string {
	return "firewall capabilities are invalid"
}
