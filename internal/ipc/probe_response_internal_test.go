package ipc

import (
	"errors"
	"testing"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

func TestEncodeProbeCapabilitiesResponseRejectsNilAndForgedPrivateState(t *testing.T) {
	tests := []struct {
		name     string
		response ProbeCapabilitiesResponse
		code     ProbeCapabilitiesResponseErrorCode
	}{
		{name: "nil", response: nil, code: ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "typed nil success", response: (*probeCapabilitiesSuccessResponse)(nil), code: ProbeCapabilitiesResponseErrorCodeSemanticRejected},
		{name: "typed nil failure", response: (*probeCapabilitiesFailureResponse)(nil), code: ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "zero capabilities", response: &probeCapabilitiesSuccessResponse{capabilities: firewall.FirewallCapabilities{}}, code: ProbeCapabilitiesResponseErrorCodeSemanticRejected},
		{name: "unknown failure", response: &probeCapabilitiesFailureResponse{failureCode: "raw"}, code: ProbeCapabilitiesResponseErrorCodeSchemaRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := EncodeProbeCapabilitiesResponse(test.response)
			if raw != nil {
				t.Fatalf("raw = %q, want nil", raw)
			}
			var typed *ProbeCapabilitiesResponseError
			if !errors.As(err, &typed) || typed.Code() != test.code {
				t.Fatalf("error = %T/%v, want %q", err, err, test.code)
			}
		})
	}
}
