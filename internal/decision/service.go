package decision

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

var (
	ErrAlreadyBanned       = errors.New("target already has an active manual ban")
	ErrSuppressionOverflow = errors.New("automatic decision suppression count overflow")
	ErrDecisionNotFound    = errors.New("decision not found")
	ErrDecisionIDConflict  = errors.New("decision id already exists")
	ErrTerminalConflict    = errors.New("decision is already terminal with another outcome")
)

// AlreadyBannedError identifies the active Manual Decision that rejected a duplicate request.
type AlreadyBannedError struct {
	DecisionID core.DecisionID
}

func (e *AlreadyBannedError) Error() string {
	return fmt.Sprintf("%v: %s", ErrAlreadyBanned, e.DecisionID)
}

func (e *AlreadyBannedError) Unwrap() error { return ErrAlreadyBanned }

// TerminalConflictError preserves the existing terminal outcome instead of overwriting it.
type TerminalConflictError struct {
	DecisionID      core.DecisionID
	State           core.DecisionState
	ExistingReason  *core.DecisionEndReason
	RequestedReason core.DecisionEndReason
}

func (e *TerminalConflictError) Error() string {
	return fmt.Sprintf("%v: %s", ErrTerminalConflict, e.DecisionID)
}

func (e *TerminalConflictError) Unwrap() error { return ErrTerminalConflict }

// AutomaticRequest contains the immutable creation facts for an Automatic Decision trigger.
type AutomaticRequest struct {
	DecisionID  core.DecisionID
	DeliveryID  core.DeliveryID
	EventID     core.EventID
	NodeID      core.NodeID
	RuleID      core.RuleID
	RuleVersion *core.RuleVersion
	AlertID     *core.AlertID
	Target      netip.Prefix
	TriggeredAt time.Time
	ExpiresAt   *time.Time
}

// AutomaticResult reports whether a new Decision was created.
type AutomaticResult struct {
	Decision core.Decision
	Created  bool
}

// ManualRequest contains one explicit Manual Ban request.
type ManualRequest struct {
	DecisionID core.DecisionID
	NodeID     core.NodeID
	Target     netip.Prefix
	CreatedAt  time.Time
	ExpiresAt  *time.Time
}

// ManualResult returns both sides of an atomic replace when Replaced is true.
type ManualResult struct {
	Previous           *core.Decision
	Current            core.Decision
	Replaced           bool
	EnforcementChanges []TargetEnforcementChange
}

// MemoryService is the M0 in-memory Decision write boundary. Its mutex represents the atomic
// transaction boundary that later SQLite code must replace, not emulate with a read-then-write.
type MemoryService struct {
	mu        sync.Mutex
	decisions []core.Decision
}

// NewMemoryService creates an empty lifecycle service.
func NewMemoryService() *MemoryService { return &MemoryService{} }

// RecordAutomatic creates one Active Automatic Decision or atomically suppresses a duplicate.
func (s *MemoryService) RecordAutomatic(request AutomaticRequest) (AutomaticResult, error) {
	if request.RuleVersion == nil {
		return AutomaticResult{}, fmt.Errorf("automatic decision requires rule version")
	}
	candidate := core.Decision{
		ID: request.DecisionID, NodeID: request.NodeID, Source: core.DecisionSourceAutomatic,
		RuleID: &request.RuleID, RuleVersion: cloneRuleVersion(request.RuleVersion), AlertID: cloneAlertID(request.AlertID),
		CanonicalTarget: request.Target, CreatedAt: request.TriggeredAt, UpdatedAt: request.TriggeredAt,
		LastTriggeredAt: request.TriggeredAt, ExpiresAt: cloneDecisionTime(request.ExpiresAt), State: core.DecisionActive,
	}
	if err := candidate.Validate(); err != nil {
		return AutomaticResult{}, fmt.Errorf("validate automatic decision: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.decisions {
		current := &s.decisions[index]
		if current.State != core.DecisionActive || current.Source != core.DecisionSourceAutomatic ||
			current.NodeID != request.NodeID || current.CanonicalTarget != request.Target ||
			current.RuleID == nil || *current.RuleID != request.RuleID {
			continue
		}
		if current.SuppressedCount == ^uint64(0) {
			return AutomaticResult{}, ErrSuppressionOverflow
		}
		if request.TriggeredAt.After(current.LastTriggeredAt) {
			current.LastTriggeredAt = request.TriggeredAt
		}
		current.SuppressedCount++
		return AutomaticResult{Decision: cloneDecision(*current)}, nil
	}
	if s.decisionIDExistsLocked(request.DecisionID) {
		return AutomaticResult{}, fmt.Errorf("%w: %s", ErrDecisionIDConflict, request.DecisionID)
	}
	s.decisions = append(s.decisions, candidate)
	return AutomaticResult{Decision: cloneDecision(candidate), Created: true}, nil
}

// BanManual creates a Manual Decision. Replace atomically revokes the old Manual Decision with
// manual_replace before publishing the new Active Decision.
func (s *MemoryService) BanManual(request ManualRequest, replace bool) (ManualResult, error) {
	candidate := core.Decision{
		ID: request.DecisionID, NodeID: request.NodeID, Source: core.DecisionSourceManual,
		CanonicalTarget: request.Target, CreatedAt: request.CreatedAt, UpdatedAt: request.CreatedAt,
		LastTriggeredAt: request.CreatedAt, ExpiresAt: cloneDecisionTime(request.ExpiresAt), State: core.DecisionActive,
	}
	if err := candidate.Validate(); err != nil {
		return ManualResult{}, fmt.Errorf("validate manual decision: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.decisionIDExistsLocked(request.DecisionID) {
		return ManualResult{}, fmt.Errorf("%w: %s", ErrDecisionIDConflict, request.DecisionID)
	}
	for index := range s.decisions {
		current := s.decisions[index]
		if current.State != core.DecisionActive || current.Source != core.DecisionSourceManual ||
			current.NodeID != request.NodeID || current.CanonicalTarget != request.Target {
			continue
		}
		if !replace {
			return ManualResult{}, &AlreadyBannedError{DecisionID: current.ID}
		}
		if request.CreatedAt.Before(current.CreatedAt) {
			return ManualResult{}, fmt.Errorf("manual replacement precedes active decision")
		}
		endedAt := request.CreatedAt
		reason := core.EndReasonManualReplace
		current.State = core.DecisionRevoked
		current.UpdatedAt = request.CreatedAt
		current.EndedAt = &endedAt
		current.EndReason = &reason
		if err := current.Validate(); err != nil {
			return ManualResult{}, fmt.Errorf("validate replaced decision: %w", err)
		}
		s.decisions[index] = current
		s.decisions = append(s.decisions, candidate)
		previous := cloneDecision(current)
		return ManualResult{Previous: &previous, Current: cloneDecision(candidate), Replaced: true}, nil
	}
	s.decisions = append(s.decisions, candidate)
	return ManualResult{Current: cloneDecision(candidate)}, nil
}

// Expire atomically terminates every due Active Decision. Terminal Decisions are unchanged.
func (s *MemoryService) Expire(now time.Time) ([]core.Decision, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("expiration time is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := make([]core.Decision, 0)
	for index := range s.decisions {
		current := s.decisions[index]
		if current.State != core.DecisionActive || current.ExpiresAt == nil || current.ExpiresAt.After(now) {
			continue
		}
		endedAt := now
		reason := core.EndReasonExpired
		current.State = core.DecisionExpired
		current.UpdatedAt = now
		current.EndedAt = &endedAt
		current.EndReason = &reason
		if err := current.Validate(); err != nil {
			return nil, fmt.Errorf("validate expired decision: %w", err)
		}
		s.decisions[index] = current
		expired = append(expired, cloneDecision(current))
	}
	return expired, nil
}

// Revoke terminates one Active Decision without depending on Firewall removal success.
func (s *MemoryService) Revoke(id core.DecisionID, endedAt time.Time, reason core.DecisionEndReason) (core.Decision, error) {
	if endedAt.IsZero() {
		return core.Decision{}, fmt.Errorf("revoke time is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.decisions {
		current := s.decisions[index]
		if current.ID != id {
			continue
		}
		if current.State != core.DecisionActive {
			if current.State == core.DecisionRevoked && current.EndReason != nil && *current.EndReason == reason {
				return cloneDecision(current), nil
			}
			return core.Decision{}, &TerminalConflictError{
				DecisionID: current.ID, State: current.State, ExistingReason: cloneEndReason(current.EndReason), RequestedReason: reason,
			}
		}
		end := endedAt
		endReason := reason
		current.State = core.DecisionRevoked
		current.UpdatedAt = endedAt
		current.EndedAt = &end
		current.EndReason = &endReason
		if err := current.Validate(); err != nil {
			return core.Decision{}, fmt.Errorf("validate revoked decision: %w", err)
		}
		s.decisions[index] = current
		return cloneDecision(current), nil
	}
	return core.Decision{}, ErrDecisionNotFound
}

// Decisions returns an immutable snapshot of all Decision history.
func (s *MemoryService) Decisions() []core.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]core.Decision, len(s.decisions))
	for index, current := range s.decisions {
		result[index] = cloneDecision(current)
	}
	return result
}

func (s *MemoryService) decisionIDExistsLocked(id core.DecisionID) bool {
	for _, current := range s.decisions {
		if current.ID == id {
			return true
		}
	}
	return false
}

func cloneDecision(value core.Decision) core.Decision {
	value.RuleID = cloneRuleID(value.RuleID)
	value.RuleVersion = cloneRuleVersion(value.RuleVersion)
	value.AlertID = cloneAlertID(value.AlertID)
	value.ExpiresAt = cloneDecisionTime(value.ExpiresAt)
	value.EndedAt = cloneDecisionTime(value.EndedAt)
	if value.EndReason != nil {
		reason := *value.EndReason
		value.EndReason = &reason
	}
	return value
}

func cloneRuleID(value *core.RuleID) *core.RuleID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneRuleVersion(value *core.RuleVersion) *core.RuleVersion {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneAlertID(value *core.AlertID) *core.AlertID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneEndReason(value *core.DecisionEndReason) *core.DecisionEndReason {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneDecisionTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
