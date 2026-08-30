package source

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestSQLiteCheckpointManagerPersistsAndLoadsCandidate(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "guard.db"), sourceMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	base := time.Unix(1_700_000_000, 0).UTC()
	nodeID := core.NodeID("00112233445566778899aabbccddeeff")
	if err := database.EnsureNodeIdentity(context.Background(), nodeID, base); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(context.Background(), "source-1", nodeID, store.SourceKindFile, base); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterFileGeneration(context.Background(), store.FileGeneration{
		SourceID: "source-1", Generation: "00112233445566778899aabbccddeeff",
		DeviceID: 1, Inode: 2, Path: "/var/log/guard.log", ObservedSize: 20, OpenedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	state := NewSQLiteStateStore(database)
	state.clock = func() time.Time { return base.Add(time.Second) }
	tracker, err := NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewCheckpointManager(tracker, state)
	completion := durableCompletion(t, 1, 10, 20)
	if err := manager.Complete(context.Background(), completion); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := state.LoadCheckpoint(context.Background(), "source-1")
	if err != nil || !found {
		t.Fatalf("LoadCheckpoint() = %+v,%v,%v", checkpoint, found, err)
	}
	if checkpoint.DeliverySequence != 1 || checkpoint.Position != completion.Position {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
}

func sourceMigrationFS() fs.FS {
	return os.DirFS(filepath.Join("..", "..", "migrations"))
}
