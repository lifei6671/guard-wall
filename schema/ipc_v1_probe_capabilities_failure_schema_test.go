package schema

import (
	"bytes"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	probeCapabilitiesFailureMaxBytes  = 4 * 1024
	probeCapabilitiesFailureMaxDepth  = 2
	probeCapabilitiesFailureMaxTokens = 64
)

var probeCapabilitiesFailureKeys = []string{"version", "operation", "error_code"}

//go:embed ipc-v1-probe-capabilities-failure.schema.json testdata/ipc-v1-probe-capabilities-failure/cases.json testdata/ipc-v1-probe-capabilities-failure/valid/*.json testdata/ipc-v1-probe-capabilities-failure/invalid/*.json
var probeCapabilitiesFailureFiles embed.FS

type probeCapabilitiesFailureCases struct {
	Valid   []probeCapabilitiesFailureCase `json:"valid"`
	Invalid []probeCapabilitiesFailureCase `json:"invalid"`
}

type probeCapabilitiesFailureCase struct {
	Path            string `json:"path"`
	Layer           string `json:"layer"`
	Classification  string `json:"classification"`
	FixtureEncoding string `json:"fixture_encoding"`
}

func TestProbeCapabilitiesFailureSchema(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFailureFile(t, "ipc-v1-probe-capabilities-failure.schema.json"))

	if got := stringValue(root["$schema"]); got != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %q", got)
	}
	if got := stringValue(root["$id"]); got != "https://guard-wall.local/schema/ipc-v1-probe-capabilities-failure.schema.json" {
		t.Fatalf("$id = %q", got)
	}
	assertJSONInteger(t, root, "x-guard-max-frame-bytes", 1<<20)
	assertJSONInteger(t, root, "x-guard-max-response-bytes", probeCapabilitiesFailureMaxBytes)
	assertJSONInteger(t, root, "x-guard-max-instance-depth", probeCapabilitiesFailureMaxDepth)
	assertJSONInteger(t, root, "x-guard-max-json-tokens", probeCapabilitiesFailureMaxTokens)

	assertProbeCapabilitiesFailureObject(t, root, probeCapabilitiesFailureKeys, 3)
	properties := probeCapabilitiesFailureProperties(t, root)
	version := probeCapabilitiesFailureProperty(t, properties, "version")
	if stringValue(version["type"]) != "integer" {
		t.Fatalf("version type = %#v", version["type"])
	}
	for _, key := range []string{"const", "minimum", "maximum"} {
		assertJSONInteger(t, version, key, 1)
	}
	operation := probeCapabilitiesFailureProperty(t, properties, "operation")
	if stringValue(operation["type"]) != "string" || stringValue(operation["const"]) != "ProbeCapabilities" {
		t.Fatalf("operation schema = %#v", operation)
	}
	assertJSONInteger(t, operation, "maxLength", 32)
	errorCode := probeCapabilitiesFailureProperty(t, properties, "error_code")
	if stringValue(errorCode["type"]) != "string" {
		t.Fatalf("error_code type = %#v", errorCode["type"])
	}
	enumeration, ok := errorCode["enum"].([]any)
	if !ok || !sameProbeCapabilitiesFailureStrings(stringSlice(enumeration), []string{"unsupported", "not_ready"}) {
		t.Fatalf("error_code enum = %#v", errorCode["enum"])
	}
	assertJSONInteger(t, errorCode, "maxLength", 32)

	forbidden := []string{"status", "payload", "message", "details", "cause"}
	for _, key := range forbidden {
		if _, found := properties[key]; found {
			t.Fatalf("forbidden property %q is defined", key)
		}
	}
}

func TestProbeCapabilitiesFailureGoldenVectors(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFailureFile(t, "ipc-v1-probe-capabilities-failure.schema.json"))
	cases := readProbeCapabilitiesFailureCases(t)
	assertProbeCapabilitiesFailureCaseCoverage(t, cases)

	for _, test := range cases.Valid {
		test := test
		t.Run("valid/"+strings.TrimSuffix(strings.TrimPrefix(test.Path, "valid/"), ".json"), func(t *testing.T) {
			raw := readProbeCapabilitiesFailureFile(t, "testdata/ipc-v1-probe-capabilities-failure/"+test.Path)
			value, classification := validateProbeCapabilitiesFailure(raw, root)
			if classification != "" {
				t.Fatalf("valid response rejected with %s", classification)
			}
			assertProbeCapabilitiesFailureCanonicalOrder(t, raw)
			if stringValue(value["operation"]) != "ProbeCapabilities" {
				t.Fatalf("operation = %#v", value["operation"])
			}
			code := stringValue(value["error_code"])
			if code != "unsupported" && code != "not_ready" {
				t.Fatalf("error_code = %q", code)
			}
		})
	}

	for _, test := range cases.Invalid {
		test := test
		t.Run("invalid/"+strings.TrimSuffix(strings.TrimPrefix(test.Path, "invalid/"), ".json"), func(t *testing.T) {
			raw := readProbeCapabilitiesFailureCase(t, test)
			_, classification := validateProbeCapabilitiesFailure(raw, root)
			if classification != test.Classification {
				t.Fatalf("classification = %q, want %q", classification, test.Classification)
			}
		})
	}
}

func TestProbeCapabilitiesFailureRequiredFieldsAndTypes(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFailureFile(t, "ipc-v1-probe-capabilities-failure.schema.json"))
	valid := decodeIPCV1JSON(t, readProbeCapabilitiesFailureFile(t, "testdata/ipc-v1-probe-capabilities-failure/valid/unsupported.json"))

	for _, key := range probeCapabilitiesFailureKeys {
		t.Run("missing/"+key, func(t *testing.T) {
			candidate := cloneProbeCapabilitiesFailureJSON(t, valid)
			delete(candidate, key)
			assertProbeCapabilitiesFailureClassification(t, root, candidate, "schema_rejected")
		})
	}
	for _, test := range []struct {
		name  string
		key   string
		value any
	}{
		{name: "version string", key: "version", value: "1"},
		{name: "operation boolean", key: "operation", value: true},
		{name: "error code boolean", key: "error_code", value: true},
		{name: "error code null", key: "error_code", value: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneProbeCapabilitiesFailureJSON(t, valid)
			candidate[test.key] = test.value
			assertProbeCapabilitiesFailureClassification(t, root, candidate, "schema_rejected")
		})
	}
}

func TestProbeCapabilitiesFailureResourceLimits(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFailureFile(t, "ipc-v1-probe-capabilities-failure.schema.json"))
	base := readProbeCapabilitiesFailureFile(t, "testdata/ipc-v1-probe-capabilities-failure/valid/unsupported.json")
	exactBytes := append(append([]byte(nil), base...), bytes.Repeat([]byte(" "), probeCapabilitiesFailureMaxBytes-len(base))...)
	if _, classification := validateProbeCapabilitiesFailure(exactBytes, root); classification != "" {
		t.Fatalf("exact byte limit rejected with %s", classification)
	}
	oneOver := readProbeCapabilitiesFailureCase(t, probeCapabilitiesFailureCase{
		Path:            "invalid/oversized-response.json",
		FixtureEncoding: "generated_padded_template",
	})
	if len(oneOver) != probeCapabilitiesFailureMaxBytes+1 {
		t.Fatalf("one-over fixture length = %d", len(oneOver))
	}
	if _, classification := validateProbeCapabilitiesFailure(oneOver, root); classification != "response_too_large" {
		t.Fatalf("one-over byte limit = %q, want response_too_large", classification)
	}

	if err := scanProbeCapabilitiesFailureJSON([]byte(`{"x":[]}`)); err != nil {
		t.Fatalf("exact depth rejected: %v", err)
	}
	if _, classification := validateProbeCapabilitiesFailure([]byte(`{"x":[[]]}`), root); classification != "max_depth_exceeded" {
		t.Fatalf("one-over depth = %q, want max_depth_exceeded", classification)
	}

	exactTokens := probeCapabilitiesFailureNullArray(59)
	if err := scanProbeCapabilitiesFailureJSON(exactTokens); err != nil {
		t.Fatalf("exact token limit rejected: %v", err)
	}
	if _, classification := validateProbeCapabilitiesFailure(probeCapabilitiesFailureNullArray(60), root); classification != "token_limit_exceeded" {
		t.Fatalf("one-over token limit = %q, want token_limit_exceeded", classification)
	}
}

func TestProbeCapabilitiesFailureClassificationIsSanitized(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFailureFile(t, "ipc-v1-probe-capabilities-failure.schema.json"))
	secret := "do-not-echo-secret"
	raw := []byte(`{"version":1,"operation":"ProbeCapabilities","error_code":"unsupported","message":"` + secret + `"}`)
	_, classification := validateProbeCapabilitiesFailure(raw, root)
	if classification != "schema_rejected" {
		t.Fatalf("classification = %q", classification)
	}
	if strings.Contains(classification, secret) || strings.Contains(classification, "message") {
		t.Fatalf("classification leaks input detail: %q", classification)
	}
}

func FuzzProbeCapabilitiesFailureClosedContract(f *testing.F) {
	root := decodeIPCV1JSON(f, readProbeCapabilitiesFailureFile(f, "ipc-v1-probe-capabilities-failure.schema.json"))
	cases := readProbeCapabilitiesFailureCases(f)
	for _, test := range cases.Valid {
		f.Add(readProbeCapabilitiesFailureFile(f, "testdata/ipc-v1-probe-capabilities-failure/"+test.Path))
	}
	for _, test := range cases.Invalid {
		if test.Classification == "duplicate_key" || test.Classification == "invalid_utf8" {
			f.Add(readProbeCapabilitiesFailureCase(f, test))
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, classification := validateProbeCapabilitiesFailure(raw, root)
		if classification != "" {
			return
		}
		if len(value) != 3 {
			t.Fatalf("accepted root properties = %d", len(value))
		}
		version, ok := jsonInt64(value["version"])
		if !ok || version != 1 || stringValue(value["operation"]) != "ProbeCapabilities" {
			t.Fatalf("accepted invalid root %#v", value)
		}
		code := stringValue(value["error_code"])
		if code != "unsupported" && code != "not_ready" {
			t.Fatalf("accepted error_code %q", code)
		}
	})
}

func validateProbeCapabilitiesFailure(raw []byte, root map[string]any) (map[string]any, string) {
	if len(raw) > probeCapabilitiesFailureMaxBytes {
		return nil, "response_too_large"
	}
	if !utf8.Valid(raw) {
		return nil, "invalid_utf8"
	}
	if err := scanProbeCapabilitiesFailureJSON(raw); err != nil {
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, "invalid_json"
	}
	value, ok := decoded.(map[string]any)
	if !ok {
		return nil, "schema_rejected"
	}
	if err := validateProbeCapabilitiesFailureSchemaValue(root, value, "$"); err != nil {
		return value, "schema_rejected"
	}
	return value, ""
}

func validateProbeCapabilitiesFailureSchemaValue(schema map[string]any, value any, path string) error {
	if constant, ok := schema["const"]; ok && !equalJSONValue(constant, value) {
		return fmt.Errorf("%s does not equal const", path)
	}
	if enumeration, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enumeration {
			if equalJSONValue(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is outside enum", path)
		}
	}

	switch stringValue(schema["type"]) {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not an object", path)
		}
		if maximum, ok := jsonInt64(schema["maxProperties"]); ok && int64(len(object)) > maximum {
			return fmt.Errorf("%s has too many properties", path)
		}
		if required, ok := schema["required"].([]any); ok {
			for _, key := range stringSlice(required) {
				if _, found := object[key]; !found {
					return fmt.Errorf("%s is missing %s", path, key)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for key, child := range object {
			rawChild, found := properties[key]
			if !found {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("%s contains unknown property %s", path, key)
				}
				continue
			}
			childSchema, ok := rawChild.(map[string]any)
			if !ok {
				return fmt.Errorf("%s schema is not an object", path+"."+key)
			}
			if err := validateProbeCapabilitiesFailureSchemaValue(childSchema, child, path+"."+key); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s is not a string", path)
		}
		if maximum, ok := jsonInt64(schema["maxLength"]); ok && int64(len(text)) > maximum {
			return fmt.Errorf("%s is too long", path)
		}
	case "integer":
		integer, ok := jsonInt64(value)
		if !ok {
			return fmt.Errorf("%s is not an integer", path)
		}
		if minimum, ok := jsonInt64(schema["minimum"]); ok && integer < minimum {
			return fmt.Errorf("%s is below minimum", path)
		}
		if maximum, ok := jsonInt64(schema["maximum"]); ok && integer > maximum {
			return fmt.Errorf("%s exceeds maximum", path)
		}
	default:
		return fmt.Errorf("unsupported schema type %q at %s", schema["type"], path)
	}
	return nil
}

func assertProbeCapabilitiesFailureObject(t *testing.T, object map[string]any, required []string, maxProperties int64) {
	t.Helper()
	if stringValue(object["type"]) != "object" || object["additionalProperties"] != false {
		t.Fatalf("object contract = %#v", object)
	}
	assertJSONInteger(t, object, "maxProperties", maxProperties)
	rawRequired, ok := object["required"].([]any)
	if !ok || !sameProbeCapabilitiesFailureStrings(stringSlice(rawRequired), required) {
		t.Fatalf("required = %#v, want %v", object["required"], required)
	}
	properties := probeCapabilitiesFailureProperties(t, object)
	got := make([]string, 0, len(properties))
	for key := range properties {
		got = append(got, key)
	}
	if !sameProbeCapabilitiesFailureStrings(got, required) {
		t.Fatalf("properties = %v, want %v", got, required)
	}
}

func probeCapabilitiesFailureProperties(t *testing.T, object map[string]any) map[string]any {
	t.Helper()
	properties, ok := object["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", object["properties"])
	}
	return properties
}

func probeCapabilitiesFailureProperty(t *testing.T, properties map[string]any, key string) map[string]any {
	t.Helper()
	property, ok := properties[key].(map[string]any)
	if !ok {
		t.Fatalf("property %s = %#v", key, properties[key])
	}
	return property
}

func sameProbeCapabilitiesFailureStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, item := range want {
		counts[item]++
	}
	for _, item := range got {
		counts[item]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func assertProbeCapabilitiesFailureCanonicalOrder(t *testing.T, raw []byte) {
	t.Helper()
	last := -1
	for _, key := range probeCapabilitiesFailureKeys {
		index := bytes.Index(raw, []byte(`"`+key+`"`))
		if index <= last {
			t.Fatalf("key %q is not in canonical order", key)
		}
		last = index
	}
}

func assertProbeCapabilitiesFailureClassification(t *testing.T, root map[string]any, value map[string]any, want string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, got := validateProbeCapabilitiesFailure(raw, root)
	if got != want {
		t.Fatalf("classification = %q, want %q", got, want)
	}
}

func cloneProbeCapabilitiesFailureJSON(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return decodeIPCV1JSON(t, raw)
}

func readProbeCapabilitiesFailureCases(tb testing.TB) probeCapabilitiesFailureCases {
	tb.Helper()
	var cases probeCapabilitiesFailureCases
	decodeIPCV1Into(tb, readProbeCapabilitiesFailureFile(tb, "testdata/ipc-v1-probe-capabilities-failure/cases.json"), &cases)
	return cases
}

func assertProbeCapabilitiesFailureCaseCoverage(t *testing.T, cases probeCapabilitiesFailureCases) {
	t.Helper()
	listed := make(map[string]struct{}, len(cases.Valid)+len(cases.Invalid))
	for _, test := range cases.Valid {
		if test.Path == "" || test.Layer != "schema" || test.Classification != "" || test.FixtureEncoding != "" {
			t.Fatalf("invalid valid-case entry %#v", test)
		}
		if _, duplicate := listed[test.Path]; duplicate {
			t.Fatalf("duplicate case path %s", test.Path)
		}
		listed[test.Path] = struct{}{}
	}
	for _, test := range cases.Invalid {
		if test.Path == "" || test.Layer == "" || test.Classification == "" {
			t.Fatalf("incomplete invalid-case entry %#v", test)
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
			t.Fatalf("unknown rejection layer %q for %s", test.Layer, test.Path)
		}
		if test.FixtureEncoding != "" && test.FixtureEncoding != "generated_from_hex_descriptor" && test.FixtureEncoding != "generated_padded_template" {
			t.Fatalf("unknown fixture encoding %q", test.FixtureEncoding)
		}
		if _, duplicate := listed[test.Path]; duplicate {
			t.Fatalf("duplicate case path %s", test.Path)
		}
		listed[test.Path] = struct{}{}
	}
	for _, pattern := range []string{
		"testdata/ipc-v1-probe-capabilities-failure/valid/*.json",
		"testdata/ipc-v1-probe-capabilities-failure/invalid/*.json",
	} {
		matches, err := fs.Glob(probeCapabilitiesFailureFiles, pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			relative := strings.TrimPrefix(match, "testdata/ipc-v1-probe-capabilities-failure/")
			if _, found := listed[relative]; !found {
				t.Errorf("golden vector %s is not listed", relative)
			}
		}
	}
}

func readProbeCapabilitiesFailureCase(tb testing.TB, test probeCapabilitiesFailureCase) []byte {
	tb.Helper()
	raw := readProbeCapabilitiesFailureFile(tb, "testdata/ipc-v1-probe-capabilities-failure/"+test.Path)
	switch test.FixtureEncoding {
	case "":
		return raw
	case "generated_from_hex_descriptor":
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
			tb.Fatal(err)
		}
		return bytes.Replace([]byte(descriptor.Template), []byte(descriptor.Marker), replacement, 1)
	case "generated_padded_template":
		var descriptor struct {
			Generator   string `json:"generator"`
			Template    string `json:"template"`
			TargetBytes int    `json:"target_bytes"`
		}
		decodeIPCV1Into(tb, raw, &descriptor)
		if descriptor.Generator != "pad_template_to_bytes" || descriptor.TargetBytes < len(descriptor.Template) {
			tb.Fatalf("invalid padded fixture descriptor for %s", test.Path)
		}
		return append([]byte(descriptor.Template), bytes.Repeat([]byte(" "), descriptor.TargetBytes-len(descriptor.Template))...)
	default:
		tb.Fatalf("unknown fixture encoding %q", test.FixtureEncoding)
		return nil
	}
}

func readProbeCapabilitiesFailureFile(tb testing.TB, name string) []byte {
	tb.Helper()
	contents, err := probeCapabilitiesFailureFiles.ReadFile(name)
	if err != nil {
		tb.Fatalf("read %s: %v", name, err)
	}
	return contents
}

type probeCapabilitiesFailureScan struct {
	tokens int
}

func scanProbeCapabilitiesFailureJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &probeCapabilitiesFailureScan{}
	if err := scanProbeCapabilitiesFailureValue(decoder, state, 0); err != nil {
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

func scanProbeCapabilitiesFailureValue(decoder *json.Decoder, state *probeCapabilitiesFailureScan, parentDepth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := countProbeCapabilitiesFailureToken(state); err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	depth := parentDepth + 1
	if depth > probeCapabilitiesFailureMaxDepth {
		return fmt.Errorf("maximum depth exceeded")
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := countProbeCapabilitiesFailureToken(state); err != nil {
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
			if err := scanProbeCapabilitiesFailureValue(decoder, state, depth); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanProbeCapabilitiesFailureValue(decoder, state, depth); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return countProbeCapabilitiesFailureToken(state)
}

func countProbeCapabilitiesFailureToken(state *probeCapabilitiesFailureScan) error {
	state.tokens++
	if state.tokens > probeCapabilitiesFailureMaxTokens {
		return fmt.Errorf("token limit exceeded")
	}
	return nil
}

func probeCapabilitiesFailureNullArray(items int) []byte {
	if items == 0 {
		return []byte(`{"x":[]}`)
	}
	return []byte(`{"x":[` + strings.Repeat("null,", items-1) + "null]}")
}
