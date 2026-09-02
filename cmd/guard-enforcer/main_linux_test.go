//go:build linux

package main

import (
	"context"
	"errors"
	"os/user"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestLookupGuardIdentity(t *testing.T) {
	tests := []struct {
		name    string
		lookup  func(string) (*user.User, error)
		want    guardIdentity
		wantErr error
	}{
		{name: "valid", lookup: func(string) (*user.User, error) { return &user.User{Uid: "1001", Gid: "1002"}, nil }, want: guardIdentity{uid: 1001, gid: 1002}},
		{name: "missing", lookup: func(string) (*user.User, error) { return nil, errors.New("missing") }, wantErr: errGuardIdentityUnavailable},
		{name: "negative uid", lookup: func(string) (*user.User, error) { return &user.User{Uid: "-1", Gid: "1002"}, nil }, wantErr: errGuardIdentityUnavailable},
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

func TestRunGuardEnforcer(t *testing.T) {
	lookup := func(string) (*user.User, error) { return &user.User{Uid: "1001", Gid: "1002"}, nil }

	t.Run("non root has no backend side effect", func(t *testing.T) {
		called := false
		err := runGuardEnforcer(context.Background(), lookup, func() int { return 1001 }, func() enforcer.MutationBackend {
			called = true
			return fakeBackend{}
		}, nil, nil, nil)
		if !errors.Is(err, errEnforcerIdentity) || called {
			t.Fatalf("runGuardEnforcer() = %v, backend called = %v", err, called)
		}
	})

	t.Run("runtime receives fixed identity and budget", func(t *testing.T) {
		var received struct {
			uid     uint32
			timeout time.Duration
			observe bool
		}
		runner := &testRunner{}
		err := runGuardEnforcer(
			context.Background(), lookup, func() int { return 0 }, func() enforcer.MutationBackend { return fakeBackend{} },
			func(uint32) (*ipc.UnixListener, error) { return nil, nil },
			func(_ enforcer.MutationBackend, _ *ipc.UnixListener, uid uint32, options ipc.EnforcerServeOptions) (enforcerRunner, error) {
				received.uid = uid
				received.timeout = options.RequestTimeout
				received.observe = options.OnRequestFailure != nil
				return runner, nil
			},
			func(*ipc.UnixListener) error { return nil },
		)
		if err != nil || !runner.called || received.uid != 1001 || received.timeout != enforcerRequestTimeout || !received.observe {
			t.Fatalf("runGuardEnforcer() err=%v run=%v identity=%d timeout=%s observer=%v", err, runner.called, received.uid, received.timeout, received.observe)
		}
	})

	t.Run("constructor failure closes listener", func(t *testing.T) {
		closed := false
		constructorErr := errors.New("constructor failed")
		err := runGuardEnforcer(
			context.Background(), lookup, func() int { return 0 }, func() enforcer.MutationBackend { return fakeBackend{} },
			func(uint32) (*ipc.UnixListener, error) { return nil, nil },
			func(enforcer.MutationBackend, *ipc.UnixListener, uint32, ipc.EnforcerServeOptions) (enforcerRunner, error) {
				return nil, constructorErr
			},
			func(*ipc.UnixListener) error { closed = true; return nil },
		)
		if !errors.Is(err, constructorErr) || !closed {
			t.Fatalf("runGuardEnforcer() = %v, listener closed = %v", err, closed)
		}
	})
}

type testRunner struct{ called bool }

func (r *testRunner) Run(context.Context) error { r.called = true; return nil }

type fakeBackend struct{}

func (fakeBackend) Probe(context.Context) (firewall.FirewallCapabilities, error) {
	return firewall.FirewallCapabilities{}, nil
}
func (fakeBackend) Snapshot(context.Context) (firewall.ManagedSnapshot, error) {
	return firewall.ManagedSnapshot{}, nil
}
func (fakeBackend) Apply(context.Context, firewall.OperationPlan) firewall.MutationResult {
	return firewall.MutationResult{}
}
func (fakeBackend) RemoveManagedInfrastructure(context.Context, firewall.RemovalAuthorization) firewall.MutationResult {
	return firewall.MutationResult{}
}
