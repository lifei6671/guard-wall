package config

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLoadYAMLDocument(t *testing.T) {
	loaded, err := Load(context.Background(), strings.NewReader(`
# JSON Schema remains the default and validation authority.
store:
  database_path: >-
    /var/lib/guard/guard.db
runtime:
  raw_queue_capacity: 1024
  shutdown_timeout: 45s
logging:
  level: warn
web:
  listen_address: "[::1]:9090"
smtp:
  credential_file: /etc/guard/smtp.credential
`))
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Store.DatabasePath != "/var/lib/guard/guard.db" ||
		loaded.Runtime.RawQueueCapacity != 1024 ||
		time.Duration(loaded.Runtime.ShutdownTimeout) != 45*time.Second ||
		loaded.Logging.Level != "warn" ||
		loaded.Web.ListenAddress != "[::1]:9090" ||
		loaded.SMTP.CredentialFile != "/etc/guard/smtp.credential" {
		t.Fatalf("loaded config = %+v", loaded)
	}
	if loaded.Runtime.EventQueueCapacity != 1024 ||
		loaded.Source.CheckpointRecordThreshold != 256 {
		t.Fatalf("schema defaults were not applied: %+v", loaded)
	}
}

func TestLoadRejectsUnsafeYAMLStructures(t *testing.T) {
	tests := []struct {
		name     string
		document string
		want     string
	}{
		{
			name: "anchor",
			document: `store: &store
  database_path: /var/lib/guard/guard.db
`,
			want: "anchors and aliases are not supported",
		},
		{
			name: "anchored key",
			document: `&key store:
  database_path: /var/lib/guard/guard.db
`,
			want: "anchors and aliases are not supported",
		},
		{
			name: "alias and merge",
			document: `defaults: &defaults
  database_path: /var/lib/guard/guard.db
store:
  <<: *defaults
`,
			want: "anchors and aliases are not supported",
		},
		{
			name: "merge without alias",
			document: `store:
  <<: {database_path: /var/lib/guard/guard.db}
`,
			want: "merge keys are not supported",
		},
		{
			name: "duplicate key",
			document: `store:
  database_path: /var/lib/guard/one.db
  database_path: /var/lib/guard/two.db
`,
			want: "duplicate mapping key",
		},
		{
			name: "duplicate root key",
			document: `store: {database_path: /var/lib/guard/one.db}
store: {database_path: /var/lib/guard/two.db}
`,
			want: "duplicate mapping key",
		},
		{
			name: "non-string key",
			document: `store:
  1: /var/lib/guard/guard.db
`,
			want: "mapping keys must be strings",
		},
		{
			name: "complex key",
			document: `? [store]
: {database_path: /var/lib/guard/guard.db}
`,
			want: "mapping keys must be strings",
		},
		{
			name: "multiple documents",
			document: `store: {database_path: /var/lib/guard/one.db}
---
store: {database_path: /var/lib/guard/two.db}
`,
			want: "multiple YAML documents",
		},
		{
			name: "second empty document",
			document: `store: {database_path: /var/lib/guard/one.db}
---
`,
			want: "multiple YAML documents",
		},
		{
			name: "custom tag",
			document: `store:
  database_path: !path /var/lib/guard/guard.db
`,
			want: "explicit YAML tags are not supported",
		},
		{
			name: "explicit core tag",
			document: `store:
  database_path: !!str /var/lib/guard/guard.db
`,
			want: "explicit YAML tags are not supported",
		},
		{
			name: "explicit tag on key",
			document: `!!str store:
  database_path: /var/lib/guard/guard.db
`,
			want: "explicit YAML tags are not supported",
		},
		{
			name: "YAML float is not an integer",
			document: `store: {database_path: /var/lib/guard/guard.db}
runtime: {raw_queue_capacity: 1.0}
`,
			want: "floating-point values are not supported",
		},
		{
			name: "implicit timestamp",
			document: `store: {database_path: 2026-08-31}
`,
			want: "timestamps must be quoted strings",
		},
		{
			name: "non-decimal integer",
			document: `store: {database_path: /var/lib/guard/guard.db}
runtime: {raw_queue_capacity: 0x10}
`,
			want: "integers must use decimal JSON syntax",
		},
		{
			name: "integer with separators",
			document: `store: {database_path: /var/lib/guard/guard.db}
runtime: {raw_queue_capacity: 1_000}
`,
			want: "integers must use decimal JSON syntax",
		},
		{
			name: "oversized integer",
			document: `store: {database_path: /var/lib/guard/guard.db}
runtime: {raw_queue_capacity: 18446744073709551616}
`,
			want: "floating-point values are not supported",
		},
		{
			name:     "null root",
			document: "null\n",
			want:     "$ must be an object",
		},
		{
			name:     "sequence root",
			document: "- store\n",
			want:     "$ must be an object",
		},
		{
			name: "YAML 1.1 boolean alias remains a string",
			document: `store: {database_path: /var/lib/guard/guard.db}
web: {security: {allow_remote_http: yes}}
`,
			want: "web.security.allow_remote_http must be a boolean",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(context.Background(), strings.NewReader(test.document))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadYAMLDocumentMarkers(t *testing.T) {
	loaded, err := Load(context.Background(), strings.NewReader(`---
store: {database_path: /var/lib/guard/guard.db}
...
`))
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Store.DatabasePath != "/var/lib/guard/guard.db" {
		t.Fatalf("database path = %q", loaded.Store.DatabasePath)
	}
}

func TestLoadYAMLDocumentByteLimit(t *testing.T) {
	base := "store: {database_path: /var/lib/guard/guard.db}\n"
	atLimit := base + "#" + strings.Repeat("x", maxYAMLDocumentBytes-len(base)-2) + "\n"
	if len(atLimit) != maxYAMLDocumentBytes {
		t.Fatalf("test document length = %d, want %d", len(atLimit), maxYAMLDocumentBytes)
	}
	if _, err := Load(context.Background(), strings.NewReader(atLimit)); err != nil {
		t.Fatalf("Load() at byte limit: %v", err)
	}

	_, err := Load(context.Background(), strings.NewReader(atLimit+" "))
	if err == nil || !strings.Contains(err.Error(), "exceeds 64 KiB limit") {
		t.Fatalf("Load() over byte limit error = %v", err)
	}
}

func TestLoadYAMLDocumentNodeLimit(t *testing.T) {
	atLimit := "[" + strings.Repeat("null,", maxYAMLDocumentNodes-3) + "null]"
	if _, err := decodeYAMLDocument(strings.NewReader(atLimit)); err != nil {
		t.Fatalf("decodeYAMLDocument() at node limit: %v", err)
	}

	overLimit := "[" + strings.Repeat("null,", maxYAMLDocumentNodes-2) + "null]"
	_, err := Load(context.Background(), strings.NewReader(overLimit))
	if err == nil || !strings.Contains(err.Error(), "exceeds node limit of 512") {
		t.Fatalf("Load() over node limit error = %v", err)
	}
}

func TestLoadYAMLDocumentDepthLimit(t *testing.T) {
	atLimit := strings.Repeat("[", maxYAMLDocumentDepth-1) + "null" +
		strings.Repeat("]", maxYAMLDocumentDepth-1)
	if _, err := decodeYAMLDocument(strings.NewReader(atLimit)); err != nil {
		t.Fatalf("decodeYAMLDocument() at depth limit: %v", err)
	}

	overLimit := strings.Repeat("[", maxYAMLDocumentDepth) + "null" +
		strings.Repeat("]", maxYAMLDocumentDepth)
	_, err := Load(context.Background(), strings.NewReader(overLimit))
	if err == nil || !strings.Contains(err.Error(), "exceeds depth limit of 32") {
		t.Fatalf("Load() over depth limit error = %v", err)
	}
}

func TestLoadYAMLRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Load(ctx, strings.NewReader("store: {database_path: /var/lib/guard/guard.db}"))
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("Load() error = %v, want context cancellation", err)
	}
}

func TestLoadYAMLSecretErrorsDoNotLeakValues(t *testing.T) {
	const secret = "do-not-leak-this-yaml-password"
	_, err := Load(context.Background(), strings.NewReader(`
store: {database_path: /var/lib/guard/guard.db}
smtp:
  password: `+secret+`
`))
	if err == nil {
		t.Fatal("Load() error = nil for inline SMTP password")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() leaked sensitive value: %v", err)
	}

	_, err = Load(context.Background(), strings.NewReader("store: ["+secret+"\n"))
	if err == nil {
		t.Fatal("Load() error = nil for invalid YAML")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() syntax error leaked sensitive value: %v", err)
	}
}
