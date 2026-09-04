package source

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestSQLiteCheckpointSessionRestartPreservesPositionAndFencesFlush(t *testing.T) {
	ctx := context.Background()
	database := openSQLiteSessionFixture(t, filepath.Join(t.TempDir(), "guard.db"))
	sessionA := beginSQLiteSourceSession(t, database, "source-1")
	stateA := NewSQLiteStateStore(database, sessionA)
	if err := stateA.InitializeFileGenerationCoverage(ctx, "00112233445566778899aabbccddeeff"); err != nil {
		t.Fatal(err)
	}
	trackerA, err := NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	managerA := NewCheckpointManager(trackerA, stateA)
	for sequence := 1; sequence <= 100; sequence++ {
		if err := managerA.Complete(ctx, durableCompletion(t, core.DeliverySequence(sequence), uint64(sequence-1)*10, uint64(sequence)*10)); err != nil {
			t.Fatal(err)
		}
	}
	before, found, err := database.LoadSourceCheckpoint(ctx, "source-1")
	if err != nil || !found || before.DeliverySequence != 100 || before.SessionID != sessionA.ID() {
		t.Fatalf("session A checkpoint=%+v found=%v err=%v", before, found, err)
	}
	id, err := store.NewSourceSessionID()
	if err != nil {
		t.Fatal(err)
	}
	sessionB, recovery, found, err := database.BeginSourceSession(ctx, "source-1", sessionA.ID(), id)
	if err != nil || !found || recovery != before {
		t.Fatalf("Begin B recovery=%+v found=%v err=%v, want %+v", recovery, found, err, before)
	}
	assertCheckpoint := func(want store.SourceCheckpoint) {
		t.Helper()
		got, found, err := database.LoadSourceCheckpoint(ctx, "source-1")
		if err != nil || !found || got != want {
			t.Fatalf("checkpoint=%+v found=%v err=%v, want %+v", got, found, err, want)
		}
	}
	assertCheckpoint(before)
	trackerB, err := NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	managerB := NewCheckpointManager(trackerB, NewSQLiteStateStore(database, sessionB))
	first := durableCompletion(t, 1, 1000, 1010)
	second := durableCompletion(t, 2, 1010, 1020)
	oldNumbering := durableCompletion(t, 101, 1000, 1010)
	if first.DeliveryID != oldNumbering.DeliveryID || first.Position != oldNumbering.Position {
		t.Fatal("session-local renumbering changed stable Delivery identity")
	}
	nodeID := core.NodeID("00112233445566778899aabbccddeeff")
	oldEvent, err := core.SecurityEventID(nodeID, oldNumbering.DeliveryID, "parser-1", "v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	newEvent, err := core.SecurityEventID(nodeID, first.DeliveryID, "parser-1", "v1", 0)
	if err != nil || oldEvent != newEvent {
		t.Fatalf("session-local renumbering changed Event identity: %s/%s err=%v", oldEvent, newEvent, err)
	}
	if err := managerB.Complete(ctx, second); err != nil {
		t.Fatal(err)
	}
	assertCheckpoint(before)
	if err := managerB.Complete(ctx, first); err != nil {
		t.Fatal(err)
	}
	after, found, err := database.LoadSourceCheckpoint(ctx, "source-1")
	if err != nil || !found || after.SessionID != sessionB.ID() || after.DeliverySequence != 2 || after.Position != second.Position {
		t.Fatalf("session B checkpoint=%+v found=%v err=%v", after, found, err)
	}
	// 旧 manager 保留失败候选，再 Flush 也不能覆盖新 session 的完整行。
	if err := managerA.Complete(ctx, oldNumbering); !errors.Is(err, store.ErrStaleSourceSession) {
		t.Fatalf("late Complete()=%v", err)
	}
	assertCheckpoint(after)
	if err := managerA.Flush(ctx); !errors.Is(err, store.ErrStaleSourceSession) {
		t.Fatalf("late Flush()=%v", err)
	}
	assertCheckpoint(after)
	if err := NewSQLiteStateStore(database, sessionB).saveCheckpoint(ctx, "another-source", 3, second.Position); err == nil {
		t.Fatal("adapter accepted a checkpoint for a different Source")
	}
	assertCheckpoint(after)
}

func openSQLiteSessionFixture(t *testing.T, path string) *store.Store {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, path, sourceMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	base := time.Unix(1_700_000_000, 0).UTC()
	nodeID := core.NodeID("00112233445566778899aabbccddeeff")
	if err := database.EnsureNodeIdentity(ctx, nodeID, base); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(ctx, "source-1", nodeID, store.SourceKindFile, base); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterFileGeneration(ctx, store.FileGeneration{
		SourceID: "source-1", Generation: "00112233445566778899aabbccddeeff",
		DeviceID: 1, Inode: 2, Path: "/var/log/guard.log", ObservedSize: 2000, OpenedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	return database
}
