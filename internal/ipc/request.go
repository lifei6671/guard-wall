package ipc

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"net/netip"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxRequestBytes = 64 * 1024
	maxJSONDepth    = 8
	maxJSONTokens   = 4096
	maxPlanPrefixes = 1024
	ownerVersionV1  = "guard/v1"
)

// Operation identifies one of the closed IPC v1 request operations.
type Operation string

const (
	OperationProbeCapabilities           Operation = "ProbeCapabilities"
	OperationSnapshotManaged             Operation = "SnapshotManaged"
	OperationApplyManagedPlan            Operation = "ApplyManagedPlan"
	OperationRemoveManagedInfrastructure Operation = "RemoveManagedInfrastructure"
)

// Domain identifies the single managed firewall domain carried by a plan.
type Domain string

const (
	DomainInfrastructure Domain = "infrastructure"
	DomainPolicy         Domain = "policy"
	DomainTarget         Domain = "target"
)

// Membership is the desired membership of a target prefix.
type Membership string

const (
	MembershipPresent Membership = "present"
	MembershipAbsent  Membership = "absent"
)

// TimeoutMode selects permanent or native-timeout target membership.
type TimeoutMode string

const (
	TimeoutModeNone   TimeoutMode = "none"
	TimeoutModeNative TimeoutMode = "native"
)

// Scope identifies a firewall traffic scope.
type Scope string

const (
	ScopeInput   Scope = "input"
	ScopeForward Scope = "forward"
)

// ErrorCode classifies an internal request validation failure. It is not a
// response-wire error contract.
type ErrorCode string

const (
	ErrorCodeRequestTooLarge        ErrorCode = "request_too_large"
	ErrorCodeInvalidUTF8            ErrorCode = "invalid_utf8"
	ErrorCodeDuplicateKey           ErrorCode = "duplicate_key"
	ErrorCodeMaxDepthExceeded       ErrorCode = "max_depth_exceeded"
	ErrorCodeTokenLimitExceeded     ErrorCode = "token_limit_exceeded"
	ErrorCodeInvalidJSON            ErrorCode = "invalid_json"
	ErrorCodeSchemaRejected         ErrorCode = "schema_rejected"
	ErrorCodePrefixLimit            ErrorCode = "prefix_limit"
	ErrorCodeNoncanonicalPrefix     ErrorCode = "noncanonical_prefix"
	ErrorCodeNoncanonicalOrder      ErrorCode = "noncanonical_order"
	ErrorCodeProtectedPolicyMissing ErrorCode = "protected_policy_missing"
	ErrorCodeInvalidTimeout         ErrorCode = "invalid_timeout"
	ErrorCodeProtectedTarget        ErrorCode = "protected_target"
	ErrorCodeInvalidScope           ErrorCode = "invalid_scope"
)

// ValidationError reports only a stable internal classification. It never
// includes request bytes or attacker-controlled field contents.
type ValidationError struct {
	code ErrorCode
}

func (e *ValidationError) Error() string {
	return "ipc request validation failed: " + string(e.code)
}

// Code returns the internal failure classification.
func (e *ValidationError) Code() ErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// Request is the closed set of decoded IPC v1 requests.
type Request interface {
	Operation() Operation
	isRequest()
}

// ProbeCapabilitiesRequest is a validated capability-model request.
type ProbeCapabilitiesRequest interface {
	Request
	isProbeCapabilitiesRequest()
}

type probeCapabilitiesRequest struct{}

func (*probeCapabilitiesRequest) Operation() Operation { return OperationProbeCapabilities }
func (*probeCapabilitiesRequest) isRequest()           {}
func (*probeCapabilitiesRequest) isProbeCapabilitiesRequest() {
}

// SnapshotManagedRequest is a validated managed-snapshot request.
type SnapshotManagedRequest interface {
	Request
	isSnapshotManagedRequest()
}

type snapshotManagedRequest struct{}

func (*snapshotManagedRequest) Operation() Operation { return OperationSnapshotManaged }
func (*snapshotManagedRequest) isRequest()           {}
func (*snapshotManagedRequest) isSnapshotManagedRequest() {
}

// ApplyManagedPlanRequest is a validated request containing one managed plan.
type ApplyManagedPlanRequest interface {
	Request
	Plan() ManagedPlan
	isApplyManagedPlanRequest()
}

type applyManagedPlanRequest struct {
	plan ManagedPlan
}

func (*applyManagedPlanRequest) Operation() Operation { return OperationApplyManagedPlan }
func (*applyManagedPlanRequest) isRequest()           {}
func (*applyManagedPlanRequest) isApplyManagedPlanRequest() {
}

// Plan returns the validated, typed managed plan.
func (r *applyManagedPlanRequest) Plan() ManagedPlan {
	if r == nil {
		return nil
	}
	return r.plan
}

// RemoveManagedInfrastructureRequest is a validated ownership-scoped removal.
type RemoveManagedInfrastructureRequest interface {
	Request
	ExpectedOwnerVersion() string
	isRemoveManagedInfrastructureRequest()
}

type removeManagedInfrastructureRequest struct {
	expectedOwnerVersion string
}

func (*removeManagedInfrastructureRequest) Operation() Operation {
	return OperationRemoveManagedInfrastructure
}
func (*removeManagedInfrastructureRequest) isRequest() {}
func (*removeManagedInfrastructureRequest) isRemoveManagedInfrastructureRequest() {
}

// ExpectedOwnerVersion returns the required Guard ownership marker.
func (r *removeManagedInfrastructureRequest) ExpectedOwnerVersion() string {
	if r == nil {
		return ""
	}
	return r.expectedOwnerVersion
}

// ManagedPlan is the closed set of single-domain IPC v1 plans.
type ManagedPlan interface {
	Domain() Domain
	OwnerVersion() string
	BasisSnapshotDigest() string
	Revision() int64
	isManagedPlan()
}

type planBase struct {
	ownerVersion        string
	basisSnapshotDigest string
	revision            int64
}

// InfrastructurePlan is a validated fixed-infrastructure plan.
type InfrastructurePlan interface {
	ManagedPlan
	SchemaVersion() int64
	isInfrastructurePlan()
}

type infrastructurePlan struct {
	planBase
	schemaVersion int64
}

func (*infrastructurePlan) Domain() Domain { return DomainInfrastructure }
func (p *infrastructurePlan) OwnerVersion() string {
	if p == nil {
		return ""
	}
	return p.ownerVersion
}
func (p *infrastructurePlan) BasisSnapshotDigest() string {
	if p == nil {
		return ""
	}
	return p.basisSnapshotDigest
}
func (p *infrastructurePlan) Revision() int64 {
	if p == nil {
		return 0
	}
	return p.revision
}
func (*infrastructurePlan) isManagedPlan()        {}
func (*infrastructurePlan) isInfrastructurePlan() {}

// SchemaVersion returns the fixed infrastructure schema version.
func (p *infrastructurePlan) SchemaVersion() int64 {
	if p == nil {
		return 0
	}
	return p.schemaVersion
}

// PolicyPlan is a validated complete policy replacement.
type PolicyPlan interface {
	ManagedPlan
	Allowlist() []netip.Prefix
	ProtectedTargets() []netip.Prefix
	isPolicyPlan()
}

type policyPlan struct {
	planBase
	allowlist        []netip.Prefix
	protectedTargets []netip.Prefix
}

func (*policyPlan) Domain() Domain { return DomainPolicy }
func (p *policyPlan) OwnerVersion() string {
	if p == nil {
		return ""
	}
	return p.ownerVersion
}
func (p *policyPlan) BasisSnapshotDigest() string {
	if p == nil {
		return ""
	}
	return p.basisSnapshotDigest
}
func (p *policyPlan) Revision() int64 {
	if p == nil {
		return 0
	}
	return p.revision
}
func (*policyPlan) isManagedPlan() {}
func (*policyPlan) isPolicyPlan()  {}

// Allowlist returns a copy of the canonical, sorted allowlist.
func (p *policyPlan) Allowlist() []netip.Prefix {
	if p == nil {
		return nil
	}
	return append([]netip.Prefix(nil), p.allowlist...)
}

// ProtectedTargets returns a copy of the canonical, sorted protected targets.
func (p *policyPlan) ProtectedTargets() []netip.Prefix {
	if p == nil {
		return nil
	}
	return append([]netip.Prefix(nil), p.protectedTargets...)
}

// TargetPlan is a validated target membership plan.
type TargetPlan interface {
	ManagedPlan
	Target() netip.Prefix
	Membership() Membership
	TimeoutMode() TimeoutMode
	EffectiveUntilUnixMicro() (int64, bool)
	Scopes() []Scope
	isTargetPlan()
}

type targetPlan struct {
	planBase
	target                  netip.Prefix
	membership              Membership
	timeoutMode             TimeoutMode
	effectiveUntilUnixMicro int64
	hasEffectiveUntil       bool
	scopes                  []Scope
}

func (*targetPlan) Domain() Domain { return DomainTarget }
func (p *targetPlan) OwnerVersion() string {
	if p == nil {
		return ""
	}
	return p.ownerVersion
}
func (p *targetPlan) BasisSnapshotDigest() string {
	if p == nil {
		return ""
	}
	return p.basisSnapshotDigest
}
func (p *targetPlan) Revision() int64 {
	if p == nil {
		return 0
	}
	return p.revision
}
func (*targetPlan) isManagedPlan() {}
func (*targetPlan) isTargetPlan()  {}

// Target returns the canonical target prefix.
func (p *targetPlan) Target() netip.Prefix {
	if p == nil {
		return netip.Prefix{}
	}
	return p.target
}

// Membership returns the desired target membership.
func (p *targetPlan) Membership() Membership {
	if p == nil {
		return ""
	}
	return p.membership
}

// TimeoutMode returns the target timeout mode.
func (p *targetPlan) TimeoutMode() TimeoutMode {
	if p == nil {
		return ""
	}
	return p.timeoutMode
}

// EffectiveUntilUnixMicro returns the native expiry and whether one is set.
func (p *targetPlan) EffectiveUntilUnixMicro() (int64, bool) {
	if p == nil {
		return 0, false
	}
	return p.effectiveUntilUnixMicro, p.hasEffectiveUntil
}

// Scopes returns a copy of the validated scope order.
func (p *targetPlan) Scopes() []Scope {
	if p == nil {
		return nil
	}
	return append([]Scope(nil), p.scopes...)
}

// DecodeRequest decodes and validates one complete IPC v1 JSON payload.
func DecodeRequest(raw []byte) (Request, error) {
	if len(raw) > maxRequestBytes {
		return nil, validationError(ErrorCodeRequestTooLarge)
	}
	if !utf8.Valid(raw) {
		return nil, validationError(ErrorCodeInvalidUTF8)
	}
	if code := scanJSON(raw); code != "" {
		return nil, validationError(code)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, validationError(ErrorCodeInvalidJSON)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, validationError(ErrorCodeSchemaRejected)
	}
	request, code := decodeRequestObject(root)
	if code != "" {
		return nil, validationError(code)
	}
	return request, nil
}

func validationError(code ErrorCode) error {
	return &ValidationError{code: code}
}

func decodeRequestObject(root map[string]any) (Request, ErrorCode) {
	if !objectShape(root, "version", "operation", "payload") {
		return nil, ErrorCodeSchemaRejected
	}
	version, ok := positiveInt64(root["version"])
	if !ok || version != 1 {
		return nil, ErrorCodeSchemaRejected
	}
	operationText, ok := root["operation"].(string)
	if !ok {
		return nil, ErrorCodeSchemaRejected
	}
	payload, ok := root["payload"].(map[string]any)
	if !ok {
		return nil, ErrorCodeSchemaRejected
	}

	switch Operation(operationText) {
	case OperationProbeCapabilities:
		if len(payload) != 0 {
			return nil, ErrorCodeSchemaRejected
		}
		return &probeCapabilitiesRequest{}, ""
	case OperationSnapshotManaged:
		if len(payload) != 0 {
			return nil, ErrorCodeSchemaRejected
		}
		return &snapshotManagedRequest{}, ""
	case OperationApplyManagedPlan:
		plan, code := decodeManagedPlan(payload)
		if code != "" {
			return nil, code
		}
		return &applyManagedPlanRequest{plan: plan}, ""
	case OperationRemoveManagedInfrastructure:
		if !objectShape(payload, "expected_owner_version") {
			return nil, ErrorCodeSchemaRejected
		}
		owner, ok := payload["expected_owner_version"].(string)
		if !ok || owner != ownerVersionV1 {
			return nil, ErrorCodeSchemaRejected
		}
		return &removeManagedInfrastructureRequest{expectedOwnerVersion: owner}, ""
	default:
		return nil, ErrorCodeSchemaRejected
	}
}

func decodeManagedPlan(payload map[string]any) (ManagedPlan, ErrorCode) {
	domainText, ok := payload["domain"].(string)
	if !ok {
		return nil, ErrorCodeSchemaRejected
	}
	switch Domain(domainText) {
	case DomainInfrastructure:
		return decodeInfrastructurePlan(payload)
	case DomainPolicy:
		return decodePolicyPlan(payload)
	case DomainTarget:
		return decodeTargetPlan(payload)
	default:
		return nil, ErrorCodeSchemaRejected
	}
}

func decodeInfrastructurePlan(payload map[string]any) (ManagedPlan, ErrorCode) {
	if !objectShape(payload, "domain", "owner_version", "basis_snapshot_digest", "infrastructure_revision", "operations") {
		return nil, ErrorCodeSchemaRejected
	}
	base, ok := decodePlanBase(payload, "infrastructure_revision")
	if !ok {
		return nil, ErrorCodeSchemaRejected
	}
	operation, ok := singleOperation(payload["operations"])
	if !ok || !objectShape(operation, "kind", "schema_version") {
		return nil, ErrorCodeSchemaRejected
	}
	kind, kindOK := operation["kind"].(string)
	schemaVersion, versionOK := positiveInt64(operation["schema_version"])
	if !kindOK || kind != "ensure_infrastructure" || !versionOK || schemaVersion != 1 {
		return nil, ErrorCodeSchemaRejected
	}
	return &infrastructurePlan{planBase: base, schemaVersion: schemaVersion}, ""
}

func decodePolicyPlan(payload map[string]any) (ManagedPlan, ErrorCode) {
	if !objectShape(payload, "domain", "owner_version", "basis_snapshot_digest", "policy_revision", "operations") {
		return nil, ErrorCodeSchemaRejected
	}
	base, ok := decodePlanBase(payload, "policy_revision")
	if !ok {
		return nil, ErrorCodeSchemaRejected
	}
	operation, ok := singleOperation(payload["operations"])
	if !ok || !objectShape(operation, "kind", "allowlist", "protected_targets") {
		return nil, ErrorCodeSchemaRejected
	}
	kind, kindOK := operation["kind"].(string)
	allowlist, allowOK := schemaPrefixStrings(operation["allowlist"], 0, maxPlanPrefixes)
	protected, protectedOK := schemaPrefixStrings(operation["protected_targets"], 2, maxPlanPrefixes)
	if !kindOK || kind != "replace_policy" || !allowOK || !protectedOK {
		return nil, ErrorCodeSchemaRejected
	}
	if len(allowlist)+len(protected) > maxPlanPrefixes {
		return nil, ErrorCodePrefixLimit
	}
	parsedAllowlist, code := canonicalPrefixList(allowlist)
	if code != "" {
		return nil, code
	}
	parsedProtected, code := canonicalPrefixList(protected)
	if code != "" {
		return nil, code
	}
	mandatory := map[string]bool{"127.0.0.0/8": false, "::1/128": false}
	for _, prefix := range protected {
		if _, required := mandatory[prefix]; required {
			mandatory[prefix] = true
		}
	}
	if !mandatory["127.0.0.0/8"] || !mandatory["::1/128"] {
		return nil, ErrorCodeProtectedPolicyMissing
	}
	return &policyPlan{
		planBase:         base,
		allowlist:        parsedAllowlist,
		protectedTargets: parsedProtected,
	}, ""
}

func decodeTargetPlan(payload map[string]any) (ManagedPlan, ErrorCode) {
	if !objectShape(payload, "domain", "owner_version", "basis_snapshot_digest", "target_generation", "operations") {
		return nil, ErrorCodeSchemaRejected
	}
	base, ok := decodePlanBase(payload, "target_generation")
	if !ok {
		return nil, ErrorCodeSchemaRejected
	}
	operation, ok := singleOperation(payload["operations"])
	if !ok || !objectShape(operation, "kind", "target", "membership", "timeout_mode", "effective_until_unix_us", "scopes") {
		return nil, ErrorCodeSchemaRejected
	}
	kind, kindOK := operation["kind"].(string)
	targetText, targetOK := schemaPrefixString(operation["target"])
	membershipText, membershipOK := operation["membership"].(string)
	timeoutText, timeoutOK := operation["timeout_mode"].(string)
	expiry, hasExpiry, expiryOK := nullablePositiveInt64(operation["effective_until_unix_us"])
	scopes, scopesOK := schemaScopes(operation["scopes"])
	if !kindOK || kind != "set_target" || !targetOK || !membershipOK || !timeoutOK || !expiryOK || !scopesOK {
		return nil, ErrorCodeSchemaRejected
	}
	membership := Membership(membershipText)
	if membership != MembershipPresent && membership != MembershipAbsent {
		return nil, ErrorCodeSchemaRejected
	}
	timeoutMode := TimeoutMode(timeoutText)
	if timeoutMode != TimeoutModeNone && timeoutMode != TimeoutModeNative {
		return nil, ErrorCodeSchemaRejected
	}
	target, code := canonicalPrefix(targetText)
	if code != "" {
		return nil, code
	}
	for _, protected := range []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	} {
		if target.Overlaps(protected) {
			return nil, ErrorCodeProtectedTarget
		}
	}
	if len(scopes) == 2 && (scopes[0] != ScopeInput || scopes[1] != ScopeForward) {
		return nil, ErrorCodeInvalidScope
	}
	if membership == MembershipAbsent && (timeoutMode != TimeoutModeNone || hasExpiry) {
		return nil, ErrorCodeInvalidTimeout
	}
	if timeoutMode == TimeoutModeNative && !hasExpiry {
		return nil, ErrorCodeInvalidTimeout
	}
	if timeoutMode == TimeoutModeNone && hasExpiry {
		return nil, ErrorCodeInvalidTimeout
	}
	return &targetPlan{
		planBase:                base,
		target:                  target,
		membership:              membership,
		timeoutMode:             timeoutMode,
		effectiveUntilUnixMicro: expiry,
		hasEffectiveUntil:       hasExpiry,
		scopes:                  scopes,
	}, ""
}

func decodePlanBase(payload map[string]any, revisionField string) (planBase, bool) {
	owner, ownerOK := payload["owner_version"].(string)
	digest, digestOK := payload["basis_snapshot_digest"].(string)
	revision, revisionOK := positiveInt64(payload[revisionField])
	if !ownerOK || owner != ownerVersionV1 || !digestOK || !validDigest(digest) || !revisionOK {
		return planBase{}, false
	}
	return planBase{ownerVersion: owner, basisSnapshotDigest: digest, revision: revision}, true
}

func objectShape(value map[string]any, required ...string) bool {
	if len(value) != len(required) {
		return false
	}
	for _, name := range required {
		if _, found := value[name]; !found {
			return false
		}
	}
	return true
}

func singleOperation(value any) (map[string]any, bool) {
	operations, ok := value.([]any)
	if !ok || len(operations) != 1 {
		return nil, false
	}
	operation, ok := operations[0].(map[string]any)
	return operation, ok
}

func positiveInt64(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	rational, ok := new(big.Rat).SetString(string(number))
	if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
		return 0, false
	}
	integer := rational.Num().Int64()
	return integer, integer > 0
}

func nullablePositiveInt64(value any) (integer int64, present bool, valid bool) {
	if value == nil {
		return 0, false, true
	}
	integer, valid = positiveInt64(value)
	return integer, true, valid
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func schemaPrefixStrings(value any, minimum, maximum int) ([]string, bool) {
	items, ok := value.([]any)
	if !ok || len(items) < minimum || len(items) > maximum {
		return nil, false
	}
	result := make([]string, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		text, ok := schemaPrefixString(item)
		if !ok {
			return nil, false
		}
		if _, duplicate := seen[text]; duplicate {
			return nil, false
		}
		seen[text] = struct{}{}
		result[index] = text
	}
	return result, true
}

func schemaPrefixString(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || len(text) < 3 || len(text) > 64 {
		return "", false
	}
	slash := strings.LastIndexByte(text, '/')
	if slash < 1 || slash == len(text)-1 || len(text)-slash-1 > 3 {
		return "", false
	}
	for index, character := range text {
		if index == slash {
			continue
		}
		if index > slash {
			if character < '0' || character > '9' {
				return "", false
			}
			continue
		}
		if !((character >= '0' && character <= '9') ||
			(character >= 'A' && character <= 'F') ||
			(character >= 'a' && character <= 'f') ||
			character == ':' || character == '.') {
			return "", false
		}
	}
	return text, true
}

func schemaScopes(value any) ([]Scope, bool) {
	items, ok := value.([]any)
	if !ok || len(items) < 1 || len(items) > 2 {
		return nil, false
	}
	result := make([]Scope, len(items))
	seen := make(map[Scope]struct{}, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		scope := Scope(text)
		if scope != ScopeInput && scope != ScopeForward {
			return nil, false
		}
		if _, duplicate := seen[scope]; duplicate {
			return nil, false
		}
		seen[scope] = struct{}{}
		result[index] = scope
	}
	return result, true
}

func canonicalPrefixList(values []string) ([]netip.Prefix, ErrorCode) {
	if !sort.StringsAreSorted(values) {
		return nil, ErrorCodeNoncanonicalOrder
	}
	result := make([]netip.Prefix, len(values))
	for index, value := range values {
		prefix, code := canonicalPrefix(value)
		if code != "" {
			return nil, code
		}
		result[index] = prefix
	}
	return result, ""
}

func canonicalPrefix(value string) (netip.Prefix, ErrorCode) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix != prefix.Masked() || prefix.String() != value {
		return netip.Prefix{}, ErrorCodeNoncanonicalPrefix
	}
	return prefix, ""
}

type jsonScan struct {
	tokens int
}

func scanJSON(raw []byte) ErrorCode {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &jsonScan{}
	if code := scanJSONValue(decoder, state, 0); code != "" {
		return code
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrorCodeInvalidJSON
	}
	return ""
}

func scanJSONValue(decoder *json.Decoder, state *jsonScan, parentDepth int) ErrorCode {
	token, err := decoder.Token()
	if err != nil {
		return ErrorCodeInvalidJSON
	}
	if code := countJSONToken(state); code != "" {
		return code
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return ""
	}
	depth := parentDepth + 1
	if depth > maxJSONDepth {
		return ErrorCodeMaxDepthExceeded
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return ErrorCodeInvalidJSON
			}
			if code := countJSONToken(state); code != "" {
				return code
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrorCodeInvalidJSON
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrorCodeDuplicateKey
			}
			seen[key] = struct{}{}
			if code := scanJSONValue(decoder, state, depth); code != "" {
				return code
			}
		}
	case '[':
		for decoder.More() {
			if code := scanJSONValue(decoder, state, depth); code != "" {
				return code
			}
		}
	default:
		return ErrorCodeInvalidJSON
	}
	closing, err := decoder.Token()
	if err != nil {
		return ErrorCodeInvalidJSON
	}
	if code := countJSONToken(state); code != "" {
		return code
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return ErrorCodeInvalidJSON
	}
	return ""
}

func countJSONToken(state *jsonScan) ErrorCode {
	state.tokens++
	if state.tokens > maxJSONTokens {
		return ErrorCodeTokenLimitExceeded
	}
	return ""
}
