package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

func TestSourceRetirementCompletedGenerationAcrossSessions(t *testing.T) {
	database, _, base := prepareRetirementCandidate(t, "complete")
	ctx := context.Background()
	before, found, err := loadFileGeneration(ctx, database.orm, testSourceID, testGeneration)
	if err != nil || !found {
		t.Fatalf("generation before=%+v/%v/%v", before, found, err)
	}
	nextBefore, found, err := loadFileGeneration(ctx, database.orm, testSourceID, testGeneration2)
	if err != nil || !found {
		t.Fatalf("next generation before=%+v/%v/%v", nextBefore, found, err)
	}
	_, checkpoint, _, err := database.LoadSourceSessionState(ctx, testSourceID)
	if err != nil || checkpoint.DeliverySequence != 1 || checkpoint.SessionID == *before.CoverageSessionID || *before.MaxDeliverySequence != 100 {
		t.Fatalf("cross-session fixture=%+v/%+v/%v", before, checkpoint, err)
	}
	rows := sourceSessionMigrationRows(t, database.db, map[string][]string{})
	at := base.Add(5 * time.Second)
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, at); err != nil {
		t.Fatal(err)
	}
	after, found, err := loadFileGeneration(ctx, database.orm, testSourceID, testGeneration)
	want := before
	want.State, want.RetiredAt = FileGenerationRetired, &at
	if err != nil || !found || !reflect.DeepEqual(after, want) {
		t.Fatalf("retired row=%+v, want=%+v found=%v err=%v", after, want, found, err)
	}
	afterRows := sourceSessionMigrationRows(t, database.db, map[string][]string{})
	delete(rows, "source_file_generations")
	delete(afterRows, "source_file_generations")
	if !reflect.DeepEqual(rows, afterRows) {
		t.Fatal("retirement changed checkpoint, receipt or business rows")
	}
	recovered, err := database.LoadRecoverableFileGenerations(ctx, testSourceID)
	if err != nil || len(recovered) != 1 || !reflect.DeepEqual(recovered[0], nextBefore) {
		t.Fatalf("recoverable=%+v/%v", recovered, err)
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, at.Add(time.Second)); !errors.Is(err, ErrFileGenerationTransition) {
		t.Fatalf("second retirement=%v", err)
	}
}

func TestSourceRetirementEmptyGenerationWithoutCheckpoint(t *testing.T) {
	database, _, base := prepareRetirementCandidate(t, "empty")
	ctx := context.Background()
	if _, found, err := database.LoadSourceCheckpoint(ctx, testSourceID); err != nil || found {
		t.Fatalf("empty checkpoint found=%v err=%v", found, err)
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, base.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	record, found, err := loadFileGeneration(ctx, database.orm, testSourceID, testGeneration)
	if err != nil || !found || record.State != FileGenerationRetired || record.DurableEndOffset == nil || *record.DurableEndOffset != 0 || record.FinalEOF == nil || *record.FinalEOF != 0 || record.RetiredAt == nil || !record.RetiredAt.Equal(base.Add(5*time.Second)) {
		t.Fatalf("empty retired row=%+v/%v/%v", record, found, err)
	}
}

func TestSourceRetirementRejectedLeavesAllRowsUnchanged(t *testing.T) {
	for _, test := range []struct {
		name string
		want error
	}{
		{"unknown", ErrFileGenerationNotDurable},
		{"unknown-empty", ErrFileGenerationNotDurable},
		{"incomplete", ErrFileGenerationNotDurable},
		{"checkpoint-at-eof", ErrFileGenerationReferenced},
		{"receipt", ErrFileGenerationReferenced},
		{"open", ErrFileGenerationTransition},
		{"predates-seal", ErrFileGenerationTransition},
		{"cancelled", context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, _, base := prepareRetirementCandidate(t, test.name)
			ctx := context.Background()
			if test.name == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			at := base.Add(5 * time.Second)
			if test.name == "predates-seal" {
				at = base
			}
			before := sourceSessionMigrationRows(t, database.db, map[string][]string{})
			if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, at); !errors.Is(err, test.want) {
				t.Fatalf("retirement=%v, want %v", err, test.want)
			}
			if !reflect.DeepEqual(before, sourceSessionMigrationRows(t, database.db, map[string][]string{})) {
				t.Fatal("rejected retirement changed durable rows")
			}
		})
	}
}

func TestSourceRetirementUpdateFailureRollsBack(t *testing.T) {
	database, _, base := prepareRetirementCandidate(t, "complete")
	before := sourceSessionMigrationRows(t, database.db, map[string][]string{})
	injected := errors.New("retirement update failed after write")
	fired := false
	const callback = "guard_wall:test_retirement_after_update"
	if err := database.orm.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "source_file_generations" || tx.Error != nil {
			return
		}
		var state string
		if err := tx.Statement.ConnPool.QueryRowContext(context.Background(), "SELECT state FROM source_file_generations WHERE source_id=? AND generation=?", testSourceID, testGeneration).Scan(&state); err != nil {
			tx.AddError(err)
			return
		}
		if state == "retired" {
			fired = true
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.orm.Callback().Update().Remove(callback); err != nil {
			t.Error(err)
		}
	})
	if err := database.RetireFileGeneration(context.Background(), testSourceID, testGeneration, base.Add(5*time.Second)); !errors.Is(err, injected) || !fired {
		t.Fatalf("injected failure=%v fired=%v", err, fired)
	}
	if !reflect.DeepEqual(before, sourceSessionMigrationRows(t, database.db, map[string][]string{})) {
		t.Fatal("failed retirement persisted a partial update")
	}
}

func TestSourceRetirementRejectsLateCheckpointAndReceipt(t *testing.T) {
	database, session, base := prepareRetirementCandidate(t, "complete")
	ctx := context.Background()
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, base.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	before := sourceSessionMigrationRows(t, database.db, map[string][]string{})
	position := filePosition(t, testGeneration, 0, 10)
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 2, position, []core.FilePosition{coverageSpan(testGeneration, 0, 10)}, base.Add(6*time.Second)); !errors.Is(err, ErrSourcePositionMismatch) {
		t.Fatalf("late checkpoint=%v", err)
	}
	// 绕过应用校验，证明数据库 trigger 仍禁止已退休代际成为恢复锚点。
	if _, err := database.db.ExecContext(ctx, "UPDATE source_checkpoints SET generation=? WHERE source_id=?", testGeneration, testSourceID); err == nil {
		t.Fatal("checkpoint trigger accepted retired generation")
	}
	file, _ := position.File()
	deliveryID, err := core.FileDeliveryID(testSourceID, file)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cleanupOpenUnitOfWork(t, unit)
	if err := unit.PutReceipt(ctx, core.ProcessingReceipt{DeliveryID: deliveryID, SourceID: testSourceID, Position: position, Kind: core.ReceiptSuccess, Committed: base.Add(6 * time.Second)}); err == nil {
		t.Fatal("late receipt was accepted")
	}
	if err := unit.Commit(); err == nil {
		t.Fatal("failed late receipt transaction committed")
	}
	if !reflect.DeepEqual(before, sourceSessionMigrationRows(t, database.db, map[string][]string{})) {
		t.Fatal("late writes changed rows")
	}
}

func prepareRetirementCandidate(t *testing.T, mode string) (*Store, SourceSession, time.Time) {
	t.Helper()
	ctx := context.Background()
	database := openTestStore(t)
	base := prepareFileSource(t, database, testSourceID, testGeneration, 10)
	session := beginTestSourceSession(t, database, testSourceID)
	known := mode != "unknown" && mode != "unknown-empty"
	if known {
		if err := database.InitializeFileGenerationCoverage(ctx, session, testGeneration); err != nil {
			t.Fatal(err)
		}
	}
	end := uint64(10)
	if mode == "empty" || mode == "unknown-empty" {
		end = 0
	}
	if mode == "open" {
		return database, session, base
	}
	if err := database.SealFileGenerationWithSession(ctx, session, testGeneration, FileGenerationOpen, end, 100, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if end == 0 {
		return database, session, base
	}
	if known && mode != "incomplete" {
		if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 100, filePosition(t, testGeneration, 0, 10), []core.FilePosition{coverageSpan(testGeneration, 0, 10)}, base.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "checkpoint-at-eof" {
		return database, session, base
	}
	if err := database.RegisterFileGeneration(ctx, FileGeneration{SourceID: testSourceID, Generation: testGeneration2, DeviceID: 1, Inode: 2, Path: "/var/log/guard.log", OpenedAt: base.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	session = beginTestSourceSession(t, database, testSourceID)
	if err := database.InitializeFileGenerationCoverage(ctx, session, testGeneration2); err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 1, filePosition(t, testGeneration2, 0, 10), []core.FilePosition{coverageSpan(testGeneration2, 0, 10)}, base.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if mode == "receipt" {
		position := filePosition(t, testGeneration, 0, 10)
		file, _ := position.File()
		deliveryID, err := core.FileDeliveryID(testSourceID, file)
		if err != nil {
			t.Fatal(err)
		}
		commitGORMReceipt(t, database, core.ProcessingReceipt{DeliveryID: deliveryID, SourceID: testSourceID, Position: position, Kind: core.ReceiptSuccess, Committed: base.Add(4 * time.Second)})
	}
	return database, session, base
}
