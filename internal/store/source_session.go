package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lifei6671/guard-wall/internal/core"
)

var (
	ErrSourceSessionConflict           = errors.New("source session compare-and-swap conflict")
	ErrStaleSourceSession              = errors.New("source session is no longer active")
	ErrSourceCheckpointCommitUncertain = errors.New("source checkpoint commit result is uncertain")
)

// SourceSessionID identifies one startup intent; it is not an ordered sequence.
type SourceSessionID string

// SourceSession binds checkpoint writes to a Source and its startup intent.
// A handle is not a process lock or proof that a previous worker has stopped.
type SourceSession struct {
	sourceID core.SourceID
	id       SourceSessionID
}

func (s SourceSession) SourceID() core.SourceID { return s.sourceID }
func (s SourceSession) ID() SourceSessionID     { return s.id }

// NewSourceSessionID creates a fresh 128-bit identity for one actual startup.
// Retain it across uncertain Begin confirmation; never reuse it after a restart.
func NewSourceSessionID() (SourceSessionID, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("new source session id: %w", err)
	}
	return SourceSessionID(hex.EncodeToString(value[:])), nil
}

// BeginSourceSession atomically replaces expectedActive and reads the stable
// recovery checkpoint. New sessions start at sequence 1, not checkpoint+1.
// Callers must establish exclusive worker ownership before Begin; this CAS
// grants neither takeover permission nor a liveness guarantee.
// A commit error is an unknown result: use ConfirmSourceSession before work.
func (s *Store) BeginSourceSession(ctx context.Context, sourceID core.SourceID, expectedActive, newID SourceSessionID) (SourceSession, SourceCheckpoint, bool, error) {
	if err := s.ready(ctx); err != nil {
		return SourceSession{}, SourceCheckpoint{}, false, err
	}
	if sourceID == "" || !isLowerHex128(string(newID)) || (expectedActive != "" && !isLowerHex128(string(expectedActive))) || newID == expectedActive {
		return SourceSession{}, SourceCheckpoint{}, false, fmt.Errorf("begin source session: valid Source, expected identity and fresh identity are required")
	}
	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return SourceSession{}, SourceCheckpoint{}, false, err
	}
	defer transaction.tx.Rollback()
	var expected any
	if expectedActive != "" {
		expected = string(expectedActive)
	}
	result := transaction.orm.WithContext(ctx).Model(&sourceRow{}).Where(map[string]any{
		SourceColumns.SourceID: string(sourceID), SourceColumns.ActiveSessionID: expected,
	}).UpdateColumn(SourceColumns.ActiveSessionID, string(newID))
	if result.Error != nil {
		return SourceSession{}, SourceCheckpoint{}, false, fmt.Errorf("begin source session: CAS: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return SourceSession{}, SourceCheckpoint{}, false, fmt.Errorf("%w: source %q", ErrSourceSessionConflict, sourceID)
	}
	checkpoint, found, err := loadSourceCheckpoint(ctx, transaction.orm, sourceID)
	if err != nil {
		return SourceSession{}, SourceCheckpoint{}, false, fmt.Errorf("begin source session: recovery checkpoint: %w", err)
	}
	if err := transaction.tx.Commit(); err != nil {
		return SourceSession{}, SourceCheckpoint{}, false, fmt.Errorf("begin source session: commit result: %w", err)
	}
	return SourceSession{sourceID: sourceID, id: newID}, checkpoint, found, nil
}

// LoadSourceSessionState reads active identity and recovery checkpoint in one
// snapshot. A historical checkpoint may belong to a different or empty session.
func (s *Store) LoadSourceSessionState(ctx context.Context, sourceID core.SourceID) (SourceSessionID, SourceCheckpoint, bool, error) {
	if err := s.ready(ctx); err != nil {
		return "", SourceCheckpoint{}, false, err
	}
	if sourceID == "" {
		return "", SourceCheckpoint{}, false, fmt.Errorf("load source session: source id is required")
	}
	transaction, err := s.beginStoreORMTransaction(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", SourceCheckpoint{}, false, err
	}
	defer transaction.tx.Rollback()
	var row sourceRow
	if err := transaction.orm.WithContext(ctx).Where(map[string]any{SourceColumns.SourceID: string(sourceID)}).Take(&row).Error; err != nil {
		return "", SourceCheckpoint{}, false, fmt.Errorf("load source session: source: %w", err)
	}
	checkpoint, found, err := loadSourceCheckpoint(ctx, transaction.orm, sourceID)
	if err != nil {
		return "", SourceCheckpoint{}, false, fmt.Errorf("load source session: checkpoint: %w", err)
	}
	if err := transaction.tx.Commit(); err != nil {
		return "", SourceCheckpoint{}, false, fmt.Errorf("load source session: finish snapshot: %w", err)
	}
	var active SourceSessionID
	if row.ActiveSessionID != nil {
		active = SourceSessionID(*row.ActiveSessionID)
	}
	return active, checkpoint, found, nil
}

// ConfirmSourceSession recovers only the same startup intent after an uncertain
// Begin. It does not create a session or reset a live caller's sequence.
func (s *Store) ConfirmSourceSession(ctx context.Context, sourceID core.SourceID, intendedID SourceSessionID) (SourceSession, SourceCheckpoint, bool, error) {
	if !isLowerHex128(string(intendedID)) {
		return SourceSession{}, SourceCheckpoint{}, false, fmt.Errorf("confirm source session: valid intended identity is required")
	}
	active, checkpoint, found, err := s.LoadSourceSessionState(ctx, sourceID)
	if err != nil {
		return SourceSession{}, SourceCheckpoint{}, false, err
	}
	if active != intendedID {
		return SourceSession{}, SourceCheckpoint{}, false, fmt.Errorf("%w: source %q", ErrSourceSessionConflict, sourceID)
	}
	return SourceSession{sourceID: sourceID, id: intendedID}, checkpoint, found, nil
}
