package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

var errUnitOfWorkClosed = errors.New("unit of work is closed")

// CriticalAudit is an append-only audit record that must commit with the
// business state it explains.
type CriticalAudit struct {
	ID             string
	IdempotencyKey string
	NodeID         core.NodeID
	Category       string
	Action         string
	Result         string
	Severity       string
	ActorType      string
	DeliveryID     *core.DeliveryID
	AlertID        *core.AlertID
	DecisionID     *core.DecisionID
	ErrorCode      string
	DetailsJSON    []byte
	CreatedAt      time.Time
}

// UnitOfWork is the only transaction handle accepted by processing domain
// writers. It is not safe for concurrent use.
type UnitOfWork struct {
	tx     *sql.Tx
	failed error
	done   bool
}

// BeginProcessing starts one processing transaction. Only the Processing
// Coordinator may commit or roll back the returned UnitOfWork.
func (s *Store) BeginProcessing(ctx context.Context) (*UnitOfWork, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("begin processing: store is closed")
	}
	if ctx == nil {
		return nil, fmt.Errorf("begin processing: context is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin processing transaction: %w", err)
	}
	return &UnitOfWork{tx: tx}, nil
}

// PutParserOutcome inserts one parser version's terminal result.
func (u *UnitOfWork) PutParserOutcome(ctx context.Context, outcome core.ParserTerminalOutcome) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := outcome.Validate(); err != nil {
		return u.fail(fmt.Errorf("put parser outcome: validate: %w", err))
	}
	kind, err := parserOutcomeKindValue(outcome.Kind)
	if err != nil {
		return u.fail(err)
	}
	_, err = u.tx.ExecContext(ctx, `
		INSERT INTO parser_terminal_outcomes(
			delivery_id, parser_id, parser_version, kind, emitted_count,
			failure_code, completed_at_us
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(outcome.DeliveryID), string(outcome.ParserID), string(outcome.ParserVersion),
		kind, int64(outcome.EmittedCount), nullableString(outcome.FailureCode),
		outcome.CompletedAt.UTC().UnixMicro())
	if err != nil {
		return u.fail(fmt.Errorf("put parser outcome for delivery %q: %w", outcome.DeliveryID, err))
	}
	return nil
}

// PutDetectionContribution inserts one Event/Rule-version membership. The
// returned boolean is true only for the first durable candidate in this transaction.
func (u *UnitOfWork) PutDetectionContribution(ctx context.Context, contribution core.DetectionContribution) (bool, error) {
	if err := u.ready(ctx); err != nil {
		return false, err
	}
	if err := contribution.Validate(); err != nil {
		return false, u.fail(fmt.Errorf("put detection contribution: validate: %w", err))
	}
	result, err := u.tx.ExecContext(ctx, `
		INSERT INTO detection_contributions(
			event_id, rule_id, rule_version, delivery_id, contributed_at_us
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(event_id, rule_id, rule_version) DO NOTHING`,
		string(contribution.EventID), string(contribution.RuleID), string(contribution.RuleVersion),
		string(contribution.DeliveryID), contribution.ContributedAt.UTC().UnixMicro())
	if err != nil {
		return false, u.fail(fmt.Errorf("put detection contribution for event %q: %w", contribution.EventID, err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, u.fail(fmt.Errorf("put detection contribution for event %q: affected rows: %w", contribution.EventID, err))
	}
	if affected == 1 {
		return true, nil
	}
	var deliveryID string
	if err := u.tx.QueryRowContext(ctx, `
		SELECT delivery_id FROM detection_contributions
		WHERE event_id = ? AND rule_id = ? AND rule_version = ?`,
		string(contribution.EventID), string(contribution.RuleID), string(contribution.RuleVersion)).Scan(&deliveryID); err != nil {
		return false, u.fail(fmt.Errorf("put detection contribution for event %q: verify duplicate: %w", contribution.EventID, err))
	}
	if deliveryID != string(contribution.DeliveryID) {
		return false, u.fail(fmt.Errorf("put detection contribution for event %q: stable delivery identity differs", contribution.EventID))
	}
	return false, nil
}

// PutDetectionOutcome inserts one applicable Event/Rule revision's terminal result.
func (u *UnitOfWork) PutDetectionOutcome(ctx context.Context, outcome core.DetectionTerminalOutcome) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := outcome.Validate(); err != nil {
		return u.fail(fmt.Errorf("put detection outcome: validate: %w", err))
	}
	kind, err := detectionOutcomeKindValue(outcome.Kind)
	if err != nil {
		return u.fail(err)
	}
	_, err = u.tx.ExecContext(ctx, `
		INSERT INTO detection_terminal_outcomes(
			delivery_id, event_id, rule_id, rule_version, kind,
			failure_code, completed_at_us
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(outcome.DeliveryID), string(outcome.EventID), string(outcome.RuleID),
		string(outcome.RuleVersion), kind, nullableString(outcome.FailureCode),
		outcome.CompletedAt.UTC().UnixMicro())
	if err != nil {
		return u.fail(fmt.Errorf("put detection outcome for event %q: %w", outcome.EventID, err))
	}
	return nil
}

// PutAlert inserts one durable Alert tied to a detection membership.
func (u *UnitOfWork) PutAlert(ctx context.Context, alert core.Alert) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := alert.Validate(); err != nil {
		return u.fail(fmt.Errorf("put alert: validate: %w", err))
	}
	_, err := u.tx.ExecContext(ctx, `
		INSERT INTO alerts(
			alert_id, node_id, event_id, rule_id, rule_version,
			canonical_target, observed_at_us, created_at_us
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(alert.ID), string(alert.NodeID), string(alert.EventID), string(alert.RuleID),
		string(alert.RuleVersion), alert.CanonicalTarget.String(),
		alert.ObservedAt.UTC().UnixMicro(), alert.CreatedAt.UTC().UnixMicro())
	if err != nil {
		return u.fail(fmt.Errorf("put alert %q: %w", alert.ID, err))
	}
	return nil
}

// PutDecision inserts one immutable Decision identity and lifecycle row.
func (u *UnitOfWork) PutDecision(ctx context.Context, decision core.Decision) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := decision.Validate(); err != nil {
		return u.fail(fmt.Errorf("put decision: validate: %w", err))
	}
	if decision.Source == core.DecisionSourceAutomatic && decision.RuleVersion == nil {
		return u.fail(fmt.Errorf("put decision: automatic decision requires rule version"))
	}
	if decision.Source == core.DecisionSourceManual &&
		(decision.RuleID != nil || decision.RuleVersion != nil || decision.AlertID != nil) {
		return u.fail(fmt.Errorf("put decision: manual decision cannot reference rule or alert"))
	}
	if decision.CreatedAt.IsZero() || decision.UpdatedAt.IsZero() || decision.LastTriggeredAt.IsZero() {
		return u.fail(fmt.Errorf("put decision: lifecycle times are required"))
	}

	source, err := decisionSourceValue(decision.Source)
	if err != nil {
		return u.fail(err)
	}
	state, err := decisionStateValue(decision.State)
	if err != nil {
		return u.fail(err)
	}

	_, err = u.tx.ExecContext(ctx, `
		INSERT INTO decisions(
			decision_id, node_id, source, rule_id, rule_version, alert_id,
			canonical_target, created_at_us, updated_at_us, last_triggered_at_us,
			expires_at_us, ended_at_us, state, end_reason, suppressed_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(decision.ID), string(decision.NodeID), source,
		nullableRuleID(decision.RuleID), nullableRuleVersion(decision.RuleVersion),
		nullableAlertID(decision.AlertID), decision.CanonicalTarget.String(),
		decision.CreatedAt.UTC().UnixMicro(), decision.UpdatedAt.UTC().UnixMicro(),
		decision.LastTriggeredAt.UTC().UnixMicro(), nullableTime(decision.ExpiresAt),
		nullableTime(decision.EndedAt), state, nullableEndReason(decision.EndReason),
		decision.SuppressedCount)
	if err != nil {
		return u.fail(fmt.Errorf("put decision %q: %w", decision.ID, err))
	}
	return nil
}

// PutProjection replaces the materialized projection for one canonical target.
func (u *UnitOfWork) PutProjection(
	ctx context.Context,
	projection core.DesiredBanProjection,
	updatedAt time.Time,
) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := projection.Validate(); err != nil {
		return u.fail(fmt.Errorf("put projection: validate: %w", err))
	}
	if updatedAt.IsZero() {
		return u.fail(fmt.Errorf("put projection: updated time is required"))
	}

	state := ""
	switch projection.State {
	case core.BanProjectionAbsent:
		if projection.ActiveCount != 0 || projection.EffectiveUntil != nil {
			return u.fail(fmt.Errorf("put projection: absent projection must be empty"))
		}
		state = "absent"
	case core.BanProjectionPresent:
		if projection.ActiveCount == 0 {
			return u.fail(fmt.Errorf("put projection: present projection requires active decisions"))
		}
		state = "present"
	default:
		return u.fail(fmt.Errorf("put projection: unsupported state %d", projection.State))
	}

	result, err := u.tx.ExecContext(ctx, `
		INSERT INTO desired_ban_projections(
			node_id, canonical_target, state, active_count, effective_until_us,
			target_projection_revision, updated_at_us
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, canonical_target) DO UPDATE SET
			state = excluded.state,
			active_count = excluded.active_count,
			effective_until_us = excluded.effective_until_us,
			target_projection_revision = excluded.target_projection_revision,
			updated_at_us = excluded.updated_at_us
		WHERE excluded.target_projection_revision > desired_ban_projections.target_projection_revision`,
		string(projection.NodeID), projection.CanonicalTarget.String(), state,
		projection.ActiveCount, nullableTime(projection.EffectiveUntil), projection.Revision,
		updatedAt.UTC().UnixMicro())
	if err != nil {
		return u.fail(fmt.Errorf("put projection %q: %w", projection.CanonicalTarget, err))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return u.fail(fmt.Errorf("put projection %q: read affected rows: %w", projection.CanonicalTarget, err))
	}
	if affected == 1 {
		return nil
	}

	var identical int
	err = u.tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM desired_ban_projections
			WHERE node_id = ? AND canonical_target = ?
				AND state = ? AND active_count = ? AND effective_until_us IS ?
				AND target_projection_revision = ?
		)`,
		string(projection.NodeID), projection.CanonicalTarget.String(), state,
		projection.ActiveCount, nullableTime(projection.EffectiveUntil), projection.Revision,
	).Scan(&identical)
	if err != nil {
		return u.fail(fmt.Errorf("put projection %q: verify idempotent revision: %w", projection.CanonicalTarget, err))
	}
	if identical == 1 {
		return nil
	}
	return u.fail(fmt.Errorf(
		"put projection %q: stale or conflicting revision %d",
		projection.CanonicalTarget, projection.Revision))
}

// AppendCriticalAudit appends a critical audit row in this UnitOfWork.
func (u *UnitOfWork) AppendCriticalAudit(ctx context.Context, audit CriticalAudit) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if audit.ID == "" || audit.IdempotencyKey == "" || audit.NodeID == "" {
		return u.fail(fmt.Errorf("append critical audit: identity fields are required"))
	}
	if audit.Category == "" || audit.Action == "" || audit.Result == "" ||
		audit.Severity == "" || audit.ActorType == "" || audit.CreatedAt.IsZero() {
		return u.fail(fmt.Errorf("append critical audit: classification fields are required"))
	}
	details := audit.DetailsJSON
	if len(details) == 0 {
		details = []byte("{}")
	}
	if !json.Valid(details) {
		return u.fail(fmt.Errorf("append critical audit: details must be valid JSON"))
	}

	_, err := u.tx.ExecContext(ctx, `
		INSERT INTO audit_logs(
			audit_id, idempotency_key, node_id, category, action, result, severity,
			critical, actor_type, delivery_id, alert_id, decision_id, error_code,
			details_json, created_at_us
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
		audit.ID, audit.IdempotencyKey, string(audit.NodeID), audit.Category,
		audit.Action, audit.Result, audit.Severity, audit.ActorType,
		nullableDeliveryID(audit.DeliveryID), nullableAlertID(audit.AlertID),
		nullableDecisionID(audit.DecisionID), nullableString(audit.ErrorCode),
		string(details), audit.CreatedAt.UTC().UnixMicro())
	if err != nil {
		return u.fail(fmt.Errorf("append critical audit %q: %w", audit.ID, err))
	}
	return nil
}

// PutReceipt inserts the terminal record for one delivery.
func (u *UnitOfWork) PutReceipt(ctx context.Context, receipt core.ProcessingReceipt) error {
	if err := u.ready(ctx); err != nil {
		return err
	}
	if err := receipt.Validate(); err != nil {
		return u.fail(fmt.Errorf("put receipt: validate: %w", err))
	}

	position, err := encodePosition(receipt.Position)
	if err != nil {
		return u.fail(fmt.Errorf("put receipt: %w", err))
	}
	kind, failure, err := encodeReceipt(receipt)
	if err != nil {
		return u.fail(err)
	}

	_, err = u.tx.ExecContext(ctx, `
		INSERT INTO processing_receipts(
			delivery_id, source_id, position_kind, generation, device_id, inode,
			start_offset, end_offset, journald_cursor, kind, failure_stage,
			failure_code, sanitized_error, terminal_action, failure_occurred_at_us,
			committed_at_us
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(receipt.DeliveryID), string(receipt.SourceID), position.kind,
		position.generation, position.deviceID, position.inode, position.startOffset,
		position.endOffset, position.cursor, kind, failure.stage, failure.code,
		failure.sanitizedError, failure.action, failure.occurredAt,
		receipt.Committed.UTC().UnixMicro())
	if err != nil {
		return u.fail(fmt.Errorf("put receipt %q: %w", receipt.DeliveryID, err))
	}
	return nil
}

// Commit commits the UnitOfWork. If any prior write failed, Commit rolls the
// transaction back and returns that first failure.
func (u *UnitOfWork) Commit() error {
	if u == nil || u.tx == nil || u.done {
		return errUnitOfWorkClosed
	}
	u.done = true
	if u.failed != nil {
		rollbackErr := u.tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return joinErrors(u.failed, rollbackErr)
		}
		return u.failed
	}
	if err := u.tx.Commit(); err != nil {
		return fmt.Errorf("commit unit of work: %w", err)
	}
	return nil
}

// Rollback rolls back the UnitOfWork.
func (u *UnitOfWork) Rollback() error {
	if u == nil || u.tx == nil || u.done {
		return errUnitOfWorkClosed
	}
	u.done = true
	if err := u.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback unit of work: %w", err)
	}
	return nil
}

func (u *UnitOfWork) ready(ctx context.Context) error {
	if u == nil || u.tx == nil || u.done {
		return errUnitOfWorkClosed
	}
	if ctx == nil {
		return u.fail(fmt.Errorf("unit of work: context is required"))
	}
	if u.failed != nil {
		return u.failed
	}
	return nil
}

func (u *UnitOfWork) fail(err error) error {
	if u != nil && u.failed == nil {
		u.failed = err
	}
	return err
}

type encodedPosition struct {
	kind        string
	generation  any
	deviceID    any
	inode       any
	startOffset any
	endOffset   any
	cursor      any
}

func encodePosition(position core.SourcePosition) (encodedPosition, error) {
	if file, ok := position.File(); ok {
		if file.DeviceID > math.MaxInt64 || file.Inode > math.MaxInt64 ||
			file.StartOffset > math.MaxInt64 || file.EndOffset > math.MaxInt64 {
			return encodedPosition{}, fmt.Errorf("file position exceeds SQLite INTEGER range")
		}
		return encodedPosition{
			kind: "file", generation: file.Generation, deviceID: int64(file.DeviceID),
			inode: int64(file.Inode), startOffset: int64(file.StartOffset),
			endOffset: int64(file.EndOffset),
		}, nil
	}
	if journald, ok := position.Journald(); ok {
		return encodedPosition{kind: "journald", cursor: journald.Cursor}, nil
	}
	return encodedPosition{}, fmt.Errorf("unsupported source position")
}

type encodedFailure struct {
	stage          any
	code           any
	sanitizedError any
	action         any
	occurredAt     any
}

func encodeReceipt(receipt core.ProcessingReceipt) (string, encodedFailure, error) {
	switch receipt.Kind {
	case core.ReceiptSuccess:
		if receipt.Failure != nil {
			return "", encodedFailure{}, fmt.Errorf("put receipt: success cannot contain failure")
		}
		return "success", encodedFailure{}, nil
	case core.ReceiptRecordPermanent:
		if receipt.Failure == nil || receipt.Failure.Stage == "" ||
			receipt.Failure.Code == "" || receipt.Failure.SanitizedError == "" ||
			receipt.Failure.Action == "" || receipt.Failure.OccurredAt.IsZero() {
			return "", encodedFailure{}, fmt.Errorf("put receipt: permanent failure is incomplete")
		}
		return "record_permanent", encodedFailure{
			stage: receipt.Failure.Stage, code: receipt.Failure.Code,
			sanitizedError: receipt.Failure.SanitizedError,
			action:         receipt.Failure.Action,
			occurredAt:     receipt.Failure.OccurredAt.UTC().UnixMicro(),
		}, nil
	default:
		return "", encodedFailure{}, fmt.Errorf("put receipt: unsupported kind %d", receipt.Kind)
	}
}

func parserOutcomeKindValue(kind core.ParserOutcomeKind) (string, error) {
	switch kind {
	case core.ParserOutcomeSuccess:
		return "success", nil
	case core.ParserOutcomeNoMatch:
		return "no_match", nil
	case core.ParserOutcomeRecordPermanent:
		return "record_permanent", nil
	default:
		return "", fmt.Errorf("put parser outcome: unsupported kind %d", kind)
	}
}

func detectionOutcomeKindValue(kind core.DetectionOutcomeKind) (string, error) {
	switch kind {
	case core.DetectionOutcomeSuccess:
		return "success", nil
	case core.DetectionOutcomeRecordPermanent:
		return "record_permanent", nil
	default:
		return "", fmt.Errorf("put detection outcome: unsupported kind %d", kind)
	}
}

func decisionSourceValue(source core.DecisionSource) (string, error) {
	switch source {
	case core.DecisionSourceAutomatic:
		return "automatic", nil
	case core.DecisionSourceManual:
		return "manual", nil
	default:
		return "", fmt.Errorf("put decision: unsupported source %d", source)
	}
}

func decisionStateValue(state core.DecisionState) (string, error) {
	switch state {
	case core.DecisionActive:
		return "active", nil
	case core.DecisionExpired:
		return "expired", nil
	case core.DecisionRevoked:
		return "revoked", nil
	default:
		return "", fmt.Errorf("put decision: unsupported state %d", state)
	}
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().UnixMicro()
}

func nullableRuleID(value *core.RuleID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullableRuleVersion(value *core.RuleVersion) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullableAlertID(value *core.AlertID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullableDecisionID(value *core.DecisionID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullableDeliveryID(value *core.DeliveryID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullableEndReason(value *core.DecisionEndReason) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
