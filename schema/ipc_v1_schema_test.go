package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	ipcV1MaxRequestBytes = 64 * 1024
	ipcV1MaxDepth        = 8
	ipcV1MaxTokens       = 4096
	ipcV1MaxPolicyPrefix = 1024
)

//go:embed ipc-v1.schema.json testdata/ipc-v1/cases.json testdata/ipc-v1/valid/*.json testdata/ipc-v1/invalid/*.json
var ipcV1Files embed.FS

type ipcV1Cases struct {
	Valid   []string           `json:"valid"`
	Invalid []ipcV1InvalidCase `json:"invalid"`
}

type ipcV1InvalidCase struct {
	Path      string `json:"path"`
	Layer     string `json:"layer"`
	ErrorCode string `json:"error_code"`
}

func TestIPCV1Schema(t *testing.T) {
	schemaBytes := readIPCV1File(t, "ipc-v1.schema.json")
	if err := scanIPCV1JSON(schemaBytes); err != nil {
		t.Fatalf("scan schema: %v", err)
	}
	root := decodeIPCV1JSON(t, schemaBytes)

	if got := stringValue(root["$schema"]); got != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %q", got)
	}
	if got := stringValue(root["$id"]); got != "https://guard-wall.local/schema/ipc-v1.schema.json" {
		t.Fatalf("$id = %q", got)
	}
	assertJSONInteger(t, root, "x-guard-max-frame-bytes", 1<<20)
	assertJSONInteger(t, root, "x-guard-max-request-bytes", ipcV1MaxRequestBytes)
	assertJSONInteger(t, root, "x-guard-max-instance-depth", ipcV1MaxDepth)
	assertJSONInteger(t, root, "x-guard-max-json-tokens", ipcV1MaxTokens)

	branches, ok := root["oneOf"].([]any)
	if !ok || len(branches) != 4 {
		t.Fatalf("root oneOf has %d branches, want 4", len(branches))
	}

	forbidden := map[string]struct{}{
		"command": {}, "command_fragment": {}, "args": {}, "binary": {}, "binary_path": {},
		"env": {}, "cwd": {}, "table": {}, "chain": {}, "set": {}, "hook": {}, "jump": {},
		"object_name": {},
	}
	propertyNames := make(map[string]struct{})
	auditIPCV1SchemaNode(t, root, "#", propertyNames)
	for name := range forbidden {
		if _, found := propertyNames[name]; found {
			t.Fatalf("forbidden attacker-controlled property %q is representable", name)
		}
	}
}

func TestIPCV1GoldenVectors(t *testing.T) {
	root := decodeIPCV1JSON(t, readIPCV1File(t, "ipc-v1.schema.json"))
	var cases ipcV1Cases
	decodeIPCV1Into(t, readIPCV1File(t, "testdata/ipc-v1/cases.json"), &cases)
	assertIPCV1CaseCoverage(t, cases)

	for _, file := range cases.Valid {
		file := file
		t.Run("valid/"+strings.TrimSuffix(strings.TrimPrefix(file, "valid/"), ".json"), func(t *testing.T) {
			raw := readIPCV1File(t, "testdata/ipc-v1/"+file)
			value, code := validateIPCV1Request(raw, root)
			if code != "" {
				t.Fatalf("valid request rejected with %s", code)
			}
			if code := validateIPCV1Semantics(value); code != "" {
				t.Fatalf("valid request failed semantic validation with %s", code)
			}
		})
	}

	for _, test := range cases.Invalid {
		test := test
		t.Run("invalid/"+strings.TrimSuffix(strings.TrimPrefix(test.Path, "invalid/"), ".json"), func(t *testing.T) {
			raw := readIPCV1File(t, "testdata/ipc-v1/"+test.Path)
			value, code := validateIPCV1Request(raw, root)
			switch test.Layer {
			case "decoder":
				if code != test.ErrorCode {
					t.Fatalf("decoder code = %q, want %q", code, test.ErrorCode)
				}
			case "schema":
				if code != "schema_rejected" {
					t.Fatalf("schema code = %q, want schema_rejected", code)
				}
			case "semantic":
				if code != "" {
					t.Fatalf("semantic vector was rejected before semantic validation: %s", code)
				}
				if got := validateIPCV1Semantics(value); got != test.ErrorCode {
					t.Fatalf("semantic code = %q, want %q", got, test.ErrorCode)
				}
			default:
				t.Fatalf("unknown rejection layer %q", test.Layer)
			}
		})
	}
}

func TestIPCV1ResourceLimits(t *testing.T) {
	root := decodeIPCV1JSON(t, readIPCV1File(t, "ipc-v1.schema.json"))
	base := readIPCV1File(t, "testdata/ipc-v1/valid/probe-capabilities.json")
	exact := append(append([]byte(nil), base...), bytes.Repeat([]byte(" "), ipcV1MaxRequestBytes-len(base))...)
	if _, code := validateIPCV1Request(exact, root); code != "" {
		t.Fatalf("exact request limit rejected with %s", code)
	}
	over := append(append([]byte(nil), exact...), ' ')
	if _, code := validateIPCV1Request(over, root); code != "request_too_large" {
		t.Fatalf("one-over request code = %q, want request_too_large", code)
	}

	policy := decodeIPCV1JSON(t, readIPCV1File(t, "testdata/ipc-v1/valid/apply-policy.json"))
	operation := policy["payload"].(map[string]any)["operations"].([]any)[0].(map[string]any)
	operation["allowlist"] = generatedCanonicalPrefixes(ipcV1MaxPolicyPrefix - 2)
	if code := validateIPCV1Semantics(policy); code != "" {
		t.Fatalf("exact policy prefix limit rejected with %s", code)
	}
	operation["allowlist"] = generatedCanonicalPrefixes(ipcV1MaxPolicyPrefix - 1)
	if code := validateIPCV1Semantics(policy); code != "prefix_limit" {
		t.Fatalf("one-over policy prefix code = %q, want prefix_limit", code)
	}

	if err := scanIPCV1JSON(nestedIPCV1Arrays(ipcV1MaxDepth)); err != nil {
		t.Fatalf("exact depth rejected: %v", err)
	}
	if err := scanIPCV1JSON(nestedIPCV1Arrays(ipcV1MaxDepth + 1)); err == nil || !strings.Contains(err.Error(), "maximum depth") {
		t.Fatalf("one-over depth error = %v, want maximum depth", err)
	}
	if err := scanIPCV1JSON(ipcV1NullArray(ipcV1MaxTokens - 2)); err != nil {
		t.Fatalf("exact token limit rejected: %v", err)
	}
	if err := scanIPCV1JSON(ipcV1NullArray(ipcV1MaxTokens - 1)); err == nil || !strings.Contains(err.Error(), "token limit") {
		t.Fatalf("one-over token error = %v, want token limit", err)
	}

	target := decodeIPCV1JSON(t, readIPCV1File(t, "testdata/ipc-v1/valid/apply-target.json"))
	targetOperation := target["payload"].(map[string]any)["operations"].([]any)[0].(map[string]any)
	targetOperation["scopes"] = []any{"input", "forward"}
	if code := validateIPCV1Semantics(target); code != "" {
		t.Fatalf("canonical dual scope rejected with %s", code)
	}
	targetOperation["scopes"] = []any{"forward", "input"}
	if code := validateIPCV1Semantics(target); code != "invalid_scope" {
		t.Fatalf("reversed dual scope code = %q, want invalid_scope", code)
	}
}

func TestIPCV1IntegerSemantics(t *testing.T) {
	root := decodeIPCV1JSON(t, readIPCV1File(t, "ipc-v1.schema.json"))
	for _, raw := range [][]byte{
		[]byte(`{"version":1.0,"operation":"ProbeCapabilities","payload":{}}`),
		[]byte(`{"version":1e0,"operation":"ProbeCapabilities","payload":{}}`),
	} {
		if _, code := validateIPCV1Request(raw, root); code != "" {
			t.Fatalf("mathematical integer rejected with %s", code)
		}
	}
	for _, raw := range [][]byte{
		[]byte(`{"version":1.5,"operation":"ProbeCapabilities","payload":{}}`),
		[]byte(`{"version":9223372036854775808,"operation":"ProbeCapabilities","payload":{}}`),
	} {
		if _, code := validateIPCV1Request(raw, root); code != "schema_rejected" {
			t.Fatalf("non-int64 request code = %q, want schema_rejected", code)
		}
	}
}

func FuzzIPCV1GoldenContract(f *testing.F) {
	rootBytes := readIPCV1File(f, "ipc-v1.schema.json")
	root := decodeIPCV1JSON(f, rootBytes)
	for _, name := range []string{
		"testdata/ipc-v1/valid/probe-capabilities.json",
		"testdata/ipc-v1/valid/apply-target.json",
		"testdata/ipc-v1/invalid/command-field.json",
		"testdata/ipc-v1/invalid/duplicate-key.json",
	} {
		f.Add(readIPCV1File(f, name))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, code := validateIPCV1Request(raw, root)
		if code != "" {
			return
		}
		if code := validateIPCV1Semantics(value); code != "" {
			return
		}
		operation, ok := value["operation"].(string)
		if !ok {
			t.Fatal("accepted request without an operation")
		}
		switch operation {
		case "ProbeCapabilities", "SnapshotManaged", "ApplyManagedPlan", "RemoveManagedInfrastructure":
		default:
			t.Fatalf("accepted operation %q outside the closed allowlist", operation)
		}
	})
}

func validateIPCV1Request(raw []byte, root map[string]any) (map[string]any, string) {
	if len(raw) > ipcV1MaxRequestBytes {
		return nil, "request_too_large"
	}
	if !utf8.Valid(raw) {
		return nil, "invalid_utf8"
	}
	if err := scanIPCV1JSON(raw); err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, "duplicate_key"
		}
		if strings.Contains(err.Error(), "maximum depth") {
			return nil, "max_depth_exceeded"
		}
		if strings.Contains(err.Error(), "token limit") {
			return nil, "token_limit_exceeded"
		}
		return nil, "invalid_json"
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

func validateIPCV1Semantics(request map[string]any) string {
	if stringValue(request["operation"]) != "ApplyManagedPlan" {
		return ""
	}
	payload := request["payload"].(map[string]any)
	operations := payload["operations"].([]any)
	operation := operations[0].(map[string]any)
	switch stringValue(payload["domain"]) {
	case "infrastructure":
		return ""
	case "policy":
		allowlist := stringSlice(operation["allowlist"].([]any))
		protected := stringSlice(operation["protected_targets"].([]any))
		if len(allowlist)+len(protected) > ipcV1MaxPolicyPrefix {
			return "prefix_limit"
		}
		if code := validateCanonicalPrefixList(allowlist); code != "" {
			return code
		}
		if code := validateCanonicalPrefixList(protected); code != "" {
			return code
		}
		mandatory := map[string]bool{"127.0.0.0/8": false, "::1/128": false}
		for _, prefix := range protected {
			if _, required := mandatory[prefix]; required {
				mandatory[prefix] = true
			}
		}
		for _, present := range mandatory {
			if !present {
				return "protected_policy_missing"
			}
		}
		return ""
	case "target":
		target, err := netip.ParsePrefix(stringValue(operation["target"]))
		if err != nil || target != target.Masked() || target.String() != stringValue(operation["target"]) {
			return "noncanonical_prefix"
		}
		for _, protected := range []netip.Prefix{
			netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
		} {
			if target.Overlaps(protected) {
				return "protected_target"
			}
		}
		scopes := stringSlice(operation["scopes"].([]any))
		if len(scopes) == 2 && (scopes[0] != "input" || scopes[1] != "forward") {
			return "invalid_scope"
		}
		membership := stringValue(operation["membership"])
		timeoutMode := stringValue(operation["timeout_mode"])
		expiry := operation["effective_until_unix_us"]
		if membership == "absent" && (timeoutMode != "none" || expiry != nil) {
			return "invalid_timeout"
		}
		if timeoutMode == "native" && expiry == nil {
			return "invalid_timeout"
		}
		if timeoutMode == "none" && expiry != nil {
			return "invalid_timeout"
		}
		return ""
	default:
		panic("schema accepted an unknown domain")
	}
}

func validateCanonicalPrefixList(prefixes []string) string {
	if !sort.StringsAreSorted(prefixes) {
		return "noncanonical_order"
	}
	for _, value := range prefixes {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix != prefix.Masked() || prefix.String() != value {
			return "noncanonical_prefix"
		}
	}
	return ""
}

func validateIPCV1SchemaValue(root, schema map[string]any, value any, path string) error {
	if reference, ok := schema["$ref"].(string); ok {
		resolved, err := resolveIPCV1Reference(root, reference)
		if err != nil {
			return err
		}
		return validateIPCV1SchemaValue(root, resolved, value, path)
	}
	if branches, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, rawBranch := range branches {
			branch := rawBranch.(map[string]any)
			if validateIPCV1SchemaValue(root, branch, value, path) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s matches %d oneOf branches", path, matches)
		}
	}

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

	typeName, _ := schema["type"].(string)
	switch typeName {
	case "":
		return nil
	case "null":
		if value != nil {
			return fmt.Errorf("%s is not null", path)
		}
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not an object", path)
		}
		if maximum, ok := jsonInt64(schema["maxProperties"]); ok && int64(len(object)) > maximum {
			return fmt.Errorf("%s has too many properties", path)
		}
		if requiredProperties, ok := schema["required"].([]any); ok {
			for _, required := range stringSlice(requiredProperties) {
				if _, found := object[required]; !found {
					return fmt.Errorf("%s is missing %s", path, required)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, child := range object {
			rawChildSchema, found := properties[name]
			if !found {
				if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
					return fmt.Errorf("%s contains unknown property %s", path, name)
				}
				continue
			}
			if err := validateIPCV1SchemaValue(root, rawChildSchema.(map[string]any), child, path+"."+name); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s is not an array", path)
		}
		if minimum, ok := jsonInt64(schema["minItems"]); ok && int64(len(array)) < minimum {
			return fmt.Errorf("%s has too few items", path)
		}
		if maximum, ok := jsonInt64(schema["maxItems"]); ok && int64(len(array)) > maximum {
			return fmt.Errorf("%s has too many items", path)
		}
		if unique, _ := schema["uniqueItems"].(bool); unique {
			seen := make(map[string]struct{}, len(array))
			for _, item := range array {
				encoded, _ := json.Marshal(item)
				key := string(encoded)
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("%s contains duplicate items", path)
				}
				seen[key] = struct{}{}
			}
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, item := range array {
				if err := validateIPCV1SchemaValue(root, itemSchema, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s is not a string", path)
		}
		if minimum, ok := jsonInt64(schema["minLength"]); ok && int64(len(text)) < minimum {
			return fmt.Errorf("%s is too short", path)
		}
		if maximum, ok := jsonInt64(schema["maxLength"]); ok && int64(len(text)) > maximum {
			return fmt.Errorf("%s is too long", path)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("compile schema pattern: %w", err)
			}
			if !compiled.MatchString(text) {
				return fmt.Errorf("%s does not match pattern", path)
			}
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
		return fmt.Errorf("unsupported schema type %q at %s", typeName, path)
	}
	return nil
}

func auditIPCV1SchemaNode(t *testing.T, value any, path string, propertyNames map[string]struct{}) {
	t.Helper()
	switch node := value.(type) {
	case map[string]any:
		if typeName, _ := node["type"].(string); typeName != "" {
			switch typeName {
			case "object":
				if additional, ok := node["additionalProperties"].(bool); !ok || additional {
					t.Fatalf("%s object is not fail-closed", path)
				}
				if _, bounded := jsonInt64(node["maxProperties"]); !bounded {
					t.Fatalf("%s object has no maxProperties", path)
				}
			case "array":
				if _, bounded := jsonInt64(node["maxItems"]); !bounded {
					t.Fatalf("%s array has no maxItems", path)
				}
			case "string":
				if _, bounded := jsonInt64(node["maxLength"]); !bounded {
					t.Fatalf("%s string has no maxLength", path)
				}
			case "integer":
				if _, bounded := jsonInt64(node["minimum"]); !bounded {
					t.Fatalf("%s integer has no minimum", path)
				}
				if _, bounded := jsonInt64(node["maximum"]); !bounded {
					t.Fatalf("%s integer has no maximum", path)
				}
			case "null":
			default:
				t.Fatalf("%s uses unsupported type %q", path, typeName)
			}
		}
		if properties, ok := node["properties"].(map[string]any); ok {
			for name := range properties {
				propertyNames[name] = struct{}{}
			}
		}
		for name, child := range node {
			auditIPCV1SchemaNode(t, child, path+"/"+name, propertyNames)
		}
	case []any:
		for index, child := range node {
			auditIPCV1SchemaNode(t, child, fmt.Sprintf("%s/%d", path, index), propertyNames)
		}
	}
}

type ipcV1Scan struct {
	depth  int
	tokens int
}

func scanIPCV1JSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &ipcV1Scan{}
	if err := scanIPCV1Value(decoder, state, 0); err != nil {
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

func scanIPCV1Value(decoder *json.Decoder, state *ipcV1Scan, parentDepth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := countIPCV1Token(state); err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	depth := parentDepth + 1
	if depth > ipcV1MaxDepth {
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
			if err := countIPCV1Token(state); err != nil {
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
			if err := scanIPCV1Value(decoder, state, depth); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanIPCV1Value(decoder, state, depth); err != nil {
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
	if err := countIPCV1Token(state); err != nil {
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

func countIPCV1Token(state *ipcV1Scan) error {
	state.tokens++
	if state.tokens > ipcV1MaxTokens {
		return fmt.Errorf("token limit exceeded")
	}
	return nil
}

func resolveIPCV1Reference(root map[string]any, reference string) (map[string]any, error) {
	if !strings.HasPrefix(reference, "#/") {
		return nil, fmt.Errorf("unsupported external reference %q", reference)
	}
	var current any = root
	for _, component := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("reference %q traverses a non-object", reference)
		}
		current, ok = object[component]
		if !ok {
			return nil, fmt.Errorf("reference %q does not resolve", reference)
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reference %q does not resolve to a schema", reference)
	}
	return resolved, nil
}

func assertIPCV1CaseCoverage(t *testing.T, cases ipcV1Cases) {
	t.Helper()
	listed := make(map[string]struct{}, len(cases.Valid)+len(cases.Invalid))
	for _, name := range cases.Valid {
		listed[name] = struct{}{}
	}
	for _, test := range cases.Invalid {
		if test.Layer != "schema" && test.ErrorCode == "" {
			t.Fatalf("invalid case %s has no error code", test.Path)
		}
		if test.Layer == "schema" && test.ErrorCode != "" {
			t.Fatalf("schema-only case %s must not freeze a response error code", test.Path)
		}
		if _, duplicate := listed[test.Path]; duplicate {
			t.Fatalf("duplicate case path %s", test.Path)
		}
		listed[test.Path] = struct{}{}
	}
	for _, pattern := range []string{"testdata/ipc-v1/valid/*.json", "testdata/ipc-v1/invalid/*.json"} {
		matches, err := fs.Glob(ipcV1Files, pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			relative := strings.TrimPrefix(match, "testdata/ipc-v1/")
			if _, found := listed[relative]; !found {
				t.Errorf("golden vector %s is not listed in cases.json", relative)
			}
		}
	}
}

func generatedCanonicalPrefixes(count int) []any {
	values := make([]string, 0, count)
	for index := 0; index < count; index++ {
		address := netip.AddrFrom4([4]byte{10, byte(index >> 16), byte(index >> 8), byte(index)})
		values = append(values, address.String()+"/32")
	}
	sort.Strings(values)
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func nestedIPCV1Arrays(depth int) []byte {
	return []byte(strings.Repeat("[", depth) + "null" + strings.Repeat("]", depth))
}

func ipcV1NullArray(items int) []byte {
	if items == 0 {
		return []byte("[]")
	}
	return []byte("[" + strings.Repeat("null,", items-1) + "null]")
}

func decodeIPCV1Object(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("request is not an object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("multiple JSON values")
	}
	return value, nil
}

func decodeIPCV1JSON(tb testing.TB, raw []byte) map[string]any {
	tb.Helper()
	value, err := decodeIPCV1Object(raw)
	if err != nil {
		tb.Fatalf("decode JSON: %v", err)
	}
	return value
}

func decodeIPCV1Into(tb testing.TB, raw []byte, destination any) {
	tb.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		tb.Fatalf("decode fixture: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		tb.Fatalf("fixture contains multiple JSON values")
	}
}

func readIPCV1File(tb testing.TB, name string) []byte {
	tb.Helper()
	contents, err := ipcV1Files.ReadFile(name)
	if err != nil {
		tb.Fatalf("read %s: %v", name, err)
	}
	return contents
}

func assertJSONInteger(t *testing.T, object map[string]any, name string, want int64) {
	t.Helper()
	got, ok := jsonInt64(object[name])
	if !ok || got != want {
		t.Fatalf("%s = %v, want %d", name, object[name], want)
	}
}

func jsonInt64(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	rational, ok := new(big.Rat).SetString(string(number))
	if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
		return 0, false
	}
	return rational.Num().Int64(), true
}

func equalJSONValue(left, right any) bool {
	leftNumber, leftIsNumber := left.(json.Number)
	rightNumber, rightIsNumber := right.(json.Number)
	if leftIsNumber && rightIsNumber {
		leftRational, leftOK := new(big.Rat).SetString(string(leftNumber))
		rightRational, rightOK := new(big.Rat).SetString(string(rightNumber))
		return leftOK && rightOK && leftRational.Cmp(rightRational) == 0
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func stringSlice(values []any) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index], _ = value.(string)
	}
	return result
}
