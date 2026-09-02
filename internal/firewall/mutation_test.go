package firewall_test

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

func TestAuthorizeMutationDomainsAndDigest(t *testing.T) {
	capabilities := readyCapabilities(t, func(*firewall.FirewallCapabilitiesSpec) {})
	snapshot := managedSnapshot(t, capabilities.Backend(), true, true)
	basis := snapshot.Digest()

	infrastructure, err := firewall.AuthorizeInfrastructureMutation(capabilities, snapshot, basis, 1)
	if err != nil {
		t.Fatalf("authorize infrastructure: %v", err)
	}
	if infrastructure.Domain() != firewall.MutationDomainInfrastructure ||
		infrastructure.SchemaVersion() != firewall.ManagedInfrastructureSchemaVersionV1 ||
		infrastructure.Backend() != capabilities.Backend() {
		t.Fatalf("unexpected infrastructure plan")
	}

	allowlist := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	protected := mandatoryProtectedTargets()
	policy, err := firewall.AuthorizePolicyMutation(capabilities, snapshot, basis, 2, allowlist, protected)
	if err != nil {
		t.Fatalf("authorize policy: %v", err)
	}
	allowlist[0] = netip.MustParsePrefix("192.0.2.0/24")
	protected[0] = netip.MustParsePrefix("192.0.2.1/32")
	if got := policy.Allowlist()[0]; got != netip.MustParsePrefix("10.0.0.0/8") {
		t.Fatalf("plan retained caller slice alias: %s", got)
	}
	returned := policy.ProtectedTargets()
	returned[0] = netip.MustParsePrefix("192.0.2.1/32")
	if policy.ProtectedTargets()[0] != netip.MustParsePrefix("127.0.0.0/8") {
		t.Fatal("plan getter exposed mutable slice")
	}

	expiry := int64(1_800_000_000_000_000)
	target, err := firewall.AuthorizeTargetMutation(
		capabilities, snapshot, basis, 3,
		netip.MustParsePrefix("192.0.2.0/24"), firewall.TargetMembershipPresent,
		firewall.ManagedTimeoutNative, expiry, true,
		[]firewall.ManagedScope{firewall.ManagedScopeInput, firewall.ManagedScopeForward},
	)
	if err != nil {
		t.Fatalf("authorize target: %v", err)
	}
	if target.Domain() != firewall.MutationDomainTarget || target.Target() != netip.MustParsePrefix("192.0.2.0/24") {
		t.Fatal("unexpected target plan")
	}

	removal, absent, err := firewall.AuthorizeManagedRemoval(
		capabilities, snapshot, firewall.ManagedOwnerVersionV1,
	)
	if err != nil || absent || removal == nil {
		t.Fatalf("authorize removal: absent=%v err=%v", absent, err)
	}

	for name, mutation := range map[string]firewall.AuthorizedMutation{
		"infrastructure": infrastructure,
		"policy":         policy,
		"target":         target,
		"removal":        removal,
	} {
		t.Run(name, func(t *testing.T) {
			if len(mutation.Digest()) != 64 || mutation.OwnerVersion() != firewall.ManagedOwnerVersionV1 ||
				mutation.BasisSnapshotDigest() != basis {
				t.Fatalf("invalid authority identity")
			}
		})
	}
	if infrastructure.Digest() == policy.Digest() || policy.Digest() == target.Digest() ||
		target.Digest() == removal.Digest() {
		t.Fatal("operation/domain digests collided")
	}

	repeated, err := firewall.AuthorizeInfrastructureMutation(capabilities, snapshot, basis, 1)
	if err != nil || repeated.Digest() != infrastructure.Digest() {
		t.Fatal("identical authority did not produce deterministic digest")
	}
	changed, err := firewall.AuthorizeInfrastructureMutation(capabilities, snapshot, basis, 2)
	if err != nil || changed.Digest() == infrastructure.Digest() {
		t.Fatal("revision was not bound into digest")
	}
	changedCapabilities := readyCapabilities(t, func(spec *firewall.FirewallCapabilitiesSpec) {
		spec.ToolVersion = "test-2"
	})
	changed, err = firewall.AuthorizeInfrastructureMutation(changedCapabilities, snapshot, basis, 1)
	if err != nil || changed.Digest() == infrastructure.Digest() {
		t.Fatal("capability authority was not bound into digest")
	}
}

func TestAuthorizeMutationFailurePrecedenceAndCapabilities(t *testing.T) {
	ready := readyCapabilities(t, func(*firewall.FirewallCapabilitiesSpec) {})
	snapshot := managedSnapshot(t, ready.Backend(), true, true)
	basis := snapshot.Digest()

	tests := []struct {
		name string
		run  func() error
		code firewall.MutationAuthorizationErrorCode
	}{
		{
			name: "invalid plan precedes invalid authority",
			run: func() error {
				_, err := firewall.AuthorizeInfrastructureMutation(firewall.FirewallCapabilities{}, firewall.ManagedSnapshot{}, "bad", 0)
				return err
			},
			code: firewall.MutationAuthorizationErrorInvalidPlan,
		},
		{
			name: "invalid capabilities",
			run: func() error {
				_, err := firewall.AuthorizeInfrastructureMutation(firewall.FirewallCapabilities{}, snapshot, basis, 1)
				return err
			},
			code: firewall.MutationAuthorizationErrorNotReady,
		},
		{
			name: "invalid snapshot",
			run: func() error {
				_, err := firewall.AuthorizeInfrastructureMutation(ready, firewall.ManagedSnapshot{}, basis, 1)
				return err
			},
			code: firewall.MutationAuthorizationErrorNotReady,
		},
		{
			name: "valid but not ready capabilities",
			run: func() error {
				notReady := readyCapabilities(t, func(spec *firewall.FirewallCapabilitiesSpec) {
					spec.MutationReady = false
					spec.OwnershipProven = false
				})
				_, err := firewall.AuthorizeInfrastructureMutation(notReady, snapshot, basis, 1)
				return err
			},
			code: firewall.MutationAuthorizationErrorNotReady,
		},
		{
			name: "authority backend drift",
			run: func() error {
				drifted := managedSnapshot(t, firewall.BackendKindIptablesNFT, true, true)
				_, err := firewall.AuthorizeInfrastructureMutation(ready, drifted, drifted.Digest(), 1)
				return err
			},
			code: firewall.MutationAuthorizationErrorNotReady,
		},
		{
			name: "stale basis",
			run: func() error {
				_, err := firewall.AuthorizeInfrastructureMutation(ready, snapshot, strings.Repeat("a", 64), 1)
				return err
			},
			code: firewall.MutationAuthorizationErrorSnapshotMismatch,
		},
		{
			name: "timeout value without presence",
			run: func() error {
				_, err := firewall.AuthorizeTargetMutation(
					firewall.FirewallCapabilities{}, firewall.ManagedSnapshot{}, strings.Repeat("a", 64), 1,
					netip.MustParsePrefix("192.0.2.1/32"), firewall.TargetMembershipPresent,
					firewall.ManagedTimeoutNone, 1, false, []firewall.ManagedScope{firewall.ManagedScopeInput},
				)
				return err
			},
			code: firewall.MutationAuthorizationErrorInvalidPlan,
		},
		{
			name: "policy requires infrastructure",
			run: func() error {
				withoutInfrastructure := managedSnapshot(t, ready.Backend(), false, false)
				_, err := firewall.AuthorizePolicyMutation(
					ready, withoutInfrastructure, withoutInfrastructure.Digest(), 1, nil, mandatoryProtectedTargets(),
				)
				return err
			},
			code: firewall.MutationAuthorizationErrorNotReady,
		},
		{
			name: "policy family unsupported",
			run: func() error {
				ipv6Only := readyCapabilities(t, func(spec *firewall.FirewallCapabilitiesSpec) { spec.IPv4 = false })
				authority := managedSnapshot(t, ipv6Only.Backend(), true, true)
				_, err := firewall.AuthorizePolicyMutation(
					ipv6Only, authority, authority.Digest(), 1, nil, mandatoryProtectedTargets(),
				)
				return err
			},
			code: firewall.MutationAuthorizationErrorUnsupported,
		},
		{
			name: "target scope unsupported",
			run: func() error {
				forwardOnly := readyCapabilities(t, func(spec *firewall.FirewallCapabilitiesSpec) { spec.HostInput = false })
				authority := managedSnapshot(t, forwardOnly.Backend(), true, true)
				_, err := firewall.AuthorizeTargetMutation(
					forwardOnly, authority, authority.Digest(), 1,
					netip.MustParsePrefix("192.0.2.1/32"), firewall.TargetMembershipPresent,
					firewall.ManagedTimeoutNone, 0, false, []firewall.ManagedScope{firewall.ManagedScopeInput},
				)
				return err
			},
			code: firewall.MutationAuthorizationErrorUnsupported,
		},
		{
			name: "native timeout unsupported",
			run: func() error {
				withoutTimeout := readyCapabilities(t, func(spec *firewall.FirewallCapabilitiesSpec) {
					spec.NativeTimeout = false
					spec.CrashSafeExpiry = false
				})
				authority := managedSnapshot(t, withoutTimeout.Backend(), true, true)
				_, err := firewall.AuthorizeTargetMutation(
					withoutTimeout, authority, authority.Digest(), 1,
					netip.MustParsePrefix("192.0.2.1/32"), firewall.TargetMembershipPresent,
					firewall.ManagedTimeoutNative, 1, true, []firewall.ManagedScope{firewall.ManagedScopeInput},
				)
				return err
			},
			code: firewall.MutationAuthorizationErrorUnsupported,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertAuthorizationCode(t, test.run(), test.code)
		})
	}
}

func TestAuthorizePolicyDoesNotRequireAtomicIptables(t *testing.T) {
	capabilities := readyCapabilities(t, func(spec *firewall.FirewallCapabilitiesSpec) {
		spec.Backend = firewall.BackendKindIptablesNFT
		spec.NativeSet = false
		spec.NativeTimeout = false
		spec.CrashSafeExpiry = false
		spec.AtomicBatch = false
	})
	snapshot := managedSnapshot(t, capabilities.Backend(), true, true)
	plan, err := firewall.AuthorizePolicyMutation(
		capabilities, snapshot, snapshot.Digest(), 1, nil, mandatoryProtectedTargets(),
	)
	if err != nil || plan == nil {
		t.Fatalf("valid non-atomic iptables authority rejected: %v", err)
	}
}

func TestAuthorizeTargetPolicyAndRemovalBoundaries(t *testing.T) {
	capabilities := readyCapabilities(t, func(*firewall.FirewallCapabilitiesSpec) {})
	withoutPolicy := managedSnapshot(t, capabilities.Backend(), true, false)

	_, err := firewall.AuthorizeTargetMutation(
		capabilities, withoutPolicy, withoutPolicy.Digest(), 1,
		netip.MustParsePrefix("192.0.2.1/32"), firewall.TargetMembershipPresent,
		firewall.ManagedTimeoutNone, 0, false, []firewall.ManagedScope{firewall.ManagedScopeInput},
	)
	assertAuthorizationCode(t, err, firewall.MutationAuthorizationErrorNotReady)

	absent, err := firewall.AuthorizeTargetMutation(
		capabilities, withoutPolicy, withoutPolicy.Digest(), 1,
		netip.MustParsePrefix("192.0.2.1/32"), firewall.TargetMembershipAbsent,
		firewall.ManagedTimeoutNone, 0, false, []firewall.ManagedScope{firewall.ManagedScopeInput},
	)
	if err != nil || absent == nil {
		t.Fatalf("safe target removal should not require policy: %v", err)
	}

	empty := managedSnapshot(t, capabilities.Backend(), false, false)
	removal, alreadyAbsent, err := firewall.AuthorizeManagedRemoval(capabilities, empty, firewall.ManagedOwnerVersionV1)
	if err != nil || removal != nil || !alreadyAbsent {
		t.Fatalf("empty removal must be confirmed no-op: removal=%v absent=%v err=%v", removal, alreadyAbsent, err)
	}
	for _, residual := range []struct {
		name    string
		policy  bool
		targets bool
	}{
		{name: "policy only", policy: true},
		{name: "target only", targets: true},
	} {
		t.Run(residual.name, func(t *testing.T) {
			partial := residualManagedSnapshot(t, residual.policy, residual.targets)
			removal, alreadyAbsent, err := firewall.AuthorizeManagedRemoval(
				capabilities, partial, firewall.ManagedOwnerVersionV1,
			)
			if err != nil || alreadyAbsent || removal == nil {
				t.Fatalf("managed residue must authorize cleanup: removal=%v absent=%v err=%v", removal, alreadyAbsent, err)
			}
		})
	}

	_, _, err = firewall.AuthorizeManagedRemoval(capabilities, empty, "foreign/v1")
	assertAuthorizationCode(t, err, firewall.MutationAuthorizationErrorOwnershipConflict)
}

func TestMutationResultMatrixAndCorrelation(t *testing.T) {
	capabilities := readyCapabilities(t, func(*firewall.FirewallCapabilitiesSpec) {})
	snapshot := managedSnapshot(t, capabilities.Backend(), true, true)
	plan, err := firewall.AuthorizeInfrastructureMutation(capabilities, snapshot, snapshot.Digest(), 1)
	if err != nil {
		t.Fatal(err)
	}
	removal, _, err := firewall.AuthorizeManagedRemoval(capabilities, snapshot, firewall.ManagedOwnerVersionV1)
	if err != nil {
		t.Fatal(err)
	}

	confirmed, err := firewall.NewConfirmedMutationResult(plan)
	if err != nil || confirmed.Validate() != nil || confirmed.Status() != firewall.MutationStatusConfirmed ||
		confirmed.MutationDigest() != plan.Digest() {
		t.Fatalf("invalid confirmed result: %+v %v", confirmed, err)
	}
	unknown, err := firewall.NewUnknownMutationResult(removal)
	if err != nil || unknown.Status() != firewall.MutationStatusUnknown {
		t.Fatalf("invalid unknown result: %+v %v", unknown, err)
	}
	if code, ok := unknown.ErrorCode(); !ok || code != firewall.MutationErrorUnknownResult {
		t.Fatal("unknown result lost closed code")
	}

	for _, code := range []firewall.MutationErrorCode{
		firewall.MutationErrorInvalidPlan,
		firewall.MutationErrorOwnershipConflict,
		firewall.MutationErrorUnsupported,
		firewall.MutationErrorNotReady,
		firewall.MutationErrorBackendRejected,
	} {
		if _, err := firewall.NewRejectedMutationResult(plan, code); err != nil {
			t.Fatalf("Apply rejected code %q: %v", code, err)
		}
	}
	if _, err := firewall.NewRejectedMutationResult(removal, firewall.MutationErrorInvalidPlan); err == nil {
		t.Fatal("Remove accepted invalid_plan result")
	}
	if _, err := firewall.NewRejectedMutationResult(plan, firewall.MutationErrorUnknownResult); err == nil {
		t.Fatal("Rejected accepted unknown_result code")
	}
	if _, err := firewall.NewConfirmedMutationResult(nil); err == nil {
		t.Fatal("nil authority produced result")
	}
	typedNil := reflect.Zero(reflect.TypeOf(plan)).Interface().(firewall.AuthorizedMutation)
	if _, err := firewall.NewConfirmedMutationResult(typedNil); err == nil {
		t.Fatal("typed-nil authority produced result")
	}
}

func readyCapabilities(
	t *testing.T,
	mutate func(*firewall.FirewallCapabilitiesSpec),
) firewall.FirewallCapabilities {
	t.Helper()
	spec := firewall.FirewallCapabilitiesSpec{
		Backend: firewall.BackendKindNftablesNative, ToolVersion: "test-1",
		IPv4: true, IPv6: true, CIDR: true, NativeSet: true,
		NativeTimeout: true, CrashSafeExpiry: true, AtomicBatch: true,
		HostInput: true, Forward: true, OwnershipProven: true, MutationReady: true,
	}
	mutate(&spec)
	capabilities, err := firewall.NewFirewallCapabilities(spec)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	return capabilities
}

func managedSnapshot(
	t *testing.T,
	backend firewall.BackendKind,
	withInfrastructure bool,
	withPolicy bool,
) firewall.ManagedSnapshot {
	t.Helper()
	stateSpec := firewall.ManagedStateSpec{}
	if withInfrastructure {
		infrastructure, err := firewall.NewInfrastructureObservation(firewall.InfrastructureObservationSpec{
			Backend: backend, OwnerVersion: firewall.ManagedOwnerVersionV1,
			SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1,
			Digest:        strings.Repeat("1", 64),
		})
		if err != nil {
			t.Fatal(err)
		}
		stateSpec.Infrastructure = &infrastructure
	}
	if withPolicy {
		policy, err := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{RelationDigest: strings.Repeat("2", 64)})
		if err != nil {
			t.Fatal(err)
		}
		stateSpec.Policy = &policy
	}
	state, err := firewall.NewManagedState(stateSpec)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: strings.Repeat("3", 64)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func residualManagedSnapshot(t *testing.T, withPolicy bool, withTarget bool) firewall.ManagedSnapshot {
	t.Helper()
	stateSpec := firewall.ManagedStateSpec{}
	if withPolicy {
		policy, err := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{RelationDigest: strings.Repeat("2", 64)})
		if err != nil {
			t.Fatal(err)
		}
		stateSpec.Policy = &policy
	}
	if withTarget {
		target, err := firewall.NewTargetObservation(firewall.TargetObservationSpec{
			Target:      netip.MustParsePrefix("192.0.2.1/32"),
			TimeoutMode: firewall.ManagedTimeoutNone,
			Scopes:      []firewall.ManagedScope{firewall.ManagedScopeInput},
		})
		if err != nil {
			t.Fatal(err)
		}
		stateSpec.Targets = []firewall.TargetObservation{target}
	}
	state, err := firewall.NewManagedState(stateSpec)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: strings.Repeat("3", 64)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{
		ManagedState: state, ForeignContext: foreign,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func mandatoryProtectedTargets() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
}

func assertAuthorizationCode(
	t *testing.T,
	err error,
	want firewall.MutationAuthorizationErrorCode,
) {
	t.Helper()
	var authorizationError *firewall.MutationAuthorizationError
	if !errors.As(err, &authorizationError) || authorizationError.Code() != want {
		t.Fatalf("authorization error = %v, want %q", err, want)
	}
}
