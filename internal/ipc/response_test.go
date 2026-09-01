package ipc_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

type mutationResponseCases struct {
	Valid   []mutationResponseCase `json:"valid"`
	Invalid []mutationResponseCase `json:"invalid"`
}

type mutationResponseCase struct {
	Path            string `json:"path"`
	Operation       string `json:"operation"`
	Status          string `json:"status"`
	Layer           string `json:"layer"`
	Classification  string `json:"classification"`
	FixtureEncoding string `json:"fixture_encoding"`
}

type mutationResponseWire struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Domain    string `json:"domain"`
	ErrorCode string `json:"error_code"`
}

func TestMutationResponseConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "status confirmed", got: string(ipc.MutationStatusConfirmed), want: "confirmed"},
		{name: "status rejected", got: string(ipc.MutationStatusRejected), want: "rejected"},
		{name: "status unknown", got: string(ipc.MutationStatusUnknown), want: "unknown"},
		{name: "wire error invalid plan", got: string(ipc.MutationErrorCodeInvalidPlan), want: "invalid_plan"},
		{name: "wire error ownership conflict", got: string(ipc.MutationErrorCodeOwnershipConflict), want: "ownership_conflict"},
		{name: "wire error unsupported", got: string(ipc.MutationErrorCodeUnsupported), want: "unsupported"},
		{name: "wire error not ready", got: string(ipc.MutationErrorCodeNotReady), want: "not_ready"},
		{name: "wire error backend rejected", got: string(ipc.MutationErrorCodeBackendRejected), want: "backend_rejected"},
		{name: "wire error unknown result", got: string(ipc.MutationErrorCodeUnknownResult), want: "unknown_result"},
		{name: "codec response too large", got: string(ipc.MutationResponseErrorCodeResponseTooLarge), want: "response_too_large"},
		{name: "codec invalid UTF-8", got: string(ipc.MutationResponseErrorCodeInvalidUTF8), want: "invalid_utf8"},
		{name: "codec duplicate key", got: string(ipc.MutationResponseErrorCodeDuplicateKey), want: "duplicate_key"},
		{name: "codec max depth", got: string(ipc.MutationResponseErrorCodeMaxDepthExceeded), want: "max_depth_exceeded"},
		{name: "codec token limit", got: string(ipc.MutationResponseErrorCodeTokenLimitExceeded), want: "token_limit_exceeded"},
		{name: "codec invalid JSON", got: string(ipc.MutationResponseErrorCodeInvalidJSON), want: "invalid_json"},
		{name: "codec schema rejected", got: string(ipc.MutationResponseErrorCodeSchemaRejected), want: "schema_rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("constant = %q, want %q", test.got, test.want)
			}
		})
	}
}

func TestMutationResponseGoldenVectors(t *testing.T) {
	cases := readMutationResponseCases(t)
	if got, want := len(cases.Valid), 12; got != want {
		t.Fatalf("valid mutation response cases = %d, want %d", got, want)
	}
	if got, want := len(cases.Invalid), 28; got != want {
		t.Fatalf("invalid mutation response cases = %d, want %d", got, want)
	}

	for _, test := range cases.Valid {
		test := test
		t.Run("valid/"+strings.TrimSuffix(filepath.Base(test.Path), filepath.Ext(test.Path)), func(t *testing.T) {
			raw := readMutationResponseFixture(t, test)
			var wire mutationResponseWire
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatalf("decode valid fixture expectation: %v", err)
			}
			response, err := ipc.DecodeMutationResponse(raw)
			if err != nil {
				t.Fatalf("DecodeMutationResponse(): %v", err)
			}
			assertMutationResponseGetters(t, response, wire)

			encoded, err := ipc.EncodeMutationResponse(response)
			if err != nil {
				t.Fatalf("EncodeMutationResponse(decoded): %v", err)
			}
			want := bytes.TrimSpace(raw)
			if !bytes.Equal(encoded, want) {
				t.Fatalf("canonical response = %s, want %s", encoded, want)
			}
		})
	}

	for _, test := range cases.Invalid {
		test := test
		t.Run("invalid/"+strings.TrimSuffix(filepath.Base(test.Path), filepath.Ext(test.Path)), func(t *testing.T) {
			raw := readMutationResponseFixture(t, test)
			response, err := ipc.DecodeMutationResponse(raw)
			if response != nil {
				t.Fatalf("DecodeMutationResponse() response = %#v, want nil", response)
			}
			assertMutationResponseErrorCode(
				t, err, ipc.MutationResponseErrorCode(test.Classification),
			)
		})
	}
}

func TestMutationResponseConstructors(t *testing.T) {
	domains := []ipc.Domain{
		ipc.DomainInfrastructure,
		ipc.DomainPolicy,
		ipc.DomainTarget,
	}
	rejectedCodes := []ipc.MutationErrorCode{
		ipc.MutationErrorCodeInvalidPlan,
		ipc.MutationErrorCodeOwnershipConflict,
		ipc.MutationErrorCodeUnsupported,
		ipc.MutationErrorCodeNotReady,
		ipc.MutationErrorCodeBackendRejected,
	}

	for _, domain := range domains {
		domain := domain
		t.Run("apply/"+string(domain)+"/confirmed", func(t *testing.T) {
			response, err := ipc.NewApplyManagedPlanConfirmedResponse(domain)
			if err != nil {
				t.Fatalf("NewApplyManagedPlanConfirmedResponse(): %v", err)
			}
			assertConstructedMutationResponse(
				t, response, ipc.OperationApplyManagedPlan,
				ipc.MutationStatusConfirmed, domain, "", false,
			)
		})
		t.Run("apply/"+string(domain)+"/unknown", func(t *testing.T) {
			response, err := ipc.NewApplyManagedPlanUnknownResponse(domain)
			if err != nil {
				t.Fatalf("NewApplyManagedPlanUnknownResponse(): %v", err)
			}
			assertConstructedMutationResponse(
				t, response, ipc.OperationApplyManagedPlan,
				ipc.MutationStatusUnknown, domain,
				ipc.MutationErrorCodeUnknownResult, true,
			)
		})
		for _, code := range rejectedCodes {
			code := code
			t.Run("apply/"+string(domain)+"/rejected/"+string(code), func(t *testing.T) {
				response, err := ipc.NewApplyManagedPlanRejectedResponse(domain, code)
				if err != nil {
					t.Fatalf("NewApplyManagedPlanRejectedResponse(): %v", err)
				}
				assertConstructedMutationResponse(
					t, response, ipc.OperationApplyManagedPlan,
					ipc.MutationStatusRejected, domain, code, true,
				)
			})
		}
	}

	t.Run("remove/confirmed", func(t *testing.T) {
		response := ipc.NewRemoveManagedInfrastructureConfirmedResponse()
		assertConstructedMutationResponse(
			t, response, ipc.OperationRemoveManagedInfrastructure,
			ipc.MutationStatusConfirmed, "", "", false,
		)
	})
	t.Run("remove/unknown", func(t *testing.T) {
		response := ipc.NewRemoveManagedInfrastructureUnknownResponse()
		assertConstructedMutationResponse(
			t, response, ipc.OperationRemoveManagedInfrastructure,
			ipc.MutationStatusUnknown, "",
			ipc.MutationErrorCodeUnknownResult, true,
		)
	})
	for _, code := range rejectedCodes[1:] {
		code := code
		t.Run("remove/rejected/"+string(code), func(t *testing.T) {
			response, err := ipc.NewRemoveManagedInfrastructureRejectedResponse(code)
			if err != nil {
				t.Fatalf("NewRemoveManagedInfrastructureRejectedResponse(): %v", err)
			}
			assertConstructedMutationResponse(
				t, response, ipc.OperationRemoveManagedInfrastructure,
				ipc.MutationStatusRejected, "", code, true,
			)
		})
	}
}

func TestMutationResponseConstructorsRejectInvalidInputs(t *testing.T) {
	invalidDomain := ipc.Domain("arbitrary")
	invalidCode := ipc.MutationErrorCode("raw_backend_failure")
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "apply confirmed empty domain",
			call: func() error {
				_, err := ipc.NewApplyManagedPlanConfirmedResponse("")
				return err
			},
		},
		{
			name: "apply confirmed invalid domain",
			call: func() error {
				_, err := ipc.NewApplyManagedPlanConfirmedResponse(invalidDomain)
				return err
			},
		},
		{
			name: "apply rejected invalid domain",
			call: func() error {
				_, err := ipc.NewApplyManagedPlanRejectedResponse(
					invalidDomain, ipc.MutationErrorCodeBackendRejected,
				)
				return err
			},
		},
		{
			name: "apply unknown invalid domain",
			call: func() error {
				_, err := ipc.NewApplyManagedPlanUnknownResponse(invalidDomain)
				return err
			},
		},
		{
			name: "apply rejected empty code",
			call: func() error {
				_, err := ipc.NewApplyManagedPlanRejectedResponse(ipc.DomainTarget, "")
				return err
			},
		},
		{
			name: "apply rejected invalid code",
			call: func() error {
				_, err := ipc.NewApplyManagedPlanRejectedResponse(ipc.DomainTarget, invalidCode)
				return err
			},
		},
		{
			name: "apply rejected unknown-result code",
			call: func() error {
				_, err := ipc.NewApplyManagedPlanRejectedResponse(
					ipc.DomainTarget, ipc.MutationErrorCodeUnknownResult,
				)
				return err
			},
		},
		{
			name: "remove rejected empty code",
			call: func() error {
				_, err := ipc.NewRemoveManagedInfrastructureRejectedResponse("")
				return err
			},
		},
		{
			name: "remove rejected invalid code",
			call: func() error {
				_, err := ipc.NewRemoveManagedInfrastructureRejectedResponse(invalidCode)
				return err
			},
		},
		{
			name: "remove rejected invalid-plan code",
			call: func() error {
				_, err := ipc.NewRemoveManagedInfrastructureRejectedResponse(
					ipc.MutationErrorCodeInvalidPlan,
				)
				return err
			},
		},
		{
			name: "remove rejected unknown-result code",
			call: func() error {
				_, err := ipc.NewRemoveManagedInfrastructureRejectedResponse(
					ipc.MutationErrorCodeUnknownResult,
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertMutationResponseErrorCode(
				t, test.call(), ipc.MutationResponseErrorCodeSchemaRejected,
			)
		})
	}
}

func TestMutationResponseEncodeRejectsNilWithoutPanic(t *testing.T) {
	var untyped ipc.MutationResponse
	var typedApply ipc.ApplyManagedPlanResponse
	var typedRemove ipc.RemoveManagedInfrastructureResponse
	apply, err := ipc.NewApplyManagedPlanConfirmedResponse(ipc.DomainTarget)
	if err != nil {
		t.Fatalf("NewApplyManagedPlanConfirmedResponse(): %v", err)
	}
	concreteApplyNil := reflect.Zero(reflect.TypeOf(apply)).Interface().(ipc.MutationResponse)
	remove := ipc.NewRemoveManagedInfrastructureConfirmedResponse()
	concreteRemoveNil := reflect.Zero(reflect.TypeOf(remove)).Interface().(ipc.MutationResponse)
	tests := []struct {
		name     string
		response ipc.MutationResponse
	}{
		{name: "untyped nil", response: untyped},
		{name: "nil apply interface", response: typedApply},
		{name: "nil remove interface", response: typedRemove},
		{name: "typed concrete apply nil", response: concreteApplyNil},
		{name: "typed concrete remove nil", response: concreteRemoveNil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("EncodeMutationResponse() panicked: %v", recovered)
				}
			}()
			raw, err := ipc.EncodeMutationResponse(test.response)
			if len(raw) != 0 {
				t.Fatalf("EncodeMutationResponse(nil) bytes = %q, want empty", raw)
			}
			assertMutationResponseErrorCode(
				t, err, ipc.MutationResponseErrorCodeSchemaRejected,
			)
		})
	}
}

func TestMutationResponseResourceLimits(t *testing.T) {
	base := readMutationResponseGoldenFile(t, "valid/remove-confirmed.json")
	exactBytes := append(bytes.TrimSpace(base), bytes.Repeat(
		[]byte(" "), 4*1024-len(bytes.TrimSpace(base)),
	)...)
	if response, err := ipc.DecodeMutationResponse(exactBytes); err != nil || response == nil {
		t.Fatalf("exact response byte limit = (%T, %v), want valid response", response, err)
	}
	response, err := ipc.DecodeMutationResponse(append(exactBytes, ' '))
	if response != nil {
		t.Fatalf("one-over response = %#v, want nil", response)
	}
	assertMutationResponseErrorCode(t, err, ipc.MutationResponseErrorCodeResponseTooLarge)

	for _, test := range []struct {
		name string
		raw  []byte
		code ipc.MutationResponseErrorCode
	}{
		{name: "exact depth reaches schema", raw: []byte(`{"x":[]}`), code: ipc.MutationResponseErrorCodeSchemaRejected},
		{name: "one-over depth", raw: []byte(`{"x":[[]]}`), code: ipc.MutationResponseErrorCodeMaxDepthExceeded},
		{name: "exact tokens reach schema", raw: mutationResponseNullArray(27), code: ipc.MutationResponseErrorCodeSchemaRejected},
		{name: "one-over tokens", raw: mutationResponseNullArray(28), code: ipc.MutationResponseErrorCodeTokenLimitExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := ipc.DecodeMutationResponse(test.raw)
			if response != nil {
				t.Fatalf("DecodeMutationResponse() response = %#v, want nil", response)
			}
			assertMutationResponseErrorCode(t, err, test.code)
		})
	}
}

func TestMutationResponseVersionIntegerSemantics(t *testing.T) {
	base := strings.TrimSpace(string(readMutationResponseGoldenFile(t, "valid/remove-confirmed.json")))
	tests := []struct {
		name    string
		version string
		valid   bool
	}{
		{name: "integer", version: "1", valid: true},
		{name: "decimal integer", version: "1.0", valid: true},
		{name: "exponent integer", version: "1e0", valid: true},
		{name: "fractional", version: "1.5"},
		{name: "zero", version: "0"},
		{name: "negative", version: "-1"},
		{name: "int64 overflow", version: "9223372036854775808"},
		{name: "string", version: `"1"`},
		{name: "null", version: "null"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(strings.Replace(base, `"version":1`, `"version":`+test.version, 1))
			response, err := ipc.DecodeMutationResponse(raw)
			if test.valid {
				if err != nil || response == nil {
					t.Fatalf("DecodeMutationResponse() = (%T, %v), want success", response, err)
				}
				encoded, encodeErr := ipc.EncodeMutationResponse(response)
				if encodeErr != nil {
					t.Fatalf("EncodeMutationResponse(): %v", encodeErr)
				}
				if !bytes.Equal(encoded, bytes.TrimSpace(readMutationResponseGoldenFile(t, "valid/remove-confirmed.json"))) {
					t.Fatalf("canonical response = %s", encoded)
				}
				return
			}
			if response != nil {
				t.Fatalf("DecodeMutationResponse() response = %#v, want nil", response)
			}
			assertMutationResponseErrorCode(t, err, ipc.MutationResponseErrorCodeSchemaRejected)
		})
	}
}

func TestMutationResponseClassificationPrecedence(t *testing.T) {
	oversizedInvalidUTF8 := append(bytes.Repeat([]byte(" "), 4*1024), 0xff)
	duplicateUnknown := []byte(`{"version":1,"operation":"RemoveManagedInfrastructure","status":"confirmed","unknown":1,"unknown":2}`)
	depthBeforeTokens := []byte(`{"x":[[` + strings.Repeat("null,", 28) + `null]]}`)
	tests := []struct {
		name string
		raw  []byte
		code ipc.MutationResponseErrorCode
	}{
		{name: "size before UTF-8", raw: oversizedInvalidUTF8, code: ipc.MutationResponseErrorCodeResponseTooLarge},
		{name: "duplicate before schema", raw: duplicateUnknown, code: ipc.MutationResponseErrorCodeDuplicateKey},
		{name: "depth before later token overflow", raw: depthBeforeTokens, code: ipc.MutationResponseErrorCodeMaxDepthExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := ipc.DecodeMutationResponse(test.raw)
			if response != nil {
				t.Fatalf("DecodeMutationResponse() response = %#v, want nil", response)
			}
			assertMutationResponseErrorCode(t, err, test.code)
		})
	}
}

func FuzzMutationResponseClosedUnion(f *testing.F) {
	cases := readMutationResponseCases(f)
	for _, test := range append(cases.Valid, cases.Invalid...) {
		f.Add(readMutationResponseFixture(f, test))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		response, err := ipc.DecodeMutationResponse(raw)
		if err != nil {
			if response != nil {
				t.Fatalf("failed decode returned response %#v", response)
			}
			return
		}
		if response == nil {
			t.Fatal("successful decode returned nil response")
		}
		switch response.Operation() {
		case ipc.OperationApplyManagedPlan:
			apply, ok := response.(ipc.ApplyManagedPlanResponse)
			if !ok || (apply.Domain() != ipc.DomainInfrastructure &&
				apply.Domain() != ipc.DomainPolicy && apply.Domain() != ipc.DomainTarget) {
				t.Fatalf("invalid Apply response %#v", response)
			}
		case ipc.OperationRemoveManagedInfrastructure:
			if _, ok := response.(ipc.RemoveManagedInfrastructureResponse); !ok {
				t.Fatalf("invalid Remove response %#v", response)
			}
		default:
			t.Fatalf("unexpected operation %q", response.Operation())
		}
		switch response.Status() {
		case ipc.MutationStatusConfirmed, ipc.MutationStatusRejected, ipc.MutationStatusUnknown:
		default:
			t.Fatalf("unexpected status %q", response.Status())
		}
	})
}

func TestMutationResponseErrorsDoNotEchoAttackerInput(t *testing.T) {
	const marker = "attacker-controlled"
	raw := readMutationResponseGoldenFile(t, "invalid/error-message.json")
	response, err := ipc.DecodeMutationResponse(raw)
	if response != nil {
		t.Fatalf("DecodeMutationResponse() response = %#v, want nil", response)
	}
	assertMutationResponseErrorCode(
		t, err, ipc.MutationResponseErrorCodeSchemaRejected,
	)
	if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), string(bytes.TrimSpace(raw))) {
		t.Fatalf("mutation response error echoed attacker input: %q", err)
	}
}

func assertConstructedMutationResponse(
	t *testing.T,
	response ipc.MutationResponse,
	wantOperation ipc.Operation,
	wantStatus ipc.MutationStatus,
	wantDomain ipc.Domain,
	wantCode ipc.MutationErrorCode,
	wantCodeFound bool,
) {
	t.Helper()
	wire := mutationResponseWire{
		Version: 1, Operation: string(wantOperation), Status: string(wantStatus),
		Domain: string(wantDomain), ErrorCode: string(wantCode),
	}
	assertMutationResponseGetters(t, response, wire)
	encoded, err := ipc.EncodeMutationResponse(response)
	if err != nil {
		t.Fatalf("EncodeMutationResponse(constructed): %v", err)
	}
	want := canonicalMutationResponse(
		wantOperation, wantStatus, wantDomain, wantCode, wantCodeFound,
	)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("constructed canonical response = %s, want %s", encoded, want)
	}
}

func assertMutationResponseGetters(
	t *testing.T,
	response ipc.MutationResponse,
	want mutationResponseWire,
) {
	t.Helper()
	if response == nil {
		t.Fatal("mutation response = nil")
	}
	if got := response.Operation(); string(got) != want.Operation {
		t.Fatalf("Operation() = %q, want %q", got, want.Operation)
	}
	if got := response.Status(); string(got) != want.Status {
		t.Fatalf("Status() = %q, want %q", got, want.Status)
	}
	code, found := response.ErrorCode()
	wantCodeFound := want.ErrorCode != ""
	if found != wantCodeFound || string(code) != want.ErrorCode {
		t.Fatalf("ErrorCode() = %q, %v, want %q, %v", code, found, want.ErrorCode, wantCodeFound)
	}
	switch want.Operation {
	case string(ipc.OperationApplyManagedPlan):
		apply, ok := response.(ipc.ApplyManagedPlanResponse)
		if !ok {
			t.Fatalf("Apply response type = %T, want ipc.ApplyManagedPlanResponse", response)
		}
		if got := apply.Domain(); string(got) != want.Domain {
			t.Fatalf("Domain() = %q, want %q", got, want.Domain)
		}
	case string(ipc.OperationRemoveManagedInfrastructure):
		if _, ok := response.(ipc.RemoveManagedInfrastructureResponse); !ok {
			t.Fatalf("Remove response type = %T, want ipc.RemoveManagedInfrastructureResponse", response)
		}
		if want.Domain != "" {
			t.Fatalf("Remove response expectation contains domain %q", want.Domain)
		}
	default:
		t.Fatalf("unexpected mutation operation %q", want.Operation)
	}
}

func canonicalMutationResponse(
	operation ipc.Operation,
	status ipc.MutationStatus,
	domain ipc.Domain,
	code ipc.MutationErrorCode,
	codeFound bool,
) []byte {
	raw := fmt.Sprintf(
		`{"version":1,"operation":%q,"status":%q`, string(operation), string(status),
	)
	if operation == ipc.OperationApplyManagedPlan {
		raw += fmt.Sprintf(`,"domain":%q`, string(domain))
	}
	if codeFound {
		raw += fmt.Sprintf(`,"error_code":%q`, string(code))
	}
	return []byte(raw + `}`)
}

func assertMutationResponseErrorCode(
	t *testing.T,
	err error,
	want ipc.MutationResponseErrorCode,
) {
	t.Helper()
	if err == nil {
		t.Fatalf("mutation response error = nil, want code %q", want)
	}
	var responseError *ipc.MutationResponseError
	if !errors.As(err, &responseError) {
		t.Fatalf("mutation response error type = %T, want *ipc.MutationResponseError", err)
	}
	if got := responseError.Code(); got != want {
		t.Fatalf("mutation response error code = %q, want %q (error: %v)", got, want, err)
	}
}

func readMutationResponseCases(tb testing.TB) mutationResponseCases {
	tb.Helper()
	var cases mutationResponseCases
	if err := json.Unmarshal(readMutationResponseGoldenFile(tb, "cases.json"), &cases); err != nil {
		tb.Fatalf("decode mutation response cases.json: %v", err)
	}
	return cases
}

func readMutationResponseFixture(tb testing.TB, test mutationResponseCase) []byte {
	tb.Helper()
	raw := readMutationResponseGoldenFile(tb, test.Path)
	if test.FixtureEncoding == "" {
		return raw
	}
	if test.FixtureEncoding != "generated_from_hex_descriptor" {
		tb.Fatalf("unsupported fixture encoding %q for %s", test.FixtureEncoding, test.Path)
	}
	var descriptor struct {
		Generator string `json:"generator"`
		Template  string `json:"template"`
		Marker    string `json:"marker"`
		BytesHex  string `json:"bytes_hex"`
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		tb.Fatalf("decode generated fixture descriptor %s: %v", test.Path, err)
	}
	if descriptor.Generator != "replace_marker_with_hex" || descriptor.Marker == "" {
		tb.Fatalf("invalid generated fixture descriptor for %s", test.Path)
	}
	replacement, err := hex.DecodeString(descriptor.BytesHex)
	if err != nil {
		tb.Fatalf("decode generated fixture bytes for %s: %v", test.Path, err)
	}
	if count := strings.Count(descriptor.Template, descriptor.Marker); count != 1 {
		tb.Fatalf("generated fixture marker count for %s = %d, want 1", test.Path, count)
	}
	return bytes.Replace(
		[]byte(descriptor.Template), []byte(descriptor.Marker), replacement, 1,
	)
}

func readMutationResponseGoldenFile(tb testing.TB, name string) []byte {
	tb.Helper()
	contents, err := os.ReadFile(filepath.Join(
		"..", "..", "schema", "testdata", "ipc-v1-mutation-response", filepath.FromSlash(name),
	))
	if err != nil {
		tb.Fatalf("read mutation response golden %s: %v", name, err)
	}
	return contents
}

func mutationResponseNullArray(items int) []byte {
	if items == 0 {
		return []byte(`{"x":[]}`)
	}
	return []byte(`{"x":[` + strings.Repeat("null,", items-1) + `null]}`)
}
