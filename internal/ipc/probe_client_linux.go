//go:build linux

package ipc

import (
	"context"
	"net"
	"time"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

// ProbeClientErrorCode classifies a local capability-probe round-trip failure.
type ProbeClientErrorCode string

const (
	ProbeClientErrorCodeDialFailed              ProbeClientErrorCode = "dial_failed"
	ProbeClientErrorCodeDeadlineFailed          ProbeClientErrorCode = "deadline_failed"
	ProbeClientErrorCodeContextCanceled         ProbeClientErrorCode = "context_canceled"
	ProbeClientErrorCodeContextDeadlineExceeded ProbeClientErrorCode = "context_deadline_exceeded"
	ProbeClientErrorCodeResponseMismatch        ProbeClientErrorCode = "response_mismatch"
)

// ProbeClientError reports only a stable local transport classification. It
// never includes socket paths, credentials, payloads, or operating-system errors.
type ProbeClientError struct {
	code  ProbeClientErrorCode
	cause error
}

func (e *ProbeClientError) Error() string {
	if e == nil {
		return "ipc capability probe round trip failed"
	}
	return "ipc capability probe round trip failed: " + string(e.code)
}

// Code returns the stable local transport classification.
func (e *ProbeClientError) Code() ProbeClientErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// Unwrap preserves only context cancellation identity. Operating-system errors
// remain intentionally hidden.
func (e *ProbeClientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// ProbeCapabilitiesRemoteError reports a closed failure returned by the
// authenticated Enforcer. It contains no backend-supplied error text.
type ProbeCapabilitiesRemoteError struct {
	code ProbeCapabilitiesFailureCode
}

func (e *ProbeCapabilitiesRemoteError) Error() string {
	if e == nil {
		return "remote capability probe failed"
	}
	return "remote capability probe failed: " + string(e.code)
}

// Code returns the closed remote ProbeCapabilities failure classification.
func (e *ProbeCapabilitiesRemoteError) Code() ProbeCapabilitiesFailureCode {
	if e == nil {
		return ""
	}
	return e.code
}

// RoundTripProbeCapabilities probes the production Enforcer over the fixed
// Unix socket and returns one validated capability snapshot. The root peer is
// authenticated before the request is written. The caller's context is the
// only Dial and I/O budget; the client sends exactly one request and never
// reconnects or retries.
func RoundTripProbeCapabilities(ctx context.Context) (firewall.FirewallCapabilities, error) {
	return roundTripProbeCapabilitiesAt(ctx, EnforcerSocketPath, 0)
}

func roundTripProbeCapabilitiesAt(
	ctx context.Context,
	socketPath string,
	expectedServerUID uint32,
) (firewall.FirewallCapabilities, error) {
	request := NewProbeCapabilitiesRequest()
	if _, err := EncodeProbeCapabilitiesRequest(request); err != nil {
		return firewall.FirewallCapabilities{}, err
	}
	if err := probeClientContextTerminationError(ctx); err != nil {
		return firewall.FirewallCapabilities{}, err
	}

	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		if contextErr := probeClientContextTerminationError(ctx); contextErr != nil {
			return firewall.FirewallCapabilities{}, contextErr
		}
		return firewall.FirewallCapabilities{}, probeClientError(ProbeClientErrorCodeDialFailed)
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return firewall.FirewallCapabilities{}, probeClientError(ProbeClientErrorCodeDialFailed)
	}
	defer func() {
		_ = unixConnection.Close()
	}()

	if err := verifyUnixPeerUID(unixConnection, expectedServerUID); err != nil {
		return firewall.FirewallCapabilities{}, err
	}
	stopWatch, err := armContextDeadline(ctx, unixConnection.SetDeadline)
	if err != nil {
		if contextErr := probeClientContextTerminationError(ctx); contextErr != nil {
			return firewall.FirewallCapabilities{}, contextErr
		}
		return firewall.FirewallCapabilities{}, probeClientError(ProbeClientErrorCodeDeadlineFailed)
	}
	defer stopWatch()
	if contextErr := probeClientContextTerminationError(ctx); contextErr != nil {
		return firewall.FirewallCapabilities{}, contextErr
	}

	if err := WriteProbeCapabilitiesRequestFrame(unixConnection, request); err != nil {
		if contextErr := probeClientContextTerminationError(ctx); contextErr != nil {
			return firewall.FirewallCapabilities{}, contextErr
		}
		return firewall.FirewallCapabilities{}, err
	}
	response, err := DecodeProbeCapabilitiesResponseFrame(unixConnection)
	if err != nil {
		if contextErr := probeClientContextTerminationError(ctx); contextErr != nil {
			return firewall.FirewallCapabilities{}, contextErr
		}
		return firewall.FirewallCapabilities{}, err
	}

	switch typed := response.(type) {
	case ProbeCapabilitiesSuccessResponse:
		capabilities := typed.Capabilities()
		if err := capabilities.Validate(); err != nil {
			return firewall.FirewallCapabilities{}, probeClientError(ProbeClientErrorCodeResponseMismatch)
		}
		return capabilities, nil
	case ProbeCapabilitiesFailureResponse:
		return firewall.FirewallCapabilities{}, &ProbeCapabilitiesRemoteError{code: typed.FailureCode()}
	default:
		return firewall.FirewallCapabilities{}, probeClientError(ProbeClientErrorCodeResponseMismatch)
	}
}

func probeClientContextTerminationError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return probeClientContextError(err)
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return probeClientContextError(context.DeadlineExceeded)
	}
	return nil
}

func probeClientContextError(err error) error {
	if err == context.DeadlineExceeded {
		return &ProbeClientError{
			code:  ProbeClientErrorCodeContextDeadlineExceeded,
			cause: context.DeadlineExceeded,
		}
	}
	return &ProbeClientError{
		code:  ProbeClientErrorCodeContextCanceled,
		cause: context.Canceled,
	}
}

func probeClientError(code ProbeClientErrorCode) error {
	return &ProbeClientError{code: code}
}
