package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"gorm.io/gorm/clause"

	"github.com/lifei6671/guard-wall/internal/core"
)

// SourceDataLossAudit 仅携带文件身份和截断观测，不包含路径或日志内容。
type SourceDataLossAudit struct {
	NodeID       core.NodeID   `json:"node_id"`
	SourceID     core.SourceID `json:"source_id"`
	Generation   string        `json:"generation"`
	DeviceID     uint64        `json:"device_id"`
	Inode        uint64        `json:"inode"`
	PreviousSize uint64        `json:"previous_size"`
	ReadOffset   uint64        `json:"read_offset"`
	ObservedSize uint64        `json:"observed_size"`
	ObservedAt   time.Time     `json:"observed_at"`
}

// RecordSourceDataLoss 独立提交 Operational Audit，按 Source/generation 保留首次证据。
// 不创建处理事务或 receipt，也不改变 checkpoint、coverage 或代际生命周期。
func (s *Store) RecordSourceDataLoss(ctx context.Context, event SourceDataLossAudit) (resultErr error) {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("record source data loss: %w", err)
	}
	if err := validateSourceDataLoss(event); err != nil {
		return fmt.Errorf("record source data loss: %w", err)
	}
	identity, err := json.Marshal([2]string{string(event.SourceID), event.Generation})
	if err != nil {
		return fmt.Errorf("encode source data loss identity: %w", err)
	}
	key := fmt.Sprintf("source-data-loss:%x", sha256.Sum256(identity))
	details, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode source data loss: %w", err)
	}
	code := "DataLossSuspected"
	row := criticalAuditRow{
		AuditID: key, IdempotencyKey: key, NodeID: string(event.NodeID),
		Category: "source", Action: "data_loss_suspected", Result: "failure",
		Severity: "warning", Critical: 0, ActorType: "source", ErrorCode: &code,
		DetailsJSON: string(details), CreatedAtUS: event.ObservedAt.UTC().UnixMicro(),
	}
	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return fmt.Errorf("record source data loss: %w", err)
	}
	defer func() {
		if err := transaction.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			resultErr = joinErrors(resultErr, err)
		}
	}()
	orm := transaction.orm.WithContext(ctx)
	// 先写入取得 SQLite 写锁，再校验关联身份；错误会回滚本次审计。
	if err := orm.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: CriticalAuditColumns.IdempotencyKey}}, DoNothing: true}).Create(&row).Error; err != nil {
		return fmt.Errorf("record source data loss: insert: %w", err)
	}
	var source sourceRow
	if err := orm.Where(map[string]any{SourceColumns.SourceID: string(event.SourceID)}).Take(&source).Error; err != nil {
		return fmt.Errorf("record source data loss: source: %w", err)
	}
	if source.NodeID != string(event.NodeID) || source.Kind != string(SourceKindFile) {
		return fmt.Errorf("record source data loss: source identity differs")
	}
	generation, found, err := loadFileGeneration(ctx, orm, event.SourceID, event.Generation)
	if err != nil {
		return err
	}
	if !found || generation.DeviceID != event.DeviceID || generation.Inode != event.Inode {
		return fmt.Errorf("record source data loss: generation identity differs")
	}
	var persisted criticalAuditRow
	if err := orm.Where(map[string]any{CriticalAuditColumns.IdempotencyKey: key}).Take(&persisted).Error; err != nil {
		return fmt.Errorf("record source data loss: read back: %w", err)
	}
	var first SourceDataLossAudit
	if err := json.Unmarshal([]byte(persisted.DetailsJSON), &first); err != nil {
		return fmt.Errorf("record source data loss: stored evidence: %w", err)
	}
	if err := validateSourceDataLoss(first); err != nil {
		return fmt.Errorf("record source data loss: stored evidence: %w", err)
	}
	if persisted.AuditID != key || persisted.NodeID != string(event.NodeID) || persisted.Category != row.Category || persisted.Action != row.Action ||
		persisted.Result != row.Result || persisted.Severity != row.Severity || persisted.Critical != 0 || persisted.ActorType != row.ActorType ||
		persisted.ErrorCode == nil || *persisted.ErrorCode != code || persisted.DeliveryID != nil || persisted.AlertID != nil || persisted.DecisionID != nil ||
		first.NodeID != event.NodeID || first.SourceID != event.SourceID || first.Generation != event.Generation || first.DeviceID != event.DeviceID || first.Inode != event.Inode ||
		persisted.CreatedAtUS != first.ObservedAt.UTC().UnixMicro() {
		return fmt.Errorf("record source data loss: idempotency identity differs")
	}
	if err := transaction.tx.Commit(); err != nil {
		return fmt.Errorf("record source data loss: commit: %w", err)
	}
	return nil
}

func validateSourceDataLoss(event SourceDataLossAudit) error {
	if !isLowerHex128(string(event.NodeID)) || event.SourceID == "" || len(event.SourceID) > 128 || !utf8.ValidString(string(event.SourceID)) || !isLowerHex128(event.Generation) {
		return fmt.Errorf("identity is invalid")
	}
	if event.DeviceID > math.MaxInt64 || event.Inode > math.MaxInt64 || event.PreviousSize > math.MaxInt64 || event.ReadOffset > math.MaxInt64 || event.ObservedSize > math.MaxInt64 {
		return fmt.Errorf("numeric field exceeds SQLite range")
	}
	if event.ObservedSize >= event.PreviousSize && event.ObservedSize >= event.ReadOffset {
		return fmt.Errorf("visible truncation evidence is required")
	}
	if event.ObservedAt.IsZero() || event.ObservedAt.Year() < 1970 || event.ObservedAt.Year() > 9999 || event.ObservedAt.UTC().UnixMicro() <= 0 {
		return fmt.Errorf("observed time is invalid")
	}
	return nil
}
