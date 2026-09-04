package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/enforcement"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FindTargetEnforcementIntent reads the transaction-local normalized Desired
// intent while deliberately ignoring observed/confirmed columns.
func (u *UnitOfWork) FindTargetEnforcementIntent(
	ctx context.Context,
	nodeID core.NodeID,
	target netip.Prefix,
) (core.NormalizedTargetEnforcementIntent, bool, error) {
	if err := u.ready(ctx); err != nil {
		return core.NormalizedTargetEnforcementIntent{}, false, err
	}
	var row targetEnforcementStateRow
	result := u.transactionORM.WithContext(ctx).
		Where(map[string]any{EnforcementStateColumns.NodeID: string(nodeID), EnforcementStateColumns.CanonicalTarget: target.String()}).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return core.NormalizedTargetEnforcementIntent{}, false, nil
	}
	if result.Error != nil {
		return core.NormalizedTargetEnforcementIntent{}, false,
			u.fail(fmt.Errorf("find target enforcement intent: %w", result.Error))
	}
	value, err := targetEnforcementIntentFromRow(row)
	if err != nil {
		return core.NormalizedTargetEnforcementIntent{}, false,
			u.fail(fmt.Errorf("find target enforcement intent: %w", err))
	}
	return value, true, nil
}

// TargetEnforcementGenerationFloor returns any legacy retry/probe generation
// that predates authoritative Intent materialization. The first Intent must
// advance beyond this fence instead of reusing generation one.
func (u *UnitOfWork) TargetEnforcementGenerationFloor(
	ctx context.Context,
	nodeID core.NodeID,
	target netip.Prefix,
) (core.TargetEnforcementGeneration, bool, error) {
	if err := u.ready(ctx); err != nil {
		return 0, false, err
	}
	var reconcile targetReconcileStateRow
	reconcileResult := u.transactionORM.WithContext(ctx).
		Where(map[string]any{TargetReconcileStateColumns.NodeID: string(nodeID), TargetReconcileStateColumns.CanonicalTarget: target.String()}).
		Take(&reconcile)
	if reconcileResult.Error != nil && !errors.Is(reconcileResult.Error, gorm.ErrRecordNotFound) {
		return 0, false, u.fail(fmt.Errorf("read target generation floor: reconcile state: %w", reconcileResult.Error))
	}
	var probes []reconcileProbeRequirementRow
	probeResult := u.transactionORM.WithContext(ctx).
		Where(map[string]any{
			ReconcileProbeRequirementColumns.NodeID:          string(nodeID),
			ReconcileProbeRequirementColumns.Domain:          "target",
			ReconcileProbeRequirementColumns.CanonicalTarget: target.String(),
		}).
		Find(&probes)
	if probeResult.Error != nil {
		return 0, false, u.fail(fmt.Errorf("read target generation floor: probe requirements: %w", probeResult.Error))
	}
	found := reconcileResult.Error == nil
	floor := int64(0)
	if found {
		floor = reconcile.TargetEnforcementGeneration
	}
	for _, probe := range probes {
		if !found || probe.TargetEnforcementGeneration > floor {
			floor = probe.TargetEnforcementGeneration
			found = true
		}
	}
	if !found {
		return 0, false, nil
	}
	return core.TargetEnforcementGeneration(floor), true, nil
}

// PutTargetEnforcementIntent inserts the first generation or advances an
// existing desired row. Equal exact state is idempotent; stale/conflicting
// generations fail the transaction.
func (u *UnitOfWork) PutTargetEnforcementIntent(
	ctx context.Context,
	intent core.NormalizedTargetEnforcementIntent,
) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := intent.Validate(); err != nil {
		return u.fail(fmt.Errorf("put target enforcement intent: validate: %w", err))
	}
	if uint64(intent.Generation) > math.MaxInt64 {
		return u.fail(fmt.Errorf("put target enforcement intent: generation is exhausted"))
	}
	desiredMembership, timeoutMode, policyCoverage, addressFamily, err := encodeTargetIntent(intent)
	if err != nil {
		return u.fail(err)
	}
	row := targetEnforcementStateRow{
		NodeID: string(intent.NodeID), CanonicalTarget: intent.CanonicalTarget.String(),
		DesiredMembership: desiredMembership, ObservedMembership: "unknown",
		TimeoutMode: timeoutMode, Scopes: int64(intent.Scopes), AddressFamily: addressFamily,
		PolicyCoverage: policyCoverage, PolicyRelationDigest: intent.PolicyRelationDigest,
		BackendAttributesDigest:     intent.BackendAttributesDigest,
		TargetEnforcementGeneration: int64(intent.Generation),
	}
	if intent.EffectiveUntil != nil {
		value := intent.EffectiveUntil.UTC().UnixMicro()
		row.EffectiveUntilUS = &value
	}
	sqlResult := gorm.WithResult()
	result := u.transactionORM.WithContext(ctx).Clauses(sqlResult, clause.OnConflict{
		Columns: []clause.Column{{Name: EnforcementStateColumns.NodeID}, {Name: EnforcementStateColumns.CanonicalTarget}},
		DoUpdates: clause.AssignmentColumns([]string{
			EnforcementStateColumns.DesiredMembership, EnforcementStateColumns.EffectiveUntilUS,
			EnforcementStateColumns.TimeoutMode, EnforcementStateColumns.Scopes, EnforcementStateColumns.AddressFamily,
			EnforcementStateColumns.PolicyCoverage, EnforcementStateColumns.PolicyRelationDigest,
			EnforcementStateColumns.BackendAttributesDigest, EnforcementStateColumns.TargetEnforcementGeneration,
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Gt{
			Column: clause.Column{Table: "excluded", Name: EnforcementStateColumns.TargetEnforcementGeneration},
			Value:  clause.Column{Table: "enforcement_states", Name: EnforcementStateColumns.TargetEnforcementGeneration},
		}}},
	}).Select(
		EnforcementStateColumns.NodeID, EnforcementStateColumns.CanonicalTarget,
		EnforcementStateColumns.DesiredMembership, EnforcementStateColumns.ObservedMembership,
		EnforcementStateColumns.EffectiveUntilUS, EnforcementStateColumns.TimeoutMode,
		EnforcementStateColumns.Scopes, EnforcementStateColumns.AddressFamily,
		EnforcementStateColumns.PolicyCoverage, EnforcementStateColumns.PolicyRelationDigest,
		EnforcementStateColumns.BackendAttributesDigest, EnforcementStateColumns.TargetEnforcementGeneration,
		EnforcementStateColumns.ConfirmedTargetEnforcementGeneration,
		EnforcementStateColumns.ConfirmedSnapshotRevision, EnforcementStateColumns.ObservedAtUS,
	).Create(&row)
	if result.Error != nil {
		return u.fail(fmt.Errorf("put target enforcement intent: %w", result.Error))
	}
	affected, err := sqlResult.Result.RowsAffected()
	if err != nil {
		return u.fail(fmt.Errorf("put target enforcement intent: affected rows: %w", err))
	}
	if affected == 1 {
		return nil
	}
	persisted, found, err := u.FindTargetEnforcementIntent(ctx, intent.NodeID, intent.CanonicalTarget)
	if err != nil {
		return err
	}
	if found && persisted.Generation == intent.Generation && enforcement.Equivalent(persisted, intent) {
		return nil
	}
	return u.fail(fmt.Errorf("put target enforcement intent: stale or conflicting generation"))
}

// ResetTargetReconcileState consumes retry budget only for a new Target
// generation. retry_epoch is preserved for an existing Target.
func (u *UnitOfWork) ResetTargetReconcileState(
	ctx context.Context,
	nodeID core.NodeID,
	target netip.Prefix,
	generation core.TargetEnforcementGeneration,
	updatedAt time.Time,
) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if generation == 0 || uint64(generation) > math.MaxInt64 {
		return u.fail(fmt.Errorf("reset target reconcile state: generation is invalid"))
	}
	if updatedAt.IsZero() || updatedAt.UTC().UnixMicro() <= 0 {
		return u.fail(fmt.Errorf("reset target reconcile state: update time is invalid"))
	}
	sqlResult := gorm.WithResult()
	result := u.transactionORM.WithContext(ctx).Clauses(sqlResult, clause.OnConflict{
		Columns: []clause.Column{{Name: TargetReconcileStateColumns.NodeID}, {Name: TargetReconcileStateColumns.CanonicalTarget}},
		DoUpdates: clause.AssignmentColumns([]string{
			TargetReconcileStateColumns.TargetEnforcementGeneration, TargetReconcileStateColumns.Status,
			TargetReconcileStateColumns.AttemptCount, TargetReconcileStateColumns.LastAttemptAtUS,
			TargetReconcileStateColumns.NextAttemptAtUS, TargetReconcileStateColumns.LastErrorCode,
			TargetReconcileStateColumns.UpdatedAtUS,
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Gt{
			Column: clause.Column{Table: "excluded", Name: TargetReconcileStateColumns.TargetEnforcementGeneration},
			Value:  clause.Column{Table: "target_reconcile_state", Name: TargetReconcileStateColumns.TargetEnforcementGeneration},
		}}},
	}).Create(&targetReconcileStateRow{
		NodeID: string(nodeID), CanonicalTarget: target.String(), TargetEnforcementGeneration: int64(generation),
		RetryEpoch: 0, Status: "pending", AttemptCount: 0, UpdatedAtUS: updatedAt.UTC().UnixMicro(),
	})
	if result.Error != nil {
		return u.fail(fmt.Errorf("reset target reconcile state: %w", result.Error))
	}
	affected, err := sqlResult.Result.RowsAffected()
	if err != nil {
		return u.fail(fmt.Errorf("reset target reconcile state: affected rows: %w", err))
	}
	if affected != 1 {
		return u.fail(fmt.Errorf("reset target reconcile state: stale generation"))
	}
	return nil
}

// AdvanceSnapshotRevision advances the singleton Desired revision exactly
// once and fails before signed SQLite INTEGER exhaustion.
func (u *UnitOfWork) AdvanceSnapshotRevision(ctx context.Context) (core.SnapshotRevision, error) {
	if err := u.ready(ctx); err != nil {
		return 0, err
	}
	var row desiredFirewallStateRow
	result := u.transactionORM.WithContext(ctx).Where(map[string]any{DesiredFirewallStateColumns.Singleton: 1}).Take(&row)
	if result.Error != nil {
		return 0, u.fail(fmt.Errorf("advance snapshot revision: read current revision: %w", result.Error))
	}
	if row.SnapshotRevision >= math.MaxInt64 {
		return 0, u.fail(fmt.Errorf("advance snapshot revision: revision is exhausted"))
	}
	nextRevision := row.SnapshotRevision + 1
	result = u.transactionORM.WithContext(ctx).Model(&desiredFirewallStateRow{}).
		Where(map[string]any{DesiredFirewallStateColumns.Singleton: 1, DesiredFirewallStateColumns.SnapshotRevision: row.SnapshotRevision}).
		Update(DesiredFirewallStateColumns.SnapshotRevision, nextRevision)
	if result.Error != nil {
		return 0, u.fail(fmt.Errorf("advance snapshot revision: %w", result.Error))
	}
	if result.RowsAffected != 1 {
		return 0, u.fail(fmt.Errorf("advance snapshot revision: revision changed concurrently"))
	}
	return core.SnapshotRevision(nextRevision), nil
}

// SnapshotRevision returns the current global Desired revision.
func (s *Store) SnapshotRevision(ctx context.Context) (core.SnapshotRevision, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("read snapshot revision: store is closed")
	}
	if ctx == nil {
		return 0, fmt.Errorf("read snapshot revision: context is required")
	}
	var row desiredFirewallStateRow
	result := s.orm.WithContext(ctx).Where(map[string]any{DesiredFirewallStateColumns.Singleton: 1}).Take(&row)
	if result.Error != nil {
		return 0, fmt.Errorf("read snapshot revision: %w", result.Error)
	}
	return core.SnapshotRevision(row.SnapshotRevision), nil
}

// LoadDesiredTargetState reads one node's normalized Target Desired intents
// together with the global Snapshot revision from the same SQLite snapshot.
func (s *Store) LoadDesiredTargetState(
	ctx context.Context,
	nodeID core.NodeID,
) (revision core.SnapshotRevision, intents []core.NormalizedTargetEnforcementIntent, returnErr error) {
	if err := s.ready(ctx); err != nil {
		return 0, nil, fmt.Errorf("load desired target state: %w", err)
	}
	if !isLowerHex128(string(nodeID)) {
		return 0, nil, fmt.Errorf("load desired target state: node id must be 128-bit lowercase hex")
	}
	transaction, err := s.beginStoreORMTransaction(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, nil, fmt.Errorf("load desired target state: begin read transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := transaction.tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = joinErrors(returnErr,
				fmt.Errorf("load desired target state: rollback read transaction: %w", rollbackErr))
		}
	}()

	if err := requireTransactionNodeIdentity(ctx, transaction.orm, nodeID); err != nil {
		return 0, nil, fmt.Errorf("load desired target state: %w", err)
	}
	var desired desiredFirewallStateRow
	result := transaction.orm.WithContext(ctx).Where(map[string]any{DesiredFirewallStateColumns.Singleton: 1}).Take(&desired)
	if result.Error != nil {
		return 0, nil, fmt.Errorf("load desired target state: read snapshot revision: %w", result.Error)
	}
	intents, err = loadTargetEnforcementIntents(ctx, transaction.orm, nodeID)
	if err != nil {
		return 0, nil, fmt.Errorf("load desired target state: %w", err)
	}
	if err := transaction.tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("load desired target state: commit read transaction: %w", err)
	}
	committed = true
	return core.SnapshotRevision(desired.SnapshotRevision), intents, nil
}

// DesiredFirewallState is the persistence alias for the domain read model.
// Policy is complete and canonical so callers can pass it directly to an
// authoritative Apply request.
type DesiredFirewallState = core.DesiredFirewallState

// LoadDesiredFirewallState reads the global Snapshot revision, complete
// node-scoped policy payload, and normalized Target Desired intents from one
// read-only SQLite transaction. Every persisted policy row must share one
// positive revision, including disabled rows, so a partial policy update is
// never applied.
func (s *Store) LoadDesiredFirewallState(
	ctx context.Context,
	nodeID core.NodeID,
) (state DesiredFirewallState, returnErr error) {
	if err := s.ready(ctx); err != nil {
		return DesiredFirewallState{}, fmt.Errorf("load desired firewall state: %w", err)
	}
	if !isLowerHex128(string(nodeID)) {
		return DesiredFirewallState{}, fmt.Errorf("load desired firewall state: node id must be 128-bit lowercase hex")
	}
	transaction, err := s.beginStoreORMTransaction(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DesiredFirewallState{}, fmt.Errorf("load desired firewall state: begin read transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := transaction.tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = joinErrors(returnErr,
				fmt.Errorf("load desired firewall state: rollback read transaction: %w", rollbackErr))
		}
	}()

	if err := requireTransactionNodeIdentity(ctx, transaction.orm, nodeID); err != nil {
		return DesiredFirewallState{}, fmt.Errorf("load desired firewall state: %w", err)
	}
	var desired desiredFirewallStateRow
	result := transaction.orm.WithContext(ctx).Where(map[string]any{DesiredFirewallStateColumns.Singleton: 1}).Take(&desired)
	if result.Error != nil {
		return DesiredFirewallState{}, fmt.Errorf("load desired firewall state: read snapshot revision: %w", result.Error)
	}

	policyRevision, policy, err := loadManagedPolicyIntent(ctx, transaction.orm, nodeID)
	if err != nil {
		return DesiredFirewallState{}, fmt.Errorf("load desired firewall state: %w", err)
	}
	targets, err := loadTargetEnforcementIntents(ctx, transaction.orm, nodeID)
	if err != nil {
		return DesiredFirewallState{}, fmt.Errorf("load desired firewall state: %w", err)
	}

	if err := transaction.tx.Commit(); err != nil {
		// database/sql 可能先因取消自动回滚，再让 Commit 返回 ErrTxDone。
		// 仅为该结果补回取消身份，其他数据库错误继续原样传播。
		if errors.Is(err, sql.ErrTxDone) && ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		return DesiredFirewallState{}, fmt.Errorf("load desired firewall state: commit read transaction: %w", err)
	}
	committed = true
	return DesiredFirewallState{
		SnapshotRevision: core.SnapshotRevision(desired.SnapshotRevision),
		PolicyRevision:   policyRevision,
		Policy:           policy,
		Targets:          targets,
	}, nil
}

func loadManagedPolicyIntent(
	ctx context.Context,
	orm *gorm.DB,
	nodeID core.NodeID,
) (core.PolicyRevision, core.ManagedPolicyIntent, error) {
	var allowlistRows []allowlistRow
	result := orm.WithContext(ctx).
		Where(map[string]any{AllowlistColumns.NodeID: string(nodeID)}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: AllowlistColumns.CanonicalTarget}}).
		Find(&allowlistRows)
	if result.Error != nil {
		return 0, core.ManagedPolicyIntent{}, fmt.Errorf("read allowlist rows: %w", result.Error)
	}
	var protectedRows []protectedTargetRow
	result = orm.WithContext(ctx).
		Where(map[string]any{ProtectedTargetColumns.NodeID: string(nodeID)}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: ProtectedTargetColumns.CanonicalTarget}}).
		Find(&protectedRows)
	if result.Error != nil {
		return 0, core.ManagedPolicyIntent{}, fmt.Errorf("read protected target rows: %w", result.Error)
	}
	var revision int64
	seenRevision := false
	allowlist := make([]netip.Prefix, 0)
	protectedTargets := make([]netip.Prefix, 0)
	consume := func(source, target string, enabled, persistedRevision int64) error {
		if enabled != 0 && enabled != 1 {
			return fmt.Errorf("policy row %s %q has invalid enabled flag %d", source, target, enabled)
		}
		if persistedRevision <= 0 {
			return fmt.Errorf("policy row %s %q has invalid revision %d", source, target, persistedRevision)
		}
		if !seenRevision {
			revision = persistedRevision
			seenRevision = true
		} else if revision != persistedRevision {
			return fmt.Errorf("policy rows have inconsistent revisions %d and %d", revision, persistedRevision)
		}
		prefix, err := netip.ParsePrefix(target)
		if err != nil || !prefix.IsValid() || prefix != prefix.Masked() {
			return fmt.Errorf("policy row %s has non-canonical target %q", source, target)
		}
		if enabled == 0 {
			return nil
		}
		switch source {
		case "allowlist":
			allowlist = append(allowlist, prefix)
		case "protected_target":
			protectedTargets = append(protectedTargets, prefix)
		default:
			return fmt.Errorf("unsupported policy row source %q", source)
		}
		return nil
	}
	for _, row := range allowlistRows {
		if err := consume("allowlist", row.CanonicalTarget, row.Enabled, row.PolicyRevision); err != nil {
			return 0, core.ManagedPolicyIntent{}, err
		}
	}
	for _, row := range protectedRows {
		if err := consume("protected_target", row.CanonicalTarget, row.Enabled, row.PolicyRevision); err != nil {
			return 0, core.ManagedPolicyIntent{}, err
		}
	}
	if !seenRevision {
		return 0, core.ManagedPolicyIntent{}, fmt.Errorf("policy rows are missing: %w", core.ErrManagedPolicyUninitialized)
	}
	policy, err := core.NewManagedPolicyIntent(allowlist, protectedTargets)
	if err != nil {
		return 0, core.ManagedPolicyIntent{}, fmt.Errorf("construct managed policy intent: %w", err)
	}
	return core.PolicyRevision(revision), policy, nil
}

func loadTargetEnforcementIntents(
	ctx context.Context,
	orm *gorm.DB,
	nodeID core.NodeID,
) ([]core.NormalizedTargetEnforcementIntent, error) {
	var rows []targetEnforcementStateRow
	result := orm.WithContext(ctx).
		Where(map[string]any{EnforcementStateColumns.NodeID: string(nodeID)}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: EnforcementStateColumns.CanonicalTarget}}).
		Find(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("read target intents: %w", result.Error)
	}
	targets := make([]core.NormalizedTargetEnforcementIntent, 0, len(rows))
	for _, row := range rows {
		intent, err := targetEnforcementIntentFromRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode target intent: %w", err)
		}
		targets = append(targets, intent)
	}
	return targets, nil
}

// PendingTargetEnforcementChanges returns one node's materialized
// generation-zero-attempt Target work that can safely be re-woken when a
// durable processing receipt is replayed after a prior post-commit
// notification failure.
func (s *Store) PendingTargetEnforcementChanges(
	ctx context.Context,
	nodeID core.NodeID,
) ([]decision.TargetEnforcementChange, error) {
	if err := s.ready(ctx); err != nil {
		return nil, fmt.Errorf("list pending target enforcement changes: %w", err)
	}
	if !isLowerHex128(string(nodeID)) {
		return nil, fmt.Errorf("list pending target enforcement changes: node id must be 128-bit lowercase hex")
	}
	transaction, err := s.beginStoreORMTransaction(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("list pending target enforcement changes: begin read transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.tx.Rollback()
		}
	}()
	if err := requireTransactionNodeIdentity(ctx, transaction.orm, nodeID); err != nil {
		return nil, fmt.Errorf("list pending target enforcement changes: %w", err)
	}
	var desired desiredFirewallStateRow
	result := transaction.orm.WithContext(ctx).Where(map[string]any{DesiredFirewallStateColumns.Singleton: 1}).Take(&desired)
	if result.Error != nil {
		return nil, fmt.Errorf("list pending target enforcement changes: snapshot revision: %w", result.Error)
	}
	var reconcileRows []targetReconcileStateRow
	result = transaction.orm.WithContext(ctx).
		Where(map[string]any{
			TargetReconcileStateColumns.NodeID:       string(nodeID),
			TargetReconcileStateColumns.Status:       "pending",
			TargetReconcileStateColumns.AttemptCount: 0,
		}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: TargetReconcileStateColumns.CanonicalTarget}}).
		Find(&reconcileRows)
	if result.Error != nil {
		return nil, fmt.Errorf("list pending target enforcement changes: retry states: %w", result.Error)
	}
	var desiredRows []targetEnforcementStateRow
	result = transaction.orm.WithContext(ctx).Where(map[string]any{EnforcementStateColumns.NodeID: string(nodeID)}).Find(&desiredRows)
	if result.Error != nil {
		return nil, fmt.Errorf("list pending target enforcement changes: target intents: %w", result.Error)
	}
	generations := make(map[string]int64, len(desiredRows))
	for _, row := range desiredRows {
		generations[row.CanonicalTarget] = row.TargetEnforcementGeneration
	}
	changes := make([]decision.TargetEnforcementChange, 0, len(reconcileRows))
	for _, row := range reconcileRows {
		generation, found := generations[row.CanonicalTarget]
		if !found || generation != row.TargetEnforcementGeneration {
			continue
		}
		prefix, err := netip.ParsePrefix(row.CanonicalTarget)
		if err != nil {
			return nil, fmt.Errorf("list pending target enforcement changes: parse target: %w", err)
		}
		changes = append(changes, decision.TargetEnforcementChange{
			NodeID: core.NodeID(row.NodeID), Target: prefix,
			Generation:       core.TargetEnforcementGeneration(row.TargetEnforcementGeneration),
			SnapshotRevision: core.SnapshotRevision(desired.SnapshotRevision),
		})
	}
	if err := transaction.tx.Commit(); err != nil {
		return nil, fmt.Errorf("list pending target enforcement changes: commit read transaction: %w", err)
	}
	committed = true
	return changes, nil
}

func targetEnforcementIntentFromRow(row targetEnforcementStateRow) (core.NormalizedTargetEnforcementIntent, error) {
	prefix, err := netip.ParsePrefix(row.CanonicalTarget)
	if err != nil {
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("parse canonical target: %w", err)
	}
	value := core.NormalizedTargetEnforcementIntent{
		NodeID: core.NodeID(row.NodeID), CanonicalTarget: prefix,
		Scopes: core.EnforcementScope(row.Scopes), Generation: core.TargetEnforcementGeneration(row.TargetEnforcementGeneration),
		PolicyRelationDigest: row.PolicyRelationDigest, BackendAttributesDigest: row.BackendAttributesDigest,
	}
	if row.EffectiveUntilUS != nil {
		decoded := time.UnixMicro(*row.EffectiveUntilUS).UTC()
		value.EffectiveUntil = &decoded
	}
	switch row.DesiredMembership {
	case "absent":
		value.BanMembership = core.BanAbsent
	case "present":
		value.BanMembership = core.BanPresent
	default:
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("unsupported desired membership %q", row.DesiredMembership)
	}
	switch row.TimeoutMode {
	case "none":
		value.TimeoutMode = core.TimeoutNone
	case "native":
		value.TimeoutMode = core.TimeoutNative
	default:
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("unsupported timeout mode %q", row.TimeoutMode)
	}
	switch row.PolicyCoverage {
	case "none":
		value.PolicyCoverage = core.PolicyCoverageNone
	case "partial":
		value.PolicyCoverage = core.PolicyCoveragePartial
	case "full":
		value.PolicyCoverage = core.PolicyCoverageFull
	default:
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("unsupported policy coverage %q", row.PolicyCoverage)
	}
	switch row.AddressFamily {
	case 4:
		value.AddressFamily = core.AddressFamilyIPv4
	case 6:
		value.AddressFamily = core.AddressFamilyIPv6
	default:
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("unsupported address family %d", row.AddressFamily)
	}
	if err := value.Validate(); err != nil {
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("validate persisted target intent: %w", err)
	}
	return value, nil
}

func encodeTargetIntent(intent core.NormalizedTargetEnforcementIntent) (string, string, string, int64, error) {
	membership := "absent"
	if intent.BanMembership == core.BanPresent {
		membership = "present"
	}
	timeoutMode := "none"
	if intent.TimeoutMode == core.TimeoutNative {
		timeoutMode = "native"
	}
	policyCoverage := "none"
	switch intent.PolicyCoverage {
	case core.PolicyCoveragePartial:
		policyCoverage = "partial"
	case core.PolicyCoverageFull:
		policyCoverage = "full"
	}
	addressFamily := int64(4)
	if intent.AddressFamily == core.AddressFamilyIPv6 {
		addressFamily = 6
	}
	return membership, timeoutMode, policyCoverage, addressFamily, nil
}
