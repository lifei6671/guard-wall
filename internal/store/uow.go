package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errUnitOfWorkClosed = errors.New("unit of work is closed")

// CriticalAudit is an append-only audit record that must commit with the
// business state it explains.
type CriticalAudit struct {
	ID             string
	IdempotencyKey string
	NodeID         core.NodeID
	Category       string
	Action         string
	Result         string
	Severity       string
	ActorType      string
	DeliveryID     *core.DeliveryID
	AlertID        *core.AlertID
	DecisionID     *core.DecisionID
	ErrorCode      string
	DetailsJSON    []byte
	CreatedAt      time.Time
}

// UnitOfWork is the only transaction handle accepted by processing domain
// writers. It is not safe for concurrent use.
type UnitOfWork struct {
	tx             *sql.Tx
	transactionORM *gorm.DB
	failed         error
	done           bool
}

type parserTerminalOutcomeRow struct {
	// DeliveryID 标识本次处理投递，同时属于复合主键并延迟关联最终 processing receipt。
	DeliveryID string `gorm:"column:delivery_id;primaryKey;autoIncrement:false"`
	// ParserID 标识产生终态结果的 parser，并与 ParserVersion 共同引用冻结的 parser revision。
	ParserID string `gorm:"column:parser_id;primaryKey;autoIncrement:false"`
	// ParserVersion 标识本次实际执行的 parser 版本，也是复合主键的一部分。
	ParserVersion string `gorm:"column:parser_version;primaryKey;autoIncrement:false"`
	// Kind 保存 success、no_match 或 record_permanent 终态分类。
	Kind string `gorm:"column:kind"`
	// EmittedCount 保存 parser 成功产生的 Event 数量，非成功终态必须为零。
	EmittedCount int64 `gorm:"column:emitted_count"`
	// FailureCode 保存永久失败分类；nil 会写入 SQL NULL，表示没有失败。
	FailureCode *string `gorm:"column:failure_code"`
	// CompletedAtUS 保存终态完成时间的 UTC Unix 微秒值。
	CompletedAtUS int64 `gorm:"column:completed_at_us"`
}

func (parserTerminalOutcomeRow) TableName() string {
	return "parser_terminal_outcomes"
}

type detectionTerminalOutcomeRow struct {
	// DeliveryID 标识拥有本结果的处理投递，并通过延迟外键关联最终 processing receipt。
	DeliveryID string `gorm:"column:delivery_id"`
	// EventID 标识被检测的稳定 Event，也是结果复合主键的一部分。
	EventID string `gorm:"column:event_id;primaryKey;autoIncrement:false"`
	// RuleID 标识得出结果的 Rule，并与 RuleVersion 共同引用冻结的 rule revision。
	RuleID string `gorm:"column:rule_id;primaryKey;autoIncrement:false"`
	// RuleVersion 标识本次实际评估的 Rule 版本，也是复合主键的一部分。
	RuleVersion string `gorm:"column:rule_version;primaryKey;autoIncrement:false"`
	// Kind 保存 success 或 record_permanent 终态分类。
	Kind string `gorm:"column:kind"`
	// FailureCode 保存永久失败分类；nil 会写入 SQL NULL，表示检测成功。
	FailureCode *string `gorm:"column:failure_code"`
	// CompletedAtUS 保存终态完成时间的 UTC Unix 微秒值。
	CompletedAtUS int64 `gorm:"column:completed_at_us"`
}

func (detectionTerminalOutcomeRow) TableName() string {
	return "detection_terminal_outcomes"
}

type alertRow struct {
	// AlertID 标识持久化告警，也是 alerts 表的主键。
	AlertID string `gorm:"column:alert_id;primaryKey;autoIncrement:false"`
	// NodeID 标识产生告警的节点，并立即外键关联 node_identity。
	NodeID string `gorm:"column:node_id"`
	// EventID 标识触发告警的 Event，并参与检测成员关系的复合唯一键与外键。
	EventID string `gorm:"column:event_id"`
	// RuleID 标识命中的 Rule，并与 RuleVersion 共同引用冻结规则和检测成员关系。
	RuleID string `gorm:"column:rule_id"`
	// RuleVersion 标识命中时实际执行的 Rule 版本，也是检测成员关系复合键的一部分。
	RuleVersion string `gorm:"column:rule_version"`
	// CanonicalTarget 保存告警命中的规范化目标字符串。
	CanonicalTarget string `gorm:"column:canonical_target"`
	// ObservedAtUS 保存触发事件观测时间的 UTC Unix 微秒值。
	ObservedAtUS int64 `gorm:"column:observed_at_us"`
	// CreatedAtUS 保存告警创建时间的 UTC Unix 微秒值。
	CreatedAtUS int64 `gorm:"column:created_at_us"`
}

func (alertRow) TableName() string {
	return "alerts"
}

type decisionRow struct {
	// DecisionID 标识不可变安全决策，也是 decisions 表的主键。
	DecisionID string `gorm:"column:decision_id;primaryKey;autoIncrement:false"`
	// NodeID 标识决策所属节点，立即外键关联 node_identity，并参与两条 active partial-unique。
	NodeID string `gorm:"column:node_id"`
	// Source 保存 automatic 或 manual 来源枚举，并选择对应的 active partial-unique。
	Source string `gorm:"column:source"`
	// RuleID 可选标识自动决策 Rule，与 RuleVersion 组成立即外键并参与 automatic active partial-unique；nil 写入 SQL NULL。
	RuleID *string `gorm:"column:rule_id"`
	// RuleVersion 可选标识自动决策冻结的 Rule 版本，并与 RuleID 组成立即外键；nil 写入 SQL NULL。
	RuleVersion *string `gorm:"column:rule_version"`
	// AlertID 可选立即外键关联触发自动决策的告警；nil 写入 SQL NULL。
	AlertID *string `gorm:"column:alert_id"`
	// CanonicalTarget 保存决策作用的规范化目标字符串，并参与两条 active partial-unique。
	CanonicalTarget string `gorm:"column:canonical_target"`
	// CreatedAtUS 保存决策创建时间的 UTC Unix 微秒值。
	CreatedAtUS int64 `gorm:"column:created_at_us"`
	// UpdatedAtUS 保存决策最后更新时间的 UTC Unix 微秒值。
	UpdatedAtUS int64 `gorm:"column:updated_at_us"`
	// LastTriggeredAtUS 保存决策最近触发时间的 UTC Unix 微秒值。
	LastTriggeredAtUS int64 `gorm:"column:last_triggered_at_us"`
	// ExpiresAtUS 保存可选过期时间的 UTC Unix 微秒值；nil 写入 SQL NULL。
	ExpiresAtUS *int64 `gorm:"column:expires_at_us"`
	// EndedAtUS 保存可选终止时间的 UTC Unix 微秒值；nil 写入 SQL NULL。
	EndedAtUS *int64 `gorm:"column:ended_at_us"`
	// State 保存 active、expired 或 revoked 生命周期状态枚举，并限定 partial-unique 只约束 active 行。
	State string `gorm:"column:state"`
	// EndReason 保存可选终止原因枚举；nil 写入 SQL NULL。
	EndReason *string `gorm:"column:end_reason"`
	// SuppressedCount 保存自动决策被后续同类事件抑制的累计次数。
	SuppressedCount uint64 `gorm:"column:suppressed_count"`
}

func (decisionRow) TableName() string {
	return "decisions"
}

type criticalAuditRow struct {
	// AuditID 标识不可变审计记录，也是 audit_logs 表的主键。
	AuditID string `gorm:"column:audit_id;primaryKey;autoIncrement:false"`
	// IdempotencyKey 标识业务审计动作的稳定幂等键，并受数据库唯一约束保护。
	IdempotencyKey string `gorm:"column:idempotency_key"`
	// NodeID 标识产生审计记录的节点，并立即外键关联 node_identity。
	NodeID string `gorm:"column:node_id"`
	// Category 保存审计事件所属的稳定业务分类。
	Category string `gorm:"column:category"`
	// Action 保存触发本条审计记录的稳定业务动作。
	Action string `gorm:"column:action"`
	// Result 保存 success、rejected 或 failure 审计结果枚举。
	Result string `gorm:"column:result"`
	// Severity 保存 info、warning 或 critical 审计严重度枚举。
	Severity string `gorm:"column:severity"`
	// Critical 为 0 或 1；AppendCriticalAudit 固定写 1，与所解释的业务状态原子提交。
	Critical int64 `gorm:"column:critical"`
	// ActorType 保存 system、administrator 或 source 审计主体枚举。
	ActorType string `gorm:"column:actor_type"`
	// DeliveryID 可选关联处理投递；nil 写入 SQL NULL。
	DeliveryID *string `gorm:"column:delivery_id"`
	// AlertID 可选关联告警；nil 写入 SQL NULL。
	AlertID *string `gorm:"column:alert_id"`
	// DecisionID 可选关联安全决策；nil 写入 SQL NULL。
	DecisionID *string `gorm:"column:decision_id"`
	// ErrorCode 保存可选稳定错误码；nil 写入 SQL NULL。
	ErrorCode *string `gorm:"column:error_code"`
	// DetailsJSON 保存已校验的 JSON 文本，空输入规范化为 {}。
	DetailsJSON string `gorm:"column:details_json"`
	// CreatedAtUS 保存审计记录创建时间的 UTC Unix 微秒值。
	CreatedAtUS int64 `gorm:"column:created_at_us"`
}

func (criticalAuditRow) TableName() string {
	return "audit_logs"
}

type detectionContributionRow struct {
	// EventID 标识被检测的稳定 Event，并与 RuleID、RuleVersion 共同组成复合主键。
	EventID string `gorm:"column:event_id;primaryKey;autoIncrement:false"`
	// RuleID 标识贡献所属 Rule，并与 RuleVersion 共同立即外键关联冻结规则版本。
	RuleID string `gorm:"column:rule_id;primaryKey;autoIncrement:false"`
	// RuleVersion 标识本次实际评估的 Rule 版本，也是复合主键的一部分。
	RuleVersion string `gorm:"column:rule_version;primaryKey;autoIncrement:false"`
	// DeliveryID 标识首次贡献该检测成员关系的投递，并通过延迟外键关联最终 processing receipt。
	DeliveryID string `gorm:"column:delivery_id"`
	// ContributedAtUS 保存检测成员关系首次产生时间的 UTC Unix 微秒值。
	ContributedAtUS int64 `gorm:"column:contributed_at_us"`
}

func (detectionContributionRow) TableName() string {
	return "detection_contributions"
}

type desiredBanProjectionRow struct {
	// NodeID 标识投影所属节点，立即外键关联 node_identity，并与 CanonicalTarget 共同组成复合主键。
	NodeID string `gorm:"column:node_id;primaryKey;autoIncrement:false"`
	// CanonicalTarget 保存规范化目标字符串，也是投影复合主键的一部分。
	CanonicalTarget string `gorm:"column:canonical_target;primaryKey;autoIncrement:false"`
	// State 保存 absent 或 present 投影状态枚举。
	State string `gorm:"column:state"`
	// ActiveCount 保存当前要求该目标被封禁的 active Decision 数量。
	ActiveCount uint64 `gorm:"column:active_count"`
	// EffectiveUntilUS 保存可选生效截止时间的 UTC Unix 微秒值；nil 写入 SQL NULL。
	EffectiveUntilUS *int64 `gorm:"column:effective_until_us"`
	// TargetProjectionRevision 保存单目标单调递增 revision，用于拒绝 stale 或冲突写入。
	TargetProjectionRevision core.TargetProjectionRevision `gorm:"column:target_projection_revision"`
	// UpdatedAtUS 保存投影最后更新时间的 UTC Unix 微秒值。
	UpdatedAtUS int64 `gorm:"column:updated_at_us"`
}

func (desiredBanProjectionRow) TableName() string {
	return "desired_ban_projections"
}

type processingReceiptRow struct {
	// DeliveryID 标识本次处理投递，也是 processing_receipts 表的主键。
	DeliveryID string `gorm:"column:delivery_id;primaryKey;autoIncrement:false"`
	// SourceID 标识投递来源，并立即外键关联 sources。
	SourceID string `gorm:"column:source_id"`
	// PositionKind 保存 file 或 journald 位置枚举，并决定位置列的 SQL NULL 组合。
	PositionKind string `gorm:"column:position_kind"`
	// Generation 保存 file generation，并与 SourceID 组成立即复合外键；journald receipt 为 nil 并写入 SQL NULL。
	Generation *string `gorm:"column:generation"`
	// DeviceID 保存 file 设备号；journald receipt 为 nil 并写入 SQL NULL。
	DeviceID *int64 `gorm:"column:device_id"`
	// Inode 保存 file inode；journald receipt 为 nil 并写入 SQL NULL。
	Inode *int64 `gorm:"column:inode"`
	// StartOffset 保存 file 起始偏移；journald receipt 为 nil 并写入 SQL NULL。
	StartOffset *int64 `gorm:"column:start_offset"`
	// EndOffset 保存 file 结束偏移；journald receipt 为 nil 并写入 SQL NULL。
	EndOffset *int64 `gorm:"column:end_offset"`
	// JournaldCursor 保存 journald cursor；file receipt 为 nil 并写入 SQL NULL。
	JournaldCursor *string `gorm:"column:journald_cursor"`
	// Kind 保存 success 或 record_permanent 终态分类，并决定失败列的 SQL NULL 组合。
	Kind string `gorm:"column:kind"`
	// FailureStage 保存永久失败阶段；success receipt 为 nil 并写入 SQL NULL。
	FailureStage *string `gorm:"column:failure_stage"`
	// FailureCode 保存永久失败稳定错误码；success receipt 为 nil 并写入 SQL NULL。
	FailureCode *string `gorm:"column:failure_code"`
	// SanitizedError 保存永久失败脱敏诊断；success receipt 为 nil 并写入 SQL NULL。
	SanitizedError *string `gorm:"column:sanitized_error"`
	// TerminalAction 保存永久失败终态动作；success receipt 为 nil 并写入 SQL NULL。
	TerminalAction *string `gorm:"column:terminal_action"`
	// FailureOccurredAtUS 保存永久失败发生时间的 UTC Unix 微秒值；success receipt 为 nil 并写入 SQL NULL。
	FailureOccurredAtUS *int64 `gorm:"column:failure_occurred_at_us"`
	// CommittedAtUS 保存 receipt 业务提交时间的 UTC Unix 微秒值。
	CommittedAtUS int64 `gorm:"column:committed_at_us"`
}

func (processingReceiptRow) TableName() string {
	return "processing_receipts"
}

func (s *Store) newUnitOfWork(tx *sql.Tx) *UnitOfWork {
	return &UnitOfWork{
		tx:             tx,
		transactionORM: newGORMTransactionSession(s.orm, tx),
	}
}

// BeginProcessing starts one processing transaction. Only the Processing
// Coordinator may commit or roll back the returned UnitOfWork.
func (s *Store) BeginProcessing(ctx context.Context) (*UnitOfWork, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("begin processing: store is closed")
	}
	if ctx == nil {
		return nil, fmt.Errorf("begin processing: context is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin processing transaction: %w", err)
	}
	return s.newUnitOfWork(tx), nil
}

// PutParserOutcome inserts one parser version's terminal result.
func (u *UnitOfWork) PutParserOutcome(ctx context.Context, outcome core.ParserTerminalOutcome) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := outcome.Validate(); err != nil {
		return u.fail(fmt.Errorf("put parser outcome: validate: %w", err))
	}
	kind, err := parserOutcomeKindValue(outcome.Kind)
	if err != nil {
		return u.fail(err)
	}
	var failureCode *string
	if outcome.FailureCode != "" {
		failureCode = &outcome.FailureCode
	}
	result := u.transactionORM.WithContext(ctx).
		Select(
			ParserTerminalOutcomeColumns.DeliveryID, ParserTerminalOutcomeColumns.ParserID,
			ParserTerminalOutcomeColumns.ParserVersion, ParserTerminalOutcomeColumns.Kind,
			ParserTerminalOutcomeColumns.EmittedCount, ParserTerminalOutcomeColumns.FailureCode,
			ParserTerminalOutcomeColumns.CompletedAtUS,
		).
		Create(&parserTerminalOutcomeRow{
			DeliveryID:    string(outcome.DeliveryID),
			ParserID:      string(outcome.ParserID),
			ParserVersion: string(outcome.ParserVersion),
			Kind:          kind,
			EmittedCount:  int64(outcome.EmittedCount),
			FailureCode:   failureCode,
			CompletedAtUS: outcome.CompletedAt.UTC().UnixMicro(),
		})
	if result.Error != nil {
		return u.fail(fmt.Errorf("put parser outcome for delivery %q: %w", outcome.DeliveryID, result.Error))
	}
	return nil
}

// PutDetectionContribution inserts one Event/Rule-version membership. The
// returned boolean is true only for the first durable candidate in this transaction.
func (u *UnitOfWork) PutDetectionContribution(ctx context.Context, contribution core.DetectionContribution) (bool, error) {
	if err := u.ready(ctx); err != nil {
		return false, err
	}
	if err := contribution.Validate(); err != nil {
		return false, u.fail(fmt.Errorf("put detection contribution: validate: %w", err))
	}
	sqlResult := gorm.WithResult()
	result := u.transactionORM.WithContext(ctx).
		Clauses(sqlResult, clause.OnConflict{
			Columns: []clause.Column{
				{Name: DetectionContributionColumns.EventID},
				{Name: DetectionContributionColumns.RuleID},
				{Name: DetectionContributionColumns.RuleVersion},
			},
			DoNothing: true,
		}).
		Select(
			DetectionContributionColumns.EventID, DetectionContributionColumns.RuleID,
			DetectionContributionColumns.RuleVersion, DetectionContributionColumns.DeliveryID,
			DetectionContributionColumns.ContributedAtUS,
		).
		Create(&detectionContributionRow{
			EventID: string(contribution.EventID), RuleID: string(contribution.RuleID),
			RuleVersion: string(contribution.RuleVersion), DeliveryID: string(contribution.DeliveryID),
			ContributedAtUS: contribution.ContributedAt.UTC().UnixMicro(),
		})
	if result.Error != nil {
		return false, u.fail(fmt.Errorf("put detection contribution for event %q: %w", contribution.EventID, result.Error))
	}
	affected, err := sqlResult.Result.RowsAffected()
	if err != nil {
		return false, u.fail(fmt.Errorf("put detection contribution for event %q: affected rows: %w", contribution.EventID, err))
	}
	if affected == 1 {
		return true, nil
	}
	var existing detectionContributionRow
	readback := u.transactionORM.WithContext(ctx).
		Select(DetectionContributionColumns.DeliveryID).
		Where(&detectionContributionRow{
			EventID: string(contribution.EventID), RuleID: string(contribution.RuleID),
			RuleVersion: string(contribution.RuleVersion),
		}).
		Take(&existing)
	if readback.Error != nil {
		readbackErr := readback.Error
		if errors.Is(readbackErr, gorm.ErrRecordNotFound) {
			readbackErr = sql.ErrNoRows
		}
		return false, u.fail(fmt.Errorf("put detection contribution for event %q: verify duplicate: %w", contribution.EventID, readbackErr))
	}
	if existing.DeliveryID != string(contribution.DeliveryID) {
		return false, u.fail(fmt.Errorf("put detection contribution for event %q: stable delivery identity differs", contribution.EventID))
	}
	return false, nil
}

// PutDetectionOutcome inserts one applicable Event/Rule revision's terminal result.
func (u *UnitOfWork) PutDetectionOutcome(ctx context.Context, outcome core.DetectionTerminalOutcome) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := outcome.Validate(); err != nil {
		return u.fail(fmt.Errorf("put detection outcome: validate: %w", err))
	}
	kind, err := detectionOutcomeKindValue(outcome.Kind)
	if err != nil {
		return u.fail(err)
	}
	var failureCode *string
	if outcome.FailureCode != "" {
		failureCode = &outcome.FailureCode
	}
	result := u.transactionORM.WithContext(ctx).
		Select(
			DetectionTerminalOutcomeColumns.DeliveryID, DetectionTerminalOutcomeColumns.EventID,
			DetectionTerminalOutcomeColumns.RuleID, DetectionTerminalOutcomeColumns.RuleVersion,
			DetectionTerminalOutcomeColumns.Kind, DetectionTerminalOutcomeColumns.FailureCode,
			DetectionTerminalOutcomeColumns.CompletedAtUS,
		).
		Create(&detectionTerminalOutcomeRow{
			DeliveryID:    string(outcome.DeliveryID),
			EventID:       string(outcome.EventID),
			RuleID:        string(outcome.RuleID),
			RuleVersion:   string(outcome.RuleVersion),
			Kind:          kind,
			FailureCode:   failureCode,
			CompletedAtUS: outcome.CompletedAt.UTC().UnixMicro(),
		})
	if result.Error != nil {
		return u.fail(fmt.Errorf("put detection outcome for event %q: %w", outcome.EventID, result.Error))
	}
	return nil
}

// PutAlert inserts one durable Alert tied to a detection membership.
func (u *UnitOfWork) PutAlert(ctx context.Context, alert core.Alert) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := alert.Validate(); err != nil {
		return u.fail(fmt.Errorf("put alert: validate: %w", err))
	}
	result := u.transactionORM.WithContext(ctx).
		Select(
			AlertColumns.AlertID, AlertColumns.NodeID, AlertColumns.EventID, AlertColumns.RuleID,
			AlertColumns.RuleVersion, AlertColumns.CanonicalTarget, AlertColumns.ObservedAtUS,
			AlertColumns.CreatedAtUS,
		).
		Create(&alertRow{
			AlertID:         string(alert.ID),
			NodeID:          string(alert.NodeID),
			EventID:         string(alert.EventID),
			RuleID:          string(alert.RuleID),
			RuleVersion:     string(alert.RuleVersion),
			CanonicalTarget: alert.CanonicalTarget.String(),
			ObservedAtUS:    alert.ObservedAt.UTC().UnixMicro(),
			CreatedAtUS:     alert.CreatedAt.UTC().UnixMicro(),
		})
	if result.Error != nil {
		return u.fail(fmt.Errorf("put alert %q: %w", alert.ID, result.Error))
	}
	return nil
}

// PutDecision inserts one immutable Decision identity and lifecycle row.
func (u *UnitOfWork) PutDecision(ctx context.Context, decision core.Decision) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := decision.Validate(); err != nil {
		return u.fail(fmt.Errorf("put decision: validate: %w", err))
	}
	if decision.Source == core.DecisionSourceAutomatic && decision.RuleVersion == nil {
		return u.fail(fmt.Errorf("put decision: automatic decision requires rule version"))
	}
	if decision.Source == core.DecisionSourceManual &&
		(decision.RuleID != nil || decision.RuleVersion != nil || decision.AlertID != nil) {
		return u.fail(fmt.Errorf("put decision: manual decision cannot reference rule or alert"))
	}
	if decision.CreatedAt.IsZero() || decision.UpdatedAt.IsZero() || decision.LastTriggeredAt.IsZero() {
		return u.fail(fmt.Errorf("put decision: lifecycle times are required"))
	}

	source, err := decisionSourceValue(decision.Source)
	if err != nil {
		return u.fail(err)
	}
	state, err := decisionStateValue(decision.State)
	if err != nil {
		return u.fail(err)
	}

	var ruleID, ruleVersion, alertID, endReason *string
	var expiresAtUS, endedAtUS *int64
	if decision.RuleID != nil {
		value := string(*decision.RuleID)
		ruleID = &value
	}
	if decision.RuleVersion != nil {
		value := string(*decision.RuleVersion)
		ruleVersion = &value
	}
	if decision.AlertID != nil {
		value := string(*decision.AlertID)
		alertID = &value
	}
	if decision.ExpiresAt != nil {
		value := decision.ExpiresAt.UTC().UnixMicro()
		expiresAtUS = &value
	}
	if decision.EndedAt != nil {
		value := decision.EndedAt.UTC().UnixMicro()
		endedAtUS = &value
	}
	if decision.EndReason != nil {
		value := string(*decision.EndReason)
		endReason = &value
	}

	result := u.transactionORM.WithContext(ctx).
		Select(
			DecisionColumns.DecisionID, DecisionColumns.NodeID, DecisionColumns.Source,
			DecisionColumns.RuleID, DecisionColumns.RuleVersion, DecisionColumns.AlertID,
			DecisionColumns.CanonicalTarget, DecisionColumns.CreatedAtUS, DecisionColumns.UpdatedAtUS,
			DecisionColumns.LastTriggeredAtUS, DecisionColumns.ExpiresAtUS, DecisionColumns.EndedAtUS,
			DecisionColumns.State, DecisionColumns.EndReason, DecisionColumns.SuppressedCount,
		).
		Create(&decisionRow{
			DecisionID: string(decision.ID), NodeID: string(decision.NodeID), Source: source,
			RuleID: ruleID, RuleVersion: ruleVersion, AlertID: alertID,
			CanonicalTarget:   decision.CanonicalTarget.String(),
			CreatedAtUS:       decision.CreatedAt.UTC().UnixMicro(),
			UpdatedAtUS:       decision.UpdatedAt.UTC().UnixMicro(),
			LastTriggeredAtUS: decision.LastTriggeredAt.UTC().UnixMicro(),
			ExpiresAtUS:       expiresAtUS, EndedAtUS: endedAtUS, State: state,
			EndReason: endReason, SuppressedCount: decision.SuppressedCount,
		})
	if result.Error != nil {
		return u.fail(fmt.Errorf("put decision %q: %w", decision.ID, result.Error))
	}
	return nil
}

// PutProjection replaces the materialized projection for one canonical target.
func (u *UnitOfWork) PutProjection(
	ctx context.Context,
	projection core.DesiredBanProjection,
	updatedAt time.Time,
) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := projection.Validate(); err != nil {
		return u.fail(fmt.Errorf("put projection: validate: %w", err))
	}
	if updatedAt.IsZero() {
		return u.fail(fmt.Errorf("put projection: updated time is required"))
	}

	state := ""
	switch projection.State {
	case core.BanProjectionAbsent:
		if projection.ActiveCount != 0 || projection.EffectiveUntil != nil {
			return u.fail(fmt.Errorf("put projection: absent projection must be empty"))
		}
		state = "absent"
	case core.BanProjectionPresent:
		if projection.ActiveCount == 0 {
			return u.fail(fmt.Errorf("put projection: present projection requires active decisions"))
		}
		state = "present"
	default:
		return u.fail(fmt.Errorf("put projection: unsupported state %d", projection.State))
	}

	var effectiveUntilUS *int64
	if projection.EffectiveUntil != nil {
		value := projection.EffectiveUntil.UTC().UnixMicro()
		effectiveUntilUS = &value
	}
	wanted := desiredBanProjectionRow{
		NodeID: string(projection.NodeID), CanonicalTarget: projection.CanonicalTarget.String(),
		State: state, ActiveCount: projection.ActiveCount, EffectiveUntilUS: effectiveUntilUS,
		TargetProjectionRevision: projection.Revision, UpdatedAtUS: updatedAt.UTC().UnixMicro(),
	}
	sqlResult := gorm.WithResult()
	result := u.transactionORM.WithContext(ctx).
		Clauses(sqlResult, clause.OnConflict{DoNothing: true}).
		Select(
			DesiredBanProjectionColumns.NodeID, DesiredBanProjectionColumns.CanonicalTarget,
			DesiredBanProjectionColumns.State, DesiredBanProjectionColumns.ActiveCount,
			DesiredBanProjectionColumns.EffectiveUntilUS,
			DesiredBanProjectionColumns.TargetProjectionRevision, DesiredBanProjectionColumns.UpdatedAtUS,
		).
		Create(&wanted)
	if result.Error != nil {
		return u.fail(fmt.Errorf("put projection %q: %w", projection.CanonicalTarget, result.Error))
	}
	affected, err := sqlResult.Result.RowsAffected()
	if err != nil {
		return u.fail(fmt.Errorf("put projection %q: read affected rows: %w", projection.CanonicalTarget, err))
	}
	if affected == 1 {
		return nil
	}

	var existing desiredBanProjectionRow
	readback := u.transactionORM.WithContext(ctx).
		Where(&desiredBanProjectionRow{NodeID: wanted.NodeID, CanonicalTarget: wanted.CanonicalTarget}).
		Take(&existing)
	if readback.Error != nil {
		return u.fail(fmt.Errorf("put projection %q: read existing revision: %w", projection.CanonicalTarget, readback.Error))
	}
	if projectionRowsEqual(existing, wanted) {
		return nil
	}
	if existing.TargetProjectionRevision >= wanted.TargetProjectionRevision {
		return u.fail(fmt.Errorf(
			"put projection %q: stale or conflicting revision %d",
			projection.CanonicalTarget, projection.Revision))
	}
	result = u.transactionORM.WithContext(ctx).
		Model(&desiredBanProjectionRow{}).
		Where(map[string]any{
			DesiredBanProjectionColumns.NodeID:                   wanted.NodeID,
			DesiredBanProjectionColumns.CanonicalTarget:          wanted.CanonicalTarget,
			DesiredBanProjectionColumns.TargetProjectionRevision: existing.TargetProjectionRevision,
		}).
		Updates(map[string]any{
			DesiredBanProjectionColumns.State:                    wanted.State,
			DesiredBanProjectionColumns.ActiveCount:              wanted.ActiveCount,
			DesiredBanProjectionColumns.EffectiveUntilUS:         wanted.EffectiveUntilUS,
			DesiredBanProjectionColumns.TargetProjectionRevision: wanted.TargetProjectionRevision,
			DesiredBanProjectionColumns.UpdatedAtUS:              wanted.UpdatedAtUS,
		})
	if result.Error != nil {
		return u.fail(fmt.Errorf("put projection %q: update revision: %w", projection.CanonicalTarget, result.Error))
	}
	if result.RowsAffected == 1 {
		return nil
	}
	return u.fail(fmt.Errorf(
		"put projection %q: stale or conflicting revision %d",
		projection.CanonicalTarget, projection.Revision))
}

func projectionRowsEqual(left, right desiredBanProjectionRow) bool {
	if left.NodeID != right.NodeID || left.CanonicalTarget != right.CanonicalTarget ||
		left.State != right.State || left.ActiveCount != right.ActiveCount ||
		left.TargetProjectionRevision != right.TargetProjectionRevision {
		return false
	}
	if left.EffectiveUntilUS == nil || right.EffectiveUntilUS == nil {
		return left.EffectiveUntilUS == nil && right.EffectiveUntilUS == nil
	}
	return *left.EffectiveUntilUS == *right.EffectiveUntilUS
}

// AppendCriticalAudit appends a critical audit row in this UnitOfWork.
func (u *UnitOfWork) AppendCriticalAudit(ctx context.Context, audit CriticalAudit) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if audit.ID == "" || audit.IdempotencyKey == "" || audit.NodeID == "" {
		return u.fail(fmt.Errorf("append critical audit: identity fields are required"))
	}
	if audit.Category == "" || audit.Action == "" || audit.Result == "" ||
		audit.Severity == "" || audit.ActorType == "" || audit.CreatedAt.IsZero() {
		return u.fail(fmt.Errorf("append critical audit: classification fields are required"))
	}
	details := audit.DetailsJSON
	if len(details) == 0 {
		details = []byte("{}")
	}
	if !json.Valid(details) {
		return u.fail(fmt.Errorf("append critical audit: details must be valid JSON"))
	}

	var deliveryID, alertID, decisionID, errorCode *string
	if audit.DeliveryID != nil {
		value := string(*audit.DeliveryID)
		deliveryID = &value
	}
	if audit.AlertID != nil {
		value := string(*audit.AlertID)
		alertID = &value
	}
	if audit.DecisionID != nil {
		value := string(*audit.DecisionID)
		decisionID = &value
	}
	if audit.ErrorCode != "" {
		value := audit.ErrorCode
		errorCode = &value
	}

	result := u.transactionORM.WithContext(ctx).
		Select(
			CriticalAuditColumns.AuditID, CriticalAuditColumns.IdempotencyKey,
			CriticalAuditColumns.NodeID, CriticalAuditColumns.Category, CriticalAuditColumns.Action,
			CriticalAuditColumns.Result, CriticalAuditColumns.Severity, CriticalAuditColumns.Critical,
			CriticalAuditColumns.ActorType, CriticalAuditColumns.DeliveryID, CriticalAuditColumns.AlertID,
			CriticalAuditColumns.DecisionID, CriticalAuditColumns.ErrorCode, CriticalAuditColumns.DetailsJSON,
			CriticalAuditColumns.CreatedAtUS,
		).
		Create(&criticalAuditRow{
			AuditID: audit.ID, IdempotencyKey: audit.IdempotencyKey, NodeID: string(audit.NodeID),
			Category: audit.Category, Action: audit.Action, Result: audit.Result,
			Severity: audit.Severity, Critical: 1, ActorType: audit.ActorType,
			DeliveryID: deliveryID, AlertID: alertID, DecisionID: decisionID, ErrorCode: errorCode,
			DetailsJSON: string(details), CreatedAtUS: audit.CreatedAt.UTC().UnixMicro(),
		})
	if result.Error != nil {
		return u.fail(fmt.Errorf("append critical audit %q: %w", audit.ID, result.Error))
	}
	return nil
}

// PutReceipt inserts the terminal record for one delivery.
func (u *UnitOfWork) PutReceipt(ctx context.Context, receipt core.ProcessingReceipt) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := receipt.Validate(); err != nil {
		return u.fail(fmt.Errorf("put receipt: validate: %w", err))
	}

	position, err := encodePosition(receipt.Position)
	if err != nil {
		return u.fail(fmt.Errorf("put receipt: %w", err))
	}
	kind, err := encodeReceipt(receipt)
	if err != nil {
		return u.fail(err)
	}

	row := processingReceiptRow{
		DeliveryID: string(receipt.DeliveryID), SourceID: string(receipt.SourceID),
		PositionKind: position.kind, Kind: kind,
		CommittedAtUS: receipt.Committed.UTC().UnixMicro(),
	}
	if file, ok := receipt.Position.File(); ok {
		generation := file.Generation
		deviceID := int64(file.DeviceID)
		inode := int64(file.Inode)
		startOffset := int64(file.StartOffset)
		endOffset := int64(file.EndOffset)
		row.Generation = &generation
		row.DeviceID = &deviceID
		row.Inode = &inode
		row.StartOffset = &startOffset
		row.EndOffset = &endOffset
	} else if journald, ok := receipt.Position.Journald(); ok {
		cursor := journald.Cursor
		row.JournaldCursor = &cursor
	}
	if receipt.Failure != nil {
		stage := receipt.Failure.Stage
		code := receipt.Failure.Code
		sanitizedError := receipt.Failure.SanitizedError
		action := receipt.Failure.Action
		occurredAtUS := receipt.Failure.OccurredAt.UTC().UnixMicro()
		row.FailureStage = &stage
		row.FailureCode = &code
		row.SanitizedError = &sanitizedError
		row.TerminalAction = &action
		row.FailureOccurredAtUS = &occurredAtUS
	}
	result := u.transactionORM.WithContext(ctx).
		Select(
			ProcessingReceiptColumns.DeliveryID, ProcessingReceiptColumns.SourceID,
			ProcessingReceiptColumns.PositionKind, ProcessingReceiptColumns.Generation,
			ProcessingReceiptColumns.DeviceID, ProcessingReceiptColumns.Inode,
			ProcessingReceiptColumns.StartOffset, ProcessingReceiptColumns.EndOffset,
			ProcessingReceiptColumns.JournaldCursor, ProcessingReceiptColumns.Kind,
			ProcessingReceiptColumns.FailureStage, ProcessingReceiptColumns.FailureCode,
			ProcessingReceiptColumns.SanitizedError, ProcessingReceiptColumns.TerminalAction,
			ProcessingReceiptColumns.FailureOccurredAtUS, ProcessingReceiptColumns.CommittedAtUS,
		).
		Create(&row)
	if result.Error != nil {
		return u.fail(fmt.Errorf("put receipt %q: %w", receipt.DeliveryID, result.Error))
	}
	return nil
}

// Commit commits the UnitOfWork. If any prior write failed, Commit rolls the
// transaction back and returns that first failure.
func (u *UnitOfWork) Commit() error {
	if u == nil || u.tx == nil || u.done {
		return errUnitOfWorkClosed
	}
	u.done = true
	if u.failed != nil {
		rollbackErr := u.tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return joinErrors(u.failed, rollbackErr)
		}
		return u.failed
	}
	if err := u.tx.Commit(); err != nil {
		return fmt.Errorf("commit unit of work: %w", err)
	}
	return nil
}

// Rollback rolls back the UnitOfWork.
func (u *UnitOfWork) Rollback() error {
	if u == nil || u.tx == nil || u.done {
		return errUnitOfWorkClosed
	}
	u.done = true
	if err := u.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback unit of work: %w", err)
	}
	return nil
}

func (u *UnitOfWork) ready(ctx context.Context) error {
	if u == nil || u.tx == nil || u.done {
		return errUnitOfWorkClosed
	}
	if ctx == nil {
		return u.fail(fmt.Errorf("unit of work: context is required"))
	}
	if u.failed != nil {
		return u.failed
	}
	return nil
}

func (u *UnitOfWork) fail(err error) error {
	if u != nil && u.failed == nil {
		u.failed = err
	}
	return err
}

type encodedPosition struct {
	kind        string
	generation  any
	deviceID    any
	inode       any
	startOffset any
	endOffset   any
	cursor      any
}

func encodePosition(position core.SourcePosition) (encodedPosition, error) {
	if file, ok := position.File(); ok {
		if file.DeviceID > math.MaxInt64 || file.Inode > math.MaxInt64 ||
			file.StartOffset > math.MaxInt64 || file.EndOffset > math.MaxInt64 {
			return encodedPosition{}, fmt.Errorf("file position exceeds SQLite INTEGER range")
		}
		return encodedPosition{
			kind: "file", generation: file.Generation, deviceID: int64(file.DeviceID),
			inode: int64(file.Inode), startOffset: int64(file.StartOffset),
			endOffset: int64(file.EndOffset),
		}, nil
	}
	if journald, ok := position.Journald(); ok {
		return encodedPosition{kind: "journald", cursor: journald.Cursor}, nil
	}
	return encodedPosition{}, fmt.Errorf("unsupported source position")
}

func encodeReceipt(receipt core.ProcessingReceipt) (string, error) {
	switch receipt.Kind {
	case core.ReceiptSuccess:
		if receipt.Failure != nil {
			return "", fmt.Errorf("put receipt: success cannot contain failure")
		}
		return "success", nil
	case core.ReceiptRecordPermanent:
		if receipt.Failure == nil || receipt.Failure.Stage == "" ||
			receipt.Failure.Code == "" || receipt.Failure.SanitizedError == "" ||
			receipt.Failure.Action == "" || receipt.Failure.OccurredAt.IsZero() {
			return "", fmt.Errorf("put receipt: permanent failure is incomplete")
		}
		return "record_permanent", nil
	default:
		return "", fmt.Errorf("put receipt: unsupported kind %d", receipt.Kind)
	}
}

func parserOutcomeKindValue(kind core.ParserOutcomeKind) (string, error) {
	switch kind {
	case core.ParserOutcomeSuccess:
		return "success", nil
	case core.ParserOutcomeNoMatch:
		return "no_match", nil
	case core.ParserOutcomeRecordPermanent:
		return "record_permanent", nil
	default:
		return "", fmt.Errorf("put parser outcome: unsupported kind %d", kind)
	}
}

func detectionOutcomeKindValue(kind core.DetectionOutcomeKind) (string, error) {
	switch kind {
	case core.DetectionOutcomeSuccess:
		return "success", nil
	case core.DetectionOutcomeRecordPermanent:
		return "record_permanent", nil
	default:
		return "", fmt.Errorf("put detection outcome: unsupported kind %d", kind)
	}
}

func decisionSourceValue(source core.DecisionSource) (string, error) {
	switch source {
	case core.DecisionSourceAutomatic:
		return "automatic", nil
	case core.DecisionSourceManual:
		return "manual", nil
	default:
		return "", fmt.Errorf("put decision: unsupported source %d", source)
	}
}

func decisionStateValue(state core.DecisionState) (string, error) {
	switch state {
	case core.DecisionActive:
		return "active", nil
	case core.DecisionExpired:
		return "expired", nil
	case core.DecisionRevoked:
		return "revoked", nil
	default:
		return "", fmt.Errorf("put decision: unsupported state %d", state)
	}
}
