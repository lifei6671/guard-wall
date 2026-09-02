//go:build linux

// guard-enforcer is the minimal privileged process boundary for GuardWall.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"os/user"
	"syscall"
	"time"

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall/nftables"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

const enforcerRequestTimeout = 5 * time.Second

var (
	errEnforcerIdentity = errors.New("guard-enforcer must run as root")
	errEnforcerContext  = errors.New("guard-enforcer context is required")
	errEnforcerBackend  = errors.New("guard-enforcer backend is required")
)

type enforcerRunner interface {
	Run(context.Context) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	err := runGuardEnforcer(
		ctx,
		user.Lookup,
		os.Geteuid,
		func() enforcer.MutationBackend { return nftables.NewBackend() },
		ipc.ListenUnix,
		newProductionRuntime,
		func(listener *ipc.UnixListener) error { return listener.Close() },
	)
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	// Process logs intentionally do not include peer, socket, plan, or backend details.
	log.Print("guard-enforcer failed")
	os.Exit(1)
}

// runGuardEnforcer resolves the fixed guard account exactly once, installs the
// fixed root:guard socket, and transfers its lifecycle to EnforcerRuntime.
// The 5 s request budget is the initial Golden State default; any change to it
// is a production IPC-contract change and must be frozen separately.
func runGuardEnforcer(
	ctx context.Context,
	lookup func(string) (*user.User, error),
	euid func() int,
	newBackend func() enforcer.MutationBackend,
	listen func(uint32) (*ipc.UnixListener, error),
	newRuntime func(enforcer.MutationBackend, *ipc.UnixListener, uint32, ipc.EnforcerServeOptions) (enforcerRunner, error),
	closeListener func(*ipc.UnixListener) error,
) error {
	if ctx == nil {
		return errEnforcerContext
	}
	if euid() != 0 {
		return errEnforcerIdentity
	}
	identity, err := lookupGuardIdentity(lookup)
	if err != nil {
		return err
	}
	if newBackend == nil || listen == nil || newRuntime == nil || closeListener == nil {
		return errEnforcerBackend
	}
	backend := newBackend()
	if backend == nil {
		return errEnforcerBackend
	}
	listener, err := listen(identity.gid)
	if err != nil {
		return err
	}
	runtime, err := newRuntime(backend, listener, identity.uid, ipc.EnforcerServeOptions{
		RequestTimeout: enforcerRequestTimeout,
		OnRequestFailure: func(error) {
			// The loop guarantees a stable, request-local error here. Keep the
			// event observable without recording untrusted peer or payload data.
			log.Print("guard-enforcer rejected IPC request")
		},
	})
	if err != nil {
		return errors.Join(err, closeListener(listener))
	}
	return runtime.Run(ctx)
}

func newProductionRuntime(
	backend enforcer.MutationBackend,
	listener *ipc.UnixListener,
	expectedGuardUID uint32,
	options ipc.EnforcerServeOptions,
) (enforcerRunner, error) {
	return enforcer.NewEnforcerRuntime(backend, listener, expectedGuardUID, options)
}
