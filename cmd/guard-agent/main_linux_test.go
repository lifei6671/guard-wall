//go:build linux

package main

import (
	"context"
	"errors"
	"os/user"
	"testing"
	"time"
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

	t.Run("wrong identity does not probe", func(t *testing.T) {
		called := false
		err := runGuardAgent(context.Background(), identityLookup, func() int { return 0 }, func(context.Context) error {
			called = true
			return nil
		})
		if !errors.Is(err, errGuardAgentIdentity) || called {
			t.Fatalf("runGuardAgent() = %v, probe called = %v", err, called)
		}
	})

	t.Run("probe failure is preserved", func(t *testing.T) {
		probeErr := errors.New("probe failed")
		err := runGuardAgent(context.Background(), identityLookup, func() int { return 1001 }, func(context.Context) error {
			return probeErr
		})
		if !errors.Is(err, errGuardAgentProbe) || !errors.Is(err, probeErr) {
			t.Fatalf("runGuardAgent() = %v", err)
		}
	})

	t.Run("probe receives fixed readiness deadline", func(t *testing.T) {
		var deadline time.Time
		err := runGuardAgent(context.Background(), identityLookup, func() int { return 1001 }, func(ctx context.Context) error {
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

	t.Run("ready process waits for cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- runGuardAgent(ctx, identityLookup, func() int { return 1001 }, func(context.Context) error {
				return nil
			})
		}()
		select {
		case err := <-done:
			t.Fatalf("runGuardAgent returned before cancellation: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("runGuardAgent() = %v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("runGuardAgent did not stop after cancellation")
		}
	})
}
