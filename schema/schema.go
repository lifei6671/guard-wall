// Package schema exposes the embedded authoritative Guard configuration schema.
package schema

import _ "embed"

//go:embed config-v1.schema.json
var configV1 []byte

// ConfigV1 returns an isolated copy of the authoritative Phase 1 schema.
func ConfigV1() []byte {
	return append([]byte(nil), configV1...)
}
