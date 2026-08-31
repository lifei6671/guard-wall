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
	value, err := scanTargetEnforcementIntent(u.tx.QueryRowContext(ctx, `
		SELECT node_id, canonical_target, desired_membership, effective_until_us,
			timeout_mode, scopes, address_family, policy_coverage,
			policy_relation_digest, backend_attributes_digest,
			target_enforcement_generation
		FROM enforcement_states
		WHERE node_id = ? AND canonical_target = ?`, string(nodeID), target.String()))
	if err == sql.ErrNoRows {
		return core.NormalizedTargetEnforcementIntent{}, false, nil
	}
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
	var floor sql.NullInt64
	if err := u.tx.QueryRowContext(ctx, `
		SELECT max(target_enforcement_generation)
		FROM (
			SELECT target_enforcement_generation
			FROM target_reconcile_state
			WHERE node_id = ? AND canonical_target = ?
			UNION ALL
			SELECT target_enforcement_generation
			FROM reconcile_probe_requirements
			WHERE node_id = ? AND domain = 'target' AND canonical_target = ?
		)`, string(nodeID), target.String(), string(nodeID), target.String()).Scan(&floor); err != nil {
		return 0, false, u.fail(fmt.Errorf("read target generation floor: %w", err))
	}
	if !floor.Valid {
		return 0, false, nil
	}
	return core.TargetEnforcementGeneration(floor.Int64), true, nil
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
	result, err := u.tx.ExecContext(ctx, `
		INSERT INTO enforcement_states(
			node_id, canonical_target, desired_membership, observed_membership,
			effective_until_us, timeout_mode, scopes, address_family,
			policy_coverage, policy_relation_digest, backend_attributes_digest,
			target_enforcement_generation, confirmed_target_enforcement_generation,
			confirmed_snapshot_revision, observed_at_us
		) VALUES (?, ?, ?, 'unknown', ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL)
		ON CONFLICT(node_id, canonical_target) DO UPDATE SET
			desired_membership = excluded.desired_membership,
			effective_until_us = excluded.effective_until_us,
			timeout_mode = excluded.timeout_mode,
			scopes = excluded.scopes,
			address_family = excluded.address_family,
			policy_coverage = excluded.policy_coverage,
			policy_relation_digest = excluded.policy_relation_digest,
			backend_attributes_digest = excluded.backend_attributes_digest,
			target_enforcement_generation = excluded.target_enforcement_generation
		WHERE excluded.target_enforcement_generation > enforcement_states.target_enforcement_generation`,
		string(intent.NodeID), intent.CanonicalTarget.String(), desiredMembership,
		nullableTime(intent.EffectiveUntil), timeoutMode, int64(intent.Scopes), addressFamily,
		policyCoverage, intent.PolicyRelationDigest, intent.BackendAttributesDigest,
		int64(intent.Generation))
	if err != nil {
		return u.fail(fmt.Errorf("put target enforcement intent: %w", err))
	}
	affected, err := result.RowsAffected()
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
	result, err := u.tx.ExecContext(ctx, `
		INSERT INTO target_reconcile_state(
			node_id, canonical_target, target_enforcement_generation,
			retry_epoch, status, attempt_count, last_attempt_at_us,
			next_attempt_at_us, last_error_code, updated_at_us
		) VALUES (?, ?, ?, 0, 'pending', 0, NULL, NULL, NULL, ?)
		ON CONFLICT(node_id, canonical_target) DO UPDATE SET
			target_enforcement_generation = excluded.target_enforcement_generation,
			status = 'pending', attempt_count = 0,
			last_attempt_at_us = NULL, next_attempt_at_us = NULL,
			last_error_code = NULL, updated_at_us = excluded.updated_at_us
		WHERE excluded.target_enforcement_generation > target_reconcile_state.target_enforcement_generation`,
		string(nodeID), target.String(), int64(generation), updatedAt.UTC().UnixMicro())
	if err != nil {
		return u.fail(fmt.Errorf("reset target reconcile state: %w", err))
	}
	affected, err := result.RowsAffected()
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
	var revision int64
	err := u.tx.QueryRowContext(ctx, `
		UPDATE desired_firewall_state
		SET snapshot_revision = snapshot_revision + 1
		WHERE singleton = 1 AND snapshot_revision < 9223372036854775807
		RETURNING snapshot_revision`).Scan(&revision)
	if err == sql.ErrNoRows {
		return 0, u.fail(fmt.Errorf("advance snapshot revision: revision is exhausted"))
	}
	if err != nil {
		return 0, u.fail(fmt.Errorf("advance snapshot revision: %w", err))
	}
	return core.SnapshotRevision(revision), nil
}

// SnapshotRevision returns the current global Desired revision.
func (s *Store) SnapshotRevision(ctx context.Context) (core.SnapshotRevision, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("read snapshot revision: store is closed")
	}
	if ctx == nil {
		return 0, fmt.Errorf("read snapshot revision: context is required")
	}
	var revision int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT snapshot_revision FROM desired_firewall_state WHERE singleton = 1`).Scan(&revision); err != nil {
		return 0, fmt.Errorf("read snapshot revision: %w", err)
	}
	return core.SnapshotRevision(revision), nil
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
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, nil, fmt.Errorf("load desired target state: begin read transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = joinErrors(returnErr,
				fmt.Errorf("load desired target state: rollback read transaction: %w", rollbackErr))
		}
	}()

	if err := s.requireNodeIdentity(ctx, tx, nodeID); err != nil {
		return 0, nil, fmt.Errorf("load desired target state: %w", err)
	}
	var persistedRevision int64
	if err := tx.QueryRowContext(ctx, `
		SELECT snapshot_revision
		FROM desired_firewall_state
		WHERE singleton = 1`).Scan(&persistedRevision); err != nil {
		return 0, nil, fmt.Errorf("load desired target state: read snapshot revision: %w", err)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT node_id, canonical_target, desired_membership, effective_until_us,
			timeout_mode, scopes, address_family, policy_coverage,
			policy_relation_digest, backend_attributes_digest,
			target_enforcement_generation
		FROM enforcement_states
		WHERE node_id = ?
		ORDER BY canonical_target`, string(nodeID))
	if err != nil {
		return 0, nil, fmt.Errorf("load desired target state: read target intents: %w", err)
	}
	intents = make([]core.NormalizedTargetEnforcementIntent, 0)
	for rows.Next() {
		intent, scanErr := scanTargetEnforcementIntent(rows)
		if scanErr != nil {
			closeErr := rows.Close()
			if closeErr != nil {
				closeErr = fmt.Errorf("close target intents: %w", closeErr)
			}
			return 0, nil, joinErrors(
				fmt.Errorf("load desired target state: scan target intent: %w", scanErr),
				closeErr)
		}
		intents = append(intents, intent)
	}
	if err := rows.Err(); err != nil {
		closeErr := rows.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close target intents: %w", closeErr)
		}
		return 0, nil, joinErrors(
			fmt.Errorf("load desired target state: iterate target intents: %w", err),
			closeErr)
	}
	if err := rows.Close(); err != nil {
		return 0, nil, fmt.Errorf("load desired target state: close target intents: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("load desired target state: commit read transaction: %w", err)
	}
	committed = true
	return core.SnapshotRevision(persistedRevision), intents, nil
}

// PendingTargetEnforcementChanges returns materialized generation-zero-attempt
// Target work that can safely be re-woken when a durable processing receipt is
// replayed after a prior post-commit notification failure.
func (s *Store) PendingTargetEnforcementChanges(
	ctx context.Context,
) ([]decision.TargetEnforcementChange, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("list pending target enforcement changes: store is closed")
	}
	if ctx == nil {
		return nil, fmt.Errorf("list pending target enforcement changes: context is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.node_id, r.canonical_target, r.target_enforcement_generation,
			d.snapshot_revision
		FROM target_reconcile_state r
		JOIN enforcement_states e
			ON e.node_id = r.node_id
			AND e.canonical_target = r.canonical_target
			AND e.target_enforcement_generation = r.target_enforcement_generation
		JOIN desired_firewall_state d ON d.singleton = 1
		WHERE r.status = 'pending' AND r.attempt_count = 0
		ORDER BY r.node_id, r.canonical_target`)
	if err != nil {
		return nil, fmt.Errorf("list pending target enforcement changes: %w", err)
	}
	defer rows.Close()
	changes := make([]decision.TargetEnforcementChange, 0)
	for rows.Next() {
		var nodeID, target string
		var generation, revision int64
		if err := rows.Scan(&nodeID, &target, &generation, &revision); err != nil {
			return nil, fmt.Errorf("list pending target enforcement changes: scan: %w", err)
		}
		prefix, err := netip.ParsePrefix(target)
		if err != nil {
			return nil, fmt.Errorf("list pending target enforcement changes: parse target: %w", err)
		}
		changes = append(changes, decision.TargetEnforcementChange{
			NodeID: core.NodeID(nodeID), Target: prefix,
			Generation:       core.TargetEnforcementGeneration(generation),
			SnapshotRevision: core.SnapshotRevision(revision),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pending target enforcement changes: rows: %w", err)
	}
	return changes, nil
}

func scanTargetEnforcementIntent(row interface{ Scan(...any) error }) (core.NormalizedTargetEnforcementIntent, error) {
	var nodeID, target, membership, timeoutMode, policyCoverage string
	var effectiveUntil sql.NullInt64
	var scopes, addressFamily, generation int64
	var policyDigest, backendDigest string
	if err := row.Scan(
		&nodeID, &target, &membership, &effectiveUntil, &timeoutMode, &scopes,
		&addressFamily, &policyCoverage, &policyDigest, &backendDigest, &generation,
	); err != nil {
		return core.NormalizedTargetEnforcementIntent{}, err
	}
	prefix, err := netip.ParsePrefix(target)
	if err != nil {
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("parse canonical target: %w", err)
	}
	value := core.NormalizedTargetEnforcementIntent{
		NodeID: core.NodeID(nodeID), CanonicalTarget: prefix,
		Scopes: core.EnforcementScope(scopes), Generation: core.TargetEnforcementGeneration(generation),
		PolicyRelationDigest: policyDigest, BackendAttributesDigest: backendDigest,
	}
	if effectiveUntil.Valid {
		decoded := time.UnixMicro(effectiveUntil.Int64).UTC()
		value.EffectiveUntil = &decoded
	}
	switch membership {
	case "absent":
		value.BanMembership = core.BanAbsent
	case "present":
		value.BanMembership = core.BanPresent
	default:
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("unsupported desired membership %q", membership)
	}
	switch timeoutMode {
	case "none":
		value.TimeoutMode = core.TimeoutNone
	case "native":
		value.TimeoutMode = core.TimeoutNative
	default:
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("unsupported timeout mode %q", timeoutMode)
	}
	switch policyCoverage {
	case "none":
		value.PolicyCoverage = core.PolicyCoverageNone
	case "partial":
		value.PolicyCoverage = core.PolicyCoveragePartial
	case "full":
		value.PolicyCoverage = core.PolicyCoverageFull
	default:
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("unsupported policy coverage %q", policyCoverage)
	}
	switch addressFamily {
	case 4:
		value.AddressFamily = core.AddressFamilyIPv4
	case 6:
		value.AddressFamily = core.AddressFamilyIPv6
	default:
		return core.NormalizedTargetEnforcementIntent{}, fmt.Errorf("unsupported address family %d", addressFamily)
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
