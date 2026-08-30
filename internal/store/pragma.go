package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
)

const connectionHookTimeout = 5 * time.Second

// Pragmas is the read-back of the frozen SQLite connection contract.
type Pragmas struct {
	JournalMode       string
	Synchronous       int64
	ForeignKeys       int64
	BusyTimeoutMS     int64
	WALAutoCheckpoint int64
}

func validateDriverConnection(conn sqlite.ExecQuerierContext, _ string) error {
	ctx, cancel := context.WithTimeout(context.Background(), connectionHookTimeout)
	defer cancel()

	readback, err := readDriverPragmas(ctx, conn)
	if err != nil {
		return fmt.Errorf("sqlite connection PRAGMA read-back: %w", err)
	}
	if err := readback.validate(); err != nil {
		return fmt.Errorf("sqlite connection PRAGMA mismatch: %w", err)
	}
	return nil
}

func readDriverPragmas(ctx context.Context, conn sqlite.ExecQuerierContext) (Pragmas, error) {
	journalMode, err := queryDriverPragma(ctx, conn, "journal_mode")
	if err != nil {
		return Pragmas{}, err
	}
	synchronous, err := queryDriverPragmaInt(ctx, conn, "synchronous")
	if err != nil {
		return Pragmas{}, err
	}
	foreignKeys, err := queryDriverPragmaInt(ctx, conn, "foreign_keys")
	if err != nil {
		return Pragmas{}, err
	}
	busyTimeout, err := queryDriverPragmaInt(ctx, conn, "busy_timeout")
	if err != nil {
		return Pragmas{}, err
	}
	walAutoCheckpoint, err := queryDriverPragmaInt(ctx, conn, "wal_autocheckpoint")
	if err != nil {
		return Pragmas{}, err
	}
	return Pragmas{
		JournalMode:       strings.ToLower(journalMode),
		Synchronous:       synchronous,
		ForeignKeys:       foreignKeys,
		BusyTimeoutMS:     busyTimeout,
		WALAutoCheckpoint: walAutoCheckpoint,
	}, nil
}

func queryDriverPragmaInt(ctx context.Context, conn sqlite.ExecQuerierContext, name string) (int64, error) {
	value, err := queryDriverPragma(ctx, conn, name)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("PRAGMA %s returned %q: %w", name, value, err)
	}
	return parsed, nil
}

func queryDriverPragma(ctx context.Context, conn sqlite.ExecQuerierContext, name string) (string, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA "+name, nil)
	if err != nil {
		return "", fmt.Errorf("query PRAGMA %s: %w", name, err)
	}
	columns := rows.Columns()
	if len(columns) != 1 {
		closeErr := rows.Close()
		return "", joinErrors(fmt.Errorf("PRAGMA %s returned %d columns", name, len(columns)), closeErr)
	}
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		closeErr := rows.Close()
		return "", joinErrors(fmt.Errorf("read PRAGMA %s: %w", name, err), closeErr)
	}
	if err := rows.Next(make([]driver.Value, 1)); err != io.EOF {
		closeErr := rows.Close()
		if err == nil {
			err = fmt.Errorf("returned more than one row")
		}
		return "", joinErrors(fmt.Errorf("read PRAGMA %s: %w", name, err), closeErr)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("close PRAGMA %s rows: %w", name, err)
	}

	switch value := values[0].(type) {
	case int64:
		return strconv.FormatInt(value, 10), nil
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		return "", fmt.Errorf("PRAGMA %s returned unsupported type %T", name, values[0])
	}
}

// Pragmas reads and validates the five frozen settings on one checked-out
// physical connection.
func (s *Store) Pragmas(ctx context.Context) (Pragmas, error) {
	if s == nil || s.db == nil {
		return Pragmas{}, fmt.Errorf("read pragmas: store is closed")
	}
	if ctx == nil {
		return Pragmas{}, fmt.Errorf("read pragmas: context is required")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Pragmas{}, fmt.Errorf("read pragmas: acquire connection: %w", err)
	}

	readback, err := readSQLPragmas(ctx, conn)
	if err != nil {
		return Pragmas{}, joinErrors(err, conn.Close())
	}
	if err := conn.Close(); err != nil {
		return Pragmas{}, fmt.Errorf("read pragmas: release connection: %w", err)
	}
	if err := readback.validate(); err != nil {
		return Pragmas{}, err
	}
	return readback, nil
}

func readSQLPragmas(ctx context.Context, conn *sql.Conn) (Pragmas, error) {
	var readback Pragmas
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&readback.JournalMode); err != nil {
		return Pragmas{}, fmt.Errorf("read PRAGMA journal_mode: %w", err)
	}
	readback.JournalMode = strings.ToLower(readback.JournalMode)
	if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&readback.Synchronous); err != nil {
		return Pragmas{}, fmt.Errorf("read PRAGMA synchronous: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&readback.ForeignKeys); err != nil {
		return Pragmas{}, fmt.Errorf("read PRAGMA foreign_keys: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&readback.BusyTimeoutMS); err != nil {
		return Pragmas{}, fmt.Errorf("read PRAGMA busy_timeout: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&readback.WALAutoCheckpoint); err != nil {
		return Pragmas{}, fmt.Errorf("read PRAGMA wal_autocheckpoint: %w", err)
	}
	return readback, nil
}

func (p Pragmas) validate() error {
	if strings.ToLower(p.JournalMode) != "wal" {
		return fmt.Errorf("journal_mode = %q, want wal", p.JournalMode)
	}
	if p.Synchronous != 2 {
		return fmt.Errorf("synchronous = %d, want 2", p.Synchronous)
	}
	if p.ForeignKeys != 1 {
		return fmt.Errorf("foreign_keys = %d, want 1", p.ForeignKeys)
	}
	if p.BusyTimeoutMS != busyTimeoutMilliseconds {
		return fmt.Errorf("busy_timeout = %d, want %d", p.BusyTimeoutMS, busyTimeoutMilliseconds)
	}
	if p.WALAutoCheckpoint != walAutoCheckpointPages {
		return fmt.Errorf("wal_autocheckpoint = %d, want %d", p.WALAutoCheckpoint, walAutoCheckpointPages)
	}
	return nil
}
