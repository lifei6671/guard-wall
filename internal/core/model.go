// Package core defines the strongly typed values shared by the M0 contract slices.
package core

import (
	"fmt"
	"net/netip"
	"sort"
	"time"
)

type (
	NodeID        string
	SourceID      string
	DeliveryID    string
	EventID       string
	ParserID      string
	ParserVersion string
	RuleID        string
	RuleVersion   string
	AlertID       string
	DecisionID    string
)

type (
	DeliverySequence            uint64
	TargetProjectionRevision    uint64
	TargetEnforcementGeneration uint64
	PolicyRevision              uint64
	InfrastructureRevision      uint64
	SnapshotRevision            uint64
	RetryEpoch                  uint64
)

// FilePosition identifies one half-open record range in a persisted file generation.
type FilePosition struct {
	Generation  string
	DeviceID    uint64
	Inode       uint64
	StartOffset uint64
	EndOffset   uint64
}

// JournaldPosition stores the opaque journal cursor without interpreting it.
type JournaldPosition struct {
	Cursor string
}

type sourcePositionKind uint8

const (
	positionFile sourcePositionKind = iota + 1
	positionJournald
)

// SourcePosition is a closed union of FilePosition and JournaldPosition.
type SourcePosition struct {
	kind     sourcePositionKind
	file     FilePosition
	journald JournaldPosition
}

// NewFilePosition validates and constructs a file position.
func NewFilePosition(position FilePosition) (SourcePosition, error) {
	if !isLowerHex128(position.Generation) {
		return SourcePosition{}, fmt.Errorf("file generation must be 128-bit lowercase hex")
	}
	if position.StartOffset > position.EndOffset {
		return SourcePosition{}, fmt.Errorf("file position start %d exceeds end %d", position.StartOffset, position.EndOffset)
	}
	return SourcePosition{kind: positionFile, file: position}, nil
}

// NewJournaldPosition validates and constructs an opaque journal position.
func NewJournaldPosition(cursor string) (SourcePosition, error) {
	if err := validateTextIdentifier("journald cursor", cursor); err != nil {
		return SourcePosition{}, err
	}
	return SourcePosition{kind: positionJournald, journald: JournaldPosition{Cursor: cursor}}, nil
}

// File returns the file variant when this position represents a file record.
func (p SourcePosition) File() (FilePosition, bool) {
	return p.file, p.kind == positionFile
}

// Journald returns the journald variant when this position represents a cursor.
func (p SourcePosition) Journald() (JournaldPosition, bool) {
	return p.journald, p.kind == positionJournald
}

// Valid reports whether the position contains exactly one supported variant.
func (p SourcePosition) Valid() bool {
	switch p.kind {
	case positionFile:
		return isLowerHex128(p.file.Generation) && p.file.StartOffset <= p.file.EndOffset
	case positionJournald:
		return validateTextIdentifier("journald cursor", p.journald.Cursor) == nil
	default:
		return false
	}
}

// RawRecord is the typed input handed from a Source to the processing pipeline.
type RawRecord struct {
	SourceID   SourceID
	ObservedAt time.Time
	Position   SourcePosition
	Content    []byte
	Metadata   map[string]string
}

// Delivery combines a stable identity with session-local ordering.
type Delivery struct {
	ID       DeliveryID
	Sequence DeliverySequence
	Record   RawRecord
}

// Validate checks the delivery boundary before any processing side effect.
func (d Delivery) Validate() error {
	if !ValidDeliveryID(d.ID) {
		return fmt.Errorf("delivery id is not canonical")
	}
	if d.Sequence == 0 {
		return fmt.Errorf("delivery sequence must be positive")
	}
	if err := validateTextIdentifier("source id", string(d.Record.SourceID)); err != nil {
		return err
	}
	if d.Record.ObservedAt.IsZero() {
		return fmt.Errorf("record observed time is required")
	}
	if !d.Record.Position.Valid() {
		return fmt.Errorf("record source position is invalid")
	}
	expected, err := deliveryIDForPosition(d.Record.SourceID, d.Record.Position)
	if err != nil {
		return fmt.Errorf("derive delivery id: %w", err)
	}
	if d.ID != expected {
		return fmt.Errorf("delivery id does not bind source and position")
	}
	return nil
}

// DurableCompletion proves both ProcessingComplete and SourceDurable for Phase 1.
type DurableCompletion struct {
	SourceID   SourceID
	DeliveryID DeliveryID
	Sequence   DeliverySequence
	Position   SourcePosition
}

// ReceiptKind is the terminal processing result persisted for a delivery.
type ReceiptKind uint8

const (
	ReceiptSuccess ReceiptKind = iota + 1
	ReceiptRecordPermanent
)

// FailureClass classifies whether an attempt may become terminal.
type FailureClass uint8

const (
	FailureRecordPermanent FailureClass = iota + 1
	FailurePlanBlocked
	FailureTransient
	FailureCancelled
)

// PermanentFailure is the sanitized diagnostic stored for a poison record.
type PermanentFailure struct {
	Stage          string
	Code           string
	SanitizedError string
	Action         string
	OccurredAt     time.Time
}

// ProcessingReceipt is the durable terminal record for one DeliveryID.
type ProcessingReceipt struct {
	DeliveryID DeliveryID
	SourceID   SourceID
	Position   SourcePosition
	Kind       ReceiptKind
	Failure    *PermanentFailure
	Committed  time.Time
}

// ParserOutcomeKind is a closed set of durable terminal parser results.
type ParserOutcomeKind uint8

const (
	ParserOutcomeSuccess ParserOutcomeKind = iota + 1
	ParserOutcomeNoMatch
	ParserOutcomeRecordPermanent
)

// ParserTerminalOutcome records one parser version's terminal result for a delivery.
type ParserTerminalOutcome struct {
	DeliveryID    DeliveryID
	ParserID      ParserID
	ParserVersion ParserVersion
	Kind          ParserOutcomeKind
	EmittedCount  uint32
	FailureCode   string
	CompletedAt   time.Time
}

// Validate checks parser terminal-result combinations.
func (o ParserTerminalOutcome) Validate() error {
	if !ValidDeliveryID(o.DeliveryID) {
		return fmt.Errorf("parser outcome delivery id is not canonical")
	}
	if err := validateTextIdentifier("parser id", string(o.ParserID)); err != nil {
		return err
	}
	if err := validateTextIdentifier("parser version", string(o.ParserVersion)); err != nil {
		return err
	}
	if o.CompletedAt.IsZero() {
		return fmt.Errorf("parser outcome completed time is required")
	}
	switch o.Kind {
	case ParserOutcomeSuccess:
		if o.EmittedCount == 0 || o.FailureCode != "" {
			return fmt.Errorf("successful parser outcome requires emitted events and no failure")
		}
	case ParserOutcomeNoMatch:
		if o.EmittedCount != 0 || o.FailureCode != "" {
			return fmt.Errorf("no-match parser outcome cannot contain events or failure")
		}
	case ParserOutcomeRecordPermanent:
		if o.EmittedCount != 0 || o.FailureCode == "" {
			return fmt.Errorf("permanent parser outcome requires failure code and no events")
		}
	default:
		return fmt.Errorf("unsupported parser outcome kind %d", o.Kind)
	}
	return nil
}

// DetectionContribution is the durable Event/Rule-version window membership key.
type DetectionContribution struct {
	DeliveryID    DeliveryID
	EventID       EventID
	RuleID        RuleID
	RuleVersion   RuleVersion
	ContributedAt time.Time
}

// DetectionOutcomeKind is the closed set of durable terminal Rule results for
// one Event and frozen Rule revision.
type DetectionOutcomeKind uint8

const (
	DetectionOutcomeSuccess DetectionOutcomeKind = iota + 1
	DetectionOutcomeRecordPermanent
)

// DetectionTerminalOutcome proves that one applicable Rule revision reached a
// terminal result for an Event in the delivery-owned transaction.
type DetectionTerminalOutcome struct {
	DeliveryID  DeliveryID
	EventID     EventID
	RuleID      RuleID
	RuleVersion RuleVersion
	Kind        DetectionOutcomeKind
	FailureCode string
	CompletedAt time.Time
}

// Validate checks Detection terminal-result combinations.
func (o DetectionTerminalOutcome) Validate() error {
	if !ValidDeliveryID(o.DeliveryID) {
		return fmt.Errorf("detection outcome delivery id is not canonical")
	}
	if !ValidEventID(o.EventID) {
		return fmt.Errorf("detection outcome event id is not canonical")
	}
	if err := validateTextIdentifier("rule id", string(o.RuleID)); err != nil {
		return err
	}
	if err := validateTextIdentifier("rule version", string(o.RuleVersion)); err != nil {
		return err
	}
	if o.CompletedAt.IsZero() {
		return fmt.Errorf("detection outcome completed time is required")
	}
	switch o.Kind {
	case DetectionOutcomeSuccess:
		if o.FailureCode != "" {
			return fmt.Errorf("successful detection outcome cannot contain a failure")
		}
	case DetectionOutcomeRecordPermanent:
		if err := validateTextIdentifier("detection failure code", o.FailureCode); err != nil {
			return fmt.Errorf("permanent detection outcome requires failure code: %w", err)
		}
		if len(o.FailureCode) > 128 {
			return fmt.Errorf("detection failure code exceeds 128 bytes")
		}
	default:
		return fmt.Errorf("unsupported detection outcome kind %d", o.Kind)
	}
	return nil
}

// Validate checks the detection membership identity.
func (c DetectionContribution) Validate() error {
	if !ValidDeliveryID(c.DeliveryID) {
		return fmt.Errorf("detection contribution delivery id is not canonical")
	}
	if !ValidEventID(c.EventID) {
		return fmt.Errorf("detection contribution event id is not canonical")
	}
	if err := validateTextIdentifier("rule id", string(c.RuleID)); err != nil {
		return err
	}
	if err := validateTextIdentifier("rule version", string(c.RuleVersion)); err != nil {
		return err
	}
	if c.ContributedAt.IsZero() {
		return fmt.Errorf("detection contribution time is required")
	}
	return nil
}

// Alert is one durable threshold result tied to an Event and Rule version.
type Alert struct {
	ID              AlertID
	NodeID          NodeID
	EventID         EventID
	RuleID          RuleID
	RuleVersion     RuleVersion
	CanonicalTarget netip.Prefix
	ObservedAt      time.Time
	CreatedAt       time.Time
}

// Validate checks alert identity, target, and lifecycle timestamps.
func (a Alert) Validate() error {
	if err := validateTextIdentifier("alert id", string(a.ID)); err != nil {
		return err
	}
	if !isLowerHex128(string(a.NodeID)) {
		return fmt.Errorf("alert node id must be 128-bit lowercase hex")
	}
	if !ValidEventID(a.EventID) {
		return fmt.Errorf("alert event id is not canonical")
	}
	if err := validateTextIdentifier("rule id", string(a.RuleID)); err != nil {
		return err
	}
	if err := validateTextIdentifier("rule version", string(a.RuleVersion)); err != nil {
		return err
	}
	if !a.CanonicalTarget.IsValid() || a.CanonicalTarget != a.CanonicalTarget.Masked() {
		return fmt.Errorf("alert target must be a canonical prefix")
	}
	if a.ObservedAt.IsZero() || a.CreatedAt.Before(a.ObservedAt) {
		return fmt.Errorf("alert timestamps are invalid")
	}
	return nil
}

// Validate checks terminal receipt combinations before they advance a checkpoint.
func (r ProcessingReceipt) Validate() error {
	if !ValidDeliveryID(r.DeliveryID) {
		return fmt.Errorf("receipt delivery id is not canonical")
	}
	if err := validateTextIdentifier("receipt source id", string(r.SourceID)); err != nil {
		return err
	}
	if !r.Position.Valid() || r.Committed.IsZero() {
		return fmt.Errorf("receipt position and committed time are required")
	}
	expected, err := deliveryIDForPosition(r.SourceID, r.Position)
	if err != nil {
		return fmt.Errorf("derive receipt delivery id: %w", err)
	}
	if r.DeliveryID != expected {
		return fmt.Errorf("receipt delivery id does not bind source and position")
	}
	switch r.Kind {
	case ReceiptSuccess:
		if r.Failure != nil {
			return fmt.Errorf("success receipt cannot contain a permanent failure")
		}
	case ReceiptRecordPermanent:
		if r.Failure == nil {
			return fmt.Errorf("permanent receipt requires failure details")
		}
		if r.Failure.Stage == "" || r.Failure.Code == "" || r.Failure.SanitizedError == "" ||
			r.Failure.Action == "" || r.Failure.OccurredAt.IsZero() {
			return fmt.Errorf("permanent failure is incomplete")
		}
	default:
		return fmt.Errorf("unsupported receipt kind %d", r.Kind)
	}
	return nil
}

// DecisionSource identifies automatic and manual security intent.
type DecisionSource uint8

const (
	DecisionSourceAutomatic DecisionSource = iota + 1
	DecisionSourceManual
)

// DecisionState contains only business lifecycle states, never retry states.
type DecisionState uint8

const (
	DecisionActive DecisionState = iota + 1
	DecisionExpired
	DecisionRevoked
)

// DecisionEndReason records why an active decision became terminal.
type DecisionEndReason string

const (
	EndReasonExpired       DecisionEndReason = "expired"
	EndReasonManual        DecisionEndReason = "manual"
	EndReasonManualReplace DecisionEndReason = "manual_replace"
	EndReasonRuleDisabled  DecisionEndReason = "rule_disabled"
	EndReasonSystemCleanup DecisionEndReason = "system_cleanup"
)

// Decision is the authoritative security intent, independent of Firewall retries.
type Decision struct {
	ID              DecisionID
	NodeID          NodeID
	Source          DecisionSource
	RuleID          *RuleID
	RuleVersion     *RuleVersion
	AlertID         *AlertID
	CanonicalTarget netip.Prefix
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastTriggeredAt time.Time
	ExpiresAt       *time.Time
	EndedAt         *time.Time
	State           DecisionState
	EndReason       *DecisionEndReason
	SuppressedCount uint64
}

// Validate checks the frozen Decision state/source combinations.
func (d Decision) Validate() error {
	if err := validateTextIdentifier("decision id", string(d.ID)); err != nil {
		return err
	}
	if !isLowerHex128(string(d.NodeID)) {
		return fmt.Errorf("node id must be 128-bit lowercase hex")
	}
	if !d.CanonicalTarget.IsValid() || d.CanonicalTarget != d.CanonicalTarget.Masked() {
		return fmt.Errorf("decision target must be a canonical prefix")
	}
	if d.Source == DecisionSourceAutomatic {
		if d.RuleID == nil || validateTextIdentifier("rule id", string(*d.RuleID)) != nil {
			return fmt.Errorf("automatic decision requires a valid rule id")
		}
	}
	if d.Source != DecisionSourceAutomatic && d.Source != DecisionSourceManual {
		return fmt.Errorf("unsupported decision source %d", d.Source)
	}
	if d.CreatedAt.IsZero() || d.UpdatedAt.Before(d.CreatedAt) || d.LastTriggeredAt.Before(d.CreatedAt) {
		return fmt.Errorf("decision timestamps are invalid")
	}
	if d.ExpiresAt != nil && d.ExpiresAt.Before(d.CreatedAt) {
		return fmt.Errorf("decision expiry precedes creation")
	}
	if d.State == DecisionActive {
		if d.EndReason != nil || d.EndedAt != nil {
			return fmt.Errorf("active decision cannot have terminal fields")
		}
		return nil
	}
	if d.EndReason == nil || d.EndedAt == nil {
		return fmt.Errorf("terminal decision requires end reason and ended time")
	}
	if d.EndedAt.Before(d.CreatedAt) {
		return fmt.Errorf("decision end precedes creation")
	}
	if d.State == DecisionExpired && *d.EndReason == EndReasonExpired {
		if d.ExpiresAt == nil {
			return fmt.Errorf("expired decision requires an expiry")
		}
		if d.EndedAt.Before(*d.ExpiresAt) {
			return fmt.Errorf("decision ended before its expiry")
		}
		return nil
	}
	if d.State != DecisionRevoked {
		return fmt.Errorf("unsupported decision state %d", d.State)
	}
	switch *d.EndReason {
	case EndReasonManual, EndReasonSystemCleanup:
		return nil
	case EndReasonManualReplace:
		if d.Source != DecisionSourceManual {
			return fmt.Errorf("manual_replace is only valid for manual decisions")
		}
		return nil
	case EndReasonRuleDisabled:
		if d.Source != DecisionSourceAutomatic {
			return fmt.Errorf("rule_disabled is only valid for automatic decisions")
		}
		return nil
	default:
		return fmt.Errorf("invalid revoked decision end reason %q", *d.EndReason)
	}
}

// BanProjectionState expresses whether any active Decision requires a ban.
type BanProjectionState uint8

const (
	BanProjectionAbsent BanProjectionState = iota
	BanProjectionPresent
)

// DesiredBanProjection aggregates active decisions for one canonical target.
type DesiredBanProjection struct {
	NodeID          NodeID
	CanonicalTarget netip.Prefix
	State           BanProjectionState
	ActiveCount     uint64
	EffectiveUntil  *time.Time
	Revision        TargetProjectionRevision
}

// Validate checks the projection identity and Present/Absent invariants.
func (p DesiredBanProjection) Validate() error {
	if !isLowerHex128(string(p.NodeID)) {
		return fmt.Errorf("projection node id must be 128-bit lowercase hex")
	}
	if !p.CanonicalTarget.IsValid() || p.CanonicalTarget != p.CanonicalTarget.Masked() {
		return fmt.Errorf("projection target must be canonical")
	}
	if p.Revision == 0 {
		return fmt.Errorf("projection revision must be positive")
	}
	switch p.State {
	case BanProjectionAbsent:
		if p.ActiveCount != 0 || p.EffectiveUntil != nil {
			return fmt.Errorf("absent projection cannot contain active decisions or expiry")
		}
	case BanProjectionPresent:
		if p.ActiveCount == 0 {
			return fmt.Errorf("present projection requires an active decision")
		}
		if p.EffectiveUntil != nil && p.EffectiveUntil.IsZero() {
			return fmt.Errorf("projection effective expiry is invalid")
		}
	default:
		return fmt.Errorf("unsupported projection state %d", p.State)
	}
	return nil
}

type BanMembership uint8

const (
	BanAbsent BanMembership = iota
	BanPresent
)

type TimeoutMode uint8

const (
	TimeoutNone TimeoutMode = iota
	TimeoutNative
)

type PolicyCoverage uint8

const (
	PolicyCoverageNone PolicyCoverage = iota
	PolicyCoveragePartial
	PolicyCoverageFull
)

type AddressFamily uint8

const (
	AddressFamilyIPv4 AddressFamily = iota + 1
	AddressFamilyIPv6
)

// EnforcementScope is a controlled INPUT/FORWARD bit set.
type EnforcementScope uint8

const (
	ScopeInput EnforcementScope = 1 << iota
	ScopeForward
)

// NormalizedTargetEnforcementIntent contains only Firewall-significant attributes.
type NormalizedTargetEnforcementIntent struct {
	NodeID                  NodeID
	CanonicalTarget         netip.Prefix
	BanMembership           BanMembership
	EffectiveUntil          *time.Time
	TimeoutMode             TimeoutMode
	Scopes                  EnforcementScope
	AddressFamily           AddressFamily
	PolicyCoverage          PolicyCoverage
	PolicyRelationDigest    string
	BackendAttributesDigest string
	Generation              TargetEnforcementGeneration
}

// Validate checks the closed enums and Firewall-significant combinations.
func (i NormalizedTargetEnforcementIntent) Validate() error {
	if !isLowerHex128(string(i.NodeID)) {
		return fmt.Errorf("intent node id must be 128-bit lowercase hex")
	}
	if !i.CanonicalTarget.IsValid() || i.CanonicalTarget != i.CanonicalTarget.Masked() {
		return fmt.Errorf("intent target must be canonical")
	}
	if i.Generation == 0 {
		return fmt.Errorf("intent generation must be positive")
	}
	if i.BanMembership != BanAbsent && i.BanMembership != BanPresent {
		return fmt.Errorf("unsupported ban membership %d", i.BanMembership)
	}
	if i.TimeoutMode != TimeoutNone && i.TimeoutMode != TimeoutNative {
		return fmt.Errorf("unsupported timeout mode %d", i.TimeoutMode)
	}
	if i.PolicyCoverage != PolicyCoverageNone && i.PolicyCoverage != PolicyCoveragePartial && i.PolicyCoverage != PolicyCoverageFull {
		return fmt.Errorf("unsupported policy coverage %d", i.PolicyCoverage)
	}
	if i.Scopes == 0 || i.Scopes&^(ScopeInput|ScopeForward) != 0 {
		return fmt.Errorf("unsupported enforcement scope %d", i.Scopes)
	}
	wantFamily := AddressFamilyIPv6
	if i.CanonicalTarget.Addr().Is4() {
		wantFamily = AddressFamilyIPv4
	}
	if i.AddressFamily != wantFamily {
		return fmt.Errorf("intent address family does not match target")
	}
	if i.BanMembership == BanAbsent && (i.EffectiveUntil != nil || i.TimeoutMode != TimeoutNone) {
		return fmt.Errorf("absent intent cannot contain timeout attributes")
	}
	if i.TimeoutMode == TimeoutNative && i.EffectiveUntil == nil {
		return fmt.Errorf("native timeout requires an effective expiry")
	}
	if i.EffectiveUntil != nil && i.EffectiveUntil.IsZero() {
		return fmt.Errorf("intent effective expiry is invalid")
	}
	return nil
}

// ManagedInfrastructureIntent is the minimal immutable infrastructure payload for M0.
type ManagedInfrastructureIntent struct {
	Backend      string
	OwnerVersion string
	Digest       string
}

// ManagedPolicyIntent is the minimal immutable managed-policy payload for M0.
type ManagedPolicyIntent struct {
	RelationDigest string
}

// DesiredFirewallSnapshot is the complete desired input to planning.
type DesiredFirewallSnapshot struct {
	SnapshotRevision       SnapshotRevision
	InfrastructureRevision InfrastructureRevision
	PolicyRevision         PolicyRevision
	Infrastructure         ManagedInfrastructureIntent
	Policy                 ManagedPolicyIntent
	Targets                []NormalizedTargetEnforcementIntent
}

// NewDesiredFirewallSnapshot validates and clones a snapshot into stable target order.
func NewDesiredFirewallSnapshot(snapshot DesiredFirewallSnapshot) (DesiredFirewallSnapshot, error) {
	if err := snapshot.Validate(); err != nil {
		return DesiredFirewallSnapshot{}, err
	}
	prepared := snapshot
	prepared.Targets = make([]NormalizedTargetEnforcementIntent, len(snapshot.Targets))
	for index, intent := range snapshot.Targets {
		prepared.Targets[index] = intent
		if intent.EffectiveUntil != nil {
			expiry := *intent.EffectiveUntil
			prepared.Targets[index].EffectiveUntil = &expiry
		}
	}
	sort.Slice(prepared.Targets, func(left, right int) bool {
		return prepared.Targets[left].CanonicalTarget.String() < prepared.Targets[right].CanonicalTarget.String()
	})
	return prepared, nil
}

// Validate checks revisions, complete domain intents, and unique target intents.
func (s DesiredFirewallSnapshot) Validate() error {
	if s.SnapshotRevision == 0 || s.InfrastructureRevision == 0 || s.PolicyRevision == 0 {
		return fmt.Errorf("desired firewall revisions must be positive")
	}
	if s.Infrastructure.Backend == "" || s.Infrastructure.OwnerVersion == "" || s.Infrastructure.Digest == "" {
		return fmt.Errorf("desired infrastructure intent is incomplete")
	}
	if s.Policy.RelationDigest == "" {
		return fmt.Errorf("desired policy relation digest is required")
	}
	seen := make(map[netip.Prefix]struct{}, len(s.Targets))
	for _, intent := range s.Targets {
		if err := intent.Validate(); err != nil {
			return fmt.Errorf("validate desired target: %w", err)
		}
		if _, duplicate := seen[intent.CanonicalTarget]; duplicate {
			return fmt.Errorf("duplicate desired target %s", intent.CanonicalTarget)
		}
		seen[intent.CanonicalTarget] = struct{}{}
	}
	return nil
}

func deliveryIDForPosition(sourceID SourceID, position SourcePosition) (DeliveryID, error) {
	if file, ok := position.File(); ok {
		return FileDeliveryID(sourceID, file)
	}
	if journald, ok := position.Journald(); ok {
		return JournaldDeliveryID(sourceID, journald.Cursor)
	}
	return "", fmt.Errorf("unsupported source position")
}

// ObservedMembership includes Unknown for ambiguous or failed probes.
type ObservedMembership uint8

const (
	ObservedMembershipUnknown ObservedMembership = iota
	ObservedMembershipAbsent
	ObservedMembershipPresent
)

// ObservedPolicyCoverage includes Unknown for ambiguous or failed probes.
type ObservedPolicyCoverage uint8

const (
	ObservedPolicyUnknown ObservedPolicyCoverage = iota
	ObservedPolicyNone
	ObservedPolicyPartial
	ObservedPolicyFull
)

// PhysicalTargetObserved is returned by a Backend without application generation.
type PhysicalTargetObserved struct {
	CanonicalTarget      netip.Prefix
	ObservedAt           time.Time
	Backend              string
	BanMembership        ObservedMembership
	PolicyCoverage       ObservedPolicyCoverage
	PolicyRelationDigest string
	TimeoutMode          TimeoutMode
	NativeExpiry         *time.Time
	Scopes               EnforcementScope
	AddressFamily        AddressFamily
	OwnerVersion         string
	LastErrorCode        string
}

// ConfirmedTargetState is written only after authoritative comparison and fencing.
type ConfirmedTargetState struct {
	PhysicalTargetObserved
	ConfirmedGeneration TargetEnforcementGeneration
}

// ReconcileStatus is persisted separately for each failure domain.
type ReconcileStatus uint8

const (
	ReconcilePending ReconcileStatus = iota + 1
	ReconcileApplying
	ReconcileConverged
	ReconcileRetryWaiting
	ReconcileDegraded
)

type InfrastructureRetryKey struct {
	Revision InfrastructureRevision
	Epoch    RetryEpoch
}

type PolicyRetryKey struct {
	Revision PolicyRevision
	Epoch    RetryEpoch
}

type TargetRetryKey struct {
	Target     netip.Prefix
	Generation TargetEnforcementGeneration
	Epoch      RetryEpoch
}

// RetryState records one domain key's bounded mutation budget.
type RetryState struct {
	Status        ReconcileStatus
	AttemptCount  uint32
	LastAttemptAt *time.Time
	NextAttemptAt *time.Time
	LastErrorCode string
}
