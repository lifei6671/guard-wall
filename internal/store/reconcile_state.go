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
)

var ErrReconcileStateRegression = errors.New("reconcile state would regress")

const maxPersistedReconcileAttempts uint32 = 6

// LoadReconcileRecovery loads the durable retry ledger and pending-Probe
// requirements for nodeID.
func (s *Store) LoadReconcileRecovery(ctx context.Context, nodeID core.NodeID) (snapshot core.ReconcileRecoverySnapshot, returnErr error) {
	if err := s.ready(ctx); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: %w", err)
	}
	if !isLowerHex128(string(nodeID)) {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: node id must be 128-bit lowercase hex")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: begin read transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = joinErrors(returnErr, rollbackErr)
		}
	}()

	if err := s.requireNodeIdentity(ctx, tx, nodeID); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: %w", err)
	}

	if state, ok, err := loadSingletonReconcileState(ctx, tx, nodeID, core.ReconcileDomainInfrastructure); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: infrastructure state: %w", err)
	} else if ok {
		snapshot.States = append(snapshot.States, state)
	}
	if state, ok, err := loadSingletonReconcileState(ctx, tx, nodeID, core.ReconcileDomainPolicy); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: policy state: %w", err)
	} else if ok {
		snapshot.States = append(snapshot.States, state)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT canonical_target, target_enforcement_generation, retry_epoch, status,
			attempt_count, last_attempt_at_us, next_attempt_at_us, last_error_code, updated_at_us
		FROM target_reconcile_state
		WHERE node_id = ?
		ORDER BY canonical_target`, string(nodeID))
	if err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: target states: %w", err)
	}
	for rows.Next() {
		state, scanErr := scanTargetReconcileState(rows, nodeID)
		if scanErr != nil {
			_ = rows.Close()
			return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: target state: %w", scanErr)
		}
		snapshot.States = append(snapshot.States, state)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: iterate target states: %w", err)
	}
	if err := rows.Close(); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: close target states: %w", err)
	}

	probeRows, err := tx.QueryContext(ctx, `
		SELECT domain, canonical_target, infrastructure_revision, policy_revision,
			target_enforcement_generation, snapshot_revision, fence_snapshot_revision,
			retry_epoch, attempt_count, recorded_at_us
		FROM reconcile_probe_requirements
		WHERE node_id = ?
		ORDER BY domain, canonical_target, infrastructure_revision, policy_revision,
			target_enforcement_generation, snapshot_revision, fence_snapshot_revision,
			retry_epoch, attempt_count`, string(nodeID))
	if err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: probe requirements: %w", err)
	}
	for probeRows.Next() {
		probe, scanErr := scanProbeRequirement(probeRows, nodeID)
		if scanErr != nil {
			_ = probeRows.Close()
			return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: probe requirement: %w", scanErr)
		}
		snapshot.ProbeRequirements = append(snapshot.ProbeRequirements, probe)
	}
	if err := probeRows.Err(); err != nil {
		_ = probeRows.Close()
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: iterate probe requirements: %w", err)
	}
	if err := probeRows.Close(); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: close probe requirements: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: commit read transaction: %w", err)
	}
	committed = true
	return snapshot, nil
}

// ApplyReconcileTransition atomically persists one retry state transition and
// its optional exact pending-Probe mutation.
func (s *Store) ApplyReconcileTransition(ctx context.Context, transition core.ReconcileStateTransition) (returnErr error) {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("apply reconcile transition: %w", err)
	}
	if err := validateReconcileTransition(transition); err != nil {
		return fmt.Errorf("apply reconcile transition: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply reconcile transition: begin: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = joinErrors(returnErr, rollbackErr)
		}
	}()

	nodeID := transition.State.NodeID
	if transition.DeleteOnly {
		nodeID = transition.DeleteProbe.NodeID
	}
	if err := s.requireNodeIdentity(ctx, tx, nodeID); err != nil {
		return fmt.Errorf("apply reconcile transition: %w", err)
	}
	if !transition.DeleteOnly {
		if err := rejectReconcileRegression(ctx, tx, transition.State); err != nil {
			return fmt.Errorf("apply reconcile transition: %w", err)
		}
	}
	if transition.DeleteProbe != nil {
		if err := deleteProbeRequirement(ctx, tx, *transition.DeleteProbe); err != nil {
			return fmt.Errorf("apply reconcile transition: delete probe requirement: %w", err)
		}
	}
	if !transition.DeleteOnly {
		if err := upsertReconcileState(ctx, tx, transition.State); err != nil {
			return fmt.Errorf("apply reconcile transition: write state: %w", err)
		}
	}
	if transition.UpsertProbe != nil {
		if err := upsertProbeRequirement(ctx, tx, *transition.UpsertProbe); err != nil {
			return fmt.Errorf("apply reconcile transition: upsert probe requirement: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return core.NewReconcileCommitUnknownError(fmt.Errorf("apply reconcile transition: commit: %w", err))
	}
	committed = true
	return nil
}

type reconcileQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) requireNodeIdentity(ctx context.Context, queryer reconcileQueryRower, nodeID core.NodeID) error {
	var persisted string
	if err := queryer.QueryRowContext(ctx, "SELECT node_id FROM node_identity WHERE singleton = 1").Scan(&persisted); err != nil {
		return fmt.Errorf("read node identity: %w", err)
	}
	if persisted != string(nodeID) {
		return fmt.Errorf("persisted node %q differs from %q", persisted, nodeID)
	}
	return nil
}

func loadSingletonReconcileState(ctx context.Context, queryer reconcileQueryRower, nodeID core.NodeID, domain core.ReconcileDomain) (core.PersistedReconcileState, bool, error) {
	var query string
	switch domain {
	case core.ReconcileDomainInfrastructure:
		query = `SELECT infrastructure_revision, retry_epoch, status, attempt_count,
			last_attempt_at_us, next_attempt_at_us, last_error_code, updated_at_us
			FROM infrastructure_reconcile_state WHERE singleton = 1`
	case core.ReconcileDomainPolicy:
		query = `SELECT policy_revision, retry_epoch, status, attempt_count,
			last_attempt_at_us, next_attempt_at_us, last_error_code, updated_at_us
			FROM policy_reconcile_state WHERE singleton = 1`
	default:
		return core.PersistedReconcileState{}, false, fmt.Errorf("unsupported domain %d", domain)
	}
	var key, epoch, attempt, updated int64
	var status string
	var lastAttempt, nextAttempt sql.NullInt64
	var errorCode sql.NullString
	err := queryer.QueryRowContext(ctx, query).Scan(
		&key, &epoch, &status, &attempt, &lastAttempt, &nextAttempt, &errorCode, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return core.PersistedReconcileState{}, false, nil
	}
	if err != nil {
		return core.PersistedReconcileState{}, false, err
	}
	state, err := decodedReconcileState(nodeID, domain, key, "", 0, epoch, status, attempt, lastAttempt, nextAttempt, errorCode, updated)
	return state, err == nil, err
}

func scanTargetReconcileState(scanner interface{ Scan(...any) error }, nodeID core.NodeID) (core.PersistedReconcileState, error) {
	var target, status string
	var generation, epoch, attempt, updated int64
	var lastAttempt, nextAttempt sql.NullInt64
	var errorCode sql.NullString
	if err := scanner.Scan(&target, &generation, &epoch, &status, &attempt, &lastAttempt, &nextAttempt, &errorCode, &updated); err != nil {
		return core.PersistedReconcileState{}, err
	}
	return decodedReconcileState(nodeID, core.ReconcileDomainTarget, 0, target, generation, epoch, status, attempt, lastAttempt, nextAttempt, errorCode, updated)
}

func decodedReconcileState(nodeID core.NodeID, domain core.ReconcileDomain, key int64, targetText string, generation, epoch int64, statusText string, attempt int64, lastAttempt, nextAttempt sql.NullInt64, errorCode sql.NullString, updated int64) (core.PersistedReconcileState, error) {
	if key < 0 || generation < 0 || epoch < 0 || attempt < 0 || attempt > math.MaxUint32 || updated <= 0 {
		return core.PersistedReconcileState{}, fmt.Errorf("persisted numeric field is out of range")
	}
	status, err := decodeReconcileStatus(statusText)
	if err != nil {
		return core.PersistedReconcileState{}, err
	}
	state := core.PersistedReconcileState{
		NodeID:     nodeID,
		Domain:     domain,
		RetryEpoch: core.RetryEpoch(epoch),
		RetryState: core.RetryState{
			Status:        status,
			AttemptCount:  uint32(attempt),
			LastAttemptAt: decodeOptionalTime(lastAttempt),
			NextAttemptAt: decodeOptionalTime(nextAttempt),
			LastErrorCode: errorCode.String,
		},
		UpdatedAt: time.UnixMicro(updated).UTC(),
	}
	switch domain {
	case core.ReconcileDomainInfrastructure:
		state.InfrastructureRevision = core.InfrastructureRevision(key)
	case core.ReconcileDomainPolicy:
		state.PolicyRevision = core.PolicyRevision(key)
	case core.ReconcileDomainTarget:
		target, parseErr := netip.ParsePrefix(targetText)
		if parseErr != nil || target != target.Masked() {
			return core.PersistedReconcileState{}, fmt.Errorf("persisted target %q is not canonical", targetText)
		}
		state.Target = target
		state.TargetGeneration = core.TargetEnforcementGeneration(generation)
	}
	if err := validatePersistedReconcileState(state); err != nil {
		return core.PersistedReconcileState{}, fmt.Errorf("persisted state is invalid: %w", err)
	}
	return state, nil
}

func scanProbeRequirement(scanner interface{ Scan(...any) error }, nodeID core.NodeID) (core.PersistedProbeRequirement, error) {
	var domainText, targetText string
	var infrastructure, policy, generation, snapshot, fence, epoch, attempt, recorded int64
	if err := scanner.Scan(&domainText, &targetText, &infrastructure, &policy, &generation, &snapshot, &fence, &epoch, &attempt, &recorded); err != nil {
		return core.PersistedProbeRequirement{}, err
	}
	if infrastructure < 0 || policy < 0 || generation < 0 || snapshot < 0 || epoch < 0 ||
		attempt <= 0 || attempt > math.MaxUint32 || recorded <= 0 || (fence != 0 && fence != 1) {
		return core.PersistedProbeRequirement{}, fmt.Errorf("persisted probe numeric field is out of range")
	}
	domain, err := decodeReconcileDomain(domainText)
	if err != nil {
		return core.PersistedProbeRequirement{}, err
	}
	probe := core.PersistedProbeRequirement{
		NodeID:                 nodeID,
		Domain:                 domain,
		InfrastructureRevision: core.InfrastructureRevision(infrastructure),
		PolicyRevision:         core.PolicyRevision(policy),
		TargetGeneration:       core.TargetEnforcementGeneration(generation),
		SnapshotRevision:       core.SnapshotRevision(snapshot),
		FenceSnapshotRevision:  fence == 1,
		RetryEpoch:             core.RetryEpoch(epoch),
		AttemptCount:           uint32(attempt),
		RecordedAt:             time.UnixMicro(recorded).UTC(),
	}
	if targetText != "" {
		target, parseErr := netip.ParsePrefix(targetText)
		if parseErr != nil || target != target.Masked() {
			return core.PersistedProbeRequirement{}, fmt.Errorf("persisted probe target %q is not canonical", targetText)
		}
		probe.Target = target
	}
	if err := validateProbeRequirement(probe); err != nil {
		return core.PersistedProbeRequirement{}, fmt.Errorf("persisted probe requirement is invalid: %w", err)
	}
	return probe, nil
}

func rejectReconcileRegression(ctx context.Context, tx *sql.Tx, state core.PersistedReconcileState) error {
	var current core.PersistedReconcileState
	var ok bool
	var err error
	if state.Domain == core.ReconcileDomainTarget {
		var target string
		var generation, epoch, attempt int64
		err = tx.QueryRowContext(ctx, `
			SELECT canonical_target, target_enforcement_generation, retry_epoch, attempt_count
			FROM target_reconcile_state WHERE node_id = ? AND canonical_target = ?`,
			string(state.NodeID), state.Target.String()).Scan(&target, &generation, &epoch, &attempt)
		if err == nil {
			if generation < 0 || epoch < 0 || attempt < 0 || attempt > math.MaxUint32 {
				return fmt.Errorf("persisted target version is out of range")
			}
			ok = true
			current.Domain = core.ReconcileDomainTarget
			current.Target = state.Target
			current.TargetGeneration = core.TargetEnforcementGeneration(generation)
			current.RetryEpoch = core.RetryEpoch(epoch)
			current.RetryState.AttemptCount = uint32(attempt)
		}
	} else {
		current, ok, err = loadSingletonReconcileState(ctx, tx, state.NodeID, state.Domain)
	}
	if errors.Is(err, sql.ErrNoRows) || !ok {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read current state: %w", err)
	}
	if compareReconcileVersion(state, current) < 0 {
		return ErrReconcileStateRegression
	}
	return nil
}

func compareReconcileVersion(left, right core.PersistedReconcileState) int {
	leftKey, rightKey := domainKey(left), domainKey(right)
	if leftKey < rightKey {
		return -1
	}
	if leftKey > rightKey {
		return 1
	}
	if left.RetryEpoch < right.RetryEpoch {
		return -1
	}
	if left.RetryEpoch > right.RetryEpoch {
		return 1
	}
	if left.RetryState.AttemptCount < right.RetryState.AttemptCount {
		return -1
	}
	if left.RetryState.AttemptCount > right.RetryState.AttemptCount {
		return 1
	}
	return 0
}

func domainKey(state core.PersistedReconcileState) uint64 {
	switch state.Domain {
	case core.ReconcileDomainInfrastructure:
		return uint64(state.InfrastructureRevision)
	case core.ReconcileDomainPolicy:
		return uint64(state.PolicyRevision)
	case core.ReconcileDomainTarget:
		return uint64(state.TargetGeneration)
	default:
		return 0
	}
}

func upsertReconcileState(ctx context.Context, tx *sql.Tx, state core.PersistedReconcileState) error {
	status := encodeReconcileStatus(state.RetryState.Status)
	lastAttempt := encodeOptionalTime(state.RetryState.LastAttemptAt)
	nextAttempt := encodeOptionalTime(state.RetryState.NextAttemptAt)
	errorCode := nullableText(state.RetryState.LastErrorCode)
	if state.Domain == core.ReconcileDomainInfrastructure {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO infrastructure_reconcile_state(
				singleton, infrastructure_revision, retry_epoch, status, attempt_count,
				last_attempt_at_us, next_attempt_at_us, last_error_code, updated_at_us)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(singleton) DO UPDATE SET
				infrastructure_revision = excluded.infrastructure_revision,
				retry_epoch = excluded.retry_epoch, status = excluded.status,
				attempt_count = excluded.attempt_count,
				last_attempt_at_us = excluded.last_attempt_at_us,
				next_attempt_at_us = excluded.next_attempt_at_us,
				last_error_code = excluded.last_error_code,
				updated_at_us = excluded.updated_at_us`,
			int64(state.InfrastructureRevision), int64(state.RetryEpoch), status,
			int64(state.RetryState.AttemptCount), lastAttempt, nextAttempt, errorCode,
			state.UpdatedAt.UTC().UnixMicro())
		return err
	}
	if state.Domain == core.ReconcileDomainPolicy {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO policy_reconcile_state(
				singleton, policy_revision, retry_epoch, status, attempt_count,
				last_attempt_at_us, next_attempt_at_us, last_error_code, updated_at_us)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(singleton) DO UPDATE SET
				policy_revision = excluded.policy_revision,
				retry_epoch = excluded.retry_epoch, status = excluded.status,
				attempt_count = excluded.attempt_count,
				last_attempt_at_us = excluded.last_attempt_at_us,
				next_attempt_at_us = excluded.next_attempt_at_us,
				last_error_code = excluded.last_error_code,
				updated_at_us = excluded.updated_at_us`,
			int64(state.PolicyRevision), int64(state.RetryEpoch), status,
			int64(state.RetryState.AttemptCount), lastAttempt, nextAttempt, errorCode,
			state.UpdatedAt.UTC().UnixMicro())
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO target_reconcile_state(
			node_id, canonical_target, target_enforcement_generation, retry_epoch,
			status, attempt_count, last_attempt_at_us, next_attempt_at_us,
			last_error_code, updated_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, canonical_target) DO UPDATE SET
			target_enforcement_generation = excluded.target_enforcement_generation,
			retry_epoch = excluded.retry_epoch, status = excluded.status,
			attempt_count = excluded.attempt_count,
			last_attempt_at_us = excluded.last_attempt_at_us,
			next_attempt_at_us = excluded.next_attempt_at_us,
			last_error_code = excluded.last_error_code,
			updated_at_us = excluded.updated_at_us`,
		string(state.NodeID), state.Target.String(), int64(state.TargetGeneration),
		int64(state.RetryEpoch), status, int64(state.RetryState.AttemptCount),
		lastAttempt, nextAttempt, errorCode, state.UpdatedAt.UTC().UnixMicro())
	return err
}

func upsertProbeRequirement(ctx context.Context, tx *sql.Tx, probe core.PersistedProbeRequirement) error {
	values := probeSQLValues(probe)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO reconcile_probe_requirements(
			node_id, domain, canonical_target, infrastructure_revision, policy_revision,
			target_enforcement_generation, snapshot_revision, fence_snapshot_revision,
			retry_epoch, attempt_count, recorded_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(
			node_id, domain, canonical_target, infrastructure_revision, policy_revision,
			target_enforcement_generation, snapshot_revision, fence_snapshot_revision,
			retry_epoch, attempt_count
		) DO UPDATE SET recorded_at_us = excluded.recorded_at_us`, values...)
	return err
}

func deleteProbeRequirement(ctx context.Context, tx *sql.Tx, probe core.PersistedProbeRequirement) error {
	values := probeSQLValues(probe)
	result, err := tx.ExecContext(ctx, `
		DELETE FROM reconcile_probe_requirements
		WHERE node_id = ? AND domain = ? AND canonical_target = ?
			AND infrastructure_revision = ? AND policy_revision = ?
			AND target_enforcement_generation = ? AND snapshot_revision = ?
			AND fence_snapshot_revision = ? AND retry_epoch = ? AND attempt_count = ?`,
		values[:10]...)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted != 1 {
		return fmt.Errorf("exact probe requirement does not exist")
	}
	return nil
}

func probeSQLValues(probe core.PersistedProbeRequirement) []any {
	target := ""
	if probe.Target.IsValid() {
		target = probe.Target.String()
	}
	return []any{
		string(probe.NodeID), encodeReconcileDomain(probe.Domain), target,
		int64(probe.InfrastructureRevision), int64(probe.PolicyRevision),
		int64(probe.TargetGeneration), int64(probe.SnapshotRevision),
		boolToInt(probe.FenceSnapshotRevision), int64(probe.RetryEpoch),
		int64(probe.AttemptCount), probe.RecordedAt.UTC().UnixMicro(),
	}
}

func validateReconcileTransition(transition core.ReconcileStateTransition) error {
	if transition.DeleteOnly {
		if transition.State != (core.PersistedReconcileState{}) {
			return fmt.Errorf("delete-only transition cannot contain state")
		}
		if transition.UpsertProbe != nil || transition.DeleteProbe == nil {
			return fmt.Errorf("delete-only transition requires exactly one deleted probe")
		}
		if err := validateProbeRequirement(*transition.DeleteProbe); err != nil {
			return fmt.Errorf("delete probe: %w", err)
		}
		return nil
	}
	if err := validatePersistedReconcileState(transition.State); err != nil {
		return err
	}
	if transition.UpsertProbe != nil {
		if err := validateProbeRequirement(*transition.UpsertProbe); err != nil {
			return fmt.Errorf("upsert probe: %w", err)
		}
		if !sameReconcileKey(transition.State, *transition.UpsertProbe) ||
			transition.State.RetryEpoch != transition.UpsertProbe.RetryEpoch ||
			transition.State.RetryState.AttemptCount != transition.UpsertProbe.AttemptCount {
			return fmt.Errorf("upsert probe does not match state key, epoch, and attempt")
		}
		if transition.State.RetryState.Status != core.ReconcileApplying &&
			transition.State.RetryState.Status != core.ReconcileRetryWaiting &&
			transition.State.RetryState.Status != core.ReconcileDegraded {
			return fmt.Errorf("upsert probe requires applying, retry-waiting, or degraded state")
		}
	}
	if transition.DeleteProbe != nil {
		if err := validateProbeRequirement(*transition.DeleteProbe); err != nil {
			return fmt.Errorf("delete probe: %w", err)
		}
		if !sameReconcileKey(transition.State, *transition.DeleteProbe) {
			return fmt.Errorf("delete probe does not match state physical key")
		}
	}
	if transition.UpsertProbe != nil && transition.DeleteProbe != nil &&
		(transition.UpsertProbe.SnapshotRevision != transition.DeleteProbe.SnapshotRevision ||
			transition.UpsertProbe.FenceSnapshotRevision != transition.DeleteProbe.FenceSnapshotRevision) {
		return fmt.Errorf("replacement probes do not share the snapshot fence")
	}
	return nil
}

func validatePersistedReconcileState(state core.PersistedReconcileState) error {
	if !isLowerHex128(string(state.NodeID)) {
		return fmt.Errorf("node id must be 128-bit lowercase hex")
	}
	if err := validateReconcileKey(state.Domain, state.InfrastructureRevision, state.PolicyRevision, state.Target, state.TargetGeneration); err != nil {
		return err
	}
	if _, err := sqliteUint64("retry epoch", uint64(state.RetryEpoch)); err != nil {
		return err
	}
	if state.UpdatedAt.IsZero() || state.UpdatedAt.UTC().UnixMicro() <= 0 {
		return fmt.Errorf("updated time is required")
	}
	if len(state.RetryState.LastErrorCode) > 128 {
		return fmt.Errorf("last error code exceeds 128 bytes")
	}
	if state.RetryState.AttemptCount > maxPersistedReconcileAttempts {
		return fmt.Errorf("attempt count exceeds bounded retry budget")
	}
	if _, err := encodeReconcileStatusChecked(state.RetryState.Status); err != nil {
		return err
	}
	if state.RetryState.AttemptCount == 0 {
		if state.RetryState.LastAttemptAt != nil || state.RetryState.NextAttemptAt != nil {
			return fmt.Errorf("zero-attempt state cannot contain attempt times")
		}
	} else if state.RetryState.LastAttemptAt == nil {
		return fmt.Errorf("nonzero-attempt state requires last attempt time")
	}
	if state.RetryState.LastAttemptAt != nil && (state.RetryState.LastAttemptAt.IsZero() || state.RetryState.LastAttemptAt.UTC().UnixMicro() <= 0) {
		return fmt.Errorf("last attempt time is invalid")
	}
	if state.RetryState.Status == core.ReconcileRetryWaiting {
		if state.RetryState.AttemptCount == 0 || state.RetryState.NextAttemptAt == nil {
			return fmt.Errorf("retry-waiting state requires attempt and next attempt time")
		}
		if state.RetryState.LastAttemptAt == nil || !state.RetryState.NextAttemptAt.After(*state.RetryState.LastAttemptAt) {
			return fmt.Errorf("next attempt time must follow last attempt time")
		}
	} else if state.RetryState.NextAttemptAt != nil {
		return fmt.Errorf("next attempt time is only valid for retry-waiting state")
	}
	if state.RetryState.NextAttemptAt != nil && (state.RetryState.NextAttemptAt.IsZero() || state.RetryState.NextAttemptAt.UTC().UnixMicro() <= 0) {
		return fmt.Errorf("next attempt time is invalid")
	}
	if state.RetryState.Status == core.ReconcileApplying && state.RetryState.AttemptCount == 0 {
		return fmt.Errorf("applying state requires a nonzero attempt")
	}
	return nil
}

func validateProbeRequirement(probe core.PersistedProbeRequirement) error {
	if !isLowerHex128(string(probe.NodeID)) {
		return fmt.Errorf("node id must be 128-bit lowercase hex")
	}
	if err := validateReconcileKey(probe.Domain, probe.InfrastructureRevision, probe.PolicyRevision, probe.Target, probe.TargetGeneration); err != nil {
		return err
	}
	if _, err := sqliteUint64("snapshot revision", uint64(probe.SnapshotRevision)); err != nil {
		return err
	}
	if _, err := sqliteUint64("retry epoch", uint64(probe.RetryEpoch)); err != nil {
		return err
	}
	if !probe.FenceSnapshotRevision && probe.SnapshotRevision != 0 {
		return fmt.Errorf("snapshot revision requires its fence flag")
	}
	if probe.FenceSnapshotRevision && probe.SnapshotRevision == 0 {
		return fmt.Errorf("snapshot fence requires a positive snapshot revision")
	}
	if probe.Domain != core.ReconcileDomainInfrastructure && (probe.SnapshotRevision != 0 || probe.FenceSnapshotRevision) {
		return fmt.Errorf("snapshot fencing is only valid for infrastructure")
	}
	if probe.AttemptCount == 0 {
		return fmt.Errorf("probe attempt must be nonzero")
	}
	if probe.AttemptCount > maxPersistedReconcileAttempts {
		return fmt.Errorf("probe attempt exceeds bounded retry budget")
	}
	if probe.RecordedAt.IsZero() || probe.RecordedAt.UTC().UnixMicro() <= 0 {
		return fmt.Errorf("probe recorded time is required")
	}
	return nil
}

func validateReconcileKey(domain core.ReconcileDomain, infrastructure core.InfrastructureRevision, policy core.PolicyRevision, target netip.Prefix, generation core.TargetEnforcementGeneration) error {
	switch domain {
	case core.ReconcileDomainInfrastructure:
		if infrastructure == 0 || policy != 0 || target.IsValid() || generation != 0 {
			return fmt.Errorf("infrastructure domain requires only infrastructure revision")
		}
		_, err := sqliteUint64("infrastructure revision", uint64(infrastructure))
		return err
	case core.ReconcileDomainPolicy:
		if policy == 0 || infrastructure != 0 || target.IsValid() || generation != 0 {
			return fmt.Errorf("policy domain requires only policy revision")
		}
		_, err := sqliteUint64("policy revision", uint64(policy))
		return err
	case core.ReconcileDomainTarget:
		if infrastructure != 0 || policy != 0 || !target.IsValid() || target != target.Masked() || generation == 0 {
			return fmt.Errorf("target domain requires canonical target and generation only")
		}
		if len(target.String()) < 3 || len(target.String()) > 64 {
			return fmt.Errorf("canonical target length is invalid")
		}
		_, err := sqliteUint64("target generation", uint64(generation))
		return err
	default:
		return fmt.Errorf("unsupported reconcile domain %d", domain)
	}
}

func sameReconcileKey(state core.PersistedReconcileState, probe core.PersistedProbeRequirement) bool {
	return state.NodeID == probe.NodeID && state.Domain == probe.Domain &&
		state.InfrastructureRevision == probe.InfrastructureRevision &&
		state.PolicyRevision == probe.PolicyRevision && state.Target == probe.Target &&
		state.TargetGeneration == probe.TargetGeneration
}

func sqliteUint64(name string, value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("%s exceeds SQLite integer range", name)
	}
	return int64(value), nil
}

func encodeReconcileDomain(domain core.ReconcileDomain) string {
	value, _ := encodeReconcileDomainChecked(domain)
	return value
}

func encodeReconcileDomainChecked(domain core.ReconcileDomain) (string, error) {
	switch domain {
	case core.ReconcileDomainInfrastructure:
		return "infrastructure", nil
	case core.ReconcileDomainPolicy:
		return "policy", nil
	case core.ReconcileDomainTarget:
		return "target", nil
	default:
		return "", fmt.Errorf("unsupported reconcile domain %d", domain)
	}
}

func decodeReconcileDomain(value string) (core.ReconcileDomain, error) {
	switch value {
	case "infrastructure":
		return core.ReconcileDomainInfrastructure, nil
	case "policy":
		return core.ReconcileDomainPolicy, nil
	case "target":
		return core.ReconcileDomainTarget, nil
	default:
		return 0, fmt.Errorf("unsupported persisted reconcile domain %q", value)
	}
}

func encodeReconcileStatus(status core.ReconcileStatus) string {
	value, _ := encodeReconcileStatusChecked(status)
	return value
}

func encodeReconcileStatusChecked(status core.ReconcileStatus) (string, error) {
	switch status {
	case core.ReconcilePending:
		return "pending", nil
	case core.ReconcileApplying:
		return "applying", nil
	case core.ReconcileConverged:
		return "converged", nil
	case core.ReconcileRetryWaiting:
		return "retry_waiting", nil
	case core.ReconcileDegraded:
		return "degraded", nil
	default:
		return "", fmt.Errorf("unsupported reconcile status %d", status)
	}
}

func decodeReconcileStatus(value string) (core.ReconcileStatus, error) {
	switch value {
	case "pending":
		return core.ReconcilePending, nil
	case "applying":
		return core.ReconcileApplying, nil
	case "converged":
		return core.ReconcileConverged, nil
	case "retry_waiting":
		return core.ReconcileRetryWaiting, nil
	case "degraded":
		return core.ReconcileDegraded, nil
	default:
		return 0, fmt.Errorf("unsupported persisted reconcile status %q", value)
	}
}

func encodeOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().UnixMicro()
}

func decodeOptionalTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	decoded := time.UnixMicro(value.Int64).UTC()
	return &decoded
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolToInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
