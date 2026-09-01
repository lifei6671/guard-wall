package ipc_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestProbeCapabilitiesResponseConstants(t *testing.T) {
	failureCodes := []struct {
		got  ipc.ProbeCapabilitiesFailureCode
		want string
	}{
		{ipc.ProbeCapabilitiesFailureCodeUnsupported, "unsupported"},
		{ipc.ProbeCapabilitiesFailureCodeNotReady, "not_ready"},
	}
	for _, test := range failureCodes {
		if string(test.got) != test.want {
			t.Fatalf("failure code = %q, want %q", test.got, test.want)
		}
	}

	localCodes := []struct {
		got  ipc.ProbeCapabilitiesResponseErrorCode
		want string
	}{
		{ipc.ProbeCapabilitiesResponseErrorCodeResponseTooLarge, "response_too_large"},
		{ipc.ProbeCapabilitiesResponseErrorCodeInvalidUTF8, "invalid_utf8"},
		{ipc.ProbeCapabilitiesResponseErrorCodeDuplicateKey, "duplicate_key"},
		{ipc.ProbeCapabilitiesResponseErrorCodeMaxDepthExceeded, "max_depth_exceeded"},
		{ipc.ProbeCapabilitiesResponseErrorCodeTokenLimitExceeded, "token_limit_exceeded"},
		{ipc.ProbeCapabilitiesResponseErrorCodeInvalidJSON, "invalid_json"},
		{ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected, "schema_rejected"},
		{ipc.ProbeCapabilitiesResponseErrorCodeSemanticRejected, "semantic_rejected"},
	}
	for _, test := range localCodes {
		if string(test.got) != test.want {
			t.Fatalf("local response code = %q, want %q", test.got, test.want)
		}
	}
}

func TestProbeCapabilitiesSuccessResponseDeterministicRoundTrip(t *testing.T) {
	capabilities := completeProbeCapabilities(t)
	response, err := ipc.NewProbeCapabilitiesSuccessResponse(capabilities)
	if err != nil {
		t.Fatalf("NewProbeCapabilitiesSuccessResponse(): %v", err)
	}
	if response.Operation() != ipc.OperationProbeCapabilities {
		t.Fatalf("operation = %q", response.Operation())
	}
	assertProbeCapabilitiesEqual(t, response.Capabilities(), capabilities)

	want := `{"version":1,"operation":"ProbeCapabilities","payload":{"backend":"nftables-native","tool_version":"nftables v1.0.9","ipv4":true,"ipv6":true,"cidr":true,"native_set":true,"native_timeout":true,"crash_safe_expiry":true,"atomic_batch":true,"host_input":true,"forward":true,"ufw_integration_proven":false,"docker_integration_proven":true,"ownership_proven":true,"mutation_ready":true}}`
	for iteration := 0; iteration < 20; iteration++ {
		raw, encodeErr := ipc.EncodeProbeCapabilitiesResponse(response)
		if encodeErr != nil {
			t.Fatalf("EncodeProbeCapabilitiesResponse(): %v", encodeErr)
		}
		if string(raw) != want {
			t.Fatalf("encoded response = %s, want %s", raw, want)
		}
	}

	decoded, err := ipc.DecodeProbeCapabilitiesResponse([]byte(want))
	if err != nil {
		t.Fatalf("DecodeProbeCapabilitiesResponse(): %v", err)
	}
	success, ok := decoded.(ipc.ProbeCapabilitiesSuccessResponse)
	if !ok {
		t.Fatalf("decoded type = %T", decoded)
	}
	assertProbeCapabilitiesEqual(t, success.Capabilities(), capabilities)
}

func TestProbeCapabilitiesMutationNotReadyIsSuccess(t *testing.T) {
	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend:     firewall.BackendKindIptablesLegacy,
		ToolVersion: "iptables v1.8.7",
		IPv4:        true,
		CIDR:        true,
		HostInput:   true,
	})
	if err != nil {
		t.Fatalf("NewFirewallCapabilities(): %v", err)
	}
	response, err := ipc.NewProbeCapabilitiesSuccessResponse(capabilities)
	if err != nil {
		t.Fatalf("NewProbeCapabilitiesSuccessResponse(): %v", err)
	}
	raw, err := ipc.EncodeProbeCapabilitiesResponse(response)
	if err != nil {
		t.Fatalf("EncodeProbeCapabilitiesResponse(): %v", err)
	}
	decoded, err := ipc.DecodeProbeCapabilitiesResponse(raw)
	if err != nil {
		t.Fatalf("DecodeProbeCapabilitiesResponse(): %v", err)
	}
	success := decoded.(ipc.ProbeCapabilitiesSuccessResponse)
	if success.Capabilities().MutationReady() {
		t.Fatal("MutationReady = true, want false success fact")
	}
}

func TestProbeCapabilitiesFailureResponsesDeterministicRoundTrip(t *testing.T) {
	tests := []ipc.ProbeCapabilitiesFailureCode{
		ipc.ProbeCapabilitiesFailureCodeUnsupported,
		ipc.ProbeCapabilitiesFailureCodeNotReady,
	}
	for _, code := range tests {
		t.Run(string(code), func(t *testing.T) {
			response, err := ipc.NewProbeCapabilitiesFailureResponse(code)
			if err != nil {
				t.Fatalf("NewProbeCapabilitiesFailureResponse(): %v", err)
			}
			want := fmt.Sprintf(`{"version":1,"operation":"ProbeCapabilities","error_code":%q}`, code)
			raw, err := ipc.EncodeProbeCapabilitiesResponse(response)
			if err != nil {
				t.Fatalf("EncodeProbeCapabilitiesResponse(): %v", err)
			}
			if string(raw) != want {
				t.Fatalf("encoded response = %s, want %s", raw, want)
			}
			decoded, err := ipc.DecodeProbeCapabilitiesResponse(raw)
			if err != nil {
				t.Fatalf("DecodeProbeCapabilitiesResponse(): %v", err)
			}
			failure, ok := decoded.(ipc.ProbeCapabilitiesFailureResponse)
			if !ok || failure.FailureCode() != code {
				t.Fatalf("decoded = %T/%v, want failure %q", decoded, decoded, code)
			}
		})
	}
}

func TestProbeCapabilitiesResponseConstructorsRejectInvalidValues(t *testing.T) {
	response, err := ipc.NewProbeCapabilitiesSuccessResponse(firewall.FirewallCapabilities{})
	if response != nil {
		t.Fatalf("invalid success response = %T, want nil", response)
	}
	assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeSemanticRejected)

	failure, err := ipc.NewProbeCapabilitiesFailureResponse("raw_backend_failure")
	if failure != nil {
		t.Fatalf("invalid failure response = %T, want nil", failure)
	}
	assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected)
}

func TestDecodeProbeCapabilitiesResponseClosedUnionAndClassification(t *testing.T) {
	valid := validProbeCapabilitiesJSON()
	tests := []struct {
		name string
		raw  []byte
		code ipc.ProbeCapabilitiesResponseErrorCode
	}{
		{name: "invalid utf8", raw: append([]byte(valid), 0xff), code: ipc.ProbeCapabilitiesResponseErrorCodeInvalidUTF8},
		{name: "duplicate root key", raw: []byte(`{"version":1,"version":1,"operation":"ProbeCapabilities","error_code":"not_ready"}`), code: ipc.ProbeCapabilitiesResponseErrorCodeDuplicateKey},
		{name: "multiple values", raw: []byte(`{"version":1,"operation":"ProbeCapabilities","error_code":"not_ready"} {}`), code: ipc.ProbeCapabilitiesResponseErrorCodeInvalidJSON},
		{name: "wrong version", raw: []byte(`{"version":2,"operation":"ProbeCapabilities","error_code":"not_ready"}`), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "wrong operation", raw: []byte(`{"version":1,"operation":"SnapshotManaged","error_code":"not_ready"}`), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "unknown failure", raw: []byte(`{"version":1,"operation":"ProbeCapabilities","error_code":"backend_failed"}`), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "success and failure", raw: []byte(strings.TrimSuffix(valid, "}") + `,"error_code":"not_ready"}`), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "failure payload", raw: []byte(`{"version":1,"operation":"ProbeCapabilities","error_code":"not_ready","message":"secret"}`), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "missing payload field", raw: []byte(strings.Replace(valid, `,"mutation_ready":true`, "", 1)), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "type confusion", raw: []byte(strings.Replace(valid, `"ipv4":true`, `"ipv4":"true"`, 1)), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "unknown payload field", raw: []byte(strings.Replace(valid, `"mutation_ready":true`, `"mutation_ready":true,"command":"id"`, 1)), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "semantic no family", raw: []byte(strings.Replace(strings.Replace(valid, `"ipv4":true`, `"ipv4":false`, 1), `"ipv6":true`, `"ipv6":false`, 1)), code: ipc.ProbeCapabilitiesResponseErrorCodeSemanticRejected},
		{name: "unknown backend", raw: []byte(strings.Replace(valid, `"nftables-native"`, `"auto"`, 1)), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "empty tool version", raw: []byte(strings.Replace(valid, `"nftables v1.0.9"`, `""`, 1)), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "leading-space tool version", raw: []byte(strings.Replace(valid, `"nftables v1.0.9"`, `" nftables v1.0.9"`, 1)), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
		{name: "invalid json", raw: []byte(`{"version":`), code: ipc.ProbeCapabilitiesResponseErrorCodeInvalidJSON},
		{name: "root array", raw: []byte(`[]`), code: ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := ipc.DecodeProbeCapabilitiesResponse(test.raw)
			if response != nil {
				t.Fatalf("response = %T, want nil", response)
			}
			assertProbeCapabilitiesResponseErrorCode(t, err, test.code)
		})
	}
}

func TestDecodeProbeCapabilitiesResponseResourceLimits(t *testing.T) {
	valid := []byte(`{"version":1,"operation":"ProbeCapabilities","error_code":"not_ready"}`)
	exact := append(append([]byte(nil), valid...), bytes.Repeat([]byte{' '}, 4096-len(valid))...)
	if _, err := ipc.DecodeProbeCapabilitiesResponse(exact); err != nil {
		t.Fatalf("4096-byte response: %v", err)
	}
	oneOver := append(exact, ' ')
	_, err := ipc.DecodeProbeCapabilitiesResponse(oneOver)
	assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeResponseTooLarge)

	_, err = ipc.DecodeProbeCapabilitiesResponse([]byte(`{"x":{"y":{}}}`))
	assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeMaxDepthExceeded)

	token64 := []byte(`{"x":[` + integerSequence(59) + `]}`)
	_, err = ipc.DecodeProbeCapabilitiesResponse(token64)
	assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	token65 := []byte(`{"x":[` + integerSequence(60) + `]}`)
	_, err = ipc.DecodeProbeCapabilitiesResponse(token65)
	assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeTokenLimitExceeded)
}

func TestProbeCapabilitiesResponseErrorsDoNotEchoInput(t *testing.T) {
	marker := "SECRET_BACKEND_OUTPUT_7ab9"
	raw := []byte(`{"version":1,"operation":"ProbeCapabilities","error_code":"` + marker + `"}`)
	_, err := ipc.DecodeProbeCapabilitiesResponse(raw)
	assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("error leaked attacker input: %q", err)
	}
}

func FuzzDecodeProbeCapabilitiesResponseClosedContract(f *testing.F) {
	f.Add([]byte(validProbeCapabilitiesJSON()))
	f.Add([]byte(`{"version":1,"operation":"ProbeCapabilities","error_code":"not_ready"}`))
	f.Add([]byte(`{"version":1,"operation":"ProbeCapabilities","error_code":"raw"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		response, err := ipc.DecodeProbeCapabilitiesResponse(raw)
		if err == nil {
			if response == nil {
				t.Fatal("nil response without error")
			}
			encoded, encodeErr := ipc.EncodeProbeCapabilitiesResponse(response)
			if encodeErr != nil {
				t.Fatalf("decoded response cannot be encoded: %v", encodeErr)
			}
			if len(encoded) > 4096 || !utf8.Valid(encoded) {
				t.Fatalf("encoded response violates resource contract")
			}
			return
		}
		if response != nil {
			t.Fatalf("response %T returned with error", response)
		}
		var typed *ipc.ProbeCapabilitiesResponseError
		if !errors.As(err, &typed) {
			t.Fatalf("error type = %T", err)
		}
	})
}

func completeProbeCapabilities(t testing.TB) firewall.FirewallCapabilities {
	t.Helper()
	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend:                 firewall.BackendKindNftablesNative,
		ToolVersion:             "nftables v1.0.9",
		IPv4:                    true,
		IPv6:                    true,
		CIDR:                    true,
		NativeSet:               true,
		NativeTimeout:           true,
		CrashSafeExpiry:         true,
		AtomicBatch:             true,
		HostInput:               true,
		Forward:                 true,
		UFWIntegrationProven:    false,
		DockerIntegrationProven: true,
		OwnershipProven:         true,
		MutationReady:           true,
	})
	if err != nil {
		t.Fatalf("NewFirewallCapabilities(): %v", err)
	}
	return capabilities
}

func validProbeCapabilitiesJSON() string {
	return `{"version":1,"operation":"ProbeCapabilities","payload":{"backend":"nftables-native","tool_version":"nftables v1.0.9","ipv4":true,"ipv6":true,"cidr":true,"native_set":true,"native_timeout":true,"crash_safe_expiry":true,"atomic_batch":true,"host_input":true,"forward":true,"ufw_integration_proven":false,"docker_integration_proven":true,"ownership_proven":true,"mutation_ready":true}}`
}

func assertProbeCapabilitiesEqual(
	t testing.TB,
	got firewall.FirewallCapabilities,
	want firewall.FirewallCapabilities,
) {
	t.Helper()
	if got.Backend() != want.Backend() ||
		got.ToolVersion() != want.ToolVersion() ||
		got.SupportsIPv4() != want.SupportsIPv4() ||
		got.SupportsIPv6() != want.SupportsIPv6() ||
		got.SupportsCIDR() != want.SupportsCIDR() ||
		got.SupportsNativeSet() != want.SupportsNativeSet() ||
		got.SupportsNativeTimeout() != want.SupportsNativeTimeout() ||
		got.SupportsCrashSafeExpiry() != want.SupportsCrashSafeExpiry() ||
		got.SupportsAtomicBatch() != want.SupportsAtomicBatch() ||
		got.SupportsHostInput() != want.SupportsHostInput() ||
		got.SupportsForward() != want.SupportsForward() ||
		got.UFWIntegrationProven() != want.UFWIntegrationProven() ||
		got.DockerIntegrationProven() != want.DockerIntegrationProven() ||
		got.OwnershipProven() != want.OwnershipProven() ||
		got.MutationReady() != want.MutationReady() {
		t.Fatalf("capabilities mismatch: got %#v, want %#v", got, want)
	}
}

func assertProbeCapabilitiesResponseErrorCode(
	t testing.TB,
	err error,
	want ipc.ProbeCapabilitiesResponseErrorCode,
) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %q", want)
	}
	var typed *ipc.ProbeCapabilitiesResponseError
	if !errors.As(err, &typed) {
		t.Fatalf("error type = %T, want *ipc.ProbeCapabilitiesResponseError", err)
	}
	if typed.Code() != want {
		t.Fatalf("error code = %q, want %q", typed.Code(), want)
	}
}

func integerSequence(count int) string {
	values := make([]string, count)
	for index := range values {
		values[index] = "0"
	}
	return strings.Join(values, ",")
}
