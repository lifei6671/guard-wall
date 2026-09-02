package firewall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"reflect"
)

// MutationOperation identifies the closed set of authorized Firewall mutations.
type MutationOperation string

const (
	MutationOperationApply  MutationOperation = "apply"
	MutationOperationRemove MutationOperation = "remove"
)

// MutationDomain identifies one independently applied managed-state domain.
type MutationDomain string

const (
	MutationDomainInfrastructure MutationDomain = "infrastructure"
	MutationDomainPolicy         MutationDomain = "policy"
	MutationDomainTarget         MutationDomain = "target"
)

// TargetMembership identifies the desired state of one managed target.
type TargetMembership string

const (
	TargetMembershipPresent TargetMembership = "present"
	TargetMembershipAbsent  TargetMembership = "absent"
)

// AuthorizedMutation is the closed set of immutable mutation authorities.
type AuthorizedMutation interface {
	Operation() MutationOperation
	Backend() BackendKind
	Capabilities() FirewallCapabilities
	OwnerVersion() string
	BasisSnapshotDigest() string
	Digest() string
	isAuthorizedMutation()
}

// OperationPlan is the closed set of single-domain Apply authorities.
type OperationPlan interface {
	AuthorizedMutation
	Domain() MutationDomain
	Revision() int64
	isOperationPlan()
}

// InfrastructureOperationPlan authorizes fixed managed infrastructure.
type InfrastructureOperationPlan interface {
	OperationPlan
	SchemaVersion() int64
	isInfrastructureOperationPlan()
}

// PolicyOperationPlan authorizes one complete managed policy replacement.
type PolicyOperationPlan interface {
	OperationPlan
	Allowlist() []netip.Prefix
	ProtectedTargets() []netip.Prefix
	isPolicyOperationPlan()
}

// TargetOperationPlan authorizes one managed target membership mutation.
type TargetOperationPlan interface {
	OperationPlan
	Target() netip.Prefix
	Membership() TargetMembership
	TimeoutMode() ManagedTimeoutMode
	EffectiveUntilUnixMicro() (int64, bool)
	Scopes() []ManagedScope
	isTargetOperationPlan()
}

// RemovalAuthorization authorizes removal only of matching Guard-owned infrastructure.
type RemovalAuthorization interface {
	AuthorizedMutation
	ExpectedOwnerVersion() string
	isRemovalAuthorization()
}

type operationPlanBase struct {
	capabilities        FirewallCapabilities
	ownerVersion        string
	basisSnapshotDigest string
	revision            int64
	digest              string
}

func (*operationPlanBase) Operation() MutationOperation { return MutationOperationApply }
func (p *operationPlanBase) Backend() BackendKind {
	if p == nil {
		return ""
	}
	return p.capabilities.Backend()
}
func (p *operationPlanBase) Capabilities() FirewallCapabilities {
	if p == nil {
		return FirewallCapabilities{}
	}
	return p.capabilities
}
func (p *operationPlanBase) OwnerVersion() string {
	if p == nil {
		return ""
	}
	return p.ownerVersion
}
func (p *operationPlanBase) BasisSnapshotDigest() string {
	if p == nil {
		return ""
	}
	return p.basisSnapshotDigest
}
func (p *operationPlanBase) Revision() int64 {
	if p == nil {
		return 0
	}
	return p.revision
}
func (p *operationPlanBase) Digest() string {
	if p == nil {
		return ""
	}
	return p.digest
}
func (*operationPlanBase) isAuthorizedMutation() {}
func (*operationPlanBase) isOperationPlan()      {}

type infrastructureOperationPlan struct {
	operationPlanBase
	schemaVersion int64
}

func (*infrastructureOperationPlan) Domain() MutationDomain {
	return MutationDomainInfrastructure
}
func (p *infrastructureOperationPlan) SchemaVersion() int64 {
	if p == nil {
		return 0
	}
	return p.schemaVersion
}
func (*infrastructureOperationPlan) isInfrastructureOperationPlan() {}

type policyOperationPlan struct {
	operationPlanBase
	allowlist        []netip.Prefix
	protectedTargets []netip.Prefix
}

func (*policyOperationPlan) Domain() MutationDomain { return MutationDomainPolicy }
func (p *policyOperationPlan) Allowlist() []netip.Prefix {
	if p == nil {
		return nil
	}
	return append([]netip.Prefix(nil), p.allowlist...)
}
func (p *policyOperationPlan) ProtectedTargets() []netip.Prefix {
	if p == nil {
		return nil
	}
	return append([]netip.Prefix(nil), p.protectedTargets...)
}
func (*policyOperationPlan) isPolicyOperationPlan() {}

type targetOperationPlan struct {
	operationPlanBase
	target                  netip.Prefix
	membership              TargetMembership
	timeoutMode             ManagedTimeoutMode
	effectiveUntilUnixMicro int64
	hasEffectiveUntil       bool
	scopes                  []ManagedScope
}

func (*targetOperationPlan) Domain() MutationDomain { return MutationDomainTarget }
func (p *targetOperationPlan) Target() netip.Prefix {
	if p == nil {
		return netip.Prefix{}
	}
	return p.target
}
func (p *targetOperationPlan) Membership() TargetMembership {
	if p == nil {
		return ""
	}
	return p.membership
}
func (p *targetOperationPlan) TimeoutMode() ManagedTimeoutMode {
	if p == nil {
		return ""
	}
	return p.timeoutMode
}
func (p *targetOperationPlan) EffectiveUntilUnixMicro() (int64, bool) {
	if p == nil {
		return 0, false
	}
	return p.effectiveUntilUnixMicro, p.hasEffectiveUntil
}
func (p *targetOperationPlan) Scopes() []ManagedScope {
	if p == nil {
		return nil
	}
	return append([]ManagedScope(nil), p.scopes...)
}
func (*targetOperationPlan) isTargetOperationPlan() {}

type removalAuthorization struct {
	capabilities        FirewallCapabilities
	ownerVersion        string
	basisSnapshotDigest string
	digest              string
}

func (*removalAuthorization) Operation() MutationOperation { return MutationOperationRemove }
func (a *removalAuthorization) Backend() BackendKind {
	if a == nil {
		return ""
	}
	return a.capabilities.Backend()
}
func (a *removalAuthorization) Capabilities() FirewallCapabilities {
	if a == nil {
		return FirewallCapabilities{}
	}
	return a.capabilities
}
func (a *removalAuthorization) OwnerVersion() string {
	if a == nil {
		return ""
	}
	return a.ownerVersion
}
func (a *removalAuthorization) ExpectedOwnerVersion() string { return a.OwnerVersion() }
func (a *removalAuthorization) BasisSnapshotDigest() string {
	if a == nil {
		return ""
	}
	return a.basisSnapshotDigest
}
func (a *removalAuthorization) Digest() string {
	if a == nil {
		return ""
	}
	return a.digest
}
func (*removalAuthorization) isAuthorizedMutation()   {}
func (*removalAuthorization) isRemovalAuthorization() {}

// AuthorizeInfrastructureMutation validates current authority and constructs a
// fixed-infrastructure Apply plan.
func AuthorizeInfrastructureMutation(
	capabilities FirewallCapabilities,
	snapshot ManagedSnapshot,
	requestedBasisDigest string,
	revision int64,
) (InfrastructureOperationPlan, error) {
	if !validSnapshotDigest(requestedBasisDigest) || revision <= 0 {
		return nil, mutationAuthorizationError(MutationAuthorizationErrorInvalidPlan)
	}
	if err := validateMutationAuthority(capabilities, snapshot, requestedBasisDigest); err != nil {
		return nil, err
	}
	base := newOperationPlanBase(capabilities, requestedBasisDigest, revision)
	plan := &infrastructureOperationPlan{
		operationPlanBase: base,
		schemaVersion:     ManagedInfrastructureSchemaVersionV1,
	}
	plan.digest = digestMutation(infrastructurePlanDigestWire{
		Format: "guard-firewall-mutation/v1", Operation: MutationOperationApply,
		Domain: plan.Domain(), Backend: plan.Backend(), Capabilities: capabilitiesDigest(capabilities),
		OwnerVersion:        plan.ownerVersion,
		BasisSnapshotDigest: plan.basisSnapshotDigest, Revision: plan.revision,
		SchemaVersion: plan.schemaVersion,
	})
	return plan, nil
}

// AuthorizePolicyMutation validates current authority and constructs one
// complete policy replacement plan.
func AuthorizePolicyMutation(
	capabilities FirewallCapabilities,
	snapshot ManagedSnapshot,
	requestedBasisDigest string,
	revision int64,
	allowlist []netip.Prefix,
	protectedTargets []netip.Prefix,
) (PolicyOperationPlan, error) {
	if !validSnapshotDigest(requestedBasisDigest) || revision <= 0 ||
		len(allowlist)+len(protectedTargets) > MaxManagedSnapshotTargets ||
		len(protectedTargets) < 2 || !validCanonicalPrefixList(allowlist) ||
		!validCanonicalPrefixList(protectedTargets) || !hasMandatoryProtectedTargets(protectedTargets) {
		return nil, mutationAuthorizationError(MutationAuthorizationErrorInvalidPlan)
	}
	if err := validateMutationAuthority(capabilities, snapshot, requestedBasisDigest); err != nil {
		return nil, err
	}
	if _, present := snapshot.ManagedState().Infrastructure(); !present {
		return nil, mutationAuthorizationError(MutationAuthorizationErrorNotReady)
	}
	if !supportsPrefixes(capabilities, allowlist) || !supportsPrefixes(capabilities, protectedTargets) {
		return nil, mutationAuthorizationError(MutationAuthorizationErrorUnsupported)
	}
	base := newOperationPlanBase(capabilities, requestedBasisDigest, revision)
	plan := &policyOperationPlan{
		operationPlanBase: base,
		allowlist:         append([]netip.Prefix(nil), allowlist...),
		protectedTargets:  append([]netip.Prefix(nil), protectedTargets...),
	}
	plan.digest = digestMutation(policyPlanDigestWire{
		Format: "guard-firewall-mutation/v1", Operation: MutationOperationApply,
		Domain: plan.Domain(), Backend: plan.Backend(), Capabilities: capabilitiesDigest(capabilities),
		OwnerVersion:        plan.ownerVersion,
		BasisSnapshotDigest: plan.basisSnapshotDigest, Revision: plan.revision,
		Allowlist: prefixStrings(plan.allowlist), ProtectedTargets: prefixStrings(plan.protectedTargets),
	})
	return plan, nil
}

// AuthorizeTargetMutation validates current authority and constructs one target plan.
func AuthorizeTargetMutation(
	capabilities FirewallCapabilities,
	snapshot ManagedSnapshot,
	requestedBasisDigest string,
	revision int64,
	target netip.Prefix,
	membership TargetMembership,
	timeoutMode ManagedTimeoutMode,
	effectiveUntilUnixMicro int64,
	hasEffectiveUntil bool,
	scopeInput []ManagedScope,
) (TargetOperationPlan, error) {
	if !validSnapshotDigest(requestedBasisDigest) || revision <= 0 || !validManagedTarget(target) ||
		membership != TargetMembershipPresent && membership != TargetMembershipAbsent {
		return nil, mutationAuthorizationError(MutationAuthorizationErrorInvalidPlan)
	}
	scopes, ok := canonicalManagedScopes(scopeInput)
	if !ok || !equalManagedScopes(scopes, scopeInput) ||
		!validTargetTimeout(membership, timeoutMode, effectiveUntilUnixMicro, hasEffectiveUntil) {
		return nil, mutationAuthorizationError(MutationAuthorizationErrorInvalidPlan)
	}
	if err := validateMutationAuthority(capabilities, snapshot, requestedBasisDigest); err != nil {
		return nil, err
	}
	state := snapshot.ManagedState()
	if _, present := state.Infrastructure(); !present {
		return nil, mutationAuthorizationError(MutationAuthorizationErrorNotReady)
	}
	if membership == TargetMembershipPresent {
		if _, present := state.Policy(); !present {
			return nil, mutationAuthorizationError(MutationAuthorizationErrorNotReady)
		}
	}
	if !supportsPrefix(capabilities, target) || !supportsScopes(capabilities, scopes) ||
		timeoutMode == ManagedTimeoutNative &&
			(!capabilities.SupportsNativeTimeout() || !capabilities.SupportsCrashSafeExpiry()) {
		return nil, mutationAuthorizationError(MutationAuthorizationErrorUnsupported)
	}
	base := newOperationPlanBase(capabilities, requestedBasisDigest, revision)
	var expiry *int64
	if hasEffectiveUntil {
		expiry = &effectiveUntilUnixMicro
	}
	plan := &targetOperationPlan{
		operationPlanBase:       base,
		target:                  target,
		membership:              membership,
		timeoutMode:             timeoutMode,
		effectiveUntilUnixMicro: effectiveUntilUnixMicro,
		hasEffectiveUntil:       hasEffectiveUntil,
		scopes:                  append([]ManagedScope(nil), scopes...),
	}
	plan.digest = digestMutation(targetPlanDigestWire{
		Format: "guard-firewall-mutation/v1", Operation: MutationOperationApply,
		Domain: plan.Domain(), Backend: plan.Backend(), Capabilities: capabilitiesDigest(capabilities),
		OwnerVersion:        plan.ownerVersion,
		BasisSnapshotDigest: plan.basisSnapshotDigest, Revision: plan.revision,
		Target: plan.target.String(), Membership: plan.membership,
		TimeoutMode: plan.timeoutMode, EffectiveUntilUnixMicro: expiry,
		Scopes: plan.Scopes(),
	})
	return plan, nil
}

// AuthorizeManagedRemoval validates ownership against the current snapshot. If
// managed infrastructure is already absent, alreadyAbsent is true and no
// backend mutation is authorized.
func AuthorizeManagedRemoval(
	capabilities FirewallCapabilities,
	snapshot ManagedSnapshot,
	expectedOwnerVersion string,
) (authorization RemovalAuthorization, alreadyAbsent bool, err error) {
	if expectedOwnerVersion != ManagedOwnerVersionV1 {
		return nil, false, mutationAuthorizationError(MutationAuthorizationErrorOwnershipConflict)
	}
	if err := validateMutationAuthority(capabilities, snapshot, ""); err != nil {
		return nil, false, err
	}
	state := snapshot.ManagedState()
	if _, infrastructurePresent := state.Infrastructure(); !infrastructurePresent {
		_, policyPresent := state.Policy()
		if !policyPresent && len(state.Targets()) == 0 {
			return nil, true, nil
		}
	}
	removal := &removalAuthorization{
		capabilities: capabilities, ownerVersion: expectedOwnerVersion,
		basisSnapshotDigest: snapshot.Digest(),
	}
	removal.digest = digestMutation(removalDigestWire{
		Format: "guard-firewall-mutation/v1", Operation: MutationOperationRemove,
		Backend: capabilities.Backend(), Capabilities: capabilitiesDigest(capabilities),
		OwnerVersion: expectedOwnerVersion, BasisSnapshotDigest: snapshot.Digest(),
	})
	return removal, false, nil
}

// MutationAuthorizationErrorCode is a stable local authorization classification.
type MutationAuthorizationErrorCode string

const (
	MutationAuthorizationErrorInvalidPlan       MutationAuthorizationErrorCode = "invalid_plan"
	MutationAuthorizationErrorOwnershipConflict MutationAuthorizationErrorCode = "ownership_conflict"
	MutationAuthorizationErrorUnsupported       MutationAuthorizationErrorCode = "unsupported"
	MutationAuthorizationErrorNotReady          MutationAuthorizationErrorCode = "not_ready"
	MutationAuthorizationErrorSnapshotMismatch  MutationAuthorizationErrorCode = "snapshot_mismatch"
)

// MutationAuthorizationError contains no request values or backend errors.
type MutationAuthorizationError struct {
	code MutationAuthorizationErrorCode
}

func (e *MutationAuthorizationError) Error() string {
	if e == nil {
		return "firewall mutation authorization failed"
	}
	return "firewall mutation authorization failed: " + string(e.code)
}

// Code returns the stable local authorization classification.
func (e *MutationAuthorizationError) Code() MutationAuthorizationErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

func mutationAuthorizationError(code MutationAuthorizationErrorCode) error {
	return &MutationAuthorizationError{code: code}
}

// MutationStatus is the closed post-mutation certainty classification.
type MutationStatus string

const (
	MutationStatusConfirmed MutationStatus = "confirmed"
	MutationStatusRejected  MutationStatus = "rejected"
	MutationStatusUnknown   MutationStatus = "unknown"
)

// MutationErrorCode is the closed, redacted reason for a rejected or unknown result.
type MutationErrorCode string

const (
	MutationErrorInvalidPlan       MutationErrorCode = "invalid_plan"
	MutationErrorOwnershipConflict MutationErrorCode = "ownership_conflict"
	MutationErrorUnsupported       MutationErrorCode = "unsupported"
	MutationErrorNotReady          MutationErrorCode = "not_ready"
	MutationErrorBackendRejected   MutationErrorCode = "backend_rejected"
	MutationErrorUnknownResult     MutationErrorCode = "unknown_result"
)

// MutationResult is an immutable result correlated to one authorized mutation.
type MutationResult struct {
	operation      MutationOperation
	domain         MutationDomain
	hasDomain      bool
	mutationDigest string
	status         MutationStatus
	errorCode      MutationErrorCode
	hasErrorCode   bool
	valid          bool
}

// NewConfirmedMutationResult constructs a result with proven complete post-state.
func NewConfirmedMutationResult(mutation AuthorizedMutation) (MutationResult, error) {
	return newMutationResult(mutation, MutationStatusConfirmed, "", false)
}

// NewRejectedMutationResult constructs a proven zero-side-effect or fully rolled-back result.
func NewRejectedMutationResult(mutation AuthorizedMutation, code MutationErrorCode) (MutationResult, error) {
	return newMutationResult(mutation, MutationStatusRejected, code, true)
}

// NewUnknownMutationResult constructs a result whose post-state is not proven.
func NewUnknownMutationResult(mutation AuthorizedMutation) (MutationResult, error) {
	return newMutationResult(mutation, MutationStatusUnknown, MutationErrorUnknownResult, true)
}

func newMutationResult(
	mutation AuthorizedMutation,
	status MutationStatus,
	code MutationErrorCode,
	hasCode bool,
) (MutationResult, error) {
	operation, domain, hasDomain, digest, ok := mutationIdentity(mutation)
	if !ok || !validMutationResultState(operation, status, code, hasCode) {
		return MutationResult{}, invalidMutationResultError{}
	}
	return MutationResult{
		operation: operation, domain: domain, hasDomain: hasDomain,
		mutationDigest: digest, status: status, errorCode: code,
		hasErrorCode: hasCode, valid: true,
	}, nil
}

// Validate rejects incomplete or contradictory result values.
func (r MutationResult) Validate() error {
	if !r.valid || !validSnapshotDigest(r.mutationDigest) ||
		!validMutationResultState(r.operation, r.status, r.errorCode, r.hasErrorCode) ||
		(r.operation == MutationOperationApply) != r.hasDomain ||
		(r.hasDomain && !validMutationDomain(r.domain)) {
		return invalidMutationResultError{}
	}
	return nil
}

func (r MutationResult) Operation() MutationOperation   { return r.operation }
func (r MutationResult) Domain() (MutationDomain, bool) { return r.domain, r.hasDomain }
func (r MutationResult) MutationDigest() string         { return r.mutationDigest }
func (r MutationResult) Status() MutationStatus         { return r.status }
func (r MutationResult) ErrorCode() (MutationErrorCode, bool) {
	return r.errorCode, r.hasErrorCode
}

func newOperationPlanBase(
	capabilities FirewallCapabilities,
	basisSnapshotDigest string,
	revision int64,
) operationPlanBase {
	return operationPlanBase{
		capabilities: capabilities, ownerVersion: ManagedOwnerVersionV1,
		basisSnapshotDigest: basisSnapshotDigest, revision: revision,
	}
}

func validateMutationAuthority(
	capabilities FirewallCapabilities,
	snapshot ManagedSnapshot,
	requestedBasisDigest string,
) error {
	if capabilities.Validate() != nil || snapshot.Validate() != nil ||
		!capabilities.MutationReady() || !capabilities.OwnershipProven() {
		return mutationAuthorizationError(MutationAuthorizationErrorNotReady)
	}
	if infrastructure, present := snapshot.ManagedState().Infrastructure(); present &&
		infrastructure.Backend() != capabilities.Backend() {
		return mutationAuthorizationError(MutationAuthorizationErrorNotReady)
	}
	if requestedBasisDigest != "" && requestedBasisDigest != snapshot.Digest() {
		return mutationAuthorizationError(MutationAuthorizationErrorSnapshotMismatch)
	}
	return nil
}

func supportsPrefixes(capabilities FirewallCapabilities, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if !supportsPrefix(capabilities, prefix) {
			return false
		}
	}
	return true
}

func supportsPrefix(capabilities FirewallCapabilities, prefix netip.Prefix) bool {
	if prefix.Addr().Is4() && !capabilities.SupportsIPv4() ||
		prefix.Addr().Is6() && !capabilities.SupportsIPv6() {
		return false
	}
	return prefix.Bits() == prefix.Addr().BitLen() || capabilities.SupportsCIDR()
}

func supportsScopes(capabilities FirewallCapabilities, scopes []ManagedScope) bool {
	for _, scope := range scopes {
		if scope == ManagedScopeInput && !capabilities.SupportsHostInput() ||
			scope == ManagedScopeForward && !capabilities.SupportsForward() {
			return false
		}
	}
	return true
}

func mutationIdentity(mutation AuthorizedMutation) (MutationOperation, MutationDomain, bool, string, bool) {
	switch value := mutation.(type) {
	case InfrastructureOperationPlan:
		if nilInterface(value) || value.Domain() != MutationDomainInfrastructure {
			return "", "", false, "", false
		}
		return MutationOperationApply, value.Domain(), true, value.Digest(), validOperationPlan(value)
	case PolicyOperationPlan:
		if nilInterface(value) || value.Domain() != MutationDomainPolicy {
			return "", "", false, "", false
		}
		return MutationOperationApply, value.Domain(), true, value.Digest(), validOperationPlan(value)
	case TargetOperationPlan:
		if nilInterface(value) || value.Domain() != MutationDomainTarget {
			return "", "", false, "", false
		}
		return MutationOperationApply, value.Domain(), true, value.Digest(), validOperationPlan(value)
	case RemovalAuthorization:
		if nilInterface(value) || value.Operation() != MutationOperationRemove ||
			value.ExpectedOwnerVersion() != ManagedOwnerVersionV1 || !validSnapshotDigest(value.BasisSnapshotDigest()) ||
			value.Capabilities().Validate() != nil || value.Backend() != value.Capabilities().Backend() ||
			!validSnapshotDigest(value.Digest()) {
			return "", "", false, "", false
		}
		return MutationOperationRemove, "", false, value.Digest(), true
	default:
		return "", "", false, "", false
	}
}

func validOperationPlan(plan OperationPlan) bool {
	return plan.Operation() == MutationOperationApply && plan.OwnerVersion() == ManagedOwnerVersionV1 &&
		plan.Capabilities().Validate() == nil && plan.Backend() == plan.Capabilities().Backend() &&
		validSnapshotDigest(plan.BasisSnapshotDigest()) && plan.Revision() > 0 &&
		validMutationDomain(plan.Domain()) && validSnapshotDigest(plan.Digest())
}

func validMutationResultState(operation MutationOperation, status MutationStatus, code MutationErrorCode, hasCode bool) bool {
	if operation != MutationOperationApply && operation != MutationOperationRemove {
		return false
	}
	switch status {
	case MutationStatusConfirmed:
		return !hasCode && code == ""
	case MutationStatusUnknown:
		return hasCode && code == MutationErrorUnknownResult
	case MutationStatusRejected:
		if !hasCode || code == MutationErrorUnknownResult || !validRejectedMutationCode(code) {
			return false
		}
		return operation == MutationOperationApply || code != MutationErrorInvalidPlan
	default:
		return false
	}
}

func validRejectedMutationCode(code MutationErrorCode) bool {
	switch code {
	case MutationErrorInvalidPlan, MutationErrorOwnershipConflict, MutationErrorUnsupported,
		MutationErrorNotReady, MutationErrorBackendRejected:
		return true
	default:
		return false
	}
}

func validMutationDomain(domain MutationDomain) bool {
	switch domain {
	case MutationDomainInfrastructure, MutationDomainPolicy, MutationDomainTarget:
		return true
	default:
		return false
	}
}

func validCanonicalPrefixList(prefixes []netip.Prefix) bool {
	for index, prefix := range prefixes {
		if !prefix.IsValid() || prefix != prefix.Masked() {
			return false
		}
		if index > 0 && prefixes[index-1].String() >= prefix.String() {
			return false
		}
	}
	return true
}

func hasMandatoryProtectedTargets(prefixes []netip.Prefix) bool {
	hasIPv4Loopback := false
	hasIPv6Loopback := false
	for _, prefix := range prefixes {
		switch prefix {
		case netip.MustParsePrefix("127.0.0.0/8"):
			hasIPv4Loopback = true
		case netip.MustParsePrefix("::1/128"):
			hasIPv6Loopback = true
		}
	}
	return hasIPv4Loopback && hasIPv6Loopback
}

func validTargetTimeout(membership TargetMembership, mode ManagedTimeoutMode, expiry int64, hasExpiry bool) bool {
	if membership == TargetMembershipAbsent {
		return mode == ManagedTimeoutNone && !hasExpiry && expiry == 0
	}
	switch mode {
	case ManagedTimeoutNone:
		return !hasExpiry && expiry == 0
	case ManagedTimeoutNative:
		return hasExpiry && expiry > 0
	default:
		return false
	}
}

func nilInterface(value any) bool {
	reflected := reflect.ValueOf(value)
	return !reflected.IsValid() || reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func equalManagedScopes(left, right []ManagedScope) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func prefixStrings(prefixes []netip.Prefix) []string {
	values := make([]string, len(prefixes))
	for index, prefix := range prefixes {
		values[index] = prefix.String()
	}
	return values
}

func digestMutation(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic("firewall mutation digest invariant failed")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type capabilitiesDigestWire struct {
	ToolVersion             string `json:"tool_version"`
	IPv4                    bool   `json:"ipv4"`
	IPv6                    bool   `json:"ipv6"`
	CIDR                    bool   `json:"cidr"`
	NativeSet               bool   `json:"native_set"`
	NativeTimeout           bool   `json:"native_timeout"`
	CrashSafeExpiry         bool   `json:"crash_safe_expiry"`
	AtomicBatch             bool   `json:"atomic_batch"`
	HostInput               bool   `json:"host_input"`
	Forward                 bool   `json:"forward"`
	UFWIntegrationProven    bool   `json:"ufw_integration_proven"`
	DockerIntegrationProven bool   `json:"docker_integration_proven"`
	OwnershipProven         bool   `json:"ownership_proven"`
	MutationReady           bool   `json:"mutation_ready"`
}

func capabilitiesDigest(capabilities FirewallCapabilities) capabilitiesDigestWire {
	return capabilitiesDigestWire{
		ToolVersion: capabilities.ToolVersion(),
		IPv4:        capabilities.SupportsIPv4(), IPv6: capabilities.SupportsIPv6(),
		CIDR: capabilities.SupportsCIDR(), NativeSet: capabilities.SupportsNativeSet(),
		NativeTimeout:   capabilities.SupportsNativeTimeout(),
		CrashSafeExpiry: capabilities.SupportsCrashSafeExpiry(),
		AtomicBatch:     capabilities.SupportsAtomicBatch(),
		HostInput:       capabilities.SupportsHostInput(), Forward: capabilities.SupportsForward(),
		UFWIntegrationProven:    capabilities.UFWIntegrationProven(),
		DockerIntegrationProven: capabilities.DockerIntegrationProven(),
		OwnershipProven:         capabilities.OwnershipProven(), MutationReady: capabilities.MutationReady(),
	}
}

type infrastructurePlanDigestWire struct {
	Format              string                 `json:"format"`
	Operation           MutationOperation      `json:"operation"`
	Domain              MutationDomain         `json:"domain"`
	Backend             BackendKind            `json:"backend"`
	Capabilities        capabilitiesDigestWire `json:"capabilities"`
	OwnerVersion        string                 `json:"owner_version"`
	BasisSnapshotDigest string                 `json:"basis_snapshot_digest"`
	Revision            int64                  `json:"revision"`
	SchemaVersion       int64                  `json:"schema_version"`
}

type policyPlanDigestWire struct {
	Format              string                 `json:"format"`
	Operation           MutationOperation      `json:"operation"`
	Domain              MutationDomain         `json:"domain"`
	Backend             BackendKind            `json:"backend"`
	Capabilities        capabilitiesDigestWire `json:"capabilities"`
	OwnerVersion        string                 `json:"owner_version"`
	BasisSnapshotDigest string                 `json:"basis_snapshot_digest"`
	Revision            int64                  `json:"revision"`
	Allowlist           []string               `json:"allowlist"`
	ProtectedTargets    []string               `json:"protected_targets"`
}

type targetPlanDigestWire struct {
	Format                  string                 `json:"format"`
	Operation               MutationOperation      `json:"operation"`
	Domain                  MutationDomain         `json:"domain"`
	Backend                 BackendKind            `json:"backend"`
	Capabilities            capabilitiesDigestWire `json:"capabilities"`
	OwnerVersion            string                 `json:"owner_version"`
	BasisSnapshotDigest     string                 `json:"basis_snapshot_digest"`
	Revision                int64                  `json:"revision"`
	Target                  string                 `json:"target"`
	Membership              TargetMembership       `json:"membership"`
	TimeoutMode             ManagedTimeoutMode     `json:"timeout_mode"`
	EffectiveUntilUnixMicro *int64                 `json:"effective_until_unix_us"`
	Scopes                  []ManagedScope         `json:"scopes"`
}

type removalDigestWire struct {
	Format              string                 `json:"format"`
	Operation           MutationOperation      `json:"operation"`
	Backend             BackendKind            `json:"backend"`
	Capabilities        capabilitiesDigestWire `json:"capabilities"`
	OwnerVersion        string                 `json:"owner_version"`
	BasisSnapshotDigest string                 `json:"basis_snapshot_digest"`
}

type invalidMutationResultError struct{}

func (invalidMutationResultError) Error() string { return "firewall mutation result is invalid" }
