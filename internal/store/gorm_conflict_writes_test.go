package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

type conflictWriteCreateCapture struct {
	calls        int
	table        string
	selects      []string
	sql          string
	vars         []any
	rowsAffected int64
}

type conflictWriteRowsAffectedErrorResult struct {
	sql.Result
	err error
}

func (r conflictWriteRowsAffectedErrorResult) RowsAffected() (int64, error) {
	return 0, r.err
}

type conflictWriteRowsAffectedErrorPool struct {
	gorm.ConnPool
	err error
}

func (p *conflictWriteRowsAffectedErrorPool) ExecContext(
	ctx context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	result, err := p.ConnPool.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return conflictWriteRowsAffectedErrorResult{Result: result, err: p.err}, nil
}

type conflictWriteStaticResult int64

func (r conflictWriteStaticResult) LastInsertId() (int64, error) { return 0, nil }
func (r conflictWriteStaticResult) RowsAffected() (int64, error) { return int64(r), nil }

type conflictWriteNoOpExecPool struct {
	gorm.ConnPool
}

func (p *conflictWriteNoOpExecPool) ExecContext(
	context.Context,
	string,
	...any,
) (sql.Result, error) {
	return conflictWriteStaticResult(0), nil
}

func TestGORMConflictWritesDetectionContributionExactCreate(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	contribution := fixture.contribution
	contribution.ContributedAt = fixture.now.In(time.FixedZone("conflict-write", 9*60*60))
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	capture := registerConflictWriteCreateCapture(t, uow, "contribution_exact")

	inserted, err := uow.PutDetectionContribution(ctx, contribution)
	if err != nil || !inserted {
		t.Fatalf("PutDetectionContribution() = %v, %v, want true, nil", inserted, err)
	}
	if capture.calls != 1 || capture.table != "detection_contributions" || capture.rowsAffected != 1 {
		t.Fatalf(
			"GORM Create capture = calls %d table %q rows %d, want 1/detection_contributions/1",
			capture.calls, capture.table, capture.rowsAffected,
		)
	}
	wantSelects := []string{"event_id", "rule_id", "rule_version", "delivery_id", "contributed_at_us"}
	if !reflect.DeepEqual(capture.selects, wantSelects) {
		t.Fatalf("GORM Create selects = %v, want %v", capture.selects, wantSelects)
	}
	wantSQL := "INSERT INTO `detection_contributions` (`event_id`,`rule_id`,`rule_version`,`delivery_id`,`contributed_at_us`) VALUES (?,?,?,?,?) ON CONFLICT (`event_id`,`rule_id`,`rule_version`) DO NOTHING"
	if normalizedSQL := strings.Join(strings.Fields(capture.sql), " "); normalizedSQL != wantSQL {
		t.Fatalf("GORM Create SQL = %q, want %q", normalizedSQL, wantSQL)
	}
	wantVars := []any{
		string(contribution.EventID), string(contribution.RuleID), string(contribution.RuleVersion),
		string(contribution.DeliveryID), contribution.ContributedAt.UTC().UnixMicro(),
	}
	if !reflect.DeepEqual(capture.vars, wantVars) {
		t.Fatalf("GORM Create vars = %#v, want %#v", capture.vars, wantVars)
	}

	if count := conflictWriteContributionCountTx(t, uow, contribution); count != 1 {
		t.Fatalf("contribution count inside UnitOfWork transaction = %d, want 1", count)
	}
	if err := uow.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
	assertConflictWriteContributionCount(t, database, contribution, 0)
}

func TestGORMConflictWritesDetectionContributionDuplicateIdentity(t *testing.T) {
	t.Run("same delivery is idempotent", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		commitConflictWriteContribution(t, database, fixture)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		capture := registerConflictWriteCreateCapture(t, uow, "contribution_same_delivery")

		inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution)
		if err != nil || inserted {
			t.Fatalf("duplicate PutDetectionContribution() = %v, %v, want false, nil", inserted, err)
		}
		if capture.calls != 1 || capture.rowsAffected != 0 {
			t.Fatalf("duplicate GORM Create capture = calls %d rows %d, want 1/0", capture.calls, capture.rowsAffected)
		}
		if err := uow.Commit(); err != nil {
			t.Fatalf("Commit(): %v", err)
		}
		assertConflictWriteContributionCount(t, database, fixture.contribution, 1)
	})

	t.Run("different delivery is sticky conflict", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		commitConflictWriteContribution(t, database, fixture)
		conflicting := fixture.contribution
		conflicting.DeliveryID = conflictWriteAlternateDeliveryID(t)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		capture := registerConflictWriteCreateCapture(t, uow, "contribution_different_delivery")

		inserted, firstErr := uow.PutDetectionContribution(ctx, conflicting)
		if firstErr == nil || inserted {
			t.Fatalf("conflicting PutDetectionContribution() = %v, %v, want false, error", inserted, firstErr)
		}
		if !strings.Contains(firstErr.Error(), "stable delivery identity differs") {
			t.Fatalf("conflicting PutDetectionContribution() error = %v", firstErr)
		}
		if capture.calls != 1 || capture.rowsAffected != 0 {
			t.Fatalf("conflicting GORM Create capture = calls %d rows %d, want 1/0", capture.calls, capture.rowsAffected)
		}
		if inserted, secondErr := uow.PutDetectionContribution(ctx, fixture.contribution); secondErr != firstErr || inserted {
			t.Fatalf("sticky PutDetectionContribution() = %v, %v, want false, original error", inserted, secondErr)
		}
		if capture.calls != 1 {
			t.Fatalf("GORM Create calls after sticky failure = %d, want 1", capture.calls)
		}
		if commitErr := uow.Commit(); commitErr != firstErr {
			t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
		}
		assertConflictWriteContributionCount(t, database, fixture.contribution, 1)
	})
}

func TestGORMConflictWritesDetectionContributionForeignKeysAndCancel(t *testing.T) {
	t.Run("rule revision foreign key fails immediately", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		contribution := fixture.contribution
		contribution.RuleVersion = "missing-v1"
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)

		inserted, firstErr := uow.PutDetectionContribution(ctx, contribution)
		if firstErr == nil || inserted {
			t.Fatalf("PutDetectionContribution() = %v, %v, want false, FK error", inserted, firstErr)
		}
		if inserted, secondErr := uow.PutDetectionContribution(ctx, fixture.contribution); secondErr != firstErr || inserted {
			t.Fatalf("sticky PutDetectionContribution() = %v, %v, want false, original error", inserted, secondErr)
		}
		if commitErr := uow.Commit(); commitErr != firstErr {
			t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
		}
		assertConflictWriteContributionCount(t, database, contribution, 0)
	})

	t.Run("delivery foreign key is checked at commit", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		capture := registerConflictWriteCreateCapture(t, uow, "contribution_deferred_delivery")

		inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution)
		if err != nil || !inserted {
			t.Fatalf("PutDetectionContribution() = %v, %v, want true, nil before commit", inserted, err)
		}
		if capture.calls != 1 || capture.rowsAffected != 1 {
			t.Fatalf("GORM Create capture = calls %d rows %d, want 1/1", capture.calls, capture.rowsAffected)
		}
		if commitErr := uow.Commit(); commitErr == nil {
			t.Fatal("Commit() without referenced receipt = nil")
		}
		assertConflictWriteContributionCount(t, database, fixture.contribution, 0)
	})

	t.Run("canceled context is sticky", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		uow, err := database.BeginProcessing(context.Background())
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		inserted, firstErr := uow.PutDetectionContribution(ctx, fixture.contribution)
		if inserted || !errors.Is(firstErr, context.Canceled) {
			t.Fatalf("PutDetectionContribution() = %v, %v, want false, context.Canceled", inserted, firstErr)
		}
		if inserted, secondErr := uow.PutDetectionContribution(context.Background(), fixture.contribution); secondErr != firstErr || inserted {
			t.Fatalf("sticky PutDetectionContribution() = %v, %v, want false, original error", inserted, secondErr)
		}
		if commitErr := uow.Commit(); commitErr != firstErr {
			t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
		}
		assertConflictWriteContributionCount(t, database, fixture.contribution, 0)
	})
}

func TestGORMConflictWritesPreserveRowsAffectedErrors(t *testing.T) {
	t.Run("detection contribution", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		rowsErr := errors.New("forced contribution RowsAffected failure")
		installConflictWriteRowsAffectedErrorPool(uow, rowsErr)

		inserted, firstErr := uow.PutDetectionContribution(ctx, fixture.contribution)
		if firstErr == nil || inserted || !errors.Is(firstErr, rowsErr) ||
			!strings.Contains(firstErr.Error(), "affected rows") {
			t.Fatalf("PutDetectionContribution() = %v, %v, want sticky RowsAffected error", inserted, firstErr)
		}
		if commitErr := uow.Commit(); commitErr != firstErr {
			t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
		}
		assertConflictWriteContributionCount(t, database, fixture.contribution, 0)
	})

	t.Run("projection", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		projection := fixture.projection
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		rowsErr := errors.New("forced projection RowsAffected failure")
		installConflictWriteRowsAffectedErrorPool(uow, rowsErr)

		firstErr := uow.PutProjection(ctx, projection, fixture.now)
		if firstErr == nil || !errors.Is(firstErr, rowsErr) ||
			!strings.Contains(firstErr.Error(), "read affected rows") {
			t.Fatalf("PutProjection() error = %v, want sticky RowsAffected error", firstErr)
		}
		if commitErr := uow.Commit(); commitErr != firstErr {
			t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
		}
		assertConflictWriteProjectionCount(t, database, projection, 0)
	})
}

func TestGORMConflictWritesPreserveDuplicateReadbackNoRowsIdentity(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	pool := &conflictWriteNoOpExecPool{ConnPool: uow.transactionORM.Statement.ConnPool}
	uow.transactionORM.Config.ConnPool = pool
	uow.transactionORM.Statement.ConnPool = pool

	inserted, firstErr := uow.PutDetectionContribution(ctx, fixture.contribution)
	if firstErr == nil || inserted || !errors.Is(firstErr, sql.ErrNoRows) ||
		!strings.Contains(firstErr.Error(), "verify duplicate") {
		t.Fatalf("PutDetectionContribution() = %v, %v, want wrapped sql.ErrNoRows", inserted, firstErr)
	}
	if commitErr := uow.Commit(); commitErr != firstErr {
		t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
	}
	assertConflictWriteContributionCount(t, database, fixture.contribution, 0)
}

func TestGORMConflictWritesProjectionExactUpsert(t *testing.T) {
	database := openTestStore(t)
	fixture := prepareProcessingFixture(t, database)
	projection := fixture.projection
	effectiveUntil := fixture.now.In(time.FixedZone("projection-expiry", -7*60*60)).Add(time.Hour)
	projection.ActiveCount = 3
	projection.EffectiveUntil = &effectiveUntil
	projection.Revision = 7
	updatedAt := fixture.now.In(time.FixedZone("projection-updated", 5*60*60+30*60)).Add(time.Minute)
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	capture := registerConflictWriteCreateCapture(t, uow, "projection_exact")

	if err := uow.PutProjection(ctx, projection, updatedAt); err != nil {
		t.Fatalf("PutProjection(): %v", err)
	}
	if capture.calls != 1 || capture.table != "desired_ban_projections" || capture.rowsAffected != 1 {
		t.Fatalf(
			"GORM Create capture = calls %d table %q rows %d, want 1/desired_ban_projections/1",
			capture.calls, capture.table, capture.rowsAffected,
		)
	}
	wantSelects := []string{
		"node_id", "canonical_target", "state", "active_count", "effective_until_us",
		"target_projection_revision", "updated_at_us",
	}
	if !reflect.DeepEqual(capture.selects, wantSelects) {
		t.Fatalf("GORM Create selects = %v, want %v", capture.selects, wantSelects)
	}
	wantEffectiveUntilUS := effectiveUntil.UTC().UnixMicro()
	wantVars := []any{
		string(projection.NodeID), projection.CanonicalTarget.String(), "present", projection.ActiveCount,
		&wantEffectiveUntilUS, projection.Revision, updatedAt.UTC().UnixMicro(),
	}
	if !reflect.DeepEqual(capture.vars, wantVars) {
		t.Fatalf("GORM Create vars = %#v, want %#v", capture.vars, wantVars)
	}
	if count := conflictWriteProjectionCountTx(t, uow, projection); count != 1 {
		t.Fatalf("projection count inside UnitOfWork transaction = %d, want 1", count)
	}
	if err := uow.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
	assertConflictWriteProjectionCount(t, database, projection, 0)
}

func TestGORMConflictWritesProjectionStateAndNulls(t *testing.T) {
	tests := []struct {
		name               string
		state              core.BanProjectionState
		activeCount        uint64
		effectiveUntil     func(time.Time) *time.Time
		wantState          string
		wantEffectiveValid bool
	}{
		{name: "absent", state: core.BanProjectionAbsent, wantState: "absent"},
		{name: "present without expiry", state: core.BanProjectionPresent, activeCount: 2, wantState: "present"},
		{
			name: "present with expiry", state: core.BanProjectionPresent, activeCount: 2,
			effectiveUntil: func(now time.Time) *time.Time {
				value := now.In(time.FixedZone("projection-expiry", 8*60*60)).Add(time.Hour)
				return &value
			},
			wantState: "present", wantEffectiveValid: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			var effectiveUntil *time.Time
			if test.effectiveUntil != nil {
				effectiveUntil = test.effectiveUntil(fixture.now)
			}
			projection := core.DesiredBanProjection{
				NodeID: fixture.projection.NodeID, CanonicalTarget: fixture.projection.CanonicalTarget,
				State: test.state, ActiveCount: test.activeCount,
				EffectiveUntil: effectiveUntil, Revision: 1,
			}
			updatedAt := fixture.now.In(time.FixedZone("projection-updated", -4*60*60)).Add(time.Minute)
			commitConflictWriteProjection(t, database, projection, updatedAt)

			state, activeCount, effectiveUntilUS, revision, storedUpdatedAt := readConflictWriteProjection(
				t, database, projection,
			)
			if state != test.wantState || activeCount != int64(test.activeCount) ||
				effectiveUntilUS.Valid != test.wantEffectiveValid || revision != 1 ||
				storedUpdatedAt != updatedAt.UTC().UnixMicro() {
				t.Fatalf(
					"projection = state %q count %d effective %+v revision %d updated %d",
					state, activeCount, effectiveUntilUS, revision, storedUpdatedAt,
				)
			}
			if test.wantEffectiveValid && effectiveUntilUS.Int64 != projection.EffectiveUntil.UTC().UnixMicro() {
				t.Fatalf("effective_until_us = %d, want %d", effectiveUntilUS.Int64, projection.EffectiveUntil.UTC().UnixMicro())
			}
		})
	}
}

func TestGORMConflictWritesProjectionRevisionFence(t *testing.T) {
	tests := []struct {
		name              string
		candidateRevision core.TargetProjectionRevision
		candidateCount    uint64
		wantError         bool
		wantCreateRows    int64
		wantRevision      int64
		wantCount         int64
		wantCandidateTime bool
	}{
		{
			name: "same revision identical readback", candidateRevision: 2, candidateCount: 1,
			wantCreateRows: 0, wantRevision: 2, wantCount: 1,
		},
		{
			name: "same revision different is sticky", candidateRevision: 2, candidateCount: 2,
			wantError: true, wantCreateRows: 0, wantRevision: 2, wantCount: 1,
		},
		{
			name: "stale revision is sticky", candidateRevision: 1, candidateCount: 2,
			wantError: true, wantCreateRows: 0, wantRevision: 2, wantCount: 1,
		},
		{
			name: "higher revision updates", candidateRevision: 3, candidateCount: 2,
			wantCreateRows: 0, wantRevision: 3, wantCount: 2, wantCandidateTime: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			base := core.DesiredBanProjection{
				NodeID: fixture.projection.NodeID, CanonicalTarget: fixture.projection.CanonicalTarget,
				State: core.BanProjectionPresent, ActiveCount: 1, Revision: 2,
			}
			baseUpdatedAt := fixture.now.Add(time.Minute)
			commitConflictWriteProjection(t, database, base, baseUpdatedAt)

			candidate := base
			candidate.Revision = test.candidateRevision
			candidate.ActiveCount = test.candidateCount
			candidateUpdatedAt := fixture.now.Add(2 * time.Minute)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)
			capture := registerConflictWriteCreateCapture(
				t, uow, "projection_revision_"+strings.ReplaceAll(test.name, " ", "_"),
			)

			firstErr := uow.PutProjection(ctx, candidate, candidateUpdatedAt)
			if test.wantError {
				if firstErr == nil {
					t.Fatal("PutProjection() error = nil")
				}
				followup := base
				followup.Revision = 4
				if secondErr := uow.PutProjection(ctx, followup, candidateUpdatedAt); secondErr != firstErr {
					t.Fatalf("sticky PutProjection() error = %v, want original %v", secondErr, firstErr)
				}
				if capture.calls != 1 {
					t.Fatalf("GORM Create calls after sticky failure = %d, want 1", capture.calls)
				}
				if commitErr := uow.Commit(); commitErr != firstErr {
					t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
				}
			} else {
				if firstErr != nil {
					t.Fatalf("PutProjection(): %v", firstErr)
				}
				if err := uow.Commit(); err != nil {
					t.Fatalf("Commit(): %v", err)
				}
			}
			if capture.calls != 1 || capture.rowsAffected != test.wantCreateRows {
				t.Fatalf(
					"GORM Create capture = calls %d rows %d, want 1/%d",
					capture.calls, capture.rowsAffected, test.wantCreateRows,
				)
			}

			_, activeCount, _, revision, storedUpdatedAt := readConflictWriteProjection(t, database, base)
			wantUpdatedAt := baseUpdatedAt.UTC().UnixMicro()
			if test.wantCandidateTime {
				wantUpdatedAt = candidateUpdatedAt.UTC().UnixMicro()
			}
			if activeCount != test.wantCount || revision != test.wantRevision || storedUpdatedAt != wantUpdatedAt {
				t.Fatalf(
					"stored projection = count %d revision %d updated %d, want %d/%d/%d",
					activeCount, revision, storedUpdatedAt, test.wantCount, test.wantRevision, wantUpdatedAt,
				)
			}
		})
	}
}

func TestGORMConflictWritesProjectionSQLiteIntegerRange(t *testing.T) {
	t.Run("maximum signed values persist", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		projection := fixture.projection
		projection.ActiveCount = math.MaxInt64
		projection.Revision = core.TargetProjectionRevision(math.MaxInt64)
		commitConflictWriteProjection(t, database, projection, fixture.now)

		_, activeCount, _, revision, _ := readConflictWriteProjection(t, database, projection)
		if activeCount != math.MaxInt64 || revision != math.MaxInt64 {
			t.Fatalf("stored projection integers = count %d revision %d, want MaxInt64", activeCount, revision)
		}
	})

	limit := uint64(math.MaxInt64) + 1
	tests := []struct {
		name   string
		mutate func(*core.DesiredBanProjection)
	}{
		{name: "active count overflow", mutate: func(projection *core.DesiredBanProjection) {
			projection.ActiveCount = limit
		}},
		{name: "revision overflow", mutate: func(projection *core.DesiredBanProjection) {
			projection.Revision = core.TargetProjectionRevision(limit)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openTestStore(t)
			fixture := prepareProcessingFixture(t, database)
			projection := fixture.projection
			test.mutate(&projection)
			ctx := context.Background()
			uow, err := database.BeginProcessing(ctx)
			if err != nil {
				t.Fatalf("BeginProcessing(): %v", err)
			}
			cleanupOpenUnitOfWork(t, uow)

			firstErr := uow.PutProjection(ctx, projection, fixture.now)
			if firstErr == nil {
				t.Fatal("PutProjection() above SQLite INTEGER range error = nil")
			}
			if secondErr := uow.PutProjection(ctx, fixture.projection, fixture.now); secondErr != firstErr {
				t.Fatalf("sticky PutProjection() error = %v, want original %v", secondErr, firstErr)
			}
			if commitErr := uow.Commit(); commitErr != firstErr {
				t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
			}
			assertConflictWriteProjectionCount(t, database, projection, 0)
		})
	}
}

func TestGORMConflictWritesProjectionForeignKeyAndCancel(t *testing.T) {
	t.Run("node foreign key is sticky", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		projection := fixture.projection
		projection.NodeID = "11111111111111111111111111111111"
		ctx := context.Background()
		uow, err := database.BeginProcessing(ctx)
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)

		firstErr := uow.PutProjection(ctx, projection, fixture.now)
		if firstErr == nil {
			t.Fatal("PutProjection() without node identity error = nil")
		}
		if secondErr := uow.PutProjection(ctx, fixture.projection, fixture.now); secondErr != firstErr {
			t.Fatalf("sticky PutProjection() error = %v, want original %v", secondErr, firstErr)
		}
		if commitErr := uow.Commit(); commitErr != firstErr {
			t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
		}
		assertConflictWriteProjectionCount(t, database, projection, 0)
	})

	t.Run("canceled context is sticky", func(t *testing.T) {
		database := openTestStore(t)
		fixture := prepareProcessingFixture(t, database)
		uow, err := database.BeginProcessing(context.Background())
		if err != nil {
			t.Fatalf("BeginProcessing(): %v", err)
		}
		cleanupOpenUnitOfWork(t, uow)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		firstErr := uow.PutProjection(ctx, fixture.projection, fixture.now)
		if !errors.Is(firstErr, context.Canceled) {
			t.Fatalf("PutProjection() error = %v, want context.Canceled", firstErr)
		}
		if secondErr := uow.PutProjection(context.Background(), fixture.projection, fixture.now); secondErr != firstErr {
			t.Fatalf("sticky PutProjection() error = %v, want original %v", secondErr, firstErr)
		}
		if commitErr := uow.Commit(); commitErr != firstErr {
			t.Fatalf("Commit() error = %v, want original %v", commitErr, firstErr)
		}
		assertConflictWriteProjectionCount(t, database, fixture.projection, 0)
	})
}

func registerConflictWriteCreateCapture(
	t *testing.T,
	uow *UnitOfWork,
	suffix string,
) *conflictWriteCreateCapture {
	t.Helper()
	capture := &conflictWriteCreateCapture{}
	callbackName := "guard_wall:test_capture_conflict_write_" + suffix
	if err := uow.transactionORM.Callback().Create().After("gorm:create").Register(
		callbackName,
		func(tx *gorm.DB) {
			capture.calls++
			capture.table = tx.Statement.Table
			capture.selects = append([]string(nil), tx.Statement.Selects...)
			capture.sql = tx.Statement.SQL.String()
			capture.vars = append([]any(nil), tx.Statement.Vars...)
			capture.rowsAffected = tx.RowsAffected
		},
	); err != nil {
		t.Fatalf("register GORM create callback: %v", err)
	}
	t.Cleanup(func() {
		_ = uow.transactionORM.Callback().Create().Remove(callbackName)
	})
	return capture
}

func installConflictWriteRowsAffectedErrorPool(uow *UnitOfWork, rowsErr error) {
	pool := &conflictWriteRowsAffectedErrorPool{
		ConnPool: uow.transactionORM.Statement.ConnPool,
		err:      rowsErr,
	}
	uow.transactionORM.Config.ConnPool = pool
	uow.transactionORM.Statement.ConnPool = pool
}

func commitConflictWriteContribution(t *testing.T, database *Store, fixture processingFixture) {
	t.Helper()
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	inserted, err := uow.PutDetectionContribution(ctx, fixture.contribution)
	if err != nil || !inserted {
		t.Fatalf("PutDetectionContribution() = %v, %v, want true, nil", inserted, err)
	}
	if err := uow.PutReceipt(ctx, fixture.receipt); err != nil {
		t.Fatalf("PutReceipt(): %v", err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
}

func commitConflictWriteProjection(
	t *testing.T,
	database *Store,
	projection core.DesiredBanProjection,
	updatedAt time.Time,
) {
	t.Helper()
	ctx := context.Background()
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatalf("BeginProcessing(): %v", err)
	}
	cleanupOpenUnitOfWork(t, uow)
	if err := uow.PutProjection(ctx, projection, updatedAt); err != nil {
		t.Fatalf("PutProjection(): %v", err)
	}
	if err := uow.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
}

func conflictWriteAlternateDeliveryID(t *testing.T) core.DeliveryID {
	t.Helper()
	deliveryID, err := core.FileDeliveryID(processingSourceID, core.FilePosition{
		Generation:  processingGeneration,
		StartOffset: 10,
		EndOffset:   20,
	})
	if err != nil {
		t.Fatalf("FileDeliveryID(): %v", err)
	}
	return deliveryID
}

func conflictWriteContributionCountTx(
	t *testing.T,
	uow *UnitOfWork,
	contribution core.DetectionContribution,
) int {
	t.Helper()
	var count int
	if err := uow.tx.QueryRowContext(context.Background(), `
		SELECT count(*) FROM detection_contributions
		WHERE event_id = ? AND rule_id = ? AND rule_version = ?`,
		string(contribution.EventID), string(contribution.RuleID), string(contribution.RuleVersion),
	).Scan(&count); err != nil {
		t.Fatalf("count contribution in transaction: %v", err)
	}
	return count
}

func assertConflictWriteContributionCount(
	t *testing.T,
	database *Store,
	contribution core.DetectionContribution,
	want int,
) {
	t.Helper()
	var count int
	if err := database.db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM detection_contributions
		WHERE event_id = ? AND rule_id = ? AND rule_version = ?`,
		string(contribution.EventID), string(contribution.RuleID), string(contribution.RuleVersion),
	).Scan(&count); err != nil {
		t.Fatalf("count contribution: %v", err)
	}
	if count != want {
		t.Fatalf("contribution count = %d, want %d", count, want)
	}
}

func conflictWriteProjectionCountTx(
	t *testing.T,
	uow *UnitOfWork,
	projection core.DesiredBanProjection,
) int {
	t.Helper()
	var count int
	if err := uow.tx.QueryRowContext(context.Background(), `
		SELECT count(*) FROM desired_ban_projections
		WHERE node_id = ? AND canonical_target = ?`,
		string(projection.NodeID), projection.CanonicalTarget.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count projection in transaction: %v", err)
	}
	return count
}

func assertConflictWriteProjectionCount(
	t *testing.T,
	database *Store,
	projection core.DesiredBanProjection,
	want int,
) {
	t.Helper()
	var count int
	if err := database.db.QueryRowContext(context.Background(), `
		SELECT count(*) FROM desired_ban_projections
		WHERE node_id = ? AND canonical_target = ?`,
		string(projection.NodeID), projection.CanonicalTarget.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count projection: %v", err)
	}
	if count != want {
		t.Fatalf("projection count = %d, want %d", count, want)
	}
}

func readConflictWriteProjection(
	t *testing.T,
	database *Store,
	projection core.DesiredBanProjection,
) (string, int64, sql.NullInt64, int64, int64) {
	t.Helper()
	var (
		state            string
		activeCount      int64
		effectiveUntilUS sql.NullInt64
		revision         int64
		updatedAtUS      int64
	)
	if err := database.db.QueryRowContext(context.Background(), `
		SELECT state, active_count, effective_until_us, target_projection_revision, updated_at_us
		FROM desired_ban_projections
		WHERE node_id = ? AND canonical_target = ?`,
		string(projection.NodeID), projection.CanonicalTarget.String(),
	).Scan(&state, &activeCount, &effectiveUntilUS, &revision, &updatedAtUS); err != nil {
		t.Fatalf("read projection: %v", err)
	}
	return state, activeCount, effectiveUntilUS, revision, updatedAtUS
}
