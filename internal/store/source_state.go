package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	result := s.orm.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: SourceColumns.SourceID}},
			DoNothing: true,
		}).
		Create(&sourceRow{
			SourceID: string(sourceID), NodeID: string(nodeID), Kind: string(kind),
			CreatedAtUS: stamp, UpdatedAtUS: stamp,
		})
	if result.Error != nil {
		return fmt.Errorf("ensure source %q: insert: %w", sourceID, result.Error)
	}
	var persisted sourceRow
	readback := s.orm.WithContext(ctx).
		Where(map[string]any{SourceColumns.SourceID: string(sourceID)}).
		Take(&persisted)
	if readback.Error != nil {
		return fmt.Errorf("ensure source %q: read back: %w", sourceID, readback.Error)
	}
	if persisted.NodeID != string(nodeID) || persisted.Kind != string(kind) {
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

	var row processingReceiptRow
	result := s.orm.WithContext(ctx).
		Where(map[string]any{ProcessingReceiptColumns.DeliveryID: string(deliveryID)}).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return core.ProcessingReceipt{}, false, nil
	}
	if result.Error != nil {
		return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt %q: %w", deliveryID, result.Error)
	}

	position, err := decodePosition(row.PositionKind, row.Generation, row.DeviceID, row.Inode, row.StartOffset, row.EndOffset, row.JournaldCursor)
	if err != nil {
		return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt %q: %w", deliveryID, err)
	}
	receipt := core.ProcessingReceipt{
		DeliveryID: deliveryID,
		SourceID:   core.SourceID(row.SourceID),
		Position:   position,
		Committed:  time.UnixMicro(row.CommittedAtUS).UTC(),
	}
	switch row.Kind {
	case "success":
		receipt.Kind = core.ReceiptSuccess
	case "record_permanent":
		if row.FailureStage == nil || row.FailureCode == nil || row.SanitizedError == nil || row.TerminalAction == nil || row.FailureOccurredAtUS == nil {
			return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt %q: permanent failure is incomplete", deliveryID)
		}
		receipt.Kind = core.ReceiptRecordPermanent
		receipt.Failure = &core.PermanentFailure{
			Stage: *row.FailureStage, Code: *row.FailureCode,
			SanitizedError: *row.SanitizedError, Action: *row.TerminalAction,
			OccurredAt: time.UnixMicro(*row.FailureOccurredAtUS).UTC(),
		}
	default:
		return core.ProcessingReceipt{}, false, fmt.Errorf("find processing receipt %q: unsupported kind %q", deliveryID, row.Kind)
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
	return loadSourceCheckpoint(ctx, s.orm.WithContext(ctx), sourceID)
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

	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return fmt.Errorf("advance source checkpoint: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.tx.Rollback()
		}
	}()
	orm := transaction.orm.WithContext(ctx)
	if err := validateCheckpointPosition(ctx, orm, sourceID, position); err != nil {
		return fmt.Errorf("advance source checkpoint %q: %w", sourceID, err)
	}
	row := sourceCheckpointRow{
		SourceID: string(sourceID), DeliverySequence: int64(sequence), PositionKind: encoded.kind,
		Generation: optionalString(encoded.generation), DeviceID: optionalInt64(encoded.deviceID),
		Inode: optionalInt64(encoded.inode), StartOffset: optionalInt64(encoded.startOffset),
		EndOffset: optionalInt64(encoded.endOffset), JournaldCursor: optionalString(encoded.cursor),
		PersistedAtUS: persistedAt.UTC().UnixMicro(),
	}
	result := orm.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: SourceCheckpointColumns.SourceID}},
		DoUpdates: clause.AssignmentColumns([]string{
			SourceCheckpointColumns.DeliverySequence, SourceCheckpointColumns.PositionKind,
			SourceCheckpointColumns.Generation, SourceCheckpointColumns.DeviceID,
			SourceCheckpointColumns.Inode, SourceCheckpointColumns.StartOffset,
			SourceCheckpointColumns.EndOffset, SourceCheckpointColumns.JournaldCursor,
			SourceCheckpointColumns.PersistedAtUS,
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Gt{
				Column: clause.Column{Table: "excluded", Name: SourceCheckpointColumns.DeliverySequence},
				Value:  clause.Column{Table: "source_checkpoints", Name: SourceCheckpointColumns.DeliverySequence},
			},
		}},
	}).Create(&row)
	if result.Error != nil {
		return fmt.Errorf("advance source checkpoint %q: write: %w", sourceID, result.Error)
	}
	if result.RowsAffected == 0 {
		current, found, loadErr := loadSourceCheckpoint(ctx, orm, sourceID)
		if loadErr != nil {
			return fmt.Errorf("advance source checkpoint %q: verify CAS: %w", sourceID, loadErr)
		}
		if !found || current.DeliverySequence != sequence || current.Position != position {
			return fmt.Errorf("%w: source %q current sequence %d, attempted %d", ErrCheckpointRegression, sourceID, current.DeliverySequence, sequence)
		}
	}
	if err := transaction.tx.Commit(); err != nil {
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

	row := newSourceFileGenerationRow(generation)
	result := s.orm.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: SourceFileGenerationColumns.SourceID}, {Name: SourceFileGenerationColumns.Generation}},
			DoNothing: true,
		}).
		Create(&row)
	if result.Error != nil {
		return fmt.Errorf("register file generation %q: %w", generation.Generation, result.Error)
	}
	persisted, found, err := loadFileGeneration(ctx, s.orm.WithContext(ctx), generation.SourceID, generation.Generation)
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
	var rows []sourceFileGenerationRow
	result := s.orm.WithContext(ctx).
		Where(map[string]any{SourceFileGenerationColumns.SourceID: string(sourceID)}).
		Where(clause.Neq{Column: clause.Column{Name: SourceFileGenerationColumns.State}, Value: string(FileGenerationRetired)}).
		Find(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("load file generations %q: %w", sourceID, result.Error)
	}
	generations := make([]FileGeneration, 0, len(rows))
	for _, row := range rows {
		generations = append(generations, fileGenerationFromRow(row))
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
	result := s.orm.WithContext(ctx).
		Model(&sourceFileGenerationRow{}).
		Where(map[string]any{
			SourceFileGenerationColumns.SourceID:   string(sourceID),
			SourceFileGenerationColumns.Generation: generation,
			SourceFileGenerationColumns.State:      string(FileGenerationOpen),
		}).
		Where(clause.Lte{Column: clause.Column{Name: SourceFileGenerationColumns.OpenedAtUS}, Value: at.UTC().UnixMicro()}).
		Updates(map[string]any{SourceFileGenerationColumns.State: string(FileGenerationDraining), SourceFileGenerationColumns.DrainingAtUS: at.UTC().UnixMicro()})
	if result.Error != nil {
		return fmt.Errorf("advance file generation %q: %w", generation, result.Error)
	}
	if result.RowsAffected != 1 {
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
	query := s.orm.WithContext(ctx).
		Model(&sourceFileGenerationRow{}).
		Where(map[string]any{
			SourceFileGenerationColumns.SourceID:   string(sourceID),
			SourceFileGenerationColumns.Generation: generation,
			SourceFileGenerationColumns.State:      string(from),
		})
	if from == FileGenerationDraining {
		query = query.Where(clause.Lte{
			Column: clause.Column{Name: SourceFileGenerationColumns.DrainingAtUS}, Value: sealedAt.UTC().UnixMicro(),
		})
	} else {
		query = query.Where(clause.Lte{
			Column: clause.Column{Name: SourceFileGenerationColumns.OpenedAtUS}, Value: sealedAt.UTC().UnixMicro(),
		})
	}
	result := query.Updates(map[string]any{
		SourceFileGenerationColumns.State:               string(FileGenerationSealed),
		SourceFileGenerationColumns.FinalEOF:            int64(finalEOF),
		SourceFileGenerationColumns.MaxDeliverySequence: int64(maxSequence),
		SourceFileGenerationColumns.SealedAtUS:          sealedAt.UTC().UnixMicro(),
	})
	if result.Error != nil {
		return fmt.Errorf("seal file generation %q: %w", generation, result.Error)
	}
	if result.RowsAffected != 1 {
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
	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return fmt.Errorf("rotate file generation: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.tx.Rollback()
		}
	}()
	orm := transaction.orm.WithContext(ctx)
	result := orm.Model(&sourceFileGenerationRow{}).
		Where(map[string]any{
			SourceFileGenerationColumns.SourceID:   string(sourceID),
			SourceFileGenerationColumns.Generation: oldGeneration,
			SourceFileGenerationColumns.State:      string(FileGenerationOpen),
		}).
		Where(clause.Lte{Column: clause.Column{Name: SourceFileGenerationColumns.OpenedAtUS}, Value: rotatedAt.UTC().UnixMicro()}).
		Updates(map[string]any{SourceFileGenerationColumns.State: string(FileGenerationDraining), SourceFileGenerationColumns.DrainingAtUS: rotatedAt.UTC().UnixMicro()})
	if result.Error != nil {
		return fmt.Errorf("rotate file generation %q: drain old: %w", oldGeneration, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: generation %q is not Open or time regressed", ErrFileGenerationTransition, oldGeneration)
	}
	nextRow := newSourceFileGenerationRow(next)
	result = orm.Create(&nextRow)
	if result.Error != nil {
		return fmt.Errorf("rotate file generation %q: register replacement %q: %w", oldGeneration, next.Generation, result.Error)
	}
	if err := transaction.tx.Commit(); err != nil {
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
	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return fmt.Errorf("retire file generation %q: begin: %w", generation, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.tx.Rollback()
		}
	}()

	orm := transaction.orm.WithContext(ctx)
	record, found, err := loadFileGeneration(ctx, orm, sourceID, generation)
	if err != nil {
		return err
	}
	if !found || record.State != FileGenerationSealed {
		return fmt.Errorf("%w: generation %q is not sealed", ErrFileGenerationTransition, generation)
	}
	var receiptReferences int64
	result := orm.Model(&processingReceiptRow{}).
		Where(map[string]any{SourceFileGenerationColumns.SourceID: string(sourceID), SourceFileGenerationColumns.Generation: generation}).
		Count(&receiptReferences)
	if result.Error != nil {
		return fmt.Errorf("retire file generation %q: count receipt references: %w", generation, result.Error)
	}
	if receiptReferences != 0 {
		return fmt.Errorf("%w: generation %q has %d receipt references", ErrFileGenerationReferenced, generation, receiptReferences)
	}
	checkpoint, checkpointFound, err := loadSourceCheckpoint(ctx, orm, sourceID)
	if err != nil {
		return fmt.Errorf("retire file generation %q: load checkpoint: %w", generation, err)
	}
	if !checkpointFound {
		return fmt.Errorf("%w: generation %q", ErrFileGenerationNotDurable, generation)
	}
	if record.MaxDeliverySequence == nil || checkpoint.DeliverySequence < *record.MaxDeliverySequence {
		return fmt.Errorf("%w: generation %q", ErrFileGenerationNotDurable, generation)
	}
	result = orm.Model(&sourceFileGenerationRow{}).
		Where(map[string]any{
			SourceFileGenerationColumns.SourceID:   string(sourceID),
			SourceFileGenerationColumns.Generation: generation,
			SourceFileGenerationColumns.State:      string(FileGenerationSealed),
		}).
		Where(clause.Lte{Column: clause.Column{Name: SourceFileGenerationColumns.SealedAtUS}, Value: retiredAt.UTC().UnixMicro()}).
		Updates(map[string]any{SourceFileGenerationColumns.State: string(FileGenerationRetired), SourceFileGenerationColumns.RetiredAtUS: retiredAt.UTC().UnixMicro()})
	if result.Error != nil {
		return fmt.Errorf("retire file generation %q: update: %w", generation, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: generation %q changed concurrently", ErrFileGenerationTransition, generation)
	}
	if err := transaction.tx.Commit(); err != nil {
		return fmt.Errorf("retire file generation %q: commit: %w", generation, err)
	}
	committed = true
	return nil
}

func (s *Store) ready(ctx context.Context) error {
	if s == nil || s.db == nil || s.orm == nil {
		return fmt.Errorf("store is closed")
	}
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	return nil
}

func loadSourceCheckpoint(ctx context.Context, orm *gorm.DB, sourceID core.SourceID) (SourceCheckpoint, bool, error) {
	if sourceID == "" {
		return SourceCheckpoint{}, false, fmt.Errorf("source id is required")
	}
	var row sourceCheckpointRow
	result := orm.WithContext(ctx).
		Where(map[string]any{SourceCheckpointColumns.SourceID: string(sourceID)}).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return SourceCheckpoint{}, false, nil
	}
	if result.Error != nil {
		return SourceCheckpoint{}, false, result.Error
	}
	position, err := decodePosition(row.PositionKind, row.Generation, row.DeviceID, row.Inode, row.StartOffset, row.EndOffset, row.JournaldCursor)
	if err != nil {
		return SourceCheckpoint{}, false, err
	}
	return SourceCheckpoint{
		SourceID: sourceID, DeliverySequence: core.DeliverySequence(row.DeliverySequence),
		Position: position, PersistedAt: time.UnixMicro(row.PersistedAtUS).UTC(),
	}, true, nil
}

func loadFileGeneration(ctx context.Context, orm *gorm.DB, sourceID core.SourceID, generation string) (FileGeneration, bool, error) {
	var row sourceFileGenerationRow
	result := orm.WithContext(ctx).
		Where(map[string]any{SourceFileGenerationColumns.SourceID: string(sourceID), SourceFileGenerationColumns.Generation: generation}).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return FileGeneration{}, false, nil
	}
	if result.Error != nil {
		return FileGeneration{}, false, fmt.Errorf("load file generation %q: %w", generation, result.Error)
	}
	return fileGenerationFromRow(row), true, nil
}

func fileGenerationFromRow(row sourceFileGenerationRow) FileGeneration {
	return FileGeneration{
		SourceID: core.SourceID(row.SourceID), Generation: row.Generation, DeviceID: uint64(row.DeviceID),
		Inode: uint64(row.Inode), Path: row.Path, State: FileGenerationState(row.State),
		ObservedSize: uint64(row.ObservedSize), OpenedAt: time.UnixMicro(row.OpenedAtUS).UTC(),
		FinalEOF: nullableUint64(row.FinalEOF), MaxDeliverySequence: nullableDeliverySequence(row.MaxDeliverySequence),
		DrainingAt: nullableUnixTime(row.DrainingAtUS), SealedAt: nullableUnixTime(row.SealedAtUS),
		RetiredAt: nullableUnixTime(row.RetiredAtUS),
	}
}

func decodePosition(kind string, generation *string, deviceID, inode, startOffset, endOffset *int64, cursor *string) (core.SourcePosition, error) {
	switch kind {
	case "file":
		if generation == nil || deviceID == nil || inode == nil || startOffset == nil || endOffset == nil ||
			*deviceID < 0 || *inode < 0 || *startOffset < 0 || *endOffset < 0 {
			return core.SourcePosition{}, fmt.Errorf("stored file position is incomplete")
		}
		return core.NewFilePosition(core.FilePosition{
			Generation: *generation, DeviceID: uint64(*deviceID), Inode: uint64(*inode),
			StartOffset: uint64(*startOffset), EndOffset: uint64(*endOffset),
		})
	case "journald":
		if cursor == nil {
			return core.SourcePosition{}, fmt.Errorf("stored journald position is incomplete")
		}
		return core.NewJournaldPosition(*cursor)
	default:
		return core.SourcePosition{}, fmt.Errorf("unsupported stored position kind %q", kind)
	}
}

func nullableUnixTime(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	result := time.UnixMicro(*value).UTC()
	return &result
}

func nullableUint64(value *int64) *uint64 {
	if value == nil {
		return nil
	}
	result := uint64(*value)
	return &result
}

func nullableDeliverySequence(value *int64) *core.DeliverySequence {
	if value == nil {
		return nil
	}
	result := core.DeliverySequence(*value)
	return &result
}

func newSourceFileGenerationRow(generation FileGeneration) sourceFileGenerationRow {
	return sourceFileGenerationRow{
		SourceID: string(generation.SourceID), Generation: generation.Generation,
		DeviceID: int64(generation.DeviceID), Inode: int64(generation.Inode), Path: generation.Path,
		State: string(FileGenerationOpen), ObservedSize: int64(generation.ObservedSize),
		OpenedAtUS: generation.OpenedAt.UTC().UnixMicro(),
	}
}

func optionalString(value any) *string {
	if value == nil {
		return nil
	}
	result, ok := value.(string)
	if !ok {
		return nil
	}
	return &result
}

func optionalInt64(value any) *int64 {
	if value == nil {
		return nil
	}
	result, ok := value.(int64)
	if !ok {
		return nil
	}
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

func validateCheckpointPosition(ctx context.Context, orm *gorm.DB, sourceID core.SourceID, position core.SourcePosition) error {
	var source sourceRow
	result := orm.WithContext(ctx).
		Where(map[string]any{SourceColumns.SourceID: string(sourceID)}).
		Take(&source)
	if result.Error != nil {
		return fmt.Errorf("load source kind: %w", result.Error)
	}
	if file, ok := position.File(); ok {
		if source.Kind != string(SourceKindFile) {
			return fmt.Errorf("%w: file position requires file source", ErrSourcePositionMismatch)
		}
		var generation sourceFileGenerationRow
		result := orm.WithContext(ctx).
			Where(map[string]any{SourceFileGenerationColumns.SourceID: string(sourceID), SourceFileGenerationColumns.Generation: file.Generation}).
			Take(&generation)
		if result.Error != nil {
			return fmt.Errorf("%w: load file generation: %v", ErrSourcePositionMismatch, result.Error)
		}
		if generation.State == string(FileGenerationRetired) || generation.DeviceID != int64(file.DeviceID) || generation.Inode != int64(file.Inode) {
			return fmt.Errorf("%w: file generation identity or lifecycle differs", ErrSourcePositionMismatch)
		}
		return nil
	}
	if _, ok := position.Journald(); ok {
		if source.Kind != string(SourceKindJournald) {
			return fmt.Errorf("%w: journald position requires journald source", ErrSourcePositionMismatch)
		}
		return nil
	}
	return fmt.Errorf("%w: unsupported position", ErrSourcePositionMismatch)
}
