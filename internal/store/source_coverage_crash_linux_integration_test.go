//go:build linux && integration

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

const coverageCrashModeEnv = "GUARD_COVERAGE_CRASH_MODE"

type coverageCrashRows struct {
	Sources     []sourceRow
	Checkpoints []sourceCheckpointRow
	Generations []sourceFileGenerationRow
	Business    map[string][]string
}

func TestSourceCoverageTransactionSIGKILLAtomicRecovery(t *testing.T) {
	migrationDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"before-commit", "after-commit"} {
		t.Run(phase, func(t *testing.T) {
			directory := t.TempDir()
			databasePath := filepath.Join(directory, "guard.db")
			markerPath := filepath.Join(directory, "marker.json")
			var output bytes.Buffer
			command := exec.Command(os.Args[0], "-test.run=^TestSourceCoverageTransactionSIGKILLHelper$", "-test.count=1", "-test.timeout=20s")
			command.Env = append(os.Environ(), coverageCrashModeEnv+"="+phase, processingCrashDatabaseEnv+"="+databasePath, processingCrashMigrationsEnv+"="+migrationDir, processingCrashMarkerEnv+"="+markerPath)
			command.Stdout = &output
			command.Stderr = &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- command.Wait() }()
			marker, err := waitForProcessingCrashMarker(command, wait, markerPath, &output)
			if err != nil {
				t.Fatal(err)
			}
			if marker.DeliveryID != phase {
				t.Fatalf("phase=%s", marker.DeliveryID)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			reader := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSourceCoverageTransactionSIGKILLHelper$", "-test.count=1", "-test.timeout=15s")
			reader.Env = append(os.Environ(), coverageCrashModeEnv+"=read", processingCrashDatabaseEnv+"="+databasePath, processingCrashMigrationsEnv+"="+migrationDir, processingCrashMarkerEnv+"="+markerPath)
			if out, err := reader.CombinedOutput(); err != nil {
				t.Fatalf("fresh reader: %v: %s", err, out)
			}
		})
	}
}

func TestSourceCoverageTransactionSIGKILLHelper(t *testing.T) {
	phase := os.Getenv(coverageCrashModeEnv)
	if phase == "" {
		t.Skip("subprocess fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	database, err := Open(ctx, os.Getenv(processingCrashDatabaseEnv), os.DirFS(os.Getenv(processingCrashMigrationsEnv)))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	markerPath := os.Getenv(processingCrashMarkerEnv)
	if phase == "read" {
		content, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatal(err)
		}
		var marker processingCrashMarker
		if err := json.Unmarshal(content, &marker); err != nil {
			t.Fatal(err)
		}
		var expected coverageCrashRows
		if err := json.Unmarshal([]byte(marker.EventID), &expected); err != nil {
			t.Fatal(err)
		}
		actual := readCoverageCrashRows(t, database)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("recovered complete rows differ: actual=%+v expected=%+v", actual, expected)
		}
		return
	}
	base := prepareFileSource(t, database, testSourceID, testGeneration, 20)
	session := beginTestSourceSession(t, database, testSourceID)
	if err := database.InitializeFileGenerationCoverage(ctx, session, testGeneration); err != nil {
		t.Fatal(err)
	}
	if err := database.RotateFileGeneration(ctx, testSourceID, testGeneration, FileGeneration{SourceID: testSourceID, Generation: testGeneration2, DeviceID: 1, Inode: 2, Path: "/var/log/guard.log", OpenedAt: base.Add(time.Second)}, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.InitializeFileGenerationCoverage(ctx, session, testGeneration2); err != nil {
		t.Fatal(err)
	}
	expected := readCoverageCrashRows(t, database)
	stamp := base.Add(2 * time.Second)
	if phase == "after-commit" {
		for i := range expected.Generations {
			end := int64(10)
			if expected.Generations[i].Generation == testGeneration2 {
				end = 20
			}
			expected.Generations[i].DurableEndOffset = &end
		}
		expected.Checkpoints = []sourceCheckpointRow{{SourceID: string(testSourceID), CheckpointSessionID: optionalString(string(session.ID())), DeliverySequence: 2, PositionKind: "file", Generation: optionalString(testGeneration2), DeviceID: optionalInt64(int64(1)), Inode: optionalInt64(int64(2)), StartOffset: optionalInt64(int64(0)), EndOffset: optionalInt64(int64(20)), PersistedAtUS: stamp.UnixMicro()}}
	}
	publish := func() {
		t.Helper()
		content, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		publishProcessingCrashMarker(t, markerPath, processingCrashMarker{DeliveryID: phase, EventID: string(content)})
		blockUntilSIGKILL()
	}
	if phase == "before-commit" {
		if err := database.orm.Callback().Update().After("gorm:update").Register("test:coverage-before-commit", func(tx *gorm.DB) {
			if tx.Statement.Table != "source_file_generations" || tx.Error != nil {
				return
			}
			updates, ok := tx.Statement.Dest.(map[string]any)
			if !ok || updates[SourceFileGenerationColumns.DurableEndOffset] != int64(10) {
				return
			}
			// 此回调位于第一个前缀 UPDATE 后：同事务 checkpoint 已写，第二代际尚未写。
			var sequence, first, second int64
			if err := tx.Statement.ConnPool.QueryRowContext(ctx, `SELECT delivery_sequence FROM source_checkpoints WHERE source_id=?`, testSourceID).Scan(&sequence); err != nil || sequence != 2 {
				t.Fatalf("transaction sequence=%d/%v", sequence, err)
			}
			if err := tx.Statement.ConnPool.QueryRowContext(ctx, `SELECT durable_end_offset FROM source_file_generations WHERE source_id=? AND generation=?`, testSourceID, testGeneration).Scan(&first); err != nil || first != 10 {
				t.Fatalf("transaction first=%d/%v", first, err)
			}
			if err := tx.Statement.ConnPool.QueryRowContext(ctx, `SELECT durable_end_offset FROM source_file_generations WHERE source_id=? AND generation=?`, testSourceID, testGeneration2).Scan(&second); err != nil || second != 0 {
				t.Fatalf("transaction second=%d/%v", second, err)
			}
			publish()
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 2, filePosition(t, testGeneration2, 0, 20), []core.FilePosition{coverageSpan(testGeneration, 0, 10), coverageSpan(testGeneration2, 0, 20)}, stamp); err != nil {
		t.Fatal(err)
	}
	if phase != "after-commit" {
		t.Fatal("pre-commit callback did not fire")
	}
	publish()
}

func readCoverageCrashRows(t *testing.T, database *Store) coverageCrashRows {
	t.Helper()
	result := coverageCrashRows{Business: sourceSessionMigrationRows(t, database.db, map[string][]string{})}
	delete(result.Business, "sources")
	delete(result.Business, "source_checkpoints")
	delete(result.Business, "source_file_generations")
	if err := database.orm.Order("source_id").Find(&result.Sources).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.orm.Order("source_id").Find(&result.Checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.orm.Order("generation").Find(&result.Generations).Error; err != nil {
		t.Fatal(err)
	}
	return result
}
