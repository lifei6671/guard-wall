package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReplaceManagedPolicy atomically replaces the complete enabled Policy set.
// expectedRevision is a compare-and-swap fence: zero is valid only before the
// first Policy write, otherwise it must equal the persisted revision.
func (u *UnitOfWork) ReplaceManagedPolicy(
	ctx context.Context,
	nodeID core.NodeID,
	expectedRevision core.PolicyRevision,
	policy core.ManagedPolicyIntent,
	updatedAt time.Time,
) (core.PolicyRevision, core.SnapshotRevision, bool, error) {
	if err := u.ready(ctx); err != nil {
		return 0, 0, false, err
	}
	if err := policy.ValidateComplete(); err != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: validate: %w", err))
	}
	if updatedAt.IsZero() || updatedAt.UTC().UnixMicro() <= 0 {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: update time is invalid"))
	}
	if err := requireTransactionNodeIdentity(ctx, u.transactionORM, nodeID); err != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: %w", err))
	}

	currentRevision, currentPolicy, found, err := loadManagedPolicyIntentIfPresent(ctx, u.transactionORM, nodeID)
	if err != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: read current policy: %w", err))
	}
	if !found {
		if expectedRevision != 0 {
			return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: expected revision %d, policy is uninitialized", expectedRevision))
		}
		currentRevision = 0
	} else {
		if expectedRevision != currentRevision {
			return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: expected revision %d, current revision is %d", expectedRevision, currentRevision))
		}
		if equivalentManagedPolicy(currentPolicy, policy) {
			hasDisabled, err := hasDisabledManagedPolicyRows(ctx, u.transactionORM, nodeID)
			if err != nil {
				return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: inspect canonical rows: %w", err))
			}
			if !hasDisabled {
				return currentRevision, 0, false, nil
			}
		}
	}
	if uint64(currentRevision) >= math.MaxInt64 {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: revision is exhausted"))
	}
	var desired desiredFirewallStateRow
	result := u.transactionORM.WithContext(ctx).Where(map[string]any{DesiredFirewallStateColumns.Singleton: 1}).Take(&desired)
	if result.Error != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: read snapshot revision: %w", result.Error))
	}
	snapshotBefore := desired.SnapshotRevision
	if snapshotBefore < 0 || snapshotBefore >= math.MaxInt64 {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: snapshot revision is exhausted"))
	}
	nextRevision := currentRevision + 1
	// Existing row triggers defend raw writers. Reset their counter only inside
	// this transaction, then publish exactly one logical snapshot increment.
	result = u.transactionORM.WithContext(ctx).Model(&desiredFirewallStateRow{}).
		Where(map[string]any{DesiredFirewallStateColumns.Singleton: 1}).Update(DesiredFirewallStateColumns.SnapshotRevision, 0)
	if result.Error != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: prepare snapshot revision: %w", result.Error))
	}
	result = u.transactionORM.WithContext(ctx).Where(map[string]any{AllowlistColumns.NodeID: string(nodeID)}).Delete(&allowlistRow{})
	if result.Error != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: delete allowlist: %w", result.Error))
	}
	result = u.transactionORM.WithContext(ctx).Where(map[string]any{ProtectedTargetColumns.NodeID: string(nodeID)}).Delete(&protectedTargetRow{})
	if result.Error != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: delete protected targets: %w", result.Error))
	}
	if err := insertManagedPolicyPrefixes(ctx, u.transactionORM, nodeID, policy.Allowlist, nextRevision, updatedAt); err != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: write allowlist: %w", err))
	}
	if err := insertProtectedTargetPrefixes(ctx, u.transactionORM, nodeID, policy.ProtectedTargets, nextRevision, updatedAt); err != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: write protected targets: %w", err))
	}
	snapshotAfter := snapshotBefore + 1
	result = u.transactionORM.WithContext(ctx).Model(&desiredFirewallStateRow{}).
		Where(map[string]any{DesiredFirewallStateColumns.Singleton: 1}).Update(DesiredFirewallStateColumns.SnapshotRevision, snapshotAfter)
	if result.Error != nil {
		return 0, 0, false, u.fail(fmt.Errorf("replace managed policy: publish snapshot revision: %w", result.Error))
	}
	return nextRevision, core.SnapshotRevision(snapshotAfter), true, nil
}

// ReadManagedPolicy returns the complete canonical Policy visible inside the
// caller-owned transaction. It is used by Target policy resolution so a
// replacement and dependent Target intents share one SQLite snapshot.
func (u *UnitOfWork) ReadManagedPolicy(
	ctx context.Context,
	nodeID core.NodeID,
) (core.PolicyRevision, core.ManagedPolicyIntent, error) {
	if err := u.ready(ctx); err != nil {
		return 0, core.ManagedPolicyIntent{}, err
	}
	revision, policy, err := loadManagedPolicyIntent(ctx, u.transactionORM, nodeID)
	if err != nil {
		return 0, core.ManagedPolicyIntent{}, u.fail(fmt.Errorf("read managed policy: %w", err))
	}
	return revision, policy, nil
}

// ResetPolicyReconcileState marks the new Policy revision pending without
// allowing an older controller completion to overwrite its retry ledger.
func (u *UnitOfWork) ResetPolicyReconcileState(
	ctx context.Context,
	revision core.PolicyRevision,
	updatedAt time.Time,
) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if revision == 0 || uint64(revision) > math.MaxInt64 {
		return u.fail(fmt.Errorf("reset policy reconcile state: revision is invalid"))
	}
	if updatedAt.IsZero() || updatedAt.UTC().UnixMicro() <= 0 {
		return u.fail(fmt.Errorf("reset policy reconcile state: update time is invalid"))
	}
	sqlResult := gorm.WithResult()
	result := u.transactionORM.WithContext(ctx).Clauses(sqlResult, clause.OnConflict{
		Columns: []clause.Column{{Name: PolicyReconcileStateColumns.Singleton}},
		DoUpdates: clause.AssignmentColumns([]string{
			PolicyReconcileStateColumns.PolicyRevision, PolicyReconcileStateColumns.RetryEpoch,
			PolicyReconcileStateColumns.Status, PolicyReconcileStateColumns.AttemptCount,
			PolicyReconcileStateColumns.LastAttemptAtUS, PolicyReconcileStateColumns.NextAttemptAtUS,
			PolicyReconcileStateColumns.LastErrorCode, PolicyReconcileStateColumns.UpdatedAtUS,
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Gt{
			Column: clause.Column{Table: "excluded", Name: PolicyReconcileStateColumns.PolicyRevision},
			Value:  clause.Column{Table: "policy_reconcile_state", Name: PolicyReconcileStateColumns.PolicyRevision},
		}}},
	}).Create(&policyReconcileStateRow{
		Singleton: 1, PolicyRevision: int64(revision), RetryEpoch: 0, Status: "pending", AttemptCount: 0,
		UpdatedAtUS: updatedAt.UTC().UnixMicro(),
	})
	if result.Error != nil {
		return u.fail(fmt.Errorf("reset policy reconcile state: %w", result.Error))
	}
	affected, err := sqlResult.Result.RowsAffected()
	if err != nil {
		return u.fail(fmt.Errorf("reset policy reconcile state: affected rows: %w", err))
	}
	if affected != 1 {
		return u.fail(fmt.Errorf("reset policy reconcile state: stale revision"))
	}
	return nil
}

func loadManagedPolicyIntentIfPresent(
	ctx context.Context,
	orm *gorm.DB,
	nodeID core.NodeID,
) (core.PolicyRevision, core.ManagedPolicyIntent, bool, error) {
	var allowlistCount, protectedCount int64
	result := orm.WithContext(ctx).Model(&allowlistRow{}).
		Where(map[string]any{AllowlistColumns.NodeID: string(nodeID)}).Count(&allowlistCount)
	if result.Error != nil {
		return 0, core.ManagedPolicyIntent{}, false, fmt.Errorf("count allowlist rows: %w", result.Error)
	}
	result = orm.WithContext(ctx).Model(&protectedTargetRow{}).
		Where(map[string]any{ProtectedTargetColumns.NodeID: string(nodeID)}).Count(&protectedCount)
	if result.Error != nil {
		return 0, core.ManagedPolicyIntent{}, false, fmt.Errorf("count protected target rows: %w", result.Error)
	}
	if allowlistCount+protectedCount == 0 {
		return 0, core.ManagedPolicyIntent{}, false, nil
	}
	revision, policy, err := loadManagedPolicyIntent(ctx, orm, nodeID)
	return revision, policy, err == nil, err
}

func hasDisabledManagedPolicyRows(ctx context.Context, orm *gorm.DB, nodeID core.NodeID) (bool, error) {
	var allowlistCount, protectedCount int64
	result := orm.WithContext(ctx).Model(&allowlistRow{}).
		Where(map[string]any{AllowlistColumns.NodeID: string(nodeID), AllowlistColumns.Enabled: 0}).Count(&allowlistCount)
	if result.Error != nil {
		return false, fmt.Errorf("read disabled allowlist rows: %w", result.Error)
	}
	result = orm.WithContext(ctx).Model(&protectedTargetRow{}).
		Where(map[string]any{ProtectedTargetColumns.NodeID: string(nodeID), ProtectedTargetColumns.Enabled: 0}).Count(&protectedCount)
	if result.Error != nil {
		return false, fmt.Errorf("read disabled protected target rows: %w", result.Error)
	}
	return allowlistCount+protectedCount != 0, nil
}

func insertManagedPolicyPrefixes(
	ctx context.Context,
	orm *gorm.DB,
	nodeID core.NodeID,
	prefixes []netip.Prefix,
	revision core.PolicyRevision,
	updatedAt time.Time,
) error {
	updatedAtUS := updatedAt.UTC().UnixMicro()
	rows := make([]allowlistRow, 0, len(prefixes))
	for _, prefix := range prefixes {
		rows = append(rows, allowlistRow{
			NodeID: string(nodeID), CanonicalTarget: prefix.String(), Enabled: 1,
			PolicyRevision: int64(revision), CreatedAtUS: updatedAtUS, UpdatedAtUS: updatedAtUS,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	if result := orm.WithContext(ctx).Create(&rows); result.Error != nil {
		return result.Error
	}
	return nil
}

func insertProtectedTargetPrefixes(
	ctx context.Context,
	orm *gorm.DB,
	nodeID core.NodeID,
	prefixes []netip.Prefix,
	revision core.PolicyRevision,
	updatedAt time.Time,
) error {
	updatedAtUS := updatedAt.UTC().UnixMicro()
	rows := make([]protectedTargetRow, 0, len(prefixes))
	for _, prefix := range prefixes {
		rows = append(rows, protectedTargetRow{
			NodeID: string(nodeID), CanonicalTarget: prefix.String(), Enabled: 1,
			PolicyRevision: int64(revision), CreatedAtUS: updatedAtUS, UpdatedAtUS: updatedAtUS,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	if result := orm.WithContext(ctx).Create(&rows); result.Error != nil {
		return result.Error
	}
	return nil
}

func equivalentManagedPolicy(left, right core.ManagedPolicyIntent) bool {
	if left.RelationDigest != right.RelationDigest || len(left.Allowlist) != len(right.Allowlist) || len(left.ProtectedTargets) != len(right.ProtectedTargets) {
		return false
	}
	for index := range left.Allowlist {
		if left.Allowlist[index] != right.Allowlist[index] {
			return false
		}
	}
	for index := range left.ProtectedTargets {
		if left.ProtectedTargets[index] != right.ProtectedTargets[index] {
			return false
		}
	}
	return true
}

func requireTransactionNodeIdentity(ctx context.Context, orm *gorm.DB, nodeID core.NodeID) error {
	var row nodeIdentityRow
	result := orm.WithContext(ctx).Where(map[string]any{NodeIdentityColumns.Singleton: 1}).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("read node identity: missing")
	}
	if result.Error != nil {
		return fmt.Errorf("read node identity: %w", result.Error)
	}
	if row.NodeID != string(nodeID) {
		return fmt.Errorf("persisted node %q differs from %q", row.NodeID, nodeID)
	}
	return nil
}
