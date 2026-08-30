package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

const (
	testSourceID    core.SourceID = "source-sqlite"
	testGeneration                = "00112233445566778899aabbccddeeff"
	testGeneration2               = "ffeeddccbbaa99887766554433221100"
)

func TestSourceCheckpointMonotonicCASRejectsRegression(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	base := prepareFileSource(t, database, testSourceID, testGeneration, 100)

	position100 := filePosition(t, testGeneration, 90, 100)
	if err := database.AdvanceSourceCheckpoint(ctx, testSourceID, 100, position100, base.Add(time.Second)); err != nil {
		t.Fatalf("AdvanceSourceCheckpoint(100): %v", err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, testSourceID, 90, filePosition(t, testGeneration, 80, 90), base.Add(2*time.Second)); !errors.Is(err, ErrCheckpointRegression) {
		t.Fatalf("AdvanceSourceCheckpoint(90) error = %v, want regression", err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, testSourceID, 100, position100, base.Add(3*time.Second)); err != nil {
		t.Fatalf("idempotent checkpoint: %v", err)
	}
	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, testSourceID)
	if err != nil || !found {
		t.Fatalf("LoadSourceCheckpoint() = %+v,%v,%v", checkpoint, found, err)
	}
	if checkpoint.DeliverySequence != 100 || checkpoint.Position != position100 {
		t.Fatalf("checkpoint regressed or changed: %+v", checkpoint)
	}
}

func TestFileGenerationLifecycleRejectsRollbackAndRestoresNonRetired(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	base := prepareFileSource(t, database, testSourceID, testGeneration, 100)

	if err := database.AdvanceFileGeneration(ctx, testSourceID, testGeneration, FileGenerationOpen, FileGenerationRetired, base.Add(time.Second)); !errors.Is(err, ErrFileGenerationTransition) {
		t.Fatalf("Open -> Retired error = %v", err)
	}
	if err := database.AdvanceFileGeneration(ctx, testSourceID, testGeneration, FileGenerationOpen, FileGenerationDraining, base.Add(-time.Second)); !errors.Is(err, ErrFileGenerationTransition) {
		t.Fatalf("regressing drain time error = %v", err)
	}
	if err := database.AdvanceFileGeneration(ctx, testSourceID, testGeneration, FileGenerationOpen, FileGenerationDraining, base.Add(time.Second)); err != nil {
		t.Fatalf("Open -> Draining: %v", err)
	}
	if err := database.AdvanceFileGeneration(ctx, testSourceID, testGeneration, FileGenerationDraining, FileGenerationOpen, base.Add(2*time.Second)); !errors.Is(err, ErrFileGenerationTransition) {
		t.Fatalf("Draining -> Open error = %v", err)
	}
	if err := database.SealFileGeneration(ctx, testSourceID, testGeneration, FileGenerationDraining, 100, 7, base); !errors.Is(err, ErrFileGenerationTransition) {
		t.Fatalf("regressing seal time error = %v", err)
	}
	if err := database.SealFileGeneration(ctx, testSourceID, testGeneration, FileGenerationDraining, 100, 7, base.Add(2*time.Second)); err != nil {
		t.Fatalf("SealFileGeneration: %v", err)
	}
	recovered, err := database.LoadRecoverableFileGenerations(ctx, testSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].State != FileGenerationSealed || recovered[0].Generation != testGeneration ||
		recovered[0].FinalEOF == nil || *recovered[0].FinalEOF != 100 ||
		recovered[0].MaxDeliverySequence == nil || *recovered[0].MaxDeliverySequence != 7 {
		t.Fatalf("recoverable generations = %+v", recovered)
	}
}

func TestRetireFileGenerationRequiresSafeCheckpointAndNoReceiptReference(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	base := prepareFileSource(t, database, testSourceID, testGeneration, 100)
	if err := database.SealFileGeneration(ctx, testSourceID, testGeneration, FileGenerationOpen, 100, 100, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, testSourceID, 99, filePosition(t, testGeneration, 0, 100), base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, base.Add(3*time.Second)); !errors.Is(err, ErrFileGenerationNotDurable) {
		t.Fatalf("retire before checkpoint error = %v", err)
	}

	position := filePosition(t, testGeneration, 90, 100)
	if err := database.AdvanceSourceCheckpoint(ctx, testSourceID, 100, position, base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, base); !errors.Is(err, ErrFileGenerationTransition) {
		t.Fatalf("regressing retire time error = %v", err)
	}
	deliveryID, err := core.FileDeliveryID(testSourceID, core.FilePosition{Generation: testGeneration, StartOffset: 90, EndOffset: 100})
	if err != nil {
		t.Fatal(err)
	}
	unit, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := unit.PutReceipt(ctx, core.ProcessingReceipt{
		DeliveryID: deliveryID, SourceID: testSourceID, Position: position,
		Kind: core.ReceiptSuccess, Committed: base.Add(4 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := unit.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, base.Add(5*time.Second)); !errors.Is(err, ErrFileGenerationReferenced) {
		t.Fatalf("retire with receipt error = %v", err)
	}
	if _, err := database.db.ExecContext(ctx, "DELETE FROM processing_receipts WHERE delivery_id = ?", string(deliveryID)); err != nil {
		t.Fatal(err)
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, base.Add(5*time.Second)); err != nil {
		t.Fatalf("safe retire: %v", err)
	}
	recovered, err := database.LoadRecoverableFileGenerations(ctx, testSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 0 {
		t.Fatalf("retired generation was restored: %+v", recovered)
	}
}

func TestAdvanceSourceCheckpointValidatesDurableIdentity(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	base := prepareFileSource(t, database, testSourceID, testGeneration, 100)

	wrongDevice, err := core.NewFilePosition(core.FilePosition{
		Generation: testGeneration, DeviceID: 9, Inode: 2, StartOffset: 0, EndOffset: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, testSourceID, 1, wrongDevice, base.Add(time.Second)); !errors.Is(err, ErrSourcePositionMismatch) {
		t.Fatalf("wrong device error = %v", err)
	}
	journald, err := core.NewJournaldPosition("cursor-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, testSourceID, 1, journald, base.Add(time.Second)); !errors.Is(err, ErrSourcePositionMismatch) {
		t.Fatalf("wrong source kind error = %v", err)
	}
}

func TestRotateFileGenerationIsAtomicAndEnforcesOneOpen(t *testing.T) {
	t.Run("successful rotate", func(t *testing.T) {
		database := openTestStore(t)
		ctx := context.Background()
		base := prepareFileSource(t, database, testSourceID, testGeneration, 100)
		next := FileGeneration{
			SourceID: testSourceID, Generation: testGeneration2, DeviceID: 3, Inode: 4,
			Path: "/var/log/guard.log.1", ObservedSize: 0, OpenedAt: base.Add(time.Second),
		}
		if err := database.RotateFileGeneration(ctx, testSourceID, testGeneration, next, next.OpenedAt); err != nil {
			t.Fatal(err)
		}
		recovered, err := database.LoadRecoverableFileGenerations(ctx, testSourceID)
		if err != nil {
			t.Fatal(err)
		}
		if len(recovered) != 2 || recovered[0].State != FileGenerationDraining || recovered[1].State != FileGenerationOpen {
			t.Fatalf("rotated generations = %+v", recovered)
		}
		if err := database.RegisterFileGeneration(ctx, FileGeneration{
			SourceID: testSourceID, Generation: "11112222333344445555666677778888",
			DeviceID: 5, Inode: 6, Path: "/var/log/another.log", OpenedAt: base.Add(2 * time.Second),
		}); err == nil {
			t.Fatal("second Open generation was accepted")
		}
	})

	t.Run("replacement insert failure rolls back drain", func(t *testing.T) {
		database := openTestStore(t)
		ctx := context.Background()
		base := prepareFileSource(t, database, testSourceID, testGeneration, 100)
		next := FileGeneration{
			SourceID: testSourceID, Generation: testGeneration, DeviceID: 3, Inode: 4,
			Path: "/var/log/guard.log.1", OpenedAt: base.Add(time.Second),
		}
		if err := database.RotateFileGeneration(ctx, testSourceID, testGeneration, next, next.OpenedAt); err == nil {
			t.Fatal("conflicting replacement unexpectedly succeeded")
		}
		recovered, err := database.LoadRecoverableFileGenerations(ctx, testSourceID)
		if err != nil {
			t.Fatal(err)
		}
		if len(recovered) != 1 || recovered[0].State != FileGenerationOpen {
			t.Fatalf("failed rotate changed old generation: %+v", recovered)
		}
	})
}

func TestRetiredGenerationRejectsLateReceiptAndCheckpoint(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	base := prepareFileSource(t, database, testSourceID, testGeneration, 100)
	position := filePosition(t, testGeneration, 90, 100)
	if err := database.SealFileGeneration(ctx, testSourceID, testGeneration, FileGenerationOpen, 100, 1, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, testSourceID, 1, position, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}

	deliveryID, err := core.FileDeliveryID(testSourceID, core.FilePosition{Generation: testGeneration, StartOffset: 90, EndOffset: 100})
	if err != nil {
		t.Fatal(err)
	}
	unit, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	putErr := unit.PutReceipt(ctx, core.ProcessingReceipt{
		DeliveryID: deliveryID, SourceID: testSourceID, Position: position,
		Kind: core.ReceiptSuccess, Committed: base.Add(4 * time.Second),
	})
	if putErr == nil {
		t.Fatal("late receipt was accepted")
	}
	if err := unit.Commit(); err == nil {
		t.Fatal("failed late receipt transaction committed")
	}
	if _, found, err := database.FindProcessingReceipt(ctx, deliveryID); err != nil || found {
		t.Fatalf("late receipt readback found=%v err=%v", found, err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, testSourceID, 2, position, base.Add(4*time.Second)); !errors.Is(err, ErrSourcePositionMismatch) {
		t.Fatalf("late checkpoint error = %v", err)
	}
	if _, err := database.db.ExecContext(ctx, `
		UPDATE source_checkpoints
		SET delivery_sequence = 2, persisted_at_us = ?
		WHERE source_id = ?`, base.Add(4*time.Second).UnixMicro(), string(testSourceID)); err == nil {
		t.Fatal("SQLite checkpoint trigger accepted retired generation")
	}
}

func prepareFileSource(t *testing.T, database *Store, sourceID core.SourceID, generation string, observedSize uint64) time.Time {
	t.Helper()
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	nodeID := core.NodeID("00112233445566778899aabbccddeeff")
	if err := database.EnsureNodeIdentity(ctx, nodeID, base); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(ctx, sourceID, nodeID, SourceKindFile, base); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterFileGeneration(ctx, FileGeneration{
		SourceID: sourceID, Generation: generation, DeviceID: 1, Inode: 2,
		Path: "/var/log/guard.log", ObservedSize: observedSize, OpenedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	return base
}

func filePosition(t *testing.T, generation string, start, end uint64) core.SourcePosition {
	t.Helper()
	position, err := core.NewFilePosition(core.FilePosition{
		Generation: generation, DeviceID: 1, Inode: 2, StartOffset: start, EndOffset: end,
	})
	if err != nil {
		t.Fatal(err)
	}
	return position
}
