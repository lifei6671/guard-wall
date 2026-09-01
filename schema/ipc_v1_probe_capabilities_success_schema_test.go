package schema

import (
	"bytes"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

const (
	probeCapabilitiesMaxBytes  = 4 * 1024
	probeCapabilitiesMaxDepth  = 2
	probeCapabilitiesMaxTokens = 64
)

var probeCapabilityPayloadKeys = []string{
	"backend",
	"tool_version",
	"ipv4",
	"ipv6",
	"cidr",
	"native_set",
	"native_timeout",
	"crash_safe_expiry",
	"atomic_batch",
	"host_input",
	"forward",
	"ufw_integration_proven",
	"docker_integration_proven",
	"ownership_proven",
	"mutation_ready",
}

//go:embed ipc-v1-probe-capabilities-success.schema.json testdata/ipc-v1-probe-capabilities-success/cases.json testdata/ipc-v1-probe-capabilities-success/valid/*.json testdata/ipc-v1-probe-capabilities-success/invalid/*.json
var probeCapabilitiesFiles embed.FS

type probeCapabilitiesCases struct {
	Valid   []probeCapabilitiesCase `json:"valid"`
	Invalid []probeCapabilitiesCase `json:"invalid"`
}

type probeCapabilitiesCase struct {
	Path            string `json:"path"`
	Layer           string `json:"layer"`
	Classification  string `json:"classification"`
	FixtureEncoding string `json:"fixture_encoding"`
}

func TestProbeCapabilitiesSuccessSchema(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "ipc-v1-probe-capabilities-success.schema.json"))

	if got := stringValue(root["$schema"]); got != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %q", got)
	}
	if got := stringValue(root["$id"]); got != "https://guard-wall.local/schema/ipc-v1-probe-capabilities-success.schema.json" {
		t.Fatalf("$id = %q", got)
	}
	assertJSONInteger(t, root, "x-guard-max-frame-bytes", 1<<20)
	assertJSONInteger(t, root, "x-guard-max-response-bytes", probeCapabilitiesMaxBytes)
	assertJSONInteger(t, root, "x-guard-max-instance-depth", probeCapabilitiesMaxDepth)
	assertJSONInteger(t, root, "x-guard-max-json-tokens", probeCapabilitiesMaxTokens)

	assertProbeCapabilitiesObject(t, root, []string{"version", "operation", "payload"}, 3)
	properties := probeCapabilitiesProperties(t, root)
	assertProbeCapabilitiesIntegerConst(t, properties, "version", 1)
	assertProbeCapabilitiesStringConst(t, properties, "operation", "ProbeCapabilities", 32)

	payload, ok := properties["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload schema = %#v, want object", properties["payload"])
	}
	assertProbeCapabilitiesObject(t, payload, probeCapabilityPayloadKeys, 15)
	payloadProperties := probeCapabilitiesProperties(t, payload)
	backend := probeCapabilitiesProperty(t, payloadProperties, "backend")
	if got := stringValue(backend["type"]); got != "string" {
		t.Fatalf("backend type = %q, want string", got)
	}
	if got := stringSlice(backend["enum"].([]any)); !sameProbeCapabilitiesStrings(got, []string{"nftables-native", "iptables-nft", "iptables-legacy"}) {
		t.Fatalf("backend enum = %v", got)
	}
	assertJSONInteger(t, backend, "maxLength", 16)

	toolVersion := probeCapabilitiesProperty(t, payloadProperties, "tool_version")
	if got := stringValue(toolVersion["type"]); got != "string" {
		t.Fatalf("tool_version type = %q, want string", got)
	}
	assertJSONInteger(t, toolVersion, "minLength", 1)
	assertJSONInteger(t, toolVersion, "maxLength", 128)
	if got := stringValue(toolVersion["pattern"]); got != `^[!-~](?:[ -~]{0,126}[!-~])?$` {
		t.Fatalf("tool_version pattern = %q", got)
	}
	for _, key := range probeCapabilityPayloadKeys[2:] {
		property := probeCapabilitiesProperty(t, payloadProperties, key)
		if got := stringValue(property["type"]); got != "boolean" || len(property) != 1 {
			t.Fatalf("%s schema = %#v, want closed boolean", key, property)
		}
	}

	var names []string
	auditProbeCapabilitiesSchema(t, root, "#", &names)
	if !sameProbeCapabilitiesStrings(names, append([]string{"version", "operation", "payload"}, probeCapabilityPayloadKeys...)) {
		t.Fatalf("wire properties = %v", names)
	}
}

func TestProbeCapabilitiesSuccessGoldenVectors(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "ipc-v1-probe-capabilities-success.schema.json"))
	cases := readProbeCapabilitiesCases(t)
	assertProbeCapabilitiesCaseCoverage(t, cases)

	for _, test := range cases.Valid {
		test := test
		t.Run("valid/"+strings.TrimSuffix(strings.TrimPrefix(test.Path, "valid/"), ".json"), func(t *testing.T) {
			raw := readProbeCapabilitiesFile(t, "testdata/ipc-v1-probe-capabilities-success/"+test.Path)
			value, classification := validateProbeCapabilitiesSuccess(raw, root)
			if classification != "" {
				t.Fatalf("valid response rejected with %s", classification)
			}
			assertProbeCapabilitiesCanonicalOrder(t, raw)
			if stringValue(value["operation"]) != "ProbeCapabilities" {
				t.Fatalf("operation = %q", value["operation"])
			}
			assertProbeCapabilitiesDomainMapping(t, value)
		})
	}

	for _, test := range cases.Invalid {
		test := test
		t.Run("invalid/"+strings.TrimSuffix(strings.TrimPrefix(test.Path, "invalid/"), ".json"), func(t *testing.T) {
			raw := readProbeCapabilitiesCase(t, test)
			_, classification := validateProbeCapabilitiesSuccess(raw, root)
			if classification != test.Classification {
				t.Fatalf("classification = %q, want %q", classification, test.Classification)
			}
		})
	}
}

func TestProbeCapabilitiesSuccessToolVersionBoundaries(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "ipc-v1-probe-capabilities-success.schema.json"))
	base := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "testdata/ipc-v1-probe-capabilities-success/valid/nftables-native-ready.json"))
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "one byte", version: "v"},
		{name: "128 bytes", version: strings.Repeat("v", 128)},
		{name: "empty", version: "", want: "schema_rejected"},
		{name: "129 bytes", version: strings.Repeat("v", 129), want: "schema_rejected"},
		{name: "leading space", version: " v1", want: "schema_rejected"},
		{name: "trailing space", version: "v1 ", want: "schema_rejected"},
		{name: "control", version: "v1\n", want: "schema_rejected"},
		{name: "non ASCII", version: "版本1", want: "schema_rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneProbeCapabilitiesJSON(t, base)
			candidate["payload"].(map[string]any)["tool_version"] = test.version
			assertProbeCapabilitiesClassification(t, root, candidate, test.want)
		})
	}
}

func TestProbeCapabilitiesSuccessRequiredFieldsAndTypes(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "ipc-v1-probe-capabilities-success.schema.json"))
	valid := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "testdata/ipc-v1-probe-capabilities-success/valid/nftables-native-ready.json"))

	for _, key := range []string{"version", "operation", "payload"} {
		t.Run("missing/root/"+key, func(t *testing.T) {
			candidate := cloneProbeCapabilitiesJSON(t, valid)
			delete(candidate, key)
			assertProbeCapabilitiesClassification(t, root, candidate, "schema_rejected")
		})
	}
	for _, key := range probeCapabilityPayloadKeys {
		t.Run("missing/payload/"+key, func(t *testing.T) {
			candidate := cloneProbeCapabilitiesJSON(t, valid)
			delete(candidate["payload"].(map[string]any), key)
			assertProbeCapabilitiesClassification(t, root, candidate, "schema_rejected")
		})
	}
	for _, key := range probeCapabilityPayloadKeys[2:] {
		t.Run("type-confusion/"+key, func(t *testing.T) {
			candidate := cloneProbeCapabilitiesJSON(t, valid)
			candidate["payload"].(map[string]any)[key] = "true"
			assertProbeCapabilitiesClassification(t, root, candidate, "schema_rejected")
		})
	}
	for _, key := range []string{"backend", "tool_version"} {
		t.Run("type-confusion/"+key, func(t *testing.T) {
			candidate := cloneProbeCapabilitiesJSON(t, valid)
			candidate["payload"].(map[string]any)[key] = true
			assertProbeCapabilitiesClassification(t, root, candidate, "schema_rejected")
		})
	}
	for _, test := range []struct {
		name  string
		key   string
		value any
	}{
		{name: "version string", key: "version", value: "1"},
		{name: "unknown operation", key: "operation", value: "probe"},
		{name: "payload array", key: "payload", value: []any{}},
	} {
		t.Run("root-type/"+test.name, func(t *testing.T) {
			candidate := cloneProbeCapabilitiesJSON(t, valid)
			candidate[test.key] = test.value
			assertProbeCapabilitiesClassification(t, root, candidate, "schema_rejected")
		})
	}
}

func TestProbeCapabilitiesSuccessDomainContradictions(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "ipc-v1-probe-capabilities-success.schema.json"))
	base := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "testdata/ipc-v1-probe-capabilities-success/valid/nftables-native-ready.json"))

	tests := []struct {
		name   string
		change map[string]any
	}{
		{name: "no IP family", change: map[string]any{"ipv4": false, "ipv6": false}},
		{name: "no policy scope", change: map[string]any{"host_input": false, "forward": false}},
		{name: "native timeout without set", change: map[string]any{"native_set": false}},
		{name: "crash safe expiry without timeout", change: map[string]any{"native_timeout": false}},
		{name: "Docker without FORWARD", change: map[string]any{"forward": false}},
		{name: "mutation without ownership", change: map[string]any{"ownership_proven": false}},
		{name: "mutation without CIDR", change: map[string]any{"cidr": false}},
		{name: "ready nftables without native set", change: map[string]any{"native_set": false, "native_timeout": false, "crash_safe_expiry": false}},
		{name: "ready nftables without atomic batch", change: map[string]any{"atomic_batch": false}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneProbeCapabilitiesJSON(t, base)
			payload := candidate["payload"].(map[string]any)
			for key, value := range test.change {
				payload[key] = value
			}
			assertProbeCapabilitiesClassification(t, root, candidate, "semantic_rejected")
		})
	}
}

func TestProbeCapabilitiesSuccessIndependentCapabilities(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "ipc-v1-probe-capabilities-success.schema.json"))
	for _, path := range []string{
		"valid/iptables-nft-atomic-without-set.json",
		"valid/iptables-legacy-set-without-atomic.json",
		"valid/ufw-forward-without-input.json",
	} {
		raw := readProbeCapabilitiesFile(t, "testdata/ipc-v1-probe-capabilities-success/"+path)
		if _, classification := validateProbeCapabilitiesSuccess(raw, root); classification != "" {
			t.Errorf("%s rejected with %s", path, classification)
		}
	}
}

func TestProbeCapabilitiesSuccessResourceLimits(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "ipc-v1-probe-capabilities-success.schema.json"))
	base := readProbeCapabilitiesFile(t, "testdata/ipc-v1-probe-capabilities-success/valid/nftables-native-ready.json")
	exactBytes := append(append([]byte(nil), base...), bytes.Repeat([]byte(" "), probeCapabilitiesMaxBytes-len(base))...)
	if _, classification := validateProbeCapabilitiesSuccess(exactBytes, root); classification != "" {
		t.Fatalf("exact byte limit rejected with %s", classification)
	}
	if _, classification := validateProbeCapabilitiesSuccess(append(exactBytes, ' '), root); classification != "response_too_large" {
		t.Fatalf("one-over byte limit = %q, want response_too_large", classification)
	}

	exactDepth := []byte(`{"x":[]}`)
	if err := scanProbeCapabilitiesJSON(exactDepth); err != nil {
		t.Fatalf("exact depth rejected: %v", err)
	}
	if _, classification := validateProbeCapabilitiesSuccess([]byte(`{"x":[[]]}`), root); classification != "max_depth_exceeded" {
		t.Fatalf("one-over depth = %q, want max_depth_exceeded", classification)
	}

	exactTokens := probeCapabilitiesNullArray(59)
	if err := scanProbeCapabilitiesJSON(exactTokens); err != nil {
		t.Fatalf("exact token limit rejected: %v", err)
	}
	if _, classification := validateProbeCapabilitiesSuccess(probeCapabilitiesNullArray(60), root); classification != "token_limit_exceeded" {
		t.Fatalf("one-over token limit = %q, want token_limit_exceeded", classification)
	}
}

func TestProbeCapabilitiesSuccessClassificationIsSanitized(t *testing.T) {
	root := decodeIPCV1JSON(t, readProbeCapabilitiesFile(t, "ipc-v1-probe-capabilities-success.schema.json"))
	secret := "do-not-echo-secret"
	raw := []byte(`{"version":1,"operation":"ProbeCapabilities","payload":{"command":"` + secret + `"}}`)
	_, classification := validateProbeCapabilitiesSuccess(raw, root)
	if classification != "schema_rejected" {
		t.Fatalf("classification = %q", classification)
	}
	if strings.Contains(classification, secret) || strings.Contains(classification, "command") {
		t.Fatalf("classification leaks input detail: %q", classification)
	}
}

func FuzzProbeCapabilitiesSuccessClosedContract(f *testing.F) {
	root := decodeIPCV1JSON(f, readProbeCapabilitiesFile(f, "ipc-v1-probe-capabilities-success.schema.json"))
	cases := readProbeCapabilitiesCases(f)
	for _, test := range cases.Valid {
		f.Add(readProbeCapabilitiesFile(f, "testdata/ipc-v1-probe-capabilities-success/"+test.Path))
	}
	for _, test := range cases.Invalid {
		switch test.Classification {
		case "duplicate_key", "invalid_utf8", "semantic_rejected":
			f.Add(readProbeCapabilitiesCase(f, test))
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, classification := validateProbeCapabilitiesSuccess(raw, root)
		if classification != "" {
			return
		}
		if jsonInt, ok := jsonInt64(value["version"]); !ok || jsonInt != 1 {
			t.Fatalf("accepted version %#v", value["version"])
		}
		if stringValue(value["operation"]) != "ProbeCapabilities" {
			t.Fatalf("accepted operation %#v", value["operation"])
		}
		if _, err := probeCapabilitiesDomain(value); err != nil {
			t.Fatalf("accepted invalid domain: %v", err)
		}
	})
}

func validateProbeCapabilitiesSuccess(raw []byte, root map[string]any) (map[string]any, string) {
	if len(raw) > probeCapabilitiesMaxBytes {
		return nil, "response_too_large"
	}
	if !utf8.Valid(raw) {
		return nil, "invalid_utf8"
	}
	if err := scanProbeCapabilitiesJSON(raw); err != nil {
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
	if err := validateProbeCapabilitiesSchemaValue(root, value, "$"); err != nil {
		return value, "schema_rejected"
	}
	if _, err := probeCapabilitiesDomain(value); err != nil {
		return value, "semantic_rejected"
	}
	return value, ""
}

func validateProbeCapabilitiesSchemaValue(schema map[string]any, value any, path string) error {
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

	switch typeName := stringValue(schema["type"]); typeName {
	case "":
		return nil
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
			if err := validateProbeCapabilitiesSchemaValue(childSchema, child, path+"."+key); err != nil {
				return err
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
		if pattern := stringValue(schema["pattern"]); pattern != "" {
			// JSON Schema uses the ECMAScript non-capturing-group spelling,
			// while Go's regexp parser intentionally omits it. Capturing does
			// not change acceptance for this anchored validation pattern.
			compiled, err := regexp.Compile(strings.ReplaceAll(pattern, "(?:", "("))
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
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s is not a boolean", path)
		}
	default:
		return fmt.Errorf("unsupported schema type %q at %s", typeName, path)
	}
	return nil
}

func probeCapabilitiesDomain(value map[string]any) (firewall.FirewallCapabilities, error) {
	payload, ok := value["payload"].(map[string]any)
	if !ok {
		return firewall.FirewallCapabilities{}, fmt.Errorf("payload is not an object")
	}
	boolean := func(key string) bool {
		result, _ := payload[key].(bool)
		return result
	}
	return firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend:                 firewall.BackendKind(stringValue(payload["backend"])),
		ToolVersion:             stringValue(payload["tool_version"]),
		IPv4:                    boolean("ipv4"),
		IPv6:                    boolean("ipv6"),
		CIDR:                    boolean("cidr"),
		NativeSet:               boolean("native_set"),
		NativeTimeout:           boolean("native_timeout"),
		CrashSafeExpiry:         boolean("crash_safe_expiry"),
		AtomicBatch:             boolean("atomic_batch"),
		HostInput:               boolean("host_input"),
		Forward:                 boolean("forward"),
		UFWIntegrationProven:    boolean("ufw_integration_proven"),
		DockerIntegrationProven: boolean("docker_integration_proven"),
		OwnershipProven:         boolean("ownership_proven"),
		MutationReady:           boolean("mutation_ready"),
	})
}

func assertProbeCapabilitiesDomainMapping(t *testing.T, value map[string]any) {
	t.Helper()
	payload := value["payload"].(map[string]any)
	capabilities, err := probeCapabilitiesDomain(value)
	if err != nil {
		t.Fatalf("rebuild FirewallCapabilities: %v", err)
	}
	if err := capabilities.Validate(); err != nil {
		t.Fatalf("validate rebuilt FirewallCapabilities: %v", err)
	}
	if string(capabilities.Backend()) != stringValue(payload["backend"]) || capabilities.ToolVersion() != stringValue(payload["tool_version"]) {
		t.Fatal("rebuilt backend or tool_version does not match payload")
	}
	got := []bool{
		capabilities.SupportsIPv4(),
		capabilities.SupportsIPv6(),
		capabilities.SupportsCIDR(),
		capabilities.SupportsNativeSet(),
		capabilities.SupportsNativeTimeout(),
		capabilities.SupportsCrashSafeExpiry(),
		capabilities.SupportsAtomicBatch(),
		capabilities.SupportsHostInput(),
		capabilities.SupportsForward(),
		capabilities.UFWIntegrationProven(),
		capabilities.DockerIntegrationProven(),
		capabilities.OwnershipProven(),
		capabilities.MutationReady(),
	}
	for index, key := range probeCapabilityPayloadKeys[2:] {
		if got[index] != payload[key].(bool) {
			t.Fatalf("rebuilt %s = %t, want %t", key, got[index], payload[key])
		}
	}
}

func auditProbeCapabilitiesSchema(t *testing.T, value any, path string, names *[]string) {
	t.Helper()
	switch node := value.(type) {
	case map[string]any:
		if typeName := stringValue(node["type"]); typeName != "" {
			switch typeName {
			case "object":
				if additional, ok := node["additionalProperties"].(bool); !ok || additional {
					t.Fatalf("%s is not a closed object", path)
				}
				if _, ok := jsonInt64(node["maxProperties"]); !ok {
					t.Fatalf("%s has no maxProperties", path)
				}
			case "string":
				if _, ok := jsonInt64(node["maxLength"]); !ok {
					t.Fatalf("%s has no maxLength", path)
				}
			case "integer":
				if _, ok := jsonInt64(node["minimum"]); !ok {
					t.Fatalf("%s has no minimum", path)
				}
				if _, ok := jsonInt64(node["maximum"]); !ok {
					t.Fatalf("%s has no maximum", path)
				}
			case "boolean":
			default:
				t.Fatalf("%s uses unsupported type %q", path, typeName)
			}
		}
		if properties, ok := node["properties"].(map[string]any); ok {
			for name := range properties {
				*names = append(*names, name)
			}
		}
		for key, child := range node {
			auditProbeCapabilitiesSchema(t, child, path+"/"+key, names)
		}
	case []any:
		for index, child := range node {
			auditProbeCapabilitiesSchema(t, child, fmt.Sprintf("%s/%d", path, index), names)
		}
	}
}

func assertProbeCapabilitiesObject(t *testing.T, object map[string]any, required []string, maxProperties int64) {
	t.Helper()
	if stringValue(object["type"]) != "object" || object["additionalProperties"] != false {
		t.Fatalf("object contract = %#v", object)
	}
	assertJSONInteger(t, object, "maxProperties", maxProperties)
	rawRequired, ok := object["required"].([]any)
	if !ok || !sameProbeCapabilitiesStrings(stringSlice(rawRequired), required) {
		t.Fatalf("required = %#v, want %v", object["required"], required)
	}
	properties := probeCapabilitiesProperties(t, object)
	got := make([]string, 0, len(properties))
	for key := range properties {
		got = append(got, key)
	}
	if !sameProbeCapabilitiesStrings(got, required) {
		t.Fatalf("properties = %v, want %v", got, required)
	}
}

func probeCapabilitiesProperties(t *testing.T, object map[string]any) map[string]any {
	t.Helper()
	properties, ok := object["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", object["properties"])
	}
	return properties
}

func probeCapabilitiesProperty(t *testing.T, properties map[string]any, key string) map[string]any {
	t.Helper()
	property, ok := properties[key].(map[string]any)
	if !ok {
		t.Fatalf("property %s = %#v", key, properties[key])
	}
	return property
}

func assertProbeCapabilitiesIntegerConst(t *testing.T, properties map[string]any, key string, value int64) {
	t.Helper()
	property := probeCapabilitiesProperty(t, properties, key)
	if stringValue(property["type"]) != "integer" {
		t.Fatalf("%s type = %#v", key, property["type"])
	}
	assertJSONInteger(t, property, "const", value)
	assertJSONInteger(t, property, "minimum", value)
	assertJSONInteger(t, property, "maximum", value)
}

func assertProbeCapabilitiesStringConst(t *testing.T, properties map[string]any, key, value string, maxLength int64) {
	t.Helper()
	property := probeCapabilitiesProperty(t, properties, key)
	if stringValue(property["type"]) != "string" || stringValue(property["const"]) != value {
		t.Fatalf("%s = %#v", key, property)
	}
	assertJSONInteger(t, property, "maxLength", maxLength)
}

func sameProbeCapabilitiesStrings(got, want []string) bool {
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

func assertProbeCapabilitiesCanonicalOrder(t *testing.T, raw []byte) {
	t.Helper()
	assertProbeCapabilitiesKeyOrder(t, raw, []string{"version", "operation", "payload"})
	payloadIndex := bytes.Index(raw, []byte(`"payload"`))
	if payloadIndex < 0 {
		t.Fatal("fixture has no payload key")
	}
	assertProbeCapabilitiesKeyOrder(t, raw[payloadIndex:], probeCapabilityPayloadKeys)
}

func assertProbeCapabilitiesKeyOrder(t *testing.T, raw []byte, keys []string) {
	t.Helper()
	last := -1
	for _, key := range keys {
		index := bytes.Index(raw, []byte(`"`+key+`"`))
		if index <= last {
			t.Fatalf("key %q is not in canonical order", key)
		}
		last = index
	}
}

func assertProbeCapabilitiesClassification(t *testing.T, root map[string]any, value map[string]any, want string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, got := validateProbeCapabilitiesSuccess(raw, root)
	if got != want {
		t.Fatalf("classification = %q, want %q", got, want)
	}
}

func cloneProbeCapabilitiesJSON(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return decodeIPCV1JSON(t, raw)
}

func readProbeCapabilitiesCases(tb testing.TB) probeCapabilitiesCases {
	tb.Helper()
	var cases probeCapabilitiesCases
	decodeIPCV1Into(tb, readProbeCapabilitiesFile(tb, "testdata/ipc-v1-probe-capabilities-success/cases.json"), &cases)
	return cases
}

func assertProbeCapabilitiesCaseCoverage(t *testing.T, cases probeCapabilitiesCases) {
	t.Helper()
	listed := make(map[string]struct{}, len(cases.Valid)+len(cases.Invalid))
	for _, test := range cases.Valid {
		if test.Path == "" || test.Layer != "semantic" || test.Classification != "" || test.FixtureEncoding != "" {
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
		case "semantic":
			if test.Classification != "semantic_rejected" || test.FixtureEncoding != "" {
				t.Fatalf("semantic case %s has classification/encoding %q/%q", test.Path, test.Classification, test.FixtureEncoding)
			}
		default:
			t.Fatalf("unknown rejection layer %q for %s", test.Layer, test.Path)
		}
		if test.FixtureEncoding != "" && test.FixtureEncoding != "generated_from_hex_descriptor" {
			t.Fatalf("unknown fixture encoding %q", test.FixtureEncoding)
		}
		if _, duplicate := listed[test.Path]; duplicate {
			t.Fatalf("duplicate case path %s", test.Path)
		}
		listed[test.Path] = struct{}{}
	}
	for _, pattern := range []string{
		"testdata/ipc-v1-probe-capabilities-success/valid/*.json",
		"testdata/ipc-v1-probe-capabilities-success/invalid/*.json",
	} {
		matches, err := fs.Glob(probeCapabilitiesFiles, pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			relative := strings.TrimPrefix(match, "testdata/ipc-v1-probe-capabilities-success/")
			if _, found := listed[relative]; !found {
				t.Errorf("golden vector %s is not listed", relative)
			}
		}
	}
}

func readProbeCapabilitiesCase(tb testing.TB, test probeCapabilitiesCase) []byte {
	tb.Helper()
	raw := readProbeCapabilitiesFile(tb, "testdata/ipc-v1-probe-capabilities-success/"+test.Path)
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
		tb.Fatal(err)
	}
	return bytes.Replace([]byte(descriptor.Template), []byte(descriptor.Marker), replacement, 1)
}

func readProbeCapabilitiesFile(tb testing.TB, name string) []byte {
	tb.Helper()
	contents, err := probeCapabilitiesFiles.ReadFile(name)
	if err != nil {
		tb.Fatalf("read %s: %v", name, err)
	}
	return contents
}

type probeCapabilitiesScan struct {
	tokens int
}

func scanProbeCapabilitiesJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &probeCapabilitiesScan{}
	if err := scanProbeCapabilitiesValue(decoder, state, 0); err != nil {
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

func scanProbeCapabilitiesValue(decoder *json.Decoder, state *probeCapabilitiesScan, parentDepth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := countProbeCapabilitiesToken(state); err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	depth := parentDepth + 1
	if depth > probeCapabilitiesMaxDepth {
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
			if err := countProbeCapabilitiesToken(state); err != nil {
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
			if err := scanProbeCapabilitiesValue(decoder, state, depth); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanProbeCapabilitiesValue(decoder, state, depth); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return countProbeCapabilitiesToken(state)
}

func countProbeCapabilitiesToken(state *probeCapabilitiesScan) error {
	state.tokens++
	if state.tokens > probeCapabilitiesMaxTokens {
		return fmt.Errorf("token limit exceeded")
	}
	return nil
}

func probeCapabilitiesNullArray(items int) []byte {
	if items == 0 {
		return []byte(`{"x":[]}`)
	}
	return []byte(`{"x":[` + strings.Repeat("null,", items-1) + "null]}")
}
