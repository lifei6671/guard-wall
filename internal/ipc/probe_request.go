package ipc

import "encoding/json"

type probeCapabilitiesRequestWire struct {
	Version   int64     `json:"version"`
	Operation Operation `json:"operation"`
	Payload   struct{}  `json:"payload"`
}

// NewProbeCapabilitiesRequest constructs the fixed empty IPC v1 capability
// probe request. Callers cannot supply commands, paths, environment variables,
// physical object names, or any other backend-controlled value.
func NewProbeCapabilitiesRequest() ProbeCapabilitiesRequest {
	return &probeCapabilitiesRequest{}
}

// EncodeProbeCapabilitiesRequest validates and deterministically encodes the
// fixed IPC v1 capability probe request as compact JSON.
func EncodeProbeCapabilitiesRequest(request ProbeCapabilitiesRequest) ([]byte, error) {
	typed, ok := request.(*probeCapabilitiesRequest)
	if !ok || typed == nil {
		return nil, validationError(ErrorCodeSchemaRejected)
	}

	raw, err := json.Marshal(probeCapabilitiesRequestWire{
		Version:   1,
		Operation: OperationProbeCapabilities,
	})
	if err != nil {
		return nil, validationError(ErrorCodeInvalidJSON)
	}
	if len(raw) > maxRequestBytes {
		return nil, validationError(ErrorCodeRequestTooLarge)
	}
	return raw, nil
}
