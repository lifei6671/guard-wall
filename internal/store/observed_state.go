package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply Observed firewall update: begin: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := transaction.tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = joinErrors(returnErr,
				fmt.Errorf("apply Observed firewall update: rollback: %w", rollbackErr))
		}
	}()

	if err := requireObservedNodeIdentity(ctx, transaction.orm, update.NodeID); err != nil {
		return fmt.Errorf("apply Observed firewall update: %w", err)
	}
	if err := writeObservedFirewallUpdate(ctx, transaction.orm, update); err != nil {
		return fmt.Errorf("apply Observed firewall update: %w", err)
	}
	if err := transaction.tx.Commit(); err != nil {
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
	transaction, err := s.beginStoreORMTransaction(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: begin read transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackErr := transaction.tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = joinErrors(returnErr,
				fmt.Errorf("load Observed firewall snapshot: rollback read transaction: %w", rollbackErr))
		}
	}()

	if err := requireObservedNodeIdentity(ctx, transaction.orm, nodeID); err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: %w", err)
	}
	snapshot.NodeID = nodeID
	if observed, found, err := loadInfrastructureObserved(ctx, transaction.orm); err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: infrastructure: %w", err)
	} else if found {
		snapshot.Infrastructure = &observed
	}
	if observed, found, err := loadPolicyObserved(ctx, transaction.orm); err != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: policy: %w", err)
	} else if found {
		snapshot.Policy = &observed
	}

	var rows []targetObservedRow
	result := transaction.orm.WithContext(ctx).
		Where(clause.Eq{Column: clause.Column{Name: EnforcementStateColumns.NodeID}, Value: string(nodeID)}).
		Where(clause.Neq{Column: clause.Column{Name: EnforcementStateColumns.ObservedAtUS}, Value: nil}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: EnforcementStateColumns.CanonicalTarget}}).
		Find(&rows)
	if result.Error != nil {
		return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: targets: %w", result.Error)
	}
	snapshot.Targets = make([]core.TargetObservedState, 0)
	for _, row := range rows {
		observed, decodeErr := decodeTargetObservedRow(row)
		if decodeErr != nil {
			return core.ObservedFirewallSnapshot{}, fmt.Errorf("load Observed firewall snapshot: decode target: %w", decodeErr)
		}
		snapshot.Targets = append(snapshot.Targets, observed)
	}
	if err := transaction.tx.Commit(); err != nil {
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

func writeObservedFirewallUpdate(ctx context.Context, orm *gorm.DB, update core.ObservedFirewallUpdate) error {
	if update.Infrastructure != nil {
		if err := upsertInfrastructureObserved(ctx, orm, update.NodeID, *update.Infrastructure); err != nil {
			return fmt.Errorf("write infrastructure: %w", err)
		}
	}
	if update.Policy != nil {
		if err := upsertPolicyObserved(ctx, orm, update.NodeID, *update.Policy); err != nil {
			return fmt.Errorf("write policy: %w", err)
		}
	}
	for _, target := range update.Targets {
		if err := updateTargetObserved(ctx, orm, update.NodeID, target); err != nil {
			return fmt.Errorf("write target %s: %w", target.CanonicalTarget, err)
		}
	}
	return nil
}

func upsertInfrastructureObserved(
	ctx context.Context,
	orm *gorm.DB,
	nodeID core.NodeID,
	observed core.InfrastructureObservedState,
) error {
	presence, err := encodeObservedPresence(observed.Presence)
	if err != nil {
		return err
	}
	row := infrastructureObservedRow{
		Singleton: 1, NodeID: string(nodeID), Presence: presence,
		ObservedAtUS: observed.ObservedAt.UTC().UnixMicro(), Backend: observed.Backend,
		OwnerVersion: observed.OwnerVersion, Digest: observed.Digest,
		LastErrorCode: observed.LastErrorCode,
	}
	if observed.ConfirmedRevision != 0 {
		row.ConfirmedRevision = sql.NullInt64{Int64: int64(observed.ConfirmedRevision), Valid: true}
	}
	sqlResult := gorm.WithResult()
	result := orm.WithContext(ctx).Clauses(sqlResult, clause.OnConflict{
		Columns: []clause.Column{{Name: InfrastructureObservedStateColumns.Singleton}},
		DoUpdates: clause.AssignmentColumns([]string{
			InfrastructureObservedStateColumns.NodeID, InfrastructureObservedStateColumns.Presence,
			InfrastructureObservedStateColumns.ObservedAtUS, InfrastructureObservedStateColumns.Backend,
			InfrastructureObservedStateColumns.OwnerVersion, InfrastructureObservedStateColumns.Digest,
			InfrastructureObservedStateColumns.ConfirmedInfrastructureRevision,
			InfrastructureObservedStateColumns.LastErrorCode,
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Gt{Column: clause.Column{Table: "excluded", Name: InfrastructureObservedStateColumns.ObservedAtUS}, Value: clause.Column{Table: "infrastructure_observed_state", Name: InfrastructureObservedStateColumns.ObservedAtUS}}}},
	}).Select(
		InfrastructureObservedStateColumns.Singleton, InfrastructureObservedStateColumns.NodeID,
		InfrastructureObservedStateColumns.Presence, InfrastructureObservedStateColumns.ObservedAtUS,
		InfrastructureObservedStateColumns.Backend, InfrastructureObservedStateColumns.OwnerVersion,
		InfrastructureObservedStateColumns.Digest, InfrastructureObservedStateColumns.ConfirmedInfrastructureRevision,
		InfrastructureObservedStateColumns.LastErrorCode,
	).Create(&row)
	if result.Error != nil {
		return result.Error
	}
	affected, err := sqlResult.Result.RowsAffected()
	if err != nil {
		return fmt.Errorf("affected rows: %w", err)
	}
	if affected == 1 {
		return nil
	}
	persisted, found, err := loadInfrastructureObserved(ctx, orm)
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
	orm *gorm.DB,
	nodeID core.NodeID,
	observed core.PolicyObservedState,
) error {
	presence, err := encodeObservedPresence(observed.Presence)
	if err != nil {
		return err
	}
	row := policyObservedRow{
		Singleton: 1, NodeID: string(nodeID), Presence: presence,
		ObservedAtUS: observed.ObservedAt.UTC().UnixMicro(), RelationDigest: observed.RelationDigest,
		LastErrorCode: observed.LastErrorCode,
	}
	if observed.ConfirmedRevision != 0 {
		row.ConfirmedRevision = sql.NullInt64{Int64: int64(observed.ConfirmedRevision), Valid: true}
	}
	sqlResult := gorm.WithResult()
	result := orm.WithContext(ctx).Clauses(sqlResult, clause.OnConflict{
		Columns: []clause.Column{{Name: PolicyObservedStateColumns.Singleton}},
		DoUpdates: clause.AssignmentColumns([]string{
			PolicyObservedStateColumns.NodeID, PolicyObservedStateColumns.Presence,
			PolicyObservedStateColumns.ObservedAtUS, PolicyObservedStateColumns.RelationDigest,
			PolicyObservedStateColumns.ConfirmedPolicyRevision, PolicyObservedStateColumns.LastErrorCode,
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Gt{Column: clause.Column{Table: "excluded", Name: PolicyObservedStateColumns.ObservedAtUS}, Value: clause.Column{Table: "policy_observed_state", Name: PolicyObservedStateColumns.ObservedAtUS}}}},
	}).Select(
		PolicyObservedStateColumns.Singleton, PolicyObservedStateColumns.NodeID,
		PolicyObservedStateColumns.Presence, PolicyObservedStateColumns.ObservedAtUS,
		PolicyObservedStateColumns.RelationDigest, PolicyObservedStateColumns.ConfirmedPolicyRevision,
		PolicyObservedStateColumns.LastErrorCode,
	).Create(&row)
	if result.Error != nil {
		return result.Error
	}
	affected, err := sqlResult.Result.RowsAffected()
	if err != nil {
		return fmt.Errorf("affected rows: %w", err)
	}
	if affected == 1 {
		return nil
	}
	persisted, found, err := loadPolicyObserved(ctx, orm)
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
	orm *gorm.DB,
	nodeID core.NodeID,
	observed core.TargetObservedState,
) error {
	desiredGeneration, persisted, found, err := loadTargetObservedForUpdate(
		ctx, orm, nodeID, observed.CanonicalTarget)
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
	nextObservedAt := observed.ObservedAt.UTC().UnixMicro()
	result := orm.WithContext(ctx).Model(&targetObservedRow{}).
		Where(clause.Eq{Column: clause.Column{Name: EnforcementStateColumns.NodeID}, Value: string(nodeID)}).
		Where(clause.Eq{Column: clause.Column{Name: EnforcementStateColumns.CanonicalTarget}, Value: observed.CanonicalTarget.String()}).
		Where(clause.Eq{Column: clause.Column{Name: EnforcementStateColumns.TargetEnforcementGeneration}, Value: int64(desiredGeneration)}).
		Where(clause.Or(
			clause.Eq{Column: clause.Column{Name: EnforcementStateColumns.ObservedAtUS}, Value: nil},
			clause.Lt{Column: clause.Column{Name: EnforcementStateColumns.ObservedAtUS}, Value: nextObservedAt},
		)).Updates(map[string]any{
		EnforcementStateColumns.ObservedMembership:                   membership,
		EnforcementStateColumns.ObservedAtUS:                         nextObservedAt,
		EnforcementStateColumns.ObservedEvidence:                     encodeTargetObservationEvidence(observed.Evidence),
		EnforcementStateColumns.ObservedBackend:                      observed.Backend,
		EnforcementStateColumns.ObservedPolicyCoverage:               policyCoverage,
		EnforcementStateColumns.ObservedPolicyRelationDigest:         observed.PolicyRelationDigest,
		EnforcementStateColumns.ObservedTimeoutMode:                  timeoutMode,
		EnforcementStateColumns.ObservedNativeExpiryUS:               encodeOptionalTime(observed.NativeExpiry),
		EnforcementStateColumns.ObservedScopes:                       int64(observed.Scopes),
		EnforcementStateColumns.ObservedAddressFamily:                encodeObservedAddressFamily(observed.AddressFamily),
		EnforcementStateColumns.ObservedOwnerVersion:                 observed.OwnerVersion,
		EnforcementStateColumns.ObservedLastErrorCode:                observed.LastErrorCode,
		EnforcementStateColumns.ConfirmedTargetEnforcementGeneration: nullableObservedUint64(uint64(observed.ConfirmedGeneration)),
		EnforcementStateColumns.ConfirmedSnapshotRevision:            nil,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("Desired generation or observation changed concurrently")
	}
	return nil
}

func requireObservedNodeIdentity(ctx context.Context, orm *gorm.DB, nodeID core.NodeID) error {
	var row observedNodeIdentityRow
	result := orm.WithContext(ctx).
		Where(clause.Eq{Column: clause.Column{Name: NodeIdentityColumns.Singleton}, Value: 1}).
		Select(NodeIdentityColumns.NodeID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("read node identity: %w", sql.ErrNoRows)
	}
	if result.Error != nil {
		return fmt.Errorf("read node identity: %w", result.Error)
	}
	if row.NodeID != string(nodeID) {
		return fmt.Errorf("persisted node %q differs from %q", row.NodeID, nodeID)
	}
	return nil
}

func loadInfrastructureObserved(ctx context.Context, orm *gorm.DB) (core.InfrastructureObservedState, bool, error) {
	var row infrastructureObservedRow
	result := orm.WithContext(ctx).
		Where(clause.Eq{Column: clause.Column{Name: InfrastructureObservedStateColumns.Singleton}, Value: 1}).
		Select(
			InfrastructureObservedStateColumns.Presence, InfrastructureObservedStateColumns.ObservedAtUS,
			InfrastructureObservedStateColumns.Backend, InfrastructureObservedStateColumns.OwnerVersion,
			InfrastructureObservedStateColumns.Digest, InfrastructureObservedStateColumns.ConfirmedInfrastructureRevision,
			InfrastructureObservedStateColumns.LastErrorCode,
		).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return core.InfrastructureObservedState{}, false, nil
	}
	if result.Error != nil {
		return core.InfrastructureObservedState{}, false, result.Error
	}
	decodedPresence, err := decodeObservedPresence(row.Presence)
	if err != nil {
		return core.InfrastructureObservedState{}, false, err
	}
	observed := core.InfrastructureObservedState{
		Presence: decodedPresence, ObservedAt: time.UnixMicro(row.ObservedAtUS).UTC(),
		Backend: row.Backend, OwnerVersion: row.OwnerVersion, Digest: row.Digest,
		ConfirmedRevision: core.InfrastructureRevision(nullInt64Value(row.ConfirmedRevision)),
		LastErrorCode:     row.LastErrorCode,
	}
	if err := observed.Validate(); err != nil {
		return core.InfrastructureObservedState{}, false,
			fmt.Errorf("validate persisted infrastructure observation: %w", err)
	}
	return observed, true, nil
}

func loadPolicyObserved(ctx context.Context, orm *gorm.DB) (core.PolicyObservedState, bool, error) {
	var row policyObservedRow
	result := orm.WithContext(ctx).
		Where(clause.Eq{Column: clause.Column{Name: PolicyObservedStateColumns.Singleton}, Value: 1}).
		Select(
			PolicyObservedStateColumns.Presence, PolicyObservedStateColumns.ObservedAtUS,
			PolicyObservedStateColumns.RelationDigest, PolicyObservedStateColumns.ConfirmedPolicyRevision,
			PolicyObservedStateColumns.LastErrorCode,
		).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return core.PolicyObservedState{}, false, nil
	}
	if result.Error != nil {
		return core.PolicyObservedState{}, false, result.Error
	}
	decodedPresence, err := decodeObservedPresence(row.Presence)
	if err != nil {
		return core.PolicyObservedState{}, false, err
	}
	observed := core.PolicyObservedState{
		Presence: decodedPresence, ObservedAt: time.UnixMicro(row.ObservedAtUS).UTC(),
		RelationDigest:    row.RelationDigest,
		ConfirmedRevision: core.PolicyRevision(nullInt64Value(row.ConfirmedRevision)),
		LastErrorCode:     row.LastErrorCode,
	}
	if err := observed.Validate(); err != nil {
		return core.PolicyObservedState{}, false,
			fmt.Errorf("validate persisted policy observation: %w", err)
	}
	return observed, true, nil
}

func loadTargetObservedForUpdate(
	ctx context.Context,
	orm *gorm.DB,
	nodeID core.NodeID,
	target netip.Prefix,
) (core.TargetEnforcementGeneration, core.TargetObservedState, bool, error) {
	var row targetObservedRow
	result := orm.WithContext(ctx).
		Where(clause.Eq{Column: clause.Column{Name: EnforcementStateColumns.NodeID}, Value: string(nodeID)}).
		Where(clause.Eq{Column: clause.Column{Name: EnforcementStateColumns.CanonicalTarget}, Value: target.String()}).
		Select(
			EnforcementStateColumns.CanonicalTarget, EnforcementStateColumns.TargetEnforcementGeneration,
			EnforcementStateColumns.ObservedMembership, EnforcementStateColumns.ObservedAtUS,
			EnforcementStateColumns.ObservedEvidence, EnforcementStateColumns.ObservedBackend,
			EnforcementStateColumns.ObservedPolicyCoverage, EnforcementStateColumns.ObservedPolicyRelationDigest,
			EnforcementStateColumns.ObservedTimeoutMode, EnforcementStateColumns.ObservedNativeExpiryUS,
			EnforcementStateColumns.ObservedScopes, EnforcementStateColumns.ObservedAddressFamily,
			EnforcementStateColumns.ObservedOwnerVersion, EnforcementStateColumns.ObservedLastErrorCode,
			EnforcementStateColumns.ConfirmedTargetEnforcementGeneration,
		).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return 0, core.TargetObservedState{}, false, nil
	}
	if result.Error != nil {
		return 0, core.TargetObservedState{}, false, result.Error
	}
	if !row.ObservedAtUS.Valid {
		return core.TargetEnforcementGeneration(row.TargetGeneration), core.TargetObservedState{}, false, nil
	}
	observed, err := decodeTargetObservedRow(row)
	if err != nil {
		return 0, core.TargetObservedState{}, false, err
	}
	return core.TargetEnforcementGeneration(row.TargetGeneration), observed, true, nil
}

func decodeTargetObservedRow(row targetObservedRow) (core.TargetObservedState, error) {
	return decodeTargetObservedState(
		row.CanonicalTarget, row.ObservedMembership, row.ObservedAtUS.Int64, row.ObservedEvidence,
		row.ObservedBackend, row.PolicyCoverage, row.PolicyDigest, row.TimeoutMode,
		row.NativeExpiryUS, row.Scopes, row.AddressFamily, row.OwnerVersion,
		row.LastErrorCode, row.ConfirmedGeneration)
}

func decodeTargetObservedState(
	target, membership string,
	observedAt int64,
	evidence, backend, policyCoverage, policyDigest, timeoutMode string,
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
	decodedEvidence, err := decodeTargetObservationEvidence(evidence)
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
			Evidence: decodedEvidence, Backend: backend, BanMembership: decodedMembership,
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
		left.Evidence == right.Evidence &&
		left.PolicyRelationDigest == right.PolicyRelationDigest && left.TimeoutMode == right.TimeoutMode &&
		equalOptionalPersistedTime(left.NativeExpiry, right.NativeExpiry) &&
		left.Scopes == right.Scopes && left.AddressFamily == right.AddressFamily &&
		left.OwnerVersion == right.OwnerVersion && left.LastErrorCode == right.LastErrorCode &&
		left.ConfirmedGeneration == right.ConfirmedGeneration
}

func encodeTargetObservationEvidence(value core.TargetObservationEvidence) string {
	if value == core.TargetObservationEvidenceManagedSnapshot {
		return "managed_snapshot"
	}
	return "complete"
}

func decodeTargetObservationEvidence(value string) (core.TargetObservationEvidence, error) {
	switch value {
	case "complete":
		return core.TargetObservationEvidenceComplete, nil
	case "managed_snapshot":
		return core.TargetObservationEvidenceManagedSnapshot, nil
	default:
		return 0, fmt.Errorf("unsupported persisted target observation evidence %q", value)
	}
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
