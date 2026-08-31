package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

// ApplyObservedFirewallUpdate atomically persists the included physical
// observations after enforcing node, time, and Desired-generation fences.
func (s *Store) ApplyObservedFirewallUpdate(ctx context.Context, update core.ObservedFirewallUpdate) (returnErr error) {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("apply Observed firewall update: %w", err)
	}
	if err := validateObservedFirewallUpdate(update); err != nil {
		return fmt.Errorf("apply Observed firewall update: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply Observed firewall update: begin: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = joinErrors(returnErr,
				fmt.Errorf("apply Observed firewall update: rollback: %w", rollbackErr))
		}
	}()

	if err := s.requireNodeIdentity(ctx, tx, update.NodeID); err != nil {
		return fmt.Errorf("apply Observed firewall update: %w", err)
	}
	if err := writeObservedFirewallUpdate(ctx, tx, update); err != nil {
		return fmt.Errorf("apply Observed firewall update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.NewReconcileCommitUnknownError(
			fmt.Errorf("apply Observed firewall update: commit: %w", err))
	}
	committed = true
	return nil
}

// LoadObservedFirewallSnapshot returns the latest durable physical cache for
// nodeID from one read-only SQLite snapshot.
func (s *Store) LoadObservedFirewallSnapshot(
	ctx context.Context,
	nodeID core.NodeID,
) (snapshot core.ObservedFirewallSnapshot, returnErr error) {
	if err := s.ready(ctx); err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: %w", err)
	}
	if !isLowerHex128(string(nodeID)) {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: node id must be 128-bit lowercase hex")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: begin read transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = joinErrors(returnErr,
				fmt.Errorf("load Observed firewall snapshot: rollback read transaction: %w", rollbackErr))
		}
	}()

	if err := s.requireNodeIdentity(ctx, tx, nodeID); err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: %w", err)
	}
	snapshot.NodeID = nodeID
	if observed, found, err := loadInfrastructureObserved(ctx, tx); err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: infrastructure: %w", err)
	} else if found {
		snapshot.Infrastructure = &observed
	}
	if observed, found, err := loadPolicyObserved(ctx, tx); err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: policy: %w", err)
	} else if found {
		snapshot.Policy = &observed
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT canonical_target, observed_membership, observed_at_us,
			observed_backend, observed_policy_coverage,
			observed_policy_relation_digest, observed_timeout_mode,
			observed_native_expiry_us, observed_scopes,
			observed_address_family, observed_owner_version,
			observed_last_error_code, confirmed_target_enforcement_generation
		FROM enforcement_states
		WHERE node_id = ? AND observed_at_us IS NOT NULL
		ORDER BY canonical_target`, string(nodeID))
	if err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: targets: %w", err)
	}
	snapshot.Targets = make([]core.TargetObservedState, 0)
	for rows.Next() {
		observed, scanErr := scanTargetObservedState(rows)
		if scanErr != nil {
			closeErr := rows.Close()
			if closeErr != nil {
				closeErr = fmt.Errorf("close target rows: %w", closeErr)
			}
			return core.ObservedFirewallSnapshot{}, joinErrors(
				fmt.Errorf("load Observed firewall snapshot: scan target: %w", scanErr),
				closeErr)
		}
		snapshot.Targets = append(snapshot.Targets, observed)
	}
	if err := rows.Err(); err != nil {
		closeErr := rows.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close target rows: %w", closeErr)
		}
		return core.ObservedFirewallSnapshot{}, joinErrors(
			fmt.Errorf("load Observed firewall snapshot: iterate targets: %w", err),
			closeErr)
	}
	if err := rows.Close(); err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: close targets: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: commit read transaction: %w", err)
	}
	committed = true
	return snapshot, nil
}

func validateObservedFirewallUpdate(update core.ObservedFirewallUpdate) error {
	if err := update.Validate(); err != nil {
		return err
	}
	if update.Infrastructure != nil {
		if _, err := sqliteUint64("confirmed infrastructure revision", uint64(update.Infrastructure.ConfirmedRevision)); err != nil {
			return err
		}
	}
	if update.Policy != nil {
		if _, err := sqliteUint64("confirmed policy revision", uint64(update.Policy.ConfirmedRevision)); err != nil {
			return err
		}
	}
	for _, target := range update.Targets {
		if _, err := sqliteUint64("confirmed target generation", uint64(target.ConfirmedGeneration)); err != nil {
			return fmt.Errorf("target %s: %w", target.CanonicalTarget, err)
		}
		if target.BanMembership != core.ObservedMembershipPresent {
			continue
		}
		if target.Scopes&^(core.ScopeInput|core.ScopeForward) != 0 {
			return fmt.Errorf("target %s: unsupported observed scope %d", target.CanonicalTarget, target.Scopes)
		}
		wantFamily := core.AddressFamilyIPv6
		if target.CanonicalTarget.Addr().Is4() {
			wantFamily = core.AddressFamilyIPv4
		}
		if target.AddressFamily != wantFamily {
			return fmt.Errorf("target %s: observed address family does not match target", target.CanonicalTarget)
		}
	}
	return nil
}

func writeObservedFirewallUpdate(ctx context.Context, tx *sql.Tx, update core.ObservedFirewallUpdate) error {
	if update.Infrastructure != nil {
		if err := upsertInfrastructureObserved(ctx, tx, update.NodeID, *update.Infrastructure); err != nil {
			return fmt.Errorf("write infrastructure: %w", err)
		}
	}
	if update.Policy != nil {
		if err := upsertPolicyObserved(ctx, tx, update.NodeID, *update.Policy); err != nil {
			return fmt.Errorf("write policy: %w", err)
		}
	}
	for _, target := range update.Targets {
		if err := updateTargetObserved(ctx, tx, update.NodeID, target); err != nil {
			return fmt.Errorf("write target %s: %w", target.CanonicalTarget, err)
		}
	}
	return nil
}

func upsertInfrastructureObserved(
	ctx context.Context,
	tx *sql.Tx,
	nodeID core.NodeID,
	observed core.InfrastructureObservedState,
) error {
	presence, err := encodeObservedPresence(observed.Presence)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO infrastructure_observed_state(
			singleton, node_id, presence, observed_at_us, backend,
			owner_version, digest, confirmed_infrastructure_revision,
			last_error_code)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			node_id = excluded.node_id,
			presence = excluded.presence,
			observed_at_us = excluded.observed_at_us,
			backend = excluded.backend,
			owner_version = excluded.owner_version,
			digest = excluded.digest,
			confirmed_infrastructure_revision = excluded.confirmed_infrastructure_revision,
			last_error_code = excluded.last_error_code
		WHERE excluded.observed_at_us > infrastructure_observed_state.observed_at_us`,
		string(nodeID), presence, observed.ObservedAt.UTC().UnixMicro(), observed.Backend,
		observed.OwnerVersion, observed.Digest, nullableObservedUint64(uint64(observed.ConfirmedRevision)),
		observed.LastErrorCode)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("affected rows: %w", err)
	}
	if affected == 1 {
		return nil
	}
	persisted, found, err := loadInfrastructureObserved(ctx, tx)
	if err != nil {
		return fmt.Errorf("read existing observation: %w", err)
	}
	if !found {
		return fmt.Errorf("existing observation disappeared")
	}
	return rejectObservedTimeConflict(
		"infrastructure", observed.ObservedAt, persisted.ObservedAt,
		equalInfrastructureObserved(observed, persisted))
}

func upsertPolicyObserved(
	ctx context.Context,
	tx *sql.Tx,
	nodeID core.NodeID,
	observed core.PolicyObservedState,
) error {
	presence, err := encodeObservedPresence(observed.Presence)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO policy_observed_state(
			singleton, node_id, presence, observed_at_us, relation_digest,
			confirmed_policy_revision, last_error_code)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			node_id = excluded.node_id,
			presence = excluded.presence,
			observed_at_us = excluded.observed_at_us,
			relation_digest = excluded.relation_digest,
			confirmed_policy_revision = excluded.confirmed_policy_revision,
			last_error_code = excluded.last_error_code
		WHERE excluded.observed_at_us > policy_observed_state.observed_at_us`,
		string(nodeID), presence, observed.ObservedAt.UTC().UnixMicro(),
		observed.RelationDigest, nullableObservedUint64(uint64(observed.ConfirmedRevision)),
		observed.LastErrorCode)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("affected rows: %w", err)
	}
	if affected == 1 {
		return nil
	}
	persisted, found, err := loadPolicyObserved(ctx, tx)
	if err != nil {
		return fmt.Errorf("read existing observation: %w", err)
	}
	if !found {
		return fmt.Errorf("existing observation disappeared")
	}
	return rejectObservedTimeConflict(
		"policy", observed.ObservedAt, persisted.ObservedAt,
		equalPolicyObserved(observed, persisted))
}

func updateTargetObserved(
	ctx context.Context,
	tx *sql.Tx,
	nodeID core.NodeID,
	observed core.TargetObservedState,
) error {
	desiredGeneration, persisted, found, err := loadTargetObservedForUpdate(
		ctx, tx, nodeID, observed.CanonicalTarget)
	if err != nil {
		return fmt.Errorf("read Desired fence and existing observation: %w", err)
	}
	if desiredGeneration == 0 {
		return fmt.Errorf("Desired target does not exist")
	}
	if observed.ConfirmedGeneration != 0 && observed.ConfirmedGeneration != desiredGeneration {
		return fmt.Errorf("confirmed generation %d differs from current Desired generation %d",
			observed.ConfirmedGeneration, desiredGeneration)
	}
	if found {
		if err := rejectObservedTimeConflict(
			"target", observed.ObservedAt, persisted.ObservedAt,
			equalTargetObserved(observed, persisted)); err != nil {
			return err
		}
		if observed.ObservedAt.UTC().UnixMicro() == persisted.ObservedAt.UTC().UnixMicro() {
			return nil
		}
	}

	membership, err := encodeObservedMembership(observed.BanMembership)
	if err != nil {
		return err
	}
	policyCoverage, err := encodeObservedPolicyCoverage(observed.PolicyCoverage)
	if err != nil {
		return err
	}
	timeoutMode, err := encodeObservedTimeoutMode(observed.TimeoutMode)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE enforcement_states
		SET observed_membership = ?, observed_at_us = ?, observed_backend = ?,
			observed_policy_coverage = ?, observed_policy_relation_digest = ?,
			observed_timeout_mode = ?, observed_native_expiry_us = ?,
			observed_scopes = ?, observed_address_family = ?,
			observed_owner_version = ?, observed_last_error_code = ?,
			confirmed_target_enforcement_generation = ?, confirmed_snapshot_revision = NULL
		WHERE node_id = ? AND canonical_target = ?
			AND target_enforcement_generation = ?
			AND (observed_at_us IS NULL OR observed_at_us < ?)`,
		membership, observed.ObservedAt.UTC().UnixMicro(), observed.Backend,
		policyCoverage, observed.PolicyRelationDigest, timeoutMode,
		encodeOptionalTime(observed.NativeExpiry), int64(observed.Scopes),
		encodeObservedAddressFamily(observed.AddressFamily), observed.OwnerVersion, observed.LastErrorCode,
		nullableObservedUint64(uint64(observed.ConfirmedGeneration)), string(nodeID),
		observed.CanonicalTarget.String(), int64(desiredGeneration),
		observed.ObservedAt.UTC().UnixMicro())
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("affected rows: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("Desired generation or observation changed concurrently")
	}
	return nil
}

func loadInfrastructureObserved(
	ctx context.Context,
	queryer reconcileQueryRower,
) (core.InfrastructureObservedState, bool, error) {
	var presence, backend, ownerVersion, digest, lastError string
	var observedAt int64
	var confirmed sql.NullInt64
	err := queryer.QueryRowContext(ctx, `
		SELECT presence, observed_at_us, backend, owner_version, digest,
			confirmed_infrastructure_revision, last_error_code
		FROM infrastructure_observed_state WHERE singleton = 1`).Scan(
		&presence, &observedAt, &backend, &ownerVersion, &digest, &confirmed, &lastError)
	if errors.Is(err, sql.ErrNoRows) {
		return core.InfrastructureObservedState{}, false, nil
	}
	if err != nil {
		return core.InfrastructureObservedState{}, false, err
	}
	decodedPresence, err := decodeObservedPresence(presence)
	if err != nil {
		return core.InfrastructureObservedState{}, false, err
	}
	observed := core.InfrastructureObservedState{
		Presence: decodedPresence, ObservedAt: time.UnixMicro(observedAt).UTC(),
		Backend: backend, OwnerVersion: ownerVersion, Digest: digest,
		ConfirmedRevision: core.InfrastructureRevision(nullInt64Value(confirmed)),
		LastErrorCode:     lastError,
	}
	if err := observed.Validate(); err != nil {
		return core.InfrastructureObservedState{}, false,
			fmt.Errorf("validate persisted infrastructure observation: %w", err)
	}
	return observed, true, nil
}

func loadPolicyObserved(
	ctx context.Context,
	queryer reconcileQueryRower,
) (core.PolicyObservedState, bool, error) {
	var presence, relationDigest, lastError string
	var observedAt int64
	var confirmed sql.NullInt64
	err := queryer.QueryRowContext(ctx, `
		SELECT presence, observed_at_us, relation_digest,
			confirmed_policy_revision, last_error_code
		FROM policy_observed_state WHERE singleton = 1`).Scan(
		&presence, &observedAt, &relationDigest, &confirmed, &lastError)
	if errors.Is(err, sql.ErrNoRows) {
		return core.PolicyObservedState{}, false, nil
	}
	if err != nil {
		return core.PolicyObservedState{}, false, err
	}
	decodedPresence, err := decodeObservedPresence(presence)
	if err != nil {
		return core.PolicyObservedState{}, false, err
	}
	observed := core.PolicyObservedState{
		Presence: decodedPresence, ObservedAt: time.UnixMicro(observedAt).UTC(),
		RelationDigest:    relationDigest,
		ConfirmedRevision: core.PolicyRevision(nullInt64Value(confirmed)),
		LastErrorCode:     lastError,
	}
	if err := observed.Validate(); err != nil {
		return core.PolicyObservedState{}, false,
			fmt.Errorf("validate persisted policy observation: %w", err)
	}
	return observed, true, nil
}

func loadTargetObservedForUpdate(
	ctx context.Context,
	queryer reconcileQueryRower,
	nodeID core.NodeID,
	target netip.Prefix,
) (core.TargetEnforcementGeneration, core.TargetObservedState, bool, error) {
	var membership, backend, policyCoverage, policyDigest, timeoutMode, ownerVersion, lastError string
	var generation int64
	var observedAt, nativeExpiry, confirmed sql.NullInt64
	var scopes, addressFamily int64
	err := queryer.QueryRowContext(ctx, `
		SELECT target_enforcement_generation, observed_membership, observed_at_us,
			observed_backend, observed_policy_coverage,
			observed_policy_relation_digest, observed_timeout_mode,
			observed_native_expiry_us, observed_scopes,
			observed_address_family, observed_owner_version,
			observed_last_error_code, confirmed_target_enforcement_generation
		FROM enforcement_states
		WHERE node_id = ? AND canonical_target = ?`, string(nodeID), target.String()).Scan(
		&generation, &membership, &observedAt, &backend, &policyCoverage,
		&policyDigest, &timeoutMode, &nativeExpiry, &scopes, &addressFamily,
		&ownerVersion, &lastError, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, core.TargetObservedState{}, false, nil
	}
	if err != nil {
		return 0, core.TargetObservedState{}, false, err
	}
	if !observedAt.Valid {
		return core.TargetEnforcementGeneration(generation), core.TargetObservedState{}, false, nil
	}
	observed, err := decodeTargetObservedState(
		target.String(), membership, observedAt.Int64, backend, policyCoverage,
		policyDigest, timeoutMode, nativeExpiry, scopes, addressFamily,
		ownerVersion, lastError, confirmed)
	if err != nil {
		return 0, core.TargetObservedState{}, false, err
	}
	return core.TargetEnforcementGeneration(generation), observed, true, nil
}

func scanTargetObservedState(scanner rowScanner) (core.TargetObservedState, error) {
	var target, membership, backend, policyCoverage, policyDigest, timeoutMode, ownerVersion, lastError string
	var observedAt, scopes, addressFamily int64
	var nativeExpiry, confirmed sql.NullInt64
	if err := scanner.Scan(
		&target, &membership, &observedAt, &backend, &policyCoverage,
		&policyDigest, &timeoutMode, &nativeExpiry, &scopes, &addressFamily,
		&ownerVersion, &lastError, &confirmed,
	); err != nil {
		return core.TargetObservedState{}, err
	}
	return decodeTargetObservedState(
		target, membership, observedAt, backend, policyCoverage, policyDigest,
		timeoutMode, nativeExpiry, scopes, addressFamily, ownerVersion, lastError, confirmed)
}

func decodeTargetObservedState(
	target, membership string,
	observedAt int64,
	backend, policyCoverage, policyDigest, timeoutMode string,
	nativeExpiry sql.NullInt64,
	scopes, addressFamily int64,
	ownerVersion, lastError string,
	confirmed sql.NullInt64,
) (core.TargetObservedState, error) {
	prefix, err := netip.ParsePrefix(target)
	if err != nil {
		return core.TargetObservedState{}, fmt.Errorf("parse canonical target: %w", err)
	}
	decodedMembership, err := decodeObservedMembership(membership)
	if err != nil {
		return core.TargetObservedState{}, err
	}
	decodedCoverage, err := decodeObservedPolicyCoverage(policyCoverage)
	if err != nil {
		return core.TargetObservedState{}, err
	}
	decodedTimeout, err := decodeObservedTimeoutMode(timeoutMode)
	if err != nil {
		return core.TargetObservedState{}, err
	}
	decodedAddressFamily, err := decodeObservedAddressFamily(addressFamily)
	if err != nil {
		return core.TargetObservedState{}, err
	}
	observed := core.TargetObservedState{
		PhysicalTargetObserved: core.PhysicalTargetObserved{
			CanonicalTarget: prefix, ObservedAt: time.UnixMicro(observedAt).UTC(),
			Backend: backend, BanMembership: decodedMembership,
			PolicyCoverage: decodedCoverage, PolicyRelationDigest: policyDigest,
			TimeoutMode: decodedTimeout, NativeExpiry: decodeOptionalTime(nativeExpiry),
			Scopes: core.EnforcementScope(scopes), AddressFamily: decodedAddressFamily,
			OwnerVersion: ownerVersion, LastErrorCode: lastError,
		},
		ConfirmedGeneration: core.TargetEnforcementGeneration(nullInt64Value(confirmed)),
	}
	if err := observed.Validate(); err != nil {
		return core.TargetObservedState{}, fmt.Errorf("validate persisted target observation: %w", err)
	}
	return observed, nil
}

func rejectObservedTimeConflict(domain string, next, current time.Time, exact bool) error {
	nextMicros := next.UTC().UnixMicro()
	currentMicros := current.UTC().UnixMicro()
	if nextMicros < currentMicros {
		return fmt.Errorf("%s observed time would regress", domain)
	}
	if nextMicros > currentMicros {
		return nil
	}
	if nextMicros == currentMicros && exact {
		return nil
	}
	return fmt.Errorf("%s observation conflicts at the same observed time", domain)
}

func equalInfrastructureObserved(left, right core.InfrastructureObservedState) bool {
	return left.Presence == right.Presence && samePersistedTime(left.ObservedAt, right.ObservedAt) &&
		left.Backend == right.Backend && left.OwnerVersion == right.OwnerVersion &&
		left.Digest == right.Digest && left.ConfirmedRevision == right.ConfirmedRevision &&
		left.LastErrorCode == right.LastErrorCode
}

func equalPolicyObserved(left, right core.PolicyObservedState) bool {
	return left.Presence == right.Presence && samePersistedTime(left.ObservedAt, right.ObservedAt) &&
		left.RelationDigest == right.RelationDigest &&
		left.ConfirmedRevision == right.ConfirmedRevision &&
		left.LastErrorCode == right.LastErrorCode
}

func equalTargetObserved(left, right core.TargetObservedState) bool {
	return left.CanonicalTarget == right.CanonicalTarget &&
		samePersistedTime(left.ObservedAt, right.ObservedAt) && left.Backend == right.Backend &&
		left.BanMembership == right.BanMembership && left.PolicyCoverage == right.PolicyCoverage &&
		left.PolicyRelationDigest == right.PolicyRelationDigest && left.TimeoutMode == right.TimeoutMode &&
		equalOptionalPersistedTime(left.NativeExpiry, right.NativeExpiry) &&
		left.Scopes == right.Scopes && left.AddressFamily == right.AddressFamily &&
		left.OwnerVersion == right.OwnerVersion && left.LastErrorCode == right.LastErrorCode &&
		left.ConfirmedGeneration == right.ConfirmedGeneration
}

func samePersistedTime(left, right time.Time) bool {
	return left.UTC().UnixMicro() == right.UTC().UnixMicro()
}

func equalOptionalPersistedTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return samePersistedTime(*left, *right)
}

func nullableObservedUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return int64(value)
}

func nullInt64Value(value sql.NullInt64) uint64 {
	if !value.Valid {
		return 0
	}
	return uint64(value.Int64)
}

func encodeObservedPresence(value core.ObservedPresence) (string, error) {
	switch value {
	case core.ObservedPresenceUnknown:
		return "unknown", nil
	case core.ObservedPresenceAbsent:
		return "absent", nil
	case core.ObservedPresencePresent:
		return "present", nil
	default:
		return "", fmt.Errorf("unsupported Observed presence %d", value)
	}
}

func decodeObservedPresence(value string) (core.ObservedPresence, error) {
	switch value {
	case "unknown":
		return core.ObservedPresenceUnknown, nil
	case "absent":
		return core.ObservedPresenceAbsent, nil
	case "present":
		return core.ObservedPresencePresent, nil
	default:
		return 0, fmt.Errorf("unsupported persisted Observed presence %q", value)
	}
}

func encodeObservedMembership(value core.ObservedMembership) (string, error) {
	switch value {
	case core.ObservedMembershipUnknown:
		return "unknown", nil
	case core.ObservedMembershipAbsent:
		return "absent", nil
	case core.ObservedMembershipPresent:
		return "present", nil
	default:
		return "", fmt.Errorf("unsupported Observed membership %d", value)
	}
}

func decodeObservedMembership(value string) (core.ObservedMembership, error) {
	switch value {
	case "unknown":
		return core.ObservedMembershipUnknown, nil
	case "absent":
		return core.ObservedMembershipAbsent, nil
	case "present":
		return core.ObservedMembershipPresent, nil
	default:
		return 0, fmt.Errorf("unsupported persisted Observed membership %q", value)
	}
}

func encodeObservedPolicyCoverage(value core.ObservedPolicyCoverage) (string, error) {
	switch value {
	case core.ObservedPolicyUnknown:
		return "unknown", nil
	case core.ObservedPolicyNone:
		return "none", nil
	case core.ObservedPolicyPartial:
		return "partial", nil
	case core.ObservedPolicyFull:
		return "full", nil
	default:
		return "", fmt.Errorf("unsupported Observed policy coverage %d", value)
	}
}

func decodeObservedPolicyCoverage(value string) (core.ObservedPolicyCoverage, error) {
	switch value {
	case "unknown":
		return core.ObservedPolicyUnknown, nil
	case "none":
		return core.ObservedPolicyNone, nil
	case "partial":
		return core.ObservedPolicyPartial, nil
	case "full":
		return core.ObservedPolicyFull, nil
	default:
		return 0, fmt.Errorf("unsupported persisted Observed policy coverage %q", value)
	}
}

func encodeObservedTimeoutMode(value core.TimeoutMode) (string, error) {
	switch value {
	case core.TimeoutNone:
		return "none", nil
	case core.TimeoutNative:
		return "native", nil
	default:
		return "", fmt.Errorf("unsupported Observed timeout mode %d", value)
	}
}

func decodeObservedTimeoutMode(value string) (core.TimeoutMode, error) {
	switch value {
	case "none":
		return core.TimeoutNone, nil
	case "native":
		return core.TimeoutNative, nil
	default:
		return 0, fmt.Errorf("unsupported persisted Observed timeout mode %q", value)
	}
}

func encodeObservedAddressFamily(value core.AddressFamily) int64 {
	if value == core.AddressFamilyIPv6 {
		return 6
	}
	if value == core.AddressFamilyIPv4 {
		return 4
	}
	return 0
}

func decodeObservedAddressFamily(value int64) (core.AddressFamily, error) {
	switch value {
	case 0:
		return 0, nil
	case 4:
		return core.AddressFamilyIPv4, nil
	case 6:
		return core.AddressFamilyIPv6, nil
	default:
		return 0, fmt.Errorf("unsupported persisted Observed address family %d", value)
	}
}
