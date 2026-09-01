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

// RunDecisionTransaction owns one short Manual or expiration transaction.
func (s *Store) RunDecisionTransaction(
	ctx context.Context,
	operation func(decision.LifecycleTransaction) error,
) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("run decision transaction: store is closed")
	}
	if ctx == nil {
		return fmt.Errorf("run decision transaction: context is required")
	}
	if operation == nil {
		return fmt.Errorf("run decision transaction: operation is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("run decision transaction: begin: %w", err)
	}
	uow := s.newUnitOfWork(tx)
	if err := operation(uow); err != nil {
		rollbackErr := uow.Rollback()
		if rollbackErr != nil {
			return joinErrors(fmt.Errorf("run decision transaction: %w", err), rollbackErr)
		}
		return fmt.Errorf("run decision transaction: %w", err)
	}
	if err := uow.Commit(); err != nil {
		return decision.NewCommitUnknownError(fmt.Errorf("run decision transaction: %w", err))
	}
	return nil
}

// InsertManualDecision inserts a candidate without turning the expected
// active-Manual uniqueness conflict into a failed transaction.
func (u *UnitOfWork) InsertManualDecision(ctx context.Context, value core.Decision) (bool, error) {
	if err := u.ready(ctx); err != nil {
		return false, err
	}
	if err := value.Validate(); err != nil {
		return false, u.fail(fmt.Errorf("insert manual decision: validate: %w", err))
	}
	if value.Source != core.DecisionSourceManual || value.RuleID != nil ||
		value.RuleVersion != nil || value.AlertID != nil {
		return false, u.fail(fmt.Errorf("insert manual decision: manual references must be empty"))
	}
	result, err := u.tx.ExecContext(ctx, `
		INSERT INTO decisions(
			decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		) VALUES (?, ?, 'manual', NULL, NULL, NULL, ?, ?, ?, ?, ?, NULL, 'active', NULL, 0)
		ON CONFLICT DO NOTHING`,
		string(value.ID), string(value.NodeID), value.CanonicalTarget.String(),
		value.CreatedAt.UTC().UnixMicro(), value.UpdatedAt.UTC().UnixMicro(),
		value.LastTriggeredAt.UTC().UnixMicro(), nullableTime(value.ExpiresAt))
	if err != nil {
		return false, u.fail(fmt.Errorf("insert manual decision %q: %w", value.ID, err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, u.fail(fmt.Errorf("insert manual decision %q: affected rows: %w", value.ID, err))
	}
	return affected == 1, nil
}

func (u *UnitOfWork) FindActiveManualDecision(
	ctx context.Context,
	nodeID core.NodeID,
	target netip.Prefix,
) (core.Decision, bool, error) {
	if err := u.ready(ctx); err != nil {
		return core.Decision{}, false, err
	}
	value, err := scanDecision(u.tx.QueryRowContext(ctx, `
		SELECT decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		FROM decisions
		WHERE node_id = ? AND canonical_target = ?
			AND source = 'manual' AND state = 'active'`,
		string(nodeID), target.String()))
	if err == sql.ErrNoRows {
		return core.Decision{}, false, nil
	}
	if err != nil {
		return core.Decision{}, false, u.fail(fmt.Errorf("find active manual decision: %w", err))
	}
	return value, true, nil
}

func (u *UnitOfWork) RevokeActiveManualDecision(
	ctx context.Context,
	id core.DecisionID,
	endedAt time.Time,
) (core.Decision, error) {
	if err := u.ready(ctx); err != nil {
		return core.Decision{}, err
	}
	result, err := u.tx.ExecContext(ctx, `
		UPDATE decisions
		SET updated_at_us = ?, ended_at_us = ?, state = 'revoked', end_reason = 'manual_replace'
		WHERE decision_id = ? AND source = 'manual' AND state = 'active'
			AND created_at_us <= ?`,
		endedAt.UTC().UnixMicro(), endedAt.UTC().UnixMicro(), string(id), endedAt.UTC().UnixMicro())
	if err != nil {
		return core.Decision{}, u.fail(fmt.Errorf("revoke active manual decision %q: %w", id, err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return core.Decision{}, u.fail(fmt.Errorf("revoke active manual decision %q: affected rows: %w", id, err))
	}
	if affected != 1 {
		return core.Decision{}, u.fail(fmt.Errorf("revoke active manual decision %q: active row changed", id))
	}
	value, err := scanDecision(u.tx.QueryRowContext(ctx, `
		SELECT decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		FROM decisions WHERE decision_id = ?`, string(id)))
	if err != nil {
		return core.Decision{}, u.fail(fmt.Errorf("read replaced manual decision %q: %w", id, err))
	}
	return value, nil
}

func (u *UnitOfWork) ExpireDueActiveDecisions(ctx context.Context, now time.Time) ([]core.Decision, error) {
	if err := u.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := u.tx.QueryContext(ctx, `
		UPDATE decisions
		SET updated_at_us = ?, ended_at_us = ?, state = 'expired', end_reason = 'expired'
		WHERE state = 'active' AND expires_at_us IS NOT NULL AND expires_at_us <= ?
		RETURNING decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count`,
		now.UTC().UnixMicro(), now.UTC().UnixMicro(), now.UTC().UnixMicro())
	if err != nil {
		return nil, u.fail(fmt.Errorf("expire due active decisions: %w", err))
	}
	defer rows.Close()
	values := make([]core.Decision, 0)
	for rows.Next() {
		value, err := scanDecision(rows)
		if err != nil {
			return nil, u.fail(fmt.Errorf("expire due active decisions: scan: %w", err))
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, u.fail(fmt.Errorf("expire due active decisions: rows: %w", err))
	}
	return values, nil
}

func (u *UnitOfWork) AppendDecisionLifecycleAudit(
	ctx context.Context,
	audit decision.LifecycleAudit,
) error {
	details, err := json.Marshal(struct {
		ReplacementID *core.DecisionID `json:"replacement_decision_id,omitempty"`
	}{ReplacementID: audit.ReplacementID})
	if err != nil {
		return u.fail(fmt.Errorf("append decision lifecycle audit: marshal details: %w", err))
	}
	decisionID := audit.DecisionID
	return u.AppendCriticalAudit(ctx, CriticalAudit{
		ID: audit.ID, IdempotencyKey: audit.IdempotencyKey, NodeID: audit.NodeID,
		Category: "decision", Action: audit.Action, Result: "success", Severity: "info",
		ActorType: audit.ActorType, DecisionID: &decisionID, DetailsJSON: details,
		CreatedAt: audit.CreatedAt,
	})
}
