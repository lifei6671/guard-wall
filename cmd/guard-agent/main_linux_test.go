//go:build linux

package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lifei6671/guard-wall/internal/config"
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
	"github.com/lifei6671/guard-wall/migrations"
)

func TestLookupGuardIdentity(t *testing.T) {
	tests := []struct {
		name    string
		lookup  func(string) (*user.User, error)
		want    guardIdentity
		wantErr error
	}{
		{
			name: "valid",
			lookup: func(name string) (*user.User, error) {
				if name != "guard" {
					t.Fatalf("lookup name = %q", name)
				}
				return &user.User{Uid: "1001", Gid: "1002"}, nil
			},
			want: guardIdentity{uid: 1001, gid: 1002},
		},
		{name: "missing", lookup: func(string) (*user.User, error) { return nil, errors.New("missing") }, wantErr: errGuardIdentityUnavailable},
		{name: "nil", lookup: func(string) (*user.User, error) { return nil, nil }, wantErr: errGuardIdentityUnavailable},
		{name: "invalid uid", lookup: func(string) (*user.User, error) { return &user.User{Uid: "-1", Gid: "1002"}, nil }, wantErr: errGuardIdentityUnavailable},
		{name: "oversized gid", lookup: func(string) (*user.User, error) { return &user.User{Uid: "1001", Gid: "4294967296"}, nil }, wantErr: errGuardIdentityUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := lookupGuardIdentity(test.lookup)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("lookupGuardIdentity() error = %v, want %v", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("lookupGuardIdentity() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRunGuardAgent(t *testing.T) {
	identityLookup := func(string) (*user.User, error) {
		return &user.User{Uid: "1001", Gid: "1002"}, nil
	}

	configPath := filepath.Join(string(filepath.Separator), "etc", "guard", "guard.yaml")
	loadedConfig := config.Config{Store: config.Store{DatabasePath: filepath.Join(t.TempDir(), "guard.db")}}
	migrationFS := fstest.MapFS{"0001_m0.sql": {Data: []byte("-- test only")}}

	t.Run("wrong identity does not load configuration", func(t *testing.T) {
		called := false
		err := runGuardAgent(context.Background(), identityLookup, func() int { return 0 }, configPath, func(context.Context, string) (config.Config, error) {
			called = true
			return loadedConfig, nil
		}, func(context.Context, string, fs.FS) (agentStore, error) {
			t.Fatal("store must not open before identity validation")
			return nil, nil
		}, migrationFS, func(context.Context) error { return nil })
		if !errors.Is(err, errGuardAgentIdentity) || called {
			t.Fatalf("runGuardAgent() = %v, config loaded = %v", err, called)
		}
	})

	t.Run("configuration failure does not open store or probe", func(t *testing.T) {
		probeCalled := false
		err := runGuardAgent(context.Background(), identityLookup, func() int { return 1001 }, configPath,
			func(context.Context, string) (config.Config, error) {
				return config.Config{}, errors.New("config failed")
			},
			func(context.Context, string, fs.FS) (agentStore, error) {
				t.Fatal("store must not open after configuration failure")
				return nil, nil
			}, migrationFS, func(context.Context) error {
				probeCalled = true
				return nil
			})
		if err == nil || probeCalled {
			t.Fatalf("runGuardAgent() = %v, probe called = %v", err, probeCalled)
		}
	})

	t.Run("probe failure is preserved and closes store", func(t *testing.T) {
		database := &testAgentStore{}
		probeErr := errors.New("probe failed")
		err := runGuardAgent(context.Background(), identityLookup, func() int { return 1001 }, configPath,
			func(context.Context, string) (config.Config, error) { return loadedConfig, nil },
			openTestAgentStore(database), migrationFS, func(context.Context) error { return probeErr })
		if !errors.Is(err, errGuardAgentProbe) || !errors.Is(err, probeErr) || !database.closed || database.nodeID == "" {
			t.Fatalf("runGuardAgent() = %v, closed = %v, nodeID = %q", err, database.closed, database.nodeID)
		}
	})

	t.Run("probe receives fixed readiness deadline", func(t *testing.T) {
		var deadline time.Time
		database := &testAgentStore{}
		err := runGuardAgent(context.Background(), identityLookup, func() int { return 1001 }, configPath,
			func(context.Context, string) (config.Config, error) { return loadedConfig, nil },
			openTestAgentStore(database), migrationFS, func(ctx context.Context) error {
				var ok bool
				deadline, ok = ctx.Deadline()
				if !ok {
					t.Fatal("probe context has no deadline")
				}
				return errors.New("stop after deadline observation")
			})
		if !errors.Is(err, errGuardAgentProbe) || deadline.IsZero() || time.Until(deadline) <= 0 || time.Until(deadline) > guardAgentProbeTimeout {
			t.Fatalf("probe deadline = %s, error = %v", deadline, err)
		}
	})

	t.Run("ready process waits for cancellation then closes store", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		database := &testAgentStore{}
		done := make(chan error, 1)
		go func() {
			done <- runGuardAgent(ctx, identityLookup, func() int { return 1001 }, configPath,
				func(context.Context, string) (config.Config, error) { return loadedConfig, nil },
				openTestAgentStore(database), migrationFS, func(context.Context) error { return nil })
		}()
		select {
		case err := <-done:
			t.Fatalf("runGuardAgent returned before cancellation: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) || !database.closed {
				t.Fatalf("runGuardAgent() = %v, closed = %v", err, database.closed)
			}
		case <-time.After(time.Second):
			t.Fatal("runGuardAgent did not stop after cancellation")
		}
	})
}

func openTestAgentStore(database agentStore) storeOpener {
	return func(context.Context, string, fs.FS) (agentStore, error) { return database, nil }
}

type testAgentStore struct {
	nodeID core.NodeID
	closed bool
}

func (s *testAgentStore) LoadNodeIdentity(context.Context) (core.NodeID, bool, error) {
	return s.nodeID, s.nodeID != "", nil
}

func (s *testAgentStore) CreateNodeIdentity(_ context.Context, nodeID core.NodeID, _ time.Time) (core.NodeID, error) {
	s.nodeID = nodeID
	return nodeID, nil
}

func (s *testAgentStore) Close() error {
	s.closed = true
	return nil
}

func TestParseGuardAgentConfigPath(t *testing.T) {
	absolute := filepath.Join(string(filepath.Separator), "etc", "guard", "guard.yaml")
	if got, err := parseGuardAgentConfigPath([]string{"--config", absolute}); err != nil || got != absolute {
		t.Fatalf("parseGuardAgentConfigPath() = (%q, %v), want (%q, nil)", got, err, absolute)
	}
	for _, arguments := range [][]string{nil, {"--config", "relative.yaml"}, {"--config", absolute, "extra"}} {
		if _, err := parseGuardAgentConfigPath(arguments); !errors.Is(err, errGuardAgentConfig) {
			t.Fatalf("parseGuardAgentConfigPath(%q) error = %v, want errGuardAgentConfig", arguments, err)
		}
	}
}

func TestRunGuardAgentLoadsConfiguredStoreAndPersistsNodeID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "guard.db")
	configPath := filepath.Join(t.TempDir(), "guard.yaml")
	configYAML := "store:\n  database_path: " + databasePath + "\n"
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	probeStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runGuardAgent(
			ctx,
			func(string) (*user.User, error) { return &user.User{Uid: "1001", Gid: "1002"}, nil },
			func() int { return 1001 },
			configPath,
			loadGuardAgentConfig,
			openGuardAgentStore,
			migrations.FS,
			func(context.Context) error {
				close(probeStarted)
				return nil
			},
		)
	}()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("guard-agent did not reach readiness probe")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runGuardAgent() = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("guard-agent did not stop after cancellation")
	}

	database, err := store.Open(context.Background(), databasePath, migrations.FS)
	if err != nil {
		t.Fatalf("reopen configured store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	nodeID, found, err := database.LoadNodeIdentity(context.Background())
	if err != nil || !found || nodeID == "" {
		t.Fatalf("LoadNodeIdentity() = (%q, %t, %v), want persisted NodeID", nodeID, found, err)
	}
}
