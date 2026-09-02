package ipc

import (
	"errors"
	"testing"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

func TestEncodeSnapshotManagedResponseRejectsNilAndForgedState(t *testing.T) {
	tests := []struct {
		name     string
		response SnapshotManagedResponse
		code     SnapshotManagedResponseErrorCode
	}{
		{name: "nil", response: nil, code: SnapshotManagedResponseErrorCodeSchemaRejected},
		{name: "typed nil success", response: (*snapshotManagedSuccessResponse)(nil), code: SnapshotManagedResponseErrorCodeSemanticRejected},
		{name: "typed nil failure", response: (*snapshotManagedFailureResponse)(nil), code: SnapshotManagedResponseErrorCodeSchemaRejected},
		{name: "zero snapshot", response: &snapshotManagedSuccessResponse{snapshot: firewall.ManagedSnapshot{}}, code: SnapshotManagedResponseErrorCodeSemanticRejected},
		{name: "unknown failure", response: &snapshotManagedFailureResponse{failureCode: "raw"}, code: SnapshotManagedResponseErrorCodeSchemaRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := EncodeSnapshotManagedResponse(test.response)
			if raw != nil {
				t.Fatalf("raw = %q, want nil", raw)
			}
			var typed *SnapshotManagedResponseError
			if !errors.As(err, &typed) || typed.Code() != test.code {
				t.Fatalf("error = %T/%v, want %q", err, err, test.code)
			}
		})
	}
}
