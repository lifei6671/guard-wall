//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/config"
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/migrations"
)

func TestAgentInstanceLockDirectoryScope(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "guard.db")
	lock, err := acquireAgentInstanceLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{databasePath, filepath.Join(directory, "other.db"), filepath.Join(alias, "guard.db")} {
		second, err := acquireAgentInstanceLock(path)
		if second != nil {
			_ = second.Close()
			t.Fatalf("acquired competing lock for %s", path)
		}
		if !errors.Is(err, errGuardAgentAlreadyRunning) {
			t.Fatalf("competing lock = %v", err)
		}
	}
	other, err := acquireAgentInstanceLock(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatalf("independent directory: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("lock created directory entries: %v, %v", entries, err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := acquireAgentInstanceLock(databasePath)
	if err != nil {
		t.Fatalf("acquire after Close: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentInstanceLockRejectsInvalidDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative.db", filepath.Join(t.TempDir(), "missing", "guard.db"), filepath.Join(file, "guard.db")} {
		lock, err := acquireAgentInstanceLock(path)
		if lock != nil || err == nil || errors.Is(err, errGuardAgentAlreadyRunning) {
			t.Fatalf("invalid path %q = (%v, %v)", path, lock, err)
		}
	}
}

type instanceTestCloser func() error

func (close instanceTestCloser) Close() error { return close() }

type instanceTestStore struct {
	testAgentStore
	close   func() error
	loadErr error
}

func (s *instanceTestStore) Close() error { return s.close() }

func (s *instanceTestStore) LoadNodeIdentity(ctx context.Context) (core.NodeID, bool, error) {
	if s.loadErr != nil {
		return "", false, s.loadErr
	}
	return s.testAgentStore.LoadNodeIdentity(ctx)
}

func TestGuardAgentInstanceLockLifetime(t *testing.T) {
	failure := errors.New("stage failure")
	closeFailure := errors.New("database close failure")
	lockFailure := errors.New("lock close failure")
	for _, test := range []struct {
		name      string
		failStage string
		closeErr  error
		lockErr   error
		cancelRun bool
		want      []string
	}{
		{name: "normal", want: []string{"config", "lock", "open", "factory", "bootstrap", "run", "database close", "lock close"}},
		{name: "configuration fails", failStage: "config", want: []string{"config"}},
		{name: "lock fails", failStage: "lock", want: []string{"config", "lock"}},
		{name: "open fails", failStage: "open", want: []string{"config", "lock", "open", "lock close"}},
		{name: "identity initialization fails", failStage: "identity", want: []string{"config", "lock", "open", "database close", "lock close"}},
		{name: "factory fails", failStage: "factory", want: []string{"config", "lock", "open", "factory", "database close", "lock close"}},
		{name: "bootstrap fails", failStage: "bootstrap", want: []string{"config", "lock", "open", "factory", "bootstrap", "database close", "lock close"}},
		{name: "runtime fails", failStage: "run", want: []string{"config", "lock", "open", "factory", "bootstrap", "run", "database close", "lock close"}},
		{name: "startup and cleanup fail", failStage: "factory", closeErr: closeFailure, lockErr: lockFailure, want: []string{"config", "lock", "open", "factory", "database close", "lock close"}},
		{name: "runtime cleanup fails", closeErr: closeFailure, lockErr: lockFailure, want: []string{"config", "lock", "open", "factory", "bootstrap", "run", "database close", "lock close"}},
		{name: "normal cancellation", cancelRun: true, want: []string{"config", "lock", "open", "factory", "bootstrap", "run", "database close", "lock close"}},
		{name: "cancellation and both cleanup failures", cancelRun: true, closeErr: closeFailure, lockErr: lockFailure, want: []string{"config", "lock", "open", "factory", "bootstrap", "run", "database close", "lock close"}},
		{name: "run failure with cancellation and cleanup failures", failStage: "run", cancelRun: true, closeErr: closeFailure, lockErr: lockFailure, want: []string{"config", "lock", "open", "factory", "bootstrap", "run", "database close", "lock close"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var events []string
			stage := func(name string) error {
				events = append(events, name)
				if name == test.failStage {
					return failure
				}
				return nil
			}
			databasePath := filepath.Join(t.TempDir(), "guard.db")
			locked := false
			database := &instanceTestStore{close: func() error {
				if !locked {
					t.Fatal("database Close ran after unlock")
				}
				events = append(events, "database close")
				return test.closeErr
			}}
			if test.failStage == "identity" {
				database.loadErr = failure
			}
			err := runGuardAgent(context.Background(),
				func(string) (*user.User, error) { return &user.User{Uid: "1001", Gid: "1002"}, nil },
				func() int { return 1001 }, "/etc/guard/guard.yaml",
				func(context.Context, string) (config.Config, error) {
					return config.Config{Store: config.Store{DatabasePath: databasePath}}, stage("config")
				},
				func(path string) (io.Closer, error) {
					if path != databasePath {
						t.Fatalf("lock path = %q", path)
					}
					if err := stage("lock"); err != nil {
						return nil, err
					}
					locked = true
					return instanceTestCloser(func() error {
						events = append(events, "lock close")
						locked = false
						return test.lockErr
					}), nil
				},
				func(context.Context, string, fs.FS) (agentStore, error) {
					if !locked {
						t.Fatal("openStore ran before lock")
					}
					return database, stage("open")
				}, migrations.FS,
				func(context.Context, core.NodeID, agentStore, config.Config) (guardAgentRuntime, error) {
					if err := stage("factory"); err != nil {
						return nil, err
					}
					return &testGuardAgentRuntime{
						bootstrap: func(context.Context, time.Time) (decision.PolicyChange, error) {
							return decision.PolicyChange{}, stage("bootstrap")
						},
						run: func(context.Context) error {
							runErr := stage("run")
							if test.cancelRun {
								if runErr == nil {
									return context.Canceled
								}
								return errors.Join(runErr, context.Canceled)
							}
							return runErr
						},
					}, nil
				})
			if !reflect.DeepEqual(events, test.want) || locked {
				t.Fatalf("events = %v, want %v; locked = %t", events, test.want, locked)
			}
			if errors.Is(err, failure) != (test.failStage != "") || errors.Is(err, closeFailure) != (test.closeErr != nil) || errors.Is(err, lockFailure) != (test.lockErr != nil) {
				t.Fatalf("lost/unexpected error identity: %v", err)
			}
			if errors.Is(err, context.Canceled) != test.cancelRun {
				t.Fatalf("cancellation identity = %v, want cancellation %t", err, test.cancelRun)
			}
			if test.failStage == "" && !test.cancelRun && test.closeErr == nil && test.lockErr == nil && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGuardAgentCompetingInstanceDoesNotOpenStore(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "guard.db")
	lock, err := acquireAgentInstanceLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()
	err = runGuardAgent(context.Background(),
		func(string) (*user.User, error) { return &user.User{Uid: "1001", Gid: "1002"}, nil },
		func() int { return 1001 }, "/etc/guard/guard.yaml",
		func(context.Context, string) (config.Config, error) {
			return config.Config{Store: config.Store{DatabasePath: databasePath}}, nil
		},
		acquireAgentInstanceLock,
		func(context.Context, string, fs.FS) (agentStore, error) {
			t.Fatal("competing Agent reached database initialization")
			return nil, nil
		}, migrations.FS, testGuardAgentRuntimeFactory(&testGuardAgentRuntime{}))
	if !errors.Is(err, errGuardAgentAlreadyRunning) {
		t.Fatalf("competing Agent = %v", err)
	}
	if _, err := os.Stat(databasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("competing Agent created database: %v", err)
	}
}

func TestGuardAgentCanceledAfterLockReleasesWithoutOpeningStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeFailure := errors.New("lock close failure")
	closeCalls := 0
	err := runGuardAgent(ctx,
		func(string) (*user.User, error) { return &user.User{Uid: "1001", Gid: "1002"}, nil },
		func() int { return 1001 }, "/etc/guard/guard.yaml",
		func(context.Context, string) (config.Config, error) { return config.Config{}, nil },
		func(string) (io.Closer, error) {
			cancel()
			return instanceTestCloser(func() error { closeCalls++; return closeFailure }), nil
		},
		func(context.Context, string, fs.FS) (agentStore, error) {
			t.Fatal("canceled Agent reached database initialization")
			return nil, nil
		}, migrations.FS, testGuardAgentRuntimeFactory(&testGuardAgentRuntime{}))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, closeFailure) || closeCalls != 1 {
		t.Fatalf("canceled Agent = %v, lock Close calls = %d", err, closeCalls)
	}
}
