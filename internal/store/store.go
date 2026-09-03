// Package store provides the Phase 1 SQLite persistence boundary.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"modernc.org/sqlite"
)

const (
	busyTimeoutMilliseconds = 5000
	walAutoCheckpointPages  = 1000
)

var registerConnectionHook sync.Once

// Store owns the shared SQLite connection pool.
type Store struct {
	db  *sql.DB
	orm *gorm.DB
}

// Open opens a local SQLite database, applies all migrations atomically, and
// verifies the frozen PRAGMA contract before returning a ready Store.
func Open(ctx context.Context, databasePath string, migrationFS fs.FS) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("open store: context is required")
	}
	if migrationFS == nil {
		return nil, fmt.Errorf("open store: migration filesystem is required")
	}

	db, err := openDatabase(ctx, databasePath)
	if err != nil {
		return nil, err
	}

	migrations, err := loadMigrations(migrationFS)
	if err != nil {
		closeErr := db.Close()
		return nil, joinErrors(fmt.Errorf("open store: load migrations: %w", err), closeErr)
	}
	if err := applyMigrations(ctx, db, migrations); err != nil {
		closeErr := db.Close()
		return nil, joinErrors(fmt.Errorf("open store: apply migrations: %w", err), closeErr)
	}

	store := &Store{db: db}
	if _, err := store.Pragmas(ctx); err != nil {
		closeErr := db.Close()
		return nil, joinErrors(fmt.Errorf("open store: verify pragmas after migration: %w", err), closeErr)
	}
	orm, err := newGORMAdapter(ctx, db)
	if err != nil {
		closeErr := db.Close()
		return nil, joinErrors(fmt.Errorf("open store: %w", err), closeErr)
	}
	store.orm = orm
	return store, nil
}

func openDatabase(ctx context.Context, databasePath string) (*sql.DB, error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, fmt.Errorf("open sqlite: database path is required")
	}
	if strings.IndexByte(databasePath, 0) >= 0 {
		return nil, fmt.Errorf("open sqlite: database path contains NUL")
	}

	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: resolve database path: %w", err)
	}

	registerConnectionHook.Do(func() {
		sqlite.RegisterConnectionHook(validateDriverConnection)
	})

	uriPath := filepath.ToSlash(absolutePath)
	if filepath.VolumeName(absolutePath) != "" {
		uriPath = "/" + uriPath
	}
	dsnURL := &url.URL{Scheme: "file", Path: uriPath}
	query := dsnURL.Query()
	query.Set("mode", "rwc")
	query.Set("_busy_timeout", fmt.Sprintf("%d", busyTimeoutMilliseconds))
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "FULL")
	query.Add("_pragma", fmt.Sprintf("wal_autocheckpoint(%d)", walAutoCheckpointPages))
	dsnURL.RawQuery = query.Encode()

	connector, err := sqlite.NewConnector(dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: create connector: %w", err)
	}
	db := sql.OpenDB(connector)
	if err := db.PingContext(ctx); err != nil {
		closeErr := db.Close()
		return nil, joinErrors(fmt.Errorf("open sqlite: ping: %w", err), closeErr)
	}
	return db, nil
}

// Close closes the SQLite connection pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil || s.orm == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

// EnsureNodeIdentity creates the singleton node identity or verifies that the
// persisted identity equals nodeID.
func (s *Store) EnsureNodeIdentity(ctx context.Context, nodeID core.NodeID, createdAt time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("ensure node identity: store is closed")
	}
	if ctx == nil {
		return fmt.Errorf("ensure node identity: context is required")
	}
	if nodeID == "" {
		return fmt.Errorf("ensure node identity: node id is required")
	}
	if !isLowerHex128(string(nodeID)) {
		return fmt.Errorf("ensure node identity: node id must be 128-bit lowercase hex")
	}
	if createdAt.IsZero() {
		return fmt.Errorf("ensure node identity: created time is required")
	}

	result := s.orm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: NodeIdentityColumns.Singleton}},
		DoNothing: true,
	}).Create(&nodeIdentityRow{
		Singleton: 1, NodeID: string(nodeID), CreatedAtUS: createdAt.UTC().UnixMicro(),
	})
	if result.Error != nil {
		return fmt.Errorf("ensure node identity: insert: %w", result.Error)
	}

	var row nodeIdentityRow
	result = s.orm.WithContext(ctx).Where(&nodeIdentityRow{Singleton: 1}).Take(&row)
	if result.Error != nil {
		return fmt.Errorf("ensure node identity: read back: %w", result.Error)
	}
	if row.NodeID != string(nodeID) {
		return fmt.Errorf("ensure node identity: persisted node %q differs from %q", row.NodeID, nodeID)
	}
	return nil
}

// LoadNodeIdentity returns the singleton persistent NodeID when bootstrap has
// already created it. A missing row is normal only before first bootstrap.
func (s *Store) LoadNodeIdentity(ctx context.Context) (core.NodeID, bool, error) {
	if s == nil || s.db == nil || s.orm == nil {
		return "", false, fmt.Errorf("load node identity: store is closed")
	}
	if ctx == nil {
		return "", false, fmt.Errorf("load node identity: context is required")
	}

	var row nodeIdentityRow
	result := s.orm.WithContext(ctx).Where(&nodeIdentityRow{Singleton: 1}).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if result.Error != nil {
		return "", false, fmt.Errorf("load node identity: %w", result.Error)
	}
	if !isLowerHex128(row.NodeID) {
		return "", false, fmt.Errorf("load node identity: persisted node id is invalid")
	}
	return core.NodeID(row.NodeID), true, nil
}

// CreateNodeIdentity atomically claims the singleton persistent NodeID and
// returns the value that owns the database. Concurrent first bootstraps
// converge on the same stored identity.
func (s *Store) CreateNodeIdentity(
	ctx context.Context,
	nodeID core.NodeID,
	createdAt time.Time,
) (core.NodeID, error) {
	if s == nil || s.db == nil || s.orm == nil {
		return "", fmt.Errorf("create node identity: store is closed")
	}
	if ctx == nil {
		return "", fmt.Errorf("create node identity: context is required")
	}
	if nodeID == "" {
		return "", fmt.Errorf("create node identity: node id is required")
	}
	if !isLowerHex128(string(nodeID)) {
		return "", fmt.Errorf("create node identity: node id must be 128-bit lowercase hex")
	}
	if createdAt.IsZero() {
		return "", fmt.Errorf("create node identity: created time is required")
	}

	result := s.orm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: NodeIdentityColumns.Singleton}},
		DoNothing: true,
	}).Create(&nodeIdentityRow{
		Singleton: 1, NodeID: string(nodeID), CreatedAtUS: createdAt.UTC().UnixMicro(),
	})
	if result.Error != nil {
		return "", fmt.Errorf("create node identity: insert: %w", result.Error)
	}

	persisted, found, err := s.LoadNodeIdentity(ctx)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("create node identity: missing after insert")
	}
	return persisted, nil
}

func joinErrors(primary, secondary error) error {
	if secondary == nil {
		return primary
	}
	return errors.Join(primary, fmt.Errorf("cleanup: %w", secondary))
}

func isLowerHex128(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
