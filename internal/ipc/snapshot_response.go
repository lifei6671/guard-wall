package ipc

import (
	"bytes"
	"encoding/json"
	"io"
	"net/netip"
	"unicode/utf8"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

const (
	maxSnapshotManagedResponseBytes  = 1 << 20
	maxSnapshotManagedResponseDepth  = 4
	maxSnapshotManagedResponseTokens = 32768
	snapshotManagedMembershipPresent = "present"
)

// SnapshotManagedFailureCode is the closed set of remote Snapshot failures.
type SnapshotManagedFailureCode string

const (
	SnapshotManagedFailureCodeUnsupported       SnapshotManagedFailureCode = "unsupported"
	SnapshotManagedFailureCodeNotReady          SnapshotManagedFailureCode = "not_ready"
	SnapshotManagedFailureCodeOwnershipConflict SnapshotManagedFailureCode = "ownership_conflict"
)

// SnapshotManagedResponseErrorCode classifies a local Snapshot response
// codec failure. It is distinct from a failure code carried on the wire.
type SnapshotManagedResponseErrorCode string

const (
	SnapshotManagedResponseErrorCodeResponseTooLarge   SnapshotManagedResponseErrorCode = "response_too_large"
	SnapshotManagedResponseErrorCodeInvalidUTF8        SnapshotManagedResponseErrorCode = "invalid_utf8"
	SnapshotManagedResponseErrorCodeDuplicateKey       SnapshotManagedResponseErrorCode = "duplicate_key"
	SnapshotManagedResponseErrorCodeMaxDepthExceeded   SnapshotManagedResponseErrorCode = "max_depth_exceeded"
	SnapshotManagedResponseErrorCodeTokenLimitExceeded SnapshotManagedResponseErrorCode = "token_limit_exceeded"
	SnapshotManagedResponseErrorCodeInvalidJSON        SnapshotManagedResponseErrorCode = "invalid_json"
	SnapshotManagedResponseErrorCodeSchemaRejected     SnapshotManagedResponseErrorCode = "schema_rejected"
	SnapshotManagedResponseErrorCodeSemanticRejected   SnapshotManagedResponseErrorCode = "semantic_rejected"
)

// SnapshotManagedResponseError reports only a stable local classification.
type SnapshotManagedResponseError struct {
	code SnapshotManagedResponseErrorCode
}

func (e *SnapshotManagedResponseError) Error() string {
	if e == nil {
		return "ipc managed snapshot response rejected"
	}
	return "ipc managed snapshot response rejected: " + string(e.code)
}

// Code returns the stable local response failure classification.
func (e *SnapshotManagedResponseError) Code() SnapshotManagedResponseErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// SnapshotManagedResponse is the closed set of IPC v1 Snapshot responses.
type SnapshotManagedResponse interface {
	Operation() Operation
	isSnapshotManagedResponse()
}

// SnapshotManagedSuccessResponse carries one validated immutable snapshot.
type SnapshotManagedSuccessResponse interface {
	SnapshotManagedResponse
	Snapshot() firewall.ManagedSnapshot
	isSnapshotManagedSuccessResponse()
}

// SnapshotManagedFailureResponse carries only a stable closed failure code.
type SnapshotManagedFailureResponse interface {
	SnapshotManagedResponse
	FailureCode() SnapshotManagedFailureCode
	isSnapshotManagedFailureResponse()
}

type snapshotManagedSuccessResponse struct {
	snapshot firewall.ManagedSnapshot
}

func (*snapshotManagedSuccessResponse) Operation() Operation              { return OperationSnapshotManaged }
func (*snapshotManagedSuccessResponse) isSnapshotManagedResponse()        {}
func (*snapshotManagedSuccessResponse) isSnapshotManagedSuccessResponse() {}

// Snapshot returns the validated immutable managed snapshot.
func (r *snapshotManagedSuccessResponse) Snapshot() firewall.ManagedSnapshot {
	if r == nil {
		return firewall.ManagedSnapshot{}
	}
	return r.snapshot
}

type snapshotManagedFailureResponse struct {
	failureCode SnapshotManagedFailureCode
}

func (*snapshotManagedFailureResponse) Operation() Operation              { return OperationSnapshotManaged }
func (*snapshotManagedFailureResponse) isSnapshotManagedResponse()        {}
func (*snapshotManagedFailureResponse) isSnapshotManagedFailureResponse() {}

// FailureCode returns the stable closed remote failure classification.
func (r *snapshotManagedFailureResponse) FailureCode() SnapshotManagedFailureCode {
	if r == nil {
		return ""
	}
	return r.failureCode
}

// NewSnapshotManagedSuccessResponse constructs a validated success response.
func NewSnapshotManagedSuccessResponse(
	snapshot firewall.ManagedSnapshot,
) (SnapshotManagedSuccessResponse, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
	}
	return &snapshotManagedSuccessResponse{snapshot: snapshot}, nil
}

// NewSnapshotManagedFailureResponse constructs a closed failure response.
func NewSnapshotManagedFailureResponse(
	code SnapshotManagedFailureCode,
) (SnapshotManagedFailureResponse, error) {
	if !validSnapshotManagedFailureCode(code) {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	return &snapshotManagedFailureResponse{failureCode: code}, nil
}

type snapshotManagedSuccessWire struct {
	Version   int64                      `json:"version"`
	Operation Operation                  `json:"operation"`
	Payload   snapshotManagedPayloadWire `json:"payload"`
}

type snapshotManagedPayloadWire struct {
	SnapshotDigest       string                      `json:"snapshot_digest"`
	Infrastructure       *snapshotInfrastructureWire `json:"infrastructure"`
	Policy               *snapshotPolicyWire         `json:"policy"`
	Targets              []snapshotTargetWire        `json:"targets"`
	ForeignContextDigest string                      `json:"foreign_context_digest"`
}

type snapshotInfrastructureWire struct {
	Backend       firewall.BackendKind `json:"backend"`
	OwnerVersion  string               `json:"owner_version"`
	SchemaVersion int64                `json:"schema_version"`
	Digest        string               `json:"digest"`
}

type snapshotPolicyWire struct {
	RelationDigest string `json:"relation_digest"`
}

type snapshotTargetWire struct {
	Target                  string                      `json:"target"`
	Membership              string                      `json:"membership"`
	TimeoutMode             firewall.ManagedTimeoutMode `json:"timeout_mode"`
	EffectiveUntilUnixMicro *int64                      `json:"effective_until_unix_us"`
	Input                   bool                        `json:"input"`
	Forward                 bool                        `json:"forward"`
}

type snapshotManagedFailureWire struct {
	Version   int64                      `json:"version"`
	Operation Operation                  `json:"operation"`
	ErrorCode SnapshotManagedFailureCode `json:"error_code"`
}

// EncodeSnapshotManagedResponse encodes one validated response as compact,
// deterministic JSON. Validation completes before bytes are returned.
func EncodeSnapshotManagedResponse(response SnapshotManagedResponse) ([]byte, error) {
	var wire any
	switch value := response.(type) {
	case *snapshotManagedSuccessResponse:
		if value == nil || value.snapshot.Validate() != nil {
			return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
		}
		wire = snapshotManagedSuccessWire{
			Version:   1,
			Operation: OperationSnapshotManaged,
			Payload:   snapshotManagedPayloadFromDomain(value.snapshot),
		}
	case *snapshotManagedFailureResponse:
		if value == nil || !validSnapshotManagedFailureCode(value.failureCode) {
			return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
		}
		wire = snapshotManagedFailureWire{
			Version:   1,
			Operation: OperationSnapshotManaged,
			ErrorCode: value.failureCode,
		}
	default:
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}

	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeInvalidJSON)
	}
	if len(raw) > maxSnapshotManagedResponseBytes {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeResponseTooLarge)
	}
	return raw, nil
}

func snapshotManagedPayloadFromDomain(snapshot firewall.ManagedSnapshot) snapshotManagedPayloadWire {
	state := snapshot.ManagedState()
	payload := snapshotManagedPayloadWire{
		SnapshotDigest:       snapshot.Digest(),
		Targets:              make([]snapshotTargetWire, 0, len(state.Targets())),
		ForeignContextDigest: snapshot.ForeignContext().Digest(),
	}
	if observation, ok := state.Infrastructure(); ok {
		payload.Infrastructure = &snapshotInfrastructureWire{
			Backend:       observation.Backend(),
			OwnerVersion:  observation.OwnerVersion(),
			SchemaVersion: observation.SchemaVersion(),
			Digest:        observation.Digest(),
		}
	}
	if observation, ok := state.Policy(); ok {
		payload.Policy = &snapshotPolicyWire{RelationDigest: observation.RelationDigest()}
	}
	for _, observation := range state.Targets() {
		wire := snapshotTargetWire{
			Target:      observation.Target().String(),
			Membership:  snapshotManagedMembershipPresent,
			TimeoutMode: observation.TimeoutMode(),
		}
		if expiry, ok := observation.EffectiveUntilUnixMicro(); ok {
			wire.EffectiveUntilUnixMicro = &expiry
		}
		for _, scope := range observation.Scopes() {
			switch scope {
			case firewall.ManagedScopeInput:
				wire.Input = true
			case firewall.ManagedScopeForward:
				wire.Forward = true
			}
		}
		payload.Targets = append(payload.Targets, wire)
	}
	return payload
}

// DecodeSnapshotManagedResponse decodes and validates one complete Snapshot
// response. Success values are rebuilt through Firewall domain constructors.
func DecodeSnapshotManagedResponse(raw []byte) (SnapshotManagedResponse, error) {
	if len(raw) > maxSnapshotManagedResponseBytes {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeResponseTooLarge)
	}
	if !utf8.Valid(raw) {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeInvalidUTF8)
	}
	if code := scanSnapshotManagedResponseJSON(raw); code != "" {
		return nil, snapshotManagedResponseError(code)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeInvalidJSON)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	version, ok := positiveInt64(root["version"])
	if !ok || version != 1 {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	operation, ok := root["operation"].(string)
	if !ok || Operation(operation) != OperationSnapshotManaged {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}

	switch {
	case objectShape(root, "version", "operation", "payload"):
		return decodeSnapshotManagedSuccess(root["payload"])
	case objectShape(root, "version", "operation", "error_code"):
		code, ok := root["error_code"].(string)
		if !ok {
			return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
		}
		return NewSnapshotManagedFailureResponse(SnapshotManagedFailureCode(code))
	default:
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
}

func decodeSnapshotManagedSuccess(value any) (SnapshotManagedResponse, error) {
	payload, ok := value.(map[string]any)
	if !ok || !objectShape(payload, "snapshot_digest", "infrastructure", "policy", "targets", "foreign_context_digest") {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	snapshotDigest, snapshotDigestOK := payload["snapshot_digest"].(string)
	foreignDigest, foreignDigestOK := payload["foreign_context_digest"].(string)
	if !snapshotDigestOK || !validSnapshotManagedDigest(snapshotDigest) ||
		!foreignDigestOK || !validSnapshotManagedDigest(foreignDigest) {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}

	infrastructure, err := decodeSnapshotInfrastructure(payload["infrastructure"])
	if err != nil {
		return nil, err
	}
	policy, err := decodeSnapshotPolicy(payload["policy"])
	if err != nil {
		return nil, err
	}
	targets, err := decodeSnapshotTargets(payload["targets"])
	if err != nil {
		return nil, err
	}
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{
		Infrastructure: infrastructure,
		Policy:         policy,
		Targets:        targets,
	})
	if err != nil {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: foreignDigest})
	if err != nil {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{
		ManagedState:   state,
		ForeignContext: foreign,
	})
	if err != nil || snapshot.Digest() != snapshotDigest {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
	}
	return &snapshotManagedSuccessResponse{snapshot: snapshot}, nil
}

func decodeSnapshotInfrastructure(value any) (*firewall.InfrastructureObservation, error) {
	if value == nil {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok || !objectShape(object, "backend", "owner_version", "schema_version", "digest") {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	backend, backendOK := object["backend"].(string)
	owner, ownerOK := object["owner_version"].(string)
	schemaVersion, schemaOK := positiveInt64(object["schema_version"])
	digest, digestOK := object["digest"].(string)
	if !backendOK || !ownerOK || !schemaOK || !digestOK || !validSnapshotManagedDigest(digest) {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	observation, err := firewall.NewInfrastructureObservation(firewall.InfrastructureObservationSpec{
		Backend:       firewall.BackendKind(backend),
		OwnerVersion:  owner,
		SchemaVersion: schemaVersion,
		Digest:        digest,
	})
	if err != nil {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
	}
	return &observation, nil
}

func decodeSnapshotPolicy(value any) (*firewall.PolicyObservation, error) {
	if value == nil {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok || !objectShape(object, "relation_digest") {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	digest, ok := object["relation_digest"].(string)
	if !ok || !validSnapshotManagedDigest(digest) {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	observation, err := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{RelationDigest: digest})
	if err != nil {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
	}
	return &observation, nil
}

func decodeSnapshotTargets(value any) ([]firewall.TargetObservation, error) {
	array, ok := value.([]any)
	if !ok || len(array) > firewall.MaxManagedSnapshotTargets {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	targets := make([]firewall.TargetObservation, 0, len(array))
	previous := ""
	for _, item := range array {
		object, ok := item.(map[string]any)
		if !ok || !objectShape(object, "target", "membership", "timeout_mode", "effective_until_unix_us", "input", "forward") {
			return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
		}
		targetText, targetOK := object["target"].(string)
		membership, membershipOK := object["membership"].(string)
		timeoutText, timeoutOK := object["timeout_mode"].(string)
		input, inputOK := object["input"].(bool)
		forward, forwardOK := object["forward"].(bool)
		if !targetOK || !membershipOK || membership != snapshotManagedMembershipPresent ||
			!timeoutOK || !inputOK || !forwardOK {
			return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
		}
		if !input && !forward {
			return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
		}
		prefix, err := netip.ParsePrefix(targetText)
		if err != nil || prefix.String() != targetText || (previous != "" && previous >= targetText) {
			return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
		}
		previous = targetText

		var expiry *int64
		if object["effective_until_unix_us"] != nil {
			value, ok := positiveInt64(object["effective_until_unix_us"])
			if !ok {
				return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSchemaRejected)
			}
			expiry = &value
		}
		scopes := make([]firewall.ManagedScope, 0, 2)
		if input {
			scopes = append(scopes, firewall.ManagedScopeInput)
		}
		if forward {
			scopes = append(scopes, firewall.ManagedScopeForward)
		}
		observation, err := firewall.NewTargetObservation(firewall.TargetObservationSpec{
			Target:                  prefix,
			TimeoutMode:             firewall.ManagedTimeoutMode(timeoutText),
			EffectiveUntilUnixMicro: expiry,
			Scopes:                  scopes,
		})
		if err != nil {
			return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeSemanticRejected)
		}
		targets = append(targets, observation)
	}
	return targets, nil
}

func validSnapshotManagedFailureCode(code SnapshotManagedFailureCode) bool {
	switch code {
	case SnapshotManagedFailureCodeUnsupported,
		SnapshotManagedFailureCodeNotReady,
		SnapshotManagedFailureCodeOwnershipConflict:
		return true
	default:
		return false
	}
}

func validSnapshotManagedDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, character := range digest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func snapshotManagedResponseError(code SnapshotManagedResponseErrorCode) error {
	return &SnapshotManagedResponseError{code: code}
}

type snapshotManagedResponseScan struct {
	tokens int
}

func scanSnapshotManagedResponseJSON(raw []byte) SnapshotManagedResponseErrorCode {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &snapshotManagedResponseScan{}
	if code := scanSnapshotManagedResponseValue(decoder, state, 0); code != "" {
		return code
	}
	if _, err := decoder.Token(); err != io.EOF {
		return SnapshotManagedResponseErrorCodeInvalidJSON
	}
	return ""
}

func scanSnapshotManagedResponseValue(
	decoder *json.Decoder,
	state *snapshotManagedResponseScan,
	parentDepth int,
) SnapshotManagedResponseErrorCode {
	token, err := decoder.Token()
	if err != nil {
		return SnapshotManagedResponseErrorCodeInvalidJSON
	}
	if code := countSnapshotManagedResponseToken(state); code != "" {
		return code
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return ""
	}
	depth := parentDepth + 1
	if depth > maxSnapshotManagedResponseDepth {
		return SnapshotManagedResponseErrorCodeMaxDepthExceeded
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return SnapshotManagedResponseErrorCodeInvalidJSON
			}
			if code := countSnapshotManagedResponseToken(state); code != "" {
				return code
			}
			key, ok := keyToken.(string)
			if !ok {
				return SnapshotManagedResponseErrorCodeInvalidJSON
			}
			if _, duplicate := seen[key]; duplicate {
				return SnapshotManagedResponseErrorCodeDuplicateKey
			}
			seen[key] = struct{}{}
			if code := scanSnapshotManagedResponseValue(decoder, state, depth); code != "" {
				return code
			}
		}
	case '[':
		for decoder.More() {
			if code := scanSnapshotManagedResponseValue(decoder, state, depth); code != "" {
				return code
			}
		}
	default:
		return SnapshotManagedResponseErrorCodeInvalidJSON
	}

	closing, err := decoder.Token()
	if err != nil {
		return SnapshotManagedResponseErrorCodeInvalidJSON
	}
	if code := countSnapshotManagedResponseToken(state); code != "" {
		return code
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return SnapshotManagedResponseErrorCodeInvalidJSON
	}
	return ""
}

func countSnapshotManagedResponseToken(state *snapshotManagedResponseScan) SnapshotManagedResponseErrorCode {
	state.tokens++
	if state.tokens > maxSnapshotManagedResponseTokens {
		return SnapshotManagedResponseErrorCodeTokenLimitExceeded
	}
	return ""
}
