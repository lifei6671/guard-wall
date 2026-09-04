//go:build linux

// guard-agent is the unprivileged process boundary for GuardWall.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/lifei6671/guard-wall/internal/clock"
	"github.com/lifei6671/guard-wall/internal/config"
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/firewall/nftables"
	"github.com/lifei6671/guard-wall/internal/reconcile"
	"github.com/lifei6671/guard-wall/internal/store"
	"github.com/lifei6671/guard-wall/migrations"
)

var (
	errGuardIdentityUnavailable = errors.New("guard identity is unavailable")
	errGuardAgentIdentity       = errors.New("guard-agent must run as guard")
	errGuardAgentContext        = errors.New("guard-agent context is required")
	errGuardAgentConfig         = errors.New("guard-agent configuration is invalid")
	errGuardAgentStore          = errors.New("guard-agent store is unavailable")
)

type guardIdentity struct {
	uid uint32
	gid uint32
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	configPath, err := parseGuardAgentConfigPath(os.Args[1:])
	if err == nil {
		err = runGuardAgent(
			ctx,
			user.Lookup,
			os.Geteuid,
			configPath,
			loadGuardAgentConfig,
			openGuardAgentStore,
			migrations.FS,
			newGuardAgentRuntime,
		)
	}
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	// Process logs intentionally do not include peer, socket, or backend details.
	log.Print("guard-agent failed")
	os.Exit(1)
}

type agentStore interface {
	core.NodeIdentityStore
	Close() error
}

type configLoader func(context.Context, string) (config.Config, error)

type storeOpener func(context.Context, string, fs.FS) (agentStore, error)

// guardAgentRuntime owns the post-bootstrap Reconcile lifecycle and assumes
// Store ownership when Run is called.
type guardAgentRuntime interface {
	BootstrapInitialManagedPolicy(context.Context, time.Time) (decision.PolicyChange, error)
	Run(context.Context) error
}

type guardAgentRuntimeFactory func(context.Context, core.NodeID, agentStore, config.Config) (guardAgentRuntime, error)

func openGuardAgentStore(ctx context.Context, databasePath string, migrationFS fs.FS) (agentStore, error) {
	return store.Open(ctx, databasePath, migrationFS)
}

// runGuardAgent verifies the fixed non-privileged identity, loads the explicit
// configuration, opens SQLite, then persists the NodeID and initial managed
// Policy before transferring Store ownership to the Reconcile runtime.
func runGuardAgent(
	ctx context.Context,
	lookup func(string) (*user.User, error),
	euid func() int,
	configPath string,
	loadConfig configLoader,
	openStore storeOpener,
	migrationFS fs.FS,
	newRuntime guardAgentRuntimeFactory,
) (returnErr error) {
	if ctx == nil {
		return errGuardAgentContext
	}
	if !filepath.IsAbs(configPath) || loadConfig == nil || openStore == nil || migrationFS == nil || newRuntime == nil {
		return errGuardAgentConfig
	}
	identity, err := lookupGuardIdentity(lookup)
	if err != nil {
		return err
	}
	if euid() < 0 || uint64(euid()) != uint64(identity.uid) {
		return errGuardAgentIdentity
	}
	loaded, err := loadConfig(ctx, configPath)
	if err != nil {
		return fmt.Errorf("load guard-agent configuration: %w", err)
	}
	database, err := openStore(ctx, loaded.Store.DatabasePath, migrationFS)
	if err != nil {
		return fmt.Errorf("open guard-agent store: %w", err)
	}
	if database == nil {
		return errGuardAgentStore
	}
	storeOwned := true
	defer func() {
		if !storeOwned {
			return
		}
		if err := database.Close(); err != nil {
			if returnErr == nil || errors.Is(returnErr, context.Canceled) {
				returnErr = fmt.Errorf("close guard-agent store: %w", err)
				return
			}
			returnErr = errors.Join(returnErr, fmt.Errorf("close guard-agent store: %w", err))
		}
	}()
	nodeID, err := core.BootstrapPersistentNodeID(ctx, database)
	if err != nil {
		return fmt.Errorf("bootstrap guard-agent node identity: %w", err)
	}
	runtime, err := newRuntime(ctx, nodeID, database, loaded)
	if err != nil {
		return fmt.Errorf("construct guard-agent reconcile runtime: %w", err)
	}
	if _, err := runtime.BootstrapInitialManagedPolicy(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("bootstrap guard-agent managed policy: %w", err)
	}
	// Runtime.Run closes Store after its Dispatcher and expiration scheduler
	// return. The caller must not retain a second close path after this point.
	storeOwned = false
	return runtime.Run(ctx)
}

func newGuardAgentRuntime(
	ctx context.Context,
	nodeID core.NodeID,
	database agentStore,
	loaded config.Config,
) (guardAgentRuntime, error) {
	runtimeStore, ok := database.(reconcile.RuntimeStore)
	if !ok {
		return nil, fmt.Errorf("guard-agent store does not support reconcile runtime")
	}
	revision, infrastructure := nftables.FixedDesiredInfrastructure()
	targetPolicies, err := nftables.NewFixedManagedPolicyTargetResolver()
	if err != nil {
		return nil, fmt.Errorf("construct native target policy resolver: %w", err)
	}
	runtime, err := reconcile.NewReconcileRuntime(ctx, reconcile.RuntimeDependencies{
		NodeID: nodeID, Backend: reconcile.NewIPCBackend(), Store: runtimeStore,
		Audit: guardAgentUnexpectedAudit{}, Clock: clock.NewWallClock(),
		Static:         reconcile.StaticDesiredFirewallState{InfrastructureRevision: revision, Infrastructure: infrastructure},
		TargetPolicies: targetPolicies, QueueCapacity: loaded.Runtime.ReconcileQueueCapacity,
	})
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

// guardAgentUnexpectedAudit fails closed if an in-memory-only retry path is
// ever reached. Persistent Controller retries commit their audit with SQLite.
type guardAgentUnexpectedAudit struct{}

func (guardAgentUnexpectedAudit) AppendCriticalAudit(context.Context, reconcile.CriticalAuditEvent) error {
	return errors.New("guard-agent reached non-persistent reconcile audit path")
}

func parseGuardAgentConfigPath(arguments []string) (string, error) {
	flags := flag.NewFlagSet("guard-agent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "absolute configuration file path")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*configPath) {
		return "", errGuardAgentConfig
	}
	return *configPath, nil
}

func loadGuardAgentConfig(ctx context.Context, path string) (loaded config.Config, returnErr error) {
	if ctx == nil || !filepath.IsAbs(path) {
		return config.Config{}, errGuardAgentConfig
	}
	file, err := os.Open(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close configuration: %w", closeErr))
		}
	}()
	loaded, err = config.Load(ctx, file)
	if err != nil {
		return config.Config{}, fmt.Errorf("load configuration: %w", err)
	}
	return loaded, nil
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
