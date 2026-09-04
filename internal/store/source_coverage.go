package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

// CoverageComplete reports a stable sealed prefix, not retirement eligibility.
func (g FileGeneration) CoverageComplete() bool {
	return g.State == FileGenerationSealed && g.DurableEndOffset != nil && g.CoverageSessionID != nil && g.FinalEOF != nil && *g.DurableEndOffset == *g.FinalEOF
}

// InitializeFileGenerationCoverage declares the reader's responsibility for all
// bytes starting at zero. It must precede delivery; it does not prove processing.
// Historical data may be initialized only after the caller verifies replayability.
func (s *Store) InitializeFileGenerationCoverage(ctx context.Context, session SourceSession, generation string) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if !isLowerHex128(generation) {
		return fmt.Errorf("initialize coverage: invalid generation")
	}
	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.tx.Rollback()
	if err := lockSourceSession(ctx, transaction.orm, session); err != nil {
		return err
	}
	persisted, found, err := loadFileGeneration(ctx, transaction.orm, session.SourceID(), generation)
	if err != nil {
		return err
	}
	if !found || persisted.State == FileGenerationRetired {
		return fmt.Errorf("%w: coverage generation unavailable", ErrSourcePositionMismatch)
	}
	if persisted.DurableEndOffset == nil {
		result := transaction.orm.WithContext(ctx).Model(&sourceFileGenerationRow{}).
			Where(map[string]any{SourceFileGenerationColumns.SourceID: string(session.SourceID()), SourceFileGenerationColumns.Generation: generation}).
			Updates(map[string]any{SourceFileGenerationColumns.DurableEndOffset: int64(0), SourceFileGenerationColumns.CoverageSessionID: string(session.ID())})
		if result.Error != nil {
			return fmt.Errorf("initialize coverage: %w", result.Error)
		}
	}
	return transaction.tx.Commit()
}

// AdvanceSourceCheckpointWithCoverage atomically saves every generation range
// released by the Source's contiguous completion tracker and its last position.
// Unknown commit results must be checked with LoadSourceCoverageState.
func (s *Store) AdvanceSourceCheckpointWithCoverage(ctx context.Context, session SourceSession, sequence core.DeliverySequence, position core.SourcePosition, ranges []core.FilePosition, persistedAt time.Time) error {
	return s.advanceSourceCheckpoint(ctx, session, sequence, position, ranges, true, persistedAt)
}

func advanceFileCoverage(ctx context.Context, orm *gorm.DB, session SourceSession, position core.SourcePosition, ranges []core.FilePosition, advancing bool) error {
	last, file := position.File()
	if !file || len(ranges) == 0 {
		return fmt.Errorf("%w: file coverage ranges required", ErrFileGenerationNotDurable)
	}
	lastCovered := false
	for _, span := range ranges {
		if !isLowerHex128(span.Generation) || span.StartOffset > span.EndOffset || span.EndOffset > math.MaxInt64 || span.DeviceID > math.MaxInt64 || span.Inode > math.MaxInt64 {
			return fmt.Errorf("%w: invalid coverage range", ErrSourcePositionMismatch)
		}
		generation, found, err := loadFileGeneration(ctx, orm, session.SourceID(), span.Generation)
		if err != nil {
			return err
		}
		if !found || generation.State == FileGenerationRetired || generation.DeviceID != span.DeviceID || generation.Inode != span.Inode {
			return fmt.Errorf("%w: coverage generation identity differs", ErrSourcePositionMismatch)
		}
		if generation.DurableEndOffset == nil || span.StartOffset > *generation.DurableEndOffset || (generation.FinalEOF != nil && span.EndOffset > *generation.FinalEOF) {
			return fmt.Errorf("%w: unknown prefix, gap or sealed boundary exceeded", ErrFileGenerationNotDurable)
		}
		if span.Generation == last.Generation && span.DeviceID == last.DeviceID && span.Inode == last.Inode && span.StartOffset <= last.StartOffset && span.EndOffset >= last.EndOffset {
			lastCovered = true
		}
		if span.EndOffset <= *generation.DurableEndOffset {
			continue
		}
		if !advancing {
			return fmt.Errorf("%w: equal sequence cannot extend coverage", ErrCheckpointRegression)
		}
		result := orm.WithContext(ctx).Model(&sourceFileGenerationRow{}).
			Where(map[string]any{SourceFileGenerationColumns.SourceID: string(session.SourceID()), SourceFileGenerationColumns.Generation: span.Generation}).
			Updates(map[string]any{SourceFileGenerationColumns.DurableEndOffset: int64(span.EndOffset), SourceFileGenerationColumns.CoverageSessionID: string(session.ID())})
		if result.Error != nil {
			return fmt.Errorf("advance generation coverage: %w", result.Error)
		}
	}
	if !lastCovered {
		return fmt.Errorf("%w: last checkpoint position absent from coverage", ErrFileGenerationNotDurable)
	}
	return nil
}

// SealFileGenerationWithSession freezes the reader's final range under current
// ownership. Previously established coverage retains its original session ID.
func (s *Store) SealFileGenerationWithSession(ctx context.Context, session SourceSession, generation string, from FileGenerationState, finalEOF uint64, maxSequence core.DeliverySequence, sealedAt time.Time) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if !isLowerHex128(generation) || sealedAt.IsZero() || finalEOF > math.MaxInt64 || uint64(maxSequence) > math.MaxInt64 {
		return fmt.Errorf("seal generation: invalid range or time")
	}
	if from != FileGenerationOpen && from != FileGenerationDraining {
		return fmt.Errorf("%w: %s -> sealed", ErrFileGenerationTransition, from)
	}
	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.tx.Rollback()
	if err := lockSourceSession(ctx, transaction.orm, session); err != nil {
		return err
	}
	if err := sealFileGeneration(ctx, transaction.orm, session.SourceID(), generation, from, finalEOF, maxSequence, sealedAt, true); err != nil {
		return err
	}
	return transaction.tx.Commit()
}

// LoadSourceCoverageState returns one read snapshot for recovery and uncertain
// commit confirmation, including every non-Retired generation, not only the last.
func (s *Store) LoadSourceCoverageState(ctx context.Context, sourceID core.SourceID) (SourceSessionID, SourceCheckpoint, bool, []FileGeneration, error) {
	if err := s.ready(ctx); err != nil {
		return "", SourceCheckpoint{}, false, nil, err
	}
	if sourceID == "" {
		return "", SourceCheckpoint{}, false, nil, fmt.Errorf("source id is required")
	}
	transaction, err := s.beginStoreORMTransaction(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", SourceCheckpoint{}, false, nil, err
	}
	defer transaction.tx.Rollback()
	var source sourceRow
	if err := transaction.orm.WithContext(ctx).Where(map[string]any{SourceColumns.SourceID: string(sourceID)}).Take(&source).Error; err != nil {
		return "", SourceCheckpoint{}, false, nil, err
	}
	checkpoint, found, err := loadSourceCheckpoint(ctx, transaction.orm, sourceID)
	if err != nil {
		return "", SourceCheckpoint{}, false, nil, err
	}
	generations, err := loadRecoverableFileGenerations(ctx, transaction.orm, sourceID)
	if err != nil {
		return "", SourceCheckpoint{}, false, nil, err
	}
	if err := transaction.tx.Commit(); err != nil {
		return "", SourceCheckpoint{}, false, nil, err
	}
	var active SourceSessionID
	if source.ActiveSessionID != nil {
		active = SourceSessionID(*source.ActiveSessionID)
	}
	return active, checkpoint, found, generations, nil
}

func lockSourceSession(ctx context.Context, orm *gorm.DB, session SourceSession) error {
	if session.SourceID() == "" || !isLowerHex128(string(session.ID())) {
		return fmt.Errorf("valid source session is required")
	}
	// 先获得写锁，再读取范围，避免 Begin 在校验与写入之间更换 owner。
	result := orm.WithContext(ctx).Model(&sourceRow{}).
		Where(map[string]any{SourceColumns.SourceID: string(session.SourceID()), SourceColumns.ActiveSessionID: string(session.ID())}).
		UpdateColumn(SourceColumns.ActiveSessionID, string(session.ID()))
	if result.Error != nil {
		return fmt.Errorf("source session CAS: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: source %q", ErrStaleSourceSession, session.SourceID())
	}
	return nil
}
