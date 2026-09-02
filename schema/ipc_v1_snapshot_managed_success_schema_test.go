package schema

import (
	"bytes"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

const (
	snapshotManagedMaxBytes   = 1 << 20
	snapshotManagedMaxDepth   = 4
	snapshotManagedMaxTokens  = 32768
	snapshotManagedMaxTargets = 1024
)

//go:embed ipc-v1-snapshot-managed-success.schema.json testdata/ipc-v1-snapshot-managed-success/cases.json testdata/ipc-v1-snapshot-managed-success/valid/*.json testdata/ipc-v1-snapshot-managed-success/invalid/*.json
var snapshotManagedSuccessFiles embed.FS

type snapshotManagedCase struct {
	Path            string `json:"path"`
	Layer           string `json:"layer"`
	Classification  string `json:"classification"`
	FixtureEncoding string `json:"fixture_encoding"`
}

type snapshotManagedCases struct {
	Valid   []snapshotManagedCase `json:"valid"`
	Invalid []snapshotManagedCase `json:"invalid"`
}

func TestSnapshotManagedSuccessSchema(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedSuccessFile(t, "ipc-v1-snapshot-managed-success.schema.json"))
	if stringValue(root["$schema"]) != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %#v", root["$schema"])
	}
	if stringValue(root["$id"]) != "https://guard-wall.local/schema/ipc-v1-snapshot-managed-success.schema.json" {
		t.Fatalf("$id = %#v", root["$id"])
	}
	assertJSONInteger(t, root, "x-guard-max-frame-bytes", snapshotManagedMaxBytes)
	assertJSONInteger(t, root, "x-guard-max-response-bytes", snapshotManagedMaxBytes)
	assertJSONInteger(t, root, "x-guard-max-instance-depth", snapshotManagedMaxDepth)
	assertJSONInteger(t, root, "x-guard-max-json-tokens", snapshotManagedMaxTokens)

	assertSnapshotObject(t, root, []string{"version", "operation", "payload"}, 3)
	properties := snapshotProperties(t, root)
	assertSnapshotConst(t, properties["version"], "integer", json.Number("1"))
	assertSnapshotConst(t, properties["operation"], "string", "SnapshotManaged")
	payload := snapshotMap(t, properties["payload"], "payload")
	assertSnapshotObject(t, payload, []string{"snapshot_digest", "infrastructure", "policy", "targets", "foreign_context_digest"}, 5)

	payloadProperties := snapshotProperties(t, payload)
	assertSnapshotDigestRef(t, payloadProperties["snapshot_digest"])
	assertSnapshotDigestRef(t, payloadProperties["foreign_context_digest"])
	assertSnapshotNullableObject(t, payloadProperties["infrastructure"], []string{"backend", "owner_version", "schema_version", "digest"}, 4)
	assertSnapshotNullableObject(t, payloadProperties["policy"], []string{"relation_digest"}, 1)
	infrastructureChoices := snapshotMap(t, payloadProperties["infrastructure"], "infrastructure")["oneOf"].([]any)
	infrastructureProperties := snapshotProperties(t, infrastructureChoices[1].(map[string]any))
	backend := snapshotMap(t, infrastructureProperties["backend"], "backend")
	if stringValue(backend["type"]) != "string" || !sameSnapshotStrings(stringSlice(backend["enum"].([]any)), []string{"nftables-native", "iptables-nft", "iptables-legacy"}) {
		t.Fatalf("backend schema = %#v", backend)
	}
	assertSnapshotConst(t, infrastructureProperties["owner_version"], "string", "guard/v1")
	assertSnapshotConst(t, infrastructureProperties["schema_version"], "integer", json.Number("1"))
	assertSnapshotDigestRef(t, infrastructureProperties["digest"])
	policyChoices := snapshotMap(t, payloadProperties["policy"], "policy")["oneOf"].([]any)
	assertSnapshotDigestRef(t, snapshotProperties(t, policyChoices[1].(map[string]any))["relation_digest"])

	targets := snapshotMap(t, payloadProperties["targets"], "targets")
	if stringValue(targets["type"]) != "array" {
		t.Fatalf("targets type = %#v", targets["type"])
	}
	assertJSONInteger(t, targets, "maxItems", snapshotManagedMaxTargets)
	target := snapshotMap(t, targets["items"], "targets.items")
	assertSnapshotObject(t, target, []string{"target", "membership", "timeout_mode", "effective_until_unix_us", "input", "forward"}, 6)
	targetProperties := snapshotProperties(t, target)
	targetPrefix := snapshotMap(t, targetProperties["target"], "target")
	if stringValue(targetPrefix["type"]) != "string" || stringValue(targetPrefix["pattern"]) != "^[0-9A-Fa-f:.]+/[0-9]{1,3}$" {
		t.Fatalf("target schema = %#v", targetPrefix)
	}
	assertSnapshotConst(t, targetProperties["membership"], "string", "present")
	timeoutMode := snapshotMap(t, targetProperties["timeout_mode"], "timeout_mode")
	if stringValue(timeoutMode["type"]) != "string" || !sameSnapshotStrings(stringSlice(timeoutMode["enum"].([]any)), []string{"none", "native"}) {
		t.Fatalf("timeout_mode schema = %#v", timeoutMode)
	}
	expiryChoices := snapshotMap(t, targetProperties["effective_until_unix_us"], "effective_until_unix_us")["oneOf"].([]any)
	if stringValue(expiryChoices[0].(map[string]any)["type"]) != "null" {
		t.Fatalf("expiry null schema = %#v", expiryChoices[0])
	}
	expiryInteger := expiryChoices[1].(map[string]any)
	if stringValue(expiryInteger["type"]) != "integer" {
		t.Fatalf("expiry integer schema = %#v", expiryInteger)
	}
	assertJSONInteger(t, expiryInteger, "minimum", 1)
	assertJSONInteger(t, expiryInteger, "maximum", 9223372036854775807)
	for _, key := range []string{"input", "forward"} {
		property := snapshotMap(t, targetProperties[key], key)
		if len(property) != 1 || stringValue(property["type"]) != "boolean" {
			t.Fatalf("%s schema = %#v", key, property)
		}
	}

	defs := snapshotMap(t, root["$defs"], "$defs")
	digest := snapshotMap(t, defs["digest"], "$defs.digest")
	if stringValue(digest["type"]) != "string" || stringValue(digest["pattern"]) != "^[0-9a-f]{64}$" {
		t.Fatalf("digest schema = %#v", digest)
	}
	assertJSONInteger(t, digest, "minLength", 64)
	assertJSONInteger(t, digest, "maxLength", 64)
}

func TestSnapshotManagedSuccessScopeMapping(t *testing.T) {
	for _, test := range []struct {
		name           string
		input, forward bool
		want           []string
	}{
		{name: "input", input: true, want: []string{"input"}},
		{name: "forward", forward: true, want: []string{"forward"}},
		{name: "input then forward", input: true, forward: true, want: []string{"input", "forward"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := snapshotWireScopes(test.input, test.forward)
			if err != nil || !sameSnapshotStringsInOrder(got, test.want) {
				t.Fatalf("scopes = %v, %v; want %v", got, err, test.want)
			}
		})
	}
	if _, err := snapshotWireScopes(false, false); err == nil {
		t.Fatal("both-false scopes accepted")
	}
}

func TestSnapshotManagedSuccessAcceptsMathematicalInteger(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedSuccessFile(t, "ipc-v1-snapshot-managed-success.schema.json"))
	raw := []byte(`{"version":1,"operation":"SnapshotManaged","payload":{"snapshot_digest":"655049f824e6f3406adade59a133701ed7efbb87dd1b5ffc79e74e02f4a1fe49","infrastructure":{"backend":"nftables-native","owner_version":"guard/v1","schema_version":1.0,"digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"policy":null,"targets":[],"foreign_context_digest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}`)
	if _, classification := validateSnapshotManagedSuccess(raw, root); classification != "" {
		t.Fatalf("Draft 2020-12 mathematical integer rejected with %s", classification)
	}
}

func TestSnapshotManagedSuccessGoldenVectors(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedSuccessFile(t, "ipc-v1-snapshot-managed-success.schema.json"))
	cases := readSnapshotManagedSuccessCases(t)
	assertSnapshotCaseCoverage(t, cases, snapshotManagedSuccessFiles, "ipc-v1-snapshot-managed-success")
	for _, test := range cases.Valid {
		test := test
		t.Run(test.Path, func(t *testing.T) {
			raw := readSnapshotManagedSuccessFile(t, "testdata/ipc-v1-snapshot-managed-success/"+test.Path)
			value, classification := validateSnapshotManagedSuccess(raw, root)
			if classification != "" {
				t.Fatalf("valid response rejected with %s", classification)
			}
			assertSnapshotManagedCanonicalOrder(t, raw)
			if err := validateSnapshotManagedSemantics(value); err != nil {
				t.Fatalf("domain mapping: %v", err)
			}
		})
	}
	for _, test := range cases.Invalid {
		test := test
		t.Run(test.Path, func(t *testing.T) {
			raw := readSnapshotManagedCase(t, snapshotManagedSuccessFiles, "ipc-v1-snapshot-managed-success", test)
			_, classification := validateSnapshotManagedSuccess(raw, root)
			if classification != test.Classification {
				t.Fatalf("classification = %q, want %q", classification, test.Classification)
			}
		})
	}
}

func TestSnapshotManagedSuccessSemanticMatrix(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedSuccessFile(t, "ipc-v1-snapshot-managed-success.schema.json"))
	base := decodeIPCV1JSON(t, readSnapshotManagedSuccessFile(t, "testdata/ipc-v1-snapshot-managed-success/valid/full-native.json"))
	tests := []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "noncanonical target", change: func(v map[string]any) { snapshotTarget(v, 0)["target"] = "192.0.2.4/24" }},
		{name: "noncanonical IPv6 target", change: func(v map[string]any) { snapshotTarget(v, 1)["target"] = "2001:0db8:0000:0000:0000:0000:0000:0000/64" }},
		{name: "bare address", change: func(v map[string]any) { snapshotTarget(v, 0)["target"] = "192.0.2.4" }},
		{name: "snapshot digest mismatch", change: func(v map[string]any) { v["payload"].(map[string]any)["snapshot_digest"] = strings.Repeat("0", 64) }},
		{name: "unordered targets", change: func(v map[string]any) {
			targets := v["payload"].(map[string]any)["targets"].([]any)
			targets[0], targets[1] = targets[1], targets[0]
		}},
		{name: "duplicate target", change: func(v map[string]any) {
			targets := v["payload"].(map[string]any)["targets"].([]any)
			targets[1] = targets[0]
		}},
		{name: "protected loopback target", change: func(v map[string]any) { snapshotTarget(v, 0)["target"] = "127.0.0.1/32" }},
		{name: "none with expiry", change: func(v map[string]any) { snapshotTarget(v, 0)["effective_until_unix_us"] = json.Number("1") }},
		{name: "native without expiry", change: func(v map[string]any) { snapshotTarget(v, 1)["effective_until_unix_us"] = nil }},
		{name: "both scopes false", change: func(v map[string]any) { p := snapshotTarget(v, 0); p["input"] = false; p["forward"] = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneSnapshotJSON(t, base)
			test.change(candidate)
			want := "semantic_rejected"
			if test.name == "bare address" {
				want = "schema_rejected"
			}
			assertSnapshotSuccessClassification(t, root, candidate, want)
		})
	}
}

func TestSnapshotManagedSuccessRequiredAndTypeConfusion(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedSuccessFile(t, "ipc-v1-snapshot-managed-success.schema.json"))
	valid := decodeIPCV1JSON(t, readSnapshotManagedSuccessFile(t, "testdata/ipc-v1-snapshot-managed-success/valid/full-native.json"))
	for _, key := range []string{"version", "operation", "payload"} {
		t.Run("missing root "+key, func(t *testing.T) {
			candidate := cloneSnapshotJSON(t, valid)
			delete(candidate, key)
			assertSnapshotSuccessClassification(t, root, candidate, "schema_rejected")
		})
	}
	for _, key := range []string{"snapshot_digest", "infrastructure", "policy", "targets", "foreign_context_digest"} {
		t.Run("missing payload "+key, func(t *testing.T) {
			candidate := cloneSnapshotJSON(t, valid)
			delete(candidate["payload"].(map[string]any), key)
			assertSnapshotSuccessClassification(t, root, candidate, "schema_rejected")
		})
	}
	for _, key := range []string{"target", "membership", "timeout_mode", "effective_until_unix_us", "input", "forward"} {
		t.Run("missing target "+key, func(t *testing.T) {
			candidate := cloneSnapshotJSON(t, valid)
			delete(snapshotTarget(candidate, 0), key)
			assertSnapshotSuccessClassification(t, root, candidate, "schema_rejected")
		})
	}
	for _, key := range []string{"input", "forward"} {
		t.Run("boolean type "+key, func(t *testing.T) {
			candidate := cloneSnapshotJSON(t, valid)
			snapshotTarget(candidate, 0)[key] = "true"
			assertSnapshotSuccessClassification(t, root, candidate, "schema_rejected")
		})
	}
}

func TestSnapshotManagedSuccessResourceLimits(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedSuccessFile(t, "ipc-v1-snapshot-managed-success.schema.json"))
	base := readSnapshotManagedSuccessFile(t, "testdata/ipc-v1-snapshot-managed-success/valid/empty-managed.json")
	exactBytes := append(append([]byte(nil), base...), bytes.Repeat([]byte(" "), snapshotManagedMaxBytes-len(base))...)
	if _, got := validateSnapshotManagedSuccess(exactBytes, root); got != "" {
		t.Fatalf("exact bytes rejected: %s", got)
	}
	if _, got := validateSnapshotManagedSuccess(append(exactBytes, ' '), root); got != "response_too_large" {
		t.Fatalf("one-over bytes = %q", got)
	}

	if err := scanSnapshotJSON([]byte(`{"x":[{"y":[]} ]}`)); err != nil {
		t.Fatalf("exact depth rejected: %v", err)
	}
	if _, got := validateSnapshotManagedSuccess([]byte(`{"x":[{"y":[[]]}]}`), root); got != "max_depth_exceeded" {
		t.Fatalf("one-over depth = %q", got)
	}

	exactTokens := snapshotNullArray(snapshotManagedMaxTokens - 2)
	if err := scanSnapshotJSON(exactTokens); err != nil {
		t.Fatalf("exact tokens rejected: %v", err)
	}
	if _, got := validateSnapshotManagedSuccess(snapshotNullArray(snapshotManagedMaxTokens-1), root); got != "token_limit_exceeded" {
		t.Fatalf("one-over tokens = %q", got)
	}

	exactTargets := snapshotResponseWithTargets(t, snapshotManagedMaxTargets)
	if _, got := validateSnapshotManagedSuccess(exactTargets, root); got != "" {
		t.Fatalf("1024 targets rejected: %s", got)
	}
	oneOverTargets := snapshotResponseWithTargets(t, snapshotManagedMaxTargets+1)
	if _, got := validateSnapshotManagedSuccess(oneOverTargets, root); got != "schema_rejected" {
		t.Fatalf("1025 targets = %q", got)
	}
}

func TestSnapshotManagedSuccessClassificationIsSanitized(t *testing.T) {
	root := decodeIPCV1JSON(t, readSnapshotManagedSuccessFile(t, "ipc-v1-snapshot-managed-success.schema.json"))
	secret := "do-not-echo-secret"
	raw := []byte(`{"version":1,"operation":"SnapshotManaged","payload":{"command":"` + secret + `"}}`)
	_, classification := validateSnapshotManagedSuccess(raw, root)
	if classification != "schema_rejected" || strings.Contains(classification, secret) || strings.Contains(classification, "command") {
		t.Fatalf("unsafe classification %q", classification)
	}
}

func FuzzSnapshotManagedSuccessClosedContract(f *testing.F) {
	root := decodeIPCV1JSON(f, readSnapshotManagedSuccessFile(f, "ipc-v1-snapshot-managed-success.schema.json"))
	cases := readSnapshotManagedSuccessCases(f)
	for _, test := range cases.Valid {
		f.Add(readSnapshotManagedSuccessFile(f, "testdata/ipc-v1-snapshot-managed-success/"+test.Path))
	}
	for _, test := range cases.Invalid {
		if test.Classification == "duplicate_key" || test.Classification == "invalid_utf8" || test.Classification == "semantic_rejected" {
			f.Add(readSnapshotManagedCase(f, snapshotManagedSuccessFiles, "ipc-v1-snapshot-managed-success", test))
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, classification := validateSnapshotManagedSuccess(raw, root)
		if classification == "" {
			if err := validateSnapshotManagedSemantics(value); err != nil {
				t.Fatalf("accepted invalid domain: %v", err)
			}
		}
	})
}

func validateSnapshotManagedSuccess(raw []byte, root map[string]any) (map[string]any, string) {
	return validateSnapshotResponse(raw, root, validateSnapshotManagedSemantics)
}

func validateSnapshotResponse(raw []byte, root map[string]any, semantic func(map[string]any) error) (map[string]any, string) {
	if len(raw) > snapshotManagedMaxBytes {
		return nil, "response_too_large"
	}
	if !utf8.Valid(raw) {
		return nil, "invalid_utf8"
	}
	if err := scanSnapshotJSON(raw); err != nil {
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
	if err := validateSnapshotSchema(root, root, value, "$"); err != nil {
		return value, "schema_rejected"
	}
	if semantic != nil {
		if err := semantic(value); err != nil {
			return value, "semantic_rejected"
		}
	}
	return value, ""
}

func validateSnapshotSchema(root, schema map[string]any, value any, path string) error {
	if ref, ok := schema["$ref"].(string); ok {
		if ref != "#/$defs/digest" {
			return fmt.Errorf("%s unsupported ref", path)
		}
		defs, _ := root["$defs"].(map[string]any)
		digest, _ := defs["digest"].(map[string]any)
		return validateSnapshotSchema(root, digest, value, path)
	}
	if alternatives, ok := schema["oneOf"].([]any); ok {
		matched := 0
		for _, raw := range alternatives {
			if validateSnapshotSchema(root, raw.(map[string]any), value, path) == nil {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("%s oneOf matches %d", path, matched)
		}
		return nil
	}
	if constant, ok := schema["const"]; ok && !equalJSONValue(constant, value) {
		return fmt.Errorf("%s const", path)
	}
	if enumeration, ok := schema["enum"].([]any); ok {
		found := false
		for _, candidate := range enumeration {
			found = found || equalJSONValue(candidate, value)
		}
		if !found {
			return fmt.Errorf("%s enum", path)
		}
	}
	switch stringValue(schema["type"]) {
	case "null":
		if value != nil {
			return fmt.Errorf("%s type", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s type", path)
		}
	case "integer":
		integer, ok := jsonInt64(value)
		if !ok {
			return fmt.Errorf("%s type", path)
		}
		if minimum, ok := jsonInt64(schema["minimum"]); ok && integer < minimum {
			return fmt.Errorf("%s minimum", path)
		}
		if maximum, ok := jsonInt64(schema["maximum"]); ok && integer > maximum {
			return fmt.Errorf("%s maximum", path)
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s type", path)
		}
		if n, ok := jsonInt64(schema["minLength"]); ok && int64(utf8.RuneCountInString(text)) < n {
			return fmt.Errorf("%s minLength", path)
		}
		if n, ok := jsonInt64(schema["maxLength"]); ok && int64(utf8.RuneCountInString(text)) > n {
			return fmt.Errorf("%s maxLength", path)
		}
		if pattern, ok := schema["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(text) {
			return fmt.Errorf("%s pattern", path)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s type", path)
		}
		if n, ok := jsonInt64(schema["minItems"]); ok && int64(len(items)) < n {
			return fmt.Errorf("%s minItems", path)
		}
		if n, ok := jsonInt64(schema["maxItems"]); ok && int64(len(items)) > n {
			return fmt.Errorf("%s maxItems", path)
		}
		itemSchema := snapshotMapUnchecked(schema["items"])
		for index, item := range items {
			if err := validateSnapshotSchema(root, itemSchema, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s type", path)
		}
		if n, ok := jsonInt64(schema["maxProperties"]); ok && int64(len(object)) > n {
			return fmt.Errorf("%s maxProperties", path)
		}
		properties := snapshotMapUnchecked(schema["properties"])
		for _, required := range stringSlice(schema["required"].([]any)) {
			if _, found := object[required]; !found {
				return fmt.Errorf("%s missing", path)
			}
		}
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			for key := range object {
				if _, found := properties[key]; !found {
					return fmt.Errorf("%s unknown", path)
				}
			}
		}
		for key, child := range object {
			if childSchema, found := properties[key].(map[string]any); found {
				if err := validateSnapshotSchema(root, childSchema, child, path+"."+key); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateSnapshotManagedSemantics(value map[string]any) error {
	payload, ok := value["payload"].(map[string]any)
	if !ok {
		return fmt.Errorf("payload")
	}
	targets, ok := payload["targets"].([]any)
	if !ok {
		return fmt.Errorf("targets")
	}
	observations := make([]firewall.TargetObservation, 0, len(targets))
	previousTarget := ""
	for _, raw := range targets {
		target := raw.(map[string]any)
		targetText := stringValue(target["target"])
		prefix, err := netip.ParsePrefix(targetText)
		if err != nil || prefix != prefix.Masked() || prefix.String() != targetText {
			return fmt.Errorf("target is not canonical")
		}
		if previousTarget != "" && previousTarget >= targetText {
			return fmt.Errorf("targets are not unique canonical order")
		}
		previousTarget = targetText
		mode := stringValue(target["timeout_mode"])
		expiry := target["effective_until_unix_us"]
		if mode == "none" && expiry != nil {
			return fmt.Errorf("none has expiry")
		}
		if mode == "native" {
			if n, ok := jsonInt64(expiry); !ok || n <= 0 {
				return fmt.Errorf("native lacks expiry")
			}
		}
		input, inputOK := target["input"].(bool)
		forward, forwardOK := target["forward"].(bool)
		if !inputOK || !forwardOK {
			return fmt.Errorf("target scope type")
		}
		scopeNames, err := snapshotWireScopes(input, forward)
		if err != nil {
			return err
		}
		scopes := make([]firewall.ManagedScope, 0, len(scopeNames))
		for _, scope := range scopeNames {
			scopes = append(scopes, firewall.ManagedScope(scope))
		}
		var expiryValue *int64
		if expiry != nil {
			integer, ok := jsonInt64(expiry)
			if !ok {
				return fmt.Errorf("expiry")
			}
			expiryValue = &integer
		}
		observation, err := firewall.NewTargetObservation(firewall.TargetObservationSpec{
			Target: prefix, TimeoutMode: firewall.ManagedTimeoutMode(mode),
			EffectiveUntilUnixMicro: expiryValue, Scopes: scopes,
		})
		if err != nil {
			return fmt.Errorf("target domain")
		}
		observations = append(observations, observation)
	}

	var infrastructure *firewall.InfrastructureObservation
	if raw := payload["infrastructure"]; raw != nil {
		object := raw.(map[string]any)
		schemaVersion, _ := jsonInt64(object["schema_version"])
		observation, err := firewall.NewInfrastructureObservation(firewall.InfrastructureObservationSpec{
			Backend: firewall.BackendKind(stringValue(object["backend"])), OwnerVersion: stringValue(object["owner_version"]),
			SchemaVersion: schemaVersion, Digest: stringValue(object["digest"]),
		})
		if err != nil {
			return fmt.Errorf("infrastructure domain")
		}
		infrastructure = &observation
	}
	var policy *firewall.PolicyObservation
	if raw := payload["policy"]; raw != nil {
		observation, err := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{
			RelationDigest: stringValue(raw.(map[string]any)["relation_digest"]),
		})
		if err != nil {
			return fmt.Errorf("policy domain")
		}
		policy = &observation
	}
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{
		Infrastructure: infrastructure, Policy: policy, Targets: observations,
	})
	if err != nil {
		return fmt.Errorf("managed state domain")
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{
		Digest: stringValue(payload["foreign_context_digest"]),
	})
	if err != nil {
		return fmt.Errorf("foreign context domain")
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{
		ManagedState: state, ForeignContext: foreign,
	})
	if err != nil || snapshot.Digest() != stringValue(payload["snapshot_digest"]) {
		return fmt.Errorf("snapshot digest mismatch")
	}
	return nil
}

func snapshotWireScopes(input, forward bool) ([]string, error) {
	scopes := make([]string, 0, 2)
	if input {
		scopes = append(scopes, "input")
	}
	if forward {
		scopes = append(scopes, "forward")
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("target has no scope")
	}
	return scopes, nil
}

type snapshotScan struct{ tokens int }

func scanSnapshotJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &snapshotScan{}
	if err := scanSnapshotValue(decoder, state, 0); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values after %v", token)
		}
		return err
	}
	return nil
}

func scanSnapshotValue(decoder *json.Decoder, state *snapshotScan, parentDepth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := countSnapshotToken(state); err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	depth := parentDepth + 1
	if depth > snapshotManagedMaxDepth {
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
			if err := countSnapshotToken(state); err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanSnapshotValue(decoder, state, depth); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanSnapshotValue(decoder, state, depth); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := countSnapshotToken(state); err != nil {
		return err
	}
	if end != json.Delim(map[json.Delim]rune{'{': '}', '[': ']'}[delimiter]) {
		return fmt.Errorf("mismatched delimiter")
	}
	return nil
}

func countSnapshotToken(state *snapshotScan) error {
	state.tokens++
	if state.tokens > snapshotManagedMaxTokens {
		return fmt.Errorf("token limit exceeded")
	}
	return nil
}

func snapshotNullArray(nulls int) []byte {
	return []byte("[" + strings.TrimSuffix(strings.Repeat("null,", nulls), ",") + "]")
}

func snapshotResponseWithTargets(t *testing.T, count int) []byte {
	t.Helper()
	targets := make([]map[string]any, count)
	for index := range targets {
		targets[index] = map[string]any{
			"target": fmt.Sprintf("198.51.%d.%d/32", index/256, index%256), "membership": "present", "timeout_mode": "none",
			"effective_until_unix_us": nil, "input": true, "forward": false,
		}
	}
	sort.Slice(targets, func(left, right int) bool {
		return stringValue(targets[left]["target"]) < stringValue(targets[right]["target"])
	})
	observations := make([]firewall.TargetObservation, 0, len(targets))
	for _, target := range targets {
		prefix := netip.MustParsePrefix(stringValue(target["target"]))
		observation, err := firewall.NewTargetObservation(firewall.TargetObservationSpec{
			Target: prefix, TimeoutMode: firewall.ManagedTimeoutNone,
			Scopes: []firewall.ManagedScope{firewall.ManagedScopeInput},
		})
		if err != nil {
			t.Fatal(err)
		}
		observations = append(observations, observation)
	}
	foreignDigest := strings.Repeat("b", 64)
	snapshotDigest := strings.Repeat("a", 64)
	if count <= snapshotManagedMaxTargets {
		state, err := firewall.NewManagedState(firewall.ManagedStateSpec{Targets: observations})
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: foreignDigest})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
		if err != nil {
			t.Fatal(err)
		}
		snapshotDigest = snapshot.Digest()
	}
	value := map[string]any{"version": 1, "operation": "SnapshotManaged", "payload": map[string]any{
		"snapshot_digest": snapshotDigest, "infrastructure": nil, "policy": nil,
		"targets": targets, "foreign_context_digest": foreignDigest,
	}}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertSnapshotManagedCanonicalOrder(t *testing.T, raw []byte) {
	t.Helper()
	rootOrder := []string{`"version":`, `"operation":`, `"payload":`}
	payloadOrder := []string{`"snapshot_digest":`, `"infrastructure":`, `"policy":`, `"targets":`, `"foreign_context_digest":`}
	for _, order := range [][]string{rootOrder, payloadOrder} {
		position := -1
		for _, marker := range order {
			next := bytes.Index(raw, []byte(marker))
			if next <= position {
				t.Fatalf("noncanonical field order at %s", marker)
			}
			position = next
		}
	}
}

func snapshotTarget(value map[string]any, index int) map[string]any {
	return value["payload"].(map[string]any)["targets"].([]any)[index].(map[string]any)
}
func snapshotMapUnchecked(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}
func snapshotMap(t *testing.T, value any, name string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v", name, value)
	}
	return object
}
func snapshotProperties(t *testing.T, schema map[string]any) map[string]any {
	return snapshotMap(t, schema["properties"], "properties")
}

func assertSnapshotObject(t *testing.T, schema map[string]any, required []string, maxProperties int64) {
	t.Helper()
	if stringValue(schema["type"]) != "object" || schema["additionalProperties"] != false {
		t.Fatalf("object is not closed: %#v", schema)
	}
	assertJSONInteger(t, schema, "maxProperties", maxProperties)
	got := stringSlice(schema["required"].([]any))
	if !sameSnapshotStrings(got, required) {
		t.Fatalf("required = %v", got)
	}
	properties := snapshotProperties(t, schema)
	names := make([]string, 0, len(properties))
	for key := range properties {
		names = append(names, key)
	}
	if !sameSnapshotStrings(names, required) {
		t.Fatalf("properties = %v", names)
	}
}

func assertSnapshotConst(t *testing.T, raw any, kind string, constant any) {
	t.Helper()
	schema := snapshotMap(t, raw, "const")
	if stringValue(schema["type"]) != kind || !equalJSONValue(schema["const"], constant) {
		t.Fatalf("const schema = %#v", schema)
	}
}
func assertSnapshotDigestRef(t *testing.T, raw any) {
	t.Helper()
	schema := snapshotMap(t, raw, "digest ref")
	if len(schema) != 1 || schema["$ref"] != "#/$defs/digest" {
		t.Fatalf("digest ref = %#v", schema)
	}
}
func assertSnapshotNullableObject(t *testing.T, raw any, required []string, max int64) {
	t.Helper()
	schema := snapshotMap(t, raw, "nullable")
	choices := schema["oneOf"].([]any)
	if len(choices) != 2 || stringValue(choices[0].(map[string]any)["type"]) != "null" {
		t.Fatalf("nullable schema = %#v", schema)
	}
	assertSnapshotObject(t, choices[1].(map[string]any), required, max)
}

func sameSnapshotStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	a, b := append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

func sameSnapshotStringsInOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func assertSnapshotSuccessClassification(t *testing.T, root, value map[string]any, want string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, got := validateSnapshotManagedSuccess(raw, root)
	if got != want {
		t.Fatalf("classification = %q, want %q", got, want)
	}
}
func cloneSnapshotJSON(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return decodeIPCV1JSON(t, raw)
}

func readSnapshotManagedSuccessCases(tb testing.TB) snapshotManagedCases {
	tb.Helper()
	var cases snapshotManagedCases
	decodeIPCV1Into(tb, readSnapshotManagedSuccessFile(tb, "testdata/ipc-v1-snapshot-managed-success/cases.json"), &cases)
	return cases
}
func readSnapshotManagedSuccessFile(tb testing.TB, name string) []byte {
	tb.Helper()
	raw, err := snapshotManagedSuccessFiles.ReadFile(name)
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

func readSnapshotManagedCase(tb testing.TB, files embed.FS, directory string, test snapshotManagedCase) []byte {
	tb.Helper()
	raw, err := files.ReadFile("testdata/" + directory + "/" + test.Path)
	if err != nil {
		tb.Fatal(err)
	}
	if test.FixtureEncoding == "" {
		return raw
	}
	var generator struct {
		Generator   string `json:"generator"`
		Template    string `json:"template"`
		Marker      string `json:"marker"`
		BytesHex    string `json:"bytes_hex"`
		TargetBytes int    `json:"target_bytes"`
		Nulls       int    `json:"nulls"`
	}
	decodeIPCV1Into(tb, raw, &generator)
	switch test.FixtureEncoding {
	case "replace_marker_with_hex":
		bad, err := hex.DecodeString(generator.BytesHex)
		if err != nil {
			tb.Fatal(err)
		}
		return bytes.Replace([]byte(generator.Template), []byte(generator.Marker), bad, 1)
	case "generated_padded_template":
		if len(generator.Template) > generator.TargetBytes {
			tb.Fatalf("template exceeds target")
		}
		return append([]byte(generator.Template), bytes.Repeat([]byte(" "), generator.TargetBytes-len(generator.Template))...)
	case "generated_null_array":
		return snapshotNullArray(generator.Nulls)
	default:
		tb.Fatalf("unknown fixture encoding %q", test.FixtureEncoding)
	}
	return nil
}

func assertSnapshotCaseCoverage(t *testing.T, cases snapshotManagedCases, files embed.FS, directory string) {
	t.Helper()
	listed := make(map[string]struct{})
	for _, test := range append(append([]snapshotManagedCase(nil), cases.Valid...), cases.Invalid...) {
		if _, duplicate := listed[test.Path]; duplicate {
			t.Fatalf("duplicate case %s", test.Path)
		}
		listed[test.Path] = struct{}{}
	}
	for _, pattern := range []string{"testdata/" + directory + "/valid/*.json", "testdata/" + directory + "/invalid/*.json"} {
		matches, err := fs.Glob(files, pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			relative := strings.TrimPrefix(match, "testdata/"+directory+"/")
			if _, ok := listed[relative]; !ok {
				t.Fatalf("unlisted fixture %s", relative)
			}
		}
	}
}
