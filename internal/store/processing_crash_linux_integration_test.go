//go:build linux && integration

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
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
)

const (
	processingCrashModeEnv       = "GUARD_PROCESSING_CRASH_MODE"
	processingCrashDatabaseEnv   = "GUARD_PROCESSING_CRASH_DATABASE"
	processingCrashMigrationsEnv = "GUARD_PROCESSING_CRASH_MIGRATIONS"
	processingCrashMarkerEnv     = "GUARD_PROCESSING_CRASH_MARKER"
	processingCrashResultEnv     = "GUARD_PROCESSING_CRASH_RESULT"
)

func TestM0Recovery003SQLiteProcessingTransactionSIGKILLReplay(t *testing.T) {
	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("resolve migration directory: %v", err)
	}
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "guard.db")
	markerPath := filepath.Join(directory, "ready.json")

	marker := runProcessingCrashWriter(t, databasePath, migrationDir, markerPath)
	snapshot := runProcessingCrashReader(t, databasePath, migrationDir)
	if snapshot.DeliveryID != marker.DeliveryID || snapshot.EventID != marker.EventID {
		t.Fatalf("stable identity changed across SIGKILL: marker=%+v snapshot=%+v", marker, snapshot)
	}
	if !snapshot.ReceiptFound || snapshot.CheckpointSequence != 1 || snapshot.CheckpointEndOffset != 10 {
		t.Fatalf("replay snapshot = %+v", snapshot)
	}
	contents, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal replay snapshot: %v", err)
	}
	digest := sha256.Sum256(contents)
	t.Logf("M0_FINAL_STATE_DIGEST=%x", digest)
}

func TestSQLiteProcessingTransactionSIGKILLReplayHelper(t *testing.T) {
	mode := os.Getenv(processingCrashModeEnv)
	if mode == "" {
		t.Skip("processing crash helper runs only as a child process")
	}
	databasePath := os.Getenv(processingCrashDatabaseEnv)
	migrationDir := os.Getenv(processingCrashMigrationsEnv)
	if databasePath == "" || migrationDir == "" {
		t.Fatal("processing crash helper environment is incomplete")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database, err := Open(ctx, databasePath, os.DirFS(migrationDir))
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
		markerPath := os.Getenv(processingCrashMarkerEnv)
		if markerPath == "" {
			t.Fatal("processing crash marker path is required")
		}
		fixture := prepareProcessingFixture(t, database)
		unit, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		writeCompleteProcessingOutcome(t, unit, fixture)
		publishProcessingCrashMarker(t, markerPath, processingCrashMarker{
			DeliveryID: string(fixture.deliveryID),
			EventID:    string(fixture.eventID),
		})
		for {
			time.Sleep(time.Hour)
		}
	case "read":
		resultPath := os.Getenv(processingCrashResultEnv)
		if resultPath == "" {
			t.Fatal("processing crash result path is required")
		}
		snapshot := recoverProcessingCrash(t, ctx, database)
		contents, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatalf("marshal processing crash snapshot: %v", err)
		}
		if err := os.WriteFile(resultPath, contents, 0o600); err != nil {
			t.Fatalf("write processing crash snapshot: %v", err)
		}
	default:
		t.Fatalf("unknown processing crash helper mode %q", mode)
	}
}

type processingCrashMarker struct {
	DeliveryID string `json:"delivery_id"`
	EventID    string `json:"event_id"`
}

type processingCrashSnapshot struct {
	DeliveryID          string `json:"delivery_id"`
	EventID             string `json:"event_id"`
	ReceiptFound        bool   `json:"receipt_found"`
	CheckpointSequence  uint64 `json:"checkpoint_sequence"`
	CheckpointEndOffset uint64 `json:"checkpoint_end_offset"`
}

func publishProcessingCrashMarker(t *testing.T, markerPath string, marker processingCrashMarker) {
	t.Helper()
	contents, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("marshal processing crash marker: %v", err)
	}
	temporaryMarker := markerPath + ".tmp"
	if err := os.WriteFile(temporaryMarker, contents, 0o600); err != nil {
		t.Fatalf("write processing crash marker: %v", err)
	}
	if err := os.Rename(temporaryMarker, markerPath); err != nil {
		t.Fatalf("publish processing crash marker: %v", err)
	}
}

func recoverProcessingCrash(
	t *testing.T,
	ctx context.Context,
	database *Store,
) processingCrashSnapshot {
	t.Helper()
	fixture := processingFixtureValues(t)
	assertProcessingCrashCounts(t, ctx, database, 0)
	if receipt, found, err := database.FindProcessingReceipt(ctx, fixture.deliveryID); err != nil || found {
		t.Fatalf("FindProcessingReceipt(before replay) = %+v,%v,%v", receipt, found, err)
	}
	if checkpoint, found, err := database.LoadSourceCheckpoint(ctx, processingSourceID); err != nil || found {
		t.Fatalf("LoadSourceCheckpoint(before replay) = %+v,%v,%v", checkpoint, found, err)
	}

	unit, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(replay): %v", err)
	}
	writeCompleteProcessingOutcome(t, unit, fixture)
	if err := unit.Commit(); err != nil {
		t.Fatalf("Commit(replay): %v", err)
	}
	assertProcessingCrashCounts(t, ctx, database, 1)

	receipt, found, err := database.FindProcessingReceipt(ctx, fixture.deliveryID)
	if err != nil || !found || receipt != fixture.receipt {
		t.Fatalf("FindProcessingReceipt(after replay) = %+v,%v,%v", receipt, found, err)
	}
	if err := database.AdvanceSourceCheckpoint(
		ctx,
		processingSourceID,
		core.DeliverySequence(1),
		fixture.position,
		fixture.now.Add(2*time.Second),
	); err != nil {
		t.Fatalf("AdvanceSourceCheckpoint(): %v", err)
	}
	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, processingSourceID)
	if err != nil || !found {
		t.Fatalf("LoadSourceCheckpoint(after replay) = %+v,%v,%v", checkpoint, found, err)
	}
	file, ok := checkpoint.Position.File()
	if !ok || checkpoint.DeliverySequence != 1 || checkpoint.Position != fixture.position {
		t.Fatalf("checkpoint after replay = %+v", checkpoint)
	}
	assertProcessingCrashCounts(t, ctx, database, 1)

	return processingCrashSnapshot{
		DeliveryID:          string(fixture.deliveryID),
		EventID:             string(fixture.eventID),
		ReceiptFound:        found,
		CheckpointSequence:  uint64(checkpoint.DeliverySequence),
		CheckpointEndOffset: file.EndOffset,
	}
}

func assertProcessingCrashCounts(t *testing.T, ctx context.Context, database *Store, want int) {
	t.Helper()
	for _, table := range []string{
		"parser_terminal_outcomes",
		"detection_terminal_outcomes",
		"detection_contributions",
		"alerts",
		"decisions",
		"desired_ban_projections",
		"audit_logs",
		"processing_receipts",
	} {
		var count int
		if err := database.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
}

func runProcessingCrashWriter(
	t *testing.T,
	databasePath string,
	migrationDir string,
	markerPath string,
) processingCrashMarker {
	t.Helper()
	var output bytes.Buffer
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestSQLiteProcessingTransactionSIGKILLReplayHelper$",
		"-test.count=1",
	)
	command.Env = append(os.Environ(),
		processingCrashModeEnv+"=write",
		processingCrashDatabaseEnv+"="+databasePath,
		processingCrashMigrationsEnv+"="+migrationDir,
		processingCrashMarkerEnv+"="+markerPath,
	)
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start processing crash writer: %v", err)
	}

	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	marker, err := waitForProcessingCrashMarker(command, wait, markerPath, &output)
	if err != nil {
		t.Fatal(err)
	}
	return marker
}

func waitForProcessingCrashMarker(
	command *exec.Cmd,
	wait <-chan error,
	markerPath string,
	output *bytes.Buffer,
) (processingCrashMarker, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	for {
		select {
		case err := <-wait:
			return processingCrashMarker{}, fmt.Errorf(
				"processing crash writer exited before marker: %v; output: %s",
				err,
				strings.TrimSpace(output.String()),
			)
		case <-ticker.C:
			contents, err := os.ReadFile(markerPath)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return processingCrashMarker{}, stopProcessingCrashWriter(
					command,
					wait,
					output,
					fmt.Errorf("read processing crash marker: %w", err),
				)
			}
			var marker processingCrashMarker
			if err := json.Unmarshal(contents, &marker); err != nil {
				continue
			}
			if marker.DeliveryID == "" || marker.EventID == "" {
				continue
			}
			if err := command.Process.Kill(); err != nil {
				waitErr := <-wait
				return processingCrashMarker{}, fmt.Errorf(
					"SIGKILL processing crash writer: %v; wait: %v; output: %s",
					err,
					waitErr,
					strings.TrimSpace(output.String()),
				)
			}
			if err := validateProcessingCrashSIGKILL(<-wait, output.String()); err != nil {
				return processingCrashMarker{}, err
			}
			return marker, nil
		case <-timer.C:
			return processingCrashMarker{}, stopProcessingCrashWriter(
				command,
				wait,
				output,
				errors.New("timed out waiting for processing crash marker"),
			)
		}
	}
}

func stopProcessingCrashWriter(
	command *exec.Cmd,
	wait <-chan error,
	output *bytes.Buffer,
	cause error,
) error {
	killErr := command.Process.Kill()
	waitErr := <-wait
	return fmt.Errorf(
		"%w; kill: %v; wait: %v; output: %s",
		cause,
		killErr,
		waitErr,
		strings.TrimSpace(output.String()),
	)
}

func validateProcessingCrashSIGKILL(err error, output string) error {
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return fmt.Errorf(
			"processing crash writer wait error = %v, want SIGKILL; output: %s",
			err,
			strings.TrimSpace(output),
		)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		return fmt.Errorf(
			"processing crash writer status = %v, want SIGKILL; output: %s",
			exitError.Sys(),
			strings.TrimSpace(output),
		)
	}
	return nil
}

func runProcessingCrashReader(
	t *testing.T,
	databasePath string,
	migrationDir string,
) processingCrashSnapshot {
	t.Helper()
	resultPath := filepath.Join(t.TempDir(), "result.json")
	commandContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(
		commandContext,
		os.Args[0],
		"-test.run=^TestSQLiteProcessingTransactionSIGKILLReplayHelper$",
		"-test.count=1",
	)
	command.Env = append(os.Environ(),
		processingCrashModeEnv+"=read",
		processingCrashDatabaseEnv+"="+databasePath,
		processingCrashMigrationsEnv+"="+migrationDir,
		processingCrashResultEnv+"="+resultPath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		if commandContext.Err() != nil {
			t.Fatalf("processing crash reader timed out: %v; output: %s", commandContext.Err(), strings.TrimSpace(string(output)))
		}
		t.Fatalf("processing crash reader: %v; output: %s", err, strings.TrimSpace(string(output)))
	}
	contents, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read processing crash result: %v", err)
	}
	var snapshot processingCrashSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatalf("decode processing crash result: %v", err)
	}
	return snapshot
}
