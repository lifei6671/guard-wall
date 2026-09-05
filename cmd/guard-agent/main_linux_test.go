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
	"github.com/lifei6671/guard-wall/internal/decision"
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
		}, acquireAgentInstanceLock, func(context.Context, string, fs.FS) (agentStore, error) {
			t.Fatal("store must not open before identity validation")
			return nil, nil
		}, migrationFS, func(context.Context, core.NodeID, agentStore, config.Config) (guardAgentRuntime, error) {
			t.Fatal("runtime must not construct before identity validation")
			return nil, nil
		})
		if !errors.Is(err, errGuardAgentIdentity) || called {
			t.Fatalf("runGuardAgent() = %v, config loaded = %v", err, called)
		}
	})

	t.Run("runtime construction failure closes store", func(t *testing.T) {
		database := &testAgentStore{}
		want := errors.New("runtime construction failed")
		err := runGuardAgent(context.Background(), identityLookup, func() int { return 1001 }, configPath,
			func(context.Context, string) (config.Config, error) { return loadedConfig, nil }, acquireAgentInstanceLock, openTestAgentStore(database), migrationFS,
			func(context.Context, core.NodeID, agentStore, config.Config) (guardAgentRuntime, error) {
				return nil, want
			})
		if !errors.Is(err, want) || database.closeCalls != 1 || database.nodeID == "" {
			t.Fatalf("runGuardAgent() = %v, close calls = %d, nodeID = %q", err, database.closeCalls, database.nodeID)
		}
	})

	t.Run("policy bootstrap failure closes store without run", func(t *testing.T) {
		database := &testAgentStore{}
		want := errors.New("bootstrap failed")
		runtime := &testGuardAgentRuntime{bootstrap: func(context.Context, time.Time) (decision.PolicyChange, error) { return decision.PolicyChange{}, want }}
		err := runGuardAgent(context.Background(), identityLookup, func() int { return 1001 }, configPath,
			func(context.Context, string) (config.Config, error) { return loadedConfig, nil }, acquireAgentInstanceLock, openTestAgentStore(database), migrationFS,
			testGuardAgentRuntimeFactory(runtime))
		if !errors.Is(err, want) || runtime.runCalls != 0 || database.closeCalls != 1 {
			t.Fatalf("runGuardAgent() = %v, runtime runs = %d, close calls = %d", err, runtime.runCalls, database.closeCalls)
		}
	})

	t.Run("agent closes store after runtime returns", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		database := &testAgentStore{}
		started := make(chan struct{})
		runtime := &testGuardAgentRuntime{run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			if database.closeCalls != 0 {
				t.Error("Agent closed Store while runtime was using it")
			}
			return nil
		}}
		done := make(chan error, 1)
		go func() {
			done <- runGuardAgent(ctx, identityLookup, func() int { return 1001 }, configPath,
				func(context.Context, string) (config.Config, error) { return loadedConfig, nil }, acquireAgentInstanceLock, openTestAgentStore(database), migrationFS,
				testGuardAgentRuntimeFactory(runtime))
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("guard-agent did not start runtime")
		}
		cancel()
		select {
		case err := <-done:
			if err != nil || runtime.bootstrapCalls != 1 || database.closeCalls != 1 {
				t.Fatalf("runGuardAgent() = %v, bootstrap calls = %d, close calls = %d", err, runtime.bootstrapCalls, database.closeCalls)
			}
		case <-time.After(time.Second):
			t.Fatal("guard-agent did not stop runtime")
		}
	})
}

func openTestAgentStore(database agentStore) storeOpener {
	return func(context.Context, string, fs.FS) (agentStore, error) { return database, nil }
}

type testAgentStore struct {
	nodeID     core.NodeID
	closeCalls int
}

func (s *testAgentStore) LoadNodeIdentity(context.Context) (core.NodeID, bool, error) {
	return s.nodeID, s.nodeID != "", nil
}

func (s *testAgentStore) CreateNodeIdentity(_ context.Context, nodeID core.NodeID, _ time.Time) (core.NodeID, error) {
	s.nodeID = nodeID
	return nodeID, nil
}

func (s *testAgentStore) Close() error {
	s.closeCalls++
	return nil
}

type testGuardAgentRuntime struct {
	bootstrap      func(context.Context, time.Time) (decision.PolicyChange, error)
	run            func(context.Context) error
	bootstrapCalls int
	runCalls       int
}

func (r *testGuardAgentRuntime) BootstrapInitialManagedPolicy(ctx context.Context, now time.Time) (decision.PolicyChange, error) {
	r.bootstrapCalls++
	if r.bootstrap != nil {
		return r.bootstrap(ctx, now)
	}
	return decision.PolicyChange{Changed: true, PolicyRevision: 1, SnapshotRevision: 1}, nil
}

func (r *testGuardAgentRuntime) Run(ctx context.Context) error {
	r.runCalls++
	if r.run != nil {
		return r.run(ctx)
	}
	return nil
}

func testGuardAgentRuntimeFactory(runtime guardAgentRuntime) guardAgentRuntimeFactory {
	return func(context.Context, core.NodeID, agentStore, config.Config) (guardAgentRuntime, error) {
		return runtime, nil
	}
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

	runtimeStarted := make(chan struct{})
	done := make(chan error, 1)
	var usedDatabase agentStore
	var runtimeNodeID core.NodeID
	go func() {
		done <- runGuardAgent(
			ctx,
			func(string) (*user.User, error) { return &user.User{Uid: "1001", Gid: "1002"}, nil },
			func() int { return 1001 },
			configPath,
			loadGuardAgentConfig,
			acquireAgentInstanceLock,
			openGuardAgentStore,
			migrations.FS,
			func(_ context.Context, nodeID core.NodeID, database agentStore, _ config.Config) (guardAgentRuntime, error) {
				usedDatabase, runtimeNodeID = database, nodeID
				return &testGuardAgentRuntime{run: func(ctx context.Context) error {
					close(runtimeStarted)
					<-ctx.Done()
					// 真实数据库在runtime返回前仍可使用，Agent随后负责关闭。
					got, found, err := database.LoadNodeIdentity(context.Background())
					if err == nil && (!found || got != nodeID) {
						t.Errorf("runtime read NodeID = %q, found=%t, want %q", got, found, nodeID)
					}
					return err
				}}, nil
			},
		)
	}()
	select {
	case <-runtimeStarted:
	case <-time.After(time.Second):
		t.Fatal("guard-agent did not start reconcile runtime")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runGuardAgent() = %v, want nil after runtime shutdown", err)
		}
	case <-time.After(time.Second):
		t.Fatal("guard-agent did not stop after cancellation")
	}

	if _, _, err := usedDatabase.LoadNodeIdentity(context.Background()); err == nil {
		t.Fatal("Agent returned with its SQLite Store still open")
	}
	database, err := store.Open(context.Background(), databasePath, migrations.FS)
	if err != nil {
		t.Fatalf("reopen configured store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	nodeID, found, err := database.LoadNodeIdentity(context.Background())
	if err != nil || !found || nodeID == "" || nodeID != runtimeNodeID {
		t.Fatalf("LoadNodeIdentity() = (%q, %t, %v), want persisted NodeID", nodeID, found, err)
	}
}

func TestNewGuardAgentRuntimeBootstrapsSQLiteOwnedPolicyBeforeRun(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "guard.db"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	nodeID, err := core.BootstrapPersistentNodeID(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newGuardAgentRuntime(ctx, nodeID, database, config.Config{
		Runtime: config.Runtime{ReconcileQueueCapacity: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	change, err := runtime.BootstrapInitialManagedPolicy(ctx, time.Unix(1_700_000_000, 0).UTC())
	if err != nil || !change.Changed || change.PolicyRevision != 1 || change.SnapshotRevision != 1 {
		t.Fatalf("BootstrapInitialManagedPolicy() = %+v, %v", change, err)
	}
	state, err := database.LoadDesiredFirewallState(ctx, nodeID)
	if err != nil || len(state.Policy.Allowlist) != 0 || len(state.Policy.ProtectedTargets) != 2 {
		t.Fatalf("LoadDesiredFirewallState() = %+v, %v", state, err)
	}
}
