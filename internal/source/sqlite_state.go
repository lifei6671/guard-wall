package source

import (
	"context"
	"fmt"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

// SQLiteStateStore adapts the durable Store to Source checkpoint and file
// generation lifecycle ports.
type SQLiteStateStore struct {
	database *store.Store
	clock    func() time.Time
}

// NewSQLiteStateStore creates a Source state adapter backed by SQLite.
func NewSQLiteStateStore(database *store.Store) *SQLiteStateStore {
	return &SQLiteStateStore{database: database, clock: func() time.Time { return time.Now().UTC() }}
}

// LoadCheckpoint restores the last durable checkpoint for a Source.
func (s *SQLiteStateStore) LoadCheckpoint(ctx context.Context, sourceID core.SourceID) (store.SourceCheckpoint, bool, error) {
	if err := s.ready(); err != nil {
		return store.SourceCheckpoint{}, false, err
	}
	return s.database.LoadSourceCheckpoint(ctx, sourceID)
}

func (s *SQLiteStateStore) saveCheckpoint(ctx context.Context, sourceID core.SourceID, sequence core.DeliverySequence, position core.SourcePosition) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.database.AdvanceSourceCheckpoint(ctx, sourceID, sequence, position, s.clock())
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
	return s.database.SealFileGeneration(ctx, sourceID, generation, from, finalEOF, maxSequence, s.clock())
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

// RetireFileGeneration applies the checkpoint/reference safety barrier.
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
