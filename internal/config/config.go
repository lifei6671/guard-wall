// Package config loads and validates Guard configuration against the embedded
// authoritative JSON Schema.
package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	configschema "github.com/lifei6671/guard-wall/schema"
)

// Duration is a schema-validated Go duration encoded as a JSON string.
type Duration time.Duration

// UnmarshalJSON decodes a duration without defining its allowed range. Ranges
// and defaults remain owned by config-v1.schema.json.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("parse duration: %w", err)
	}
	*d = Duration(parsed)
	return nil
}

// Runtime contains process queues and shutdown behavior.
type Runtime struct {
	RawQueueCapacity       int      `json:"raw_queue_capacity"`
	EventQueueCapacity     int      `json:"event_queue_capacity"`
	ReconcileQueueCapacity int      `json:"reconcile_queue_capacity"`
	ShutdownTimeout        Duration `json:"shutdown_timeout"`
}

// Source contains Source checkpoint persistence controls.
type Source struct {
	CheckpointInterval        Duration `json:"checkpoint_interval"`
	CheckpointRecordThreshold int      `json:"checkpoint_record_threshold"`
}

// Store contains restart-bound SQLite configuration.
type Store struct {
	DatabasePath string `json:"database_path"`
}

// Logging contains atomically reloadable logging configuration.
type Logging struct {
	Level string `json:"level"`
}

// WebSecurity contains explicit acknowledgements for unsafe Web exposure.
type WebSecurity struct {
	AllowRemoteHTTP bool `json:"allow_remote_http"`
}

// Web contains the management HTTP listener configuration.
type Web struct {
	ListenAddress string      `json:"listen_address"`
	Security      WebSecurity `json:"security"`
}

// SMTP contains only the Phase 1 credential-file ownership boundary. The
// credential content is not part of Config and must never be persisted here.
type SMTP struct {
	CredentialFile string `json:"credential_file"`
}

// Config is the typed Phase 1 runtime configuration.
type Config struct {
	SchemaVersion int     `json:"schema_version"`
	Runtime       Runtime `json:"runtime"`
	Source        Source  `json:"source"`
	Store         Store   `json:"store"`
	Logging       Logging `json:"logging"`
	Web           Web     `json:"web"`
	SMTP          SMTP    `json:"smtp"`
}

// Load reads one JSON configuration document. JSON is also a valid YAML 1.2
// document, so this provides the dependency-free M0 loader without claiming
// support for the wider YAML syntax surface.
func Load(ctx context.Context, reader io.Reader) (Config, error) {
	if ctx == nil {
		return Config{}, fmt.Errorf("load config: context is required")
	}
	if reader == nil {
		return Config{}, fmt.Errorf("load config: reader is required")
	}
	if err := ctx.Err(); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}

	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return Config{}, fmt.Errorf("load config: decode document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return Config{}, fmt.Errorf("load config: trailing content: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}

	authority, err := parseSchema(configschema.ConfigV1())
	if err != nil {
		return Config{}, fmt.Errorf("load config: authoritative schema: %w", err)
	}
	normalized, err := authority.validate(document)
	if err != nil {
		return Config{}, fmt.Errorf("load config: validate: %w", err)
	}

	encoded, err := json.Marshal(normalized)
	if err != nil {
		return Config{}, fmt.Errorf("load config: encode normalized document: %w", err)
	}
	typedDecoder := json.NewDecoder(bytes.NewReader(encoded))
	typedDecoder.DisallowUnknownFields()
	var result Config
	if err := typedDecoder.Decode(&result); err != nil {
		return Config{}, fmt.Errorf("load config: decode typed document: %w", err)
	}
	return result, nil
}
