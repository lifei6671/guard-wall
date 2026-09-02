package enforcer_test

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestAuthorizeMutationRequestMapsClosedDomains(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	basis := snapshot.Digest()

	infrastructureRequest, err := ipc.NewApplyInfrastructureRequest(basis, 1)
	if err != nil {
		t.Fatal(err)
	}
	policyRequest, err := ipc.NewApplyPolicyRequest(
		basis, 2,
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		bridgeProtectedTargets(),
	)
	if err != nil {
		t.Fatal(err)
	}
	targetRequest, err := ipc.NewApplyTargetRequest(
		basis, 3, netip.MustParsePrefix("192.0.2.0/24"),
		ipc.MembershipPresent, ipc.TimeoutModeNative, 1_800_000_000_000_000, true,
		[]ipc.Scope{ipc.ScopeInput, ipc.ScopeForward},
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		request ipc.MutationRequest
		domain  firewall.MutationDomain
	}{
		{name: "infrastructure", request: infrastructureRequest, domain: firewall.MutationDomainInfrastructure},
		{name: "policy", request: policyRequest, domain: firewall.MutationDomainPolicy},
		{name: "target", request: targetRequest, domain: firewall.MutationDomainTarget},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorization, err := enforcer.AuthorizeMutationRequest(test.request, capabilities, snapshot)
			if err != nil {
				t.Fatalf("authorize: %v", err)
			}
			mutation, ok := authorization.Mutation()
			if !ok {
				t.Fatal("missing authorized mutation")
			}
			plan, ok := mutation.(firewall.OperationPlan)
			if !ok || plan.Domain() != test.domain || plan.BasisSnapshotDigest() != basis ||
				plan.Backend() != capabilities.Backend() {
				t.Fatalf("unexpected mapped plan")
			}
			if _, immediate := authorization.ImmediateResponse(); immediate {
				t.Fatal("Apply unexpectedly produced immediate response")
			}
			switch typed := mutation.(type) {
			case firewall.InfrastructureOperationPlan:
				if typed.SchemaVersion() != firewall.ManagedInfrastructureSchemaVersionV1 {
					t.Fatal("infrastructure schema was not preserved")
				}
			case firewall.PolicyOperationPlan:
				if !reflect.DeepEqual(typed.Allowlist(), []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}) ||
					!reflect.DeepEqual(typed.ProtectedTargets(), bridgeProtectedTargets()) {
					t.Fatal("policy payload was not preserved")
				}
			case firewall.TargetOperationPlan:
				expiry, hasExpiry := typed.EffectiveUntilUnixMicro()
				if typed.Target() != netip.MustParsePrefix("192.0.2.0/24") ||
					typed.Membership() != firewall.TargetMembershipPresent ||
					typed.TimeoutMode() != firewall.ManagedTimeoutNative ||
					!hasExpiry || expiry != 1_800_000_000_000_000 ||
					!reflect.DeepEqual(typed.Scopes(), []firewall.ManagedScope{
						firewall.ManagedScopeInput, firewall.ManagedScopeForward,
					}) {
					t.Fatal("target payload was not preserved")
				}
			default:
				t.Fatalf("unexpected plan type %T", mutation)
			}
		})
	}

	removeAuthorization, err := enforcer.AuthorizeMutationRequest(
		ipc.NewRemoveManagedInfrastructureRequest(), capabilities, snapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	mutation, ok := removeAuthorization.Mutation()
	if !ok {
		t.Fatal("missing removal authority")
	}
	if _, ok := mutation.(firewall.RemovalAuthorization); !ok {
		t.Fatalf("remove mapped to %T", mutation)
	}
}

func TestAuthorizeMutationRequestRejectsNilStaleAndNotReady(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	valid, err := ipc.NewApplyInfrastructureRequest(snapshot.Digest(), 1)
	if err != nil {
		t.Fatal(err)
	}
	typedNil := reflect.Zero(reflect.TypeOf(valid)).Interface().(ipc.MutationRequest)

	tests := []struct {
		name    string
		request ipc.MutationRequest
		caps    firewall.FirewallCapabilities
		code    enforcer.BridgeErrorCode
	}{
		{name: "nil", request: nil, caps: capabilities, code: enforcer.BridgeErrorInvalidPlan},
		{name: "typed nil", request: typedNil, caps: capabilities, code: enforcer.BridgeErrorInvalidPlan},
		{name: "invalid authority", request: valid, caps: firewall.FirewallCapabilities{}, code: enforcer.BridgeErrorNotReady},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorization, err := enforcer.AuthorizeMutationRequest(test.request, test.caps, snapshot)
			if _, ok := authorization.Mutation(); ok {
				t.Fatal("failure returned executable mutation")
			}
			assertBridgeCode(t, err, test.code)
		})
	}

	stale, err := ipc.NewApplyInfrastructureRequest(strings.Repeat("a", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := enforcer.AuthorizeMutationRequest(stale, capabilities, snapshot)
	if _, ok := authorization.Mutation(); ok {
		t.Fatal("stale request returned executable mutation")
	}
	assertBridgeCode(t, err, enforcer.BridgeErrorSnapshotMismatch)
}

func TestAuthorizeRemovalAlreadyAbsentIsImmediateConfirmed(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), false, false)
	authorization, err := enforcer.AuthorizeMutationRequest(
		ipc.NewRemoveManagedInfrastructureRequest(), capabilities, snapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := authorization.Mutation(); ok {
		t.Fatal("already-absent removal authorized backend work")
	}
	response, ok := authorization.ImmediateResponse()
	if !ok || response.Operation() != ipc.OperationRemoveManagedInfrastructure ||
		response.Status() != ipc.MutationStatusConfirmed {
		t.Fatalf("unexpected no-op response: %T", response)
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
			partial := bridgeResidualSnapshot(t, residual.policy, residual.targets)
			authorization, err := enforcer.AuthorizeMutationRequest(
				ipc.NewRemoveManagedInfrastructureRequest(), capabilities, partial,
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := authorization.Mutation(); !ok {
				t.Fatal("managed residue did not produce removal authority")
			}
			if _, immediate := authorization.ImmediateResponse(); immediate {
				t.Fatal("managed residue produced confirmed no-op")
			}
		})
	}
}

func TestMapMutationResultPreservesEveryApplyDomain(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	basis := snapshot.Digest()
	infrastructure, _ := ipc.NewApplyInfrastructureRequest(basis, 1)
	policy, _ := ipc.NewApplyPolicyRequest(basis, 2, nil, bridgeProtectedTargets())
	target, _ := ipc.NewApplyTargetRequest(
		basis, 3, netip.MustParsePrefix("192.0.2.1/32"),
		ipc.MembershipPresent, ipc.TimeoutModeNone, 0, false, []ipc.Scope{ipc.ScopeInput},
	)

	for _, test := range []struct {
		name    string
		request ipc.MutationRequest
		domain  ipc.Domain
	}{
		{name: "infrastructure", request: infrastructure, domain: ipc.DomainInfrastructure},
		{name: "policy", request: policy, domain: ipc.DomainPolicy},
		{name: "target", request: target, domain: ipc.DomainTarget},
	} {
		t.Run(test.name, func(t *testing.T) {
			authorization, err := enforcer.AuthorizeMutationRequest(test.request, capabilities, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			mutation, _ := authorization.Mutation()
			results := []struct {
				status ipc.MutationStatus
				build  func() firewall.MutationResult
			}{
				{status: ipc.MutationStatusConfirmed, build: func() firewall.MutationResult {
					result, _ := firewall.NewConfirmedMutationResult(mutation)
					return result
				}},
				{status: ipc.MutationStatusRejected, build: func() firewall.MutationResult {
					result, _ := firewall.NewRejectedMutationResult(mutation, firewall.MutationErrorUnsupported)
					return result
				}},
				{status: ipc.MutationStatusUnknown, build: func() firewall.MutationResult {
					result, _ := firewall.NewUnknownMutationResult(mutation)
					return result
				}},
			}
			for _, resultCase := range results {
				response, err := enforcer.MapMutationResult(authorization, resultCase.build())
				if err != nil {
					t.Fatal(err)
				}
				apply, ok := response.(ipc.ApplyManagedPlanResponse)
				if !ok || apply.Domain() != test.domain || apply.Status() != resultCase.status {
					t.Fatalf("response lost domain/status: %T", response)
				}
			}
		})
	}
}

func TestMapMutationResultExhaustiveApplyAndRemoval(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	request, err := ipc.NewApplyInfrastructureRequest(snapshot.Digest(), 1)
	if err != nil {
		t.Fatal(err)
	}
	applyAuthorization, err := enforcer.AuthorizeMutationRequest(request, capabilities, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	applyMutation, _ := applyAuthorization.Mutation()

	confirmed, _ := firewall.NewConfirmedMutationResult(applyMutation)
	response, err := enforcer.MapMutationResult(applyAuthorization, confirmed)
	assertResponse(t, response, err, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)

	unknown, _ := firewall.NewUnknownMutationResult(applyMutation)
	response, err = enforcer.MapMutationResult(applyAuthorization, unknown)
	assertResponse(t, response, err, ipc.OperationApplyManagedPlan, ipc.MutationStatusUnknown, ipc.MutationErrorCodeUnknownResult, true)

	applyCodes := []struct {
		firewall firewall.MutationErrorCode
		ipc      ipc.MutationErrorCode
	}{
		{firewall.MutationErrorInvalidPlan, ipc.MutationErrorCodeInvalidPlan},
		{firewall.MutationErrorOwnershipConflict, ipc.MutationErrorCodeOwnershipConflict},
		{firewall.MutationErrorUnsupported, ipc.MutationErrorCodeUnsupported},
		{firewall.MutationErrorNotReady, ipc.MutationErrorCodeNotReady},
		{firewall.MutationErrorBackendRejected, ipc.MutationErrorCodeBackendRejected},
	}
	for _, pair := range applyCodes {
		result, resultErr := firewall.NewRejectedMutationResult(applyMutation, pair.firewall)
		if resultErr != nil {
			t.Fatal(resultErr)
		}
		response, err = enforcer.MapMutationResult(applyAuthorization, result)
		assertResponse(t, response, err, ipc.OperationApplyManagedPlan, ipc.MutationStatusRejected, pair.ipc, true)
	}

	removeAuthorization, err := enforcer.AuthorizeMutationRequest(
		ipc.NewRemoveManagedInfrastructureRequest(), capabilities, snapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	removeMutation, _ := removeAuthorization.Mutation()
	removeConfirmed, _ := firewall.NewConfirmedMutationResult(removeMutation)
	response, err = enforcer.MapMutationResult(removeAuthorization, removeConfirmed)
	assertResponse(t, response, err, ipc.OperationRemoveManagedInfrastructure, ipc.MutationStatusConfirmed, "", false)
	removeUnknown, _ := firewall.NewUnknownMutationResult(removeMutation)
	response, err = enforcer.MapMutationResult(removeAuthorization, removeUnknown)
	assertResponse(t, response, err, ipc.OperationRemoveManagedInfrastructure, ipc.MutationStatusUnknown, ipc.MutationErrorCodeUnknownResult, true)
	removeRejected, _ := firewall.NewRejectedMutationResult(removeMutation, firewall.MutationErrorBackendRejected)
	response, err = enforcer.MapMutationResult(removeAuthorization, removeRejected)
	assertResponse(t, response, err, ipc.OperationRemoveManagedInfrastructure, ipc.MutationStatusRejected, ipc.MutationErrorCodeBackendRejected, true)
}

func TestMapMutationResultRejectsUncorrelatedIdentity(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	firstRequest, _ := ipc.NewApplyInfrastructureRequest(snapshot.Digest(), 1)
	secondRequest, _ := ipc.NewApplyInfrastructureRequest(snapshot.Digest(), 2)
	first, _ := enforcer.AuthorizeMutationRequest(firstRequest, capabilities, snapshot)
	second, _ := enforcer.AuthorizeMutationRequest(secondRequest, capabilities, snapshot)
	secondMutation, _ := second.Mutation()
	wrongResult, _ := firewall.NewConfirmedMutationResult(secondMutation)

	response, err := enforcer.MapMutationResult(first, wrongResult)
	if response != nil {
		t.Fatalf("identity mismatch produced response: %T", response)
	}
	assertBridgeCode(t, err, enforcer.BridgeErrorResultMismatch)

	response, err = enforcer.MapMutationResult(first, firewall.MutationResult{})
	if response != nil {
		t.Fatalf("zero result produced response: %T", response)
	}
	assertBridgeCode(t, err, enforcer.BridgeErrorResultMismatch)
}

func assertResponse(
	t *testing.T,
	response ipc.MutationResponse,
	err error,
	operation ipc.Operation,
	status ipc.MutationStatus,
	code ipc.MutationErrorCode,
	hasCode bool,
) {
	t.Helper()
	if err != nil || response == nil || response.Operation() != operation || response.Status() != status {
		t.Fatalf("response=%T operation=%q status=%q err=%v", response, response.Operation(), response.Status(), err)
	}
	gotCode, gotHasCode := response.ErrorCode()
	if gotHasCode != hasCode || gotCode != code {
		t.Fatalf("error code=(%q,%v), want (%q,%v)", gotCode, gotHasCode, code, hasCode)
	}
}

func assertBridgeCode(t *testing.T, err error, want enforcer.BridgeErrorCode) {
	t.Helper()
	var bridgeError *enforcer.BridgeError
	if !errors.As(err, &bridgeError) || bridgeError.Code() != want {
		t.Fatalf("bridge error=%v, want %q", err, want)
	}
}

func bridgeCapabilities(t *testing.T) firewall.FirewallCapabilities {
	t.Helper()
	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend: firewall.BackendKindNftablesNative, ToolVersion: "test-1",
		IPv4: true, IPv6: true, CIDR: true, NativeSet: true,
		NativeTimeout: true, CrashSafeExpiry: true, AtomicBatch: true,
		HostInput: true, Forward: true, OwnershipProven: true, MutationReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return capabilities
}

func bridgeSnapshot(
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

func bridgeResidualSnapshot(t *testing.T, withPolicy bool, withTarget bool) firewall.ManagedSnapshot {
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

func bridgeProtectedTargets() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
}
