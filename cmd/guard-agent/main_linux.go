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

	"github.com/lifei6671/guard-wall/internal/config"
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/ipc"
	"github.com/lifei6671/guard-wall/internal/store"
	"github.com/lifei6671/guard-wall/migrations"
)

const guardAgentProbeTimeout = 5 * time.Second

var (
	errGuardIdentityUnavailable = errors.New("guard identity is unavailable")
	errGuardAgentIdentity       = errors.New("guard-agent must run as guard")
	errGuardAgentProbe          = errors.New("guard-agent readiness probe failed")
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
			func(ctx context.Context) error {
				_, err := ipc.RoundTripProbeCapabilities(ctx)
				return err
			},
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

func openGuardAgentStore(ctx context.Context, databasePath string, migrationFS fs.FS) (agentStore, error) {
	return store.Open(ctx, databasePath, migrationFS)
}

// runGuardAgent verifies the fixed non-privileged identity, loads the explicit
// configuration, opens and owns SQLite, then bootstraps the durable NodeID
// before its fixed IPC readiness probe. It does not start Reconcile or issue
// Firewall mutations.
func runGuardAgent(
	ctx context.Context,
	lookup func(string) (*user.User, error),
	euid func() int,
	configPath string,
	loadConfig configLoader,
	openStore storeOpener,
	migrationFS fs.FS,
	probe func(context.Context) error,
) (returnErr error) {
	if ctx == nil {
		return errGuardAgentContext
	}
	if !filepath.IsAbs(configPath) || loadConfig == nil || openStore == nil || migrationFS == nil || probe == nil {
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
	defer func() {
		if err := database.Close(); err != nil {
			if returnErr == nil || errors.Is(returnErr, context.Canceled) {
				returnErr = fmt.Errorf("close guard-agent store: %w", err)
				return
			}
			returnErr = errors.Join(returnErr, fmt.Errorf("close guard-agent store: %w", err))
		}
	}()
	if _, err := core.BootstrapPersistentNodeID(ctx, database); err != nil {
		return fmt.Errorf("bootstrap guard-agent node identity: %w", err)
	}
	probeContext, cancel := context.WithTimeout(ctx, guardAgentProbeTimeout)
	defer cancel()
	if err := probe(probeContext); err != nil {
		return errors.Join(errGuardAgentProbe, err)
	}
	<-ctx.Done()
	return ctx.Err()
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
