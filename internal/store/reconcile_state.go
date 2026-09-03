package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/guard-wall/internal/core"
)

// ErrReconcileStateRegression is retained as the Store-facing classification.
var ErrReconcileStateRegression = core.ErrReconcileStateRegression

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
	transaction, err := s.beginStoreORMTransaction(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: begin read transaction: %w", err)
	}
	tx, transactionORM := transaction.tx, transaction.orm
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

	if err := s.requireNodeIdentity(ctx, transactionORM, nodeID); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: %w", err)
	}

	if state, ok, err := loadSingletonReconcileState(ctx, transactionORM, nodeID, core.ReconcileDomainInfrastructure); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: infrastructure state: %w", err)
	} else if ok {
		snapshot.States = append(snapshot.States, state)
	}
	if state, ok, err := loadSingletonReconcileState(ctx, transactionORM, nodeID, core.ReconcileDomainPolicy); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: policy state: %w", err)
	} else if ok {
		snapshot.States = append(snapshot.States, state)
	}

	var rows []targetReconcileStateRow
	result := transactionORM.WithContext(ctx).
		Where(&targetReconcileStateRow{NodeID: string(nodeID)}).
		Clauses(orderByColumns(TargetReconcileStateColumns.CanonicalTarget)).
		Find(&rows)
	if result.Error != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: target states: %w", result.Error)
	}
	for _, row := range rows {
		state, scanErr := decodeTargetReconcileState(nodeID, row)
		if scanErr != nil {
			return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: target state: %w", scanErr)
		}
		snapshot.States = append(snapshot.States, state)
	}

	var probeRows []reconcileProbeRequirementRow
	result = transactionORM.WithContext(ctx).
		Where(&reconcileProbeRequirementRow{NodeID: string(nodeID)}).
		Clauses(orderByColumns(
			ReconcileProbeRequirementColumns.Domain, ReconcileProbeRequirementColumns.CanonicalTarget,
			ReconcileProbeRequirementColumns.InfrastructureRevision, ReconcileProbeRequirementColumns.PolicyRevision,
			ReconcileProbeRequirementColumns.TargetEnforcementGeneration, ReconcileProbeRequirementColumns.SnapshotRevision,
			ReconcileProbeRequirementColumns.FenceSnapshotRevision, ReconcileProbeRequirementColumns.RetryEpoch,
			ReconcileProbeRequirementColumns.AttemptCount,
		)).
		Find(&probeRows)
	if result.Error != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: probe requirements: %w", result.Error)
	}
	for _, row := range probeRows {
		probe, scanErr := decodeProbeRequirement(nodeID, row)
		if scanErr != nil {
			return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: probe requirement: %w", scanErr)
		}
		snapshot.ProbeRequirements = append(snapshot.ProbeRequirements, probe)
	}
	if err := tx.Commit(); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: commit read transaction: %w", err)
	}
	committed = true
	return snapshot, nil
}

// loadReconcileRecoveryInTx reads the full retry recovery snapshot through an
// already-open read transaction so callers can bind it to related evidence.
func loadReconcileRecoveryInTx(ctx context.Context, orm *gorm.DB, nodeID core.NodeID) (snapshot core.ReconcileRecoverySnapshot, returnErr error) {
	if err := requireNodeIdentity(ctx, orm, nodeID); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: %w", err)
	}
	if state, ok, err := loadSingletonReconcileState(ctx, orm, nodeID, core.ReconcileDomainInfrastructure); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: infrastructure state: %w", err)
	} else if ok {
		snapshot.States = append(snapshot.States, state)
	}
	if state, ok, err := loadSingletonReconcileState(ctx, orm, nodeID, core.ReconcileDomainPolicy); err != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: policy state: %w", err)
	} else if ok {
		snapshot.States = append(snapshot.States, state)
	}
	var rows []targetReconcileStateRow
	result := orm.WithContext(ctx).Where(&targetReconcileStateRow{NodeID: string(nodeID)}).
		Clauses(orderByColumns(TargetReconcileStateColumns.CanonicalTarget)).Find(&rows)
	if result.Error != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: target states: %w", result.Error)
	}
	for _, row := range rows {
		state, scanErr := decodeTargetReconcileState(nodeID, row)
		if scanErr != nil {
			return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: target state: %w", scanErr)
		}
		snapshot.States = append(snapshot.States, state)
	}
	var probeRows []reconcileProbeRequirementRow
	result = orm.WithContext(ctx).Where(&reconcileProbeRequirementRow{NodeID: string(nodeID)}).
		Clauses(orderByColumns(
			ReconcileProbeRequirementColumns.Domain, ReconcileProbeRequirementColumns.CanonicalTarget,
			ReconcileProbeRequirementColumns.InfrastructureRevision, ReconcileProbeRequirementColumns.PolicyRevision,
			ReconcileProbeRequirementColumns.TargetEnforcementGeneration, ReconcileProbeRequirementColumns.SnapshotRevision,
			ReconcileProbeRequirementColumns.FenceSnapshotRevision, ReconcileProbeRequirementColumns.RetryEpoch,
			ReconcileProbeRequirementColumns.AttemptCount,
		)).
		Find(&probeRows)
	if result.Error != nil {
		return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: probe requirements: %w", result.Error)
	}
	for _, row := range probeRows {
		probe, scanErr := decodeProbeRequirement(nodeID, row)
		if scanErr != nil {
			return core.ReconcileRecoverySnapshot{}, fmt.Errorf("load reconcile recovery: probe requirement: %w", scanErr)
		}
		snapshot.ProbeRequirements = append(snapshot.ProbeRequirements, probe)
	}
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

	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply reconcile transition: begin: %w", err)
	}
	tx, transactionORM := transaction.tx, transaction.orm
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
	if err := s.requireNodeIdentity(ctx, transactionORM, nodeID); err != nil {
		return fmt.Errorf("apply reconcile transition: %w", err)
	}
	if !transition.DeleteOnly {
		if err := rejectReconcileRegression(ctx, transactionORM, transition.State); err != nil {
			return fmt.Errorf("apply reconcile transition: %w", err)
		}
	}
	if transition.DeleteProbe != nil {
		if err := deleteProbeRequirement(ctx, transactionORM, *transition.DeleteProbe); err != nil {
			return fmt.Errorf("apply reconcile transition: delete probe requirement: %w", err)
		}
	}
	if !transition.DeleteOnly {
		if err := upsertReconcileState(ctx, transactionORM, transition.State); err != nil {
			return fmt.Errorf("apply reconcile transition: write state: %w", err)
		}
	}
	if transition.UpsertProbe != nil {
		if err := upsertProbeRequirement(ctx, transactionORM, *transition.UpsertProbe); err != nil {
			return fmt.Errorf("apply reconcile transition: upsert probe requirement: %w", err)
		}
	}
	if transition.Observed != nil {
		if err := writeObservedFirewallUpdate(ctx, transactionORM, *transition.Observed); err != nil {
			return fmt.Errorf("apply reconcile transition: write Observed state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return core.NewReconcileCommitUnknownError(fmt.Errorf("apply reconcile transition: commit: %w", err))
	}
	committed = true
	return nil
}

// ApplyReconcileRetryTransition atomically persists an administrator-created
// Pending retry epoch and its mandatory critical audit record.
func (s *Store) ApplyReconcileRetryTransition(ctx context.Context, transition core.ReconcileRetryTransition) (returnErr error) {
	if err := s.ready(ctx); err != nil {
		return fmt.Errorf("apply reconcile retry transition: %w", err)
	}
	if err := validateReconcileRetryTransition(transition); err != nil {
		return fmt.Errorf("apply reconcile retry transition: %w", err)
	}
	transaction, err := s.beginStoreORMTransaction(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply reconcile retry transition: begin: %w", err)
	}
	tx, transactionORM := transaction.tx, transaction.orm
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
	if err := s.requireNodeIdentity(ctx, transactionORM, transition.State.NodeID); err != nil {
		return fmt.Errorf("apply reconcile retry transition: %w", err)
	}
	if err := rejectReconcileRegression(ctx, transactionORM, transition.State); err != nil {
		return fmt.Errorf("apply reconcile retry transition: %w", err)
	}
	previous, err := currentRetryEpoch(ctx, transactionORM, transition.State)
	if err != nil {
		return fmt.Errorf("apply reconcile retry transition: %w", err)
	}
	if transition.Audit.PreviousEpoch != previous {
		return fmt.Errorf("apply reconcile retry transition: audit previous epoch %d does not match durable epoch %d", transition.Audit.PreviousEpoch, previous)
	}
	if err := upsertReconcileState(ctx, transactionORM, transition.State); err != nil {
		return fmt.Errorf("apply reconcile retry transition: write state: %w", err)
	}
	if err := writeReconcileRetryAudit(ctx, transactionORM, transition); err != nil {
		return fmt.Errorf("apply reconcile retry transition: write audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.NewReconcileCommitUnknownError(fmt.Errorf("apply reconcile retry transition: commit: %w", err))
	}
	committed = true
	return nil
}

// ReadReconcileRetryTransition proves an indeterminate retry commit only when
// its exact retry ledger state and mandatory audit record are both present.
func (s *Store) ReadReconcileRetryTransition(ctx context.Context, transition core.ReconcileRetryTransition) (core.ReconcileRetryReadback, error) {
	if err := s.ready(ctx); err != nil {
		return core.ReconcileRetryReadback{}, fmt.Errorf("read reconcile retry transition: %w", err)
	}
	transaction, err := s.beginStoreORMTransaction(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return core.ReconcileRetryReadback{}, fmt.Errorf("read reconcile retry transition: begin: %w", err)
	}
	tx, transactionORM := transaction.tx, transaction.orm
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	recovery, err := loadReconcileRecoveryInTx(ctx, transactionORM, transition.State.NodeID)
	if err != nil {
		return core.ReconcileRetryReadback{}, err
	}
	row, err := readReconcileRetryAudit(ctx, transactionORM, transition.Audit.ID)
	if err != nil {
		return core.ReconcileRetryReadback{}, err
	}
	stateApplied := recoveryContainsState(recovery, transition.State)
	auditApplied := row == reconcileRetryAuditDetails(transition)
	if stateApplied != auditApplied || (row.ID != "" && !auditApplied) {
		return core.ReconcileRetryReadback{}, fmt.Errorf("read reconcile retry transition: ledger and audit do not match atomically")
	}
	if err := tx.Commit(); err != nil {
		return core.ReconcileRetryReadback{}, fmt.Errorf("read reconcile retry transition: commit: %w", err)
	}
	committed = true
	return core.ReconcileRetryReadback{
		Recovery: recovery,
		Applied:  stateApplied && auditApplied,
	}, nil
}

type reconcileRetryAuditRecord struct {
	ID, IdempotencyKey, NodeID, Category, Action, Result, Severity, ActorType, Details string
	Critical                                                                           int64
	DeliveryID, AlertID, DecisionID, ErrorCode                                         sql.NullString
	CreatedAtUS                                                                        int64
}

func reconcileRetryAuditDetails(transition core.ReconcileRetryTransition) reconcileRetryAuditRecord {
	state := transition.State
	details := struct {
		Domain                 string `json:"domain"`
		InfrastructureRevision uint64 `json:"infrastructure_revision,omitempty"`
		PolicyRevision         uint64 `json:"policy_revision,omitempty"`
		CanonicalTarget        string `json:"canonical_target,omitempty"`
		TargetGeneration       uint64 `json:"target_enforcement_generation,omitempty"`
		PreviousRetryEpoch     uint64 `json:"previous_retry_epoch"`
		NewRetryEpoch          uint64 `json:"new_retry_epoch"`
	}{
		PreviousRetryEpoch: uint64(transition.Audit.PreviousEpoch), NewRetryEpoch: uint64(state.RetryEpoch),
	}
	switch state.Domain {
	case core.ReconcileDomainInfrastructure:
		details.Domain, details.InfrastructureRevision = "infrastructure", uint64(state.InfrastructureRevision)
	case core.ReconcileDomainPolicy:
		details.Domain, details.PolicyRevision = "policy", uint64(state.PolicyRevision)
	case core.ReconcileDomainTarget:
		details.Domain, details.CanonicalTarget, details.TargetGeneration = "target", state.Target.String(), uint64(state.TargetGeneration)
	}
	raw, _ := json.Marshal(details)
	return reconcileRetryAuditRecord{
		ID: transition.Audit.ID, IdempotencyKey: transition.Audit.IdempotencyKey, NodeID: string(transition.Audit.NodeID),
		Category: "reconcile", Action: "reconcile_retry", Result: "success", Severity: "info",
		ActorType: transition.Audit.ActorType, Details: string(raw), Critical: 1,
		CreatedAtUS: transition.Audit.OccurredAt.UTC().UnixMicro(),
	}
}

func writeReconcileRetryAudit(ctx context.Context, orm *gorm.DB, transition core.ReconcileRetryTransition) error {
	record := reconcileRetryAuditDetails(transition)
	result := orm.WithContext(ctx).
		Select(
			CriticalAuditColumns.AuditID, CriticalAuditColumns.IdempotencyKey, CriticalAuditColumns.NodeID,
			CriticalAuditColumns.Category, CriticalAuditColumns.Action, CriticalAuditColumns.Result,
			CriticalAuditColumns.Severity, CriticalAuditColumns.Critical, CriticalAuditColumns.ActorType,
			CriticalAuditColumns.DeliveryID, CriticalAuditColumns.AlertID, CriticalAuditColumns.DecisionID,
			CriticalAuditColumns.ErrorCode, CriticalAuditColumns.DetailsJSON, CriticalAuditColumns.CreatedAtUS,
		).
		Create(&criticalAuditRow{
			AuditID: record.ID, IdempotencyKey: record.IdempotencyKey, NodeID: record.NodeID,
			Category: record.Category, Action: record.Action, Result: record.Result, Severity: record.Severity,
			Critical: record.Critical, ActorType: record.ActorType, DetailsJSON: record.Details,
			CreatedAtUS: record.CreatedAtUS,
		})
	return result.Error
}

func readReconcileRetryAudit(ctx context.Context, orm *gorm.DB, auditID string) (reconcileRetryAuditRecord, error) {
	var row criticalAuditRow
	result := orm.WithContext(ctx).Where(&criticalAuditRow{AuditID: auditID}).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return reconcileRetryAuditRecord{}, nil
	}
	if result.Error != nil {
		return reconcileRetryAuditRecord{}, fmt.Errorf("read retry audit: %w", result.Error)
	}
	return reconcileRetryAuditRecord{
		ID: row.AuditID, IdempotencyKey: row.IdempotencyKey, NodeID: row.NodeID,
		Category: row.Category, Action: row.Action, Result: row.Result, Severity: row.Severity,
		Critical: row.Critical, ActorType: row.ActorType,
		DeliveryID: sqlNullString(row.DeliveryID), AlertID: sqlNullString(row.AlertID),
		DecisionID: sqlNullString(row.DecisionID), ErrorCode: sqlNullString(row.ErrorCode),
		Details: row.DetailsJSON, CreatedAtUS: row.CreatedAtUS,
	}, nil
}

func recoveryContainsState(recovery core.ReconcileRecoverySnapshot, expected core.PersistedReconcileState) bool {
	for _, state := range recovery.States {
		if state.NodeID == expected.NodeID && state.Domain == expected.Domain &&
			state.InfrastructureRevision == expected.InfrastructureRevision && state.PolicyRevision == expected.PolicyRevision &&
			state.Target == expected.Target && state.TargetGeneration == expected.TargetGeneration &&
			state.RetryEpoch == expected.RetryEpoch && state.RetryState.Status == expected.RetryState.Status &&
			state.RetryState.AttemptCount == expected.RetryState.AttemptCount && state.UpdatedAt.UTC().UnixMicro() == expected.UpdatedAt.UTC().UnixMicro() {
			return true
		}
	}
	return false
}

func validateReconcileRetryTransition(transition core.ReconcileRetryTransition) error {
	if err := validateReconcileTransition(core.ReconcileStateTransition{State: transition.State}); err != nil {
		return err
	}
	state := transition.State
	audit := transition.Audit
	if state.RetryState.Status != core.ReconcilePending || state.RetryState.AttemptCount != 0 ||
		state.RetryState.LastAttemptAt != nil || state.RetryState.NextAttemptAt != nil || state.RetryState.LastErrorCode != "" {
		return fmt.Errorf("administrator retry must create a clean Pending state")
	}
	if audit.ID == "" || len(audit.ID) > 160 || audit.IdempotencyKey == "" || len(audit.IdempotencyKey) > 256 {
		return fmt.Errorf("retry audit identity is invalid")
	}
	if audit.NodeID == "" || audit.NodeID != state.NodeID || audit.ActorType != "administrator" {
		return fmt.Errorf("retry audit identity does not match administrator node")
	}
	if audit.OccurredAt.IsZero() || audit.OccurredAt.UTC().UnixMicro() <= 0 ||
		audit.OccurredAt.UTC().UnixMicro() != state.UpdatedAt.UTC().UnixMicro() {
		return fmt.Errorf("retry audit time does not match transition")
	}
	if state.RetryEpoch == 0 || state.RetryEpoch != audit.PreviousEpoch+1 {
		return fmt.Errorf("retry audit epoch does not advance exactly once")
	}
	return nil
}

func (s *Store) requireNodeIdentity(ctx context.Context, orm *gorm.DB, nodeID core.NodeID) error {
	return requireNodeIdentity(ctx, orm, nodeID)
}

func requireNodeIdentity(ctx context.Context, orm *gorm.DB, nodeID core.NodeID) error {
	var row nodeIdentityRow
	result := orm.WithContext(ctx).Where(&nodeIdentityRow{Singleton: 1}).Take(&row)
	if result.Error != nil {
		return fmt.Errorf("read node identity: %w", result.Error)
	}
	if row.NodeID != string(nodeID) {
		return fmt.Errorf("persisted node %q differs from %q", row.NodeID, nodeID)
	}
	return nil
}

func loadSingletonReconcileState(ctx context.Context, orm *gorm.DB, nodeID core.NodeID, domain core.ReconcileDomain) (core.PersistedReconcileState, bool, error) {
	switch domain {
	case core.ReconcileDomainInfrastructure:
		var row infrastructureReconcileStateRow
		result := orm.WithContext(ctx).Where(&infrastructureReconcileStateRow{Singleton: 1}).Take(&row)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return core.PersistedReconcileState{}, false, nil
		}
		if result.Error != nil {
			return core.PersistedReconcileState{}, false, result.Error
		}
		state, err := decodeReconcileState(nodeID, domain, row.InfrastructureRevision, "", 0, row.RetryEpoch,
			row.Status, row.AttemptCount, row.LastAttemptAtUS, row.NextAttemptAtUS, row.LastErrorCode, row.UpdatedAtUS)
		return state, err == nil, err
	case core.ReconcileDomainPolicy:
		var row policyReconcileStateRow
		result := orm.WithContext(ctx).Where(&policyReconcileStateRow{Singleton: 1}).Take(&row)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return core.PersistedReconcileState{}, false, nil
		}
		if result.Error != nil {
			return core.PersistedReconcileState{}, false, result.Error
		}
		state, err := decodeReconcileState(nodeID, domain, row.PolicyRevision, "", 0, row.RetryEpoch,
			row.Status, row.AttemptCount, row.LastAttemptAtUS, row.NextAttemptAtUS, row.LastErrorCode, row.UpdatedAtUS)
		return state, err == nil, err
	default:
		return core.PersistedReconcileState{}, false, fmt.Errorf("unsupported domain %d", domain)
	}
}

func decodeTargetReconcileState(nodeID core.NodeID, row targetReconcileStateRow) (core.PersistedReconcileState, error) {
	return decodeReconcileState(nodeID, core.ReconcileDomainTarget, 0, row.CanonicalTarget,
		row.TargetEnforcementGeneration, row.RetryEpoch, row.Status, row.AttemptCount,
		row.LastAttemptAtUS, row.NextAttemptAtUS, row.LastErrorCode, row.UpdatedAtUS)
}

func decodeReconcileState(nodeID core.NodeID, domain core.ReconcileDomain, key int64, targetText string, generation, epoch int64, statusText string, attempt int64, lastAttempt, nextAttempt *int64, errorCode *string, updated int64) (core.PersistedReconcileState, error) {
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
			LastAttemptAt: decodeOptionalTimePointer(lastAttempt),
			NextAttemptAt: decodeOptionalTimePointer(nextAttempt),
			LastErrorCode: valueOrEmpty(errorCode),
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

func decodeProbeRequirement(nodeID core.NodeID, row reconcileProbeRequirementRow) (core.PersistedProbeRequirement, error) {
	domainText, targetText := row.Domain, row.CanonicalTarget
	infrastructure, policy, generation := row.InfrastructureRevision, row.PolicyRevision, row.TargetEnforcementGeneration
	snapshot, fence, epoch, attempt, recorded := row.SnapshotRevision, row.FenceSnapshotRevision, row.RetryEpoch, row.AttemptCount, row.RecordedAtUS
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

func rejectReconcileRegression(ctx context.Context, orm *gorm.DB, state core.PersistedReconcileState) error {
	var current core.PersistedReconcileState
	var ok bool
	var err error
	if state.Domain == core.ReconcileDomainTarget {
		var row targetReconcileStateRow
		result := orm.WithContext(ctx).Where(&targetReconcileStateRow{
			NodeID: string(state.NodeID), CanonicalTarget: state.Target.String(),
		}).Take(&row)
		err = result.Error
		if err == nil {
			if row.TargetEnforcementGeneration < 0 || row.RetryEpoch < 0 || row.AttemptCount < 0 || row.AttemptCount > math.MaxUint32 {
				return fmt.Errorf("persisted target version is out of range")
			}
			ok = true
			current.Domain = core.ReconcileDomainTarget
			current.Target = state.Target
			current.TargetGeneration = core.TargetEnforcementGeneration(row.TargetEnforcementGeneration)
			current.RetryEpoch = core.RetryEpoch(row.RetryEpoch)
			current.RetryState.AttemptCount = uint32(row.AttemptCount)
		}
	} else {
		current, ok, err = loadSingletonReconcileState(ctx, orm, state.NodeID, state.Domain)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || !ok {
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

func currentRetryEpoch(ctx context.Context, orm *gorm.DB, state core.PersistedReconcileState) (core.RetryEpoch, error) {
	if state.Domain == core.ReconcileDomainTarget {
		var row targetReconcileStateRow
		result := orm.WithContext(ctx).Select(TargetReconcileStateColumns.RetryEpoch).Where(&targetReconcileStateRow{
			NodeID: string(state.NodeID), CanonicalTarget: state.Target.String(),
		}).Take(&row)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		if result.Error != nil {
			return 0, fmt.Errorf("read current target retry epoch: %w", result.Error)
		}
		if row.RetryEpoch < 0 {
			return 0, fmt.Errorf("persisted target retry epoch is out of range")
		}
		return core.RetryEpoch(row.RetryEpoch), nil
	}
	current, ok, err := loadSingletonReconcileState(ctx, orm, state.NodeID, state.Domain)
	if err != nil {
		return 0, fmt.Errorf("read current retry epoch: %w", err)
	}
	if !ok {
		return 0, nil
	}
	return current.RetryEpoch, nil
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

func upsertReconcileState(ctx context.Context, orm *gorm.DB, state core.PersistedReconcileState) error {
	status := encodeReconcileStatus(state.RetryState.Status)
	lastAttempt := optionalTimePointer(state.RetryState.LastAttemptAt)
	nextAttempt := optionalTimePointer(state.RetryState.NextAttemptAt)
	errorCode := optionalTextPointer(state.RetryState.LastErrorCode)
	if state.Domain == core.ReconcileDomainInfrastructure {
		return orm.WithContext(ctx).Clauses(reconcileUpsertConflict(
			[]string{InfrastructureReconcileStateColumns.Singleton},
			reconcileStateUpdateColumns(
				InfrastructureReconcileStateColumns.InfrastructureRevision,
				InfrastructureReconcileStateColumns.RetryEpoch, InfrastructureReconcileStateColumns.Status,
				InfrastructureReconcileStateColumns.AttemptCount, InfrastructureReconcileStateColumns.LastAttemptAtUS,
				InfrastructureReconcileStateColumns.NextAttemptAtUS, InfrastructureReconcileStateColumns.LastErrorCode,
				InfrastructureReconcileStateColumns.UpdatedAtUS,
			),
		)).
			Create(&infrastructureReconcileStateRow{Singleton: 1, InfrastructureRevision: int64(state.InfrastructureRevision),
				RetryEpoch: int64(state.RetryEpoch), Status: status, AttemptCount: int64(state.RetryState.AttemptCount),
				LastAttemptAtUS: lastAttempt, NextAttemptAtUS: nextAttempt, LastErrorCode: errorCode,
				UpdatedAtUS: state.UpdatedAt.UTC().UnixMicro()}).Error
	}
	if state.Domain == core.ReconcileDomainPolicy {
		return orm.WithContext(ctx).Clauses(reconcileUpsertConflict(
			[]string{PolicyReconcileStateColumns.Singleton},
			reconcileStateUpdateColumns(
				PolicyReconcileStateColumns.PolicyRevision, PolicyReconcileStateColumns.RetryEpoch,
				PolicyReconcileStateColumns.Status, PolicyReconcileStateColumns.AttemptCount,
				PolicyReconcileStateColumns.LastAttemptAtUS, PolicyReconcileStateColumns.NextAttemptAtUS,
				PolicyReconcileStateColumns.LastErrorCode, PolicyReconcileStateColumns.UpdatedAtUS,
			),
		)).
			Create(&policyReconcileStateRow{Singleton: 1, PolicyRevision: int64(state.PolicyRevision),
				RetryEpoch: int64(state.RetryEpoch), Status: status, AttemptCount: int64(state.RetryState.AttemptCount),
				LastAttemptAtUS: lastAttempt, NextAttemptAtUS: nextAttempt, LastErrorCode: errorCode,
				UpdatedAtUS: state.UpdatedAt.UTC().UnixMicro()}).Error
	}
	return orm.WithContext(ctx).Clauses(reconcileUpsertConflict(
		[]string{TargetReconcileStateColumns.NodeID, TargetReconcileStateColumns.CanonicalTarget},
		reconcileStateUpdateColumns(
			TargetReconcileStateColumns.TargetEnforcementGeneration, TargetReconcileStateColumns.RetryEpoch,
			TargetReconcileStateColumns.Status, TargetReconcileStateColumns.AttemptCount,
			TargetReconcileStateColumns.LastAttemptAtUS, TargetReconcileStateColumns.NextAttemptAtUS,
			TargetReconcileStateColumns.LastErrorCode, TargetReconcileStateColumns.UpdatedAtUS,
		),
	)).
		Create(&targetReconcileStateRow{NodeID: string(state.NodeID), CanonicalTarget: state.Target.String(),
			TargetEnforcementGeneration: int64(state.TargetGeneration), RetryEpoch: int64(state.RetryEpoch),
			Status: status, AttemptCount: int64(state.RetryState.AttemptCount), LastAttemptAtUS: lastAttempt,
			NextAttemptAtUS: nextAttempt, LastErrorCode: errorCode, UpdatedAtUS: state.UpdatedAt.UTC().UnixMicro()}).Error
}

func upsertProbeRequirement(ctx context.Context, orm *gorm.DB, probe core.PersistedProbeRequirement) error {
	row := reconcileProbeRequirementFromCore(probe)
	return orm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: ReconcileProbeRequirementColumns.NodeID}, {Name: ReconcileProbeRequirementColumns.Domain},
			{Name: ReconcileProbeRequirementColumns.CanonicalTarget},
			{Name: ReconcileProbeRequirementColumns.InfrastructureRevision}, {Name: ReconcileProbeRequirementColumns.PolicyRevision},
			{Name: ReconcileProbeRequirementColumns.TargetEnforcementGeneration}, {Name: ReconcileProbeRequirementColumns.SnapshotRevision},
			{Name: ReconcileProbeRequirementColumns.FenceSnapshotRevision}, {Name: ReconcileProbeRequirementColumns.RetryEpoch},
			{Name: ReconcileProbeRequirementColumns.AttemptCount},
		},
		DoUpdates: clause.AssignmentColumns([]string{ReconcileProbeRequirementColumns.RecordedAtUS}),
	}).Create(&row).Error
}

func deleteProbeRequirement(ctx context.Context, orm *gorm.DB, probe core.PersistedProbeRequirement) error {
	row := reconcileProbeRequirementFromCore(probe)
	// map 条件保留零值字段，确保完整 Probe 物理键参与精确删除。
	result := orm.WithContext(ctx).Where(map[string]any{
		ReconcileProbeRequirementColumns.NodeID:                      row.NodeID,
		ReconcileProbeRequirementColumns.Domain:                      row.Domain,
		ReconcileProbeRequirementColumns.CanonicalTarget:             row.CanonicalTarget,
		ReconcileProbeRequirementColumns.InfrastructureRevision:      row.InfrastructureRevision,
		ReconcileProbeRequirementColumns.PolicyRevision:              row.PolicyRevision,
		ReconcileProbeRequirementColumns.TargetEnforcementGeneration: row.TargetEnforcementGeneration,
		ReconcileProbeRequirementColumns.SnapshotRevision:            row.SnapshotRevision,
		ReconcileProbeRequirementColumns.FenceSnapshotRevision:       row.FenceSnapshotRevision,
		ReconcileProbeRequirementColumns.RetryEpoch:                  row.RetryEpoch,
		ReconcileProbeRequirementColumns.AttemptCount:                row.AttemptCount,
	}).Delete(&reconcileProbeRequirementRow{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("exact probe requirement does not exist")
	}
	return nil
}

func reconcileProbeRequirementFromCore(probe core.PersistedProbeRequirement) reconcileProbeRequirementRow {
	target := ""
	if probe.Target.IsValid() {
		target = probe.Target.String()
	}
	return reconcileProbeRequirementRow{
		NodeID: string(probe.NodeID), Domain: encodeReconcileDomain(probe.Domain), CanonicalTarget: target,
		InfrastructureRevision: int64(probe.InfrastructureRevision), PolicyRevision: int64(probe.PolicyRevision),
		TargetEnforcementGeneration: int64(probe.TargetGeneration), SnapshotRevision: int64(probe.SnapshotRevision),
		FenceSnapshotRevision: boolToInt(probe.FenceSnapshotRevision), RetryEpoch: int64(probe.RetryEpoch),
		AttemptCount: int64(probe.AttemptCount), RecordedAtUS: probe.RecordedAt.UTC().UnixMicro(),
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
		if transition.Observed != nil {
			return fmt.Errorf("delete-only transition cannot contain Observed state")
		}
		return nil
	}
	if err := validatePersistedReconcileState(transition.State); err != nil {
		return err
	}
	if transition.Observed != nil {
		if err := validateObservedFirewallUpdate(*transition.Observed); err != nil {
			return fmt.Errorf("Observed state: %w", err)
		}
		if transition.Observed.NodeID != transition.State.NodeID {
			return fmt.Errorf("Observed state node does not match reconcile state")
		}
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

func decodeOptionalTimePointer(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	decoded := time.UnixMicro(*value).UTC()
	return &decoded
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

func orderByColumns(names ...string) clause.OrderBy {
	columns := make([]clause.OrderByColumn, 0, len(names))
	for _, name := range names {
		columns = append(columns, clause.OrderByColumn{Column: clause.Column{Name: name}})
	}
	return clause.OrderBy{Columns: columns}
}

func reconcileStateUpdateColumns(key, retryEpoch, status, attemptCount, lastAttemptAtUS, nextAttemptAtUS, lastErrorCode, updatedAtUS string) []string {
	return []string{key, retryEpoch, status, attemptCount, lastAttemptAtUS, nextAttemptAtUS, lastErrorCode, updatedAtUS}
}

func reconcileUpsertConflict(conflictColumns []string, updateColumns []string) clause.OnConflict {
	columns := make([]clause.Column, 0, len(conflictColumns))
	for _, name := range conflictColumns {
		columns = append(columns, clause.Column{Name: name})
	}
	return clause.OnConflict{
		Columns:   columns,
		DoUpdates: clause.AssignmentColumns(updateColumns),
	}
}

func optionalTimePointer(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	encoded := value.UTC().UnixMicro()
	return &encoded
}

func optionalTextPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sqlNullString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}
