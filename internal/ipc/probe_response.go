package ipc

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

const (
	maxProbeCapabilitiesResponseBytes  = 4 * 1024
	maxProbeCapabilitiesResponseDepth  = 2
	maxProbeCapabilitiesResponseTokens = 64
)

// ProbeCapabilitiesFailureCode is the closed set of remote Probe failures.
// It never carries backend output or other attacker-controlled details.
type ProbeCapabilitiesFailureCode string

const (
	// ProbeCapabilitiesFailureCodeUnsupported reports a known unsupported backend,
	// tool, or topology.
	ProbeCapabilitiesFailureCodeUnsupported ProbeCapabilitiesFailureCode = "unsupported"
	// ProbeCapabilitiesFailureCodeNotReady reports that a complete trustworthy
	// capability observation is not currently available.
	ProbeCapabilitiesFailureCodeNotReady ProbeCapabilitiesFailureCode = "not_ready"
)

// ProbeCapabilitiesResponseErrorCode classifies a local Probe response codec
// failure. It is intentionally distinct from a failure code carried on the wire.
type ProbeCapabilitiesResponseErrorCode string

const (
	ProbeCapabilitiesResponseErrorCodeResponseTooLarge   ProbeCapabilitiesResponseErrorCode = "response_too_large"
	ProbeCapabilitiesResponseErrorCodeInvalidUTF8        ProbeCapabilitiesResponseErrorCode = "invalid_utf8"
	ProbeCapabilitiesResponseErrorCodeDuplicateKey       ProbeCapabilitiesResponseErrorCode = "duplicate_key"
	ProbeCapabilitiesResponseErrorCodeMaxDepthExceeded   ProbeCapabilitiesResponseErrorCode = "max_depth_exceeded"
	ProbeCapabilitiesResponseErrorCodeTokenLimitExceeded ProbeCapabilitiesResponseErrorCode = "token_limit_exceeded"
	ProbeCapabilitiesResponseErrorCodeInvalidJSON        ProbeCapabilitiesResponseErrorCode = "invalid_json"
	ProbeCapabilitiesResponseErrorCodeSchemaRejected     ProbeCapabilitiesResponseErrorCode = "schema_rejected"
	ProbeCapabilitiesResponseErrorCodeSemanticRejected   ProbeCapabilitiesResponseErrorCode = "semantic_rejected"
)

// ProbeCapabilitiesResponseError reports only a stable local classification.
// It never includes response bytes, backend output, or attacker-controlled data.
type ProbeCapabilitiesResponseError struct {
	code ProbeCapabilitiesResponseErrorCode
}

func (e *ProbeCapabilitiesResponseError) Error() string {
	if e == nil {
		return "ipc probe capabilities response rejected"
	}
	return "ipc probe capabilities response rejected: " + string(e.code)
}

// Code returns the stable local response failure classification.
func (e *ProbeCapabilitiesResponseError) Code() ProbeCapabilitiesResponseErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// ProbeCapabilitiesResponse is the closed set of IPC v1 Probe responses.
type ProbeCapabilitiesResponse interface {
	Operation() Operation
	isProbeCapabilitiesResponse()
}

// ProbeCapabilitiesSuccessResponse carries one validated immutable capability
// observation. MutationReady false remains a successful observation.
type ProbeCapabilitiesSuccessResponse interface {
	ProbeCapabilitiesResponse
	Capabilities() firewall.FirewallCapabilities
	isProbeCapabilitiesSuccessResponse()
}

// ProbeCapabilitiesFailureResponse carries only a stable closed failure code.
type ProbeCapabilitiesFailureResponse interface {
	ProbeCapabilitiesResponse
	FailureCode() ProbeCapabilitiesFailureCode
	isProbeCapabilitiesFailureResponse()
}

type probeCapabilitiesSuccessResponse struct {
	capabilities firewall.FirewallCapabilities
}

func (*probeCapabilitiesSuccessResponse) Operation() Operation {
	return OperationProbeCapabilities
}
func (*probeCapabilitiesSuccessResponse) isProbeCapabilitiesResponse() {}
func (*probeCapabilitiesSuccessResponse) isProbeCapabilitiesSuccessResponse() {
}

// Capabilities returns the validated immutable capability observation.
func (r *probeCapabilitiesSuccessResponse) Capabilities() firewall.FirewallCapabilities {
	if r == nil {
		return firewall.FirewallCapabilities{}
	}
	return r.capabilities
}

type probeCapabilitiesFailureResponse struct {
	failureCode ProbeCapabilitiesFailureCode
}

func (*probeCapabilitiesFailureResponse) Operation() Operation {
	return OperationProbeCapabilities
}
func (*probeCapabilitiesFailureResponse) isProbeCapabilitiesResponse() {}
func (*probeCapabilitiesFailureResponse) isProbeCapabilitiesFailureResponse() {
}

// FailureCode returns the stable closed remote failure classification.
func (r *probeCapabilitiesFailureResponse) FailureCode() ProbeCapabilitiesFailureCode {
	if r == nil {
		return ""
	}
	return r.failureCode
}

// NewProbeCapabilitiesSuccessResponse constructs a validated success response.
func NewProbeCapabilitiesSuccessResponse(
	capabilities firewall.FirewallCapabilities,
) (ProbeCapabilitiesSuccessResponse, error) {
	if err := capabilities.Validate(); err != nil {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSemanticRejected)
	}
	return &probeCapabilitiesSuccessResponse{capabilities: capabilities}, nil
}

// NewProbeCapabilitiesFailureResponse constructs a closed failure response.
func NewProbeCapabilitiesFailureResponse(
	code ProbeCapabilitiesFailureCode,
) (ProbeCapabilitiesFailureResponse, error) {
	if !validProbeCapabilitiesFailureCode(code) {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	}
	return &probeCapabilitiesFailureResponse{failureCode: code}, nil
}

type probeCapabilitiesSuccessWire struct {
	Version   int64                        `json:"version"`
	Operation Operation                    `json:"operation"`
	Payload   probeCapabilitiesPayloadWire `json:"payload"`
}

type probeCapabilitiesPayloadWire struct {
	Backend                 firewall.BackendKind `json:"backend"`
	ToolVersion             string               `json:"tool_version"`
	IPv4                    bool                 `json:"ipv4"`
	IPv6                    bool                 `json:"ipv6"`
	CIDR                    bool                 `json:"cidr"`
	NativeSet               bool                 `json:"native_set"`
	NativeTimeout           bool                 `json:"native_timeout"`
	CrashSafeExpiry         bool                 `json:"crash_safe_expiry"`
	AtomicBatch             bool                 `json:"atomic_batch"`
	HostInput               bool                 `json:"host_input"`
	Forward                 bool                 `json:"forward"`
	UFWIntegrationProven    bool                 `json:"ufw_integration_proven"`
	DockerIntegrationProven bool                 `json:"docker_integration_proven"`
	OwnershipProven         bool                 `json:"ownership_proven"`
	MutationReady           bool                 `json:"mutation_ready"`
}

type probeCapabilitiesFailureWire struct {
	Version   int64                        `json:"version"`
	Operation Operation                    `json:"operation"`
	ErrorCode ProbeCapabilitiesFailureCode `json:"error_code"`
}

// EncodeProbeCapabilitiesResponse encodes one validated response as compact,
// deterministic JSON. It validates success capabilities again immediately
// before encoding and never returns bytes together with an error.
func EncodeProbeCapabilitiesResponse(response ProbeCapabilitiesResponse) ([]byte, error) {
	var wire any
	switch value := response.(type) {
	case *probeCapabilitiesSuccessResponse:
		if value == nil || value.capabilities.Validate() != nil {
			return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSemanticRejected)
		}
		wire = probeCapabilitiesSuccessWire{
			Version:   1,
			Operation: OperationProbeCapabilities,
			Payload:   probeCapabilitiesPayloadFromDomain(value.capabilities),
		}
	case *probeCapabilitiesFailureResponse:
		if value == nil || !validProbeCapabilitiesFailureCode(value.failureCode) {
			return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
		}
		wire = probeCapabilitiesFailureWire{
			Version:   1,
			Operation: OperationProbeCapabilities,
			ErrorCode: value.failureCode,
		}
	default:
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	}

	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeInvalidJSON)
	}
	if len(raw) > maxProbeCapabilitiesResponseBytes {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeResponseTooLarge)
	}
	return raw, nil
}

func probeCapabilitiesPayloadFromDomain(
	capabilities firewall.FirewallCapabilities,
) probeCapabilitiesPayloadWire {
	return probeCapabilitiesPayloadWire{
		Backend:                 capabilities.Backend(),
		ToolVersion:             capabilities.ToolVersion(),
		IPv4:                    capabilities.SupportsIPv4(),
		IPv6:                    capabilities.SupportsIPv6(),
		CIDR:                    capabilities.SupportsCIDR(),
		NativeSet:               capabilities.SupportsNativeSet(),
		NativeTimeout:           capabilities.SupportsNativeTimeout(),
		CrashSafeExpiry:         capabilities.SupportsCrashSafeExpiry(),
		AtomicBatch:             capabilities.SupportsAtomicBatch(),
		HostInput:               capabilities.SupportsHostInput(),
		Forward:                 capabilities.SupportsForward(),
		UFWIntegrationProven:    capabilities.UFWIntegrationProven(),
		DockerIntegrationProven: capabilities.DockerIntegrationProven(),
		OwnershipProven:         capabilities.OwnershipProven(),
		MutationReady:           capabilities.MutationReady(),
	}
}

// DecodeProbeCapabilitiesResponse decodes and validates one complete Probe
// response JSON payload. Success payloads are rebuilt through the Firewall
// domain constructor; no second, looser semantic authority is maintained here.
func DecodeProbeCapabilitiesResponse(raw []byte) (ProbeCapabilitiesResponse, error) {
	if len(raw) > maxProbeCapabilitiesResponseBytes {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeResponseTooLarge)
	}
	if !utf8.Valid(raw) {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeInvalidUTF8)
	}
	if code := scanProbeCapabilitiesResponseJSON(raw); code != "" {
		return nil, probeCapabilitiesResponseError(code)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeInvalidJSON)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	}
	version, ok := positiveInt64(root["version"])
	if !ok || version != 1 {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	}
	operation, ok := root["operation"].(string)
	if !ok || Operation(operation) != OperationProbeCapabilities {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	}

	switch {
	case objectShape(root, "version", "operation", "payload"):
		return decodeProbeCapabilitiesSuccess(root["payload"])
	case objectShape(root, "version", "operation", "error_code"):
		code, ok := root["error_code"].(string)
		if !ok {
			return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
		}
		response, err := NewProbeCapabilitiesFailureResponse(ProbeCapabilitiesFailureCode(code))
		if err != nil {
			return nil, err
		}
		return response, nil
	default:
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	}
}

func decodeProbeCapabilitiesSuccess(value any) (ProbeCapabilitiesResponse, error) {
	payload, ok := value.(map[string]any)
	if !ok || !objectShape(
		payload,
		"backend",
		"tool_version",
		"ipv4",
		"ipv6",
		"cidr",
		"native_set",
		"native_timeout",
		"crash_safe_expiry",
		"atomic_batch",
		"host_input",
		"forward",
		"ufw_integration_proven",
		"docker_integration_proven",
		"ownership_proven",
		"mutation_ready",
	) {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	}

	backend, backendOK := payload["backend"].(string)
	toolVersion, toolVersionOK := payload["tool_version"].(string)
	ipv4, ipv4OK := payload["ipv4"].(bool)
	ipv6, ipv6OK := payload["ipv6"].(bool)
	cidr, cidrOK := payload["cidr"].(bool)
	nativeSet, nativeSetOK := payload["native_set"].(bool)
	nativeTimeout, nativeTimeoutOK := payload["native_timeout"].(bool)
	crashSafeExpiry, crashSafeExpiryOK := payload["crash_safe_expiry"].(bool)
	atomicBatch, atomicBatchOK := payload["atomic_batch"].(bool)
	hostInput, hostInputOK := payload["host_input"].(bool)
	forward, forwardOK := payload["forward"].(bool)
	ufwIntegrationProven, ufwIntegrationProvenOK := payload["ufw_integration_proven"].(bool)
	dockerIntegrationProven, dockerIntegrationProvenOK := payload["docker_integration_proven"].(bool)
	ownershipProven, ownershipProvenOK := payload["ownership_proven"].(bool)
	mutationReady, mutationReadyOK := payload["mutation_ready"].(bool)
	if !backendOK || !validProbeCapabilitiesWireBackend(backend) ||
		!toolVersionOK || !validProbeCapabilitiesWireToolVersion(toolVersion) ||
		!ipv4OK || !ipv6OK || !cidrOK ||
		!nativeSetOK || !nativeTimeoutOK || !crashSafeExpiryOK || !atomicBatchOK ||
		!hostInputOK || !forwardOK || !ufwIntegrationProvenOK ||
		!dockerIntegrationProvenOK || !ownershipProvenOK || !mutationReadyOK {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	}

	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend:                 firewall.BackendKind(backend),
		ToolVersion:             toolVersion,
		IPv4:                    ipv4,
		IPv6:                    ipv6,
		CIDR:                    cidr,
		NativeSet:               nativeSet,
		NativeTimeout:           nativeTimeout,
		CrashSafeExpiry:         crashSafeExpiry,
		AtomicBatch:             atomicBatch,
		HostInput:               hostInput,
		Forward:                 forward,
		UFWIntegrationProven:    ufwIntegrationProven,
		DockerIntegrationProven: dockerIntegrationProven,
		OwnershipProven:         ownershipProven,
		MutationReady:           mutationReady,
	})
	if err != nil {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeSemanticRejected)
	}
	return &probeCapabilitiesSuccessResponse{capabilities: capabilities}, nil
}

func validProbeCapabilitiesWireBackend(backend string) bool {
	switch firewall.BackendKind(backend) {
	case firewall.BackendKindNftablesNative,
		firewall.BackendKindIptablesNFT,
		firewall.BackendKindIptablesLegacy:
		return true
	default:
		return false
	}
}

func validProbeCapabilitiesWireToolVersion(version string) bool {
	if len(version) == 0 || len(version) > 128 || version[0] == ' ' || version[len(version)-1] == ' ' {
		return false
	}
	for index := 0; index < len(version); index++ {
		if version[index] < 0x20 || version[index] > 0x7e {
			return false
		}
	}
	return true
}

func validProbeCapabilitiesFailureCode(code ProbeCapabilitiesFailureCode) bool {
	switch code {
	case ProbeCapabilitiesFailureCodeUnsupported, ProbeCapabilitiesFailureCodeNotReady:
		return true
	default:
		return false
	}
}

func probeCapabilitiesResponseError(code ProbeCapabilitiesResponseErrorCode) error {
	return &ProbeCapabilitiesResponseError{code: code}
}

type probeCapabilitiesResponseScan struct {
	tokens int
}

func scanProbeCapabilitiesResponseJSON(raw []byte) ProbeCapabilitiesResponseErrorCode {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &probeCapabilitiesResponseScan{}
	if code := scanProbeCapabilitiesResponseValue(decoder, state, 0); code != "" {
		return code
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ProbeCapabilitiesResponseErrorCodeInvalidJSON
	}
	return ""
}

func scanProbeCapabilitiesResponseValue(
	decoder *json.Decoder,
	state *probeCapabilitiesResponseScan,
	parentDepth int,
) ProbeCapabilitiesResponseErrorCode {
	token, err := decoder.Token()
	if err != nil {
		return ProbeCapabilitiesResponseErrorCodeInvalidJSON
	}
	if code := countProbeCapabilitiesResponseToken(state); code != "" {
		return code
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return ""
	}
	depth := parentDepth + 1
	if depth > maxProbeCapabilitiesResponseDepth {
		return ProbeCapabilitiesResponseErrorCodeMaxDepthExceeded
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return ProbeCapabilitiesResponseErrorCodeInvalidJSON
			}
			if code := countProbeCapabilitiesResponseToken(state); code != "" {
				return code
			}
			key, ok := keyToken.(string)
			if !ok {
				return ProbeCapabilitiesResponseErrorCodeInvalidJSON
			}
			if _, duplicate := seen[key]; duplicate {
				return ProbeCapabilitiesResponseErrorCodeDuplicateKey
			}
			seen[key] = struct{}{}
			if code := scanProbeCapabilitiesResponseValue(decoder, state, depth); code != "" {
				return code
			}
		}
	case '[':
		for decoder.More() {
			if code := scanProbeCapabilitiesResponseValue(decoder, state, depth); code != "" {
				return code
			}
		}
	default:
		return ProbeCapabilitiesResponseErrorCodeInvalidJSON
	}

	closing, err := decoder.Token()
	if err != nil {
		return ProbeCapabilitiesResponseErrorCodeInvalidJSON
	}
	if code := countProbeCapabilitiesResponseToken(state); code != "" {
		return code
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return ProbeCapabilitiesResponseErrorCodeInvalidJSON
	}
	return ""
}

func countProbeCapabilitiesResponseToken(
	state *probeCapabilitiesResponseScan,
) ProbeCapabilitiesResponseErrorCode {
	state.tokens++
	if state.tokens > maxProbeCapabilitiesResponseTokens {
		return ProbeCapabilitiesResponseErrorCodeTokenLimitExceeded
	}
	return ""
}
