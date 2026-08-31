package processor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/store"
)

// SQLiteStoreAdapter connects Coordinator's package-private transaction ports
// to the Phase 1 SQLite Store.
type SQLiteStoreAdapter struct {
	database  *store.Store
	finalizer *decision.DesiredStateFinalizer
	wake      decision.TargetWakeSink
	commit    func(*store.UnitOfWork) error
}

// NewSQLiteStoreAdapter returns the base receipt/transaction adapter. A
// processing plan that can emit Automatic Decisions must instead use
// NewEnforcingSQLiteStoreAdapter so it cannot bypass Desired-state finalization.
func NewSQLiteStoreAdapter(database *store.Store) *SQLiteStoreAdapter {
	return &SQLiteStoreAdapter{database: database, commit: func(unit *store.UnitOfWork) error {
		return unit.Commit()
	}}
}

// NewEnforcingSQLiteStoreAdapter constructs the processing adapter that also
// finalizes Automatic Decision Target intents and emits confirmed-commit wakes.
func NewEnforcingSQLiteStoreAdapter(
	database *store.Store,
	finalizer *decision.DesiredStateFinalizer,
	wake decision.TargetWakeSink,
) (*SQLiteStoreAdapter, error) {
	if database == nil {
		return nil, fmt.Errorf("SQLite processing store is required")
	}
	if finalizer == nil {
		return nil, fmt.Errorf("desired state finalizer is required")
	}
	if wake == nil {
		return nil, fmt.Errorf("target wake sink is required")
	}
	adapter := NewSQLiteStoreAdapter(database)
	adapter.finalizer = finalizer
	adapter.wake = wake
	return adapter, nil
}

func (s *SQLiteStoreAdapter) findReceipt(ctx context.Context, id core.DeliveryID) (core.ProcessingReceipt, bool, error) {
	if s == nil || s.database == nil {
		return core.ProcessingReceipt{}, false, fmt.Errorf("SQLite processing store is required")
	}
	return s.database.FindProcessingReceipt(ctx, id)
}

func (s *SQLiteStoreAdapter) notifyReceiptReplay(ctx context.Context) error {
	if s == nil || s.database == nil {
		return fmt.Errorf("SQLite processing store is required")
	}
	if s.wake == nil {
		return nil
	}
	changes, err := s.database.PendingTargetEnforcementChanges(ctx)
	if err != nil {
		return err
	}
	return decision.WakeCommittedTargets(ctx, s.wake, changes)
}

func (s *SQLiteStoreAdapter) beginProcessing(ctx context.Context) (processingUnitOfWork, error) {
	if s == nil || s.database == nil || s.commit == nil {
		return nil, fmt.Errorf("SQLite processing store is required")
	}
	unit, err := s.database.BeginProcessing(ctx)
	if err != nil {
		return nil, err
	}
	return &sqliteUnitOfWork{
		unit: unit, finalizer: s.finalizer, wake: s.wake, commitFn: s.commit,
	}, nil
}

type sqliteUnitOfWork struct {
	unit          *store.UnitOfWork
	finalizer     *decision.DesiredStateFinalizer
	wake          decision.TargetWakeSink
	commitFn      func(*store.UnitOfWork) error
	projections   []core.DesiredBanProjection
	changes       []decision.TargetEnforcementChange
	lastUpdatedAt time.Time
}

func (u *sqliteUnitOfWork) writeParserOutcome(ctx context.Context, outcome core.ParserTerminalOutcome) error {
	return u.unit.PutParserOutcome(ctx, outcome)
}

func (u *sqliteUnitOfWork) writeDetectionOutcome(ctx context.Context, outcome core.DetectionTerminalOutcome) error {
	return u.unit.PutDetectionOutcome(ctx, outcome)
}

func (u *sqliteUnitOfWork) writeDetectionContribution(
	ctx context.Context,
	contribution core.DetectionContribution,
) (bool, error) {
	return u.unit.PutDetectionContribution(ctx, contribution)
}

func (u *sqliteUnitOfWork) writeAlert(ctx context.Context, alert core.Alert) error {
	return u.unit.PutAlert(ctx, alert)
}

func (u *sqliteUnitOfWork) recordAutomaticDecision(ctx context.Context, request decision.AutomaticRequest) error {
	result, err := decision.RecordAutomaticInTransaction(ctx, u.unit, request)
	if err != nil {
		return err
	}
	if result.Projection != nil {
		u.projections = append(u.projections, *result.Projection)
		if request.TriggeredAt.After(u.lastUpdatedAt) {
			u.lastUpdatedAt = request.TriggeredAt
		}
	}
	return nil
}

func (u *sqliteUnitOfWork) finalizeDesiredState(ctx context.Context) error {
	if len(u.projections) == 0 {
		return nil
	}
	if u.finalizer == nil || u.wake == nil {
		return fmt.Errorf("automatic Decision desired-state dependencies are required")
	}
	changes, err := u.finalizer.FinalizeTargets(ctx, u.unit, u.projections, u.lastUpdatedAt)
	if err != nil {
		return err
	}
	u.changes = changes
	return nil
}

func (u *sqliteUnitOfWork) notifyCommitted(ctx context.Context) error {
	return decision.WakeCommittedTargets(ctx, u.wake, u.changes)
}

func (u *sqliteUnitOfWork) writeCriticalAudit(ctx context.Context, audit store.CriticalAudit) error {
	return u.unit.AppendCriticalAudit(ctx, audit)
}

func (u *sqliteUnitOfWork) writeReceipt(ctx context.Context, receipt core.ProcessingReceipt) error {
	return u.unit.PutReceipt(ctx, receipt)
}

func (u *sqliteUnitOfWork) commit(ctx context.Context) (commitState, error) {
	if ctx == nil {
		if err := u.unit.Rollback(); err != nil {
			return commitRejected, errors.Join(fmt.Errorf("commit context is required"), err)
		}
		return commitRejected, fmt.Errorf("commit context is required")
	}
	if err := ctx.Err(); err != nil {
		if rollbackErr := u.unit.Rollback(); rollbackErr != nil {
			return commitRejected, errors.Join(err, fmt.Errorf("rollback canceled commit: %w", rollbackErr))
		}
		return commitRejected, err
	}
	if err := u.commitFn(u.unit); err != nil {
		// database/sql cannot prove that a failed Commit was rejected before the
		// durability point. Map it conservatively to Unknown and force the
		// Coordinator's independent receipt read-back.
		return commitUnknown, err
	}
	return commitConfirmed, nil
}

func (u *sqliteUnitOfWork) rollback() error {
	return u.unit.Rollback()
}
