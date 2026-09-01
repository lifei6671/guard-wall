package schema

import (
	"bytes"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	ipcV1MutationResponseMaxBytes  = 4 * 1024
	ipcV1MutationResponseMaxDepth  = 2
	ipcV1MutationResponseMaxTokens = 32
)

//go:embed ipc-v1-mutation-response.schema.json testdata/ipc-v1-mutation-response/cases.json testdata/ipc-v1-mutation-response/valid/*.json testdata/ipc-v1-mutation-response/invalid/*.json
var ipcV1MutationResponseFiles embed.FS

type ipcV1MutationResponseCases struct {
	Valid   []ipcV1MutationResponseCase `json:"valid"`
	Invalid []ipcV1MutationResponseCase `json:"invalid"`
}

type ipcV1MutationResponseCase struct {
	Path            string `json:"path"`
	Operation       string `json:"operation"`
	Status          string `json:"status"`
	Layer           string `json:"layer"`
	Classification  string `json:"classification"`
	FixtureEncoding string `json:"fixture_encoding"`
}

func TestIPCV1MutationResponseSchema(t *testing.T) {
	schemaBytes := readIPCV1MutationResponseFile(t, "ipc-v1-mutation-response.schema.json")
	if err := scanIPCV1JSON(schemaBytes); err != nil {
		t.Fatalf("scan schema: %v", err)
	}
	root := decodeIPCV1JSON(t, schemaBytes)

	if got := stringValue(root["$schema"]); got != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %q", got)
	}
	if got := stringValue(root["$id"]); got != "https://guard-wall.local/schema/ipc-v1-mutation-response.schema.json" {
		t.Fatalf("$id = %q", got)
	}
	assertJSONInteger(t, root, "x-guard-max-frame-bytes", 1<<20)
	assertJSONInteger(t, root, "x-guard-max-response-bytes", ipcV1MutationResponseMaxBytes)
	assertJSONInteger(t, root, "x-guard-max-instance-depth", ipcV1MutationResponseMaxDepth)
	assertJSONInteger(t, root, "x-guard-max-json-tokens", ipcV1MutationResponseMaxTokens)

	branches, ok := root["oneOf"].([]any)
	if !ok || len(branches) != 6 {
		t.Fatalf("root oneOf has %d branches, want 6", len(branches))
	}
	wantReferences := []string{
		"#/$defs/applyConfirmed",
		"#/$defs/applyRejected",
		"#/$defs/applyUnknown",
		"#/$defs/removeConfirmed",
		"#/$defs/removeRejected",
		"#/$defs/removeUnknown",
	}
	gotReferences := make([]string, 0, len(branches))
	for _, rawBranch := range branches {
		branch, ok := rawBranch.(map[string]any)
		if !ok || len(branch) != 1 {
			t.Fatalf("root oneOf branch = %#v, want one $ref", rawBranch)
		}
		gotReferences = append(gotReferences, stringValue(branch["$ref"]))
	}
	assertIPCV1MutationResponseStringSet(t, "root oneOf references", gotReferences, wantReferences)

	propertyNames := make(map[string]struct{})
	auditIPCV1SchemaNode(t, root, "#", propertyNames)
	gotProperties := make([]string, 0, len(propertyNames))
	for name := range propertyNames {
		gotProperties = append(gotProperties, name)
	}
	assertIPCV1MutationResponseStringSet(t, "wire properties", gotProperties, []string{
		"domain", "error_code", "operation", "status", "version",
	})

	assertIPCV1MutationResponseDefinitionEnum(t, root, "domain", []string{"infrastructure", "policy", "target"})
	assertIPCV1MutationResponseDefinitionEnum(t, root, "applyRejectedErrorCode", []string{
		"backend_rejected", "invalid_plan", "not_ready", "ownership_conflict", "unsupported",
	})
	assertIPCV1MutationResponseDefinitionEnum(t, root, "removeRejectedErrorCode", []string{
		"backend_rejected", "not_ready", "ownership_conflict", "unsupported",
	})
	unknownCode := ipcV1MutationResponseDefinition(t, root, "unknownErrorCode")
	if got := stringValue(unknownCode["const"]); got != "unknown_result" {
		t.Fatalf("unknown error code = %q, want unknown_result", got)
	}
}

func TestIPCV1MutationResponseGoldenVectors(t *testing.T) {
	root := decodeIPCV1JSON(t, readIPCV1MutationResponseFile(t, "ipc-v1-mutation-response.schema.json"))
	cases := readIPCV1MutationResponseCases(t)
	assertIPCV1MutationResponseCaseCoverage(t, cases)

	wantVariants := map[string]struct{}{
		"ApplyManagedPlan/confirmed/infrastructure": {},
		"ApplyManagedPlan/rejected/infrastructure":  {},
		"ApplyManagedPlan/unknown/infrastructure":   {},
		"ApplyManagedPlan/confirmed/policy":         {},
		"ApplyManagedPlan/rejected/policy":          {},
		"ApplyManagedPlan/unknown/policy":           {},
		"ApplyManagedPlan/confirmed/target":         {},
		"ApplyManagedPlan/rejected/target":          {},
		"ApplyManagedPlan/unknown/target":           {},
		"RemoveManagedInfrastructure/confirmed/-":   {},
		"RemoveManagedInfrastructure/rejected/-":    {},
		"RemoveManagedInfrastructure/unknown/-":     {},
	}
	gotVariants := make(map[string]struct{}, len(cases.Valid))

	for _, test := range cases.Valid {
		test := test
		t.Run("valid/"+strings.TrimSuffix(strings.TrimPrefix(test.Path, "valid/"), ".json"), func(t *testing.T) {
			raw := readIPCV1MutationResponseFile(t, "testdata/ipc-v1-mutation-response/"+test.Path)
			value, classification := validateIPCV1MutationResponse(raw, root)
			if classification != "" {
				t.Fatalf("valid response rejected with %s", classification)
			}
			if got := stringValue(value["operation"]); got != test.Operation {
				t.Fatalf("operation = %q, manifest wants %q", got, test.Operation)
			}
			if got := stringValue(value["status"]); got != test.Status {
				t.Fatalf("status = %q, manifest wants %q", got, test.Status)
			}
			domain := stringValue(value["domain"])
			if domain == "" {
				domain = "-"
			}
			variant := test.Operation + "/" + test.Status + "/" + domain
			if _, duplicate := gotVariants[variant]; duplicate {
				t.Fatalf("duplicate valid operation/status/domain variant %q", variant)
			}
			gotVariants[variant] = struct{}{}
			if matches := countIPCV1MutationResponseBranches(root, value); matches != 1 {
				t.Fatalf("valid response matches %d oneOf branches, want exactly 1", matches)
			}
		})
	}
	assertIPCV1MutationResponseKeySet(t, "valid operation/status/domain variants", gotVariants, wantVariants)

	for _, test := range cases.Invalid {
		test := test
		t.Run("invalid/"+strings.TrimSuffix(strings.TrimPrefix(test.Path, "invalid/"), ".json"), func(t *testing.T) {
			raw := readIPCV1MutationResponseCase(t, test)
			_, classification := validateIPCV1MutationResponse(raw, root)
			if classification != test.Classification {
				t.Fatalf("classification = %q, want %q", classification, test.Classification)
			}
		})
	}
}

func TestIPCV1MutationResponseErrorCodeMatrix(t *testing.T) {
	root := decodeIPCV1JSON(t, readIPCV1MutationResponseFile(t, "ipc-v1-mutation-response.schema.json"))
	applyRejectedCodes := []string{"invalid_plan", "ownership_conflict", "unsupported", "not_ready", "backend_rejected"}
	removeRejectedCodes := []string{"ownership_conflict", "unsupported", "not_ready", "backend_rejected"}

	for _, code := range applyRejectedCodes {
		raw := []byte(fmt.Sprintf(`{"version":1,"operation":"ApplyManagedPlan","status":"rejected","domain":"target","error_code":%q}`, code))
		assertIPCV1MutationResponseAccepted(t, root, "apply/rejected/"+code, raw)
	}
	for _, code := range removeRejectedCodes {
		raw := []byte(fmt.Sprintf(`{"version":1,"operation":"RemoveManagedInfrastructure","status":"rejected","error_code":%q}`, code))
		assertIPCV1MutationResponseAccepted(t, root, "remove/rejected/"+code, raw)
	}
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "apply unknown", raw: `{"version":1,"operation":"ApplyManagedPlan","status":"unknown","domain":"policy","error_code":"unknown_result"}`},
		{name: "remove unknown", raw: `{"version":1,"operation":"RemoveManagedInfrastructure","status":"unknown","error_code":"unknown_result"}`},
	} {
		assertIPCV1MutationResponseAccepted(t, root, test.name, []byte(test.raw))
	}

	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "remove invalid plan", raw: `{"version":1,"operation":"RemoveManagedInfrastructure","status":"rejected","error_code":"invalid_plan"}`},
		{name: "apply rejected unknown result", raw: `{"version":1,"operation":"ApplyManagedPlan","status":"rejected","domain":"target","error_code":"unknown_result"}`},
		{name: "remove rejected unknown result", raw: `{"version":1,"operation":"RemoveManagedInfrastructure","status":"rejected","error_code":"unknown_result"}`},
		{name: "apply unknown rejected code", raw: `{"version":1,"operation":"ApplyManagedPlan","status":"unknown","domain":"target","error_code":"backend_rejected"}`},
		{name: "unlisted error", raw: `{"version":1,"operation":"ApplyManagedPlan","status":"rejected","domain":"target","error_code":"raw_backend_failure"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, classification := validateIPCV1MutationResponse([]byte(test.raw), root); classification != "schema_rejected" {
				t.Fatalf("classification = %q, want schema_rejected", classification)
			}
		})
	}
}

func TestIPCV1MutationResponseResourceLimits(t *testing.T) {
	root := decodeIPCV1JSON(t, readIPCV1MutationResponseFile(t, "ipc-v1-mutation-response.schema.json"))
	base := readIPCV1MutationResponseFile(t, "testdata/ipc-v1-mutation-response/valid/remove-confirmed.json")
	exactBytes := append(append([]byte(nil), base...), bytes.Repeat([]byte(" "), ipcV1MutationResponseMaxBytes-len(base))...)
	if _, classification := validateIPCV1MutationResponse(exactBytes, root); classification != "" {
		t.Fatalf("exact response byte limit rejected with %s", classification)
	}
	if _, classification := validateIPCV1MutationResponse(append(exactBytes, ' '), root); classification != "response_too_large" {
		t.Fatalf("one-over response byte classification = %q, want response_too_large", classification)
	}

	exactDepth := []byte(`{"x":[]}`)
	if err := scanIPCV1MutationResponseJSON(exactDepth); err != nil {
		t.Fatalf("exact response depth rejected: %v", err)
	}
	oneOverDepth := []byte(`{"x":[[]]}`)
	if _, classification := validateIPCV1MutationResponse(oneOverDepth, root); classification != "max_depth_exceeded" {
		t.Fatalf("one-over response depth classification = %q, want max_depth_exceeded", classification)
	}

	exactTokens := ipcV1MutationResponseNullArray(27)
	if err := scanIPCV1MutationResponseJSON(exactTokens); err != nil {
		t.Fatalf("exact response token limit rejected: %v", err)
	}
	oneOverTokens := ipcV1MutationResponseNullArray(28)
	if _, classification := validateIPCV1MutationResponse(oneOverTokens, root); classification != "token_limit_exceeded" {
		t.Fatalf("one-over response token classification = %q, want token_limit_exceeded", classification)
	}
}

func FuzzIPCV1MutationResponseClosedUnion(f *testing.F) {
	root := decodeIPCV1JSON(f, readIPCV1MutationResponseFile(f, "ipc-v1-mutation-response.schema.json"))
	cases := readIPCV1MutationResponseCases(f)
	for _, test := range cases.Valid {
		f.Add(readIPCV1MutationResponseFile(f, "testdata/ipc-v1-mutation-response/"+test.Path))
	}
	for _, test := range cases.Invalid {
		switch test.Classification {
		case "duplicate_key", "invalid_utf8", "max_depth_exceeded", "token_limit_exceeded":
			f.Add(readIPCV1MutationResponseCase(f, test))
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, classification := validateIPCV1MutationResponse(raw, root)
		if classification != "" {
			return
		}
		operation := stringValue(value["operation"])
		status := stringValue(value["status"])
		switch operation {
		case "ApplyManagedPlan":
			switch stringValue(value["domain"]) {
			case "infrastructure", "policy", "target":
			default:
				t.Fatalf("accepted Apply response with domain %q", value["domain"])
			}
		case "RemoveManagedInfrastructure":
			if _, found := value["domain"]; found {
				t.Fatal("accepted Remove response with domain")
			}
		default:
			t.Fatalf("accepted operation %q outside mutation response union", operation)
		}
		switch status {
		case "confirmed", "rejected", "unknown":
		default:
			t.Fatalf("accepted status %q outside mutation response union", status)
		}
		if matches := countIPCV1MutationResponseBranches(root, value); matches != 1 {
			t.Fatalf("accepted response matches %d oneOf branches, want exactly 1", matches)
		}
	})
}

func validateIPCV1MutationResponse(raw []byte, root map[string]any) (map[string]any, string) {
	if len(raw) > ipcV1MutationResponseMaxBytes {
		return nil, "response_too_large"
	}
	if !utf8.Valid(raw) {
		return nil, "invalid_utf8"
	}
	if err := scanIPCV1MutationResponseJSON(raw); err != nil {
		switch {
		case strings.Contains(err.Error(), "duplicate key"):
			return nil, "duplicate_key"
		case strings.Contains(err.Error(), "maximum depth"):
			return nil, "max_depth_exceeded"
		case strings.Contains(err.Error(), "token limit"):
			return nil, "token_limit_exceeded"
		default:
			return nil, "invalid_json"
		}
	}
	value, err := decodeIPCV1Object(raw)
	if err != nil {
		return nil, "invalid_json"
	}
	if err := validateIPCV1SchemaValue(root, root, value, "$"); err != nil {
		return value, "schema_rejected"
	}
	return value, ""
}

func countIPCV1MutationResponseBranches(root, value map[string]any) int {
	branches, _ := root["oneOf"].([]any)
	matches := 0
	for _, rawBranch := range branches {
		branch, ok := rawBranch.(map[string]any)
		if ok && validateIPCV1SchemaValue(root, branch, value, "$") == nil {
			matches++
		}
	}
	return matches
}

func assertIPCV1MutationResponseAccepted(t *testing.T, root map[string]any, name string, raw []byte) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		value, classification := validateIPCV1MutationResponse(raw, root)
		if classification != "" {
			t.Fatalf("valid response rejected with %s", classification)
		}
		if matches := countIPCV1MutationResponseBranches(root, value); matches != 1 {
			t.Fatalf("valid response matches %d oneOf branches, want exactly 1", matches)
		}
	})
}

func assertIPCV1MutationResponseCaseCoverage(t *testing.T, cases ipcV1MutationResponseCases) {
	t.Helper()
	listed := make(map[string]struct{}, len(cases.Valid)+len(cases.Invalid))
	for _, test := range cases.Valid {
		if test.Path == "" || test.Operation == "" || test.Status == "" || test.Layer != "schema" || test.Classification != "" || test.FixtureEncoding != "" {
			t.Fatalf("invalid valid-case manifest entry %#v", test)
		}
		if _, duplicate := listed[test.Path]; duplicate {
			t.Fatalf("duplicate case path %s", test.Path)
		}
		listed[test.Path] = struct{}{}
	}
	for _, test := range cases.Invalid {
		if test.Path == "" || test.Layer == "" || test.Classification == "" {
			t.Fatalf("incomplete invalid-case manifest entry %#v", test)
		}
		switch test.Layer {
		case "schema":
			if test.Classification != "schema_rejected" || test.FixtureEncoding != "" {
				t.Fatalf("schema case %s has classification/encoding %q/%q", test.Path, test.Classification, test.FixtureEncoding)
			}
		case "decoder":
			if test.Classification != "duplicate_key" && test.Classification != "invalid_json" && test.Classification != "invalid_utf8" {
				t.Fatalf("decoder case %s has classification %q", test.Path, test.Classification)
			}
		case "resource":
			if test.Classification != "response_too_large" && test.Classification != "max_depth_exceeded" && test.Classification != "token_limit_exceeded" {
				t.Fatalf("resource case %s has classification %q", test.Path, test.Classification)
			}
		default:
			t.Fatalf("invalid rejection layer %q for %s", test.Layer, test.Path)
		}
		if test.FixtureEncoding != "" && test.FixtureEncoding != "generated_from_hex_descriptor" {
			t.Fatalf("invalid fixture encoding %q for %s", test.FixtureEncoding, test.Path)
		}
		if test.FixtureEncoding == "generated_from_hex_descriptor" && test.Classification != "invalid_utf8" {
			t.Fatalf("encoded fixture %s has classification %q, want invalid_utf8", test.Path, test.Classification)
		}
		if _, duplicate := listed[test.Path]; duplicate {
			t.Fatalf("duplicate case path %s", test.Path)
		}
		listed[test.Path] = struct{}{}
	}
	for _, pattern := range []string{
		"testdata/ipc-v1-mutation-response/valid/*.json",
		"testdata/ipc-v1-mutation-response/invalid/*.json",
	} {
		matches, err := fs.Glob(ipcV1MutationResponseFiles, pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			relative := strings.TrimPrefix(match, "testdata/ipc-v1-mutation-response/")
			if _, found := listed[relative]; !found {
				t.Errorf("golden vector %s is not listed in cases.json", relative)
			}
		}
	}
}

func readIPCV1MutationResponseCases(tb testing.TB) ipcV1MutationResponseCases {
	tb.Helper()
	var cases ipcV1MutationResponseCases
	decodeIPCV1Into(tb, readIPCV1MutationResponseFile(tb, "testdata/ipc-v1-mutation-response/cases.json"), &cases)
	return cases
}

func readIPCV1MutationResponseCase(tb testing.TB, test ipcV1MutationResponseCase) []byte {
	tb.Helper()
	raw := readIPCV1MutationResponseFile(tb, "testdata/ipc-v1-mutation-response/"+test.Path)
	if test.FixtureEncoding == "" {
		return raw
	}
	var descriptor struct {
		Generator string `json:"generator"`
		Template  string `json:"template"`
		Marker    string `json:"marker"`
		BytesHex  string `json:"bytes_hex"`
	}
	decodeIPCV1Into(tb, raw, &descriptor)
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
	return bytes.Replace([]byte(descriptor.Template), []byte(descriptor.Marker), replacement, 1)
}

func readIPCV1MutationResponseFile(tb testing.TB, name string) []byte {
	tb.Helper()
	contents, err := ipcV1MutationResponseFiles.ReadFile(name)
	if err != nil {
		tb.Fatalf("read %s: %v", name, err)
	}
	return contents
}

func ipcV1MutationResponseDefinition(t *testing.T, root map[string]any, name string) map[string]any {
	t.Helper()
	definitions, ok := root["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema has no $defs object")
	}
	definition, ok := definitions[name].(map[string]any)
	if !ok {
		t.Fatalf("schema has no definition %q", name)
	}
	return definition
}

func assertIPCV1MutationResponseDefinitionEnum(t *testing.T, root map[string]any, name string, want []string) {
	t.Helper()
	definition := ipcV1MutationResponseDefinition(t, root, name)
	rawValues, ok := definition["enum"].([]any)
	if !ok {
		t.Fatalf("definition %q has no enum", name)
	}
	assertIPCV1MutationResponseStringSet(t, name+" enum", stringSlice(rawValues), want)
}

func assertIPCV1MutationResponseStringSet(t *testing.T, name string, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
		if index > 0 && got[index] == got[index-1] {
			t.Fatalf("%s contains duplicate %q", name, got[index])
		}
	}
}

func assertIPCV1MutationResponseKeySet(t *testing.T, name string, got, want map[string]struct{}) {
	t.Helper()
	gotKeys := make([]string, 0, len(got))
	for key := range got {
		gotKeys = append(gotKeys, key)
	}
	wantKeys := make([]string, 0, len(want))
	for key := range want {
		wantKeys = append(wantKeys, key)
	}
	assertIPCV1MutationResponseStringSet(t, name, gotKeys, wantKeys)
}

type ipcV1MutationResponseScan struct {
	depth  int
	tokens int
}

func scanIPCV1MutationResponseJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &ipcV1MutationResponseScan{}
	if err := scanIPCV1MutationResponseValue(decoder, state, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func scanIPCV1MutationResponseValue(decoder *json.Decoder, state *ipcV1MutationResponseScan, parentDepth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := countIPCV1MutationResponseToken(state); err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	depth := parentDepth + 1
	if depth > ipcV1MutationResponseMaxDepth {
		return fmt.Errorf("maximum depth exceeded")
	}
	if depth > state.depth {
		state.depth = depth
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := countIPCV1MutationResponseToken(state); err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanIPCV1MutationResponseValue(decoder, state, depth); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanIPCV1MutationResponseValue(decoder, state, depth); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := countIPCV1MutationResponseToken(state); err != nil {
		return err
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return fmt.Errorf("closing delimiter = %q, want %q", closing, want)
	}
	return nil
}

func countIPCV1MutationResponseToken(state *ipcV1MutationResponseScan) error {
	state.tokens++
	if state.tokens > ipcV1MutationResponseMaxTokens {
		return fmt.Errorf("token limit exceeded")
	}
	return nil
}

func ipcV1MutationResponseNullArray(items int) []byte {
	if items == 0 {
		return []byte(`{"x":[]}`)
	}
	return []byte(`{"x":[` + strings.Repeat("null,", items-1) + "null]}")
}
