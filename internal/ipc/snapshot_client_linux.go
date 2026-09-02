//go:build linux

package ipc

import (
	"context"
	"net"
	"time"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

// SnapshotClientErrorCode classifies a local managed-snapshot round-trip failure.
type SnapshotClientErrorCode string

const (
	SnapshotClientErrorCodeDialFailed              SnapshotClientErrorCode = "dial_failed"
	SnapshotClientErrorCodeDeadlineFailed          SnapshotClientErrorCode = "deadline_failed"
	SnapshotClientErrorCodeContextCanceled         SnapshotClientErrorCode = "context_canceled"
	SnapshotClientErrorCodeContextDeadlineExceeded SnapshotClientErrorCode = "context_deadline_exceeded"
	SnapshotClientErrorCodeResponseMismatch        SnapshotClientErrorCode = "response_mismatch"
)

// SnapshotClientError reports only a stable local transport classification.
type SnapshotClientError struct {
	code  SnapshotClientErrorCode
	cause error
}

func (e *SnapshotClientError) Error() string {
	if e == nil {
		return "ipc managed snapshot round trip failed"
	}
	return "ipc managed snapshot round trip failed: " + string(e.code)
}

// Code returns the stable local transport classification.
func (e *SnapshotClientError) Code() SnapshotClientErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// Unwrap preserves only context cancellation identity.
func (e *SnapshotClientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// SnapshotManagedRemoteError reports one closed failure returned by the
// authenticated Enforcer.
type SnapshotManagedRemoteError struct {
	code SnapshotManagedFailureCode
}

func (e *SnapshotManagedRemoteError) Error() string {
	if e == nil {
		return "remote managed snapshot failed"
	}
	return "remote managed snapshot failed: " + string(e.code)
}

// Code returns the closed remote SnapshotManaged failure classification.
func (e *SnapshotManagedRemoteError) Code() SnapshotManagedFailureCode {
	if e == nil {
		return ""
	}
	return e.code
}

// RoundTripSnapshotManaged reads one managed Snapshot from the production
// Enforcer over the fixed Unix socket. The root peer is authenticated before
// the request is written. The caller context is the only Dial and I/O budget;
// the client never reconnects or retries.
func RoundTripSnapshotManaged(ctx context.Context) (firewall.ManagedSnapshot, error) {
	return roundTripSnapshotManagedAt(ctx, EnforcerSocketPath, 0)
}

func roundTripSnapshotManagedAt(
	ctx context.Context,
	socketPath string,
	expectedServerUID uint32,
) (firewall.ManagedSnapshot, error) {
	request := NewSnapshotManagedRequest()
	if _, err := EncodeSnapshotManagedRequest(request); err != nil {
		return firewall.ManagedSnapshot{}, err
	}
	if err := snapshotClientContextTerminationError(ctx); err != nil {
		return firewall.ManagedSnapshot{}, err
	}

	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		if contextErr := snapshotClientContextTerminationError(ctx); contextErr != nil {
			return firewall.ManagedSnapshot{}, contextErr
		}
		return firewall.ManagedSnapshot{}, snapshotClientError(SnapshotClientErrorCodeDialFailed)
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return firewall.ManagedSnapshot{}, snapshotClientError(SnapshotClientErrorCodeDialFailed)
	}
	defer func() { _ = unixConnection.Close() }()

	if err := verifyUnixPeerUID(unixConnection, expectedServerUID); err != nil {
		return firewall.ManagedSnapshot{}, err
	}
	stopWatch, err := armContextDeadline(ctx, unixConnection.SetDeadline)
	if err != nil {
		if contextErr := snapshotClientContextTerminationError(ctx); contextErr != nil {
			return firewall.ManagedSnapshot{}, contextErr
		}
		return firewall.ManagedSnapshot{}, snapshotClientError(SnapshotClientErrorCodeDeadlineFailed)
	}
	defer stopWatch()
	if contextErr := snapshotClientContextTerminationError(ctx); contextErr != nil {
		return firewall.ManagedSnapshot{}, contextErr
	}

	if err := WriteSnapshotManagedRequestFrame(unixConnection, request); err != nil {
		if contextErr := snapshotClientContextTerminationError(ctx); contextErr != nil {
			return firewall.ManagedSnapshot{}, contextErr
		}
		return firewall.ManagedSnapshot{}, err
	}
	response, err := DecodeSnapshotManagedResponseFrame(unixConnection)
	if err != nil {
		if contextErr := snapshotClientContextTerminationError(ctx); contextErr != nil {
			return firewall.ManagedSnapshot{}, contextErr
		}
		return firewall.ManagedSnapshot{}, err
	}

	switch typed := response.(type) {
	case SnapshotManagedSuccessResponse:
		snapshot := typed.Snapshot()
		if err := snapshot.Validate(); err != nil {
			return firewall.ManagedSnapshot{}, snapshotClientError(SnapshotClientErrorCodeResponseMismatch)
		}
		return snapshot, nil
	case SnapshotManagedFailureResponse:
		return firewall.ManagedSnapshot{}, &SnapshotManagedRemoteError{code: typed.FailureCode()}
	default:
		return firewall.ManagedSnapshot{}, snapshotClientError(SnapshotClientErrorCodeResponseMismatch)
	}
}

func snapshotClientContextTerminationError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return snapshotClientContextError(err)
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return snapshotClientContextError(context.DeadlineExceeded)
	}
	return nil
}

func snapshotClientContextError(err error) error {
	if err == context.DeadlineExceeded {
		return &SnapshotClientError{
			code:  SnapshotClientErrorCodeContextDeadlineExceeded,
			cause: context.DeadlineExceeded,
		}
	}
	return &SnapshotClientError{
		code:  SnapshotClientErrorCodeContextCanceled,
		cause: context.Canceled,
	}
}

func snapshotClientError(code SnapshotClientErrorCode) error {
	return &SnapshotClientError{code: code}
}
