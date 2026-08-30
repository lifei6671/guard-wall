package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var migrationNamePattern = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)

type migration struct {
	version  int64
	name     string
	checksum string
	sql      string
}

func loadMigrations(migrationFS fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, ".")
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		matches := migrationNamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		contents, err := fs.ReadFile(migrationFS, path.Clean(entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		if len(strings.TrimSpace(string(contents))) == 0 {
			return nil, fmt.Errorf("migration %q is empty", entry.Name())
		}
		digest := sha256.Sum256(contents)
		migrations = append(migrations, migration{
			version:  version,
			name:     strings.TrimSuffix(entry.Name(), ".sql"),
			checksum: hex.EncodeToString(digest[:]),
			sql:      string(contents),
		})
	}
	if len(migrations) == 0 {
		return nil, fmt.Errorf("no migrations found")
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	for index := 1; index < len(migrations); index++ {
		if migrations[index-1].version == migrations[index].version {
			return nil, fmt.Errorf("duplicate migration version %d", migrations[index].version)
		}
	}
	return migrations, nil
}

func applyMigrations(ctx context.Context, db *sql.DB, migrations []migration) (returnErr error) {
	if ctx == nil {
		return fmt.Errorf("apply migrations: context is required")
	}
	if db == nil {
		return fmt.Errorf("apply migrations: database is required")
	}
	if len(migrations) == 0 {
		return fmt.Errorf("apply migrations: at least one migration is required")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackErr := tx.Rollback()
			if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				returnErr = joinErrors(returnErr, rollbackErr)
			}
		}
	}()

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY CHECK (version > 0),
			name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 128),
			checksum_sha256 TEXT NOT NULL CHECK (length(checksum_sha256) = 64),
			applied_at_us INTEGER NOT NULL CHECK (applied_at_us > 0)
		) STRICT`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT version, name, checksum_sha256
		FROM schema_migrations
		ORDER BY version`)
	if err != nil {
		return fmt.Errorf("read migration history: %w", err)
	}
	persisted := make([]migration, 0, len(migrations))
	for rows.Next() {
		var item migration
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			return joinErrors(fmt.Errorf("scan migration history: %w", err), rows.Close())
		}
		persisted = append(persisted, item)
	}
	if err := rows.Err(); err != nil {
		return joinErrors(fmt.Errorf("iterate migration history: %w", err), rows.Close())
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close migration history: %w", err)
	}

	if len(persisted) > len(migrations) {
		return fmt.Errorf(
			"database migration history has %d versions, binary only has %d: downgrade is not supported",
			len(persisted), len(migrations))
	}
	for index, applied := range persisted {
		expected := migrations[index]
		if applied.version != expected.version {
			return fmt.Errorf(
				"database migration history is not a prefix: position %d has version %04d, want %04d",
				index+1, applied.version, expected.version)
		}
		if applied.name != expected.name || applied.checksum != expected.checksum {
			return fmt.Errorf("migration %04d identity mismatch", applied.version)
		}
	}

	for _, item := range migrations[len(persisted):] {

		if _, err := tx.ExecContext(ctx, item.sql); err != nil {
			return fmt.Errorf("execute migration %04d_%s: %w", item.version, item.name, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations(version, name, checksum_sha256, applied_at_us)
			VALUES (?, ?, ?, ?)`,
			item.version, item.name, item.checksum, time.Now().UTC().UnixMicro()); err != nil {
			return fmt.Errorf("record migration %04d: %w", item.version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	committed = true
	return nil
}
