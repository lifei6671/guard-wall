package source

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestSQLiteCheckpointCrossGenerationCompletionWaitsForOldHole(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "guard.db")
	database, err := store.Open(ctx, path, sourceMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if database != nil {
			if err := database.Close(); err != nil {
				t.Errorf("Close(): %v", err)
			}
		}
	})
	base := time.Unix(1_700_000_000, 0).UTC()
	const sourceID core.SourceID = "source-1"
	const oldGeneration = "00112233445566778899aabbccddeeff"
	const newGeneration = "ffeeddccbbaa99887766554433221100"
	const nodeID core.NodeID = "00112233445566778899aabbccddeeff"
	if err := database.EnsureNodeIdentity(ctx, nodeID, base); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(ctx, sourceID, nodeID, store.SourceKindFile, base); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterFileGeneration(ctx, store.FileGeneration{
		SourceID: sourceID, Generation: oldGeneration, DeviceID: 1, Inode: 2,
		Path: "/var/log/guard.log", ObservedSize: 200, OpenedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	state := NewSQLiteStateStore(database, beginSQLiteSourceSession(t, database, sourceID))
	if err := state.InitializeFileGenerationCoverage(ctx, oldGeneration); err != nil {
		t.Fatal(err)
	}
	state.clock = func() time.Time { return base.Add(time.Second) }
	tracker, err := NewCompletionTracker(sourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewCheckpointManager(tracker, state)
	positions := []core.FilePosition{
		{Generation: oldGeneration, DeviceID: 1, Inode: 2, StartOffset: 0, EndOffset: 100},
		{Generation: oldGeneration, DeviceID: 1, Inode: 2, StartOffset: 100, EndOffset: 200},
		{Generation: newGeneration, DeviceID: 1, Inode: 3, StartOffset: 0, EndOffset: 10},
	}
	completions := make([]core.DurableCompletion, len(positions))
	for i, file := range positions {
		position, err := core.NewFilePosition(file)
		if err != nil {
			t.Fatal(err)
		}
		id, err := core.FileDeliveryID(sourceID, file)
		if err != nil {
			t.Fatal(err)
		}
		completions[i] = core.DurableCompletion{
			SourceID: sourceID, DeliveryID: id, Sequence: core.DeliverySequence(i + 1), Position: position,
		}
	}
	commitAndComplete := func(completion core.DurableCompletion) {
		t.Helper()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := uow.PutReceipt(ctx, core.ProcessingReceipt{
			SourceID: sourceID, DeliveryID: completion.DeliveryID, Position: completion.Position,
			Kind: core.ReceiptSuccess, Committed: base.Add(time.Second),
		}); err != nil {
			if rollbackErr := uow.Rollback(); rollbackErr != nil {
				t.Errorf("Rollback(): %v", rollbackErr)
			}
			t.Fatal(err)
		}
		if err := uow.Commit(); err != nil {
			t.Fatal(err)
		}
		receipt, found, err := database.FindProcessingReceipt(ctx, completion.DeliveryID)
		if err != nil || !found || receipt.SourceID != sourceID || receipt.Position != completion.Position {
			t.Fatalf("receipt readback = %+v, %v, %v", receipt, found, err)
		}
		completion.Position = receipt.Position
		if err := manager.Complete(ctx, completion); err != nil {
			t.Fatal(err)
		}
	}
	assertCheckpoint := func(want core.DurableCompletion) {
		t.Helper()
		checkpoint, found, err := database.LoadSourceCheckpoint(ctx, sourceID)
		if err != nil || !found {
			t.Fatalf("LoadCheckpoint() = %+v, %v, %v", checkpoint, found, err)
		}
		if checkpoint.DeliverySequence != want.Sequence || checkpoint.Position != want.Position {
			t.Fatalf("checkpoint = %+v; want sequence %d position %+v", checkpoint, want.Sequence, want.Position)
		}
	}
	assertCoverage := func(oldEnd, newEnd uint64) {
		t.Helper()
		generations, err := database.LoadRecoverableFileGenerations(ctx, sourceID)
		if err != nil || len(generations) != 2 {
			t.Fatalf("recover generations=%+v err=%v", generations, err)
		}
		want := map[string]uint64{oldGeneration: oldEnd, newGeneration: newEnd}
		for _, generation := range generations {
			end, ok := want[generation.Generation]
			if !ok || generation.DurableEndOffset == nil || *generation.DurableEndOffset != end || generation.CoverageSessionID == nil {
				t.Fatalf("generation coverage=%+v want ends=%v", generation, want)
			}
		}
	}
	commitAndComplete(completions[0])
	assertCheckpoint(completions[0])
	if err := state.RotateFileGeneration(ctx, sourceID, oldGeneration, store.FileGeneration{
		SourceID: sourceID, Generation: newGeneration, DeviceID: 1, Inode: 3,
		Path: "/var/log/guard.log", ObservedSize: 10,
	}); err != nil {
		t.Fatal(err)
	}
	// 新 generation 先提交；旧 generation 的序号 2 尚未完成，不能越过空缺。
	if err := state.InitializeFileGenerationCoverage(ctx, newGeneration); err != nil {
		t.Fatal(err)
	}
	commitAndComplete(completions[2])
	assertCheckpoint(completions[0])
	assertCoverage(100, 0)
	if err := manager.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	assertCheckpoint(completions[0])
	assertCoverage(100, 0)
	commitAndComplete(completions[1])
	// 跨 generation 后偏移从 200 回到 10，连续投递序号才是推进依据。
	assertCheckpoint(completions[2])
	assertCoverage(200, 10)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = nil
	database, err = store.Open(ctx, path, sourceMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	assertCheckpoint(completions[2])
	assertCoverage(200, 10)
}
