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

func TestSourceCoverageAtomicRangesRetryAndSessionRecovery(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	base := prepareFileSource(t, database, testSourceID, testGeneration, 30)
	a := beginTestSourceSession(t, database, testSourceID)
	if err := database.InitializeFileGenerationCoverage(ctx, a, testGeneration); err != nil {
		t.Fatal(err)
	}
	if err := database.RotateFileGeneration(ctx, testSourceID, testGeneration, FileGeneration{SourceID: testSourceID, Generation: testGeneration2, DeviceID: 1, Inode: 2, Path: "/var/log/guard.log", OpenedAt: base.Add(time.Second)}, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.InitializeFileGenerationCoverage(ctx, a, testGeneration2); err != nil {
		t.Fatal(err)
	}
	spans := []core.FilePosition{coverageSpan(testGeneration, 0, 20), coverageSpan(testGeneration2, 0, 10)}
	last := filePosition(t, testGeneration, 10, 20)
	stamp := base.Add(2 * time.Second)
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, a, 3, last, spans, stamp); err != nil {
		t.Fatal(err)
	}
	active, cp, found, generations, err := database.LoadSourceCoverageState(ctx, testSourceID)
	if err != nil || !found || active != a.ID() || cp.SessionID != a.ID() || cp.DeliverySequence != 3 || cp.Position != last || cp.PersistedAt != stamp || len(generations) != 2 {
		t.Fatalf("snapshot=%v/%+v/%v/%+v/%v", active, cp, found, generations, err)
	}
	for i, end := range []uint64{20, 10} {
		if generations[i].DurableEndOffset == nil || *generations[i].DurableEndOffset != end || *generations[i].CoverageSessionID != a.ID() {
			t.Fatalf("generation=%+v", generations[i])
		}
	}
	before := sourceSessionMigrationRows(t, database.db, map[string][]string{})
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, a, 3, last, spans, stamp.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, sourceSessionMigrationRows(t, database.db, map[string][]string{})) {
		t.Fatal("idempotent retry changed rows")
	}
	// 同一 sequence 不能追加另一个代际的新证明。
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, a, 3, last, []core.FilePosition{spans[0], coverageSpan(testGeneration2, 0, 20)}, stamp); !errors.Is(err, ErrCheckpointRegression) {
		t.Fatalf("equal sequence extension=%v", err)
	}
	// 第一个代际已执行 UPDATE 后，第二个代际的洞使 checkpoint 和全部行回滚。
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, a, 4, filePosition(t, testGeneration, 20, 30), []core.FilePosition{coverageSpan(testGeneration, 20, 30), coverageSpan(testGeneration2, 11, 20)}, stamp); !errors.Is(err, ErrFileGenerationNotDurable) {
		t.Fatalf("gap=%v", err)
	}
	if !reflect.DeepEqual(before, sourceSessionMigrationRows(t, database.db, map[string][]string{})) {
		t.Fatal("failed batch changed rows")
	}
	// 提交确认丢失后，累计范围携带已提交前缀和连续新后缀。
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, a, 4, filePosition(t, testGeneration, 20, 30), []core.FilePosition{coverageSpan(testGeneration, 0, 30), spans[1]}, stamp.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	b := beginTestSourceSession(t, database, testSourceID)
	if err := database.InitializeFileGenerationCoverage(ctx, b, testGeneration); err != nil {
		t.Fatal(err)
	}
	_, _, _, generations, err = database.LoadSourceCoverageState(ctx, testSourceID)
	if err != nil || *generations[0].DurableEndOffset != 30 || *generations[0].CoverageSessionID != a.ID() {
		t.Fatalf("recovery reset prefix: %+v/%v", generations, err)
	}
	if err := database.SealFileGenerationWithSession(ctx, b, testGeneration, FileGenerationDraining, 30, 1, stamp.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, b, 1, filePosition(t, testGeneration2, 10, 20), []core.FilePosition{coverageSpan(testGeneration2, 10, 20)}, stamp.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	_, cp, _, generations, err = database.LoadSourceCoverageState(ctx, testSourceID)
	if err != nil || cp.SessionID != b.ID() || cp.DeliverySequence != 1 || !generations[0].CoverageComplete() || *generations[0].CoverageSessionID != a.ID() || *generations[1].CoverageSessionID != b.ID() {
		t.Fatalf("new session snapshot=%+v/%+v/%v", cp, generations, err)
	}
	before = sourceSessionMigrationRows(t, database.db, map[string][]string{})
	for _, operation := range []func() error{
		func() error { return database.InitializeFileGenerationCoverage(ctx, a, testGeneration2) },
		func() error {
			return database.SealFileGenerationWithSession(ctx, a, testGeneration2, FileGenerationOpen, 20, 1, stamp.Add(4*time.Second))
		},
		func() error {
			return database.AdvanceSourceCheckpointWithCoverage(ctx, a, 5, filePosition(t, testGeneration2, 20, 30), []core.FilePosition{coverageSpan(testGeneration2, 20, 30)}, stamp)
		},
	} {
		if err := operation(); !errors.Is(err, ErrStaleSourceSession) {
			t.Fatalf("stale owner=%v", err)
		}
	}
	if !reflect.DeepEqual(before, sourceSessionMigrationRows(t, database.db, map[string][]string{})) {
		t.Fatal("stale owner changed rows")
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, stamp.Add(5*time.Second)); err != nil {
		t.Fatalf("retire complete prior-session generation: %v", err)
	}
	_, cp, found, generations, err = database.LoadSourceCoverageState(ctx, testSourceID)
	if err != nil || !found || cp.SessionID != b.ID() || cp.Position != filePosition(t, testGeneration2, 10, 20) || len(generations) != 1 || generations[0].Generation != testGeneration2 {
		t.Fatalf("recovery after retirement=%+v/%+v/%v", cp, generations, err)
	}
}

func TestSourceCoverageSealOrdersAndBoundaries(t *testing.T) {
	for _, order := range []string{"seal-first", "complete-first", "empty", "unknown-empty"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			database := openTestStore(t)
			base := prepareFileSource(t, database, testSourceID, testGeneration, 10)
			session := beginTestSourceSession(t, database, testSourceID)
			end := uint64(10)
			if order == "empty" || order == "unknown-empty" {
				end = 0
			}
			if order != "unknown-empty" {
				if err := database.InitializeFileGenerationCoverage(ctx, session, testGeneration); err != nil {
					t.Fatal(err)
				}
			}
			seal := func() {
				t.Helper()
				if err := database.SealFileGenerationWithSession(ctx, session, testGeneration, FileGenerationOpen, end, 1, base.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			advance := func() {
				t.Helper()
				if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 1, filePosition(t, testGeneration, 0, end), []core.FilePosition{coverageSpan(testGeneration, 0, end)}, base.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if order == "seal-first" {
				seal()
				advance()
			} else if order == "complete-first" {
				advance()
				seal()
			} else {
				seal()
			}
			_, _, _, generations, err := database.LoadSourceCoverageState(ctx, testSourceID)
			if err != nil || len(generations) != 1 || generations[0].CoverageComplete() != (order != "unknown-empty") {
				t.Fatalf("completion=%+v/%v", generations, err)
			}
			before := sourceSessionMigrationRows(t, database.db, map[string][]string{})
			if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 2, filePosition(t, testGeneration, end, end+1), []core.FilePosition{coverageSpan(testGeneration, end, end+1)}, base.Add(2*time.Second)); !errors.Is(err, ErrFileGenerationNotDurable) {
				t.Fatalf("beyond EOF/unknown=%v", err)
			}
			if !reflect.DeepEqual(before, sourceSessionMigrationRows(t, database.db, map[string][]string{})) {
				t.Fatal("invalid range changed rows")
			}
		})
	}
}

func TestSourceCoverageRejectsBypassIdentityAndCancellation(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	base := prepareFileSource(t, database, testSourceID, testGeneration, 10)
	session := beginTestSourceSession(t, database, testSourceID)
	if err := database.InitializeFileGenerationCoverage(ctx, session, testGeneration); err != nil {
		t.Fatal(err)
	}
	before := sourceSessionMigrationRows(t, database.db, map[string][]string{})
	if err := database.AdvanceSourceCheckpoint(ctx, session, 1, filePosition(t, testGeneration, 0, 10), base); !errors.Is(err, ErrFileGenerationNotDurable) {
		t.Fatalf("legacy bypass=%v", err)
	}
	if err := database.SealFileGeneration(ctx, testSourceID, testGeneration, FileGenerationOpen, 10, 1, base); !errors.Is(err, ErrFileGenerationTransition) {
		t.Fatalf("sessionless seal=%v", err)
	}
	bad := coverageSpan(testGeneration, 0, 10)
	bad.Inode = 3
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, session, 1, filePosition(t, testGeneration, 0, 10), []core.FilePosition{bad}, base); !errors.Is(err, ErrSourcePositionMismatch) {
		t.Fatalf("identity=%v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := database.AdvanceSourceCheckpointWithCoverage(cancelled, session, 1, filePosition(t, testGeneration, 0, 10), []core.FilePosition{coverageSpan(testGeneration, 0, 10)}, base); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if !reflect.DeepEqual(before, sourceSessionMigrationRows(t, database.db, map[string][]string{})) {
		t.Fatal("rejected operation changed rows")
	}
}

func TestSourceCoverageReadbackUsesOneSnapshot(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	base := prepareFileSource(t, database, testSourceID, testGeneration, 20)
	a := beginTestSourceSession(t, database, testSourceID)
	if err := database.InitializeFileGenerationCoverage(ctx, a, testGeneration); err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpointWithCoverage(ctx, a, 1, filePosition(t, testGeneration, 0, 10), []core.FilePosition{coverageSpan(testGeneration, 0, 10)}, base); err != nil {
		t.Fatal(err)
	}
	other := secondSourceSessionStore(t, database)
	fired := false
	var switchErr error
	const callback = "test:coverage-snapshot"
	if err := database.orm.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "sources" {
			return
		}
		fired = true
		id, err := NewSourceSessionID()
		if err != nil {
			switchErr = err
			return
		}
		b, _, _, err := other.BeginSourceSession(ctx, testSourceID, a.ID(), id)
		if err == nil {
			err = other.AdvanceSourceCheckpointWithCoverage(ctx, b, 1, filePosition(t, testGeneration, 10, 20), []core.FilePosition{coverageSpan(testGeneration, 10, 20)}, base.Add(time.Second))
		}
		switchErr = err
	}); err != nil {
		t.Fatal(err)
	}
	active, cp, found, generations, err := database.LoadSourceCoverageState(ctx, testSourceID)
	if removeErr := database.orm.Callback().Query().Remove(callback); removeErr != nil {
		t.Fatal(removeErr)
	}
	if err != nil || switchErr != nil || !fired || !found || active != a.ID() || cp.SessionID != a.ID() || *generations[0].DurableEndOffset != 10 || *generations[0].CoverageSessionID != a.ID() {
		t.Fatalf("snapshot=%s/%+v/%+v/%v/%v", active, cp, generations, err, switchErr)
	}
	_, cp, _, generations, err = database.LoadSourceCoverageState(ctx, testSourceID)
	if err != nil || cp.SessionID == a.ID() || *generations[0].DurableEndOffset != 20 || *generations[0].CoverageSessionID != cp.SessionID {
		t.Fatalf("fresh snapshot=%+v/%+v/%v", cp, generations, err)
	}
}

func coverageSpan(generation string, start, end uint64) core.FilePosition {
	return core.FilePosition{Generation: generation, DeviceID: 1, Inode: 2, StartOffset: start, EndOffset: end}
}
