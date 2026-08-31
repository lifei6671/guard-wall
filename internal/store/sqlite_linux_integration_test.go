//go:build linux && integration

package store

import (
	"bytes"
	"context"
	"database/sql"
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
	sqliteDurabilityModeEnv       = "GUARD_SQLITE_DURABILITY_MODE"
	sqliteDurabilityDatabaseEnv   = "GUARD_SQLITE_DURABILITY_DATABASE"
	sqliteDurabilityMarkerEnv     = "GUARD_SQLITE_DURABILITY_MARKER"
	sqliteDurabilityMigrationsEnv = "GUARD_SQLITE_DURABILITY_MIGRATIONS"

	sqliteDurabilityNodeID core.NodeID = "0123456789abcdef0123456789abcdef"
)

func TestSQLiteLinuxSIGKILLReopenDurability(t *testing.T) {
	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("resolve migration directory: %v", err)
	}

	tests := []struct {
		name       string
		writerMode string
		marker     string
		verifyMode string
	}{
		{
			name:       "committed node identity survives",
			writerMode: "write_committed",
			marker:     "commit-returned",
			verifyMode: "verify_committed",
		},
		{
			name:       "uncommitted node identity is absent",
			writerMode: "write_uncommitted",
			marker:     "transaction-open",
			verifyMode: "verify_uncommitted",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "guard.db")
			markerPath := databasePath + ".marker"

			writer := startSQLiteDurabilityProcess(
				t,
				test.writerMode,
				databasePath,
				markerPath,
				migrationDir,
			)
			if err := writer.waitForMarker(markerPath, test.marker, 10*time.Second); err != nil {
				writer.stop()
				t.Fatalf("wait for writer boundary: %v; output: %s", err, writer.output())
			}
			writer.killAndAssertSIGKILL(t)

			output, err := runSQLiteDurabilityProcess(
				test.verifyMode,
				databasePath,
				markerPath,
				migrationDir,
			)
			if err != nil {
				t.Fatalf("reopen verification: %v; output: %s", err, output)
			}
		})
	}
}

func TestSQLiteLinuxDurabilityHelper(t *testing.T) {
	mode := os.Getenv(sqliteDurabilityModeEnv)
	if mode == "" {
		t.Skip("durability helper runs only as a child process")
	}
	databasePath := os.Getenv(sqliteDurabilityDatabaseEnv)
	markerPath := os.Getenv(sqliteDurabilityMarkerEnv)
	migrationDir := os.Getenv(sqliteDurabilityMigrationsEnv)
	if databasePath == "" || markerPath == "" || migrationDir == "" {
		t.Fatal("durability helper environment is incomplete")
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
	case "write_committed":
		if err := database.EnsureNodeIdentity(
			ctx,
			sqliteDurabilityNodeID,
			time.Unix(100, 0).UTC(),
		); err != nil {
			t.Fatalf("EnsureNodeIdentity(): %v", err)
		}
		writeSQLiteDurabilityMarker(t, markerPath, "commit-returned")
		blockUntilSIGKILL()
	case "write_uncommitted":
		tx, err := database.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx(): %v", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO node_identity(singleton, node_id, created_at_us)
			VALUES (1, ?, ?)`, string(sqliteDurabilityNodeID), time.Unix(100, 0).UnixMicro()); err != nil {
			t.Fatalf("insert uncommitted node identity: %v", err)
		}
		writeSQLiteDurabilityMarker(t, markerPath, "transaction-open")
		blockUntilSIGKILL()
	case "verify_committed":
		verifySQLiteDurabilityStore(t, ctx, database, true)
	case "verify_uncommitted":
		verifySQLiteDurabilityStore(t, ctx, database, false)
	default:
		t.Fatalf("unknown durability helper mode %q", mode)
	}
}

type sqliteDurabilityProcess struct {
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	wait    chan error
	done    bool
}

func startSQLiteDurabilityProcess(
	t *testing.T,
	mode string,
	databasePath string,
	markerPath string,
	migrationDir string,
) *sqliteDurabilityProcess {
	t.Helper()

	process := &sqliteDurabilityProcess{wait: make(chan error, 1)}
	process.command = sqliteDurabilityCommand(mode, databasePath, markerPath, migrationDir)
	process.command.Stdout = &process.stdout
	process.command.Stderr = &process.stderr
	if err := process.command.Start(); err != nil {
		t.Fatalf("start durability helper %q: %v", mode, err)
	}
	go func() {
		process.wait <- process.command.Wait()
	}()
	t.Cleanup(process.stop)
	return process
}

func runSQLiteDurabilityProcess(
	mode string,
	databasePath string,
	markerPath string,
	migrationDir string,
) (string, error) {
	command := sqliteDurabilityCommand(mode, databasePath, markerPath, migrationDir)
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func sqliteDurabilityCommand(
	mode string,
	databasePath string,
	markerPath string,
	migrationDir string,
) *exec.Cmd {
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestSQLiteLinuxDurabilityHelper$",
		"-test.count=1",
	)
	command.Env = append(
		os.Environ(),
		sqliteDurabilityModeEnv+"="+mode,
		sqliteDurabilityDatabaseEnv+"="+databasePath,
		sqliteDurabilityMarkerEnv+"="+markerPath,
		sqliteDurabilityMigrationsEnv+"="+migrationDir,
	)
	return command
}

func (process *sqliteDurabilityProcess) waitForMarker(
	markerPath string,
	want string,
	timeout time.Duration,
) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-process.wait:
			process.done = true
			return fmt.Errorf("helper exited before marker: %w", err)
		case <-ticker.C:
			contents, err := os.ReadFile(markerPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return fmt.Errorf("read marker: %w", err)
			}
			if string(contents) != want {
				return fmt.Errorf("marker = %q, want %q", contents, want)
			}
			return nil
		case <-deadline.C:
			return fmt.Errorf("marker %q was not published within %s", want, timeout)
		}
	}
}

func (process *sqliteDurabilityProcess) killAndAssertSIGKILL(t *testing.T) {
	t.Helper()
	if process.done {
		t.Fatal("durability helper exited before SIGKILL")
	}
	if err := process.command.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL durability helper: %v", err)
	}
	waitErr := <-process.wait
	process.done = true

	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("helper wait error = %v, want killed process", waitErr)
	}
	waitStatus, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() || waitStatus.Signal() != syscall.SIGKILL {
		t.Fatalf("helper wait status = %v, want SIGKILL", exitErr.Sys())
	}
}

func (process *sqliteDurabilityProcess) stop() {
	if process.done {
		return
	}
	_ = process.command.Process.Kill()
	<-process.wait
	process.done = true
}

func (process *sqliteDurabilityProcess) output() string {
	return strings.TrimSpace(process.stdout.String() + process.stderr.String())
}

func writeSQLiteDurabilityMarker(t *testing.T, path string, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("write durability marker: %v", err)
	}
}

func verifySQLiteDurabilityStore(
	t *testing.T,
	ctx context.Context,
	database *Store,
	wantCommitted bool,
) {
	t.Helper()

	pragmas, err := database.Pragmas(ctx)
	if err != nil {
		t.Fatalf("Pragmas(): %v", err)
	}
	if err := pragmas.validate(); err != nil {
		t.Fatalf("Pragmas() validation: %v", err)
	}

	migrations, err := loadMigrations(os.DirFS(os.Getenv(sqliteDurabilityMigrationsEnv)))
	if err != nil {
		t.Fatalf("loadMigrations(): %v", err)
	}
	var migrationCount int
	if err := database.db.QueryRowContext(
		ctx,
		"SELECT count(*) FROM schema_migrations",
	).Scan(&migrationCount); err != nil {
		t.Fatalf("read migration ledger: %v", err)
	}
	if migrationCount != len(migrations) {
		t.Fatalf("migration count = %d, want %d", migrationCount, len(migrations))
	}

	var persisted string
	err = database.db.QueryRowContext(
		ctx,
		"SELECT node_id FROM node_identity WHERE singleton = 1",
	).Scan(&persisted)
	if wantCommitted {
		if err != nil {
			t.Fatalf("read committed node identity: %v", err)
		}
		if persisted != string(sqliteDurabilityNodeID) {
			t.Fatalf("node identity = %q, want %q", persisted, sqliteDurabilityNodeID)
		}
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read uncommitted node identity: %v", err)
	}
}

func blockUntilSIGKILL() {
	for {
		time.Sleep(time.Hour)
	}
}
