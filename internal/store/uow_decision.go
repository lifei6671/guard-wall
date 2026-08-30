package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
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
	result, err := u.tx.ExecContext(ctx, `
		INSERT INTO decisions(
			decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		) VALUES (?, ?, 'automatic', ?, ?, ?, ?, ?, ?, ?, ?, NULL, 'active', NULL, 0)
		ON CONFLICT DO NOTHING`,
		string(value.ID), string(value.NodeID), string(*value.RuleID), string(*value.RuleVersion),
		string(*value.AlertID), value.CanonicalTarget.String(), value.CreatedAt.UTC().UnixMicro(),
		value.UpdatedAt.UTC().UnixMicro(), value.LastTriggeredAt.UTC().UnixMicro(), nullableTime(value.ExpiresAt))
	if err != nil {
		return false, u.fail(fmt.Errorf("insert automatic decision %q: %w", value.ID, err))
	}
	affected, err := result.RowsAffected()
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
	row := u.tx.QueryRowContext(ctx, `
		SELECT decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		FROM decisions
		WHERE node_id = ? AND rule_id = ? AND canonical_target = ?
			AND source = 'automatic' AND state = 'active'`,
		string(nodeID), string(ruleID), target.String())
	value, err := scanDecision(row)
	if err == sql.ErrNoRows {
		return core.Decision{}, false, nil
	}
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
	value, err := scanDecision(u.tx.QueryRowContext(ctx, `
		SELECT decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		FROM decisions WHERE decision_id = ?`, string(id)))
	if err == sql.ErrNoRows {
		return core.Decision{}, false, nil
	}
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
	result, err := u.tx.ExecContext(ctx, `
		UPDATE decisions
		SET last_triggered_at_us = MAX(last_triggered_at_us, ?),
			suppressed_count = suppressed_count + 1
		WHERE decision_id = ? AND source = 'automatic' AND state = 'active'
			AND suppressed_count < 9223372036854775807`,
		triggeredAt.UTC().UnixMicro(), string(id))
	if err != nil {
		return core.Decision{}, u.fail(fmt.Errorf("suppress automatic decision %q: %w", id, err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return core.Decision{}, u.fail(fmt.Errorf("suppress automatic decision %q: affected rows: %w", id, err))
	}
	if affected != 1 {
		return core.Decision{}, u.fail(decision.ErrSuppressionOverflow)
	}
	value, err := scanDecision(u.tx.QueryRowContext(ctx, `
		SELECT decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		FROM decisions WHERE decision_id = ?`, string(id)))
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
	rows, err := u.tx.QueryContext(ctx, `
		SELECT decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		FROM decisions
		WHERE node_id = ? AND canonical_target = ? AND state = 'active'
		ORDER BY decision_id`, string(nodeID), target.String())
	if err != nil {
		return nil, u.fail(fmt.Errorf("list active decisions: %w", err))
	}
	defer rows.Close()
	values := make([]core.Decision, 0)
	for rows.Next() {
		value, err := scanDecision(rows)
		if err != nil {
			return nil, u.fail(fmt.Errorf("list active decisions: scan: %w", err))
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, u.fail(fmt.Errorf("list active decisions: rows: %w", err))
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
	var state string
	var activeCount, revision uint64
	var effectiveUntil sql.NullInt64
	err := u.tx.QueryRowContext(ctx, `
		SELECT state, active_count, effective_until_us, target_projection_revision
		FROM desired_ban_projections
		WHERE node_id = ? AND canonical_target = ?`, string(nodeID), target.String()).Scan(
		&state, &activeCount, &effectiveUntil, &revision,
	)
	if err == sql.ErrNoRows {
		return core.DesiredBanProjection{}, false, nil
	}
	if err != nil {
		return core.DesiredBanProjection{}, false, u.fail(fmt.Errorf("find target projection: %w", err))
	}
	projection := core.DesiredBanProjection{
		NodeID: nodeID, CanonicalTarget: target, ActiveCount: activeCount,
		Revision: core.TargetProjectionRevision(revision),
	}
	switch state {
	case "absent":
		projection.State = core.BanProjectionAbsent
	case "present":
		projection.State = core.BanProjectionPresent
	default:
		return core.DesiredBanProjection{}, false, u.fail(fmt.Errorf("unsupported projection state %q", state))
	}
	if effectiveUntil.Valid {
		value := time.UnixMicro(effectiveUntil.Int64).UTC()
		projection.EffectiveUntil = &value
	}
	if err := projection.Validate(); err != nil {
		return core.DesiredBanProjection{}, false, u.fail(fmt.Errorf("validate persisted projection: %w", err))
	}
	return projection, true, nil
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

type decisionScanner interface {
	Scan(...any) error
}

func scanDecision(scanner decisionScanner) (core.Decision, error) {
	var (
		id, nodeID, source, target, state       string
		ruleID, ruleVersion, alertID, endReason sql.NullString
		createdAt, updatedAt, lastTriggeredAt   int64
		expiresAt, endedAt                      sql.NullInt64
		suppressedCount                         uint64
	)
	if err := scanner.Scan(
		&id, &nodeID, &source, &ruleID, &ruleVersion, &alertID, &target,
		&createdAt, &updatedAt, &lastTriggeredAt, &expiresAt, &endedAt,
		&state, &endReason, &suppressedCount,
	); err != nil {
		return core.Decision{}, err
	}
	prefix, err := netip.ParsePrefix(target)
	if err != nil {
		return core.Decision{}, fmt.Errorf("parse canonical target: %w", err)
	}
	value := core.Decision{
		ID: core.DecisionID(id), NodeID: core.NodeID(nodeID), CanonicalTarget: prefix,
		CreatedAt: time.UnixMicro(createdAt).UTC(), UpdatedAt: time.UnixMicro(updatedAt).UTC(),
		LastTriggeredAt: time.UnixMicro(lastTriggeredAt).UTC(), SuppressedCount: suppressedCount,
	}
	switch source {
	case "automatic":
		value.Source = core.DecisionSourceAutomatic
	case "manual":
		value.Source = core.DecisionSourceManual
	default:
		return core.Decision{}, fmt.Errorf("unsupported decision source %q", source)
	}
	switch state {
	case "active":
		value.State = core.DecisionActive
	case "expired":
		value.State = core.DecisionExpired
	case "revoked":
		value.State = core.DecisionRevoked
	default:
		return core.Decision{}, fmt.Errorf("unsupported decision state %q", state)
	}
	if ruleID.Valid {
		converted := core.RuleID(ruleID.String)
		value.RuleID = &converted
	}
	if ruleVersion.Valid {
		converted := core.RuleVersion(ruleVersion.String)
		value.RuleVersion = &converted
	}
	if alertID.Valid {
		converted := core.AlertID(alertID.String)
		value.AlertID = &converted
	}
	if expiresAt.Valid {
		converted := time.UnixMicro(expiresAt.Int64).UTC()
		value.ExpiresAt = &converted
	}
	if endedAt.Valid {
		converted := time.UnixMicro(endedAt.Int64).UTC()
		value.EndedAt = &converted
	}
	if endReason.Valid {
		converted := core.DecisionEndReason(endReason.String)
		value.EndReason = &converted
	}
	if err := value.Validate(); err != nil {
		return core.Decision{}, fmt.Errorf("validate persisted decision: %w", err)
	}
	return value, nil
}
