// Package migrations exposes the versioned SQLite migration assets embedded in
// production binaries.
package migrations

import "embed"

// FS contains every versioned SQLite migration.
//
//go:embed *.sql
var FS embed.FS
