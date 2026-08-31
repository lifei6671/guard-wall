//go:build linux && integration

package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

const (
	sourceCrashModeEnv       = "GUARD_SOURCE_CRASH_MODE"
	sourceCrashWindowEnv     = "GUARD_SOURCE_CRASH_WINDOW"
	sourceCrashDatabaseEnv   = "GUARD_SOURCE_CRASH_DATABASE"
	sourceCrashMigrationsEnv = "GUARD_SOURCE_CRASH_MIGRATIONS"
	sourceCrashMarkerEnv     = "GUARD_SOURCE_CRASH_MARKER"
	sourceCrashResultEnv     = "GUARD_SOURCE_CRASH_RESULT"

	sourceCrashBeforeReceipt    = "after_rotation_before_receipt"
	sourceCrashBeforeCheckpoint = "after_receipt_before_checkpoint"
	sourceCrashSourceID         = core.SourceID("source-linux-transition-crash")
	sourceCrashNodeID           = core.NodeID("abcdef0123456789abcdef0123456789")
	sourceCrashOldGeneration    = "11112222333344445555666677778888"
	sourceCrashNewGeneration    = "88887777666655554444333322221111"
)

func TestSQLiteSourceGenerationTransitionSIGKILLRecovery(t *testing.T) {
	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("resolve migration directory: %v", err)
	}

	for _, window := range []string{sourceCrashBeforeReceipt, sourceCrashBeforeCheckpoint} {
		window := window
		t.Run(window, func(t *testing.T) {
			directory := t.TempDir()
			databasePath := filepath.Join(directory, "guard.db")
			markerPath := filepath.Join(directory, "ready")

			marker := runSourceCrashWriter(t, window, databasePath, migrationDir, markerPath)
			snapshot := runSourceCrashReader(t, window, databasePath, migrationDir)

			if snapshot.Window != window {
				t.Fatalf("snapshot window = %q, want %q", snapshot.Window, window)
			}
			if !snapshot.OldReceiptFound {
				t.Fatal("old receipt was not recovered")
			}
			wantNewReceipt := window == sourceCrashBeforeCheckpoint
			if snapshot.NewReceiptFound != wantNewReceipt {
				t.Fatalf("new receipt found = %v, want %v", snapshot.NewReceiptFound, wantNewReceipt)
			}
			if snapshot.BeforeCheckpointFound ||
				snapshot.AfterCheckpointSequence != 2 ||
				snapshot.AfterCheckpointGeneration != sourceCrashNewGeneration {
				t.Fatalf("checkpoint recovery = %+v", snapshot)
			}
			if snapshot.OldDeliveryID == snapshot.NewDeliveryID {
				t.Fatal("rotated generations produced the same DeliveryID for equal offsets")
			}
			if snapshot.OldEventID == snapshot.NewEventID {
				t.Fatal("rotated generations produced the same EventID for equal parser inputs")
			}
			if snapshot.OldDeliveryID != marker.OldDeliveryID ||
				snapshot.NewDeliveryID != marker.NewDeliveryID ||
				snapshot.OldEventID != marker.OldEventID ||
				snapshot.NewEventID != marker.NewEventID {
				t.Fatalf("stable IDs changed across SIGKILL: marker=%+v snapshot=%+v", marker, snapshot)
			}
		})
	}
}

func TestSQLiteSourceGenerationTransitionSIGKILLRecoveryHelper(t *testing.T) {
	mode := os.Getenv(sourceCrashModeEnv)
	if mode == "" {
		t.Skip("crash helper runs only as a child process")
	}
	window := os.Getenv(sourceCrashWindowEnv)
	databasePath := os.Getenv(sourceCrashDatabaseEnv)
	migrationDir := os.Getenv(sourceCrashMigrationsEnv)
	if window == "" || databasePath == "" || migrationDir == "" {
		t.Fatal("crash helper environment is incomplete")
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
		markerPath := os.Getenv(sourceCrashMarkerEnv)
		if markerPath == "" {
			t.Fatal("crash marker path is required")
		}
		marker := seedSourceCrashWindow(t, ctx, database, window)
		contents, err := json.Marshal(marker)
		if err != nil {
			t.Fatalf("marshal crash marker: %v", err)
		}
		temporaryMarker := markerPath + ".tmp"
		if err := os.WriteFile(temporaryMarker, contents, 0o600); err != nil {
			t.Fatalf("write crash marker: %v", err)
		}
		if err := os.Rename(temporaryMarker, markerPath); err != nil {
			t.Fatalf("publish crash marker: %v", err)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "read":
		resultPath := os.Getenv(sourceCrashResultEnv)
		if resultPath == "" {
			t.Fatal("crash result path is required")
		}
		snapshot := recoverSourceCrashWindow(t, ctx, database, window)
		contents, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatalf("marshal crash snapshot: %v", err)
		}
		if err := os.WriteFile(resultPath, contents, 0o600); err != nil {
			t.Fatalf("write crash snapshot: %v", err)
		}
	default:
		t.Fatalf("unknown crash helper mode %q", mode)
	}
}

type sourceCrashSnapshot struct {
	Window                    string `json:"window"`
	OldDeliveryID             string `json:"old_delivery_id"`
	NewDeliveryID             string `json:"new_delivery_id"`
	OldEventID                string `json:"old_event_id"`
	NewEventID                string `json:"new_event_id"`
	OldReceiptFound           bool   `json:"old_receipt_found"`
	NewReceiptFound           bool   `json:"new_receipt_found"`
	BeforeCheckpointFound     bool   `json:"before_checkpoint_found"`
	AfterCheckpointSequence   uint64 `json:"after_checkpoint_sequence"`
	AfterCheckpointGeneration string `json:"after_checkpoint_generation"`
}

type sourceCrashMarker struct {
	Window        string `json:"window"`
	OldDeliveryID string `json:"old_delivery_id"`
	NewDeliveryID string `json:"new_delivery_id"`
	OldEventID    string `json:"old_event_id"`
	NewEventID    string `json:"new_event_id"`
}

func seedSourceCrashWindow(t *testing.T, ctx context.Context, database *store.Store, window string) sourceCrashMarker {
	t.Helper()
	base := time.Unix(1_700_100_000, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, sourceCrashNodeID, base); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	if err := database.EnsureSource(ctx, sourceCrashSourceID, sourceCrashNodeID, store.SourceKindFile, base); err != nil {
		t.Fatalf("EnsureSource(): %v", err)
	}

	state := NewSQLiteStateStore(database)
	state.clock = func() time.Time { return base.Add(2 * time.Second) }
	if err := state.RegisterFileGeneration(ctx, store.FileGeneration{
		SourceID: sourceCrashSourceID, Generation: sourceCrashOldGeneration,
		DeviceID: 21, Inode: 201, Path: "/var/log/guard-crash.log",
		ObservedSize: 10, OpenedAt: base,
	}); err != nil {
		t.Fatalf("RegisterFileGeneration(): %v", err)
	}
	oldPosition := sourceCrashPosition(t, sourceCrashOldGeneration, 21, 201)
	oldDeliveryID := sourceCrashDeliveryID(t, oldPosition)
	putSourceCrashReceipt(t, ctx, database, oldDeliveryID, oldPosition, base.Add(time.Second))

	if err := state.RotateFileGeneration(ctx, sourceCrashSourceID, sourceCrashOldGeneration, store.FileGeneration{
		SourceID: sourceCrashSourceID, Generation: sourceCrashNewGeneration,
		DeviceID: 22, Inode: 202, Path: "/var/log/guard-crash.log", ObservedSize: 10,
	}); err != nil {
		t.Fatalf("RotateFileGeneration(): %v", err)
	}

	newPosition := sourceCrashPosition(t, sourceCrashNewGeneration, 22, 202)
	newDeliveryID := sourceCrashDeliveryID(t, newPosition)
	switch window {
	case sourceCrashBeforeReceipt:
	case sourceCrashBeforeCheckpoint:
		putSourceCrashReceipt(t, ctx, database, newDeliveryID, newPosition, base.Add(3*time.Second))
	default:
		t.Fatalf("unknown crash window %q", window)
	}
	return sourceCrashMarker{
		Window:        window,
		OldDeliveryID: string(oldDeliveryID), NewDeliveryID: string(newDeliveryID),
		OldEventID: string(sourceCrashEventID(t, oldDeliveryID)),
		NewEventID: string(sourceCrashEventID(t, newDeliveryID)),
	}
}

func recoverSourceCrashWindow(
	t *testing.T,
	ctx context.Context,
	database *store.Store,
	window string,
) sourceCrashSnapshot {
	t.Helper()
	state := NewSQLiteStateStore(database)
	generations, err := state.RecoverFileGenerations(ctx, sourceCrashSourceID)
	if err != nil {
		t.Fatalf("RecoverFileGenerations(): %v", err)
	}
	if len(generations) != 2 ||
		generations[0].Generation != sourceCrashOldGeneration ||
		generations[0].DeviceID != 21 ||
		generations[0].Inode != 201 ||
		generations[0].Path != "/var/log/guard-crash.log" ||
		generations[0].ObservedSize != 10 ||
		generations[0].State != store.FileGenerationDraining ||
		generations[1].Generation != sourceCrashNewGeneration ||
		generations[1].DeviceID != 22 ||
		generations[1].Inode != 202 ||
		generations[1].Path != "/var/log/guard-crash.log" ||
		generations[1].ObservedSize != 10 ||
		generations[1].State != store.FileGenerationOpen {
		t.Fatalf("recovered generations = %+v", generations)
	}

	oldPosition := sourceCrashPosition(t, sourceCrashOldGeneration, 21, 201)
	newPosition := sourceCrashPosition(t, sourceCrashNewGeneration, 22, 202)
	oldDeliveryID := sourceCrashDeliveryID(t, oldPosition)
	newDeliveryID := sourceCrashDeliveryID(t, newPosition)
	oldEventID := sourceCrashEventID(t, oldDeliveryID)
	newEventID := sourceCrashEventID(t, newDeliveryID)

	oldReceipt, oldFound, err := database.FindProcessingReceipt(ctx, oldDeliveryID)
	if err != nil {
		t.Fatalf("FindProcessingReceipt(old): %v", err)
	}
	if !oldFound || oldReceipt.Position != oldPosition {
		t.Fatalf("old receipt = %+v, found=%v", oldReceipt, oldFound)
	}
	newReceipt, newFound, err := database.FindProcessingReceipt(ctx, newDeliveryID)
	if err != nil {
		t.Fatalf("FindProcessingReceipt(new): %v", err)
	}
	if newFound && newReceipt.Position != newPosition {
		t.Fatalf("new receipt = %+v, found=%v", newReceipt, newFound)
	}

	checkpoint, checkpointFound, err := state.LoadCheckpoint(ctx, sourceCrashSourceID)
	if err != nil {
		t.Fatalf("LoadCheckpoint() = %+v, found=%v err=%v", checkpoint, checkpointFound, err)
	}
	if checkpointFound {
		t.Fatalf("checkpoint crossed the old-generation hole: %+v", checkpoint)
	}

	switch window {
	case sourceCrashBeforeReceipt:
		if newFound {
			t.Fatal("new receipt exists before its first outcome transaction")
		}
		putSourceCrashReceipt(t, ctx, database, newDeliveryID, newPosition, time.Unix(1_700_100_003, 0).UTC())
	case sourceCrashBeforeCheckpoint:
		if !newFound {
			t.Fatal("new receipt was not durable across SIGKILL")
		}
	default:
		t.Fatalf("unknown crash window %q", window)
	}

	advanceRecoveredSourceCrashCheckpoint(t, ctx, state, oldDeliveryID, oldPosition, newDeliveryID, newPosition)
	after, found, err := state.LoadCheckpoint(ctx, sourceCrashSourceID)
	if err != nil || !found {
		t.Fatalf("LoadCheckpoint(after) = %+v, found=%v err=%v", after, found, err)
	}
	afterFile, ok := after.Position.File()
	if !ok || after.DeliverySequence != 2 || afterFile.Generation != sourceCrashNewGeneration {
		t.Fatalf("checkpoint after recovery advance = %+v", after)
	}

	return sourceCrashSnapshot{
		Window:        window,
		OldDeliveryID: string(oldDeliveryID), NewDeliveryID: string(newDeliveryID),
		OldEventID: string(oldEventID), NewEventID: string(newEventID),
		OldReceiptFound: oldFound, NewReceiptFound: newFound,
		BeforeCheckpointFound:     checkpointFound,
		AfterCheckpointSequence:   uint64(after.DeliverySequence),
		AfterCheckpointGeneration: afterFile.Generation,
	}
}

func putSourceCrashReceipt(
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
		DeliveryID: deliveryID, SourceID: sourceCrashSourceID,
		Position: position, Kind: core.ReceiptSuccess, Committed: committed,
	}); err != nil {
		t.Fatalf("PutReceipt(): %v", err)
	}
	if err := unit.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
}

func advanceRecoveredSourceCrashCheckpoint(
	t *testing.T,
	ctx context.Context,
	state *SQLiteStateStore,
	oldDeliveryID core.DeliveryID,
	oldPosition core.SourcePosition,
	newDeliveryID core.DeliveryID,
	newPosition core.SourcePosition,
) {
	t.Helper()
	tracker, err := NewCompletionTracker(sourceCrashSourceID, 1)
	if err != nil {
		t.Fatalf("NewCompletionTracker(): %v", err)
	}
	manager := NewCheckpointManager(tracker, state)
	for sequence, completion := range []struct {
		deliveryID core.DeliveryID
		position   core.SourcePosition
	}{
		{deliveryID: oldDeliveryID, position: oldPosition},
		{deliveryID: newDeliveryID, position: newPosition},
	} {
		if err := manager.Complete(ctx, core.DurableCompletion{
			SourceID: sourceCrashSourceID, DeliveryID: completion.deliveryID,
			Sequence: core.DeliverySequence(sequence + 1), Position: completion.position,
		}); err != nil {
			t.Fatalf("Complete(sequence=%d): %v", sequence+1, err)
		}
	}
}

func sourceCrashPosition(t *testing.T, generation string, deviceID uint64, inode uint64) core.SourcePosition {
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

func sourceCrashDeliveryID(t *testing.T, position core.SourcePosition) core.DeliveryID {
	t.Helper()
	file, ok := position.File()
	if !ok {
		t.Fatal("source position is not a file position")
	}
	deliveryID, err := core.FileDeliveryID(sourceCrashSourceID, file)
	if err != nil {
		t.Fatalf("FileDeliveryID(): %v", err)
	}
	return deliveryID
}

func sourceCrashEventID(t *testing.T, deliveryID core.DeliveryID) core.EventID {
	t.Helper()
	eventID, err := core.SecurityEventID(sourceCrashNodeID, deliveryID, "parser-crash", "v1", 0)
	if err != nil {
		t.Fatalf("SecurityEventID(): %v", err)
	}
	return eventID
}

func runSourceCrashWriter(
	t *testing.T,
	window string,
	databasePath string,
	migrationDir string,
	markerPath string,
) sourceCrashMarker {
	t.Helper()
	var output bytes.Buffer
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestSQLiteSourceGenerationTransitionSIGKILLRecoveryHelper$",
		"-test.count=1",
	)
	command.Env = append(os.Environ(),
		sourceCrashModeEnv+"=write",
		sourceCrashWindowEnv+"="+window,
		sourceCrashDatabaseEnv+"="+databasePath,
		sourceCrashMigrationsEnv+"="+migrationDir,
		sourceCrashMarkerEnv+"="+markerPath,
	)
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start crash writer: %v", err)
	}

	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	marker, err := waitForSourceCrashMarker(command, wait, markerPath, window, &output)
	if err != nil {
		t.Fatal(err)
	}
	return marker
}

func waitForSourceCrashMarker(
	command *exec.Cmd,
	wait <-chan error,
	markerPath string,
	window string,
	output *bytes.Buffer,
) (sourceCrashMarker, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	for {
		select {
		case err := <-wait:
			return sourceCrashMarker{}, fmt.Errorf(
				"crash writer exited before marker: %v; output: %s",
				err, strings.TrimSpace(output.String()),
			)
		case <-ticker.C:
			contents, err := os.ReadFile(markerPath)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return sourceCrashMarker{}, stopSourceCrashWriter(command, wait, output, fmt.Errorf("read crash marker: %w", err))
			}
			var marker sourceCrashMarker
			if err := json.Unmarshal(contents, &marker); err != nil {
				continue
			}
			if marker.Window != window {
				return sourceCrashMarker{}, stopSourceCrashWriter(
					command, wait, output,
					fmt.Errorf("crash marker window = %q, want %q", marker.Window, window),
				)
			}
			if err := command.Process.Kill(); err != nil {
				waitErr := <-wait
				return sourceCrashMarker{}, fmt.Errorf(
					"SIGKILL crash writer: %v; wait: %v; output: %s",
					err, waitErr, strings.TrimSpace(output.String()),
				)
			}
			if err := validateSourceCrashSIGKILL(<-wait, output.String()); err != nil {
				return sourceCrashMarker{}, err
			}
			return marker, nil
		case <-timer.C:
			return sourceCrashMarker{}, stopSourceCrashWriter(
				command, wait, output, errors.New("timed out waiting for crash marker"),
			)
		}
	}
}

func stopSourceCrashWriter(command *exec.Cmd, wait <-chan error, output *bytes.Buffer, cause error) error {
	killErr := command.Process.Kill()
	waitErr := <-wait
	return fmt.Errorf(
		"%w; kill: %v; wait: %v; output: %s",
		cause, killErr, waitErr, strings.TrimSpace(output.String()),
	)
}

func validateSourceCrashSIGKILL(err error, output string) error {
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return fmt.Errorf("crash writer wait error = %v, want SIGKILL; output: %s", err, strings.TrimSpace(output))
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		return fmt.Errorf("crash writer status = %v, want SIGKILL; output: %s", exitError.Sys(), strings.TrimSpace(output))
	}
	return nil
}

func runSourceCrashReader(
	t *testing.T,
	window string,
	databasePath string,
	migrationDir string,
) sourceCrashSnapshot {
	t.Helper()
	resultPath := filepath.Join(t.TempDir(), "result.json")
	commandContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(
		commandContext,
		os.Args[0],
		"-test.run=^TestSQLiteSourceGenerationTransitionSIGKILLRecoveryHelper$",
		"-test.count=1",
	)
	command.Env = append(os.Environ(),
		sourceCrashModeEnv+"=read",
		sourceCrashWindowEnv+"="+window,
		sourceCrashDatabaseEnv+"="+databasePath,
		sourceCrashMigrationsEnv+"="+migrationDir,
		sourceCrashResultEnv+"="+resultPath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		if commandContext.Err() != nil {
			t.Fatalf("crash reader timed out: %v; output: %s", commandContext.Err(), strings.TrimSpace(string(output)))
		}
		t.Fatalf("crash reader: %v; output: %s", err, strings.TrimSpace(string(output)))
	}
	contents, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read crash result: %v", err)
	}
	var snapshot sourceCrashSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatalf("decode crash result: %v", err)
	}
	return snapshot
}
