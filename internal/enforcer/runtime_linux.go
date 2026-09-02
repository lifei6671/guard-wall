//go:build linux

package enforcer

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

var (
	// ErrEnforcerListenerRequired reports a missing Enforcer listener.
	ErrEnforcerListenerRequired = errors.New("enforcer listener is required")
	// ErrEnforcerRuntimeRequired reports a missing Enforcer runtime.
	ErrEnforcerRuntimeRequired = errors.New("enforcer runtime is required")
	// ErrEnforcerRuntimeStarted reports an already-running or terminated runtime.
	ErrEnforcerRuntimeStarted = errors.New("enforcer runtime has already started")
)

const (
	enforcerRuntimeReady uint32 = iota
	enforcerRuntimeRunning
	enforcerRuntimeStopped
)

// EnforcerRuntime owns one injected listener and one closed handler set for one
// MutationBackend. It must not be copied after first use. Successful construction
// transfers listener lifecycle ownership to the runtime: Run closes the listener
// after every terminal serve result, including cancellation, fatal failure, or panic.
// A pre-existing external serve owner is the only retryable result and leaves the
// listener open so its current owner is never interrupted.
type EnforcerRuntime struct {
	listener         enforcerRuntimeListener
	handlers         ipc.EnforcerHandlers
	expectedGuardUID uint32
	options          ipc.EnforcerServeOptions
	state            atomic.Uint32
}

// NewEnforcerRuntime constructs exactly one complete handler set over backend and
// transfers listener lifecycle ownership to the returned runtime.
func NewEnforcerRuntime(
	backend MutationBackend,
	listener *ipc.UnixListener,
	expectedGuardUID uint32,
	options ipc.EnforcerServeOptions,
) (*EnforcerRuntime, error) {
	if listener == nil {
		return nil, ErrEnforcerListenerRequired
	}
	return newEnforcerRuntime(backend, listener, expectedGuardUID, options)
}

func newEnforcerRuntime(
	backend MutationBackend,
	listener enforcerRuntimeListener,
	expectedGuardUID uint32,
	options ipc.EnforcerServeOptions,
) (*EnforcerRuntime, error) {
	return newEnforcerRuntimeWithFactory(backend, listener, expectedGuardUID, options, NewEnforcerHandlers)
}

func newEnforcerRuntimeWithFactory(
	backend MutationBackend,
	listener enforcerRuntimeListener,
	expectedGuardUID uint32,
	options ipc.EnforcerServeOptions,
	newHandlers enforcerHandlerFactory,
) (*EnforcerRuntime, error) {
	if listener == nil {
		return nil, ErrEnforcerListenerRequired
	}
	handlers, err := newHandlers(backend)
	if err != nil {
		return nil, err
	}
	return &EnforcerRuntime{
		listener:         listener,
		handlers:         handlers,
		expectedGuardUID: expectedGuardUID,
		options:          options,
	}, nil
}

// Run serves closed Enforcer requests until the underlying loop terminates. It
// preserves the serve error identity and joins a terminal listener Close failure.
func (r *EnforcerRuntime) Run(ctx context.Context) (err error) {
	if r == nil {
		return ErrEnforcerRuntimeRequired
	}
	if !r.state.CompareAndSwap(enforcerRuntimeReady, enforcerRuntimeRunning) {
		return ErrEnforcerRuntimeStarted
	}

	retryable := false
	defer func() {
		if retryable {
			r.state.Store(enforcerRuntimeReady)
			return
		}
		r.state.Store(enforcerRuntimeStopped)
		err = errors.Join(err, r.listener.Close())
	}()

	err = r.listener.ServeEnforcer(ctx, r.expectedGuardUID, r.handlers, r.options)
	if listenerAlreadyServing(err) {
		retryable = true
	}
	return err
}

type enforcerRuntimeListener interface {
	ServeEnforcer(context.Context, uint32, ipc.EnforcerHandlers, ipc.EnforcerServeOptions) error
	Close() error
}

type enforcerHandlerFactory func(MutationBackend) (ipc.EnforcerHandlers, error)

type listenerErrorCoder interface {
	Code() ipc.ListenerErrorCode
}

func listenerAlreadyServing(err error) bool {
	var coded listenerErrorCoder
	return errors.As(err, &coded) && coded.Code() == ipc.ListenerErrorCodeAlreadyServing
}
