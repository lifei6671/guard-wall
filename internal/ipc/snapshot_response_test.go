package ipc_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestSnapshotManagedSuccessResponseDeterministicRoundTrip(t *testing.T) {
	snapshot := completeManagedSnapshot(t)
	response, err := ipc.NewSnapshotManagedSuccessResponse(snapshot)
	if err != nil {
		t.Fatalf("NewSnapshotManagedSuccessResponse(): %v", err)
	}

	var first []byte
	for iteration := 0; iteration < 32; iteration++ {
		raw, err := ipc.EncodeSnapshotManagedResponse(response)
		if err != nil {
			t.Fatalf("EncodeSnapshotManagedResponse() iteration %d: %v", iteration, err)
		}
		if iteration == 0 {
			first = raw
		} else if !bytes.Equal(raw, first) {
			t.Fatalf("encoding changed at iteration %d", iteration)
		}
	}
	if !bytes.Contains(first, []byte(`"membership":"present"`)) ||
		!bytes.Contains(first, []byte(`"input":true,"forward":true`)) {
		t.Fatalf("encoded success lost closed target facts: %s", first)
	}

	decoded, err := ipc.DecodeSnapshotManagedResponse(first)
	if err != nil {
		t.Fatalf("DecodeSnapshotManagedResponse(): %v", err)
	}
	success, ok := decoded.(ipc.SnapshotManagedSuccessResponse)
	if !ok {
		t.Fatalf("decoded type = %T, want success", decoded)
	}
	if success.Snapshot().Digest() != snapshot.Digest() || success.Operation() != ipc.OperationSnapshotManaged {
		t.Fatalf("decoded snapshot = %q/%q, want %q/%q", success.Snapshot().Digest(), success.Operation(), snapshot.Digest(), ipc.OperationSnapshotManaged)
	}
	if err := success.Snapshot().Validate(); err != nil {
		t.Fatalf("decoded snapshot Validate(): %v", err)
	}
}

func TestSnapshotManagedFailureResponseExactClosedUnion(t *testing.T) {
	tests := []struct {
		code ipc.SnapshotManagedFailureCode
		want string
	}{
		{ipc.SnapshotManagedFailureCodeUnsupported, `{"version":1,"operation":"SnapshotManaged","error_code":"unsupported"}`},
		{ipc.SnapshotManagedFailureCodeNotReady, `{"version":1,"operation":"SnapshotManaged","error_code":"not_ready"}`},
		{ipc.SnapshotManagedFailureCodeOwnershipConflict, `{"version":1,"operation":"SnapshotManaged","error_code":"ownership_conflict"}`},
	}
	for _, test := range tests {
		response, err := ipc.NewSnapshotManagedFailureResponse(test.code)
		if err != nil {
			t.Fatalf("NewSnapshotManagedFailureResponse(%q): %v", test.code, err)
		}
		raw, err := ipc.EncodeSnapshotManagedResponse(response)
		if err != nil {
			t.Fatalf("EncodeSnapshotManagedResponse(%q): %v", test.code, err)
		}
		if string(raw) != test.want {
			t.Fatalf("encoded failure = %s, want %s", raw, test.want)
		}
		decoded, err := ipc.DecodeSnapshotManagedResponse(raw)
		if err != nil {
			t.Fatalf("DecodeSnapshotManagedResponse(%q): %v", test.code, err)
		}
		failure, ok := decoded.(ipc.SnapshotManagedFailureResponse)
		if !ok || failure.FailureCode() != test.code {
			t.Fatalf("decoded failure = %T/%v", decoded, decoded)
		}
	}
}

func TestSnapshotManagedProductionCodecMatchesValidSchemaGoldens(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate snapshot response test")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	if _, err := os.Stat(filepath.Join(repositoryRoot, "schema")); err != nil {
		workingDirectory, workingErr := os.Getwd()
		if workingErr != nil {
			t.Fatalf("Getwd(): %v", workingErr)
		}
		repositoryRoot = workingDirectory
	}
	patterns := []string{
		filepath.Join(repositoryRoot, "schema", "testdata", "ipc-v1-snapshot-managed-success", "valid", "*.json"),
		filepath.Join(repositoryRoot, "schema", "testdata", "ipc-v1-snapshot-managed-failure", "valid", "*.json"),
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			t.Fatalf("Glob(%q) = %v/%v", pattern, matches, err)
		}
		for _, path := range matches {
			t.Run(filepath.Base(path), func(t *testing.T) {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile(%q): %v", path, err)
				}
				response, err := ipc.DecodeSnapshotManagedResponse(raw)
				if err != nil {
					t.Fatalf("production decoder rejected valid golden: %v", err)
				}
				reencoded, err := ipc.EncodeSnapshotManagedResponse(response)
				if err != nil {
					t.Fatalf("production encoder rejected valid golden: %v", err)
				}
				if !bytes.Equal(bytes.TrimSpace(raw), reencoded) {
					t.Fatalf("golden is not deterministic production wire\ngot:  %s\nwant: %s", reencoded, bytes.TrimSpace(raw))
				}
			})
		}
	}
}

func TestSnapshotManagedProductionCodecAcceptsMathematicalInteger(t *testing.T) {
	raw := []byte(`{"version":1,"operation":"SnapshotManaged","payload":{"snapshot_digest":"655049f824e6f3406adade59a133701ed7efbb87dd1b5ffc79e74e02f4a1fe49","infrastructure":{"backend":"nftables-native","owner_version":"guard/v1","schema_version":1.0,"digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"policy":null,"targets":[],"foreign_context_digest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}`)
	response, err := ipc.DecodeSnapshotManagedResponse(raw)
	if err != nil {
		t.Fatalf("DecodeSnapshotManagedResponse(1.0): %v", err)
	}
	canonical, err := ipc.EncodeSnapshotManagedResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(canonical, []byte(`"schema_version":1`)) || bytes.Contains(canonical, []byte(`"schema_version":1.0`)) {
		t.Fatalf("canonical schema version = %s", canonical)
	}
}

func TestDecodeSnapshotManagedResponseRejectsMalformedAndNoncanonical(t *testing.T) {
	validResponse, err := ipc.NewSnapshotManagedSuccessResponse(completeManagedSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	valid, err := ipc.EncodeSnapshotManagedResponse(validResponse)
	if err != nil {
		t.Fatal(err)
	}
	mismatchedDigest := bytes.Replace(valid, []byte(validResponse.Snapshot().Digest()), []byte(strings.Repeat("f", 64)), 1)
	var unorderedValue map[string]any
	if err := json.Unmarshal(valid, &unorderedValue); err != nil {
		t.Fatal(err)
	}
	targetValues := unorderedValue["payload"].(map[string]any)["targets"].([]any)
	targetValues[0], targetValues[1] = targetValues[1], targetValues[0]
	unorderedTargets, err := json.Marshal(unorderedValue)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  []byte
		code ipc.SnapshotManagedResponseErrorCode
	}{
		{name: "invalid utf8", raw: append([]byte(`{"version":1}`), 0xff), code: ipc.SnapshotManagedResponseErrorCodeInvalidUTF8},
		{name: "duplicate key", raw: []byte(`{"version":1,"version":1,"operation":"SnapshotManaged","error_code":"not_ready"}`), code: ipc.SnapshotManagedResponseErrorCodeDuplicateKey},
		{name: "multiple values", raw: []byte(`{"version":1,"operation":"SnapshotManaged","error_code":"not_ready"}{}`), code: ipc.SnapshotManagedResponseErrorCodeInvalidJSON},
		{name: "wrong operation", raw: []byte(`{"version":1,"operation":"ProbeCapabilities","error_code":"not_ready"}`), code: ipc.SnapshotManagedResponseErrorCodeSchemaRejected},
		{name: "unknown failure", raw: []byte(`{"version":1,"operation":"SnapshotManaged","error_code":"raw-secret"}`), code: ipc.SnapshotManagedResponseErrorCodeSchemaRejected},
		{name: "failure payload injection", raw: []byte(`{"version":1,"operation":"SnapshotManaged","error_code":"not_ready","payload":{}}`), code: ipc.SnapshotManagedResponseErrorCodeSchemaRejected},
		{name: "depth five", raw: []byte(`{"version":1,"operation":"SnapshotManaged","payload":{"snapshot_digest":"` + strings.Repeat("a", 64) + `","infrastructure":null,"policy":null,"targets":[{"target":"192.0.2.1/32","membership":"present","timeout_mode":"none","effective_until_unix_us":null,"input":true,"forward":false,"extra":{}}],"foreign_context_digest":"` + strings.Repeat("b", 64) + `"}}`), code: ipc.SnapshotManagedResponseErrorCodeMaxDepthExceeded},
		{name: "uppercase digest", raw: bytes.Replace(valid, []byte(strings.Repeat("d", 64)), []byte(strings.Repeat("D", 64)), 1), code: ipc.SnapshotManagedResponseErrorCodeSchemaRejected},
		{name: "mismatched snapshot digest", raw: mismatchedDigest, code: ipc.SnapshotManagedResponseErrorCodeSemanticRejected},
		{name: "noncanonical target", raw: bytes.Replace(valid, []byte("192.0.2.1/32"), []byte("192.0.2.1/24"), 1), code: ipc.SnapshotManagedResponseErrorCodeSemanticRejected},
		{name: "unordered targets", raw: unorderedTargets, code: ipc.SnapshotManagedResponseErrorCodeSemanticRejected},
		{name: "unknown target membership", raw: bytes.Replace(valid, []byte(`"membership":"present"`), []byte(`"membership":"absent"`), 1), code: ipc.SnapshotManagedResponseErrorCodeSchemaRejected},
		{name: "no target scope", raw: bytes.Replace(valid, []byte(`"input":true,"forward":false`), []byte(`"input":false,"forward":false`), 1), code: ipc.SnapshotManagedResponseErrorCodeSemanticRejected},
		{name: "none with expiry", raw: bytes.Replace(valid, []byte(`"effective_until_unix_us":null`), []byte(`"effective_until_unix_us":1`), 1), code: ipc.SnapshotManagedResponseErrorCodeSemanticRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := ipc.DecodeSnapshotManagedResponse(test.raw)
			if response != nil {
				t.Fatalf("response = %T, want nil", response)
			}
			assertSnapshotResponseErrorCode(t, err, test.code)
			if strings.Contains(err.Error(), "raw-secret") || strings.Contains(err.Error(), "192.0.2.1") {
				t.Fatalf("error leaked response contents: %q", err)
			}
		})
	}
}

func TestSnapshotManagedResponseResourceBounds(t *testing.T) {
	const maxResponseTokens = 32768

	response, err := ipc.NewSnapshotManagedFailureResponse(ipc.SnapshotManagedFailureCodeNotReady)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ipc.EncodeSnapshotManagedResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	exact := append(append([]byte(nil), raw...), bytes.Repeat([]byte{' '}, (1<<20)-len(raw))...)
	if _, err := ipc.DecodeSnapshotManagedResponse(exact); err != nil {
		t.Fatalf("1 MiB exact response: %v", err)
	}
	if decoded, err := ipc.DecodeSnapshotManagedResponse(append(exact, ' ')); decoded != nil {
		t.Fatalf("one-over response = %T, want nil", decoded)
	} else {
		assertSnapshotResponseErrorCode(t, err, ipc.SnapshotManagedResponseErrorCodeResponseTooLarge)
	}

	exactTokens := snapshotResponseNullArray(maxResponseTokens - 2)
	if decoded, err := ipc.DecodeSnapshotManagedResponse(exactTokens); decoded != nil {
		t.Fatalf("32768-token response = %T, want nil", decoded)
	} else {
		assertSnapshotResponseErrorCode(t, err, ipc.SnapshotManagedResponseErrorCodeSchemaRejected)
	}
	oneOverTokens := snapshotResponseNullArray(maxResponseTokens - 1)
	if decoded, err := ipc.DecodeSnapshotManagedResponse(oneOverTokens); decoded != nil {
		t.Fatalf("32769-token response = %T, want nil", decoded)
	} else {
		assertSnapshotResponseErrorCode(t, err, ipc.SnapshotManagedResponseErrorCodeTokenLimitExceeded)
	}

	targets := make([]firewall.TargetObservation, 0, firewall.MaxManagedSnapshotTargets)
	for index := 0; index < firewall.MaxManagedSnapshotTargets; index++ {
		prefix := netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, byte(index >> 8), byte(index)}), 128)
		observation, err := firewall.NewTargetObservation(firewall.TargetObservationSpec{
			Target:      prefix,
			TimeoutMode: firewall.ManagedTimeoutNone,
			Scopes:      []firewall.ManagedScope{firewall.ManagedScopeInput},
		})
		if err != nil {
			t.Fatalf("target %d: %v", index, err)
		}
		targets = append(targets, observation)
	}
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{Targets: targets})
	if err != nil {
		t.Fatalf("1024 targets: %v", err)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: strings.Repeat("e", 64)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
	if err != nil {
		t.Fatal(err)
	}
	success, err := ipc.NewSnapshotManagedSuccessResponse(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ipc.EncodeSnapshotManagedResponse(success)
	if err != nil {
		t.Fatalf("encode 1024 targets: %v", err)
	}
	if _, err := ipc.DecodeSnapshotManagedResponse(encoded); err != nil {
		t.Fatalf("decode 1024 targets: %v", err)
	}
	var oneOverValue map[string]any
	if err := json.Unmarshal(encoded, &oneOverValue); err != nil {
		t.Fatal(err)
	}
	payload := oneOverValue["payload"].(map[string]any)
	targetValues := payload["targets"].([]any)
	payload["targets"] = append(targetValues, map[string]any{
		"target":                  "ffff::1/128",
		"membership":              "present",
		"timeout_mode":            "none",
		"effective_until_unix_us": nil,
		"input":                   true,
		"forward":                 false,
	})
	oneOverTargets, err := json.Marshal(oneOverValue)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := ipc.DecodeSnapshotManagedResponse(oneOverTargets); decoded != nil {
		t.Fatalf("1025-target response = %T, want nil", decoded)
	} else {
		assertSnapshotResponseErrorCode(t, err, ipc.SnapshotManagedResponseErrorCodeSchemaRejected)
	}
}

func snapshotResponseNullArray(nulls int) []byte {
	return []byte("[" + strings.TrimSuffix(strings.Repeat("null,", nulls), ",") + "]")
}

func TestSnapshotManagedResponseFrameRoundTripAndBoundaries(t *testing.T) {
	response, err := ipc.NewSnapshotManagedSuccessResponse(completeManagedSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := ipc.WriteSnapshotManagedResponseFrame(&output, response); err != nil {
		t.Fatalf("WriteSnapshotManagedResponseFrame(): %v", err)
	}
	decoded, err := ipc.DecodeSnapshotManagedResponseFrame(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("DecodeSnapshotManagedResponseFrame(): %v", err)
	}
	if decoded.(ipc.SnapshotManagedSuccessResponse).Snapshot().Digest() != response.Snapshot().Digest() {
		t.Fatal("frame round trip changed snapshot")
	}

	t.Run("truncated header", func(t *testing.T) {
		_, err := ipc.DecodeSnapshotManagedResponseFrame(bytes.NewReader([]byte{0, 0, 0}))
		assertSnapshotFrameErrorCode(t, err, ipc.FrameErrorCodeTruncatedLength)
	})
	t.Run("oversized header before payload", func(t *testing.T) {
		reader := &probeHeaderOnlyReader{header: snapshotResponseHeader((1 << 20) + 1)}
		_, err := ipc.DecodeSnapshotManagedResponseFrame(reader)
		assertSnapshotFrameErrorCode(t, err, ipc.FrameErrorCodeFrameTooLarge)
		if reader.payloadRead {
			t.Fatal("decoder read payload after oversized header")
		}
	})
	t.Run("truncated payload", func(t *testing.T) {
		_, err := ipc.DecodeSnapshotManagedResponseFrame(bytes.NewReader(append(snapshotResponseHeader(4), []byte("abc")...)))
		assertSnapshotFrameErrorCode(t, err, ipc.FrameErrorCodeTruncatedPayload)
	})
	t.Run("short writes", func(t *testing.T) {
		writer := &probeChunkWriter{maxChunk: 3}
		if err := ipc.WriteSnapshotManagedResponseFrame(writer, response); err != nil {
			t.Fatal(err)
		}
		if writer.writes < 2 {
			t.Fatalf("writes = %d, want multiple", writer.writes)
		}
	})
}

func FuzzDecodeSnapshotManagedResponse(f *testing.F) {
	failure, _ := ipc.NewSnapshotManagedFailureResponse(ipc.SnapshotManagedFailureCodeNotReady)
	raw, _ := ipc.EncodeSnapshotManagedResponse(failure)
	f.Add(raw)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"version":1,"operation":"SnapshotManaged","error_code":"ownership_conflict"}`))
	f.Fuzz(func(t *testing.T, value []byte) {
		response, err := ipc.DecodeSnapshotManagedResponse(value)
		if err != nil {
			if response != nil {
				t.Fatalf("error returned with response %T", response)
			}
			var typed *ipc.SnapshotManagedResponseError
			if !errors.As(err, &typed) {
				t.Fatalf("error type = %T, want stable response error", err)
			}
			return
		}
		reencoded, err := ipc.EncodeSnapshotManagedResponse(response)
		if err != nil {
			t.Fatalf("accepted response cannot re-encode: %v", err)
		}
		if _, err := ipc.DecodeSnapshotManagedResponse(reencoded); err != nil {
			t.Fatalf("re-encoded response cannot decode: %v", err)
		}
	})
}

func completeManagedSnapshot(t testing.TB) firewall.ManagedSnapshot {
	t.Helper()
	infrastructure, err := firewall.NewInfrastructureObservation(firewall.InfrastructureObservationSpec{
		Backend:       firewall.BackendKindNftablesNative,
		OwnerVersion:  firewall.ManagedOwnerVersionV1,
		SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1,
		Digest:        strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{RelationDigest: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	expiry := int64(1700000000000000)
	targetSpecs := []firewall.TargetObservationSpec{
		{Target: netip.MustParsePrefix("2001:db8::/64"), TimeoutMode: firewall.ManagedTimeoutNative, EffectiveUntilUnixMicro: &expiry, Scopes: []firewall.ManagedScope{firewall.ManagedScopeForward, firewall.ManagedScopeInput}},
		{Target: netip.MustParsePrefix("192.0.2.1/32"), TimeoutMode: firewall.ManagedTimeoutNone, Scopes: []firewall.ManagedScope{firewall.ManagedScopeInput}},
	}
	targets := make([]firewall.TargetObservation, 0, len(targetSpecs))
	for _, spec := range targetSpecs {
		observation, err := firewall.NewTargetObservation(spec)
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, observation)
	}
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{
		Infrastructure: &infrastructure,
		Policy:         &policy,
		Targets:        targets,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: strings.Repeat("d", 64)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertSnapshotResponseErrorCode(t testing.TB, err error, want ipc.SnapshotManagedResponseErrorCode) {
	t.Helper()
	var typed *ipc.SnapshotManagedResponseError
	if !errors.As(err, &typed) || typed.Code() != want {
		t.Fatalf("error = %T/%v, want %q", err, err, want)
	}
}

func assertSnapshotFrameErrorCode(t testing.TB, err error, want ipc.FrameErrorCode) {
	t.Helper()
	var typed *ipc.FrameError
	if !errors.As(err, &typed) || typed.Code() != want {
		t.Fatalf("error = %T/%v, want frame code %q", err, err, want)
	}
}

func snapshotResponseHeader(length uint32) []byte {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, length)
	return header
}
