//go:build linux

package ipc

import (
	"context"
	"io"
)

// ProbeCapabilitiesHandler returns one closed typed response for an
// authenticated empty ProbeCapabilities request. It cannot return raw JSON,
// arbitrary error text, or backend-controlled wire fields.
type ProbeCapabilitiesHandler func(context.Context) ProbeCapabilitiesResponse

// ProbeServerErrorCode classifies a local ProbeCapabilities server adapter failure.
type ProbeServerErrorCode string

const (
	ProbeServerErrorCodeUnavailable         ProbeServerErrorCode = "unavailable"
	ProbeServerErrorCodeHandlerRequired     ProbeServerErrorCode = "handler_required"
	ProbeServerErrorCodeUnexpectedOperation ProbeServerErrorCode = "unexpected_operation"
)

// ProbeServerError reports only a stable local adapter classification.
type ProbeServerError struct {
	code ProbeServerErrorCode
}

func (e *ProbeServerError) Error() string {
	if e == nil {
		return "ipc capability probe server failed"
	}
	return "ipc capability probe server failed: " + string(e.code)
}

// Code returns the stable local adapter classification.
func (e *ProbeServerError) Code() ProbeServerErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// ServeProbeCapabilitiesOnce accepts, authenticates, and serves exactly one
// capability probe request. The adapter owns and closes the accepted
// connection. Authentication and request decoding complete before handler is
// called. A canceled context is checked after the handler and before the first
// response byte is written.
func (l *UnixListener) ServeProbeCapabilitiesOnce(
	ctx context.Context,
	expectedGuardUID uint32,
	handler ProbeCapabilitiesHandler,
) error {
	if l == nil {
		return probeServerError(ProbeServerErrorCodeUnavailable)
	}
	if handler == nil {
		return probeServerError(ProbeServerErrorCodeHandlerRequired)
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
	defer func() {
		_ = connection.Close()
	}()

	if _, ok := request.(ProbeCapabilitiesRequest); !ok {
		return probeServerError(ProbeServerErrorCodeUnexpectedOperation)
	}
	if err := contextTerminationError(ctx); err != nil {
		return err
	}

	response := handler(ctx)
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	payload, err := EncodeProbeCapabilitiesResponse(response)
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
		return probeServerError(ProbeServerErrorCodeUnavailable)
	}
	defer stopWatch()
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	return writeProbeCapabilitiesServerPayload(ctx, connection, payload)
}

func writeProbeCapabilitiesServerPayload(ctx context.Context, writer io.Writer, payload []byte) error {
	if err := writeFramePayload(writer, payload); err != nil {
		if contextErr := contextTerminationError(ctx); contextErr != nil {
			return contextErr
		}
		return err
	}
	// A complete frame is the server delivery point. Cancellation observed only
	// after that point cannot make the already-delivered response incomplete.
	return nil
}

func probeServerError(code ProbeServerErrorCode) error {
	return &ProbeServerError{code: code}
}
