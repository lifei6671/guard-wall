package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

var (
	ErrCheckpointRegression     = errors.New("source checkpoint would regress")
	ErrFileGenerationTransition = errors.New("invalid file generation transition")
	ErrFileGenerationReferenced = errors.New("file generation is still referenced")
	ErrFileGenerationNotDurable = errors.New("file generation checkpoint has not safely advanced")
	ErrSourcePositionMismatch   = errors.New("source position does not match durable source identity")
)

// SourceKind is the stable storage value for a configured Source.
type SourceKind string

const (
	SourceKindFile     SourceKind = "file"
	SourceKindJournald SourceKind = "journald"
)

// SourceCheckpoint is the highest durable contiguous position for one Source.
type SourceCheckpoint struct {
	SourceID         core.SourceID
	DeliverySequence core.DeliverySequence
	Position         core.SourcePosition
	PersistedAt      time.Time
}

// FileGenerationState is the monotonic lifecycle stored for one file generation.
type FileGenerationState string

const (
	FileGenerationOpen     FileGenerationState = "open"
	FileGenerationDraining FileGenerationState = "draining"
	FileGenerationSealed   FileGenerationState = "sealed"
	FileGenerationRetired  FileGenerationState = "retired"
)

// FileGeneration is the durable identity and lifecycle of one observed file.
type FileGeneration struct {
	SourceID            core.SourceID
	Generation          string
	DeviceID            uint64
	Inode               uint64
	Path                string
	State               FileGenerationState
	ObservedSize        uint64
	FinalEOF            *uint64
	MaxDeliverySequence *core.DeliverySequence
	OpenedAt            time.Time
	DrainingAt          *time.Time
	SealedAt            *time.Time
	RetiredAt           *time.Time
}

// EnsureSource inserts the minimal Source identity required by the frozen M0 schema.
func (s *Store) EnsureSource(ctx context.Context, sourceID core.SourceID, nodeID core.NodeID, kind SourceKind, now time.Time) error {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("ensure source: %w", err)
	}
	if sourceID == "" || len(sourceID) > 128 {
		return fmt.Errorf("ensure source: source id is invalid")
	}
	if !isLowerHex128(string(nodeID)) {
		return fmt.Errorf("ensure source: node id must be 128-bit lowercase hex")
	}
	if kind != SourceKindFile && kind != SourceKindJournald {
		return fmt.Errorf("ensure source: unsupported kind %q", kind)
	}
	if now.IsZero() {
		return fmt.Errorf("ensure source: time is required")
	}

	stamp := now.UTC().UnixMicro()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sources(source_id, node_id, kind, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(source_id) DO NOTHING`, string(sourceID), string(nodeID), string(kind), stamp, stamp); err != nil {
		return fmt.Errorf("ensure source %q: insert: %w", sourceID, err)
	}
	var persistedNode, persistedKind string
	if err := s.db.QueryRowContext(ctx,
		"SELECT node_id, kind FROM sources WHERE source_id = ?", string(sourceID)).Scan(&persistedNode, &persistedKind); err != nil {
		return fmt.Errorf("ensure source %q: read back: %w", sourceID, err)
	}
	if persistedNode != string(nodeID) || persistedKind != string(kind) {
		return fmt.Errorf("ensure source %q: persisted identity differs", sourceID)
	}
	return nil
}

// FindProcessingReceipt reads one terminal receipt by its stable DeliveryID.
func (s *Store) FindProcessingReceipt(ctx context.Context, deliveryID core.DeliveryID) (core.ProcessingReceipt, bool, error) {
	if err := s.ready(ctx); err != nil {
		return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt: %w", err)
	}
	if !core.ValidDeliveryID(deliveryID) {
		return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt: delivery id is not canonical")
	}

	var (
		sourceID, positionKind, kind string
		generation, cursor           sql.NullString
		deviceID, inode              sql.NullInt64
		startOffset, endOffset       sql.NullInt64
		failureStage, failureCode    sql.NullString
		sanitizedError, action       sql.NullString
		failureOccurred, committed   sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT source_id, position_kind, generation, device_id, inode,
			start_offset, end_offset, journald_cursor, kind, failure_stage,
			failure_code, sanitized_error, terminal_action, failure_occurred_at_us,
			committed_at_us
		FROM processing_receipts
		WHERE delivery_id = ?`, string(deliveryID)).Scan(
		&sourceID, &positionKind, &generation, &deviceID, &inode,
		&startOffset, &endOffset, &cursor, &kind, &failureStage,
		&failureCode, &sanitizedError, &action, &failureOccurred, &committed,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ProcessingReceipt{}, false, nil
	}
	if err != nil {
		return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt %q: %w", deliveryID, err)
	}

	position, err := decodePosition(positionKind, generation, deviceID, inode, startOffset, endOffset, cursor)
	if err != nil {
		return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt %q: %w", deliveryID, err)
	}
	receipt := core.ProcessingReceipt{
		DeliveryID: deliveryID,
		SourceID:   core.SourceID(sourceID),
		Position:   position,
		Committed:  time.UnixMicro(committed.Int64).UTC(),
	}
	switch kind {
	case "success":
		receipt.Kind = core.ReceiptSuccess
	case "record_permanent":
		if !failureStage.Valid || !failureCode.Valid || !sanitizedError.Valid || !action.Valid || !failureOccurred.Valid {
			return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt %q: permanent failure is incomplete", deliveryID)
		}
		receipt.Kind = core.ReceiptRecordPermanent
		receipt.Failure = &core.PermanentFailure{
			Stage: failureStage.String, Code: failureCode.String,
			SanitizedError: sanitizedError.String, Action: action.String,
			OccurredAt: time.UnixMicro(failureOccurred.Int64).UTC(),
		}
	default:
		return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt %q: unsupported kind %q", deliveryID, kind)
	}
	if err := receipt.Validate(); err != nil {
		return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt %q: validate: %w", deliveryID, err)
	}
	return receipt, true, nil
}

// LoadSourceCheckpoint returns the durable Source checkpoint, when present.
func (s *Store) LoadSourceCheckpoint(ctx context.Context, sourceID core.SourceID) (SourceCheckpoint, bool, error) {
	if err := s.ready(ctx); err != nil {
		return SourceCheckpoint{}, false, fmt.Errorf("load source checkpoint: %w", err)
	}
	return loadSourceCheckpoint(ctx, s.db, sourceID)
}

// AdvanceSourceCheckpoint performs a monotonic sequence CAS. Equal sequence and
// position is idempotent; lower or conflicting sequence values are rejected.
func (s *Store) AdvanceSourceCheckpoint(ctx context.Context, sourceID core.SourceID, sequence core.DeliverySequence, position core.SourcePosition, persistedAt time.Time) error {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("advance source checkpoint: %w", err)
	}
	if sourceID == "" || sequence == 0 || uint64(sequence) > math.MaxInt64 {
		return fmt.Errorf("advance source checkpoint: source and positive SQLite-range sequence are required")
	}
	if persistedAt.IsZero() {
		return fmt.Errorf("advance source checkpoint: persisted time is required")
	}
	encoded, err := encodePosition(position)
	if err != nil {
		return fmt.Errorf("advance source checkpoint: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("advance source checkpoint: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := validateCheckpointPosition(ctx, tx, sourceID, position); err != nil {
		return fmt.Errorf("advance source checkpoint %q: %w", sourceID, err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO source_checkpoints(
			source_id, delivery_sequence, position_kind, generation, device_id, inode,
			start_offset, end_offset, journald_cursor, persisted_at_us
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET
			delivery_sequence = excluded.delivery_sequence,
			position_kind = excluded.position_kind,
			generation = excluded.generation,
			device_id = excluded.device_id,
			inode = excluded.inode,
			start_offset = excluded.start_offset,
			end_offset = excluded.end_offset,
			journald_cursor = excluded.journald_cursor,
			persisted_at_us = excluded.persisted_at_us
		WHERE excluded.delivery_sequence > source_checkpoints.delivery_sequence`,
		string(sourceID), int64(sequence), encoded.kind, encoded.generation, encoded.deviceID,
		encoded.inode, encoded.startOffset, encoded.endOffset, encoded.cursor,
		persistedAt.UTC().UnixMicro())
	if err != nil {
		return fmt.Errorf("advance source checkpoint %q: write: %w", sourceID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("advance source checkpoint %q: affected rows: %w", sourceID, err)
	}
	if affected == 0 {
		current, found, loadErr := loadSourceCheckpoint(ctx, tx, sourceID)
		if loadErr != nil {
			return fmt.Errorf("advance source checkpoint %q: verify CAS: %w", sourceID, loadErr)
		}
		if !found || current.DeliverySequence != sequence || current.Position != position {
			return fmt.Errorf("%w: source %q current sequence %d, attempted %d", ErrCheckpointRegression, sourceID, current.DeliverySequence, sequence)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("advance source checkpoint %q: commit: %w", sourceID, err)
	}
	committed = true
	return nil
}

// RegisterFileGeneration durably registers an immutable generation before use.
func (s *Store) RegisterFileGeneration(ctx context.Context, generation FileGeneration) error {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("register file generation: %w", err)
	}
	if err := validateNewFileGeneration(generation); err != nil {
		return fmt.Errorf("register file generation: %w", err)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO source_file_generations(
			source_id, generation, device_id, inode, path, state, observed_size, opened_at_us
		) VALUES (?, ?, ?, ?, ?, 'open', ?, ?)
		ON CONFLICT(source_id, generation) DO NOTHING`,
		string(generation.SourceID), generation.Generation, int64(generation.DeviceID),
		int64(generation.Inode), generation.Path, int64(generation.ObservedSize),
		generation.OpenedAt.UTC().UnixMicro())
	if err != nil {
		return fmt.Errorf("register file generation %q: %w", generation.Generation, err)
	}
	persisted, found, err := s.loadFileGeneration(ctx, s.db, generation.SourceID, generation.Generation)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("register file generation %q: read back missing", generation.Generation)
	}
	if persisted.SourceID != generation.SourceID || persisted.Generation != generation.Generation ||
		persisted.DeviceID != generation.DeviceID || persisted.Inode != generation.Inode ||
		persisted.Path != generation.Path || persisted.ObservedSize != generation.ObservedSize ||
		persisted.OpenedAt.UnixMicro() != generation.OpenedAt.UTC().UnixMicro() {
		return fmt.Errorf("register file generation %q: immutable identity differs", generation.Generation)
	}
	if persisted.State != FileGenerationOpen {
		return fmt.Errorf("%w: generation %q is already %s", ErrFileGenerationTransition, generation.Generation, persisted.State)
	}
	return nil
}

// LoadRecoverableFileGenerations restores all non-Retired generations.
func (s *Store) LoadRecoverableFileGenerations(ctx context.Context, sourceID core.SourceID) ([]FileGeneration, error) {
	if err := s.ready(ctx); err != nil {
		return nil, fmt.Errorf("load file generations: %w", err)
	}
	if sourceID == "" {
		return nil, fmt.Errorf("load file generations: source id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT generation, device_id, inode, path, state, observed_size,
			final_eof, max_delivery_sequence, opened_at_us,
			draining_at_us, sealed_at_us, retired_at_us
		FROM source_file_generations
		WHERE source_id = ? AND state <> 'retired'`, string(sourceID))
	if err != nil {
		return nil, fmt.Errorf("load file generations %q: %w", sourceID, err)
	}
	defer rows.Close()
	var generations []FileGeneration
	for rows.Next() {
		generation, err := scanFileGeneration(rows, sourceID)
		if err != nil {
			return nil, fmt.Errorf("load file generations %q: %w", sourceID, err)
		}
		generations = append(generations, generation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load file generations %q: rows: %w", sourceID, err)
	}
	sort.Slice(generations, func(left, right int) bool {
		if generations[left].OpenedAt.Equal(generations[right].OpenedAt) {
			return generations[left].Generation < generations[right].Generation
		}
		return generations[left].OpenedAt.Before(generations[right].OpenedAt)
	})
	return generations, nil
}

// AdvanceFileGeneration moves one Open generation to Draining.
func (s *Store) AdvanceFileGeneration(ctx context.Context, sourceID core.SourceID, generation string, from, to FileGenerationState, at time.Time) error {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("advance file generation: %w", err)
	}
	if sourceID == "" || !isLowerHex128(generation) || at.IsZero() {
		return fmt.Errorf("advance file generation: identity and time are required")
	}
	if from != FileGenerationOpen || to != FileGenerationDraining {
		return fmt.Errorf("%w: %s -> %s", ErrFileGenerationTransition, from, to)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE source_file_generations
		SET state = 'draining', draining_at_us = ?
		WHERE source_id = ? AND generation = ? AND state = 'open'
			AND opened_at_us <= ?`, at.UTC().UnixMicro(), string(sourceID), generation, at.UTC().UnixMicro())
	if err != nil {
		return fmt.Errorf("advance file generation %q: %w", generation, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("advance file generation %q: affected rows: %w", generation, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: generation %q is not in %s", ErrFileGenerationTransition, generation, from)
	}
	return nil
}

// SealFileGeneration freezes the final EOF and highest DeliverySequence that
// must be covered by a durable checkpoint before retirement.
func (s *Store) SealFileGeneration(ctx context.Context, sourceID core.SourceID, generation string, from FileGenerationState, finalEOF uint64, maxSequence core.DeliverySequence, sealedAt time.Time) error {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("seal file generation: %w", err)
	}
	if sourceID == "" || !isLowerHex128(generation) || sealedAt.IsZero() ||
		finalEOF > math.MaxInt64 || uint64(maxSequence) > math.MaxInt64 {
		return fmt.Errorf("seal file generation: identity, SQLite-range high-water, and time are required")
	}
	if from != FileGenerationOpen && from != FileGenerationDraining {
		return fmt.Errorf("%w: %s -> %s", ErrFileGenerationTransition, from, FileGenerationSealed)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE source_file_generations
		SET state = 'sealed', final_eof = ?, max_delivery_sequence = ?, sealed_at_us = ?
		WHERE source_id = ? AND generation = ? AND state = ?
			AND COALESCE(draining_at_us, opened_at_us) <= ?`,
		int64(finalEOF), int64(maxSequence), sealedAt.UTC().UnixMicro(),
		string(sourceID), generation, string(from), sealedAt.UTC().UnixMicro())
	if err != nil {
		return fmt.Errorf("seal file generation %q: %w", generation, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("seal file generation %q: affected rows: %w", generation, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: generation %q is not in %s or time regressed", ErrFileGenerationTransition, generation, from)
	}
	return nil
}

// RotateFileGeneration atomically drains the current Open generation and
// registers its Open replacement. A failed replacement insert restores old.
func (s *Store) RotateFileGeneration(ctx context.Context, sourceID core.SourceID, oldGeneration string, next FileGeneration, rotatedAt time.Time) error {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("rotate file generation: %w", err)
	}
	if sourceID == "" || !isLowerHex128(oldGeneration) || rotatedAt.IsZero() || next.SourceID != sourceID {
		return fmt.Errorf("rotate file generation: identity and time are required")
	}
	if err := validateNewFileGeneration(next); err != nil {
		return fmt.Errorf("rotate file generation: %w", err)
	}
	if next.OpenedAt.UTC().UnixMicro() != rotatedAt.UTC().UnixMicro() {
		return fmt.Errorf("rotate file generation: replacement opened time must equal rotation time")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rotate file generation: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	result, err := tx.ExecContext(ctx, `
		UPDATE source_file_generations
		SET state = 'draining', draining_at_us = ?
		WHERE source_id = ? AND generation = ? AND state = 'open'
			AND opened_at_us <= ?`, rotatedAt.UTC().UnixMicro(), string(sourceID), oldGeneration, rotatedAt.UTC().UnixMicro())
	if err != nil {
		return fmt.Errorf("rotate file generation %q: drain old: %w", oldGeneration, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rotate file generation %q: affected rows: %w", oldGeneration, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: generation %q is not Open or time regressed", ErrFileGenerationTransition, oldGeneration)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO source_file_generations(
			source_id, generation, device_id, inode, path, state, observed_size, opened_at_us
		) VALUES (?, ?, ?, ?, ?, 'open', ?, ?)`,
		string(next.SourceID), next.Generation, int64(next.DeviceID), int64(next.Inode),
		next.Path, int64(next.ObservedSize), next.OpenedAt.UTC().UnixMicro()); err != nil {
		return fmt.Errorf("rotate file generation %q: register replacement %q: %w", oldGeneration, next.Generation, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rotate file generation %q: commit: %w", oldGeneration, err)
	}
	committed = true
	return nil
}

// RetireFileGeneration changes Sealed to Retired only after the checkpoint has
// safely passed the generation and no terminal receipt still references it.
func (s *Store) RetireFileGeneration(ctx context.Context, sourceID core.SourceID, generation string, retiredAt time.Time) error {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("retire file generation: %w", err)
	}
	if sourceID == "" || !isLowerHex128(generation) || retiredAt.IsZero() {
		return fmt.Errorf("retire file generation: identity and time are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("retire file generation %q: begin: %w", generation, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	record, found, err := s.loadFileGeneration(ctx, tx, sourceID, generation)
	if err != nil {
		return err
	}
	if !found || record.State != FileGenerationSealed {
		return fmt.Errorf("%w: generation %q is not sealed", ErrFileGenerationTransition, generation)
	}
	var receiptReferences int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM processing_receipts
		WHERE source_id = ? AND generation = ?`, string(sourceID), generation).Scan(&receiptReferences); err != nil {
		return fmt.Errorf("retire file generation %q: count receipt references: %w", generation, err)
	}
	if receiptReferences != 0 {
		return fmt.Errorf("%w: generation %q has %d receipt references", ErrFileGenerationReferenced, generation, receiptReferences)
	}
	checkpoint, checkpointFound, err := loadSourceCheckpoint(ctx, tx, sourceID)
	if err != nil {
		return fmt.Errorf("retire file generation %q: load checkpoint: %w", generation, err)
	}
	if !checkpointFound {
		return fmt.Errorf("%w: generation %q", ErrFileGenerationNotDurable, generation)
	}
	if record.MaxDeliverySequence == nil || checkpoint.DeliverySequence < *record.MaxDeliverySequence {
		return fmt.Errorf("%w: generation %q", ErrFileGenerationNotDurable, generation)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE source_file_generations
		SET state = 'retired', retired_at_us = ?
		WHERE source_id = ? AND generation = ? AND state = 'sealed'
			AND sealed_at_us <= ?`, retiredAt.UTC().UnixMicro(), string(sourceID), generation, retiredAt.UTC().UnixMicro())
	if err != nil {
		return fmt.Errorf("retire file generation %q: update: %w", generation, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("retire file generation %q: affected rows: %w", generation, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: generation %q changed concurrently", ErrFileGenerationTransition, generation)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("retire file generation %q: commit: %w", generation, err)
	}
	committed = true
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store is closed")
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	return nil
}

func loadSourceCheckpoint(ctx context.Context, queryer queryRower, sourceID core.SourceID) (SourceCheckpoint, bool, error) {
	if sourceID == "" {
		return SourceCheckpoint{}, false, fmt.Errorf("source id is required")
	}
	var (
		sequence, persistedAt  int64
		positionKind           string
		generation, cursor     sql.NullString
		deviceID, inode        sql.NullInt64
		startOffset, endOffset sql.NullInt64
	)
	err := queryer.QueryRowContext(ctx, `
		SELECT delivery_sequence, position_kind, generation, device_id, inode,
			start_offset, end_offset, journald_cursor, persisted_at_us
		FROM source_checkpoints WHERE source_id = ?`, string(sourceID)).Scan(
		&sequence, &positionKind, &generation, &deviceID, &inode,
		&startOffset, &endOffset, &cursor, &persistedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceCheckpoint{}, false, nil
	}
	if err != nil {
		return SourceCheckpoint{}, false, err
	}
	position, err := decodePosition(positionKind, generation, deviceID, inode, startOffset, endOffset, cursor)
	if err != nil {
		return SourceCheckpoint{}, false, err
	}
	return SourceCheckpoint{
		SourceID: sourceID, DeliverySequence: core.DeliverySequence(sequence),
		Position: position, PersistedAt: time.UnixMicro(persistedAt).UTC(),
	}, true, nil
}

func (s *Store) loadFileGeneration(ctx context.Context, queryer queryRower, sourceID core.SourceID, generation string) (FileGeneration, bool, error) {
	row := queryer.QueryRowContext(ctx, `
		SELECT generation, device_id, inode, path, state, observed_size,
			final_eof, max_delivery_sequence, opened_at_us,
			draining_at_us, sealed_at_us, retired_at_us
		FROM source_file_generations
		WHERE source_id = ? AND generation = ?`, string(sourceID), generation)
	record, err := scanFileGeneration(row, sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return FileGeneration{}, false, nil
	}
	if err != nil {
		return FileGeneration{}, false, fmt.Errorf("load file generation %q: %w", generation, err)
	}
	return record, true, nil
}

func scanFileGeneration(scanner rowScanner, sourceID core.SourceID) (FileGeneration, error) {
	var (
		generation, path, state         string
		deviceID, inode, observedSize   int64
		finalEOF, maxSequence           sql.NullInt64
		openedAt                        int64
		drainingAt, sealedAt, retiredAt sql.NullInt64
	)
	if err := scanner.Scan(&generation, &deviceID, &inode, &path, &state, &observedSize,
		&finalEOF, &maxSequence,
		&openedAt, &drainingAt, &sealedAt, &retiredAt); err != nil {
		return FileGeneration{}, err
	}
	return FileGeneration{
		SourceID: sourceID, Generation: generation, DeviceID: uint64(deviceID),
		Inode: uint64(inode), Path: path, State: FileGenerationState(state),
		ObservedSize: uint64(observedSize), OpenedAt: time.UnixMicro(openedAt).UTC(),
		FinalEOF: nullableUint64(finalEOF), MaxDeliverySequence: nullableDeliverySequence(maxSequence),
		DrainingAt: nullableUnixTime(drainingAt), SealedAt: nullableUnixTime(sealedAt),
		RetiredAt: nullableUnixTime(retiredAt),
	}, nil
}

func decodePosition(kind string, generation sql.NullString, deviceID, inode, startOffset, endOffset sql.NullInt64, cursor sql.NullString) (core.SourcePosition, error) {
	switch kind {
	case "file":
		if !generation.Valid || !deviceID.Valid || !inode.Valid || !startOffset.Valid || !endOffset.Valid ||
			deviceID.Int64 < 0 || inode.Int64 < 0 || startOffset.Int64 < 0 || endOffset.Int64 < 0 {
			return core.SourcePosition{}, fmt.Errorf("stored file position is incomplete")
		}
		return core.NewFilePosition(core.FilePosition{
			Generation: generation.String, DeviceID: uint64(deviceID.Int64), Inode: uint64(inode.Int64),
			StartOffset: uint64(startOffset.Int64), EndOffset: uint64(endOffset.Int64),
		})
	case "journald":
		if !cursor.Valid {
			return core.SourcePosition{}, fmt.Errorf("stored journald position is incomplete")
		}
		return core.NewJournaldPosition(cursor.String)
	default:
		return core.SourcePosition{}, fmt.Errorf("unsupported stored position kind %q", kind)
	}
}

func nullableUnixTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.UnixMicro(value.Int64).UTC()
	return &result
}

func nullableUint64(value sql.NullInt64) *uint64 {
	if !value.Valid {
		return nil
	}
	result := uint64(value.Int64)
	return &result
}

func nullableDeliverySequence(value sql.NullInt64) *core.DeliverySequence {
	if !value.Valid {
		return nil
	}
	result := core.DeliverySequence(value.Int64)
	return &result
}

func validateNewFileGeneration(generation FileGeneration) error {
	if generation.SourceID == "" || !isLowerHex128(generation.Generation) ||
		generation.Path == "" || len(generation.Path) > 4096 {
		return fmt.Errorf("identity is invalid")
	}
	if generation.DeviceID > math.MaxInt64 || generation.Inode > math.MaxInt64 ||
		generation.ObservedSize > math.MaxInt64 {
		return fmt.Errorf("numeric field exceeds SQLite range")
	}
	if generation.OpenedAt.IsZero() || generation.OpenedAt.UTC().UnixMicro() <= 0 {
		return fmt.Errorf("opened time is required")
	}
	if generation.State != "" && generation.State != FileGenerationOpen {
		return fmt.Errorf("new generation must be open")
	}
	if generation.FinalEOF != nil || generation.MaxDeliverySequence != nil ||
		generation.DrainingAt != nil || generation.SealedAt != nil || generation.RetiredAt != nil {
		return fmt.Errorf("new generation cannot contain lifecycle high-water or terminal times")
	}
	return nil
}

func validateCheckpointPosition(ctx context.Context, tx *sql.Tx, sourceID core.SourceID, position core.SourcePosition) error {
	var sourceKind string
	if err := tx.QueryRowContext(ctx, "SELECT kind FROM sources WHERE source_id = ?", string(sourceID)).Scan(&sourceKind); err != nil {
		return fmt.Errorf("load source kind: %w", err)
	}
	if file, ok := position.File(); ok {
		if sourceKind != string(SourceKindFile) {
			return fmt.Errorf("%w: file position requires file source", ErrSourcePositionMismatch)
		}
		var deviceID, inode int64
		var state string
		if err := tx.QueryRowContext(ctx, `
			SELECT device_id, inode, state
			FROM source_file_generations
			WHERE source_id = ? AND generation = ?`, string(sourceID), file.Generation).Scan(&deviceID, &inode, &state); err != nil {
			return fmt.Errorf("%w: load file generation: %v", ErrSourcePositionMismatch, err)
		}
		if state == string(FileGenerationRetired) || deviceID != int64(file.DeviceID) || inode != int64(file.Inode) {
			return fmt.Errorf("%w: file generation identity or lifecycle differs", ErrSourcePositionMismatch)
		}
		return nil
	}
	if _, ok := position.Journald(); ok {
		if sourceKind != string(SourceKindJournald) {
			return fmt.Errorf("%w: journald position requires journald source", ErrSourcePositionMismatch)
		}
		return nil
	}
	return fmt.Errorf("%w: unsupported position", ErrSourcePositionMismatch)
}
