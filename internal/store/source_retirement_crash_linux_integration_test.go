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

const retirementCrashModeEnv = "GUARD_RETIREMENT_CRASH_MODE"

func TestSourceRetirementSIGKILLRecovery(t *testing.T) {
	migrations, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"before-commit", "after-commit"} {
		t.Run(phase, func(t *testing.T) {
			directory := t.TempDir()
			markerPath := filepath.Join(directory, "marker.json")
			env := append(os.Environ(), processingCrashDatabaseEnv+"="+filepath.Join(directory, "guard.db"), processingCrashMigrationsEnv+"="+migrations, processingCrashMarkerEnv+"="+markerPath)
			command := exec.Command(os.Args[0], "-test.run=^TestSourceRetirementSIGKILLHelper$", "-test.count=1", "-test.timeout=20s")
			command.Env = append(env, retirementCrashModeEnv+"="+phase)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
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
			reader := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSourceRetirementSIGKILLHelper$", "-test.count=1", "-test.timeout=15s")
			reader.Env = append(env, retirementCrashModeEnv+"=read")
			if out, err := reader.CombinedOutput(); err != nil {
				t.Fatalf("fresh recovery: %v: %s", err, out)
			}
		})
	}
}

func TestSourceRetirementSIGKILLHelper(t *testing.T) {
	phase := os.Getenv(retirementCrashModeEnv)
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
		if actual := readCoverageCrashRows(t, database); !reflect.DeepEqual(actual, expected) {
			t.Fatalf("recovered rows differ: got=%+v want=%+v", actual, expected)
		}
		_, cp, found, generations, err := database.LoadSourceCoverageState(ctx, testSourceID)
		wantCount := 2
		if marker.DeliveryID == "after-commit" {
			wantCount = 1
		}
		if err != nil || !found || cp.Position != filePosition(t, testGeneration2, 0, 10) || len(generations) != wantCount {
			t.Fatalf("recovery=%+v/%+v/%v", cp, generations, err)
		}
		if generations[len(generations)-1].Generation != testGeneration2 || *generations[len(generations)-1].DurableEndOffset != 10 {
			t.Fatalf("remaining recovery prefix=%+v", generations)
		}
		return
	}
	base := prepareFileSource(t, database, testSourceID, testGeneration, 10)
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
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 2, filePosition(t, testGeneration2, 0, 10), []core.FilePosition{coverageSpan(testGeneration, 0, 10), coverageSpan(testGeneration2, 0, 10)}, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.SealFileGenerationWithSession(ctx, session, testGeneration, FileGenerationDraining, 10, 1, base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	expected := readCoverageCrashRows(t, database)
	stamp := base.Add(4 * time.Second)
	if phase == "after-commit" {
		for i := range expected.Generations {
			if expected.Generations[i].Generation == testGeneration {
				expected.Generations[i].State = string(FileGenerationRetired)
				expected.Generations[i].RetiredAtUS = optionalInt64(stamp.UnixMicro())
			}
		}
	}
	publish := func() {
		content, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		publishProcessingCrashMarker(t, markerPath, processingCrashMarker{DeliveryID: phase, EventID: string(content)})
		blockUntilSIGKILL()
	}
	if phase == "before-commit" {
		if err := database.orm.Callback().Update().After("gorm:update").Register("test:retirement-before-commit", func(tx *gorm.DB) {
			if tx.Statement.Table != "source_file_generations" || tx.Error != nil {
				return
			}
			var state string
			var at int64
			if err := tx.Statement.ConnPool.QueryRowContext(ctx, "SELECT state, retired_at_us FROM source_file_generations WHERE generation=?", testGeneration).Scan(&state, &at); err != nil || state != string(FileGenerationRetired) || at != stamp.UnixMicro() {
				t.Fatalf("in-transaction retirement=%s/%d/%v", state, at, err)
			}
			publish()
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, stamp); err != nil {
		t.Fatal(err)
	}
	if phase != "after-commit" {
		t.Fatal("pre-commit callback did not fire")
	}
	publish()
}
