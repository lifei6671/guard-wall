//go:build linux

package ipc

import (
	"context"
	"io"
)

// MutationHandler returns one closed typed response for an authenticated
// mutation request. It cannot return raw JSON or arbitrary error text.
type MutationHandler func(context.Context, MutationRequest) MutationResponse

// MutationServerErrorCode classifies a local mutation server adapter failure.
type MutationServerErrorCode string

const (
	MutationServerErrorCodeUnavailable         MutationServerErrorCode = "unavailable"
	MutationServerErrorCodeHandlerRequired     MutationServerErrorCode = "handler_required"
	MutationServerErrorCodeUnexpectedOperation MutationServerErrorCode = "unexpected_operation"
	MutationServerErrorCodeResponseMismatch    MutationServerErrorCode = "response_mismatch"
)

// MutationServerError reports only a stable local adapter classification.
type MutationServerError struct {
	code MutationServerErrorCode
}

func (e *MutationServerError) Error() string {
	if e == nil {
		return "ipc mutation server failed"
	}
	return "ipc mutation server failed: " + string(e.code)
}

// Code returns the stable local adapter classification.
func (e *MutationServerError) Code() MutationServerErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// ServeMutationOnce accepts, authenticates, and serves exactly one mutation
// request. The adapter owns and closes the accepted connection. Authentication
// and decoding complete before handler execution; response correlation and
// encoding complete before the first response byte is written.
func (l *UnixListener) ServeMutationOnce(
	ctx context.Context,
	expectedGuardUID uint32,
	handler MutationHandler,
) error {
	if l == nil {
		return mutationServerError(MutationServerErrorCodeUnavailable)
	}
	if handler == nil {
		return mutationServerError(MutationServerErrorCodeHandlerRequired)
	}
	if err := contextTerminationError(ctx); err != nil {
		return err
	}

	connection, request, err := l.AcceptRequest(ctx, expectedGuardUID)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()

	mutationRequest, ok := request.(MutationRequest)
	if !ok {
		return mutationServerError(MutationServerErrorCodeUnexpectedOperation)
	}
	if err := contextTerminationError(ctx); err != nil {
		return err
	}

	response := handler(ctx, mutationRequest)
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	if !mutationResponseMatchesRequest(mutationRequest, response) {
		return mutationServerError(MutationServerErrorCodeResponseMismatch)
	}
	payload, err := EncodeMutationResponse(response)
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
		return mutationServerError(MutationServerErrorCodeUnavailable)
	}
	defer stopWatch()
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	return writeMutationServerPayload(ctx, connection, payload)
}

func writeMutationServerPayload(ctx context.Context, writer io.Writer, payload []byte) error {
	if err := writeFramePayload(writer, payload); err != nil {
		if contextErr := contextTerminationError(ctx); contextErr != nil {
			return contextErr
		}
		return err
	}
	// A complete frame is the delivery point. Later cancellation cannot make the
	// already-delivered response incomplete.
	return nil
}

func mutationServerError(code MutationServerErrorCode) error {
	return &MutationServerError{code: code}
}
