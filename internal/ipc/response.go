package ipc

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

const (
	maxMutationResponseBytes  = 4 * 1024
	maxMutationResponseDepth  = 2
	maxMutationResponseTokens = 32
)

// MutationStatus identifies one of the closed mutation response outcomes.
type MutationStatus string

const (
	MutationStatusConfirmed MutationStatus = "confirmed"
	MutationStatusRejected  MutationStatus = "rejected"
	MutationStatusUnknown   MutationStatus = "unknown"
)

// MutationErrorCode is a stable error code carried on the mutation response wire.
type MutationErrorCode string

const (
	MutationErrorCodeInvalidPlan       MutationErrorCode = "invalid_plan"
	MutationErrorCodeOwnershipConflict MutationErrorCode = "ownership_conflict"
	MutationErrorCodeUnsupported       MutationErrorCode = "unsupported"
	MutationErrorCodeNotReady          MutationErrorCode = "not_ready"
	MutationErrorCodeBackendRejected   MutationErrorCode = "backend_rejected"
	MutationErrorCodeUnknownResult     MutationErrorCode = "unknown_result"
)

// MutationResponseErrorCode classifies a local mutation response codec failure.
// It is intentionally distinct from MutationErrorCode on the response wire.
type MutationResponseErrorCode string

const (
	MutationResponseErrorCodeResponseTooLarge   MutationResponseErrorCode = "response_too_large"
	MutationResponseErrorCodeInvalidUTF8        MutationResponseErrorCode = "invalid_utf8"
	MutationResponseErrorCodeDuplicateKey       MutationResponseErrorCode = "duplicate_key"
	MutationResponseErrorCodeMaxDepthExceeded   MutationResponseErrorCode = "max_depth_exceeded"
	MutationResponseErrorCodeTokenLimitExceeded MutationResponseErrorCode = "token_limit_exceeded"
	MutationResponseErrorCodeInvalidJSON        MutationResponseErrorCode = "invalid_json"
	MutationResponseErrorCodeSchemaRejected     MutationResponseErrorCode = "schema_rejected"
)

// MutationResponseError reports only a stable local codec classification. It
// never includes response bytes or attacker-controlled field contents.
type MutationResponseError struct {
	code MutationResponseErrorCode
}

func (e *MutationResponseError) Error() string {
	if e == nil {
		return "ipc mutation response rejected"
	}
	return "ipc mutation response rejected: " + string(e.code)
}

// Code returns the stable local codec failure classification.
func (e *MutationResponseError) Code() MutationResponseErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// MutationResponse is the closed set of decoded IPC v1 mutation responses.
type MutationResponse interface {
	Operation() Operation
	Status() MutationStatus
	ErrorCode() (MutationErrorCode, bool)
	isMutationResponse()
}

// ApplyManagedPlanResponse is a typed response for one managed plan domain.
type ApplyManagedPlanResponse interface {
	MutationResponse
	Domain() Domain
	isApplyManagedPlanResponse()
}

// RemoveManagedInfrastructureResponse is a typed ownership-scoped removal response.
type RemoveManagedInfrastructureResponse interface {
	MutationResponse
	isRemoveManagedInfrastructureResponse()
}

type mutationResponseBase struct {
	status       MutationStatus
	errorCode    MutationErrorCode
	hasErrorCode bool
}

func (r mutationResponseBase) Status() MutationStatus {
	return r.status
}

func (r mutationResponseBase) ErrorCode() (MutationErrorCode, bool) {
	return r.errorCode, r.hasErrorCode
}

type applyManagedPlanResponse struct {
	mutationResponseBase
	domain Domain
}

func (*applyManagedPlanResponse) Operation() Operation { return OperationApplyManagedPlan }
func (*applyManagedPlanResponse) isMutationResponse()  {}
func (*applyManagedPlanResponse) isApplyManagedPlanResponse() {
}

func (r *applyManagedPlanResponse) Domain() Domain {
	if r == nil {
		return ""
	}
	return r.domain
}

type removeManagedInfrastructureResponse struct {
	mutationResponseBase
}

func (*removeManagedInfrastructureResponse) Operation() Operation {
	return OperationRemoveManagedInfrastructure
}
func (*removeManagedInfrastructureResponse) isMutationResponse() {}
func (*removeManagedInfrastructureResponse) isRemoveManagedInfrastructureResponse() {
}

// NewApplyManagedPlanConfirmedResponse constructs a confirmed Apply response.
func NewApplyManagedPlanConfirmedResponse(domain Domain) (ApplyManagedPlanResponse, error) {
	return newApplyManagedPlanResponse(domain, MutationStatusConfirmed, "", false)
}

// NewApplyManagedPlanRejectedResponse constructs a rejected Apply response.
func NewApplyManagedPlanRejectedResponse(domain Domain, code MutationErrorCode) (ApplyManagedPlanResponse, error) {
	return newApplyManagedPlanResponse(domain, MutationStatusRejected, code, true)
}

// NewApplyManagedPlanUnknownResponse constructs an unknown Apply response.
func NewApplyManagedPlanUnknownResponse(domain Domain) (ApplyManagedPlanResponse, error) {
	return newApplyManagedPlanResponse(domain, MutationStatusUnknown, MutationErrorCodeUnknownResult, true)
}

// NewRemoveManagedInfrastructureConfirmedResponse constructs a confirmed Remove response.
func NewRemoveManagedInfrastructureConfirmedResponse() RemoveManagedInfrastructureResponse {
	return &removeManagedInfrastructureResponse{mutationResponseBase: mutationResponseBase{status: MutationStatusConfirmed}}
}

// NewRemoveManagedInfrastructureRejectedResponse constructs a rejected Remove response.
func NewRemoveManagedInfrastructureRejectedResponse(code MutationErrorCode) (RemoveManagedInfrastructureResponse, error) {
	if !validRemoveRejectedErrorCode(code) {
		return nil, mutationResponseError(MutationResponseErrorCodeSchemaRejected)
	}
	return &removeManagedInfrastructureResponse{mutationResponseBase: mutationResponseBase{
		status:       MutationStatusRejected,
		errorCode:    code,
		hasErrorCode: true,
	}}, nil
}

// NewRemoveManagedInfrastructureUnknownResponse constructs an unknown Remove response.
func NewRemoveManagedInfrastructureUnknownResponse() RemoveManagedInfrastructureResponse {
	return &removeManagedInfrastructureResponse{mutationResponseBase: mutationResponseBase{
		status:       MutationStatusUnknown,
		errorCode:    MutationErrorCodeUnknownResult,
		hasErrorCode: true,
	}}
}

func newApplyManagedPlanResponse(
	domain Domain,
	status MutationStatus,
	code MutationErrorCode,
	hasCode bool,
) (ApplyManagedPlanResponse, error) {
	if !validMutationDomain(domain) || !validApplyResponseState(status, code, hasCode) {
		return nil, mutationResponseError(MutationResponseErrorCodeSchemaRejected)
	}
	return &applyManagedPlanResponse{
		mutationResponseBase: mutationResponseBase{status: status, errorCode: code, hasErrorCode: hasCode},
		domain:               domain,
	}, nil
}

type applyMutationResponseWire struct {
	Version   int64              `json:"version"`
	Operation Operation          `json:"operation"`
	Status    MutationStatus     `json:"status"`
	Domain    Domain             `json:"domain"`
	ErrorCode *MutationErrorCode `json:"error_code,omitempty"`
}

type removeMutationResponseWire struct {
	Version   int64              `json:"version"`
	Operation Operation          `json:"operation"`
	Status    MutationStatus     `json:"status"`
	ErrorCode *MutationErrorCode `json:"error_code,omitempty"`
}

// EncodeMutationResponse encodes one validated response as compact deterministic JSON.
func EncodeMutationResponse(response MutationResponse) ([]byte, error) {
	var wire any
	switch value := response.(type) {
	case *applyManagedPlanResponse:
		if value == nil || !validMutationDomain(value.domain) ||
			!validApplyResponseState(value.status, value.errorCode, value.hasErrorCode) {
			return nil, mutationResponseError(MutationResponseErrorCodeSchemaRejected)
		}
		wire = applyMutationResponseWire{
			Version:   1,
			Operation: OperationApplyManagedPlan,
			Status:    value.status,
			Domain:    value.domain,
			ErrorCode: mutationErrorCodePointer(value.errorCode, value.hasErrorCode),
		}
	case *removeManagedInfrastructureResponse:
		if value == nil || !validRemoveResponseState(value.status, value.errorCode, value.hasErrorCode) {
			return nil, mutationResponseError(MutationResponseErrorCodeSchemaRejected)
		}
		wire = removeMutationResponseWire{
			Version:   1,
			Operation: OperationRemoveManagedInfrastructure,
			Status:    value.status,
			ErrorCode: mutationErrorCodePointer(value.errorCode, value.hasErrorCode),
		}
	default:
		return nil, mutationResponseError(MutationResponseErrorCodeSchemaRejected)
	}

	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, mutationResponseError(MutationResponseErrorCodeInvalidJSON)
	}
	if len(raw) > maxMutationResponseBytes {
		return nil, mutationResponseError(MutationResponseErrorCodeResponseTooLarge)
	}
	return raw, nil
}

func mutationErrorCodePointer(code MutationErrorCode, present bool) *MutationErrorCode {
	if !present {
		return nil
	}
	return &code
}

// DecodeMutationResponse decodes and validates one complete mutation response JSON payload.
func DecodeMutationResponse(raw []byte) (MutationResponse, error) {
	if len(raw) > maxMutationResponseBytes {
		return nil, mutationResponseError(MutationResponseErrorCodeResponseTooLarge)
	}
	if !utf8.Valid(raw) {
		return nil, mutationResponseError(MutationResponseErrorCodeInvalidUTF8)
	}
	if code := scanMutationResponseJSON(raw); code != "" {
		return nil, mutationResponseError(code)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, mutationResponseError(MutationResponseErrorCodeInvalidJSON)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, mutationResponseError(MutationResponseErrorCodeSchemaRejected)
	}
	response, ok := decodeMutationResponseObject(root)
	if !ok {
		return nil, mutationResponseError(MutationResponseErrorCodeSchemaRejected)
	}
	return response, nil
}

func decodeMutationResponseObject(root map[string]any) (MutationResponse, bool) {
	version, ok := positiveInt64(root["version"])
	if !ok || version != 1 {
		return nil, false
	}
	operationText, ok := root["operation"].(string)
	if !ok {
		return nil, false
	}
	statusText, ok := root["status"].(string)
	if !ok {
		return nil, false
	}
	status := MutationStatus(statusText)

	switch Operation(operationText) {
	case OperationApplyManagedPlan:
		return decodeApplyManagedPlanResponse(root, status)
	case OperationRemoveManagedInfrastructure:
		return decodeRemoveManagedInfrastructureResponse(root, status)
	default:
		return nil, false
	}
}

func decodeApplyManagedPlanResponse(root map[string]any, status MutationStatus) (MutationResponse, bool) {
	var code MutationErrorCode
	var hasCode bool
	switch status {
	case MutationStatusConfirmed:
		if !objectShape(root, "version", "operation", "status", "domain") {
			return nil, false
		}
	case MutationStatusRejected, MutationStatusUnknown:
		if !objectShape(root, "version", "operation", "status", "domain", "error_code") {
			return nil, false
		}
		codeText, ok := root["error_code"].(string)
		if !ok {
			return nil, false
		}
		code, hasCode = MutationErrorCode(codeText), true
	default:
		return nil, false
	}
	domainText, ok := root["domain"].(string)
	if !ok {
		return nil, false
	}
	response, err := newApplyManagedPlanResponse(Domain(domainText), status, code, hasCode)
	return response, err == nil
}

func decodeRemoveManagedInfrastructureResponse(root map[string]any, status MutationStatus) (MutationResponse, bool) {
	var code MutationErrorCode
	var hasCode bool
	switch status {
	case MutationStatusConfirmed:
		if !objectShape(root, "version", "operation", "status") {
			return nil, false
		}
	case MutationStatusRejected, MutationStatusUnknown:
		if !objectShape(root, "version", "operation", "status", "error_code") {
			return nil, false
		}
		codeText, ok := root["error_code"].(string)
		if !ok {
			return nil, false
		}
		code, hasCode = MutationErrorCode(codeText), true
	default:
		return nil, false
	}
	if !validRemoveResponseState(status, code, hasCode) {
		return nil, false
	}
	return &removeManagedInfrastructureResponse{mutationResponseBase: mutationResponseBase{
		status:       status,
		errorCode:    code,
		hasErrorCode: hasCode,
	}}, true
}

func validMutationDomain(domain Domain) bool {
	switch domain {
	case DomainInfrastructure, DomainPolicy, DomainTarget:
		return true
	default:
		return false
	}
}

func validApplyResponseState(status MutationStatus, code MutationErrorCode, hasCode bool) bool {
	switch status {
	case MutationStatusConfirmed:
		return !hasCode && code == ""
	case MutationStatusRejected:
		return hasCode && validApplyRejectedErrorCode(code)
	case MutationStatusUnknown:
		return hasCode && code == MutationErrorCodeUnknownResult
	default:
		return false
	}
}

func validRemoveResponseState(status MutationStatus, code MutationErrorCode, hasCode bool) bool {
	switch status {
	case MutationStatusConfirmed:
		return !hasCode && code == ""
	case MutationStatusRejected:
		return hasCode && validRemoveRejectedErrorCode(code)
	case MutationStatusUnknown:
		return hasCode && code == MutationErrorCodeUnknownResult
	default:
		return false
	}
}

func validApplyRejectedErrorCode(code MutationErrorCode) bool {
	switch code {
	case MutationErrorCodeInvalidPlan,
		MutationErrorCodeOwnershipConflict,
		MutationErrorCodeUnsupported,
		MutationErrorCodeNotReady,
		MutationErrorCodeBackendRejected:
		return true
	default:
		return false
	}
}

func validRemoveRejectedErrorCode(code MutationErrorCode) bool {
	switch code {
	case MutationErrorCodeOwnershipConflict,
		MutationErrorCodeUnsupported,
		MutationErrorCodeNotReady,
		MutationErrorCodeBackendRejected:
		return true
	default:
		return false
	}
}

func mutationResponseError(code MutationResponseErrorCode) error {
	return &MutationResponseError{code: code}
}

type mutationResponseScan struct {
	tokens int
}

func scanMutationResponseJSON(raw []byte) MutationResponseErrorCode {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &mutationResponseScan{}
	if code := scanMutationResponseValue(decoder, state, 0); code != "" {
		return code
	}
	if _, err := decoder.Token(); err != io.EOF {
		return MutationResponseErrorCodeInvalidJSON
	}
	return ""
}

func scanMutationResponseValue(
	decoder *json.Decoder,
	state *mutationResponseScan,
	parentDepth int,
) MutationResponseErrorCode {
	token, err := decoder.Token()
	if err != nil {
		return MutationResponseErrorCodeInvalidJSON
	}
	if code := countMutationResponseToken(state); code != "" {
		return code
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return ""
	}
	depth := parentDepth + 1
	if depth > maxMutationResponseDepth {
		return MutationResponseErrorCodeMaxDepthExceeded
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return MutationResponseErrorCodeInvalidJSON
			}
			if code := countMutationResponseToken(state); code != "" {
				return code
			}
			key, ok := keyToken.(string)
			if !ok {
				return MutationResponseErrorCodeInvalidJSON
			}
			if _, duplicate := seen[key]; duplicate {
				return MutationResponseErrorCodeDuplicateKey
			}
			seen[key] = struct{}{}
			if code := scanMutationResponseValue(decoder, state, depth); code != "" {
				return code
			}
		}
	case '[':
		for decoder.More() {
			if code := scanMutationResponseValue(decoder, state, depth); code != "" {
				return code
			}
		}
	default:
		return MutationResponseErrorCodeInvalidJSON
	}

	closing, err := decoder.Token()
	if err != nil {
		return MutationResponseErrorCodeInvalidJSON
	}
	if code := countMutationResponseToken(state); code != "" {
		return code
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return MutationResponseErrorCodeInvalidJSON
	}
	return ""
}

func countMutationResponseToken(state *mutationResponseScan) MutationResponseErrorCode {
	state.tokens++
	if state.tokens > maxMutationResponseTokens {
		return MutationResponseErrorCodeTokenLimitExceeded
	}
	return ""
}
