//go:build linux

package ipc

import (
	"context"
	"io"
)

// SnapshotManagedHandler returns one closed typed response for an
// authenticated SnapshotManaged request.
type SnapshotManagedHandler func(context.Context) SnapshotManagedResponse

// SnapshotServerErrorCode classifies a local SnapshotManaged adapter failure.
type SnapshotServerErrorCode string

const (
	SnapshotServerErrorCodeUnavailable         SnapshotServerErrorCode = "unavailable"
	SnapshotServerErrorCodeHandlerRequired     SnapshotServerErrorCode = "handler_required"
	SnapshotServerErrorCodeUnexpectedOperation SnapshotServerErrorCode = "unexpected_operation"
)

// SnapshotServerError reports only a stable local adapter classification.
type SnapshotServerError struct {
	code SnapshotServerErrorCode
}

func (e *SnapshotServerError) Error() string {
	if e == nil {
		return "ipc managed snapshot server failed"
	}
	return "ipc managed snapshot server failed: " + string(e.code)
}

// Code returns the stable local adapter classification.
func (e *SnapshotServerError) Code() SnapshotServerErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// ServeSnapshotManagedOnce accepts, authenticates, and serves exactly one
// managed snapshot request. The adapter owns and closes the accepted
// connection. Authentication and decoding complete before handler execution.
func (l *UnixListener) ServeSnapshotManagedOnce(
	ctx context.Context,
	expectedGuardUID uint32,
	handler SnapshotManagedHandler,
) error {
	if l == nil {
		return snapshotServerError(SnapshotServerErrorCodeUnavailable)
	}
	if handler == nil {
		return snapshotServerError(SnapshotServerErrorCodeHandlerRequired)
	}
	if err := contextTerminationError(ctx); err != nil {
		return err
	}

	connection, request, err := l.AcceptRequest(ctx, expectedGuardUID)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()

	if _, ok := request.(SnapshotManagedRequest); !ok {
		return snapshotServerError(SnapshotServerErrorCodeUnexpectedOperation)
	}
	if err := contextTerminationError(ctx); err != nil {
		return err
	}

	response := handler(ctx)
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	payload, err := EncodeSnapshotManagedResponse(response)
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
		return snapshotServerError(SnapshotServerErrorCodeUnavailable)
	}
	defer stopWatch()
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	return writeSnapshotManagedServerPayload(ctx, connection, payload)
}

func writeSnapshotManagedServerPayload(ctx context.Context, writer io.Writer, payload []byte) error {
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

func snapshotServerError(code SnapshotServerErrorCode) error {
	return &SnapshotServerError{code: code}
}
