//go:build linux

package reconcile

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/enforcement"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestReconcileSnapshotPreservesManagedBasisAndLimitedTargetEvidence(t *testing.T) {
	infrastructure, err := firewall.NewInfrastructureObservation(firewall.InfrastructureObservationSpec{
		Backend: firewall.BackendKindNftablesNative, OwnerVersion: firewall.ManagedOwnerVersionV1,
		SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1, Digest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{RelationDigest: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	expiry := int64(1_900_000_000_000_000)
	target := netip.MustParsePrefix("192.0.2.4/32")
	observedTarget, err := firewall.NewTargetObservation(firewall.TargetObservationSpec{
		Target: target, TimeoutMode: firewall.ManagedTimeoutNative, EffectiveUntilUnixMicro: &expiry,
		Scopes: []firewall.ManagedScope{firewall.ManagedScopeInput, firewall.ManagedScopeForward},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{Infrastructure: &infrastructure, Policy: &policy, Targets: []firewall.TargetObservation{observedTarget}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
	if err != nil {
		t.Fatal(err)
	}

	got, err := reconcileSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest() != snapshot.Digest() || got.Infrastructure == nil || got.Policy == nil {
		t.Fatal("managed snapshot digest or global domains were not preserved")
	}
	physical, ok := got.Targets[target]
	if !ok || physical.Evidence != core.TargetObservationEvidenceManagedSnapshot ||
		physical.Backend != "" || physical.OwnerVersion != "" ||
		physical.PolicyCoverage != core.ObservedPolicyUnknown || physical.PolicyRelationDigest != "" ||
		physical.BanMembership != core.ObservedMembershipPresent || physical.Scopes != core.ScopeInput|core.ScopeForward {
		t.Fatalf("limited target mapping = %#v", physical)
	}
}

func TestMapIPCApplyResponseRejectsCrossDomainAndPreservesUnknown(t *testing.T) {
	plan := OperationPlan{Domain: DomainPolicy, Digest: strings.Repeat("d", 64)}
	unknown, err := ipc.NewApplyManagedPlanUnknownResponse(ipc.DomainPolicy)
	if err != nil {
		t.Fatal(err)
	}
	result, err := mapIPCApplyResponse(plan, unknown)
	if err != nil || result.Kind != ResultUnknown || result.ErrorCode != "unknown_result" {
		t.Fatalf("unknown response = %#v, %v", result, err)
	}
	wrong, err := ipc.NewApplyManagedPlanConfirmedResponse(ipc.DomainTarget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapIPCApplyResponse(plan, wrong); err == nil {
		t.Fatal("cross-domain IPC response was accepted")
	}
}

func TestIPCBackendAppliesCompletePolicyAgainstFreshAuthority(t *testing.T) {
	snapshot := managedSnapshotForIPCBackend(t, strings.Repeat("b", 64), strings.Repeat("c", 64))
	capabilities := managedCapabilitiesForIPCBackend(t)
	response, err := ipc.NewApplyManagedPlanConfirmedResponse(ipc.DomainPolicy)
	if err != nil {
		t.Fatal(err)
	}
	transport := &ipcTransportStub{capabilities: capabilities, snapshot: snapshot, response: response}
	policy, err := core.NewManagedPolicyIntent([]netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := OperationPlan{Domain: DomainPolicy, DesiredPolicy: policy, ExpectedPolicyRevision: 9, BasisSnapshotDigest: snapshot.Digest()}
	plan.Digest = PlanDigest(plan)
	result, err := newIPCBackend(transport).Apply(context.Background(), plan)
	if err != nil || result.Kind != ResultConfirmed || transport.capabilityCalls != 1 || transport.snapshotCalls != 1 || transport.mutationCalls != 1 {
		t.Fatalf("Apply() result=%#v err=%v calls=%d/%d/%d", result, err, transport.capabilityCalls, transport.snapshotCalls, transport.mutationCalls)
	}
	if transport.request == nil || transport.request.Operation() != ipc.OperationApplyManagedPlan {
		t.Fatalf("mutation request = %#v", transport.request)
	}
}

func TestIPCBackendAppliesNativeTargetWithSafetyGrace(t *testing.T) {
	snapshot := managedSnapshotForIPCBackend(t, strings.Repeat("b", 64), strings.Repeat("c", 64))
	response, err := ipc.NewApplyManagedPlanConfirmedResponse(ipc.DomainTarget)
	if err != nil {
		t.Fatal(err)
	}
	transport := &ipcTransportStub{capabilities: managedCapabilitiesForIPCBackend(t), snapshot: snapshot, response: response}
	effectiveUntil := time.Unix(1_900_000_000, 123_456_000).UTC()
	target := netip.MustParsePrefix("203.0.113.77/32")
	plan := OperationPlan{
		Domain: DomainTarget, Target: target, ExpectedTargetGeneration: 1, BasisSnapshotDigest: snapshot.Digest(),
		DesiredTarget: core.NormalizedTargetEnforcementIntent{
			NodeID: "00112233445566778899aabbccddeeff", CanonicalTarget: target,
			BanMembership: core.BanPresent, EffectiveUntil: &effectiveUntil, TimeoutMode: core.TimeoutNative,
			Scopes: core.ScopeInput | core.ScopeForward, AddressFamily: core.AddressFamilyIPv4,
			PolicyCoverage: core.PolicyCoverageNone, BackendAttributesDigest: strings.Repeat("d", 64), Generation: 1,
		},
	}
	plan.Digest = PlanDigest(plan)
	if _, err := newIPCBackend(transport).Apply(context.Background(), plan); err != nil {
		t.Fatalf("Apply(): %v", err)
	}
	request, ok := transport.request.(ipc.ApplyManagedPlanRequest)
	if !ok {
		t.Fatalf("request = %T, want ApplyManagedPlanRequest", transport.request)
	}
	targetPlan, ok := request.Plan().(ipc.TargetPlan)
	if !ok {
		t.Fatalf("request plan = %T, want TargetPlan", request.Plan())
	}
	want := enforcement.NativeExpiryForIntent(plan.DesiredTarget)
	if expiry, found := targetPlan.EffectiveUntilUnixMicro(); !found || want == nil || expiry != want.UnixMicro() {
		t.Fatalf("physical native expiry = (%d, %v), want %v", expiry, found, want)
	}
}

func TestIPCBackendProbeHealthUsesOnlyAuthenticatedCapabilityProbe(t *testing.T) {
	transport := &ipcTransportStub{capabilities: managedCapabilitiesForIPCBackend(t)}
	if err := newIPCBackend(transport).ProbeHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if transport.capabilityCalls != 1 || transport.snapshotCalls != 0 || transport.mutationCalls != 0 {
		t.Fatalf("ProbeHealth() calls=%d/%d/%d, want capability probe only", transport.capabilityCalls, transport.snapshotCalls, transport.mutationCalls)
	}
}

func TestControllerIPCBackendRejectsChangedApplyBasis(t *testing.T) {
	policy, err := core.NewManagedPolicyIntent([]netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := managedCapabilitiesForIPCBackend(t)
	initial := managedSnapshotForIPCBackend(t, policy.RelationDigest, strings.Repeat("c", 64))
	changed := managedSnapshotForIPCBackend(t, policy.RelationDigest, strings.Repeat("d", 64))
	transport := &ipcTransportStub{capabilities: capabilities, snapshots: []firewall.ManagedSnapshot{initial, changed}}
	controller := newTestController(t, newIPCBackend(transport), newManualClock(), &memoryAudit{})
	desired := desiredSnapshot()
	desired.Policy = policy
	setDesired(t, controller, desired)

	result, err := controller.Execute(context.Background(), policyPlan(desired))
	if err != nil || result.Apply.Kind != ResultRejected || result.Apply.ErrorCode != "snapshot_mismatch" {
		t.Fatalf("Execute() result=%+v err=%v, want basis rejection", result, err)
	}
	if transport.capabilityCalls != 2 || transport.snapshotCalls != 2 || transport.mutationCalls != 0 {
		t.Fatalf("calls=%d/%d/%d, want pre-Apply and Apply authority reads without mutation", transport.capabilityCalls, transport.snapshotCalls, transport.mutationCalls)
	}
}

func TestControllerIPCBackendTransportFailureRequiresProbe(t *testing.T) {
	policy, err := core.NewManagedPolicyIntent(nil, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := managedSnapshotForIPCBackend(t, policy.RelationDigest, strings.Repeat("c", 64))
	transport := &ipcTransportStub{
		capabilities: managedCapabilitiesForIPCBackend(t), snapshots: []firewall.ManagedSnapshot{snapshot, snapshot},
		mutationErr: fmt.Errorf("socket closed"),
	}
	controller := newTestController(t, newIPCBackend(transport), newManualClock(), &memoryAudit{})
	desired := desiredSnapshot()
	desired.Policy = policy
	setDesired(t, controller, desired)

	if _, err := controller.Execute(context.Background(), policyPlan(desired)); err == nil {
		t.Fatal("transport failure was accepted")
	}
	if !controller.ProbeRequired() {
		t.Fatal("transport failure did not enter the Probe barrier")
	}
}

type ipcTransportStub struct {
	capabilities                                  firewall.FirewallCapabilities
	snapshot                                      firewall.ManagedSnapshot
	response                                      ipc.MutationResponse
	snapshots                                     []firewall.ManagedSnapshot
	mutationErr                                   error
	capabilityCalls, snapshotCalls, mutationCalls int
	request                                       ipc.MutationRequest
}

func (s *ipcTransportStub) ProbeCapabilities(context.Context) (firewall.FirewallCapabilities, error) {
	s.capabilityCalls++
	return s.capabilities, nil
}
func (s *ipcTransportStub) SnapshotManaged(context.Context) (firewall.ManagedSnapshot, error) {
	s.snapshotCalls++
	if len(s.snapshots) != 0 {
		index := s.snapshotCalls - 1
		if index >= len(s.snapshots) {
			index = len(s.snapshots) - 1
		}
		return s.snapshots[index], nil
	}
	return s.snapshot, nil
}
func (s *ipcTransportStub) Mutation(_ context.Context, request ipc.MutationRequest) (ipc.MutationResponse, error) {
	s.mutationCalls++
	s.request = request
	if s.mutationErr != nil {
		return nil, s.mutationErr
	}
	if s.response == nil {
		return nil, fmt.Errorf("missing response")
	}
	return s.response, nil
}

func managedCapabilitiesForIPCBackend(t *testing.T) firewall.FirewallCapabilities {
	t.Helper()
	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend: firewall.BackendKindNftablesNative, ToolVersion: "nft-test", IPv4: true, IPv6: true,
		CIDR: true, NativeSet: true, NativeTimeout: true, CrashSafeExpiry: true, AtomicBatch: true,
		HostInput: true, Forward: true, OwnershipProven: true, MutationReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return capabilities
}

func managedSnapshotForIPCBackend(t *testing.T, relationDigest, foreignDigest string) firewall.ManagedSnapshot {
	t.Helper()
	infrastructure, err := firewall.NewInfrastructureObservation(firewall.InfrastructureObservationSpec{Backend: firewall.BackendKindNftablesNative, OwnerVersion: firewall.ManagedOwnerVersionV1, SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1, Digest: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{RelationDigest: relationDigest})
	if err != nil {
		t.Fatal(err)
	}
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{Infrastructure: &infrastructure, Policy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: foreignDigest})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
