//go:build linux

package ipc

import (
	"context"
	"io"
)

// EnforcerHandlers is the complete closed handler set for one authenticated
// Enforcer connection. Apply and Remove requests share the mutation handler's
// closed MutationRequest/MutationResponse contract.
type EnforcerHandlers struct {
	ProbeCapabilities ProbeCapabilitiesHandler
	SnapshotManaged   SnapshotManagedHandler
	Mutation          MutationHandler
}

// EnforcerServerErrorCode classifies a local unified Enforcer adapter failure.
type EnforcerServerErrorCode string

const (
	EnforcerServerErrorCodeUnavailable         EnforcerServerErrorCode = "unavailable"
	EnforcerServerErrorCodeHandlerRequired     EnforcerServerErrorCode = "handler_required"
	EnforcerServerErrorCodeContextRequired     EnforcerServerErrorCode = "context_required"
	EnforcerServerErrorCodeTimeoutRequired     EnforcerServerErrorCode = "timeout_required"
	EnforcerServerErrorCodeObserverRequired    EnforcerServerErrorCode = "observer_required"
	EnforcerServerErrorCodeUnexpectedOperation EnforcerServerErrorCode = "unexpected_operation"
	EnforcerServerErrorCodeResponseMismatch    EnforcerServerErrorCode = "response_mismatch"
)

// EnforcerServerError reports only a stable local router classification.
type EnforcerServerError struct {
	code EnforcerServerErrorCode
}

func (e *EnforcerServerError) Error() string {
	if e == nil {
		return "ipc enforcer server failed"
	}
	return "ipc enforcer server failed: " + string(e.code)
}

// Code returns the stable local router classification.
func (e *EnforcerServerError) Code() EnforcerServerErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// ServeEnforcerOnce accepts, authenticates, and serves exactly one closed IPC
// request. The adapter owns and closes the accepted connection, while the
// caller retains listener ownership. Authentication and decoding complete
// before exactly one typed handler is called. Response correlation and
// encoding complete before the first response byte is written.
func (l *UnixListener) ServeEnforcerOnce(
	ctx context.Context,
	expectedGuardUID uint32,
	handlers EnforcerHandlers,
) error {
	if l == nil {
		return enforcerServerError(EnforcerServerErrorCodeUnavailable)
	}
	if err := validateEnforcerHandlers(handlers); err != nil {
		return err
	}
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	releaseServeOwner, err := l.acquireServeOwner()
	if err != nil {
		return err
	}
	defer releaseServeOwner()

	connection, request, err := l.acceptRequest(ctx, expectedGuardUID)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()

	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	payload, err := encodeEnforcerServerPayload(ctx, request, handlers)
	if err != nil {
		return err
	}
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	stopWatch, err := armContextDeadline(ctx, connection.SetDeadline)
	if err != nil {
		if contextErr := contextTerminationError(ctx); contextErr != nil {
			return contextErr
		}
		return enforcerServerError(EnforcerServerErrorCodeUnavailable)
	}
	defer stopWatch()
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	return writeEnforcerServerPayload(ctx, connection, payload)
}

func encodeEnforcerServerPayload(
	ctx context.Context,
	request Request,
	handlers EnforcerHandlers,
) ([]byte, error) {
	var payload []byte
	var err error
	switch typedRequest := request.(type) {
	case ProbeCapabilitiesRequest:
		response := handlers.ProbeCapabilities(ctx)
		if err := contextTerminationError(ctx); err != nil {
			return nil, err
		}
		payload, err = EncodeProbeCapabilitiesResponse(response)
	case SnapshotManagedRequest:
		response := handlers.SnapshotManaged(ctx)
		if err := contextTerminationError(ctx); err != nil {
			return nil, err
		}
		payload, err = EncodeSnapshotManagedResponse(response)
	case MutationRequest:
		response := handlers.Mutation(ctx, typedRequest)
		if err := contextTerminationError(ctx); err != nil {
			return nil, err
		}
		if !mutationResponseMatchesRequest(typedRequest, response) {
			return nil, enforcerServerError(EnforcerServerErrorCodeResponseMismatch)
		}
		payload, err = EncodeMutationResponse(response)
	default:
		return nil, enforcerServerError(EnforcerServerErrorCodeUnexpectedOperation)
	}
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func validateEnforcerHandlers(handlers EnforcerHandlers) error {
	if handlers.ProbeCapabilities == nil || handlers.SnapshotManaged == nil || handlers.Mutation == nil {
		return enforcerServerError(EnforcerServerErrorCodeHandlerRequired)
	}
	return nil
}

func writeEnforcerServerPayload(ctx context.Context, writer io.Writer, payload []byte) error {
	if err := writeFramePayload(writer, payload); err != nil {
		if contextErr := contextTerminationError(ctx); contextErr != nil {
			return contextErr
		}
		return err
	}
	// A complete frame is the delivery point. Later cancellation cannot make
	// the already-delivered response incomplete.
	return nil
}

func enforcerServerError(code EnforcerServerErrorCode) error {
	return &EnforcerServerError{code: code}
}
