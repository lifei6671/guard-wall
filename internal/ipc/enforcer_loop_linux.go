//go:build linux

package ipc

import (
	"context"
	"errors"
	"net"
	"time"
)

// EnforcerServeOptions freezes the per-connection budget and observation
// boundary for one persistent Enforcer serve loop. OnRequestFailure is called
// synchronously with only a stable, sanitized request-local error. It must
// return promptly and must not call another blocking serve method on the same
// listener.
type EnforcerServeOptions struct {
	RequestTimeout   time.Duration
	OnRequestFailure func(error)
}

// ServeEnforcer serially accepts and serves authenticated closed IPC requests
// until the parent context terminates or a runtime-fatal failure occurs. The
// per-request timeout starts only after Accept succeeds. The loop owns every
// accepted connection, while the caller retains listener ownership. Only one
// serve operation may own a listener at a time. Handlers must honor their
// request context; the loop does not detach or force-stop them.
func (l *UnixListener) ServeEnforcer(
	ctx context.Context,
	expectedGuardUID uint32,
	handlers EnforcerHandlers,
	options EnforcerServeOptions,
) error {
	if l == nil || l.listener == nil {
		return enforcerServerError(EnforcerServerErrorCodeUnavailable)
	}
	if err := validateEnforcerHandlers(handlers); err != nil {
		return err
	}
	if ctx == nil {
		return enforcerServerError(EnforcerServerErrorCodeContextRequired)
	}
	if options.RequestTimeout <= 0 {
		return enforcerServerError(EnforcerServerErrorCodeTimeoutRequired)
	}
	if options.OnRequestFailure == nil {
		return enforcerServerError(EnforcerServerErrorCodeObserverRequired)
	}
	if err := contextTerminationError(ctx); err != nil {
		return err
	}
	releaseServeOwner, err := l.acquireServeOwner()
	if err != nil {
		return err
	}
	defer releaseServeOwner()

	for {
		connection, err := l.acceptUnix(ctx)
		if err != nil {
			return err
		}

		result := serveEnforcerAttempt(
			ctx,
			connection,
			expectedGuardUID,
			handlers,
			options.RequestTimeout,
		)

		if err := contextTerminationError(ctx); err != nil {
			return err
		}
		if result.err == nil {
			continue
		}
		if !result.requestLocal {
			return result.err
		}
		options.OnRequestFailure(result.err)
	}
}

func serveEnforcerAttempt(
	parentCtx context.Context,
	connection *net.UnixConn,
	expectedGuardUID uint32,
	handlers EnforcerHandlers,
	requestTimeout time.Duration,
) enforcerConnectionResult {
	requestCtx, cancelRequest := context.WithTimeout(parentCtx, requestTimeout)
	defer cancelRequest()
	defer func() { _ = connection.Close() }()

	return serveEnforcerConnection(requestCtx, connection, expectedGuardUID, handlers)
}

type enforcerConnectionResult struct {
	err          error
	requestLocal bool
}

func serveEnforcerConnection(
	ctx context.Context,
	connection *net.UnixConn,
	expectedGuardUID uint32,
	handlers EnforcerHandlers,
) enforcerConnectionResult {
	stopWatch, err := armContextDeadline(ctx, connection.SetDeadline)
	if err != nil {
		if contextErr := contextTerminationError(ctx); contextErr != nil {
			return localEnforcerConnectionFailure(contextErr)
		}
		return fatalEnforcerConnectionFailure(
			enforcerServerError(EnforcerServerErrorCodeUnavailable),
		)
	}
	defer stopWatch()

	request, err := DecodeUnixFrame(connection, expectedGuardUID)
	if contextErr := contextTerminationError(ctx); contextErr != nil {
		return localEnforcerConnectionFailure(contextErr)
	}
	if err != nil {
		if enforcerDecodeFailureIsRequestLocal(err) {
			return localEnforcerConnectionFailure(err)
		}
		return fatalEnforcerConnectionFailure(err)
	}

	payload, err := encodeEnforcerServerPayload(ctx, request, handlers)
	if contextErr := contextTerminationError(ctx); contextErr != nil {
		return localEnforcerConnectionFailure(contextErr)
	}
	if err != nil {
		return fatalEnforcerConnectionFailure(err)
	}
	err = writeEnforcerServerPayload(ctx, connection, payload)
	if err == nil {
		recordCompletedEnforcerOperation(request)
		return enforcerConnectionResult{}
	}
	if contextErr := contextTerminationError(ctx); contextErr != nil {
		return localEnforcerConnectionFailure(contextErr)
	}
	if enforcerWriteFailureIsRequestLocal(err) {
		return localEnforcerConnectionFailure(err)
	}
	return fatalEnforcerConnectionFailure(err)
}

func enforcerDecodeFailureIsRequestLocal(err error) bool {
	var peerError *PeerError
	if errors.As(err, &peerError) {
		return peerError.Code() == PeerErrorCodeUIDMismatch
	}
	var frameError *FrameError
	if errors.As(err, &frameError) {
		switch frameError.Code() {
		case FrameErrorCodeTruncatedLength,
			FrameErrorCodeFrameTooLarge,
			FrameErrorCodeTruncatedPayload:
			return true
		default:
			return false
		}
	}
	var validationError *ValidationError
	return errors.As(err, &validationError)
}

func enforcerWriteFailureIsRequestLocal(err error) bool {
	var frameError *FrameError
	return errors.As(err, &frameError) && frameError.Code() == FrameErrorCodeWriteFailed
}

func localEnforcerConnectionFailure(err error) enforcerConnectionResult {
	return enforcerConnectionResult{err: err, requestLocal: true}
}

func fatalEnforcerConnectionFailure(err error) enforcerConnectionResult {
	return enforcerConnectionResult{err: err}
}
