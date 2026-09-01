package ipc

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestEncodeMutationRequestRejectsInvalidInternalState(t *testing.T) {
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
			name:    "nil plan",
			request: &applyManagedPlanRequest{},
			code:    ErrorCodeSchemaRejected,
		},
		{
			name: "typed nil plan",
			request: &applyManagedPlanRequest{
				plan: (*infrastructurePlan)(nil),
			},
			code: ErrorCodeSchemaRejected,
		},
		{
			name: "infrastructure owner",
			request: &applyManagedPlanRequest{plan: &infrastructurePlan{
				planBase:      planBase{ownerVersion: "foreign/v1", basisSnapshotDigest: digest, revision: 1},
				schemaVersion: 1,
			}},
			code: ErrorCodeSchemaRejected,
		},
		{
			name: "infrastructure schema version",
			request: &applyManagedPlanRequest{plan: &infrastructurePlan{
				planBase:      base,
				schemaVersion: 2,
			}},
			code: ErrorCodeSchemaRejected,
		},
		{
			name: "policy owner",
			request: &applyManagedPlanRequest{plan: &policyPlan{
				planBase:         planBase{ownerVersion: "foreign/v1", basisSnapshotDigest: digest, revision: 1},
				protectedTargets: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
			}},
			code: ErrorCodeSchemaRejected,
		},
		{
			name: "target hidden expiry",
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
			name: "remove owner",
			request: &removeManagedInfrastructureRequest{
				expectedOwnerVersion: "foreign/v1",
			},
			code: ErrorCodeSchemaRejected,
		},
		{
			name: "typed nil policy plan",
			request: &applyManagedPlanRequest{
				plan: (*policyPlan)(nil),
			},
			code: ErrorCodeSchemaRejected,
		},
		{
			name: "typed nil target plan",
			request: &applyManagedPlanRequest{
				plan: (*targetPlan)(nil),
			},
			code: ErrorCodeSchemaRejected,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := EncodeMutationRequest(test.request)
			if len(raw) != 0 {
				t.Fatalf("EncodeMutationRequest() bytes = %q, want empty", raw)
			}
			var validationError *ValidationError
			if !errors.As(err, &validationError) {
				t.Fatalf("EncodeMutationRequest() error = %T, want *ValidationError", err)
			}
			if got := validationError.Code(); got != test.code {
				t.Fatalf("EncodeMutationRequest() code = %q, want %q", got, test.code)
			}
			if strings.Contains(err.Error(), "foreign/v1") {
				t.Fatalf("EncodeMutationRequest() leaked forged owner: %q", err)
			}
		})
	}
}
