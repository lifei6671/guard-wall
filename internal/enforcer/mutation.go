// Package enforcer contains privileged, side-effect-free authorization bridges.
package enforcer

import (
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

// BridgeErrorCode is a stable local semantic-bridge failure classification.
type BridgeErrorCode string

const (
	BridgeErrorInvalidPlan       BridgeErrorCode = "invalid_plan"
	BridgeErrorOwnershipConflict BridgeErrorCode = "ownership_conflict"
	BridgeErrorUnsupported       BridgeErrorCode = "unsupported"
	BridgeErrorNotReady          BridgeErrorCode = "not_ready"
	BridgeErrorSnapshotMismatch  BridgeErrorCode = "snapshot_mismatch"
	BridgeErrorResultMismatch    BridgeErrorCode = "result_mismatch"
)

// BridgeError contains no request values, object names, or backend errors.
type BridgeError struct {
	code BridgeErrorCode
}

func (e *BridgeError) Error() string {
	if e == nil {
		return "enforcer mutation bridge failed"
	}
	return "enforcer mutation bridge failed: " + string(e.code)
}

// Code returns the stable local failure classification.
func (e *BridgeError) Code() BridgeErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// Authorization contains exactly one executable Firewall authority or one
// immediate, side-effect-free IPC response.
type Authorization struct {
	mutation  firewall.AuthorizedMutation
	immediate ipc.MutationResponse
	valid     bool
}

// Mutation returns the authorized Firewall mutation when backend work is required.
func (a Authorization) Mutation() (firewall.AuthorizedMutation, bool) {
	return a.mutation, a.valid && a.mutation != nil && a.immediate == nil
}

// ImmediateResponse returns a response that requires no backend work.
func (a Authorization) ImmediateResponse() (ipc.MutationResponse, bool) {
	return a.immediate, a.valid && a.immediate != nil && a.mutation == nil
}

// AuthorizeMutationRequest maps a validated IPC mutation into a Firewall
// authority using the supplied current capabilities and managed snapshot.
// It performs no I/O and cannot invoke a backend.
func AuthorizeMutationRequest(
	request ipc.MutationRequest,
	capabilities firewall.FirewallCapabilities,
	snapshot firewall.ManagedSnapshot,
) (Authorization, error) {
	switch value := request.(type) {
	case ipc.ApplyManagedPlanRequest:
		if value == nil || value.Operation() != ipc.OperationApplyManagedPlan {
			return Authorization{}, bridgeError(BridgeErrorInvalidPlan)
		}
		mutation, err := authorizeApplyPlan(value.Plan(), capabilities, snapshot)
		if err != nil {
			return Authorization{}, err
		}
		return Authorization{mutation: mutation, valid: true}, nil
	case ipc.RemoveManagedInfrastructureRequest:
		if value == nil || value.Operation() != ipc.OperationRemoveManagedInfrastructure ||
			value.ExpectedOwnerVersion() == "" {
			return Authorization{}, bridgeError(BridgeErrorInvalidPlan)
		}
		removal, alreadyAbsent, err := firewall.AuthorizeManagedRemoval(
			capabilities, snapshot, value.ExpectedOwnerVersion(),
		)
		if err != nil {
			return Authorization{}, mapAuthorizationError(err)
		}
		if alreadyAbsent {
			return Authorization{
				immediate: ipc.NewRemoveManagedInfrastructureConfirmedResponse(),
				valid:     true,
			}, nil
		}
		return Authorization{mutation: removal, valid: true}, nil
	default:
		return Authorization{}, bridgeError(BridgeErrorInvalidPlan)
	}
}

func authorizeApplyPlan(
	plan ipc.ManagedPlan,
	capabilities firewall.FirewallCapabilities,
	snapshot firewall.ManagedSnapshot,
) (firewall.AuthorizedMutation, error) {
	if plan == nil || plan.OwnerVersion() != firewall.ManagedOwnerVersionV1 ||
		plan.BasisSnapshotDigest() == "" || plan.Revision() <= 0 {
		return nil, bridgeError(BridgeErrorInvalidPlan)
	}

	var (
		mutation firewall.AuthorizedMutation
		err      error
	)
	switch value := plan.(type) {
	case ipc.InfrastructurePlan:
		if value == nil || value.Domain() != ipc.DomainInfrastructure ||
			value.SchemaVersion() != firewall.ManagedInfrastructureSchemaVersionV1 {
			return nil, bridgeError(BridgeErrorInvalidPlan)
		}
		mutation, err = firewall.AuthorizeInfrastructureMutation(
			capabilities, snapshot, value.BasisSnapshotDigest(), value.Revision(),
		)
	case ipc.PolicyPlan:
		if value == nil || value.Domain() != ipc.DomainPolicy {
			return nil, bridgeError(BridgeErrorInvalidPlan)
		}
		mutation, err = firewall.AuthorizePolicyMutation(
			capabilities, snapshot, value.BasisSnapshotDigest(), value.Revision(),
			value.Allowlist(), value.ProtectedTargets(),
		)
	case ipc.TargetPlan:
		if value == nil || value.Domain() != ipc.DomainTarget {
			return nil, bridgeError(BridgeErrorInvalidPlan)
		}
		membership, ok := mapMembership(value.Membership())
		if !ok {
			return nil, bridgeError(BridgeErrorInvalidPlan)
		}
		timeoutMode, ok := mapTimeoutMode(value.TimeoutMode())
		if !ok {
			return nil, bridgeError(BridgeErrorInvalidPlan)
		}
		scopes, ok := mapScopes(value.Scopes())
		if !ok {
			return nil, bridgeError(BridgeErrorInvalidPlan)
		}
		expiry, hasExpiry := value.EffectiveUntilUnixMicro()
		mutation, err = firewall.AuthorizeTargetMutation(
			capabilities, snapshot, value.BasisSnapshotDigest(), value.Revision(),
			value.Target(), membership, timeoutMode, expiry, hasExpiry, scopes,
		)
	default:
		return nil, bridgeError(BridgeErrorInvalidPlan)
	}
	if err != nil {
		return nil, mapAuthorizationError(err)
	}
	return mutation, nil
}

// MapMutationResult maps one correlated Firewall result into a closed IPC response.
func MapMutationResult(expected Authorization, result firewall.MutationResult) (ipc.MutationResponse, error) {
	mutation, ok := expected.Mutation()
	if !ok || result.Validate() != nil || result.Operation() != mutation.Operation() ||
		result.MutationDigest() != mutation.Digest() {
		return nil, bridgeError(BridgeErrorResultMismatch)
	}

	if plan, apply := mutation.(firewall.OperationPlan); apply {
		domain, hasDomain := result.Domain()
		if !hasDomain || domain != plan.Domain() {
			return nil, bridgeError(BridgeErrorResultMismatch)
		}
		ipcDomain, ok := mapResultDomain(domain)
		if !ok {
			return nil, bridgeError(BridgeErrorResultMismatch)
		}
		return mapApplyResult(ipcDomain, result)
	}
	if removal, remove := mutation.(firewall.RemovalAuthorization); remove {
		if removal == nil {
			return nil, bridgeError(BridgeErrorResultMismatch)
		}
		if _, hasDomain := result.Domain(); hasDomain {
			return nil, bridgeError(BridgeErrorResultMismatch)
		}
		return mapRemovalResult(result)
	}
	return nil, bridgeError(BridgeErrorResultMismatch)
}

func mapApplyResult(domain ipc.Domain, result firewall.MutationResult) (ipc.MutationResponse, error) {
	switch result.Status() {
	case firewall.MutationStatusConfirmed:
		response, err := ipc.NewApplyManagedPlanConfirmedResponse(domain)
		return response, err
	case firewall.MutationStatusUnknown:
		response, err := ipc.NewApplyManagedPlanUnknownResponse(domain)
		return response, err
	case firewall.MutationStatusRejected:
		code, present := result.ErrorCode()
		if !present {
			return nil, bridgeError(BridgeErrorResultMismatch)
		}
		mapped, ok := mapResultErrorCode(code)
		if !ok {
			return nil, bridgeError(BridgeErrorResultMismatch)
		}
		response, err := ipc.NewApplyManagedPlanRejectedResponse(domain, mapped)
		return response, err
	default:
		return nil, bridgeError(BridgeErrorResultMismatch)
	}
}

func mapRemovalResult(result firewall.MutationResult) (ipc.MutationResponse, error) {
	switch result.Status() {
	case firewall.MutationStatusConfirmed:
		return ipc.NewRemoveManagedInfrastructureConfirmedResponse(), nil
	case firewall.MutationStatusUnknown:
		return ipc.NewRemoveManagedInfrastructureUnknownResponse(), nil
	case firewall.MutationStatusRejected:
		code, present := result.ErrorCode()
		if !present || code == firewall.MutationErrorInvalidPlan {
			return nil, bridgeError(BridgeErrorResultMismatch)
		}
		mapped, ok := mapResultErrorCode(code)
		if !ok {
			return nil, bridgeError(BridgeErrorResultMismatch)
		}
		response, err := ipc.NewRemoveManagedInfrastructureRejectedResponse(mapped)
		return response, err
	default:
		return nil, bridgeError(BridgeErrorResultMismatch)
	}
}

func mapAuthorizationError(err error) error {
	authorizationError, ok := err.(*firewall.MutationAuthorizationError)
	if !ok || authorizationError == nil {
		return bridgeError(BridgeErrorNotReady)
	}
	switch authorizationError.Code() {
	case firewall.MutationAuthorizationErrorInvalidPlan:
		return bridgeError(BridgeErrorInvalidPlan)
	case firewall.MutationAuthorizationErrorOwnershipConflict:
		return bridgeError(BridgeErrorOwnershipConflict)
	case firewall.MutationAuthorizationErrorUnsupported:
		return bridgeError(BridgeErrorUnsupported)
	case firewall.MutationAuthorizationErrorNotReady:
		return bridgeError(BridgeErrorNotReady)
	case firewall.MutationAuthorizationErrorSnapshotMismatch:
		return bridgeError(BridgeErrorSnapshotMismatch)
	default:
		return bridgeError(BridgeErrorNotReady)
	}
}

func mapMembership(value ipc.Membership) (firewall.TargetMembership, bool) {
	switch value {
	case ipc.MembershipPresent:
		return firewall.TargetMembershipPresent, true
	case ipc.MembershipAbsent:
		return firewall.TargetMembershipAbsent, true
	default:
		return "", false
	}
}

func mapTimeoutMode(value ipc.TimeoutMode) (firewall.ManagedTimeoutMode, bool) {
	switch value {
	case ipc.TimeoutModeNone:
		return firewall.ManagedTimeoutNone, true
	case ipc.TimeoutModeNative:
		return firewall.ManagedTimeoutNative, true
	default:
		return "", false
	}
}

func mapScopes(values []ipc.Scope) ([]firewall.ManagedScope, bool) {
	mapped := make([]firewall.ManagedScope, len(values))
	for index, value := range values {
		switch value {
		case ipc.ScopeInput:
			mapped[index] = firewall.ManagedScopeInput
		case ipc.ScopeForward:
			mapped[index] = firewall.ManagedScopeForward
		default:
			return nil, false
		}
	}
	return mapped, true
}

func mapResultDomain(domain firewall.MutationDomain) (ipc.Domain, bool) {
	switch domain {
	case firewall.MutationDomainInfrastructure:
		return ipc.DomainInfrastructure, true
	case firewall.MutationDomainPolicy:
		return ipc.DomainPolicy, true
	case firewall.MutationDomainTarget:
		return ipc.DomainTarget, true
	default:
		return "", false
	}
}

func mapResultErrorCode(code firewall.MutationErrorCode) (ipc.MutationErrorCode, bool) {
	switch code {
	case firewall.MutationErrorInvalidPlan:
		return ipc.MutationErrorCodeInvalidPlan, true
	case firewall.MutationErrorOwnershipConflict:
		return ipc.MutationErrorCodeOwnershipConflict, true
	case firewall.MutationErrorUnsupported:
		return ipc.MutationErrorCodeUnsupported, true
	case firewall.MutationErrorNotReady:
		return ipc.MutationErrorCodeNotReady, true
	case firewall.MutationErrorBackendRejected:
		return ipc.MutationErrorCodeBackendRejected, true
	default:
		return "", false
	}
}

func bridgeError(code BridgeErrorCode) error { return &BridgeError{code: code} }
