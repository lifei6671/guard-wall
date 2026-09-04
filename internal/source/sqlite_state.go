package source

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

// SQLiteStateStore adapts the durable Store to Source checkpoint and file
// generation lifecycle ports.
type SQLiteStateStore struct {
	database *store.Store
	session  store.SourceSession
	clock    func() time.Time
}

// NewSQLiteStateStore binds checkpoint writes to the session established by BeginSourceSession.
// A restarted Source must begin a new session and create its tracker from sequence 1.
func NewSQLiteStateStore(database *store.Store, session store.SourceSession) *SQLiteStateStore {
	return &SQLiteStateStore{database: database, session: session, clock: func() time.Time { return time.Now().UTC() }}
}

// LoadCheckpoint restores the last durable checkpoint for a Source.
func (s *SQLiteStateStore) LoadCheckpoint(ctx context.Context, sourceID core.SourceID) (store.SourceCheckpoint, bool, error) {
	if err := s.ready(); err != nil {
		return store.SourceCheckpoint{}, false, err
	}
	return s.database.LoadSourceCheckpoint(ctx, sourceID)
}

func (s *SQLiteStateStore) saveCheckpoint(ctx context.Context, sourceID core.SourceID, sequence core.DeliverySequence, position core.SourcePosition, ranges ...core.FilePosition) error {
	if err := s.ready(); err != nil {
		return err
	}
	if sourceID != s.session.SourceID() {
		return fmt.Errorf("checkpoint source does not match bound session")
	}
	var err error
	if _, journal := position.Journald(); journal && len(ranges) == 0 {
		err = s.database.AdvanceSourceCheckpoint(ctx, s.session, sequence, position, s.clock())
	} else {
		err = s.database.AdvanceSourceCheckpointWithCoverage(ctx, s.session, sequence, position, ranges, s.clock())
	}
	if !errors.Is(err, store.ErrSourceCheckpointCommitUncertain) {
		return err
	}
	// 仅提交结果未知时读回；验证失败或旧session错误不能被已存在的数据掩盖。
	confirmed, readErr := s.checkpointConfirmed(ctx, sequence, position, ranges)
	if readErr != nil {
		return errors.Join(err, fmt.Errorf("confirm source coverage: %w", readErr))
	}
	if !confirmed {
		return err
	}
	return nil
}

// checkpointConfirmed 以一个持久快照核对整批候选，不单独猜测某个 UPDATE 的结果。
func (s *SQLiteStateStore) checkpointConfirmed(ctx context.Context, sequence core.DeliverySequence, position core.SourcePosition, ranges []core.FilePosition) (bool, error) {
	sourceID := s.session.SourceID()
	active, checkpoint, found, generations, readErr := s.database.LoadSourceCoverageState(ctx, sourceID)
	if readErr != nil {
		return false, readErr
	}
	if active != s.session.ID() || !found || checkpoint.SessionID != s.session.ID() || checkpoint.DeliverySequence != sequence || checkpoint.Position != position {
		return false, nil
	}
	for _, span := range ranges {
		confirmed := false
		for _, generation := range generations {
			if generation.Generation == span.Generation && generation.DeviceID == span.DeviceID && generation.Inode == span.Inode && generation.DurableEndOffset != nil && *generation.DurableEndOffset >= span.EndOffset {
				confirmed = true
				break
			}
		}
		if !confirmed {
			return false, nil
		}
	}
	return true, nil
}

// InitializeFileGenerationCoverage 声明该代际从0完整读取；必须在首条投递前调用。
// 历史未知代际须由调用方先确认原字节、framing及幂等记录仍可用。
func (s *SQLiteStateStore) InitializeFileGenerationCoverage(ctx context.Context, generation string) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.database.InitializeFileGenerationCoverage(ctx, s.session, generation)
}

// RegisterFileGeneration durably establishes a generation before its first delivery.
func (s *SQLiteStateStore) RegisterFileGeneration(ctx context.Context, generation store.FileGeneration) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.database.RegisterFileGeneration(ctx, generation)
}

// RecoverFileGenerations returns every non-Retired generation after restart.
func (s *SQLiteStateStore) RecoverFileGenerations(ctx context.Context, sourceID core.SourceID) ([]store.FileGeneration, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.database.LoadRecoverableFileGenerations(ctx, sourceID)
}

// AdvanceFileGeneration performs one legal monotonic lifecycle CAS.
func (s *SQLiteStateStore) AdvanceFileGeneration(ctx context.Context, sourceID core.SourceID, generation string, from, to store.FileGenerationState) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.database.AdvanceFileGeneration(ctx, sourceID, generation, from, to, s.clock())
}

// SealFileGeneration freezes the final EOF and delivery high-water.
func (s *SQLiteStateStore) SealFileGeneration(ctx context.Context, sourceID core.SourceID, generation string, from store.FileGenerationState, finalEOF uint64, maxSequence core.DeliverySequence) error {
	if err := s.ready(); err != nil {
		return err
	}
	if sourceID != s.session.SourceID() {
		return fmt.Errorf("generation source does not match bound session")
	}
	return s.database.SealFileGenerationWithSession(ctx, s.session, generation, from, finalEOF, maxSequence, s.clock())
}

// RotateFileGeneration atomically drains the old generation and opens next.
func (s *SQLiteStateStore) RotateFileGeneration(ctx context.Context, sourceID core.SourceID, oldGeneration string, next store.FileGeneration) error {
	if err := s.ready(); err != nil {
		return err
	}
	now := s.clock()
	next.OpenedAt = now
	return s.database.RotateFileGeneration(ctx, sourceID, oldGeneration, next, now)
}

// RetireFileGeneration retires a fully covered sealed generation after receipt
// and recovery-checkpoint protection checks; it does not delete stored rows.
func (s *SQLiteStateStore) RetireFileGeneration(ctx context.Context, sourceID core.SourceID, generation string) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.database.RetireFileGeneration(ctx, sourceID, generation, s.clock())
}

func (s *SQLiteStateStore) ready() error {
	if s == nil || s.database == nil || s.clock == nil {
		return fmt.Errorf("SQLite source state store is required")
	}
	return nil
}
