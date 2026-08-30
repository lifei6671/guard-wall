package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	configschema "github.com/lifei6671/guard-wall/schema"
)

func TestAuthoritativeSchemaParses(t *testing.T) {
	contents := configschema.ConfigV1()
	if !json.Valid(contents) {
		t.Fatal("config-v1.schema.json is not valid JSON")
	}
	parsed, err := parseSchema(contents)
	if err != nil {
		t.Fatalf("parseSchema(): %v", err)
	}
	if parsed.Type != "object" || len(parsed.Properties) == 0 {
		t.Fatalf("parsed schema root = type %q with %d properties", parsed.Type, len(parsed.Properties))
	}
}

func TestSchemaOwnershipMetadata(t *testing.T) {
	parsed, err := parseSchema(configschema.ConfigV1())
	if err != nil {
		t.Fatalf("parseSchema(): %v", err)
	}
	var inspect func(string, schemaNode)
	inspect = func(path string, node schemaNode) {
		t.Helper()
		if len(node.Properties) > 0 {
			for name, child := range node.Properties {
				inspect(childPath(path, name), child)
			}
			return
		}
		if node.Owner == "" || node.HotReload == nil ||
			node.RestartRequired == nil || node.Sensitive == nil {
			t.Errorf("schema field %s has incomplete ownership metadata", path)
		}
		if *node.HotReload && *node.RestartRequired {
			t.Errorf("schema field %s cannot be hot-reloaded and restart-bound", path)
		}
	}
	inspect("$", parsed)
}

func TestOwnershipMatrixMatchesContract(t *testing.T) {
	tests := []struct {
		path string
		want FieldPolicy
	}{
		{
			path: "web.listen_address",
			want: FieldPolicy{Owner: "yaml", RestartRequired: true},
		},
		{
			path: "store.database_path",
			want: FieldPolicy{Owner: "yaml", RestartRequired: true},
		},
		{
			path: "logging.level",
			want: FieldPolicy{Owner: "yaml", HotReload: true},
		},
		{
			path: "smtp.credential_file",
			want: FieldPolicy{Owner: "yaml", RestartRequired: true, Sensitive: true},
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			got, err := LookupFieldPolicy(test.path)
			if err != nil {
				t.Fatalf("LookupFieldPolicy(): %v", err)
			}
			if got != test.want {
				t.Fatalf("policy = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestLookupFieldPolicyRejectsNonFields(t *testing.T) {
	for _, path := range []string{"", "runtime", "unknown.field"} {
		t.Run(path, func(t *testing.T) {
			if _, err := LookupFieldPolicy(path); !errors.Is(err, ErrFieldPolicyNotFound) {
				t.Fatalf("LookupFieldPolicy() error = %v", err)
			}
		})
	}
}

func TestConfigAndSchemaLeafPathsDoNotDrift(t *testing.T) {
	parsed, err := parseSchema(configschema.ConfigV1())
	if err != nil {
		t.Fatalf("parseSchema(): %v", err)
	}
	schemaPaths := collectSchemaLeafPaths("", parsed)
	typePaths := collectConfigLeafPaths("", reflect.TypeOf(Config{}))
	sort.Strings(schemaPaths)
	sort.Strings(typePaths)
	if !reflect.DeepEqual(schemaPaths, typePaths) {
		t.Fatalf("Config/Schema leaf drift\nSchema: %v\nConfig: %v", schemaPaths, typePaths)
	}
}

func TestLoadAppliesSchemaDefaults(t *testing.T) {
	loaded, err := Load(context.Background(), strings.NewReader(`{
		"store": {"database_path": "/var/lib/guard/guard.db"}
	}`))
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.SchemaVersion != 1 ||
		loaded.Runtime.RawQueueCapacity != 512 ||
		loaded.Runtime.EventQueueCapacity != 1024 ||
		loaded.Runtime.ReconcileQueueCapacity != 256 ||
		time.Duration(loaded.Runtime.ShutdownTimeout) != 30*time.Second ||
		time.Duration(loaded.Source.CheckpointInterval) != time.Second ||
		loaded.Source.CheckpointRecordThreshold != 256 ||
		loaded.Logging.Level != "info" ||
		loaded.Web.ListenAddress != "127.0.0.1:8080" ||
		loaded.Web.Security.AllowRemoteHTTP {
		t.Fatalf("schema defaults were not applied: %+v", loaded)
	}
}

func TestLoadResourceBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		value   any
		wantErr bool
	}{
		{name: "raw queue minimum", path: "runtime.raw_queue_capacity", value: 1},
		{name: "raw queue maximum", path: "runtime.raw_queue_capacity", value: 65536},
		{name: "raw queue below minimum", path: "runtime.raw_queue_capacity", value: 0, wantErr: true},
		{name: "raw queue above maximum", path: "runtime.raw_queue_capacity", value: 65537, wantErr: true},
		{name: "event queue minimum", path: "runtime.event_queue_capacity", value: 1},
		{name: "event queue maximum", path: "runtime.event_queue_capacity", value: 65536},
		{name: "event queue below minimum", path: "runtime.event_queue_capacity", value: 0, wantErr: true},
		{name: "event queue above maximum", path: "runtime.event_queue_capacity", value: 65537, wantErr: true},
		{name: "reconcile queue minimum", path: "runtime.reconcile_queue_capacity", value: 1},
		{name: "reconcile queue maximum", path: "runtime.reconcile_queue_capacity", value: 65536},
		{name: "reconcile queue below minimum", path: "runtime.reconcile_queue_capacity", value: 0, wantErr: true},
		{name: "reconcile queue above maximum", path: "runtime.reconcile_queue_capacity", value: 65537, wantErr: true},
		{name: "checkpoint interval minimum", path: "source.checkpoint_interval", value: "100ms"},
		{name: "checkpoint interval maximum", path: "source.checkpoint_interval", value: "30s"},
		{name: "checkpoint interval below minimum", path: "source.checkpoint_interval", value: "99ms", wantErr: true},
		{name: "checkpoint interval above maximum", path: "source.checkpoint_interval", value: "30001ms", wantErr: true},
		{name: "checkpoint threshold minimum", path: "source.checkpoint_record_threshold", value: 1},
		{name: "checkpoint threshold maximum", path: "source.checkpoint_record_threshold", value: 10000},
		{name: "checkpoint threshold below minimum", path: "source.checkpoint_record_threshold", value: 0, wantErr: true},
		{name: "checkpoint threshold above maximum", path: "source.checkpoint_record_threshold", value: 10001, wantErr: true},
		{name: "shutdown minimum", path: "runtime.shutdown_timeout", value: "5s"},
		{name: "shutdown maximum", path: "runtime.shutdown_timeout", value: "300s"},
		{name: "shutdown below minimum", path: "runtime.shutdown_timeout", value: "4999ms", wantErr: true},
		{name: "shutdown above maximum", path: "runtime.shutdown_timeout", value: "300001ms", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := baseDocument()
			setDocumentValue(document, test.path, test.value)
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			_, err = Load(context.Background(), strings.NewReader(string(encoded)))
			if (err != nil) != test.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name     string
		document string
		wantPath string
	}{
		{
			name:     "unknown root field",
			document: `{"store":{"database_path":"/var/lib/guard/guard.db"},"mystery":true}`,
			wantPath: "mystery",
		},
		{
			name:     "unknown nested field",
			document: `{"store":{"database_path":"/var/lib/guard/guard.db"},"runtime":{"drop_when_full":true}}`,
			wantPath: "runtime.drop_when_full",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(context.Background(), strings.NewReader(test.document))
			if err == nil || !strings.Contains(err.Error(), test.wantPath) {
				t.Fatalf("Load() error = %v, want path %q", err, test.wantPath)
			}
		})
	}
}

func TestLoadSecretReferenceBoundary(t *testing.T) {
	const secret = "do-not-leak-this-password"
	_, err := Load(context.Background(), strings.NewReader(fmt.Sprintf(
		`{"store":{"database_path":"/var/lib/guard/guard.db"},"smtp":{"password":%q}}`, secret)))
	if err == nil {
		t.Fatal("Load() error = nil for inline SMTP password")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() leaked sensitive value: %v", err)
	}

	loaded, err := Load(context.Background(), strings.NewReader(`{
		"store":{"database_path":"/var/lib/guard/guard.db"},
		"smtp":{"credential_file":"/etc/guard/smtp.credential"}
	}`))
	if err != nil {
		t.Fatalf("Load() credential reference: %v", err)
	}
	if loaded.SMTP.CredentialFile != "/etc/guard/smtp.credential" {
		t.Fatalf("credential file = %q", loaded.SMTP.CredentialFile)
	}

	_, err = Load(context.Background(), strings.NewReader(`{
		"store":{"database_path":"/var/lib/guard/guard.db"},
		"smtp":{"credential_file":"relative-secret"}
	}`))
	if err == nil || strings.Contains(err.Error(), "relative-secret") {
		t.Fatalf("relative credential error = %v", err)
	}
}

func TestLoadNonLoopbackPlaintextPolicy(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		allow     bool
		wantError bool
	}{
		{name: "IPv4 loopback", address: "127.0.0.1:8080"},
		{name: "IPv6 loopback", address: "[::1]:8080"},
		{name: "wildcard denied", address: "0.0.0.0:8080", wantError: true},
		{name: "remote denied", address: "192.0.2.10:8080", wantError: true},
		{name: "wildcard explicitly allowed", address: "0.0.0.0:8080", allow: true},
		{name: "missing port", address: "127.0.0.1", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := baseDocument()
			document["web"] = map[string]any{
				"listen_address": test.address,
				"security":       map[string]any{"allow_remote_http": test.allow},
			}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			_, err = Load(context.Background(), strings.NewReader(string(encoded)))
			if (err != nil) != test.wantError {
				t.Fatalf("Load() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestLoadRejectsRequiredAndTrailingContent(t *testing.T) {
	tests := []struct {
		name     string
		document string
	}{
		{name: "missing store", document: `{}`},
		{name: "missing database path", document: `{"store":{}}`},
		{name: "wrong schema version", document: `{"schema_version":2,"store":{"database_path":"/x"}}`},
		{name: "trailing document", document: `{"store":{"database_path":"/x"}} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Load(context.Background(), strings.NewReader(test.document)); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func baseDocument() map[string]any {
	return map[string]any{
		"store": map[string]any{"database_path": "/var/lib/guard/guard.db"},
	}
}

func setDocumentValue(document map[string]any, path string, value any) {
	components := strings.Split(path, ".")
	current := document
	for _, component := range components[:len(components)-1] {
		next, ok := current[component].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[component] = next
		}
		current = next
	}
	current[components[len(components)-1]] = value
}

func collectSchemaLeafPaths(prefix string, node schemaNode) []string {
	if len(node.Properties) == 0 {
		return []string{prefix}
	}
	paths := make([]string, 0)
	for name, child := range node.Properties {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		paths = append(paths, collectSchemaLeafPaths(path, child)...)
	}
	return paths
}

func collectConfigLeafPaths(prefix string, configType reflect.Type) []string {
	paths := make([]string, 0)
	for index := 0; index < configType.NumField(); index++ {
		field := configType.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if field.Type.Kind() == reflect.Struct {
			paths = append(paths, collectConfigLeafPaths(path, field.Type)...)
			continue
		}
		paths = append(paths, path)
	}
	return paths
}
