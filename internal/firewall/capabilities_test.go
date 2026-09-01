package firewall_test

import (
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

const invalidCapabilitiesMessage = "firewall capabilities are invalid"

func TestFirewallCapabilitiesBackendKindsAreClosed(t *testing.T) {
	tests := []struct {
		name string
		kind firewall.BackendKind
		wire string
	}{
		{name: "nftables native", kind: firewall.BackendKindNftablesNative, wire: "nftables-native"},
		{name: "iptables nft", kind: firewall.BackendKindIptablesNFT, wire: "iptables-nft"},
		{name: "iptables legacy", kind: firewall.BackendKindIptablesLegacy, wire: "iptables-legacy"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if got := string(test.kind); got != test.wire {
				t.Fatalf("backend kind = %q, want %q", got, test.wire)
			}
			spec := minimalFirewallCapabilitiesSpec(test.kind)
			capabilities, err := firewall.NewFirewallCapabilities(spec)
			if err != nil {
				t.Fatalf("NewFirewallCapabilities(%q): %v", test.kind, err)
			}
			if capabilities.Backend() != test.kind {
				t.Fatalf("Backend() = %q, want %q", capabilities.Backend(), test.kind)
			}
			if err := capabilities.Validate(); err != nil {
				t.Fatalf("Validate(%q): %v", test.kind, err)
			}
		})
	}
}

func TestFirewallCapabilitiesCompleteSnapshotGetters(t *testing.T) {
	spec := completeFirewallCapabilitiesSpec()
	capabilities, err := firewall.NewFirewallCapabilities(spec)
	if err != nil {
		t.Fatalf("NewFirewallCapabilities(): %v", err)
	}
	if err := capabilities.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}

	if got := capabilities.Backend(); got != spec.Backend {
		t.Fatalf("Backend() = %q, want %q", got, spec.Backend)
	}
	if got := capabilities.ToolVersion(); got != spec.ToolVersion {
		t.Fatalf("ToolVersion() = %q, want %q", got, spec.ToolVersion)
	}
	assertCapabilityBool(t, "SupportsIPv4", capabilities.SupportsIPv4(), spec.IPv4)
	assertCapabilityBool(t, "SupportsIPv6", capabilities.SupportsIPv6(), spec.IPv6)
	assertCapabilityBool(t, "SupportsCIDR", capabilities.SupportsCIDR(), spec.CIDR)
	assertCapabilityBool(t, "SupportsNativeSet", capabilities.SupportsNativeSet(), spec.NativeSet)
	assertCapabilityBool(t, "SupportsNativeTimeout", capabilities.SupportsNativeTimeout(), spec.NativeTimeout)
	assertCapabilityBool(t, "SupportsCrashSafeExpiry", capabilities.SupportsCrashSafeExpiry(), spec.CrashSafeExpiry)
	assertCapabilityBool(t, "SupportsAtomicBatch", capabilities.SupportsAtomicBatch(), spec.AtomicBatch)
	assertCapabilityBool(t, "SupportsHostInput", capabilities.SupportsHostInput(), spec.HostInput)
	assertCapabilityBool(t, "SupportsForward", capabilities.SupportsForward(), spec.Forward)
	assertCapabilityBool(t, "UFWIntegrationProven", capabilities.UFWIntegrationProven(), spec.UFWIntegrationProven)
	assertCapabilityBool(t, "DockerIntegrationProven", capabilities.DockerIntegrationProven(), spec.DockerIntegrationProven)
	assertCapabilityBool(t, "OwnershipProven", capabilities.OwnershipProven(), spec.OwnershipProven)
	assertCapabilityBool(t, "MutationReady", capabilities.MutationReady(), spec.MutationReady)
}

func TestFirewallCapabilitiesOwnsConstructorInput(t *testing.T) {
	spec := completeFirewallCapabilitiesSpec()
	capabilities, err := firewall.NewFirewallCapabilities(spec)
	if err != nil {
		t.Fatalf("NewFirewallCapabilities(): %v", err)
	}

	spec.Backend = firewall.BackendKindIptablesLegacy
	spec.ToolVersion = "mutated caller version"
	spec.IPv4 = false
	spec.IPv6 = false
	spec.CIDR = false
	spec.NativeSet = false
	spec.NativeTimeout = false
	spec.CrashSafeExpiry = false
	spec.AtomicBatch = false
	spec.HostInput = false
	spec.Forward = false
	spec.UFWIntegrationProven = false
	spec.DockerIntegrationProven = false
	spec.OwnershipProven = false
	spec.MutationReady = false

	if got := capabilities.Backend(); got != firewall.BackendKindNftablesNative {
		t.Fatalf("Backend() after caller mutation = %q, want nftables-native", got)
	}
	if got := capabilities.ToolVersion(); got != "nftables v1.1.0" {
		t.Fatalf("ToolVersion() after caller mutation = %q, want original", got)
	}
	for name, got := range map[string]bool{
		"IPv4": capabilities.SupportsIPv4(), "IPv6": capabilities.SupportsIPv6(),
		"CIDR": capabilities.SupportsCIDR(), "NativeSet": capabilities.SupportsNativeSet(),
		"NativeTimeout":   capabilities.SupportsNativeTimeout(),
		"CrashSafeExpiry": capabilities.SupportsCrashSafeExpiry(),
		"AtomicBatch":     capabilities.SupportsAtomicBatch(), "HostInput": capabilities.SupportsHostInput(),
		"Forward": capabilities.SupportsForward(), "UFW": capabilities.UFWIntegrationProven(),
		"Docker": capabilities.DockerIntegrationProven(), "Ownership": capabilities.OwnershipProven(),
		"MutationReady": capabilities.MutationReady(),
	} {
		if !got {
			t.Fatalf("%s getter changed after caller mutation", name)
		}
	}
	if err := capabilities.Validate(); err != nil {
		t.Fatalf("Validate() after caller mutation: %v", err)
	}
}

func TestFirewallCapabilitiesToolVersionBoundary(t *testing.T) {
	tests := []struct {
		name    string
		version string
		valid   bool
	}{
		{name: "one byte", version: "v", valid: true},
		{name: "printable interior space", version: "iptables v1.8.10 (nf_tables)", valid: true},
		{name: "exact 128 bytes", version: strings.Repeat("v", 128), valid: true},
		{name: "empty", version: ""},
		{name: "one over 128 bytes", version: strings.Repeat("v", 129)},
		{name: "leading space", version: " nftables v1.1.0"},
		{name: "trailing space", version: "nftables v1.1.0 "},
		{name: "newline", version: "nftables\nv1.1.0"},
		{name: "tab", version: "nftables\tv1.1.0"},
		{name: "DEL", version: "nftables\x7fv1.1.0"},
		{name: "non ASCII", version: "nftables-版本"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			spec := minimalFirewallCapabilitiesSpec(firewall.BackendKindNftablesNative)
			spec.ToolVersion = test.version
			capabilities, err := firewall.NewFirewallCapabilities(spec)
			if test.valid {
				if err != nil {
					t.Fatalf("NewFirewallCapabilities() exact version rejected: %v", err)
				}
				if got := capabilities.ToolVersion(); got != test.version {
					t.Fatalf("ToolVersion() = %q, want exact input", got)
				}
				return
			}
			assertInvalidFirewallCapabilities(t, capabilities, err, test.version)
		})
	}
}

func TestFirewallCapabilitiesAddressFamiliesAndScopes(t *testing.T) {
	valid := []struct {
		name      string
		ipv4      bool
		ipv6      bool
		hostInput bool
		forward   bool
		cidr      bool
	}{
		{name: "IPv4 input", ipv4: true, hostInput: true},
		{name: "IPv6 forward CIDR", ipv6: true, forward: true, cidr: true},
		{name: "dual stack both scopes", ipv4: true, ipv6: true, hostInput: true, forward: true, cidr: true},
	}
	for _, test := range valid {
		test := test
		t.Run("valid/"+test.name, func(t *testing.T) {
			spec := minimalFirewallCapabilitiesSpec(firewall.BackendKindIptablesNFT)
			spec.IPv4, spec.IPv6, spec.CIDR = test.ipv4, test.ipv6, test.cidr
			spec.HostInput, spec.Forward = test.hostInput, test.forward
			capabilities, err := firewall.NewFirewallCapabilities(spec)
			if err != nil {
				t.Fatalf("NewFirewallCapabilities(): %v", err)
			}
			assertCapabilityBool(t, "SupportsIPv4", capabilities.SupportsIPv4(), test.ipv4)
			assertCapabilityBool(t, "SupportsIPv6", capabilities.SupportsIPv6(), test.ipv6)
			assertCapabilityBool(t, "SupportsCIDR", capabilities.SupportsCIDR(), test.cidr)
			assertCapabilityBool(t, "SupportsHostInput", capabilities.SupportsHostInput(), test.hostInput)
			assertCapabilityBool(t, "SupportsForward", capabilities.SupportsForward(), test.forward)
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*firewall.FirewallCapabilitiesSpec)
	}{
		{
			name: "no IP family",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.IPv4, spec.IPv6 = false, false
			},
		},
		{
			name: "CIDR without IP family",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.IPv4, spec.IPv6, spec.CIDR = false, false, true
			},
		},
		{
			name: "no packet path scope",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.HostInput, spec.Forward = false, false
			},
		},
	} {
		t.Run("invalid/"+test.name, func(t *testing.T) {
			spec := minimalFirewallCapabilitiesSpec(firewall.BackendKindIptablesNFT)
			test.mutate(&spec)
			capabilities, err := firewall.NewFirewallCapabilities(spec)
			assertInvalidFirewallCapabilities(t, capabilities, err, spec.ToolVersion)
		})
	}
}

func TestFirewallCapabilitiesDependencyValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*firewall.FirewallCapabilitiesSpec)
	}{
		{
			name: "native timeout requires native set",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.NativeSet = false
			},
		},
		{
			name: "crash safe expiry requires native timeout",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.NativeTimeout = false
				spec.CrashSafeExpiry = true
			},
		},
		{
			name: "Docker integration requires Forward",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.Forward = false
				spec.DockerIntegrationProven = true
			},
		},
		{
			name: "mutation readiness requires ownership proof",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.OwnershipProven = false
			},
		},
		{
			name: "mutation readiness requires CIDR",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.CIDR = false
			},
		},
		{
			name: "ready nftables native requires native set",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.NativeSet = false
				spec.NativeTimeout = false
				spec.CrashSafeExpiry = false
			},
		},
		{
			name: "ready nftables native requires atomic batch",
			mutate: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.AtomicBatch = false
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			spec := completeFirewallCapabilitiesSpec()
			test.mutate(&spec)
			capabilities, err := firewall.NewFirewallCapabilities(spec)
			assertInvalidFirewallCapabilities(t, capabilities, err, spec.ToolVersion)
		})
	}
}

func TestFirewallCapabilitiesUFWIntegrationDoesNotRequireHostInput(t *testing.T) {
	spec := minimalFirewallCapabilitiesSpec(firewall.BackendKindIptablesNFT)
	spec.HostInput = false
	spec.Forward = true
	spec.UFWIntegrationProven = true

	capabilities, err := firewall.NewFirewallCapabilities(spec)
	if err != nil {
		t.Fatalf("NewFirewallCapabilities(): %v", err)
	}
	if !capabilities.UFWIntegrationProven() {
		t.Fatal("UFWIntegrationProven() = false, want true")
	}
	if capabilities.SupportsHostInput() || !capabilities.SupportsForward() {
		t.Fatal("UFW integration proof incorrectly changed packet path scope")
	}
}

func TestFirewallCapabilitiesIptablesNativeSetDoesNotRequireAtomicBatch(t *testing.T) {
	spec := minimalFirewallCapabilitiesSpec(firewall.BackendKindIptablesNFT)
	spec.CIDR = true
	spec.NativeSet = true
	spec.AtomicBatch = false
	spec.OwnershipProven = true
	spec.MutationReady = true

	capabilities, err := firewall.NewFirewallCapabilities(spec)
	if err != nil {
		t.Fatalf("NewFirewallCapabilities(): %v", err)
	}
	if !capabilities.SupportsNativeSet() || capabilities.SupportsAtomicBatch() {
		t.Fatal("iptables native-set capability was incorrectly tied to atomic batch")
	}
	if !capabilities.MutationReady() {
		t.Fatal("MutationReady() = false, want true")
	}
}

func TestFirewallCapabilitiesReadinessRelationships(t *testing.T) {
	tests := []struct {
		name       string
		backend    firewall.BackendKind
		prepare    func(*firewall.FirewallCapabilitiesSpec)
		wantReady  bool
		wantOwner  bool
		wantAtomic bool
	}{
		{
			name:    "unproven ownership remains non-ready",
			backend: firewall.BackendKindNftablesNative,
			prepare: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.OwnershipProven = false
				spec.MutationReady = false
			},
		},
		{
			name:    "proven ownership may remain non-ready",
			backend: firewall.BackendKindNftablesNative,
			prepare: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.OwnershipProven = true
				spec.MutationReady = false
			},
			wantOwner: true,
		},
		{
			name:    "iptables nft ready permits atomic batch without native set",
			backend: firewall.BackendKindIptablesNFT,
			prepare: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.CIDR = true
				spec.AtomicBatch = true
				spec.OwnershipProven = true
				spec.MutationReady = true
			},
			wantReady:  true,
			wantOwner:  true,
			wantAtomic: true,
		},
		{
			name:    "iptables legacy ready does not claim native capabilities",
			backend: firewall.BackendKindIptablesLegacy,
			prepare: func(spec *firewall.FirewallCapabilitiesSpec) {
				spec.CIDR = true
				spec.OwnershipProven = true
				spec.MutationReady = true
			},
			wantReady: true,
			wantOwner: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			spec := minimalFirewallCapabilitiesSpec(test.backend)
			test.prepare(&spec)
			capabilities, err := firewall.NewFirewallCapabilities(spec)
			if err != nil {
				t.Fatalf("NewFirewallCapabilities(): %v", err)
			}
			if capabilities.MutationReady() != test.wantReady {
				t.Fatalf("MutationReady() = %t, want %t", capabilities.MutationReady(), test.wantReady)
			}
			if capabilities.OwnershipProven() != test.wantOwner {
				t.Fatalf("OwnershipProven() = %t, want %t", capabilities.OwnershipProven(), test.wantOwner)
			}
			if test.backend != firewall.BackendKindNftablesNative && test.wantReady {
				if capabilities.SupportsNativeSet() {
					t.Fatal("iptables readiness incorrectly required or synthesized native set capability")
				}
				if got := capabilities.SupportsAtomicBatch(); got != test.wantAtomic {
					t.Fatalf("SupportsAtomicBatch() = %t, want %t independent of NativeSet", got, test.wantAtomic)
				}
			}
		})
	}
}

func TestFirewallCapabilitiesRejectsInvalidBackendAndSanitizesErrors(t *testing.T) {
	tests := []struct {
		name    string
		backend firewall.BackendKind
		version string
	}{
		{name: "zero backend", backend: "", version: "zero-backend-secret"},
		{name: "unknown backend", backend: firewall.BackendKind("shell-executor"), version: "unknown-backend-secret"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			spec := minimalFirewallCapabilitiesSpec(test.backend)
			spec.ToolVersion = test.version
			capabilities, err := firewall.NewFirewallCapabilities(spec)
			assertInvalidFirewallCapabilities(t, capabilities, err, test.version, string(test.backend))
		})
	}
}

func TestFirewallCapabilitiesZeroValueCannotClaimValidity(t *testing.T) {
	var capabilities firewall.FirewallCapabilities
	if err := capabilities.Validate(); err == nil {
		t.Fatal("zero FirewallCapabilities.Validate() error = nil")
	} else if err.Error() != invalidCapabilitiesMessage {
		t.Fatalf("zero FirewallCapabilities.Validate() error = %q, want %q", err, invalidCapabilitiesMessage)
	}
	if capabilities.Backend() != "" || capabilities.ToolVersion() != "" {
		t.Fatalf("zero identity = %q/%q, want empty", capabilities.Backend(), capabilities.ToolVersion())
	}
	for name, got := range map[string]bool{
		"IPv4": capabilities.SupportsIPv4(), "IPv6": capabilities.SupportsIPv6(),
		"CIDR": capabilities.SupportsCIDR(), "NativeSet": capabilities.SupportsNativeSet(),
		"NativeTimeout":   capabilities.SupportsNativeTimeout(),
		"CrashSafeExpiry": capabilities.SupportsCrashSafeExpiry(),
		"AtomicBatch":     capabilities.SupportsAtomicBatch(), "HostInput": capabilities.SupportsHostInput(),
		"Forward": capabilities.SupportsForward(), "UFW": capabilities.UFWIntegrationProven(),
		"Docker": capabilities.DockerIntegrationProven(), "Ownership": capabilities.OwnershipProven(),
		"MutationReady": capabilities.MutationReady(),
	} {
		if got {
			t.Fatalf("zero value %s getter = true", name)
		}
	}
}

func minimalFirewallCapabilitiesSpec(kind firewall.BackendKind) firewall.FirewallCapabilitiesSpec {
	return firewall.FirewallCapabilitiesSpec{
		Backend:     kind,
		ToolVersion: "tool v1.0",
		IPv4:        true,
		HostInput:   true,
	}
}

func completeFirewallCapabilitiesSpec() firewall.FirewallCapabilitiesSpec {
	return firewall.FirewallCapabilitiesSpec{
		Backend:                 firewall.BackendKindNftablesNative,
		ToolVersion:             "nftables v1.1.0",
		IPv4:                    true,
		IPv6:                    true,
		CIDR:                    true,
		NativeSet:               true,
		NativeTimeout:           true,
		CrashSafeExpiry:         true,
		AtomicBatch:             true,
		HostInput:               true,
		Forward:                 true,
		UFWIntegrationProven:    true,
		DockerIntegrationProven: true,
		OwnershipProven:         true,
		MutationReady:           true,
	}
}

func assertCapabilityBool(t *testing.T, name string, got, want bool) {
	t.Helper()
	if got != want {
		t.Fatalf("%s() = %t, want %t", name, got, want)
	}
}

func assertInvalidFirewallCapabilities(
	t *testing.T,
	capabilities firewall.FirewallCapabilities,
	err error,
	forbidden ...string,
) {
	t.Helper()
	if err == nil {
		t.Fatal("NewFirewallCapabilities() error = nil, want invalid capabilities")
	}
	if err.Error() != invalidCapabilitiesMessage {
		t.Fatalf("invalid capabilities error = %q, want %q", err, invalidCapabilitiesMessage)
	}
	if capabilities != (firewall.FirewallCapabilities{}) {
		t.Fatalf("invalid constructor returned non-zero capabilities: %#v", capabilities)
	}
	for _, marker := range forbidden {
		if marker != "" && strings.Contains(err.Error(), marker) {
			t.Fatalf("invalid capabilities error leaked %q: %q", marker, err)
		}
	}
}
