package source

import (
	"context"
	"errors"
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
	session := beginSQLiteSourceSession(t, database, "source-1")
	state := NewSQLiteStateStore(database, session)
	if err := state.InitializeFileGenerationCoverage(context.Background(), "00112233445566778899aabbccddeeff"); err != nil {
		t.Fatal(err)
	}
	state.clock = func() time.Time { return base.Add(time.Second) }
	tracker, err := NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewCheckpointManager(tracker, state)
	completion := durableCompletion(t, 1, 0, 20)
	if err := manager.Complete(context.Background(), completion); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := state.LoadCheckpoint(context.Background(), "source-1")
	if err != nil || !found {
		t.Fatalf("LoadCheckpoint() = %+v,%v,%v", checkpoint, found, err)
	}
	if checkpoint.SessionID != session.ID() || checkpoint.DeliverySequence != 1 || checkpoint.Position != completion.Position {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
}

func TestSQLiteLegacyCheckpointPreservesNonzeroStartWithoutCoverage(t *testing.T) {
	ctx := context.Background()
	database := openSQLiteSessionFixture(t, filepath.Join(t.TempDir(), "guard.db"))
	session := beginSQLiteSourceSession(t, database, "source-1")
	completion := durableCompletion(t, 1, 10, 20)
	want := store.SourceCheckpoint{
		SourceID: "source-1", SessionID: session.ID(), DeliverySequence: 1,
		Position: completion.Position, PersistedAt: time.Unix(1_700_000_001, 0).UTC(),
	}
	if err := database.AdvanceSourceCheckpoint(ctx, session, want.DeliverySequence, want.Position, want.PersistedAt); err != nil {
		t.Fatal(err)
	}
	got, found, err := database.LoadSourceCheckpoint(ctx, "source-1")
	if err != nil || !found || got != want {
		t.Fatalf("legacy checkpoint=%+v found=%v err=%v want=%+v", got, found, err, want)
	}
	generations, err := database.LoadRecoverableFileGenerations(ctx, "source-1")
	if err != nil || len(generations) != 1 || generations[0].DurableEndOffset != nil {
		t.Fatalf("legacy checkpoint created coverage: %+v err=%v", generations, err)
	}
}

func sourceMigrationFS() fs.FS {
	return os.DirFS(filepath.Join("..", "..", "migrations"))
}

func TestSQLiteCheckpointManagerJournaldRetainsOpaqueCursor(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "guard.db"), sourceMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	base := time.Unix(1_700_000_000, 0).UTC()
	node := core.NodeID("00112233445566778899aabbccddeeff")
	if err := database.EnsureNodeIdentity(ctx, node, base); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(ctx, "journal", node, store.SourceKindJournald, base); err != nil {
		t.Fatal(err)
	}
	session := beginSQLiteSourceSession(t, database, "journal")
	state := NewSQLiteStateStore(database, session)
	tracker, err := NewCompletionTracker("journal", 1)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewCheckpointManager(tracker, state)
	var last core.SourcePosition
	for i, cursor := range []string{"opaque:z/2", "opaque:a/1"} {
		position, err := core.NewJournaldPosition(cursor)
		if err != nil {
			t.Fatal(err)
		}
		id, err := core.JournaldDeliveryID("journal", cursor)
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Complete(ctx, core.DurableCompletion{SourceID: "journal", DeliveryID: id, Sequence: core.DeliverySequence(i + 1), Position: position}); err != nil {
			t.Fatal(err)
		}
		last = position
	}
	active, cp, found, generations, err := database.LoadSourceCoverageState(ctx, "journal")
	if err != nil || !found || active != session.ID() || cp.SessionID != session.ID() || cp.DeliverySequence != 2 || cp.Position != last || len(generations) != 0 {
		t.Fatalf("journal state=%s/%+v/%v/%+v/%v", active, cp, found, generations, err)
	}
}

func TestSQLiteCoverageConfirmationRequiresWholeCurrentSnapshot(t *testing.T) {
	ctx := context.Background()
	database := openSQLiteSessionFixture(t, filepath.Join(t.TempDir(), "guard.db"))
	session := beginSQLiteSourceSession(t, database, "source-1")
	state := NewSQLiteStateStore(database, session)
	completion := durableCompletion(t, 1, 0, 10)
	span, _ := completion.Position.File()
	if err := state.InitializeFileGenerationCoverage(ctx, span.Generation); err != nil {
		t.Fatal(err)
	}
	if confirmed, err := state.checkpointConfirmed(ctx, 1, completion.Position, []core.FilePosition{span}); err != nil || confirmed {
		t.Fatalf("uncommitted=%v/%v", confirmed, err)
	}
	// 使用真实提交后的快照模拟确认丢失，不注入或宣称真实driver Commit故障。
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 1, completion.Position, []core.FilePosition{span}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if confirmed, err := state.checkpointConfirmed(ctx, 1, completion.Position, []core.FilePosition{span}); err != nil || !confirmed {
		t.Fatalf("committed=%v/%v", confirmed, err)
	}
	missing := span
	missing.Generation = "11112233445566778899aabbccddeeff"
	if confirmed, err := state.checkpointConfirmed(ctx, 1, completion.Position, []core.FilePosition{span, missing}); err != nil || confirmed {
		t.Fatalf("missing generation=%v/%v", confirmed, err)
	}
	beyond := span
	beyond.EndOffset++
	if confirmed, err := state.checkpointConfirmed(ctx, 1, completion.Position, []core.FilePosition{beyond}); err != nil || confirmed {
		t.Fatalf("missing suffix=%v/%v", confirmed, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if confirmed, err := state.checkpointConfirmed(cancelled, 1, completion.Position, []core.FilePosition{span}); confirmed || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read=%v/%v", confirmed, err)
	}
	beginSQLiteSourceSession(t, database, "source-1")
	if confirmed, err := state.checkpointConfirmed(ctx, 1, completion.Position, []core.FilePosition{span}); err != nil || confirmed {
		t.Fatalf("stale owner=%v/%v", confirmed, err)
	}
}

// 测试调用者保证旧 worker 已结束；每次调用代表一次新的 Source 启动。
func beginSQLiteSourceSession(t *testing.T, database *store.Store, sourceID core.SourceID) store.SourceSession {
	t.Helper()
	ctx := context.Background()
	expected, _, _, err := database.LoadSourceSessionState(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.NewSourceSessionID()
	if err != nil {
		t.Fatal(err)
	}
	session, _, _, err := database.BeginSourceSession(ctx, sourceID, expected, id)
	if err != nil {
		t.Fatal(err)
	}
	return session
}
