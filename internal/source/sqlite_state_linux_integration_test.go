//go:build linux && integration

package source

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

const (
	sourceRestartModeEnv       = "GUARD_SOURCE_RESTART_MODE"
	sourceRestartDatabaseEnv   = "GUARD_SOURCE_RESTART_DATABASE"
	sourceRestartMigrationsEnv = "GUARD_SOURCE_RESTART_MIGRATIONS"
	sourceRestartResultEnv     = "GUARD_SOURCE_RESTART_RESULT"

	sourceRestartSourceID      core.SourceID = "source-linux-restart"
	sourceRestartNodeID        core.NodeID   = "0123456789abcdef0123456789abcdef"
	sourceRestartOldGeneration               = "00112233445566778899aabbccddeeff"
	sourceRestartNewGeneration               = "ffeeddccbbaa99887766554433221100"
)

func TestSQLiteSourceGenerationRestartReplay(t *testing.T) {
	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("resolve migration directory: %v", err)
	}
	databasePath := filepath.Join(t.TempDir(), "guard.db")

	written := runSourceRestartHelper(t, "write", databasePath, migrationDir)
	recovered := runSourceRestartHelper(t, "read", databasePath, migrationDir)

	if !reflect.DeepEqual(recovered, written) {
		t.Fatalf("restart snapshot changed:\nwritten:   %+v\nrecovered: %+v", written, recovered)
	}
	if written.OldDeliveryID == written.NewDeliveryID {
		t.Fatal("rotated generations produced the same DeliveryID for equal offsets")
	}
	if written.OldEventID == written.NewEventID {
		t.Fatal("rotated generations produced the same EventID for equal parser inputs")
	}
}

func TestSQLiteSourceGenerationRestartReplayHelper(t *testing.T) {
	mode := os.Getenv(sourceRestartModeEnv)
	if mode == "" {
		t.Skip("restart helper runs only as a child process")
	}
	databasePath := os.Getenv(sourceRestartDatabaseEnv)
	migrationDir := os.Getenv(sourceRestartMigrationsEnv)
	resultPath := os.Getenv(sourceRestartResultEnv)
	if databasePath == "" || migrationDir == "" || resultPath == "" {
		t.Fatal("restart helper environment is incomplete")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database, err := store.Open(ctx, databasePath, os.DirFS(migrationDir))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	}()

	switch mode {
	case "write":
		seedSourceRestartState(t, ctx, database)
	case "read":
		// A fresh process reaches this branch after the writer closed the database.
	default:
		t.Fatalf("unknown restart helper mode %q", mode)
	}

	snapshot := readSourceRestartSnapshot(t, ctx, database)
	contents, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal restart snapshot: %v", err)
	}
	if err := os.WriteFile(resultPath, contents, 0o600); err != nil {
		t.Fatalf("write restart snapshot: %v", err)
	}
}

type sourceRestartSnapshot struct {
	OldDeliveryID        string                     `json:"old_delivery_id"`
	NewDeliveryID        string                     `json:"new_delivery_id"`
	OldEventID           string                     `json:"old_event_id"`
	NewEventID           string                     `json:"new_event_id"`
	Generations          []sourceGenerationSnapshot `json:"generations"`
	CheckpointSequence   uint64                     `json:"checkpoint_sequence"`
	CheckpointGeneration string                     `json:"checkpoint_generation"`
	CheckpointEndOffset  uint64                     `json:"checkpoint_end_offset"`
	OldReceiptFound      bool                       `json:"old_receipt_found"`
	NewReceiptFound      bool                       `json:"new_receipt_found"`
}

type sourceGenerationSnapshot struct {
	Generation   string `json:"generation"`
	DeviceID     uint64 `json:"device_id"`
	Inode        uint64 `json:"inode"`
	Path         string `json:"path"`
	State        string `json:"state"`
	ObservedSize uint64 `json:"observed_size"`
}

func seedSourceRestartState(t *testing.T, ctx context.Context, database *store.Store) {
	t.Helper()
	base := time.Unix(1_700_000_000, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, sourceRestartNodeID, base); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	if err := database.EnsureSource(ctx, sourceRestartSourceID, sourceRestartNodeID, store.SourceKindFile, base); err != nil {
		t.Fatalf("EnsureSource(): %v", err)
	}

	state := NewSQLiteStateStore(database)
	state.clock = func() time.Time { return base.Add(2 * time.Second) }
	if err := state.RegisterFileGeneration(ctx, store.FileGeneration{
		SourceID: sourceRestartSourceID, Generation: sourceRestartOldGeneration,
		DeviceID: 11, Inode: 101, Path: "/var/log/guard.log",
		ObservedSize: 10, OpenedAt: base,
	}); err != nil {
		t.Fatalf("RegisterFileGeneration(): %v", err)
	}

	oldPosition := sourceRestartPosition(t, sourceRestartOldGeneration, 11, 101)
	oldDeliveryID := sourceRestartDeliveryID(t, oldPosition)
	putSourceRestartReceipt(t, ctx, database, oldDeliveryID, oldPosition, base.Add(time.Second))

	if err := state.RotateFileGeneration(ctx, sourceRestartSourceID, sourceRestartOldGeneration, store.FileGeneration{
		SourceID: sourceRestartSourceID, Generation: sourceRestartNewGeneration,
		DeviceID: 12, Inode: 202, Path: "/var/log/guard.log", ObservedSize: 10,
	}); err != nil {
		t.Fatalf("RotateFileGeneration(): %v", err)
	}

	newPosition := sourceRestartPosition(t, sourceRestartNewGeneration, 12, 202)
	newDeliveryID := sourceRestartDeliveryID(t, newPosition)
	putSourceRestartReceipt(t, ctx, database, newDeliveryID, newPosition, base.Add(3*time.Second))

	tracker, err := NewCompletionTracker(sourceRestartSourceID, 1)
	if err != nil {
		t.Fatalf("NewCompletionTracker(): %v", err)
	}
	checkpoints := NewCheckpointManager(tracker, state)
	for sequence, item := range []struct {
		id       core.DeliveryID
		position core.SourcePosition
	}{
		{id: oldDeliveryID, position: oldPosition},
		{id: newDeliveryID, position: newPosition},
	} {
		if err := checkpoints.Complete(ctx, core.DurableCompletion{
			SourceID: sourceRestartSourceID, DeliveryID: item.id,
			Sequence: core.DeliverySequence(sequence + 1), Position: item.position,
		}); err != nil {
			t.Fatalf("Complete(sequence=%d): %v", sequence+1, err)
		}
	}
}

func readSourceRestartSnapshot(t *testing.T, ctx context.Context, database *store.Store) sourceRestartSnapshot {
	t.Helper()
	state := NewSQLiteStateStore(database)
	generations, err := state.RecoverFileGenerations(ctx, sourceRestartSourceID)
	if err != nil {
		t.Fatalf("RecoverFileGenerations(): %v", err)
	}
	if len(generations) != 2 {
		t.Fatalf("recoverable generations = %d, want 2", len(generations))
	}

	oldPosition := sourceRestartPosition(t, sourceRestartOldGeneration, 11, 101)
	newPosition := sourceRestartPosition(t, sourceRestartNewGeneration, 12, 202)
	oldDeliveryID := sourceRestartDeliveryID(t, oldPosition)
	newDeliveryID := sourceRestartDeliveryID(t, newPosition)
	oldEventID := sourceRestartEventID(t, oldDeliveryID)
	newEventID := sourceRestartEventID(t, newDeliveryID)

	oldReceipt, oldFound, err := database.FindProcessingReceipt(ctx, oldDeliveryID)
	if err != nil {
		t.Fatalf("FindProcessingReceipt(old): %v", err)
	}
	newReceipt, newFound, err := database.FindProcessingReceipt(ctx, newDeliveryID)
	if err != nil {
		t.Fatalf("FindProcessingReceipt(new): %v", err)
	}
	if !oldFound || oldReceipt.Position != oldPosition {
		t.Fatalf("old receipt = %+v, found=%v", oldReceipt, oldFound)
	}
	if !newFound || newReceipt.Position != newPosition {
		t.Fatalf("new receipt = %+v, found=%v", newReceipt, newFound)
	}

	checkpoint, found, err := state.LoadCheckpoint(ctx, sourceRestartSourceID)
	if err != nil || !found {
		t.Fatalf("LoadCheckpoint() = %+v, found=%v err=%v", checkpoint, found, err)
	}
	checkpointFile, ok := checkpoint.Position.File()
	if !ok {
		t.Fatalf("checkpoint position = %+v, want file", checkpoint.Position)
	}

	snapshot := sourceRestartSnapshot{
		OldDeliveryID: string(oldDeliveryID), NewDeliveryID: string(newDeliveryID),
		OldEventID: string(oldEventID), NewEventID: string(newEventID),
		CheckpointSequence:   uint64(checkpoint.DeliverySequence),
		CheckpointGeneration: checkpointFile.Generation,
		CheckpointEndOffset:  checkpointFile.EndOffset,
		OldReceiptFound:      oldFound, NewReceiptFound: newFound,
	}
	for _, generation := range generations {
		snapshot.Generations = append(snapshot.Generations, sourceGenerationSnapshot{
			Generation: generation.Generation, DeviceID: generation.DeviceID,
			Inode: generation.Inode, Path: generation.Path,
			State: string(generation.State), ObservedSize: generation.ObservedSize,
		})
	}
	if snapshot.Generations[0].Generation != sourceRestartOldGeneration ||
		snapshot.Generations[0].State != string(store.FileGenerationDraining) ||
		snapshot.Generations[1].Generation != sourceRestartNewGeneration ||
		snapshot.Generations[1].State != string(store.FileGenerationOpen) ||
		snapshot.CheckpointSequence != 2 ||
		snapshot.CheckpointGeneration != sourceRestartNewGeneration ||
		snapshot.CheckpointEndOffset != 10 {
		t.Fatalf("restart state = %+v", snapshot)
	}
	return snapshot
}

func putSourceRestartReceipt(
	t *testing.T,
	ctx context.Context,
	database *store.Store,
	deliveryID core.DeliveryID,
	position core.SourcePosition,
	committed time.Time,
) {
	t.Helper()
	unit, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	if err := unit.PutReceipt(ctx, core.ProcessingReceipt{
		DeliveryID: deliveryID, SourceID: sourceRestartSourceID,
		Position: position, Kind: core.ReceiptSuccess, Committed: committed,
	}); err != nil {
		t.Fatalf("PutReceipt(): %v", err)
	}
	if err := unit.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
}

func sourceRestartPosition(t *testing.T, generation string, deviceID uint64, inode uint64) core.SourcePosition {
	t.Helper()
	position, err := core.NewFilePosition(core.FilePosition{
		Generation: generation, DeviceID: deviceID, Inode: inode,
		StartOffset: 0, EndOffset: 10,
	})
	if err != nil {
		t.Fatalf("NewFilePosition(): %v", err)
	}
	return position
}

func sourceRestartDeliveryID(t *testing.T, position core.SourcePosition) core.DeliveryID {
	t.Helper()
	file, ok := position.File()
	if !ok {
		t.Fatal("source position is not a file position")
	}
	deliveryID, err := core.FileDeliveryID(sourceRestartSourceID, file)
	if err != nil {
		t.Fatalf("FileDeliveryID(): %v", err)
	}
	return deliveryID
}

func sourceRestartEventID(t *testing.T, deliveryID core.DeliveryID) core.EventID {
	t.Helper()
	eventID, err := core.SecurityEventID(sourceRestartNodeID, deliveryID, "parser-1", "v1", 0)
	if err != nil {
		t.Fatalf("SecurityEventID(): %v", err)
	}
	return eventID
}

func runSourceRestartHelper(
	t *testing.T,
	mode string,
	databasePath string,
	migrationDir string,
) sourceRestartSnapshot {
	t.Helper()
	resultPath := filepath.Join(t.TempDir(), mode+".json")
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestSQLiteSourceGenerationRestartReplayHelper$",
		"-test.count=1",
	)
	command.Env = append(
		os.Environ(),
		sourceRestartModeEnv+"="+mode,
		sourceRestartDatabaseEnv+"="+databasePath,
		sourceRestartMigrationsEnv+"="+migrationDir,
		sourceRestartResultEnv+"="+resultPath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("restart helper %q: %v; output: %s", mode, err, strings.TrimSpace(string(output)))
	}
	contents, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read restart helper %q result: %v", mode, err)
	}
	var snapshot sourceRestartSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatalf("decode restart helper %q result: %v", mode, err)
	}
	return snapshot
}
