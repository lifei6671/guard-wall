package ipc

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestMutationRequestFrameForgedInternalStateWritesNothing(t *testing.T) {
	digest := strings.Repeat("a", 64)
	base := planBase{
		ownerVersion:        ownerVersionV1,
		basisSnapshotDigest: digest,
		revision:            1,
	}
	tests := []struct {
		name    string
		request MutationRequest
		code    ErrorCode
	}{
		{
			name: "foreign infrastructure owner",
			request: &applyManagedPlanRequest{plan: &infrastructurePlan{
				planBase: planBase{
					ownerVersion:        "forged-owner-secret",
					basisSnapshotDigest: digest,
					revision:            1,
				},
				schemaVersion: 1,
			}},
			code: ErrorCodeSchemaRejected,
		},
		{
			name: "hidden target expiry",
			request: &applyManagedPlanRequest{plan: &targetPlan{
				planBase:                base,
				target:                  netip.MustParsePrefix("192.0.2.4/32"),
				membership:              MembershipPresent,
				timeoutMode:             TimeoutModeNone,
				effectiveUntilUnixMicro: 1,
				scopes:                  []Scope{ScopeInput},
			}},
			code: ErrorCodeInvalidTimeout,
		},
		{
			name: "foreign removal owner",
			request: &removeManagedInfrastructureRequest{
				expectedOwnerVersion: "forged-owner-secret",
			},
			code: ErrorCodeSchemaRejected,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &mutationRequestFrameInternalWriter{}
			err := WriteMutationRequestFrame(writer, test.request)
			var validationError *ValidationError
			if !errors.As(err, &validationError) {
				t.Fatalf("WriteMutationRequestFrame() error = %T, want *ValidationError", err)
			}
			if got := validationError.Code(); got != test.code {
				t.Fatalf("WriteMutationRequestFrame() code = %q, want %q", got, test.code)
			}
			if writer.writeCalls != 0 || writer.Len() != 0 {
				t.Fatalf("forged request wrote %d bytes in %d calls, want zero writes", writer.Len(), writer.writeCalls)
			}
			if strings.Contains(err.Error(), "forged-owner-secret") {
				t.Fatalf("WriteMutationRequestFrame() leaked forged state: %q", err)
			}
		})
	}
}

type mutationRequestFrameInternalWriter struct {
	bytes.Buffer
	writeCalls int
}

func (w *mutationRequestFrameInternalWriter) Write(contents []byte) (int, error) {
	w.writeCalls++
	return w.Buffer.Write(contents)
}
