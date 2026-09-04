//go:build linux && integration

package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestSQLiteSourceSessionBeginSIGKILLRestartSequenceOne(t *testing.T) {
	if mode := os.Getenv("GUARD_SESSION_RESTART_MODE"); mode != "" {
		runSQLiteSessionRestartChild(t, mode)
		return
	}
	path := filepath.Join(t.TempDir(), "guard.db")
	marker := filepath.Join(t.TempDir(), "begun.json")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	command := func(mode string) *exec.Cmd {
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteSourceSessionBeginSIGKILLRestartSequenceOne$", "-test.timeout=30s")
		child.Env = append(os.Environ(), "GUARD_SESSION_RESTART_MODE="+mode, "GUARD_SESSION_RESTART_DB="+path, "GUARD_SESSION_RESTART_MARKER="+marker)
		return child
	}
	if output, err := command("seed").CombinedOutput(); err != nil {
		t.Fatalf("seed session A: %v\n%s", err, output)
	}
	child := command("begin")
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	reaped := false
	defer func() {
		if !reaped {
			if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("cleanup child: %v", err)
			}
			if err := <-done; err != nil {
				t.Logf("cleanup child exit: %v", err)
			}
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	ready := false
	for !ready {
		select {
		case err := <-done:
			reaped = true
			t.Fatalf("Begin child exited before marker: %v\n%s", err, output.String())
		case <-ctx.Done():
			t.Fatal("Begin child did not publish marker")
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				ready = true
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	// 只终止本测试创建、已经提交 Begin B 且未提交 checkpoint 的进程。
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := <-done
	reaped = true
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("Begin child exit=%v", err)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("Begin child status=%v", exitError.Sys())
	}
	if output, err := command("restart").CombinedOutput(); err != nil {
		t.Fatalf("restart session C: %v\n%s", err, output)
	}
}

type sqliteSessionSnapshot struct {
	ActiveSession     store.SourceSessionID
	CheckpointSession store.SourceSessionID
	SourceID          core.SourceID
	Sequence          core.DeliverySequence
	Position          core.FilePosition
	PersistedAt       time.Time
}

func runSQLiteSessionRestartChild(t *testing.T, mode string) {
	path, marker := os.Getenv("GUARD_SESSION_RESTART_DB"), os.Getenv("GUARD_SESSION_RESTART_MARKER")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if mode == "seed" {
		database := openSQLiteSessionFixture(t, path)
		session := beginSQLiteSourceSession(t, database, "source-1")
		if err := database.InitializeFileGenerationCoverage(ctx, session, "00112233445566778899aabbccddeeff"); err != nil {
			t.Fatal(err)
		}
		tracker, err := NewCompletionTracker("source-1", 1)
		if err != nil {
			t.Fatal(err)
		}
		manager := NewCheckpointManager(tracker, NewSQLiteStateStore(database, session))
		for sequence := 1; sequence <= 100; sequence++ {
			if err := manager.Complete(ctx, durableCompletion(t, core.DeliverySequence(sequence), uint64(sequence-1)*10, uint64(sequence)*10)); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	database, err := store.Open(ctx, path, sourceMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	active, checkpoint, found, err := database.LoadSourceSessionState(ctx, "source-1")
	if err != nil || !found || checkpoint.DeliverySequence != 100 {
		t.Fatalf("recovery state=%+v found=%v err=%v", checkpoint, found, err)
	}
	position, ok := checkpoint.Position.File()
	if !ok {
		t.Fatal("recovery checkpoint is not a File Position")
	}
	snapshot := sqliteSessionSnapshot{ActiveSession: active, CheckpointSession: checkpoint.SessionID, SourceID: checkpoint.SourceID, Sequence: checkpoint.DeliverySequence, Position: position, PersistedAt: checkpoint.PersistedAt}
	if mode == "restart" {
		contents, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		var before sqliteSessionSnapshot
		if err := json.Unmarshal(contents, &before); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(snapshot, before) {
			t.Fatalf("SIGKILL changed complete recovery state: got=%+v want=%+v", snapshot, before)
		}
	} else if mode != "begin" {
		t.Fatalf("unknown mode %q", mode)
	}
	id, err := store.NewSourceSessionID()
	if err != nil {
		t.Fatal(err)
	}
	session, recovered, found, err := database.BeginSourceSession(ctx, "source-1", active, id)
	if err != nil || !found || recovered != checkpoint || session.ID() == active {
		t.Fatalf("Begin recovery=%+v found=%v err=%v", recovered, found, err)
	}
	if mode == "begin" {
		snapshot.ActiveSession = session.ID()
		contents, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker+".tmp", contents, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(marker+".tmp", marker); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		t.Fatal("Begin child was not killed")
	}
	// 从 Begin 返回的稳定 Position 接续，本次 session 重新编号为 1。
	completion := durableCompletion(t, 1, position.EndOffset, position.EndOffset+10)
	tracker, err := NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewCheckpointManager(tracker, NewSQLiteStateStore(database, session))
	if err := manager.Complete(ctx, completion); err != nil {
		t.Fatal(err)
	}
	after, found, err := database.LoadSourceCheckpoint(ctx, "source-1")
	if err != nil || !found || after.SessionID != session.ID() || after.DeliverySequence != 1 || after.Position != completion.Position {
		t.Fatalf("restarted checkpoint=%+v found=%v err=%v", after, found, err)
	}
}
