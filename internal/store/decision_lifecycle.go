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

// RequireNodeIdentity rejects a transaction that does not belong to the
// persisted singleton node before it can write node-scoped lifecycle state.
func (u *UnitOfWork) RequireNodeIdentity(ctx context.Context, nodeID core.NodeID) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if nodeID == "" {
		return u.fail(fmt.Errorf("require node identity: node id is required"))
	}
	result := u.transactionORM.WithContext(ctx).
		Model(&nodeIdentityRow{}).
		Where(&nodeIdentityRow{Singleton: 1, NodeID: string(nodeID)}).
		Update(NodeIdentityColumns.NodeID, string(nodeID))
	if result.Error != nil {
		return u.fail(fmt.Errorf("require node identity: write fence: %w", result.Error))
	}
	if result.RowsAffected != 1 {
		return u.fail(fmt.Errorf("require node identity: persisted node does not match %q", nodeID))
	}
	return nil
}

// RunPolicyTransaction owns one authoritative Policy replacement transaction.
func (s *Store) RunPolicyTransaction(
	ctx context.Context,
	operation func(decision.PolicyTransaction) error,
) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("run policy transaction: store is closed")
	}
	if ctx == nil {
		return fmt.Errorf("run policy transaction: context is required")
	}
	if operation == nil {
		return fmt.Errorf("run policy transaction: operation is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("run policy transaction: begin: %w", err)
	}
	uow := s.newUnitOfWork(tx)
	if err := operation(uow); err != nil {
		rollbackErr := uow.Rollback()
		if rollbackErr != nil {
			return joinErrors(fmt.Errorf("run policy transaction: %w", err), rollbackErr)
		}
		return fmt.Errorf("run policy transaction: %w", err)
	}
	if err := uow.Commit(); err != nil {
		return decision.NewCommitUnknownError(fmt.Errorf("run policy transaction: %w", err))
	}
	return nil
}

// AppendPolicyWriteAudit records the committed authoritative Policy revision.
func (u *UnitOfWork) AppendPolicyWriteAudit(ctx context.Context, audit decision.PolicyWriteAudit) error {
	details, err := json.Marshal(struct {
		PolicyRevision core.PolicyRevision `json:"policy_revision"`
		RelationDigest string              `json:"relation_digest"`
	}{PolicyRevision: audit.PolicyRevision, RelationDigest: audit.RelationDigest})
	if err != nil {
		return u.fail(fmt.Errorf("append policy write audit: marshal details: %w", err))
	}
	return u.AppendCriticalAudit(ctx, CriticalAudit{
		ID: audit.ID, IdempotencyKey: audit.IdempotencyKey, NodeID: audit.NodeID,
		Category: "policy", Action: "replace", Result: "success", Severity: "info",
		ActorType: audit.ActorType, DetailsJSON: details, CreatedAt: audit.CreatedAt,
	})
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
			DecisionID: string(value.ID), NodeID: string(value.NodeID), Source: "manual",
			CanonicalTarget: value.CanonicalTarget.String(),
			CreatedAtUS:     value.CreatedAt.UTC().UnixMicro(), UpdatedAtUS: value.UpdatedAt.UTC().UnixMicro(),
			LastTriggeredAtUS: value.LastTriggeredAt.UTC().UnixMicro(),
			ExpiresAtUS:       decisionTimeMicroseconds(value.ExpiresAt), State: "active",
		})
	if result.Error != nil {
		return false, u.fail(fmt.Errorf("insert manual decision %q: %w", value.ID, result.Error))
	}
	affected, err := sqlResult.Result.RowsAffected()
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
	var row decisionRow
	result := u.transactionORM.WithContext(ctx).
		Where(map[string]any{
			DecisionColumns.NodeID:          string(nodeID),
			DecisionColumns.CanonicalTarget: target.String(),
			DecisionColumns.Source:          "manual", DecisionColumns.State: "active",
		}).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return core.Decision{}, false, nil
	}
	if result.Error != nil {
		return core.Decision{}, false, u.fail(fmt.Errorf("find active manual decision: %w", result.Error))
	}
	value, err := decisionFromRow(row)
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
	row := decisionRow{}
	result := u.transactionORM.WithContext(ctx).
		Model(&row).
		Clauses(clause.Returning{}).
		Where(&decisionRow{DecisionID: string(id), Source: "manual", State: "active"}).
		Clauses(clause.Where{Exprs: []clause.Expression{
			clause.Lte{Column: clause.Column{Name: DecisionColumns.CreatedAtUS}, Value: endedAt.UTC().UnixMicro()},
		}}).
		Updates(map[string]any{
			DecisionColumns.UpdatedAtUS: endedAt.UTC().UnixMicro(),
			DecisionColumns.EndedAtUS:   endedAt.UTC().UnixMicro(),
			DecisionColumns.State:       "revoked",
			DecisionColumns.EndReason:   "manual_replace",
		})
	if result.Error != nil {
		return core.Decision{}, u.fail(fmt.Errorf("revoke active manual decision %q: %w", id, result.Error))
	}
	if result.RowsAffected != 1 {
		return core.Decision{}, u.fail(fmt.Errorf("revoke active manual decision %q: active row changed", id))
	}
	value, err := decisionFromRow(row)
	if err != nil {
		return core.Decision{}, u.fail(fmt.Errorf("read replaced manual decision %q: %w", id, err))
	}
	return value, nil
}

func (u *UnitOfWork) ExpireDueActiveDecisions(
	ctx context.Context,
	nodeID core.NodeID,
	now time.Time,
) ([]core.Decision, error) {
	if err := u.ready(ctx); err != nil {
		return nil, err
	}
	if nodeID == "" {
		return nil, u.fail(fmt.Errorf("expire due active decisions: node id is required"))
	}
	if err := u.RequireNodeIdentity(ctx, nodeID); err != nil {
		return nil, u.fail(fmt.Errorf("expire due active decisions: %w", err))
	}
	rows := make([]decisionRow, 0)
	result := u.transactionORM.WithContext(ctx).
		Where(&decisionRow{NodeID: string(nodeID), State: "active"}).
		Clauses(clause.Where{Exprs: []clause.Expression{
			clause.Lte{Column: clause.Column{Name: DecisionColumns.ExpiresAtUS}, Value: now.UTC().UnixMicro()},
		}}).
		Find(&rows)
	if result.Error != nil {
		return nil, u.fail(fmt.Errorf("expire due active decisions: %w", result.Error))
	}
	values := make([]core.Decision, 0, len(rows))
	for _, row := range rows {
		endedAtUS := now.UTC().UnixMicro()
		endReason := "expired"
		result = u.transactionORM.WithContext(ctx).
			Model(&decisionRow{}).
			Where(&decisionRow{
				DecisionID: row.DecisionID, NodeID: string(nodeID), State: "active",
				ExpiresAtUS: row.ExpiresAtUS,
			}).
			Updates(map[string]any{
				DecisionColumns.UpdatedAtUS: endedAtUS,
				DecisionColumns.EndedAtUS:   endedAtUS,
				DecisionColumns.State:       "expired",
				DecisionColumns.EndReason:   endReason,
			})
		if result.Error != nil {
			return nil, u.fail(fmt.Errorf("expire due active decisions: %w", result.Error))
		}
		if result.RowsAffected != 1 {
			return nil, u.fail(fmt.Errorf("expire due active decisions: active row changed"))
		}
		row.UpdatedAtUS = endedAtUS
		row.EndedAtUS = &endedAtUS
		row.State = "expired"
		row.EndReason = &endReason
		value, err := decisionFromRow(row)
		if err != nil {
			return nil, u.fail(fmt.Errorf("expire due active decisions: read returned decision: %w", err))
		}
		values = append(values, value)
	}
	return values, nil
}

func decisionTimeMicroseconds(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	result := value.UTC().UnixMicro()
	return &result
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
