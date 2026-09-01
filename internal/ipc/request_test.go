package ipc_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

const (
	maxRequestBytes = 64 * 1024
	maxDepth        = 8
	maxTokens       = 4096
	maxPolicyPrefix = 1024
)

type goldenCases struct {
	Valid   []string            `json:"valid"`
	Invalid []invalidGoldenCase `json:"invalid"`
}

type invalidGoldenCase struct {
	Path      string `json:"path"`
	Layer     string `json:"layer"`
	ErrorCode string `json:"error_code"`
}

func TestContractConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "operation/probe", got: string(ipc.OperationProbeCapabilities), want: "ProbeCapabilities"},
		{name: "operation/snapshot", got: string(ipc.OperationSnapshotManaged), want: "SnapshotManaged"},
		{name: "operation/apply", got: string(ipc.OperationApplyManagedPlan), want: "ApplyManagedPlan"},
		{name: "operation/remove", got: string(ipc.OperationRemoveManagedInfrastructure), want: "RemoveManagedInfrastructure"},
		{name: "domain/infrastructure", got: string(ipc.DomainInfrastructure), want: "infrastructure"},
		{name: "domain/policy", got: string(ipc.DomainPolicy), want: "policy"},
		{name: "domain/target", got: string(ipc.DomainTarget), want: "target"},
		{name: "membership/present", got: string(ipc.MembershipPresent), want: "present"},
		{name: "membership/absent", got: string(ipc.MembershipAbsent), want: "absent"},
		{name: "timeout/none", got: string(ipc.TimeoutModeNone), want: "none"},
		{name: "timeout/native", got: string(ipc.TimeoutModeNative), want: "native"},
		{name: "scope/input", got: string(ipc.ScopeInput), want: "input"},
		{name: "scope/forward", got: string(ipc.ScopeForward), want: "forward"},
		{name: "error/request too large", got: string(ipc.ErrorCodeRequestTooLarge), want: "request_too_large"},
		{name: "error/invalid UTF-8", got: string(ipc.ErrorCodeInvalidUTF8), want: "invalid_utf8"},
		{name: "error/duplicate key", got: string(ipc.ErrorCodeDuplicateKey), want: "duplicate_key"},
		{name: "error/max depth", got: string(ipc.ErrorCodeMaxDepthExceeded), want: "max_depth_exceeded"},
		{name: "error/token limit", got: string(ipc.ErrorCodeTokenLimitExceeded), want: "token_limit_exceeded"},
		{name: "error/invalid JSON", got: string(ipc.ErrorCodeInvalidJSON), want: "invalid_json"},
		{name: "error/schema", got: string(ipc.ErrorCodeSchemaRejected), want: "schema_rejected"},
		{name: "error/prefix limit", got: string(ipc.ErrorCodePrefixLimit), want: "prefix_limit"},
		{name: "error/noncanonical prefix", got: string(ipc.ErrorCodeNoncanonicalPrefix), want: "noncanonical_prefix"},
		{name: "error/noncanonical order", got: string(ipc.ErrorCodeNoncanonicalOrder), want: "noncanonical_order"},
		{name: "error/protected policy", got: string(ipc.ErrorCodeProtectedPolicyMissing), want: "protected_policy_missing"},
		{name: "error/invalid timeout", got: string(ipc.ErrorCodeInvalidTimeout), want: "invalid_timeout"},
		{name: "error/protected target", got: string(ipc.ErrorCodeProtectedTarget), want: "protected_target"},
		{name: "error/invalid scope", got: string(ipc.ErrorCodeInvalidScope), want: "invalid_scope"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.got; got != test.want {
				t.Fatalf("constant = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDecodeRequestTargetVariants(t *testing.T) {
	request, err := ipc.DecodeRequest(targetRequest(t, []string{"input", "forward"}))
	if err != nil {
		t.Fatalf("canonical absent target rejected: %v", err)
	}
	plan := request.(ipc.ApplyManagedPlanRequest).Plan().(ipc.TargetPlan)
	if plan.Membership() != ipc.MembershipAbsent || plan.TimeoutMode() != ipc.TimeoutModeNone {
		t.Fatalf("membership/timeout = %q/%q, want absent/none", plan.Membership(), plan.TimeoutMode())
	}
	if expiry, ok := plan.EffectiveUntilUnixMicro(); ok || expiry != 0 {
		t.Fatalf("null expiry = %d, %v, want 0, false", expiry, ok)
	}
	if got := plan.Scopes(); len(got) != 2 || got[0] != ipc.ScopeInput || got[1] != ipc.ScopeForward {
		t.Fatalf("scopes = %#v, want [input forward]", got)
	}

	request, err = ipc.DecodeRequest(targetRequest(t, []string{"forward", "input"}))
	if request != nil {
		t.Fatalf("reversed scopes request = %#v, want nil", request)
	}
	assertErrorCode(t, err, ipc.ErrorCodeInvalidScope)
}

func TestDecodeRequestIntegerSemantics(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		code ipc.ErrorCode
	}{
		{name: "decimal integer", raw: `{"version":1.0,"operation":"ProbeCapabilities","payload":{}}`},
		{name: "exponent integer", raw: `{"version":1e0,"operation":"ProbeCapabilities","payload":{}}`},
		{name: "fractional", raw: `{"version":1.5,"operation":"ProbeCapabilities","payload":{}}`, code: ipc.ErrorCodeSchemaRejected},
		{name: "zero", raw: `{"version":0e9,"operation":"ProbeCapabilities","payload":{}}`, code: ipc.ErrorCodeSchemaRejected},
		{name: "overflow", raw: `{"version":9223372036854775808,"operation":"ProbeCapabilities","payload":{}}`, code: ipc.ErrorCodeSchemaRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := ipc.DecodeRequest([]byte(test.raw))
			if test.code == "" {
				if err != nil || request.Operation() != ipc.OperationProbeCapabilities {
					t.Fatalf("DecodeRequest() = %T, %v", request, err)
				}
				return
			}
			if request != nil {
				t.Fatalf("DecodeRequest() request = %#v, want nil", request)
			}
			assertErrorCode(t, err, test.code)
		})
	}

	raw := []byte(`{"version":1,"operation":"ApplyManagedPlan","payload":{"domain":"infrastructure","owner_version":"guard/v1","basis_snapshot_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","infrastructure_revision":1e0,"operations":[{"kind":"ensure_infrastructure","schema_version":1.0}]}}`)
	request, err := ipc.DecodeRequest(raw)
	if err != nil {
		t.Fatalf("mathematical integer plan rejected: %v", err)
	}
	plan := request.(ipc.ApplyManagedPlanRequest).Plan().(ipc.InfrastructurePlan)
	if plan.Revision() != 1 || plan.SchemaVersion() != 1 {
		t.Fatalf("revision/schema = %d/%d, want 1/1", plan.Revision(), plan.SchemaVersion())
	}
}

func TestDecodeRequestGoldenVectors(t *testing.T) {
	cases := readGoldenCases(t)
	if got, want := len(cases.Valid), 6; got != want {
		t.Fatalf("valid case count = %d, want %d", got, want)
	}
	if got, want := len(cases.Invalid), 23; got != want {
		t.Fatalf("invalid case count = %d, want %d", got, want)
	}

	for _, name := range cases.Valid {
		name := name
		t.Run("valid/"+strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), func(t *testing.T) {
			request, err := ipc.DecodeRequest(readGoldenFile(t, name))
			if err != nil {
				t.Fatalf("DecodeRequest() error = %v", err)
			}
			assertValidGoldenRequest(t, name, request)
		})
	}

	for _, test := range cases.Invalid {
		test := test
		t.Run("invalid/"+strings.TrimSuffix(filepath.Base(test.Path), filepath.Ext(test.Path)), func(t *testing.T) {
			request, err := ipc.DecodeRequest(readGoldenFile(t, test.Path))
			if request != nil {
				t.Fatalf("DecodeRequest() request = %#v, want nil", request)
			}
			var wantCode ipc.ErrorCode
			switch test.Layer {
			case "schema":
				if test.ErrorCode != "" {
					t.Fatalf("schema case freezes unexpected error code %q", test.ErrorCode)
				}
				wantCode = ipc.ErrorCodeSchemaRejected
			case "decoder", "semantic":
				if test.ErrorCode == "" {
					t.Fatalf("%s case has no error code", test.Layer)
				}
				wantCode = ipc.ErrorCode(test.ErrorCode)
			default:
				t.Fatalf("unknown rejection layer %q", test.Layer)
			}
			assertErrorCode(t, err, wantCode)
		})
	}
}

func TestDecodeRequestRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		code ipc.ErrorCode
	}{
		{name: "invalid UTF-8", raw: []byte{'{', '"', 0xff, '"', ':', '1', '}'}, code: ipc.ErrorCodeInvalidUTF8},
		{name: "multiple JSON values", raw: []byte(`{"version":1,"operation":"ProbeCapabilities","payload":{}} {}`), code: ipc.ErrorCodeInvalidJSON},
		{name: "malformed JSON", raw: []byte(`{"version":1`), code: ipc.ErrorCodeInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := ipc.DecodeRequest(test.raw)
			if request != nil {
				t.Fatalf("DecodeRequest() request = %#v, want nil", request)
			}
			assertErrorCode(t, err, test.code)
		})
	}
}

func TestDecodeRequestResourceLimits(t *testing.T) {
	t.Run("request bytes", func(t *testing.T) {
		base := readGoldenFile(t, "valid/probe-capabilities.json")
		exact := append(append([]byte(nil), base...), bytes.Repeat([]byte(" "), maxRequestBytes-len(base))...)
		if _, err := ipc.DecodeRequest(exact); err != nil {
			t.Fatalf("exact request limit rejected: %v", err)
		}
		request, err := ipc.DecodeRequest(append(exact, ' '))
		if request != nil {
			t.Fatalf("one-over request = %#v, want nil", request)
		}
		assertErrorCode(t, err, ipc.ErrorCodeRequestTooLarge)
	})

	t.Run("depth", func(t *testing.T) {
		_, err := ipc.DecodeRequest(nestedArrays(maxDepth))
		assertErrorCode(t, err, ipc.ErrorCodeSchemaRejected)
		_, err = ipc.DecodeRequest(nestedArrays(maxDepth + 1))
		assertErrorCode(t, err, ipc.ErrorCodeMaxDepthExceeded)
	})

	t.Run("tokens", func(t *testing.T) {
		_, err := ipc.DecodeRequest(nullArray(maxTokens - 2))
		assertErrorCode(t, err, ipc.ErrorCodeSchemaRejected)
		_, err = ipc.DecodeRequest(nullArray(maxTokens - 1))
		assertErrorCode(t, err, ipc.ErrorCodeTokenLimitExceeded)
	})

	t.Run("policy prefixes", func(t *testing.T) {
		exact := policyRequest(t, maxPolicyPrefix-2)
		request, err := ipc.DecodeRequest(exact)
		if err != nil {
			t.Fatalf("exact prefix limit rejected: %v", err)
		}
		plan := request.(ipc.ApplyManagedPlanRequest).Plan().(ipc.PolicyPlan)
		if got := len(plan.Allowlist()) + len(plan.ProtectedTargets()); got != maxPolicyPrefix {
			t.Fatalf("accepted prefix count = %d, want %d", got, maxPolicyPrefix)
		}

		_, err = ipc.DecodeRequest(policyRequest(t, maxPolicyPrefix-1))
		assertErrorCode(t, err, ipc.ErrorCodePrefixLimit)
	})
}

func TestDecodeRequestDoesNotEchoInput(t *testing.T) {
	const marker = "do-not-echo-secret-7b5ea2c8"
	raw := []byte(`{"version":1,"operation":"ProbeCapabilities","payload":{},"unknown":"` + marker + `"}`)
	_, err := ipc.DecodeRequest(raw)
	assertErrorCode(t, err, ipc.ErrorCodeSchemaRejected)
	if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), string(raw)) {
		t.Fatalf("validation error echoed attacker-controlled input: %q", err)
	}
}

func FuzzDecodeRequestClosedUnion(f *testing.F) {
	for _, name := range []string{
		"valid/probe-capabilities.json",
		"valid/snapshot-managed.json",
		"valid/apply-target.json",
		"valid/remove-managed-infrastructure.json",
		"invalid/command-field.json",
		"invalid/duplicate-key.json",
	} {
		f.Add(readGoldenFile(f, name))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		request, err := ipc.DecodeRequest(raw)
		if err != nil {
			if request != nil {
				t.Fatalf("rejected input returned request %#v", request)
			}
			var validationError *ipc.ValidationError
			if !errors.As(err, &validationError) {
				t.Fatalf("DecodeRequest() error type = %T, want *ipc.ValidationError", err)
			}
			return
		}
		assertClosedRequestUnion(t, request)
	})
}

func assertValidGoldenRequest(t *testing.T, name string, request ipc.Request) {
	t.Helper()
	switch name {
	case "valid/probe-capabilities.json":
		if request.Operation() != ipc.OperationProbeCapabilities {
			t.Fatalf("operation = %q", request.Operation())
		}
		if _, ok := request.(ipc.ProbeCapabilitiesRequest); !ok {
			t.Fatalf("request type = %T, want ipc.ProbeCapabilitiesRequest", request)
		}
	case "valid/snapshot-managed.json":
		if request.Operation() != ipc.OperationSnapshotManaged {
			t.Fatalf("operation = %q", request.Operation())
		}
		if _, ok := request.(ipc.SnapshotManagedRequest); !ok {
			t.Fatalf("request type = %T, want ipc.SnapshotManagedRequest", request)
		}
	case "valid/remove-managed-infrastructure.json":
		remove, ok := request.(ipc.RemoveManagedInfrastructureRequest)
		if !ok {
			t.Fatalf("request type = %T, want *ipc.RemoveManagedInfrastructureRequest", request)
		}
		if request.Operation() != ipc.OperationRemoveManagedInfrastructure || remove.ExpectedOwnerVersion() != "guard/v1" {
			t.Fatalf("remove request = operation %q, owner %q", request.Operation(), remove.ExpectedOwnerVersion())
		}
	case "valid/apply-infrastructure.json":
		apply := assertApplyRequest(t, request, ipc.DomainInfrastructure, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		plan, ok := apply.Plan().(ipc.InfrastructurePlan)
		if !ok {
			t.Fatalf("plan type = %T, want *ipc.InfrastructurePlan", apply.Plan())
		}
		if plan.Revision() != 1 || plan.SchemaVersion() != 1 {
			t.Fatalf("plan revision/schema = %d/%d, want 1/1", plan.Revision(), plan.SchemaVersion())
		}
	case "valid/apply-policy.json":
		apply := assertApplyRequest(t, request, ipc.DomainPolicy, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		plan, ok := apply.Plan().(ipc.PolicyPlan)
		if !ok {
			t.Fatalf("plan type = %T, want *ipc.PolicyPlan", apply.Plan())
		}
		assertPrefixes(t, plan.Allowlist(), []string{"198.51.100.0/24"})
		assertPrefixes(t, plan.ProtectedTargets(), []string{"127.0.0.0/8", "::1/128"})
		allowlist := plan.Allowlist()
		allowlist[0] = netip.MustParsePrefix("203.0.113.0/24")
		assertPrefixes(t, plan.Allowlist(), []string{"198.51.100.0/24"})
		protected := plan.ProtectedTargets()
		protected[0] = netip.MustParsePrefix("203.0.113.0/24")
		assertPrefixes(t, plan.ProtectedTargets(), []string{"127.0.0.0/8", "::1/128"})
	case "valid/apply-target.json":
		apply := assertApplyRequest(t, request, ipc.DomainTarget, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
		plan, ok := apply.Plan().(ipc.TargetPlan)
		if !ok {
			t.Fatalf("plan type = %T, want *ipc.TargetPlan", apply.Plan())
		}
		if got, want := plan.Target(), netip.MustParsePrefix("192.0.2.4/32"); got != want {
			t.Fatalf("target = %v, want %v", got, want)
		}
		if plan.Membership() != ipc.MembershipPresent || plan.TimeoutMode() != ipc.TimeoutModeNative {
			t.Fatalf("target membership/timeout = %q/%q", plan.Membership(), plan.TimeoutMode())
		}
		if expiry, ok := plan.EffectiveUntilUnixMicro(); !ok || expiry != 1700000000000000 {
			t.Fatalf("expiry = %d, %v", expiry, ok)
		}
		if got := plan.Scopes(); len(got) != 1 || got[0] != ipc.ScopeInput {
			t.Fatalf("scopes = %#v, want [input]", got)
		}
		scopes := plan.Scopes()
		scopes[0] = ipc.ScopeForward
		if got := plan.Scopes(); len(got) != 1 || got[0] != ipc.ScopeInput {
			t.Fatalf("Scopes() exposed mutable storage: %#v", got)
		}
	default:
		t.Fatalf("unhandled valid golden %q", name)
	}
}

func assertApplyRequest(t *testing.T, request ipc.Request, domain ipc.Domain, digest string) ipc.ApplyManagedPlanRequest {
	t.Helper()
	apply, ok := request.(ipc.ApplyManagedPlanRequest)
	if !ok {
		t.Fatalf("request type = %T, want *ipc.ApplyManagedPlanRequest", request)
	}
	if request.Operation() != ipc.OperationApplyManagedPlan {
		t.Fatalf("operation = %q, want %q", request.Operation(), ipc.OperationApplyManagedPlan)
	}
	plan := apply.Plan()
	if plan.Domain() != domain || plan.OwnerVersion() != "guard/v1" || plan.BasisSnapshotDigest() != digest || plan.Revision() != 1 {
		t.Fatalf("plan metadata = domain %q, owner %q, digest %q, revision %d", plan.Domain(), plan.OwnerVersion(), plan.BasisSnapshotDigest(), plan.Revision())
	}
	return apply
}

func assertClosedRequestUnion(t *testing.T, request ipc.Request) {
	t.Helper()
	switch request.Operation() {
	case ipc.OperationProbeCapabilities:
		if _, ok := request.(ipc.ProbeCapabilitiesRequest); !ok {
			t.Fatalf("operation %q has request type %T", request.Operation(), request)
		}
	case ipc.OperationSnapshotManaged:
		if _, ok := request.(ipc.SnapshotManagedRequest); !ok {
			t.Fatalf("operation %q has request type %T", request.Operation(), request)
		}
	case ipc.OperationApplyManagedPlan:
		apply, ok := request.(ipc.ApplyManagedPlanRequest)
		if !ok {
			t.Fatalf("operation %q has request type %T", request.Operation(), request)
		}
		switch apply.Plan().Domain() {
		case ipc.DomainInfrastructure:
			if _, ok := apply.Plan().(ipc.InfrastructurePlan); !ok {
				t.Fatalf("domain %q has plan type %T", apply.Plan().Domain(), apply.Plan())
			}
		case ipc.DomainPolicy:
			if _, ok := apply.Plan().(ipc.PolicyPlan); !ok {
				t.Fatalf("domain %q has plan type %T", apply.Plan().Domain(), apply.Plan())
			}
		case ipc.DomainTarget:
			if _, ok := apply.Plan().(ipc.TargetPlan); !ok {
				t.Fatalf("domain %q has plan type %T", apply.Plan().Domain(), apply.Plan())
			}
		default:
			t.Fatalf("accepted domain %q outside closed union", apply.Plan().Domain())
		}
	case ipc.OperationRemoveManagedInfrastructure:
		if _, ok := request.(ipc.RemoveManagedInfrastructureRequest); !ok {
			t.Fatalf("operation %q has request type %T", request.Operation(), request)
		}
	default:
		t.Fatalf("accepted operation %q outside closed union", request.Operation())
	}
}

func assertErrorCode(t *testing.T, err error, want ipc.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("DecodeRequest() error = nil, want code %q", want)
	}
	var validationError *ipc.ValidationError
	if !errors.As(err, &validationError) {
		t.Fatalf("DecodeRequest() error type = %T, want *ipc.ValidationError", err)
	}
	if validationError.Code() == "" {
		t.Fatal("DecodeRequest() returned an unclassified validation error")
	}
	if got := validationError.Code(); got != want {
		t.Fatalf("error code = %q, want %q (error: %v)", got, want, err)
	}
}

func assertPrefixes(t *testing.T, got []netip.Prefix, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("prefix count = %d, want %d", len(got), len(want))
	}
	for index, value := range got {
		if value.String() != want[index] {
			t.Fatalf("prefix[%d] = %q, want %q", index, value, want[index])
		}
	}
}

func readGoldenCases(t *testing.T) goldenCases {
	t.Helper()
	var cases goldenCases
	if err := json.Unmarshal(readGoldenFile(t, "cases.json"), &cases); err != nil {
		t.Fatalf("decode cases.json: %v", err)
	}
	return cases
}

func readGoldenFile(tb testing.TB, name string) []byte {
	tb.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "schema", "testdata", "ipc-v1", filepath.FromSlash(name)))
	if err != nil {
		tb.Fatalf("read golden %s: %v", name, err)
	}
	return contents
}

func policyRequest(t *testing.T, allowlistCount int) []byte {
	t.Helper()
	allowlist := make([]string, 0, allowlistCount)
	for index := 0; index < allowlistCount; index++ {
		address := netip.AddrFrom4([4]byte{10, byte(index >> 16), byte(index >> 8), byte(index)})
		allowlist = append(allowlist, address.String()+"/32")
	}
	sort.Strings(allowlist)
	request := map[string]any{
		"version":   1,
		"operation": "ApplyManagedPlan",
		"payload": map[string]any{
			"domain":                "policy",
			"owner_version":         "guard/v1",
			"basis_snapshot_digest": strings.Repeat("d", 64),
			"policy_revision":       1,
			"operations": []any{map[string]any{
				"kind":              "replace_policy",
				"allowlist":         allowlist,
				"protected_targets": []string{"127.0.0.0/8", "::1/128"},
			}},
		},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func targetRequest(t *testing.T, scopes []string) []byte {
	t.Helper()
	request := map[string]any{
		"version":   1,
		"operation": "ApplyManagedPlan",
		"payload": map[string]any{
			"domain":                "target",
			"owner_version":         "guard/v1",
			"basis_snapshot_digest": strings.Repeat("e", 64),
			"target_generation":     2,
			"operations": []any{map[string]any{
				"kind":                    "set_target",
				"target":                  "198.51.100.8/32",
				"membership":              "absent",
				"timeout_mode":            "none",
				"effective_until_unix_us": nil,
				"scopes":                  scopes,
			}},
		},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func nestedArrays(depth int) []byte {
	return []byte(strings.Repeat("[", depth) + "null" + strings.Repeat("]", depth))
}

func nullArray(items int) []byte {
	if items == 0 {
		return []byte("[]")
	}
	return []byte("[" + strings.Repeat("null,", items-1) + "null]")
}
