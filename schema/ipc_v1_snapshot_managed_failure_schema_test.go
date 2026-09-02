package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

var snapshotManagedFailureKeys = []string{"version", "operation", "error_code"}

//go:embed ipc-v1-snapshot-managed-failure.schema.json testdata/ipc-v1-snapshot-managed-failure/cases.json testdata/ipc-v1-snapshot-managed-failure/valid/*.json testdata/ipc-v1-snapshot-managed-failure/invalid/*.json
var snapshotManagedFailureFiles embed.FS

func TestSnapshotManagedFailureSchema(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedFailureFile(t, "ipc-v1-snapshot-managed-failure.schema.json"))
	if stringValue(root["$schema"]) != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %#v", root["$schema"])
	}
	if stringValue(root["$id"]) != "https://guard-wall.local/schema/ipc-v1-snapshot-managed-failure.schema.json" {
		t.Fatalf("$id = %#v", root["$id"])
	}
	assertJSONInteger(t, root, "x-guard-max-frame-bytes", snapshotManagedMaxBytes)
	assertJSONInteger(t, root, "x-guard-max-response-bytes", snapshotManagedMaxBytes)
	assertJSONInteger(t, root, "x-guard-max-instance-depth", snapshotManagedMaxDepth)
	assertJSONInteger(t, root, "x-guard-max-json-tokens", snapshotManagedMaxTokens)
	assertSnapshotObject(t, root, snapshotManagedFailureKeys, 3)
	properties := snapshotProperties(t, root)
	assertSnapshotConst(t, properties["version"], "integer", json.Number("1"))
	assertSnapshotConst(t, properties["operation"], "string", "SnapshotManaged")
	errorCode := snapshotMap(t, properties["error_code"], "error_code")
	if stringValue(errorCode["type"]) != "string" || !sameSnapshotStrings(stringSlice(errorCode["enum"].([]any)), []string{"unsupported", "not_ready", "ownership_conflict"}) {
		t.Fatalf("error_code schema = %#v", errorCode)
	}
	assertJSONInteger(t, errorCode, "maxLength", 32)
	for _, forbidden := range []string{"status", "payload", "message", "details", "cause", "error", "command", "binary", "env", "cwd", "object_name"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("forbidden property %q", forbidden)
		}
	}
}

func TestSnapshotManagedFailureGoldenVectors(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedFailureFile(t, "ipc-v1-snapshot-managed-failure.schema.json"))
	cases := readSnapshotManagedFailureCases(t)
	assertSnapshotCaseCoverage(t, cases, snapshotManagedFailureFiles, "ipc-v1-snapshot-managed-failure")
	for _, test := range cases.Valid {
		test := test
		t.Run(test.Path, func(t *testing.T) {
			raw := readSnapshotManagedFailureFile(t, "testdata/ipc-v1-snapshot-managed-failure/"+test.Path)
			value, classification := validateSnapshotManagedFailure(raw, root)
			if classification != "" {
				t.Fatalf("valid failure rejected with %s", classification)
			}
			assertSnapshotFailureCanonicalOrder(t, raw)
			if len(value) != 3 {
				t.Fatalf("root fields = %d", len(value))
			}
		})
	}
	for _, test := range cases.Invalid {
		test := test
		t.Run(test.Path, func(t *testing.T) {
			raw := readSnapshotManagedCase(t, snapshotManagedFailureFiles, "ipc-v1-snapshot-managed-failure", test)
			_, classification := validateSnapshotManagedFailure(raw, root)
			if classification != test.Classification {
				t.Fatalf("classification = %q, want %q", classification, test.Classification)
			}
		})
	}
}

func TestSnapshotManagedFailureRequiredAndTypeConfusion(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedFailureFile(t, "ipc-v1-snapshot-managed-failure.schema.json"))
	valid := decodeIPCV1JSON(t, readSnapshotManagedFailureFile(t, "testdata/ipc-v1-snapshot-managed-failure/valid/unsupported.json"))
	for _, key := range snapshotManagedFailureKeys {
		t.Run("missing "+key, func(t *testing.T) {
			candidate := cloneSnapshotJSON(t, valid)
			delete(candidate, key)
			assertSnapshotFailureClassification(t, root, candidate, "schema_rejected")
		})
	}
	for _, test := range []struct {
		name, key string
		value     any
	}{
		{name: "version string", key: "version", value: "1"},
		{name: "operation boolean", key: "operation", value: true},
		{name: "error code boolean", key: "error_code", value: true},
		{name: "error code null", key: "error_code", value: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneSnapshotJSON(t, valid)
			candidate[test.key] = test.value
			assertSnapshotFailureClassification(t, root, candidate, "schema_rejected")
		})
	}
}

func TestSnapshotManagedFailureResourceLimits(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedFailureFile(t, "ipc-v1-snapshot-managed-failure.schema.json"))
	base := readSnapshotManagedFailureFile(t, "testdata/ipc-v1-snapshot-managed-failure/valid/unsupported.json")
	exactBytes := append(append([]byte(nil), base...), bytes.Repeat([]byte(" "), snapshotManagedMaxBytes-len(base))...)
	if _, got := validateSnapshotManagedFailure(exactBytes, root); got != "" {
		t.Fatalf("exact bytes rejected: %s", got)
	}
	if _, got := validateSnapshotManagedFailure(append(exactBytes, ' '), root); got != "response_too_large" {
		t.Fatalf("one-over bytes = %q", got)
	}
	if err := scanSnapshotJSON([]byte(`{"x":[{"y":[]}]}`)); err != nil {
		t.Fatalf("exact depth rejected: %v", err)
	}
	if _, got := validateSnapshotManagedFailure([]byte(`{"x":[{"y":[[]]}]}`), root); got != "max_depth_exceeded" {
		t.Fatalf("one-over depth = %q", got)
	}
	exactTokens := snapshotNullArray(snapshotManagedMaxTokens - 2)
	if err := scanSnapshotJSON(exactTokens); err != nil {
		t.Fatalf("exact tokens rejected: %v", err)
	}
	if _, got := validateSnapshotManagedFailure(snapshotNullArray(snapshotManagedMaxTokens-1), root); got != "token_limit_exceeded" {
		t.Fatalf("one-over tokens = %q", got)
	}
}

func TestSnapshotManagedFailureClassificationIsSanitized(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedFailureFile(t, "ipc-v1-snapshot-managed-failure.schema.json"))
	secret := "do-not-echo-secret"
	raw := []byte(`{"version":1,"operation":"SnapshotManaged","error_code":"unsupported","message":"` + secret + `"}`)
	_, classification := validateSnapshotManagedFailure(raw, root)
	if classification != "schema_rejected" || strings.Contains(classification, secret) || strings.Contains(classification, "message") {
		t.Fatalf("unsafe classification %q", classification)
	}
}

func FuzzSnapshotManagedFailureClosedContract(f *testing.F) {
	root := decodeIPCV1JSON(f, readSnapshotManagedFailureFile(f, "ipc-v1-snapshot-managed-failure.schema.json"))
	cases := readSnapshotManagedFailureCases(f)
	for _, test := range cases.Valid {
		f.Add(readSnapshotManagedFailureFile(f, "testdata/ipc-v1-snapshot-managed-failure/"+test.Path))
	}
	for _, test := range cases.Invalid {
		if test.Classification == "duplicate_key" || test.Classification == "invalid_utf8" {
			f.Add(readSnapshotManagedCase(f, snapshotManagedFailureFiles, "ipc-v1-snapshot-managed-failure", test))
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, classification := validateSnapshotManagedFailure(raw, root)
		if classification != "" {
			return
		}
		if len(value) != 3 || stringValue(value["operation"]) != "SnapshotManaged" {
			t.Fatalf("accepted invalid failure %#v", value)
		}
		code := stringValue(value["error_code"])
		if code != "unsupported" && code != "not_ready" && code != "ownership_conflict" {
			t.Fatalf("accepted code %q", code)
		}
	})
}

func validateSnapshotManagedFailure(raw []byte, root map[string]any) (map[string]any, string) {
	return validateSnapshotResponse(raw, root, nil)
}

func assertSnapshotFailureClassification(t *testing.T, root, value map[string]any, want string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, got := validateSnapshotManagedFailure(raw, root)
	if got != want {
		t.Fatalf("classification = %q, want %q", got, want)
	}
}

func assertSnapshotFailureCanonicalOrder(t *testing.T, raw []byte) {
	t.Helper()
	markers := []string{`"version":`, `"operation":`, `"error_code":`}
	positions := make([]int, len(markers))
	for i, marker := range markers {
		positions[i] = bytes.Index(raw, []byte(marker))
		if positions[i] < 0 {
			t.Fatalf("missing %s", marker)
		}
	}
	if !sort.IntsAreSorted(positions) {
		t.Fatalf("noncanonical field order %v", positions)
	}
}

func readSnapshotManagedFailureCases(tb testing.TB) snapshotManagedCases {
	tb.Helper()
	var cases snapshotManagedCases
	decodeIPCV1Into(tb, readSnapshotManagedFailureFile(tb, "testdata/ipc-v1-snapshot-managed-failure/cases.json"), &cases)
	return cases
}
func readSnapshotManagedFailureFile(tb testing.TB, name string) []byte {
	tb.Helper()
	raw, err := snapshotManagedFailureFiles.ReadFile(name)
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}
