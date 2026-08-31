package store

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestSQLiteLoadDesiredTargetStateReturnsOneOrderedSnapshot(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}

	expiresAt := time.Unix(4_000, 123_000).UTC()
	want := []core.NormalizedTargetEnforcementIntent{
		{
			NodeID: testNodeID, CanonicalTarget: netip.MustParsePrefix("192.0.2.1/32"),
			BanMembership: core.BanAbsent, TimeoutMode: core.TimeoutNone,
			Scopes: core.ScopeInput, AddressFamily: core.AddressFamilyIPv4,
			PolicyCoverage: core.PolicyCoverageNone, BackendAttributesDigest: strings.Repeat("a", 64),
			Generation: 2,
		},
		{
			NodeID: testNodeID, CanonicalTarget: netip.MustParsePrefix("2001:db8::1/128"),
			BanMembership: core.BanPresent, EffectiveUntil: &expiresAt, TimeoutMode: core.TimeoutNative,
			Scopes: core.ScopeInput | core.ScopeForward, AddressFamily: core.AddressFamilyIPv6,
			PolicyCoverage: core.PolicyCoverageFull, PolicyRelationDigest: strings.Repeat("b", 64),
			BackendAttributesDigest: strings.Repeat("c", 64), Generation: 3,
		},
	}
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	// Insert in reverse order so the read assertion proves canonical ordering.
	if err := uow.PutTargetEnforcementIntent(ctx, want[1]); err != nil {
		t.Fatalf("PutTargetEnforcementIntent(IPv6): %v", err)
	}
	if err := uow.PutTargetEnforcementIntent(ctx, want[0]); err != nil {
		t.Fatalf("PutTargetEnforcementIntent(IPv4): %v", err)
	}
	for expected := core.SnapshotRevision(1); expected <= 2; expected++ {
		revision, err := uow.AdvanceSnapshotRevision(ctx)
		if err != nil {
			t.Fatalf("AdvanceSnapshotRevision(%d): %v", expected, err)
		}
		if revision != expected {
			t.Fatalf("AdvanceSnapshotRevision(%d) = %d", expected, revision)
		}
	}
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}

	revision, got, err := database.LoadDesiredTargetState(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadDesiredTargetState(): %v", err)
	}
	if revision != 2 {
		t.Fatalf("revision = %d, want 2", revision)
	}
	if len(got) != len(want) {
		t.Fatalf("len(intents) = %d, want %d: %+v", len(got), len(want), got)
	}
	for index := range want {
		if !sameDesiredTargetIntent(got[index], want[index]) {
			t.Fatalf("intent[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestSQLiteLoadDesiredTargetStateReturnsEmptyNodeSnapshot(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}

	revision, intents, err := database.LoadDesiredTargetState(ctx, testNodeID)
	if err != nil {
		t.Fatalf("LoadDesiredTargetState(): %v", err)
	}
	if revision != 0 || intents == nil || len(intents) != 0 {
		t.Fatalf("empty snapshot = revision %d intents %#v", revision, intents)
	}
}

func TestSQLiteLoadDesiredTargetStateRejectsInvalidInputsAndNodeMismatch(t *testing.T) {
	validOtherNode := core.NodeID("fedcba9876543210fedcba9876543210")
	tests := []struct {
		name      string
		prepare   func(*testing.T, *Store)
		store     func(*Store) *Store
		ctx       func() context.Context
		nodeID    core.NodeID
		wantError string
		wantIs    error
	}{
		{
			name: "nil store", store: func(*Store) *Store { return nil },
			ctx: func() context.Context { return context.Background() }, nodeID: testNodeID,
			wantError: "store is closed",
		},
		{
			name: "nil context", ctx: func() context.Context { return nil }, nodeID: testNodeID,
			wantError: "context is required",
		},
		{
			name: "invalid node", ctx: func() context.Context { return context.Background() }, nodeID: "INVALID",
			wantError: "node id must be 128-bit lowercase hex",
		},
		{
			name: "missing identity", ctx: func() context.Context { return context.Background() }, nodeID: testNodeID,
			wantError: "read node identity",
		},
		{
			name: "different persisted node",
			prepare: func(t *testing.T, database *Store) {
				t.Helper()
				if err := database.EnsureNodeIdentity(context.Background(), testNodeID, time.Unix(100, 0).UTC()); err != nil {
					t.Fatalf("EnsureNodeIdentity(): %v", err)
				}
			},
			ctx: func() context.Context { return context.Background() }, nodeID: validOtherNode,
			wantError: "persisted node",
		},
		{
			name: "canceled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			nodeID: testNodeID, wantError: "begin read transaction", wantIs: context.Canceled,
		},
		{
			name: "closed store",
			prepare: func(t *testing.T, database *Store) {
				t.Helper()
				if err := database.Close(); err != nil {
					t.Fatalf("Close(): %v", err)
				}
			},
			ctx: func() context.Context { return context.Background() }, nodeID: testNodeID,
			wantError: "begin read transaction",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			if test.prepare != nil {
				test.prepare(t, database)
			}
			targetStore := database
			if test.store != nil {
				targetStore = test.store(database)
			}
			_, _, err := targetStore.LoadDesiredTargetState(test.ctx(), test.nodeID)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("LoadDesiredTargetState() error = %v, want containing %q", err, test.wantError)
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("LoadDesiredTargetState() error = %v, want errors.Is(%v)", err, test.wantIs)
			}
		})
	}
}

func sameDesiredTargetIntent(left, right core.NormalizedTargetEnforcementIntent) bool {
	if left.NodeID != right.NodeID || left.CanonicalTarget != right.CanonicalTarget ||
		left.BanMembership != right.BanMembership || left.TimeoutMode != right.TimeoutMode ||
		left.Scopes != right.Scopes || left.AddressFamily != right.AddressFamily ||
		left.PolicyCoverage != right.PolicyCoverage ||
		left.PolicyRelationDigest != right.PolicyRelationDigest ||
		left.BackendAttributesDigest != right.BackendAttributesDigest || left.Generation != right.Generation {
		return false
	}
	if left.EffectiveUntil == nil || right.EffectiveUntil == nil {
		return left.EffectiveUntil == nil && right.EffectiveUntil == nil
	}
	return left.EffectiveUntil.Equal(*right.EffectiveUntil)
}
