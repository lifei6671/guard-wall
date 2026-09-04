package core

import (
	"fmt"
	"sort"
	"time"
)

// ObservedPresence distinguishes an unavailable observation from an
// authoritative absence or presence.
type ObservedPresence uint8

const (
	ObservedPresenceUnknown ObservedPresence = iota
	ObservedPresenceAbsent
	ObservedPresencePresent
)

// InfrastructureObservedState is the latest authoritative physical
// infrastructure observation and its optional confirmed Desired fence.
type InfrastructureObservedState struct {
	Presence          ObservedPresence
	ObservedAt        time.Time
	Backend           string
	OwnerVersion      string
	Digest            string
	ConfirmedRevision InfrastructureRevision
	LastErrorCode     string
}

// PolicyObservedState is the latest authoritative physical policy
// observation and its optional confirmed Desired fence.
type PolicyObservedState struct {
	Presence          ObservedPresence
	ObservedAt        time.Time
	RelationDigest    string
	ConfirmedRevision PolicyRevision
	LastErrorCode     string
}

// TargetObservedState is the latest authoritative physical Target
// observation and its optional confirmed Desired generation.
type TargetObservedState struct {
	PhysicalTargetObserved
	ConfirmedGeneration TargetEnforcementGeneration
}

// TargetObservationEvidence records which physical target fields the backend
// actually observed. Complete evidence carries the full legacy ownership and
// policy projection. ManagedSnapshot is limited to the target facts exposed by
// firewall.ManagedSnapshot. A confirmation with this evidence covers only the
// target facts it exposes; Policy and Infrastructure remain separate domains.
type TargetObservationEvidence uint8

const (
	TargetObservationEvidenceComplete TargetObservationEvidence = iota
	TargetObservationEvidenceManagedSnapshot
)

// ObservedFirewallUpdate atomically replaces Infrastructure and Policy when
// included and upserts each included Target key. Nil Infrastructure/Policy
// values, an empty Targets slice, and unlisted Target keys remain unchanged.
type ObservedFirewallUpdate struct {
	NodeID         NodeID
	Infrastructure *InfrastructureObservedState
	Policy         *PolicyObservedState
	Targets        []TargetObservedState
}

// ObservedFirewallSnapshot is the latest durable Observed cache for one node.
// Infrastructure and Policy are nil until their first observation is written.
type ObservedFirewallSnapshot struct {
	NodeID         NodeID
	Infrastructure *InfrastructureObservedState
	Policy         *PolicyObservedState
	Targets        []TargetObservedState
}

// Validate verifies an Observed update before it crosses a persistence boundary.
func (u ObservedFirewallUpdate) Validate() error {
	if !isLowerHex128(string(u.NodeID)) {
		return fmt.Errorf("node id must be 128-bit lowercase hex")
	}
	if u.Infrastructure == nil && u.Policy == nil && len(u.Targets) == 0 {
		return fmt.Errorf("at least one Observed domain update is required")
	}
	if u.Infrastructure != nil {
		if err := u.Infrastructure.Validate(); err != nil {
			return fmt.Errorf("infrastructure: %w", err)
		}
	}
	if u.Policy != nil {
		if err := u.Policy.Validate(); err != nil {
			return fmt.Errorf("policy: %w", err)
		}
	}
	targets := append([]TargetObservedState(nil), u.Targets...)
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].CanonicalTarget.String() < targets[j].CanonicalTarget.String()
	})
	for index, target := range targets {
		if err := target.Validate(); err != nil {
			return fmt.Errorf("target %d: %w", index, err)
		}
		if index != 0 && targets[index-1].CanonicalTarget == target.CanonicalTarget {
			return fmt.Errorf("target %s is duplicated", target.CanonicalTarget)
		}
	}
	return nil
}

// Validate verifies one Infrastructure observation.
func (s InfrastructureObservedState) Validate() error {
	if err := validateObservedTime(s.ObservedAt); err != nil {
		return err
	}
	switch s.Presence {
	case ObservedPresenceUnknown:
		if s.Backend != "" || s.OwnerVersion != "" || s.Digest != "" || s.ConfirmedRevision != 0 {
			return fmt.Errorf("unknown observation cannot contain physical or confirmed state")
		}
		if s.LastErrorCode == "" {
			return fmt.Errorf("unknown observation requires an error code")
		}
	case ObservedPresenceAbsent:
		if s.Backend != "" || s.OwnerVersion != "" || s.Digest != "" || s.ConfirmedRevision != 0 || s.LastErrorCode != "" {
			return fmt.Errorf("absent observation cannot contain physical, confirmed, or error state")
		}
	case ObservedPresencePresent:
		if s.Backend == "" || s.OwnerVersion == "" || s.Digest == "" {
			return fmt.Errorf("present observation requires complete physical state")
		}
		if s.LastErrorCode != "" {
			return fmt.Errorf("present observation cannot contain an error code")
		}
	default:
		return fmt.Errorf("presence is invalid")
	}
	return nil
}

// Validate verifies one Policy observation.
func (s PolicyObservedState) Validate() error {
	if err := validateObservedTime(s.ObservedAt); err != nil {
		return err
	}
	switch s.Presence {
	case ObservedPresenceUnknown:
		if s.RelationDigest != "" || s.ConfirmedRevision != 0 {
			return fmt.Errorf("unknown observation cannot contain physical or confirmed state")
		}
		if s.LastErrorCode == "" {
			return fmt.Errorf("unknown observation requires an error code")
		}
	case ObservedPresenceAbsent:
		if s.RelationDigest != "" || s.ConfirmedRevision != 0 || s.LastErrorCode != "" {
			return fmt.Errorf("absent observation cannot contain physical, confirmed, or error state")
		}
	case ObservedPresencePresent:
		if s.RelationDigest == "" {
			return fmt.Errorf("present observation requires a relation digest")
		}
		if s.LastErrorCode != "" {
			return fmt.Errorf("present observation cannot contain an error code")
		}
	default:
		return fmt.Errorf("presence is invalid")
	}
	return nil
}

// Validate verifies one Target observation.
func (s TargetObservedState) Validate() error {
	if !s.CanonicalTarget.IsValid() || s.CanonicalTarget != s.CanonicalTarget.Masked() {
		return fmt.Errorf("canonical target is invalid")
	}
	if err := validateObservedTime(s.ObservedAt); err != nil {
		return err
	}
	switch s.BanMembership {
	case ObservedMembershipUnknown:
		if s.Evidence != TargetObservationEvidenceComplete || s.hasTargetPhysicalState() || s.ConfirmedGeneration != 0 {
			return fmt.Errorf("unknown observation cannot contain physical or confirmed state")
		}
		if s.LastErrorCode == "" {
			return fmt.Errorf("unknown observation requires an error code")
		}
	case ObservedMembershipAbsent:
		if !s.validEvidence() {
			return fmt.Errorf("absent observation has invalid evidence")
		}
		if s.hasTargetPhysicalState() || s.LastErrorCode != "" {
			return fmt.Errorf("absent observation cannot contain physical or error state")
		}
	case ObservedMembershipPresent:
		if err := s.validatePresent(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("membership is invalid")
	}
	return nil
}

func (s TargetObservedState) validatePresent() error {
	if !s.validEvidence() {
		return fmt.Errorf("present observation has invalid evidence")
	}
	if s.Scopes == 0 || (s.AddressFamily != AddressFamilyIPv4 && s.AddressFamily != AddressFamilyIPv6) {
		return fmt.Errorf("present observation requires target identity")
	}
	if s.Scopes&^(ScopeInput|ScopeForward) != 0 {
		return fmt.Errorf("present observation has unsupported enforcement scopes")
	}
	wantFamily := AddressFamilyIPv6
	if s.CanonicalTarget.Addr().Is4() {
		wantFamily = AddressFamilyIPv4
	}
	if s.AddressFamily != wantFamily {
		return fmt.Errorf("present observation address family does not match target")
	}
	if s.TimeoutMode != TimeoutNone && s.TimeoutMode != TimeoutNative {
		return fmt.Errorf("present observation has invalid timeout mode")
	}
	if (s.TimeoutMode == TimeoutNative) != (s.NativeExpiry != nil) {
		return fmt.Errorf("native timeout and expiry must be present together")
	}
	if s.NativeExpiry != nil && (s.NativeExpiry.IsZero() || s.NativeExpiry.UTC().UnixMicro() <= 0) {
		return fmt.Errorf("native expiry is invalid")
	}
	if s.LastErrorCode != "" {
		return fmt.Errorf("present observation cannot contain an error code")
	}
	if s.Evidence == TargetObservationEvidenceManagedSnapshot {
		if s.Backend != "" || s.OwnerVersion != "" || s.PolicyCoverage != ObservedPolicyUnknown ||
			s.PolicyRelationDigest != "" {
			return fmt.Errorf("managed snapshot observation cannot contain inferred state")
		}
		return nil
	}
	if s.Backend == "" || s.OwnerVersion == "" {
		return fmt.Errorf("present observation requires complete physical identity")
	}
	if s.PolicyCoverage < ObservedPolicyNone || s.PolicyCoverage > ObservedPolicyFull {
		return fmt.Errorf("present observation has invalid policy coverage")
	}
	if s.PolicyCoverage == ObservedPolicyNone && s.PolicyRelationDigest != "" {
		return fmt.Errorf("no policy coverage requires an empty relation digest")
	}
	if s.PolicyCoverage != ObservedPolicyNone && s.PolicyRelationDigest == "" {
		return fmt.Errorf("covered observation requires a relation digest")
	}
	return nil
}

func (s TargetObservedState) validEvidence() bool {
	return s.Evidence == TargetObservationEvidenceComplete ||
		s.Evidence == TargetObservationEvidenceManagedSnapshot
}

func (s TargetObservedState) hasTargetPhysicalState() bool {
	return s.Backend != "" || s.PolicyCoverage != ObservedPolicyUnknown ||
		s.PolicyRelationDigest != "" || s.TimeoutMode != TimeoutNone || s.NativeExpiry != nil ||
		s.Scopes != 0 || s.AddressFamily != 0 || s.OwnerVersion != ""
}

func validateObservedTime(observedAt time.Time) error {
	if observedAt.IsZero() || observedAt.UTC().UnixMicro() <= 0 {
		return fmt.Errorf("observed time is invalid")
	}
	return nil
}
