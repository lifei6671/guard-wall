package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InsertAutomaticDecision inserts a candidate without treating the expected
// active-automatic uniqueness conflict as a failed UnitOfWork.
func (u *UnitOfWork) InsertAutomaticDecision(ctx context.Context, value core.Decision) (bool, error) {
	if err := u.ready(ctx); err != nil {
		return false, err
	}
	if err := value.Validate(); err != nil {
		return false, u.fail(fmt.Errorf("insert automatic decision: validate: %w", err))
	}
	if value.Source != core.DecisionSourceAutomatic || value.RuleID == nil ||
		value.RuleVersion == nil || value.AlertID == nil {
		return false, u.fail(fmt.Errorf("insert automatic decision: automatic references are required"))
	}
	ruleID := string(*value.RuleID)
	ruleVersion := string(*value.RuleVersion)
	alertID := string(*value.AlertID)
	sqlResult := gorm.WithResult()
	result := u.transactionORM.WithContext(ctx).
		Clauses(sqlResult, clause.OnConflict{DoNothing: true}).
		Select(
			DecisionColumns.DecisionID, DecisionColumns.NodeID, DecisionColumns.Source,
			DecisionColumns.RuleID, DecisionColumns.RuleVersion, DecisionColumns.AlertID,
			DecisionColumns.CanonicalTarget, DecisionColumns.CreatedAtUS, DecisionColumns.UpdatedAtUS,
			DecisionColumns.LastTriggeredAtUS, DecisionColumns.ExpiresAtUS, DecisionColumns.EndedAtUS,
			DecisionColumns.State, DecisionColumns.EndReason, DecisionColumns.SuppressedCount,
		).
		Create(&decisionRow{
			DecisionID: string(value.ID), NodeID: string(value.NodeID), Source: "automatic",
			RuleID: &ruleID, RuleVersion: &ruleVersion, AlertID: &alertID,
			CanonicalTarget: value.CanonicalTarget.String(),
			CreatedAtUS:     value.CreatedAt.UTC().UnixMicro(), UpdatedAtUS: value.UpdatedAt.UTC().UnixMicro(),
			LastTriggeredAtUS: value.LastTriggeredAt.UTC().UnixMicro(),
			ExpiresAtUS:       decisionTimeMicroseconds(value.ExpiresAt), State: "active",
		})
	if result.Error != nil {
		return false, u.fail(fmt.Errorf("insert automatic decision %q: %w", value.ID, result.Error))
	}
	affected, err := sqlResult.Result.RowsAffected()
	if err != nil {
		return false, u.fail(fmt.Errorf("insert automatic decision %q: affected rows: %w", value.ID, err))
	}
	return affected == 1, nil
}

func (u *UnitOfWork) FindActiveAutomaticDecision(
	ctx context.Context,
	nodeID core.NodeID,
	ruleID core.RuleID,
	target netip.Prefix,
) (core.Decision, bool, error) {
	if err := u.ready(ctx); err != nil {
		return core.Decision{}, false, err
	}
	var row decisionRow
	result := u.transactionORM.WithContext(ctx).
		Where(map[string]any{
			DecisionColumns.NodeID: string(nodeID), DecisionColumns.RuleID: string(ruleID),
			DecisionColumns.CanonicalTarget: target.String(), DecisionColumns.Source: "automatic",
			DecisionColumns.State: "active",
		}).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return core.Decision{}, false, nil
	}
	if result.Error != nil {
		return core.Decision{}, false, u.fail(fmt.Errorf("find active automatic decision: %w", result.Error))
	}
	value, err := decisionFromRow(row)
	if err != nil {
		return core.Decision{}, false, u.fail(fmt.Errorf("find active automatic decision: %w", err))
	}
	return value, true, nil
}

func (u *UnitOfWork) FindDecisionByID(
	ctx context.Context,
	id core.DecisionID,
) (core.Decision, bool, error) {
	if err := u.ready(ctx); err != nil {
		return core.Decision{}, false, err
	}
	var row decisionRow
	result := u.transactionORM.WithContext(ctx).
		Where(&decisionRow{DecisionID: string(id)}).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return core.Decision{}, false, nil
	}
	if result.Error != nil {
		return core.Decision{}, false, u.fail(fmt.Errorf("find decision %q: %w", id, result.Error))
	}
	value, err := decisionFromRow(row)
	if err != nil {
		return core.Decision{}, false, u.fail(fmt.Errorf("find decision %q: %w", id, err))
	}
	return value, true, nil
}

func (u *UnitOfWork) SuppressAutomaticDecision(
	ctx context.Context,
	id core.DecisionID,
	triggeredAt time.Time,
) (core.Decision, error) {
	if err := u.ready(ctx); err != nil {
		return core.Decision{}, err
	}
	var row decisionRow
	result := u.transactionORM.WithContext(ctx).
		Where(&decisionRow{DecisionID: string(id), Source: "automatic", State: "active"}).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return core.Decision{}, u.fail(decision.ErrSuppressionOverflow)
	}
	if result.Error != nil {
		return core.Decision{}, u.fail(fmt.Errorf("suppress automatic decision %q: %w", id, result.Error))
	}
	const maxSuppressedCount uint64 = ^uint64(0) >> 1
	if row.SuppressedCount >= maxSuppressedCount {
		return core.Decision{}, u.fail(decision.ErrSuppressionOverflow)
	}
	lastTriggeredAtUS := triggeredAt.UTC().UnixMicro()
	if row.LastTriggeredAtUS > lastTriggeredAtUS {
		lastTriggeredAtUS = row.LastTriggeredAtUS
	}
	suppressedCount := row.SuppressedCount + 1
	result = u.transactionORM.WithContext(ctx).
		Model(&decisionRow{}).
		Where(&decisionRow{
			DecisionID: string(id), Source: "automatic", State: "active",
			SuppressedCount: row.SuppressedCount,
		}).
		Updates(map[string]any{
			DecisionColumns.LastTriggeredAtUS: lastTriggeredAtUS,
			DecisionColumns.SuppressedCount:   suppressedCount,
		})
	if result.Error != nil {
		return core.Decision{}, u.fail(fmt.Errorf("suppress automatic decision %q: %w", id, result.Error))
	}
	if result.RowsAffected != 1 {
		return core.Decision{}, u.fail(decision.ErrSuppressionOverflow)
	}
	row.LastTriggeredAtUS = lastTriggeredAtUS
	row.SuppressedCount = suppressedCount
	value, err := decisionFromRow(row)
	if err != nil {
		return core.Decision{}, u.fail(fmt.Errorf("read suppressed automatic decision %q: %w", id, err))
	}
	return value, nil
}

func (u *UnitOfWork) ListActiveDecisions(
	ctx context.Context,
	nodeID core.NodeID,
	target netip.Prefix,
) ([]core.Decision, error) {
	if err := u.ready(ctx); err != nil {
		return nil, err
	}
	rows := make([]decisionRow, 0)
	result := u.transactionORM.WithContext(ctx).
		Where(map[string]any{
			DecisionColumns.NodeID:          string(nodeID),
			DecisionColumns.CanonicalTarget: target.String(),
			DecisionColumns.State:           "active",
		}).
		Clauses(orderByColumns(DecisionColumns.DecisionID)).
		Find(&rows)
	if result.Error != nil {
		return nil, u.fail(fmt.Errorf("list active decisions: %w", result.Error))
	}
	values := make([]core.Decision, 0, len(rows))
	for _, row := range rows {
		value, err := decisionFromRow(row)
		if err != nil {
			return nil, u.fail(fmt.Errorf("list active decisions: decode persisted row: %w", err))
		}
		values = append(values, value)
	}
	return values, nil
}

func (u *UnitOfWork) FindDecisionProjection(
	ctx context.Context,
	nodeID core.NodeID,
	target netip.Prefix,
) (core.DesiredBanProjection, bool, error) {
	if err := u.ready(ctx); err != nil {
		return core.DesiredBanProjection{}, false, err
	}
	var row desiredBanProjectionRow
	result := u.transactionORM.WithContext(ctx).
		Where(&desiredBanProjectionRow{NodeID: string(nodeID), CanonicalTarget: target.String()}).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return core.DesiredBanProjection{}, false, nil
	}
	if result.Error != nil {
		return core.DesiredBanProjection{}, false, u.fail(fmt.Errorf("find target projection: %w", result.Error))
	}
	projection, err := projectionFromRow(row)
	if err != nil {
		return core.DesiredBanProjection{}, false, u.fail(fmt.Errorf("find target projection: %w", err))
	}
	return projection, true, nil
}

// ListDecisionProjections returns the complete materialized Projection set in
// canonical target order for one node inside the caller-owned transaction.
func (u *UnitOfWork) ListDecisionProjections(
	ctx context.Context,
	nodeID core.NodeID,
) ([]core.DesiredBanProjection, error) {
	if err := u.ready(ctx); err != nil {
		return nil, err
	}
	rows := make([]desiredBanProjectionRow, 0)
	result := u.transactionORM.WithContext(ctx).
		Where(&desiredBanProjectionRow{NodeID: string(nodeID)}).
		Clauses(orderByColumns(DesiredBanProjectionColumns.CanonicalTarget)).
		Find(&rows)
	if result.Error != nil {
		return nil, u.fail(fmt.Errorf("list decision projections: %w", result.Error))
	}
	projections := make([]core.DesiredBanProjection, 0, len(rows))
	for _, row := range rows {
		projection, err := projectionFromRow(row)
		if err != nil {
			return nil, u.fail(fmt.Errorf("list decision projections: %w", err))
		}
		if projection.NodeID != nodeID {
			return nil, u.fail(fmt.Errorf("list decision projections: persisted node %q differs from %q", projection.NodeID, nodeID))
		}
		projections = append(projections, projection)
	}
	return projections, nil
}

func (u *UnitOfWork) PutDecisionProjection(
	ctx context.Context,
	projection core.DesiredBanProjection,
	updatedAt time.Time,
) error {
	return u.PutProjection(ctx, projection, updatedAt)
}

func (u *UnitOfWork) AppendAutomaticCreatedAudit(ctx context.Context, audit decision.AutomaticCreatedAudit) error {
	details := []byte(`{"source":"automatic"}`)
	return u.AppendCriticalAudit(ctx, CriticalAudit{
		ID: audit.ID, IdempotencyKey: audit.IdempotencyKey, NodeID: audit.NodeID,
		Category: "decision", Action: "automatic_create", Result: "success", Severity: "info",
		ActorType: "system", DeliveryID: audit.DeliveryID, AlertID: &audit.AlertID,
		DecisionID: &audit.DecisionID, DetailsJSON: details, CreatedAt: audit.CreatedAt,
	})
}

func (u *UnitOfWork) AppendAutomaticSuppressedAudit(ctx context.Context, audit decision.AutomaticSuppressedAudit) error {
	details, err := json.Marshal(struct {
		Source      string           `json:"source"`
		EventID     core.EventID     `json:"event_id"`
		RuleID      core.RuleID      `json:"rule_id"`
		RuleVersion core.RuleVersion `json:"rule_version"`
	}{"automatic", audit.EventID, audit.RuleID, audit.RuleVersion})
	if err != nil {
		return u.fail(fmt.Errorf("append automatic suppression audit: marshal details: %w", err))
	}
	deliveryID := audit.DeliveryID
	return u.AppendCriticalAudit(ctx, CriticalAudit{
		ID: audit.ID, IdempotencyKey: audit.IdempotencyKey, NodeID: audit.NodeID,
		Category: "decision", Action: "automatic_suppress", Result: "success", Severity: "info",
		ActorType: "system", DeliveryID: &deliveryID, AlertID: &audit.AlertID,
		DecisionID: &audit.DecisionID, DetailsJSON: details, CreatedAt: audit.CreatedAt,
	})
}

func decisionFromRow(row decisionRow) (core.Decision, error) {
	prefix, err := netip.ParsePrefix(row.CanonicalTarget)
	if err != nil {
		return core.Decision{}, fmt.Errorf("parse canonical target: %w", err)
	}
	value := core.Decision{
		ID: core.DecisionID(row.DecisionID), NodeID: core.NodeID(row.NodeID), CanonicalTarget: prefix,
		CreatedAt: time.UnixMicro(row.CreatedAtUS).UTC(), UpdatedAt: time.UnixMicro(row.UpdatedAtUS).UTC(),
		LastTriggeredAt: time.UnixMicro(row.LastTriggeredAtUS).UTC(), SuppressedCount: row.SuppressedCount,
	}
	switch row.Source {
	case "automatic":
		value.Source = core.DecisionSourceAutomatic
	case "manual":
		value.Source = core.DecisionSourceManual
	default:
		return core.Decision{}, fmt.Errorf("unsupported decision source %q", row.Source)
	}
	switch row.State {
	case "active":
		value.State = core.DecisionActive
	case "expired":
		value.State = core.DecisionExpired
	case "revoked":
		value.State = core.DecisionRevoked
	default:
		return core.Decision{}, fmt.Errorf("unsupported decision state %q", row.State)
	}
	if row.RuleID != nil {
		converted := core.RuleID(*row.RuleID)
		value.RuleID = &converted
	}
	if row.RuleVersion != nil {
		converted := core.RuleVersion(*row.RuleVersion)
		value.RuleVersion = &converted
	}
	if row.AlertID != nil {
		converted := core.AlertID(*row.AlertID)
		value.AlertID = &converted
	}
	if row.ExpiresAtUS != nil {
		converted := time.UnixMicro(*row.ExpiresAtUS).UTC()
		value.ExpiresAt = &converted
	}
	if row.EndedAtUS != nil {
		converted := time.UnixMicro(*row.EndedAtUS).UTC()
		value.EndedAt = &converted
	}
	if row.EndReason != nil {
		converted := core.DecisionEndReason(*row.EndReason)
		value.EndReason = &converted
	}
	if err := value.Validate(); err != nil {
		return core.Decision{}, fmt.Errorf("validate persisted decision: %w", err)
	}
	return value, nil
}

func projectionFromRow(row desiredBanProjectionRow) (core.DesiredBanProjection, error) {
	prefix, err := netip.ParsePrefix(row.CanonicalTarget)
	if err != nil || !prefix.IsValid() || prefix != prefix.Masked() {
		return core.DesiredBanProjection{}, fmt.Errorf("persisted target %q is not canonical", row.CanonicalTarget)
	}
	projection := core.DesiredBanProjection{
		NodeID: core.NodeID(row.NodeID), CanonicalTarget: prefix,
		ActiveCount: row.ActiveCount, Revision: row.TargetProjectionRevision,
	}
	switch row.State {
	case "absent":
		projection.State = core.BanProjectionAbsent
	case "present":
		projection.State = core.BanProjectionPresent
	default:
		return core.DesiredBanProjection{}, fmt.Errorf("unsupported projection state %q", row.State)
	}
	if row.EffectiveUntilUS != nil {
		value := time.UnixMicro(*row.EffectiveUntilUS).UTC()
		projection.EffectiveUntil = &value
	}
	if err := projection.Validate(); err != nil {
		return core.DesiredBanProjection{}, fmt.Errorf("validate persisted projection: %w", err)
	}
	return projection, nil
}
