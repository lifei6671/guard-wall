//go:build linux && integration

package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	journaldRestartModeEnv       = "GUARD_JOURNALD_RESTART_MODE"
	journaldRestartDatabaseEnv   = "GUARD_JOURNALD_RESTART_DATABASE"
	journaldRestartMigrationsEnv = "GUARD_JOURNALD_RESTART_MIGRATIONS"
	journaldRestartInputEnv      = "GUARD_JOURNALD_RESTART_INPUT"
	journaldRestartResultEnv     = "GUARD_JOURNALD_RESTART_RESULT"

	journaldRestartSourceID core.SourceID = "source-linux-journald-restart"
	journaldRestartNodeID   core.NodeID   = "fedcba9876543210fedcba9876543210"
)

func TestSQLiteJournaldCursorRestartReplay(t *testing.T) {
	cursors := loadLiveJournaldCursors(t)
	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("resolve migration directory: %v", err)
	}
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "guard.db")
	inputPath := filepath.Join(directory, "cursors.json")
	writeJournaldRestartInput(t, inputPath, cursors)

	written := runJournaldRestartHelper(t, "write", databasePath, migrationDir, inputPath)
	recovered := runJournaldRestartHelper(t, "read", databasePath, migrationDir, inputPath)

	if !reflect.DeepEqual(recovered, written) {
		t.Fatalf("restart snapshot changed:\nwritten:   %+v\nrecovered: %+v", written, recovered)
	}
	if written.Cursors[0] == written.Cursors[1] {
		t.Fatal("journalctl returned duplicate cursors")
	}
	if written.DeliveryIDs[0] == written.DeliveryIDs[1] {
		t.Fatal("distinct Journald cursors produced the same DeliveryID")
	}
	if written.EventIDs[0] == written.EventIDs[1] {
		t.Fatal("distinct Journald cursors produced the same EventID")
	}
}

func TestSQLiteJournaldCursorRestartReplayHelper(t *testing.T) {
	mode := os.Getenv(journaldRestartModeEnv)
	if mode == "" {
		t.Skip("restart helper runs only as a child process")
	}
	databasePath := os.Getenv(journaldRestartDatabaseEnv)
	migrationDir := os.Getenv(journaldRestartMigrationsEnv)
	inputPath := os.Getenv(journaldRestartInputEnv)
	resultPath := os.Getenv(journaldRestartResultEnv)
	if databasePath == "" || migrationDir == "" || inputPath == "" || resultPath == "" {
		t.Fatal("restart helper environment is incomplete")
	}

	cursors := readJournaldRestartInput(t, inputPath)
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
		seedJournaldRestartState(t, ctx, database, cursors)
	case "read":
		// A fresh process reaches this branch after the writer closed SQLite.
	default:
		t.Fatalf("unknown restart helper mode %q", mode)
	}

	snapshot := readJournaldRestartSnapshot(t, ctx, database, cursors)
	contents, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal restart snapshot: %v", err)
	}
	if err := os.WriteFile(resultPath, contents, 0o600); err != nil {
		t.Fatalf("write restart snapshot: %v", err)
	}
}

type journaldRestartInput struct {
	Cursors []string `json:"cursors"`
}

type journaldRestartSnapshot struct {
	Cursors             []string              `json:"cursors"`
	DeliveryIDs         []string              `json:"delivery_ids"`
	EventIDs            []string              `json:"event_ids"`
	ReceiptsFound       []bool                `json:"receipts_found"`
	CheckpointSequence  uint64                `json:"checkpoint_sequence"`
	CheckpointSession   store.SourceSessionID `json:"checkpoint_session"`
	CheckpointPersisted time.Time             `json:"checkpoint_persisted"`
	CheckpointCursor    string                `json:"checkpoint_cursor"`
}

func loadLiveJournaldCursors(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("journalctl"); err != nil {
		t.Fatalf("journalctl is unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		"journalctl", "--no-pager", "-n", "16", "-o", "json", "--output-fields=__CURSOR",
	)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("readable Journald is required: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(output))
	seen := make(map[string]struct{})
	var cursors []string
	for len(cursors) < 2 {
		var entry struct {
			Cursor string `json:"__CURSOR"`
		}
		if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode journalctl JSON: %v", err)
		}
		if entry.Cursor == "" {
			continue
		}
		if _, found := seen[entry.Cursor]; found {
			continue
		}
		seen[entry.Cursor] = struct{}{}
		cursors = append(cursors, entry.Cursor)
	}
	if len(cursors) != 2 {
		t.Fatalf("need two distinct readable Journald cursors, found %d", len(cursors))
	}
	return cursors
}

func writeJournaldRestartInput(t *testing.T, path string, cursors []string) {
	t.Helper()
	contents, err := json.Marshal(journaldRestartInput{Cursors: cursors})
	if err != nil {
		t.Fatalf("marshal cursor input: %v", err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write cursor input: %v", err)
	}
}

func readJournaldRestartInput(t *testing.T, path string) []string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cursor input: %v", err)
	}
	var input journaldRestartInput
	if err := json.Unmarshal(contents, &input); err != nil {
		t.Fatalf("decode cursor input: %v", err)
	}
	if len(input.Cursors) != 2 || input.Cursors[0] == "" || input.Cursors[1] == "" || input.Cursors[0] == input.Cursors[1] {
		t.Fatal("cursor input must contain two distinct non-empty cursors")
	}
	return input.Cursors
}

func seedJournaldRestartState(t *testing.T, ctx context.Context, database *store.Store, cursors []string) {
	t.Helper()
	base := time.Unix(1_700_200_000, 0).UTC()
	if err := database.EnsureNodeIdentity(ctx, journaldRestartNodeID, base); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	if err := database.EnsureSource(ctx, journaldRestartSourceID, journaldRestartNodeID, store.SourceKindJournald, base); err != nil {
		t.Fatalf("EnsureSource(): %v", err)
	}

	state := NewSQLiteStateStore(database, beginSQLiteSourceSession(t, database, journaldRestartSourceID))
	state.clock = func() time.Time { return base.Add(3 * time.Second) }
	tracker, err := NewCompletionTracker(journaldRestartSourceID, 1)
	if err != nil {
		t.Fatalf("NewCompletionTracker(): %v", err)
	}
	checkpoints := NewCheckpointManager(tracker, state)
	for index, cursor := range cursors {
		position := journaldRestartPosition(t, cursor)
		deliveryID := journaldRestartDeliveryID(t, cursor)
		putJournaldRestartReceipt(t, ctx, database, deliveryID, position, base.Add(time.Duration(index+1)*time.Second))
		if err := checkpoints.Complete(ctx, core.DurableCompletion{
			SourceID: journaldRestartSourceID, DeliveryID: deliveryID,
			Sequence: core.DeliverySequence(index + 1), Position: position,
		}); err != nil {
			t.Fatalf("Complete(sequence=%d): %v", index+1, err)
		}
	}
}

func readJournaldRestartSnapshot(
	t *testing.T,
	ctx context.Context,
	database *store.Store,
	cursors []string,
) journaldRestartSnapshot {
	t.Helper()
	snapshot := journaldRestartSnapshot{Cursors: append([]string(nil), cursors...)}
	for _, cursor := range cursors {
		position := journaldRestartPosition(t, cursor)
		deliveryID := journaldRestartDeliveryID(t, cursor)
		eventID, err := core.SecurityEventID(journaldRestartNodeID, deliveryID, "parser-journald", "v1", 0)
		if err != nil {
			t.Fatalf("SecurityEventID(): %v", err)
		}
		receipt, found, err := database.FindProcessingReceipt(ctx, deliveryID)
		if err != nil {
			t.Fatalf("FindProcessingReceipt(): %v", err)
		}
		if !found || receipt.Position != position {
			t.Fatalf("receipt = %+v, found=%v", receipt, found)
		}
		snapshot.DeliveryIDs = append(snapshot.DeliveryIDs, string(deliveryID))
		snapshot.EventIDs = append(snapshot.EventIDs, string(eventID))
		snapshot.ReceiptsFound = append(snapshot.ReceiptsFound, found)
	}

	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, journaldRestartSourceID)
	if err != nil || !found {
		t.Fatalf("LoadCheckpoint() = %+v, found=%v err=%v", checkpoint, found, err)
	}
	journal, ok := checkpoint.Position.Journald()
	if !ok {
		t.Fatalf("checkpoint position = %+v, want Journald", checkpoint.Position)
	}
	if checkpoint.DeliverySequence != 2 || journal.Cursor != cursors[1] {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
	snapshot.CheckpointSequence = uint64(checkpoint.DeliverySequence)
	snapshot.CheckpointSession = checkpoint.SessionID
	snapshot.CheckpointPersisted = checkpoint.PersistedAt
	snapshot.CheckpointCursor = journal.Cursor
	return snapshot
}

func journaldRestartPosition(t *testing.T, cursor string) core.SourcePosition {
	t.Helper()
	position, err := core.NewJournaldPosition(cursor)
	if err != nil {
		t.Fatalf("NewJournaldPosition(): %v", err)
	}
	return position
}

func journaldRestartDeliveryID(t *testing.T, cursor string) core.DeliveryID {
	t.Helper()
	deliveryID, err := core.JournaldDeliveryID(journaldRestartSourceID, cursor)
	if err != nil {
		t.Fatalf("JournaldDeliveryID(): %v", err)
	}
	return deliveryID
}

func putJournaldRestartReceipt(
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
		DeliveryID: deliveryID, SourceID: journaldRestartSourceID,
		Position: position, Kind: core.ReceiptSuccess, Committed: committed,
	}); err != nil {
		t.Fatalf("PutReceipt(): %v", err)
	}
	if err := unit.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
}

func runJournaldRestartHelper(
	t *testing.T,
	mode string,
	databasePath string,
	migrationDir string,
	inputPath string,
) journaldRestartSnapshot {
	t.Helper()
	resultPath := filepath.Join(t.TempDir(), mode+".json")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		os.Args[0],
		"-test.run=^TestSQLiteJournaldCursorRestartReplayHelper$",
		"-test.count=1",
	)
	command.Env = append(os.Environ(),
		journaldRestartModeEnv+"="+mode,
		journaldRestartDatabaseEnv+"="+databasePath,
		journaldRestartMigrationsEnv+"="+migrationDir,
		journaldRestartInputEnv+"="+inputPath,
		journaldRestartResultEnv+"="+resultPath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("restart helper %q timed out: %v; output: %s", mode, ctx.Err(), strings.TrimSpace(string(output)))
		}
		t.Fatalf("restart helper %q: %v; output: %s", mode, err, strings.TrimSpace(string(output)))
	}
	contents, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read restart helper %q result: %v", mode, err)
	}
	var snapshot journaldRestartSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatalf("decode restart helper %q result: %v", mode, err)
	}
	return snapshot
}
