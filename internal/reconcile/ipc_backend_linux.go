//go:build linux

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/firewall/nftables"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

// ipcTransport keeps the production socket fixed while allowing the bridge's
// semantic mapping to be tested without exporting a test socket or UID.
type ipcTransport interface {
	ProbeCapabilities(context.Context) (firewall.FirewallCapabilities, error)
	SnapshotManaged(context.Context) (firewall.ManagedSnapshot, error)
	Mutation(context.Context, ipc.MutationRequest) (ipc.MutationResponse, error)
}

type productionIPCTransport struct{}

func (productionIPCTransport) ProbeCapabilities(ctx context.Context) (firewall.FirewallCapabilities, error) {
	return ipc.RoundTripProbeCapabilities(ctx)
}

func (productionIPCTransport) SnapshotManaged(ctx context.Context) (firewall.ManagedSnapshot, error) {
	return ipc.RoundTripSnapshotManaged(ctx)
}

func (productionIPCTransport) Mutation(ctx context.Context, request ipc.MutationRequest) (ipc.MutationResponse, error) {
	return ipc.RoundTripMutation(ctx, request)
}

// IPCBackend is the Linux production boundary between Reconcile and the fixed
// authenticated Enforcer socket. It never exposes transport configuration or retries.
type IPCBackend struct{ transport ipcTransport }

var _ Backend = (*IPCBackend)(nil)

// NewIPCBackend constructs the fixed-socket production backend.
func NewIPCBackend() *IPCBackend { return &IPCBackend{transport: productionIPCTransport{}} }

// RequiresFreshBasis makes Controller bind every IPC mutation to a Probe taken
// inside the single mutation lock.
func (*IPCBackend) RequiresFreshBasis() bool { return true }

func newIPCBackend(transport ipcTransport) *IPCBackend { return &IPCBackend{transport: transport} }

func (b *IPCBackend) Probe(ctx context.Context) (Snapshot, error) {
	if b == nil || b.transport == nil {
		return Snapshot{}, fmt.Errorf("ipc backend is unavailable")
	}
	if err := contextError(ctx); err != nil {
		return Snapshot{}, err
	}
	capabilities, err := b.transport.ProbeCapabilities(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if err := capabilities.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("validate IPC capabilities: %w", err)
	}
	snapshot, err := b.transport.SnapshotManaged(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return reconcileSnapshot(snapshot)
}

func (b *IPCBackend) Apply(ctx context.Context, plan OperationPlan) (ApplyResult, error) {
	if b == nil || b.transport == nil {
		return ApplyResult{}, fmt.Errorf("ipc backend is unavailable")
	}
	if err := contextError(ctx); err != nil {
		return ApplyResult{}, err
	}
	if err := ValidatePlan(plan); err != nil {
		return rejectedPlan(plan, "invalid_plan"), nil
	}
	capabilities, err := b.transport.ProbeCapabilities(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := capabilities.Validate(); err != nil {
		return ApplyResult{}, fmt.Errorf("validate IPC capabilities: %w", err)
	}
	snapshot, err := b.transport.SnapshotManaged(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return ApplyResult{}, fmt.Errorf("validate IPC managed snapshot: %w", err)
	}
	request, code, err := authorizeIPCRequest(capabilities, snapshot, plan)
	if err != nil {
		return rejectedPlan(plan, code), nil
	}
	response, err := b.transport.Mutation(ctx, request)
	if err != nil {
		return ApplyResult{}, err
	}
	return mapIPCApplyResponse(plan, response)
}

func authorizeIPCRequest(
	capabilities firewall.FirewallCapabilities,
	snapshot firewall.ManagedSnapshot,
	plan OperationPlan,
) (ipc.MutationRequest, string, error) {
	var err error
	switch plan.Domain {
	case DomainInfrastructure:
		if !nftables.MatchesFixedDesiredInfrastructure(plan.ExpectedInfrastructureRevision, plan.DesiredInfrastructure) {
			return nil, "invalid_plan", fmt.Errorf("infrastructure plan does not match fixed nftables layout")
		}
		if capabilities.Backend() != firewall.BackendKindNftablesNative {
			return nil, "not_ready", fmt.Errorf("IPC backend is not native nftables")
		}
		if observed, present := snapshot.ManagedState().Infrastructure(); present && !nftables.MatchesFixedInfrastructureObservation(observed) {
			return nil, "ownership_conflict", fmt.Errorf("observed Infrastructure does not match fixed nftables layout")
		}
		_, err = firewall.AuthorizeInfrastructureMutation(
			capabilities, snapshot, plan.BasisSnapshotDigest, int64(plan.ExpectedInfrastructureRevision))
		if err == nil {
			request, requestErr := ipc.NewApplyInfrastructureRequest(plan.BasisSnapshotDigest, int64(plan.ExpectedInfrastructureRevision))
			if requestErr != nil {
				return nil, "invalid_plan", requestErr
			}
			return request, "", nil
		}
	case DomainPolicy:
		_, err = firewall.AuthorizePolicyMutation(capabilities, snapshot, plan.BasisSnapshotDigest,
			int64(plan.ExpectedPolicyRevision), plan.DesiredPolicy.Allowlist, plan.DesiredPolicy.ProtectedTargets)
		if err == nil {
			request, requestErr := ipc.NewApplyPolicyRequest(plan.BasisSnapshotDigest, int64(plan.ExpectedPolicyRevision),
				plan.DesiredPolicy.Allowlist, plan.DesiredPolicy.ProtectedTargets)
			if requestErr != nil {
				return nil, "invalid_plan", requestErr
			}
			return request, "", nil
		}
	case DomainTarget:
		membership := firewall.TargetMembershipAbsent
		if plan.DesiredTarget.BanMembership == core.BanPresent {
			membership = firewall.TargetMembershipPresent
		}
		timeoutMode := firewall.ManagedTimeoutNone
		if plan.DesiredTarget.TimeoutMode == core.TimeoutNative {
			timeoutMode = firewall.ManagedTimeoutNative
		}
		expiry := int64(0)
		hasExpiry := plan.DesiredTarget.EffectiveUntil != nil
		if hasExpiry {
			expiry = plan.DesiredTarget.EffectiveUntil.UTC().UnixMicro()
		}
		scopes := firewallScopes(plan.DesiredTarget.Scopes)
		_, err = firewall.AuthorizeTargetMutation(capabilities, snapshot, plan.BasisSnapshotDigest,
			int64(plan.ExpectedTargetGeneration), plan.Target, membership, timeoutMode, expiry, hasExpiry, scopes)
		if err == nil {
			request, requestErr := ipc.NewApplyTargetRequest(plan.BasisSnapshotDigest, int64(plan.ExpectedTargetGeneration), plan.Target,
				ipcMembership(plan.DesiredTarget.BanMembership), ipcTimeoutMode(plan.DesiredTarget.TimeoutMode), expiry, hasExpiry,
				ipcScopes(plan.DesiredTarget.Scopes))
			if requestErr != nil {
				return nil, "invalid_plan", requestErr
			}
			return request, "", nil
		}
	default:
		return nil, "invalid_plan", fmt.Errorf("unknown reconcile domain %d", plan.Domain)
	}
	if err == nil {
		return nil, "invalid_plan", fmt.Errorf("construct IPC request")
	}
	return nil, authorizationCode(err), err
}

func mapIPCApplyResponse(plan OperationPlan, response ipc.MutationResponse) (ApplyResult, error) {
	typed, ok := response.(ipc.ApplyManagedPlanResponse)
	if !ok || typed == nil || typed.Domain() != ipcDomain(plan.Domain) {
		return ApplyResult{}, fmt.Errorf("IPC mutation response does not match request")
	}
	result := ApplyResult{Domain: plan.Domain, Target: plan.Target, Digest: plan.Digest}
	switch typed.Status() {
	case ipc.MutationStatusConfirmed:
		result.Kind = ResultConfirmed
	case ipc.MutationStatusRejected:
		code, ok := typed.ErrorCode()
		if !ok {
			return ApplyResult{}, fmt.Errorf("IPC rejected response has no error code")
		}
		result.Kind, result.ErrorCode = ResultRejected, string(code)
	case ipc.MutationStatusUnknown:
		result.Kind, result.ErrorCode = ResultUnknown, "unknown_result"
	default:
		return ApplyResult{}, fmt.Errorf("IPC mutation response has unknown status")
	}
	return result, nil
}

func reconcileSnapshot(snapshot firewall.ManagedSnapshot) (Snapshot, error) {
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("validate IPC managed snapshot: %w", err)
	}
	state := snapshot.ManagedState()
	result := Snapshot{BasisDigest: snapshot.Digest(), Targets: make(map[netip.Prefix]core.PhysicalTargetObserved)}
	if infrastructure, ok := state.Infrastructure(); ok {
		result.Infrastructure = &PhysicalInfrastructure{Backend: string(infrastructure.Backend()), OwnerVersion: infrastructure.OwnerVersion(), Digest: infrastructure.Digest()}
	}
	if policy, ok := state.Policy(); ok {
		result.Policy = &PhysicalPolicy{RelationDigest: policy.RelationDigest()}
	}
	for _, target := range state.Targets() {
		observed := core.PhysicalTargetObserved{CanonicalTarget: target.Target(), Evidence: core.TargetObservationEvidenceManagedSnapshot,
			BanMembership: core.ObservedMembershipPresent, TimeoutMode: coreTimeoutMode(target.TimeoutMode()), Scopes: coreScopes(target.Scopes()), AddressFamily: targetAddressFamily(target.Target())}
		if expiry, ok := target.EffectiveUntilUnixMicro(); ok {
			value := time.UnixMicro(expiry).UTC()
			observed.NativeExpiry = &value
		}
		result.Targets[target.Target()] = observed
	}
	return result, nil
}

func rejectedPlan(plan OperationPlan, code string) ApplyResult {
	return ApplyResult{Kind: ResultRejected, Domain: plan.Domain, Target: plan.Target, Digest: plan.Digest, ErrorCode: code}
}

func authorizationCode(err error) string {
	var authorization *firewall.MutationAuthorizationError
	if errors.As(err, &authorization) {
		return string(authorization.Code())
	}
	return "invalid_plan"
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	return ctx.Err()
}

func firewallScopes(scopes core.EnforcementScope) []firewall.ManagedScope {
	result := make([]firewall.ManagedScope, 0, 2)
	if scopes&core.ScopeInput != 0 {
		result = append(result, firewall.ManagedScopeInput)
	}
	if scopes&core.ScopeForward != 0 {
		result = append(result, firewall.ManagedScopeForward)
	}
	return result
}

func ipcScopes(scopes core.EnforcementScope) []ipc.Scope {
	result := make([]ipc.Scope, 0, 2)
	if scopes&core.ScopeInput != 0 {
		result = append(result, ipc.ScopeInput)
	}
	if scopes&core.ScopeForward != 0 {
		result = append(result, ipc.ScopeForward)
	}
	return result
}

func ipcMembership(membership core.BanMembership) ipc.Membership {
	if membership == core.BanPresent {
		return ipc.MembershipPresent
	}
	return ipc.MembershipAbsent
}

func ipcTimeoutMode(mode core.TimeoutMode) ipc.TimeoutMode {
	if mode == core.TimeoutNative {
		return ipc.TimeoutModeNative
	}
	return ipc.TimeoutModeNone
}

func ipcDomain(domain Domain) ipc.Domain {
	switch domain {
	case DomainInfrastructure:
		return ipc.DomainInfrastructure
	case DomainPolicy:
		return ipc.DomainPolicy
	case DomainTarget:
		return ipc.DomainTarget
	default:
		return ""
	}
}

func coreTimeoutMode(mode firewall.ManagedTimeoutMode) core.TimeoutMode {
	if mode == firewall.ManagedTimeoutNative {
		return core.TimeoutNative
	}
	return core.TimeoutNone
}

func coreScopes(scopes []firewall.ManagedScope) core.EnforcementScope {
	var result core.EnforcementScope
	for _, scope := range scopes {
		if scope == firewall.ManagedScopeInput {
			result |= core.ScopeInput
		}
		if scope == firewall.ManagedScopeForward {
			result |= core.ScopeForward
		}
	}
	return result
}

func targetAddressFamily(target netip.Prefix) core.AddressFamily {
	if target.Addr().Is4() {
		return core.AddressFamilyIPv4
	}
	return core.AddressFamilyIPv6
}
