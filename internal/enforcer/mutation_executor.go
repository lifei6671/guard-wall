package enforcer

import (
	"context"
	"errors"
	"reflect"

	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

var (
	// ErrMutationBackendRequired reports a missing executor dependency.
	ErrMutationBackendRequired = errors.New("enforcer mutation backend is required")
	// ErrMutationBackendUnsupported reports a known unsupported backend or topology.
	ErrMutationBackendUnsupported = errors.New("enforcer mutation backend is unsupported")
	// ErrMutationBackendNotReady reports that a trustworthy observation is unavailable.
	ErrMutationBackendNotReady = errors.New("enforcer mutation backend is not ready")
	// ErrMutationBackendOwnershipConflict reports unproven Guard ownership.
	ErrMutationBackendOwnershipConflict = errors.New("enforcer mutation backend ownership conflict")
)

// MutationBackend is the consumer-owned privileged mutation port. Implementations
// receive only closed Firewall authorities and must revalidate the authority's
// backend, capabilities, ownership, and snapshot basis immediately before the
// first side effect. A result is Rejected only after proving zero side effects or
// complete rollback; every unproven post-state must be returned as Unknown.
type MutationBackend interface {
	Probe(context.Context) (firewall.FirewallCapabilities, error)
	Snapshot(context.Context) (firewall.ManagedSnapshot, error)
	Apply(context.Context, firewall.OperationPlan) firewall.MutationResult
	RemoveManagedInfrastructure(context.Context, firewall.RemovalAuthorization) firewall.MutationResult
}

// MutationExecutor serializes fresh observation, authorization, and at most one
// privileged mutation for each request. It stores no authority between requests
// and must not be copied after first use.
type MutationExecutor struct {
	gate    chan struct{}
	backend MutationBackend
}

// NewMutationExecutor constructs a serial mutation executor.
func NewMutationExecutor(backend MutationBackend) (*MutationExecutor, error) {
	if nilMutationBackend(backend) {
		return nil, ErrMutationBackendRequired
	}
	return &MutationExecutor{gate: make(chan struct{}, 1), backend: backend}, nil
}

func nilMutationBackend(backend MutationBackend) bool {
	if backend == nil {
		return true
	}
	value := reflect.ValueOf(backend)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Execute handles one already-decoded closed mutation request. A malformed
// internal request returns nil so the IPC adapter rejects it as a response
// mismatch; backend error text is never returned on the wire.
func (e *MutationExecutor) Execute(ctx context.Context, request ipc.MutationRequest) ipc.MutationResponse {
	if !correlatableMutationRequest(request) {
		return nil
	}
	if ctx == nil || e == nil || e.backend == nil {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}
	if err := ctx.Err(); err != nil {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}

	select {
	case e.gate <- struct{}{}:
		defer func() { <-e.gate }()
	case <-ctx.Done():
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}

	if err := ctx.Err(); err != nil {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}
	capabilities, err := e.backend.Probe(ctx)
	if err != nil {
		return rejectedMutationResponse(request, mutationObservationErrorCode(err))
	}
	if capabilities.Validate() != nil || !capabilities.MutationReady() || !capabilities.OwnershipProven() {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}
	if err := ctx.Err(); err != nil {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}

	snapshot, err := e.backend.Snapshot(ctx)
	if err != nil {
		return rejectedMutationResponse(request, mutationObservationErrorCode(err))
	}
	if snapshot.Validate() != nil {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}
	if err := ctx.Err(); err != nil {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}

	authorization, err := AuthorizeMutationRequest(request, capabilities, snapshot)
	if err != nil {
		return rejectedMutationResponse(request, mutationAuthorizationErrorCode(err))
	}
	if response, immediate := authorization.ImmediateResponse(); immediate {
		return response
	}
	mutation, ok := authorization.Mutation()
	if !ok {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}
	if err := ctx.Err(); err != nil {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}

	var result firewall.MutationResult
	switch value := mutation.(type) {
	case firewall.OperationPlan:
		result = e.backend.Apply(ctx, value)
	case firewall.RemovalAuthorization:
		result = e.backend.RemoveManagedInfrastructure(ctx, value)
	default:
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}

	response, err := MapMutationResult(authorization, result)
	if err == nil {
		return response
	}
	unknown, unknownErr := firewall.NewUnknownMutationResult(mutation)
	if unknownErr != nil {
		return nil
	}
	response, err = MapMutationResult(authorization, unknown)
	if err != nil {
		return nil
	}
	return response
}

func correlatableMutationRequest(request ipc.MutationRequest) bool {
	switch value := request.(type) {
	case ipc.ApplyManagedPlanRequest:
		return value != nil && value.Plan() != nil
	case ipc.RemoveManagedInfrastructureRequest:
		return value != nil && value.ExpectedOwnerVersion() == firewall.ManagedOwnerVersionV1
	default:
		return false
	}
}

func mutationObservationErrorCode(err error) ipc.MutationErrorCode {
	var ownershipConflict firewall.OwnershipConflictError
	switch {
	case errors.Is(err, ErrMutationBackendUnsupported):
		return ipc.MutationErrorCodeUnsupported
	case errors.Is(err, ErrMutationBackendOwnershipConflict), errors.As(err, &ownershipConflict):
		return ipc.MutationErrorCodeOwnershipConflict
	default:
		return ipc.MutationErrorCodeNotReady
	}
}

func mutationAuthorizationErrorCode(err error) ipc.MutationErrorCode {
	var bridge *BridgeError
	if !errors.As(err, &bridge) {
		return ipc.MutationErrorCodeNotReady
	}
	switch bridge.Code() {
	case BridgeErrorInvalidPlan:
		return ipc.MutationErrorCodeInvalidPlan
	case BridgeErrorOwnershipConflict:
		return ipc.MutationErrorCodeOwnershipConflict
	case BridgeErrorUnsupported:
		return ipc.MutationErrorCodeUnsupported
	default:
		return ipc.MutationErrorCodeNotReady
	}
}

func rejectedMutationResponse(request ipc.MutationRequest, code ipc.MutationErrorCode) ipc.MutationResponse {
	switch value := request.(type) {
	case ipc.ApplyManagedPlanRequest:
		if value == nil || value.Plan() == nil {
			return nil
		}
		response, err := ipc.NewApplyManagedPlanRejectedResponse(value.Plan().Domain(), code)
		if err != nil {
			return nil
		}
		return response
	case ipc.RemoveManagedInfrastructureRequest:
		if value == nil {
			return nil
		}
		if code == ipc.MutationErrorCodeInvalidPlan {
			code = ipc.MutationErrorCodeNotReady
		}
		response, err := ipc.NewRemoveManagedInfrastructureRejectedResponse(code)
		if err != nil {
			return nil
		}
		return response
	default:
		return nil
	}
}
