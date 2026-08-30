package decision

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

// AutomaticCreatedAudit is the Decision-owned Critical Audit payload. The
// transaction adapter persists it beside the Decision and Projection.
type AutomaticCreatedAudit struct {
	ID             string
	IdempotencyKey string
	DeliveryID     *core.DeliveryID
	NodeID         core.NodeID
	AlertID        core.AlertID
	DecisionID     core.DecisionID
	CreatedAt      time.Time
}

// AutomaticSuppressedAudit records one duplicate Alert that was folded into
// an existing active Automatic Decision.
type AutomaticSuppressedAudit struct {
	ID             string
	IdempotencyKey string
	DeliveryID     core.DeliveryID
	NodeID         core.NodeID
	AlertID        core.AlertID
	DecisionID     core.DecisionID
	RuleID         core.RuleID
	RuleVersion    core.RuleVersion
	EventID        core.EventID
	CreatedAt      time.Time
}

// ProjectionTransaction contains the shared materialized-Projection writes.
type ProjectionTransaction interface {
	ListActiveDecisions(context.Context, core.NodeID, netip.Prefix) ([]core.Decision, error)
	FindDecisionProjection(context.Context, core.NodeID, netip.Prefix) (core.DesiredBanProjection, bool, error)
	PutDecisionProjection(context.Context, core.DesiredBanProjection, time.Time) error
}

type AutomaticTransaction interface {
	ProjectionTransaction
	InsertAutomaticDecision(context.Context, core.Decision) (bool, error)
	FindDecisionByID(context.Context, core.DecisionID) (core.Decision, bool, error)
	FindActiveAutomaticDecision(context.Context, core.NodeID, core.RuleID, netip.Prefix) (core.Decision, bool, error)
	SuppressAutomaticDecision(context.Context, core.DecisionID, time.Time) (core.Decision, error)
	AppendAutomaticCreatedAudit(context.Context, AutomaticCreatedAudit) error
	AppendAutomaticSuppressedAudit(context.Context, AutomaticSuppressedAudit) error
}

// TransactionAutomaticResult reports the authoritative Decision mutation and
// the new Projection only when the active Decision composition changed.
type TransactionAutomaticResult struct {
	Decision   core.Decision
	Created    bool
	Projection *core.DesiredBanProjection
}

// RecordAutomaticInTransaction creates or suppresses an Automatic Decision.
// Creation, Projection revision, and Critical Audit are written through the
// same caller-owned transaction. A duplicate never refreshes expiry, Rule
// revision, Alert identity, UpdatedAt, or Projection revision.
func RecordAutomaticInTransaction(
	ctx context.Context,
	tx AutomaticTransaction,
	request AutomaticRequest,
) (TransactionAutomaticResult, error) {
	if ctx == nil || tx == nil {
		return TransactionAutomaticResult{}, fmt.Errorf("automatic decision transaction and context are required")
	}
	if request.RuleVersion == nil || request.AlertID == nil {
		return TransactionAutomaticResult{}, fmt.Errorf("automatic decision requires rule version and alert")
	}
	if !core.ValidDeliveryID(request.DeliveryID) || !core.ValidEventID(request.EventID) {
		return TransactionAutomaticResult{}, fmt.Errorf("automatic decision requires canonical delivery and event identities")
	}
	candidate := core.Decision{
		ID: request.DecisionID, NodeID: request.NodeID, Source: core.DecisionSourceAutomatic,
		RuleID: &request.RuleID, RuleVersion: cloneRuleVersion(request.RuleVersion),
		AlertID: cloneAlertID(request.AlertID), CanonicalTarget: request.Target,
		CreatedAt: request.TriggeredAt, UpdatedAt: request.TriggeredAt,
		LastTriggeredAt: request.TriggeredAt, ExpiresAt: cloneDecisionTime(request.ExpiresAt),
		State: core.DecisionActive,
	}
	if err := candidate.Validate(); err != nil {
		return TransactionAutomaticResult{}, fmt.Errorf("validate automatic decision: %w", err)
	}

	inserted, err := tx.InsertAutomaticDecision(ctx, candidate)
	if err != nil {
		return TransactionAutomaticResult{}, err
	}
	if !inserted {
		byID, idFound, err := tx.FindDecisionByID(ctx, request.DecisionID)
		if err != nil {
			return TransactionAutomaticResult{}, err
		}
		current, found, err := tx.FindActiveAutomaticDecision(ctx, request.NodeID, request.RuleID, request.Target)
		if err != nil {
			return TransactionAutomaticResult{}, err
		}
		if !found {
			return TransactionAutomaticResult{}, fmt.Errorf("%w: %s", ErrDecisionIDConflict, request.DecisionID)
		}
		if idFound && byID.ID != current.ID {
			return TransactionAutomaticResult{}, fmt.Errorf("%w: %s", ErrDecisionIDConflict, request.DecisionID)
		}
		if current.SuppressedCount >= math.MaxInt64 {
			return TransactionAutomaticResult{}, ErrSuppressionOverflow
		}
		current, err = tx.SuppressAutomaticDecision(ctx, current.ID, request.TriggeredAt)
		if err != nil {
			return TransactionAutomaticResult{}, err
		}
		projection, changed, err := rebuildProjection(ctx, tx, request.NodeID, request.Target, request.TriggeredAt, false)
		if err != nil {
			return TransactionAutomaticResult{}, err
		}
		if err := tx.AppendAutomaticSuppressedAudit(ctx, automaticSuppressedAudit(request, current.ID)); err != nil {
			return TransactionAutomaticResult{}, err
		}
		result := TransactionAutomaticResult{Decision: current}
		if changed {
			result.Projection = &projection
		}
		return result, nil
	}

	projection, _, err := rebuildProjection(ctx, tx, request.NodeID, request.Target, request.TriggeredAt, true)
	if err != nil {
		return TransactionAutomaticResult{}, err
	}
	audit := automaticCreatedAudit(request)
	if err := tx.AppendAutomaticCreatedAudit(ctx, audit); err != nil {
		return TransactionAutomaticResult{}, err
	}
	return TransactionAutomaticResult{Decision: candidate, Created: true, Projection: &projection}, nil
}

func rebuildProjection(
	ctx context.Context,
	tx ProjectionTransaction,
	nodeID core.NodeID,
	target netip.Prefix,
	updatedAt time.Time,
	forceRevision bool,
) (core.DesiredBanProjection, bool, error) {
	current, found, err := tx.FindDecisionProjection(ctx, nodeID, target)
	if err != nil {
		return core.DesiredBanProjection{}, false, err
	}
	revision := core.TargetProjectionRevision(1)
	if found {
		revision = current.Revision
	}
	active, err := tx.ListActiveDecisions(ctx, nodeID, target)
	if err != nil {
		return core.DesiredBanProjection{}, false, err
	}
	desired, err := AggregateProjection(nodeID, target, revision, active)
	if err != nil {
		return core.DesiredBanProjection{}, false, err
	}
	if found && !forceRevision && sameProjectionContent(current, desired) {
		return current, false, nil
	}
	if found {
		if current.Revision >= core.TargetProjectionRevision(math.MaxInt64) {
			return core.DesiredBanProjection{}, false, fmt.Errorf("target projection revision overflow")
		}
		desired.Revision = current.Revision + 1
	}
	if err := tx.PutDecisionProjection(ctx, desired, updatedAt); err != nil {
		return core.DesiredBanProjection{}, false, err
	}
	return desired, true, nil
}

func sameProjectionContent(left, right core.DesiredBanProjection) bool {
	if left.NodeID != right.NodeID || left.CanonicalTarget != right.CanonicalTarget ||
		left.State != right.State || left.ActiveCount != right.ActiveCount {
		return false
	}
	if left.EffectiveUntil == nil || right.EffectiveUntil == nil {
		return left.EffectiveUntil == nil && right.EffectiveUntil == nil
	}
	return left.EffectiveUntil.Equal(*right.EffectiveUntil)
}

func automaticCreatedAudit(request AutomaticRequest) AutomaticCreatedAudit {
	digest := sha256.Sum256([]byte("automatic-decision-created:" + string(request.DecisionID)))
	hexDigest := hex.EncodeToString(digest[:])
	deliveryID := request.DeliveryID
	return AutomaticCreatedAudit{
		ID: "audit-decision-" + hexDigest, IdempotencyKey: "decision-created:" + hexDigest,
		DeliveryID: &deliveryID, NodeID: request.NodeID, AlertID: *request.AlertID,
		DecisionID: request.DecisionID, CreatedAt: request.TriggeredAt,
	}
}

func automaticSuppressedAudit(request AutomaticRequest, decisionID core.DecisionID) AutomaticSuppressedAudit {
	digest := decisionIdentityDigest(
		"decision:suppress", string(request.DeliveryID), string(request.EventID),
		string(request.RuleID), string(*request.RuleVersion),
	)
	hexDigest := hex.EncodeToString(digest[:])
	return AutomaticSuppressedAudit{
		ID: "audit-suppression-" + hexDigest, IdempotencyKey: "decision-suppressed:" + hexDigest,
		DeliveryID: request.DeliveryID, NodeID: request.NodeID, AlertID: *request.AlertID,
		DecisionID: decisionID, RuleID: request.RuleID, RuleVersion: *request.RuleVersion,
		EventID: request.EventID, CreatedAt: request.TriggeredAt,
	}
}

func decisionIdentityDigest(parts ...string) [sha256.Size]byte {
	size := 0
	for _, part := range parts {
		size += 4 + len(part)
	}
	frame := make([]byte, 0, size)
	var length [4]byte
	for _, part := range parts {
		binary.BigEndian.PutUint32(length[:], uint32(len(part)))
		frame = append(frame, length[:]...)
		frame = append(frame, part...)
	}
	return sha256.Sum256(frame)
}
