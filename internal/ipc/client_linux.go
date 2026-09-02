//go:build linux

package ipc

import (
	"context"
	"net"
	"time"
)

// MutationClientErrorCode classifies a local mutation round-trip failure.
type MutationClientErrorCode string

const (
	MutationClientErrorCodeDialFailed              MutationClientErrorCode = "dial_failed"
	MutationClientErrorCodeDeadlineFailed          MutationClientErrorCode = "deadline_failed"
	MutationClientErrorCodeContextCanceled         MutationClientErrorCode = "context_canceled"
	MutationClientErrorCodeContextDeadlineExceeded MutationClientErrorCode = "context_deadline_exceeded"
	MutationClientErrorCodeResponseMismatch        MutationClientErrorCode = "response_mismatch"
)

// MutationClientError reports only a stable local client classification. It
// never includes socket paths, credentials, payloads, or operating-system errors.
type MutationClientError struct {
	code  MutationClientErrorCode
	cause error
}

func (e *MutationClientError) Error() string {
	if e == nil {
		return "ipc mutation round trip failed"
	}
	return "ipc mutation round trip failed: " + string(e.code)
}

// Code returns the stable local client failure classification.
func (e *MutationClientError) Code() MutationClientErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// Unwrap preserves only context cancellation identity. Operating-system errors
// remain intentionally hidden.
func (e *MutationClientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// RoundTripMutation sends one typed mutation request to the production
// Enforcer and returns one correlated typed response. The production socket and
// root peer identity are fixed. The caller's context is the only Dial and I/O
// time budget. The client never retries and never constructs an Unknown wire
// response; after a write begins, callers must treat any missing or mismatched
// response as an indeterminate mutation result and reconcile before retrying.
func RoundTripMutation(ctx context.Context, request MutationRequest) (MutationResponse, error) {
	return roundTripMutationAt(ctx, EnforcerSocketPath, 0, request)
}

func roundTripMutationAt(
	ctx context.Context,
	socketPath string,
	expectedServerUID uint32,
	request MutationRequest,
) (MutationResponse, error) {
	// Reject invalid typed state before touching the transport. The public frame
	// writer validates again immediately before its first write.
	if _, err := EncodeMutationRequest(request); err != nil {
		return nil, err
	}
	if err := mutationClientContextTerminationError(ctx); err != nil {
		return nil, err
	}

	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		if contextErr := mutationClientContextTerminationError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, mutationClientError(MutationClientErrorCodeDialFailed)
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, mutationClientError(MutationClientErrorCodeDialFailed)
	}
	defer func() {
		// A complete correlated response determines the remote result. A local
		// close failure cannot safely replace that result or improve an earlier
		// transport error, so connection cleanup is deliberately best-effort.
		_ = unixConnection.Close()
	}()

	if err := verifyUnixPeerUID(unixConnection, expectedServerUID); err != nil {
		return nil, err
	}
	stopWatch, err := armContextDeadline(ctx, unixConnection.SetDeadline)
	if err != nil {
		if contextErr := mutationClientContextTerminationError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, mutationClientError(MutationClientErrorCodeDeadlineFailed)
	}
	defer stopWatch()
	if contextErr := mutationClientContextTerminationError(ctx); contextErr != nil {
		return nil, contextErr
	}

	if err := WriteMutationRequestFrame(unixConnection, request); err != nil {
		if contextErr := mutationClientContextTerminationError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	response, err := DecodeMutationResponseFrame(unixConnection)
	if err != nil {
		if contextErr := mutationClientContextTerminationError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	if !mutationResponseMatchesRequest(request, response) {
		return nil, mutationClientError(MutationClientErrorCodeResponseMismatch)
	}
	return response, nil
}

func mutationResponseMatchesRequest(request MutationRequest, response MutationResponse) bool {
	if request == nil || response == nil || request.Operation() != response.Operation() {
		return false
	}

	switch typedRequest := request.(type) {
	case *applyManagedPlanRequest:
		typedResponse, ok := response.(*applyManagedPlanResponse)
		return ok && typedRequest != nil && typedRequest.plan != nil && typedResponse != nil &&
			typedResponse.domain == typedRequest.plan.Domain()
	case *removeManagedInfrastructureRequest:
		typedResponse, ok := response.(*removeManagedInfrastructureResponse)
		return ok && typedRequest != nil && typedResponse != nil
	default:
		return false
	}
}

func mutationClientContextTerminationError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return mutationClientContextError(err)
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return mutationClientContextError(context.DeadlineExceeded)
	}
	return nil
}

func mutationClientContextError(err error) error {
	if err == context.DeadlineExceeded {
		return &MutationClientError{
			code:  MutationClientErrorCodeContextDeadlineExceeded,
			cause: context.DeadlineExceeded,
		}
	}
	return &MutationClientError{
		code:  MutationClientErrorCodeContextCanceled,
		cause: context.Canceled,
	}
}

func mutationClientError(code MutationClientErrorCode) error {
	return &MutationClientError{code: code}
}
