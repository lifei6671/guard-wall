package ipc

import "encoding/json"

type snapshotManagedRequestWire struct {
	Version   int64     `json:"version"`
	Operation Operation `json:"operation"`
	Payload   struct{}  `json:"payload"`
}

// NewSnapshotManagedRequest constructs the fixed empty IPC v1 managed
// snapshot request. Callers cannot select physical Firewall objects or supply
// backend-controlled values.
func NewSnapshotManagedRequest() SnapshotManagedRequest {
	return &snapshotManagedRequest{}
}

// EncodeSnapshotManagedRequest validates and deterministically encodes the
// fixed IPC v1 managed snapshot request as compact JSON.
func EncodeSnapshotManagedRequest(request SnapshotManagedRequest) ([]byte, error) {
	typed, ok := request.(*snapshotManagedRequest)
	if !ok || typed == nil {
		return nil, validationError(ErrorCodeSchemaRejected)
	}

	raw, err := json.Marshal(snapshotManagedRequestWire{
		Version:   1,
		Operation: OperationSnapshotManaged,
	})
	if err != nil {
		return nil, validationError(ErrorCodeInvalidJSON)
	}
	if len(raw) > maxRequestBytes {
		return nil, validationError(ErrorCodeRequestTooLarge)
	}
	return raw, nil
}
