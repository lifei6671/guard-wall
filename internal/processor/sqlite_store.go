package processor

import (
	"context"
	"errors"
	"fmt"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/store"
)

// SQLiteStoreAdapter connects Coordinator's package-private transaction ports
// to the Phase 1 SQLite Store.
type SQLiteStoreAdapter struct {
	database *store.Store
	commit   func(*store.UnitOfWork) error
}

// NewSQLiteStoreAdapter returns the production receipt/transaction adapter.
func NewSQLiteStoreAdapter(database *store.Store) *SQLiteStoreAdapter {
	return &SQLiteStoreAdapter{database: database, commit: func(unit *store.UnitOfWork) error {
		return unit.Commit()
	}}
}

func (s *SQLiteStoreAdapter) findReceipt(ctx context.Context, id core.DeliveryID) (core.ProcessingReceipt, bool, error) {
	if s == nil || s.database == nil {
		return core.ProcessingReceipt{}, false, fmt.Errorf("SQLite processing store is required")
	}
	return s.database.FindProcessingReceipt(ctx, id)
}

func (s *SQLiteStoreAdapter) beginProcessing(ctx context.Context) (processingUnitOfWork, error) {
	if s == nil || s.database == nil || s.commit == nil {
		return nil, fmt.Errorf("SQLite processing store is required")
	}
	unit, err := s.database.BeginProcessing(ctx)
	if err != nil {
		return nil, err
	}
	return &sqliteUnitOfWork{unit: unit, commitFn: s.commit}, nil
}

type sqliteUnitOfWork struct {
	unit     *store.UnitOfWork
	commitFn func(*store.UnitOfWork) error
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
	_, err := decision.RecordAutomaticInTransaction(ctx, u.unit, request)
	return err
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
