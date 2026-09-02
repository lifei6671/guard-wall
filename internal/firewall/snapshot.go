package firewall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"sort"
)

const (
	// ManagedOwnerVersionV1 is the only ownership marker accepted by the v1
	// managed Firewall snapshot contract.
	ManagedOwnerVersionV1 = "guard/v1"
	// ManagedInfrastructureSchemaVersionV1 is the only managed infrastructure
	// layout currently understood by Guard.
	ManagedInfrastructureSchemaVersionV1 int64 = 1
	// MaxManagedSnapshotTargets bounds one managed snapshot response.
	MaxManagedSnapshotTargets = 1024
)

// ManagedScope identifies a packet-path scope observed for one managed target.
type ManagedScope string

const (
	// ManagedScopeInput identifies host INPUT enforcement.
	ManagedScopeInput ManagedScope = "input"
	// ManagedScopeForward identifies FORWARD enforcement.
	ManagedScopeForward ManagedScope = "forward"
)

// ManagedTimeoutMode identifies how one present target expires.
type ManagedTimeoutMode string

const (
	// ManagedTimeoutNone identifies a target without native expiry.
	ManagedTimeoutNone ManagedTimeoutMode = "none"
	// ManagedTimeoutNative identifies a target with backend-native expiry.
	ManagedTimeoutNative ManagedTimeoutMode = "native"
)

// InfrastructureObservationSpec is the untrusted constructor input for one
// present Guard-owned infrastructure observation.
type InfrastructureObservationSpec struct {
	Backend       BackendKind
	OwnerVersion  string
	SchemaVersion int64
	Digest        string
}

// InfrastructureObservation is an immutable, validated observation of
// present Guard-owned infrastructure. Its zero value is invalid.
type InfrastructureObservation struct {
	backend       BackendKind
	ownerVersion  string
	schemaVersion int64
	digest        string
	valid         bool
}

// NewInfrastructureObservation validates one infrastructure observation.
func NewInfrastructureObservation(spec InfrastructureObservationSpec) (InfrastructureObservation, error) {
	observation := InfrastructureObservation{
		backend:       spec.Backend,
		ownerVersion:  spec.OwnerVersion,
		schemaVersion: spec.SchemaVersion,
		digest:        spec.Digest,
		valid:         true,
	}
	if err := observation.Validate(); err != nil {
		return InfrastructureObservation{}, err
	}
	return observation, nil
}

// Validate rejects an incomplete or unsupported infrastructure observation.
func (o InfrastructureObservation) Validate() error {
	if !o.valid || !validBackendKind(o.backend) || o.ownerVersion != ManagedOwnerVersionV1 ||
		o.schemaVersion != ManagedInfrastructureSchemaVersionV1 || !validSnapshotDigest(o.digest) {
		return invalidManagedSnapshotError{}
	}
	return nil
}

// Backend returns the physical Firewall implementation that produced the observation.
func (o InfrastructureObservation) Backend() BackendKind { return o.backend }

// OwnerVersion returns the fixed Guard ownership marker.
func (o InfrastructureObservation) OwnerVersion() string { return o.ownerVersion }

// SchemaVersion returns the managed infrastructure schema version.
func (o InfrastructureObservation) SchemaVersion() int64 { return o.schemaVersion }

// Digest returns the canonical managed infrastructure digest.
func (o InfrastructureObservation) Digest() string { return o.digest }

// PolicyObservationSpec is the untrusted constructor input for one present
// managed policy observation.
type PolicyObservationSpec struct {
	RelationDigest string
}

// PolicyObservation is an immutable, validated managed policy observation.
// It exposes only a canonical relation digest, never raw policy entries.
type PolicyObservation struct {
	relationDigest string
	valid          bool
}

// NewPolicyObservation validates one managed policy observation.
func NewPolicyObservation(spec PolicyObservationSpec) (PolicyObservation, error) {
	observation := PolicyObservation{relationDigest: spec.RelationDigest, valid: true}
	if err := observation.Validate(); err != nil {
		return PolicyObservation{}, err
	}
	return observation, nil
}

// Validate rejects an incomplete managed policy observation.
func (o PolicyObservation) Validate() error {
	if !o.valid || !validSnapshotDigest(o.relationDigest) {
		return invalidManagedSnapshotError{}
	}
	return nil
}

// RelationDigest returns the canonical relation digest without revealing policy entries.
func (o PolicyObservation) RelationDigest() string { return o.relationDigest }

// TargetObservationSpec is the untrusted constructor input for one present
// managed target. Presence is expressed by inclusion in ManagedState.Targets.
type TargetObservationSpec struct {
	Target                  netip.Prefix
	TimeoutMode             ManagedTimeoutMode
	EffectiveUntilUnixMicro *int64
	Scopes                  []ManagedScope
}

// TargetObservation is an immutable, validated present managed target.
type TargetObservation struct {
	target                  netip.Prefix
	timeoutMode             ManagedTimeoutMode
	effectiveUntilUnixMicro int64
	hasEffectiveUntil       bool
	scopes                  [2]ManagedScope
	scopeCount              int
	valid                   bool
}

// NewTargetObservation validates and owns one present managed target observation.
func NewTargetObservation(spec TargetObservationSpec) (TargetObservation, error) {
	observation := TargetObservation{
		target:      spec.Target,
		timeoutMode: spec.TimeoutMode,
		valid:       true,
	}
	if spec.EffectiveUntilUnixMicro != nil {
		observation.effectiveUntilUnixMicro = *spec.EffectiveUntilUnixMicro
		observation.hasEffectiveUntil = true
	}
	scopes, ok := canonicalManagedScopes(spec.Scopes)
	if !ok {
		return TargetObservation{}, invalidManagedSnapshotError{}
	}
	copy(observation.scopes[:], scopes)
	observation.scopeCount = len(scopes)
	if err := observation.Validate(); err != nil {
		return TargetObservation{}, err
	}
	return observation, nil
}

// Validate rejects an incomplete or contradictory target observation.
func (o TargetObservation) Validate() error {
	if !o.valid || !validManagedTarget(o.target) || o.scopeCount < 1 || o.scopeCount > len(o.scopes) {
		return invalidManagedSnapshotError{}
	}
	if _, ok := canonicalManagedScopes(o.scopes[:o.scopeCount]); !ok {
		return invalidManagedSnapshotError{}
	}
	switch o.timeoutMode {
	case ManagedTimeoutNone:
		if o.hasEffectiveUntil {
			return invalidManagedSnapshotError{}
		}
	case ManagedTimeoutNative:
		if !o.hasEffectiveUntil || o.effectiveUntilUnixMicro <= 0 {
			return invalidManagedSnapshotError{}
		}
	default:
		return invalidManagedSnapshotError{}
	}
	return nil
}

// Target returns the canonical, non-loopback target prefix.
func (o TargetObservation) Target() netip.Prefix { return o.target }

// TimeoutMode returns the target's expiry mechanism.
func (o TargetObservation) TimeoutMode() ManagedTimeoutMode { return o.timeoutMode }

// EffectiveUntilUnixMicro returns the positive Unix-microsecond expiry when present.
func (o TargetObservation) EffectiveUntilUnixMicro() (int64, bool) {
	return o.effectiveUntilUnixMicro, o.hasEffectiveUntil
}

// Scopes returns a detached, canonical scope list.
func (o TargetObservation) Scopes() []ManagedScope {
	return append([]ManagedScope(nil), o.scopes[:o.scopeCount]...)
}

// ManagedStateSpec is the untrusted constructor input for a partial managed
// state. Infrastructure and Policy are independently optional so drift and
// partial installation remain observable.
type ManagedStateSpec struct {
	Infrastructure *InfrastructureObservation
	Policy         *PolicyObservation
	Targets        []TargetObservation
}

// ManagedState is an immutable, canonical view of Guard-owned state.
// Its zero value is invalid.
type ManagedState struct {
	infrastructure    InfrastructureObservation
	hasInfrastructure bool
	policy            PolicyObservation
	hasPolicy         bool
	targets           []TargetObservation
	valid             bool
}

// NewManagedState validates, copies, and canonicalizes one partial managed state.
func NewManagedState(spec ManagedStateSpec) (ManagedState, error) {
	state := ManagedState{valid: true}
	if spec.Infrastructure != nil {
		state.infrastructure = *spec.Infrastructure
		state.hasInfrastructure = true
	}
	if spec.Policy != nil {
		state.policy = *spec.Policy
		state.hasPolicy = true
	}
	state.targets = append([]TargetObservation(nil), spec.Targets...)
	sort.Slice(state.targets, func(i, j int) bool {
		return state.targets[i].target.String() < state.targets[j].target.String()
	})
	if err := state.Validate(); err != nil {
		return ManagedState{}, err
	}
	return state, nil
}

// Validate rejects invalid observations, duplicate targets, and oversized states.
func (s ManagedState) Validate() error {
	if !s.valid || len(s.targets) > MaxManagedSnapshotTargets {
		return invalidManagedSnapshotError{}
	}
	if s.hasInfrastructure {
		if err := s.infrastructure.Validate(); err != nil {
			return invalidManagedSnapshotError{}
		}
	}
	if s.hasPolicy {
		if err := s.policy.Validate(); err != nil {
			return invalidManagedSnapshotError{}
		}
	}
	for index, target := range s.targets {
		if err := target.Validate(); err != nil {
			return invalidManagedSnapshotError{}
		}
		if index > 0 && s.targets[index-1].target == target.target {
			return invalidManagedSnapshotError{}
		}
		if index > 0 && s.targets[index-1].target.String() > target.target.String() {
			return invalidManagedSnapshotError{}
		}
	}
	return nil
}

// Infrastructure returns the present infrastructure observation, if any.
func (s ManagedState) Infrastructure() (InfrastructureObservation, bool) {
	return s.infrastructure, s.hasInfrastructure
}

// Policy returns the present managed policy observation, if any.
func (s ManagedState) Policy() (PolicyObservation, bool) { return s.policy, s.hasPolicy }

// Targets returns a detached, canonically sorted target list.
func (s ManagedState) Targets() []TargetObservation {
	return append([]TargetObservation(nil), s.targets...)
}

// ForeignContextSpec is the untrusted constructor input for packet-path facts
// that the Firewall backend may inspect but must never mutate.
type ForeignContextSpec struct {
	Digest string
}

// ForeignContext is an immutable, digest-only view of foreign packet-path facts.
type ForeignContext struct {
	digest string
	valid  bool
}

// NewForeignContext validates one read-only foreign context.
func NewForeignContext(spec ForeignContextSpec) (ForeignContext, error) {
	context := ForeignContext{digest: spec.Digest, valid: true}
	if err := context.Validate(); err != nil {
		return ForeignContext{}, err
	}
	return context, nil
}

// Validate rejects an incomplete foreign context.
func (c ForeignContext) Validate() error {
	if !c.valid || !validSnapshotDigest(c.digest) {
		return invalidManagedSnapshotError{}
	}
	return nil
}

// Digest returns the canonical foreign-context digest.
func (c ForeignContext) Digest() string { return c.digest }

// ManagedSnapshotSpec is the validated-constructor input for one complete
// managed-state and read-only foreign-context observation.
type ManagedSnapshotSpec struct {
	ManagedState   ManagedState
	ForeignContext ForeignContext
}

// ManagedSnapshot is an immutable, platform-neutral Firewall Snapshot result.
// Its digest covers all managed semantics plus the foreign-context digest.
type ManagedSnapshot struct {
	managedState   ManagedState
	foreignContext ForeignContext
	digest         string
	valid          bool
}

// NewManagedSnapshot validates and digests one canonical snapshot.
func NewManagedSnapshot(spec ManagedSnapshotSpec) (ManagedSnapshot, error) {
	snapshot := ManagedSnapshot{
		managedState:   spec.ManagedState,
		foreignContext: spec.ForeignContext,
		valid:          true,
	}
	if err := snapshot.managedState.Validate(); err != nil {
		return ManagedSnapshot{}, invalidManagedSnapshotError{}
	}
	if err := snapshot.foreignContext.Validate(); err != nil {
		return ManagedSnapshot{}, invalidManagedSnapshotError{}
	}
	digest, err := computeManagedSnapshotDigest(snapshot.managedState, snapshot.foreignContext)
	if err != nil {
		return ManagedSnapshot{}, invalidManagedSnapshotError{}
	}
	snapshot.digest = digest
	return snapshot, nil
}

// Validate rejects incomplete or internally inconsistent snapshots.
func (s ManagedSnapshot) Validate() error {
	if !s.valid || !validSnapshotDigest(s.digest) || s.managedState.Validate() != nil || s.foreignContext.Validate() != nil {
		return invalidManagedSnapshotError{}
	}
	digest, err := computeManagedSnapshotDigest(s.managedState, s.foreignContext)
	if err != nil || digest != s.digest {
		return invalidManagedSnapshotError{}
	}
	return nil
}

// ManagedState returns the immutable managed-state value.
func (s ManagedSnapshot) ManagedState() ManagedState { return s.managedState }

// ForeignContext returns the immutable read-only foreign-context value.
func (s ManagedSnapshot) ForeignContext() ForeignContext { return s.foreignContext }

// Digest returns the versioned canonical SHA-256 snapshot digest.
func (s ManagedSnapshot) Digest() string { return s.digest }

// OwnershipConflictError reports that same-name infrastructure could not be
// proven to carry the fixed Guard owner/version marker. It intentionally holds
// no object identity or underlying error.
type OwnershipConflictError struct{}

// Error returns a stable, non-sensitive conflict classification.
func (OwnershipConflictError) Error() string { return "firewall ownership conflict" }

// NewOwnershipConflictError constructs the typed, redacted conflict boundary.
func NewOwnershipConflictError() error { return OwnershipConflictError{} }

type snapshotDigestWire struct {
	Format         string                    `json:"format"`
	Infrastructure *infrastructureDigestWire `json:"infrastructure"`
	Policy         *policyDigestWire         `json:"policy"`
	Targets        []targetDigestWire        `json:"targets"`
	ForeignDigest  string                    `json:"foreign_digest"`
}

type infrastructureDigestWire struct {
	Backend       BackendKind `json:"backend"`
	OwnerVersion  string      `json:"owner_version"`
	SchemaVersion int64       `json:"schema_version"`
	Digest        string      `json:"digest"`
}

type policyDigestWire struct {
	RelationDigest string `json:"relation_digest"`
}

type targetDigestWire struct {
	Target                  string             `json:"target"`
	TimeoutMode             ManagedTimeoutMode `json:"timeout_mode"`
	EffectiveUntilUnixMicro *int64             `json:"effective_until_unix_us"`
	Scopes                  []ManagedScope     `json:"scopes"`
}

func computeManagedSnapshotDigest(state ManagedState, foreign ForeignContext) (string, error) {
	wire := snapshotDigestWire{
		Format:        "guard-managed-snapshot/v1",
		Targets:       make([]targetDigestWire, 0, len(state.targets)),
		ForeignDigest: foreign.digest,
	}
	if state.hasInfrastructure {
		wire.Infrastructure = &infrastructureDigestWire{
			Backend:       state.infrastructure.backend,
			OwnerVersion:  state.infrastructure.ownerVersion,
			SchemaVersion: state.infrastructure.schemaVersion,
			Digest:        state.infrastructure.digest,
		}
	}
	if state.hasPolicy {
		wire.Policy = &policyDigestWire{RelationDigest: state.policy.relationDigest}
	}
	for _, target := range state.targets {
		var expiry *int64
		if target.hasEffectiveUntil {
			value := target.effectiveUntilUnixMicro
			expiry = &value
		}
		wire.Targets = append(wire.Targets, targetDigestWire{
			Target:                  target.target.String(),
			TimeoutMode:             target.timeoutMode,
			EffectiveUntilUnixMicro: expiry,
			Scopes:                  target.Scopes(),
		})
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalManagedScopes(scopes []ManagedScope) ([]ManagedScope, bool) {
	input := false
	forward := false
	for _, scope := range scopes {
		switch scope {
		case ManagedScopeInput:
			if input {
				return nil, false
			}
			input = true
		case ManagedScopeForward:
			if forward {
				return nil, false
			}
			forward = true
		default:
			return nil, false
		}
	}
	canonical := make([]ManagedScope, 0, 2)
	if input {
		canonical = append(canonical, ManagedScopeInput)
	}
	if forward {
		canonical = append(canonical, ManagedScopeForward)
	}
	return canonical, len(canonical) > 0
}

func validManagedTarget(target netip.Prefix) bool {
	if !target.IsValid() || target != target.Masked() {
		return false
	}
	for _, protected := range []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	} {
		if target.Overlaps(protected) {
			return false
		}
	}
	return true
}

func validSnapshotDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	for _, character := range digest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

type invalidManagedSnapshotError struct{}

func (invalidManagedSnapshotError) Error() string { return "managed firewall snapshot is invalid" }
