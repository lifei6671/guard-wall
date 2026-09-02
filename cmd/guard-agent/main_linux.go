//go:build linux

// guard-agent is the unprivileged process boundary for GuardWall.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

const guardAgentProbeTimeout = 5 * time.Second

var (
	errGuardIdentityUnavailable = errors.New("guard identity is unavailable")
	errGuardAgentIdentity       = errors.New("guard-agent must run as guard")
	errGuardAgentProbe          = errors.New("guard-agent readiness probe failed")
	errGuardAgentContext        = errors.New("guard-agent context is required")
)

type guardIdentity struct {
	uid uint32
	gid uint32
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	err := runGuardAgent(ctx, user.Lookup, os.Geteuid, func(ctx context.Context) error {
		_, err := ipc.RoundTripProbeCapabilities(ctx)
		return err
	})
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	// Process logs intentionally do not include peer, socket, or backend details.
	log.Print("guard-agent failed")
	os.Exit(1)
}

// runGuardAgent verifies the fixed non-privileged identity, performs exactly
// one closed IPC readiness probe, then owns the signal-cancelled process
// lifetime. Reconciliation and database ownership are intentionally outside
// this process boundary until their runtime contracts are wired.
func runGuardAgent(
	ctx context.Context,
	lookup func(string) (*user.User, error),
	euid func() int,
	probe func(context.Context) error,
) error {
	if ctx == nil {
		return errGuardAgentContext
	}
	identity, err := lookupGuardIdentity(lookup)
	if err != nil {
		return err
	}
	if euid() < 0 || uint64(euid()) != uint64(identity.uid) {
		return errGuardAgentIdentity
	}
	probeContext, cancel := context.WithTimeout(ctx, guardAgentProbeTimeout)
	defer cancel()
	if err := probe(probeContext); err != nil {
		return errors.Join(errGuardAgentProbe, err)
	}
	<-ctx.Done()
	return ctx.Err()
}

func lookupGuardIdentity(lookup func(string) (*user.User, error)) (guardIdentity, error) {
	if lookup == nil {
		return guardIdentity{}, errGuardIdentityUnavailable
	}
	account, err := lookup("guard")
	if err != nil || account == nil {
		return guardIdentity{}, errGuardIdentityUnavailable
	}
	uid, err := parseIdentityNumber(account.Uid)
	if err != nil {
		return guardIdentity{}, errGuardIdentityUnavailable
	}
	gid, err := parseIdentityNumber(account.Gid)
	if err != nil {
		return guardIdentity{}, errGuardIdentityUnavailable
	}
	return guardIdentity{uid: uid, gid: gid}, nil
}

func parseIdentityNumber(value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(parsed), nil
}
