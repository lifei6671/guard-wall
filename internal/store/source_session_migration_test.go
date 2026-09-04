package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestSourceSessionMigrationPreservesLegacyRows(t *testing.T) {
	ctx := context.Background()
	legacyFS := fstest.MapFS{}
	for _, name := range []string{"0001_m0.sql", "0002_detection_terminal_outcomes.sql", "0003_reconcile_restart_recovery.sql", "0004_desired_firewall_authority.sql", "0005_observed_firewall_state.sql", "0006_managed_snapshot_target_evidence.sql"} {
		content, err := fs.ReadFile(migrationFileSystem(), name)
		if err != nil {
			t.Fatal(err)
		}
		legacyFS[name] = &fstest.MapFile{Data: content}
	}
	path := filepath.Join(t.TempDir(), "legacy.db")
	database, err := Open(ctx, path, legacyFS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	fixture := processingFixtureValues(t)
	now := fixture.now.UnixMicro()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, fixture.now); err != nil {
		t.Fatal(err)
	}
	// 使用升级前六版的 SQL fixture；新模型不能向旧表插入 session 列。
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sources(source_id,node_id,kind,created_at_us,updated_at_us) VALUES (?,?,'file',?,?)`, []any{processingSourceID, testNodeID, now, now}},
		{`INSERT INTO source_file_generations(source_id,generation,device_id,inode,path,state,observed_size,opened_at_us) VALUES (?,?,1,2,'/var/log/processing.log','open',10,?)`, []any{processingSourceID, processingGeneration, now}},
		{`INSERT INTO parsers(parser_id,enabled,created_at_us,updated_at_us) VALUES (?,1,?,?)`, []any{processingParserID, now, now}},
		{`INSERT INTO parser_versions(parser_id,version,definition,definition_sha256,created_at_us) VALUES (?,?,'{}',?,?)`, []any{processingParserID, processingParserVersion, strings.Repeat("1", 64), now}},
		{`UPDATE parsers SET active_version=? WHERE parser_id=?`, []any{processingParserVersion, processingParserID}},
		{`INSERT INTO rules(rule_id,enabled,created_at_us,updated_at_us) VALUES ('rule-1',1,?,?)`, []any{now, now}},
		{`INSERT INTO rule_versions(rule_id,version,definition,definition_sha256,created_at_us) VALUES ('rule-1','v1','{}',?,?)`, []any{strings.Repeat("2", 64), now}},
		{`UPDATE rules SET active_version='v1' WHERE rule_id='rule-1'`, nil},
		{`INSERT INTO source_checkpoints(source_id,delivery_sequence,position_kind,generation,device_id,inode,start_offset,end_offset,persisted_at_us) VALUES (?,100,'file',?,1,2,0,10,?)`, []any{processingSourceID, processingGeneration, now}},
	} {
		if _, err := database.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	unit, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writeCompleteProcessingOutcome(t, unit, fixture)
	if err := unit.Commit(); err != nil {
		t.Fatal(err)
	}
	// 历史已退休行只保留为恢复锚点；升级不能把它复活或补发资格。
	if _, err := database.db.ExecContext(ctx, `UPDATE source_file_generations SET state='retired', final_eof=10, max_delivery_sequence=100, sealed_at_us=?, retired_at_us=? WHERE source_id=?`, now, now, processingSourceID); err != nil {
		t.Fatal(err)
	}
	columns := make(map[string][]string)
	before := sourceSessionMigrationRows(t, database.db, columns)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path, migrationFileSystem())
	if err != nil {
		t.Fatal(err)
	}
	if after := sourceSessionMigrationRows(t, database.db, columns); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed legacy columns: before=%v after=%v", before, after)
	}
	active, checkpoint, found, err := database.LoadSourceSessionState(ctx, processingSourceID)
	if err != nil || !found || active != "" || checkpoint.SessionID != "" || checkpoint.DeliverySequence != 100 || checkpoint.Position != fixture.position || checkpoint.PersistedAt != fixture.now {
		t.Fatalf("legacy session state=%s/%+v/%v/%v", active, checkpoint, found, err)
	}
	id, err := NewSourceSessionID()
	if err != nil {
		t.Fatal(err)
	}
	session, recovery, found, err := database.BeginSourceSession(ctx, processingSourceID, "", id)
	if err != nil || !found || recovery != checkpoint {
		t.Fatalf("legacy Begin=%+v/%v/%v", recovery, found, err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, session, 1, fixture.position, fixture.now.Add(time.Second)); !errors.Is(err, ErrSourcePositionMismatch) {
		t.Fatalf("retired anchor write=%v", err)
	}
	if current, _, err := database.LoadSourceCheckpoint(ctx, processingSourceID); err != nil || current != checkpoint {
		t.Fatalf("retired anchor changed=%+v/%v", current, err)
	}
	if generations, err := database.LoadRecoverableFileGenerations(ctx, processingSourceID); err != nil || len(generations) != 0 {
		t.Fatalf("historical retired restored=%v/%v", generations, err)
	}
}

func TestSourceSessionMigrationChecksIdentityFormat(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	base := prepareFileSource(t, database, testSourceID, testGeneration, 100)
	session := beginTestSourceSession(t, database, testSourceID)
	if err := database.AdvanceSourceCheckpoint(ctx, session, 1, filePosition(t, testGeneration, 0, 10), base); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "short", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		for _, target := range []struct{ table, column string }{{"sources", "active_session_id"}, {"source_checkpoints", "checkpoint_session_id"}} {
			if _, err := database.db.ExecContext(ctx, "UPDATE "+target.table+" SET "+target.column+"=? WHERE source_id=?", value, testSourceID); err == nil {
				t.Fatalf("%s accepted invalid identity %q", target.column, value)
			}
		}
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE source_checkpoints SET checkpoint_session_id=NULL WHERE source_id=?`, testSourceID); err != nil {
		t.Fatal(err)
	}
	// 迁移前无归属的高水位允许当前 session 从 1 首次接管。
	if _, err := database.db.ExecContext(ctx, `UPDATE source_checkpoints SET delivery_sequence=100 WHERE source_id=?`, testSourceID); err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, session, 1, filePosition(t, testGeneration, 10, 20), base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, testSourceID)
	if err != nil || !found || checkpoint.SessionID != session.ID() || checkpoint.DeliverySequence != 1 {
		t.Fatalf("first session takeover=%+v/%v/%v", checkpoint, found, err)
	}
}

func sourceSessionMigrationRows(t *testing.T, database *sql.DB, columns map[string][]string) map[string][]string {
	t.Helper()
	result := make(map[string][]string)
	for _, table := range []string{"sources", "source_checkpoints", "source_file_generations", "processing_receipts", "parser_terminal_outcomes", "detection_terminal_outcomes", "detection_contributions", "audit_logs", "alerts", "decisions", "desired_ban_projections"} {
		selection := "*"
		if prior, ok := columns[table]; ok {
			selection = strings.Join(prior, ",")
		}
		rows, err := database.Query("SELECT " + selection + " FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		names, err := rows.Columns()
		if err != nil {
			t.Fatal(errors.Join(err, rows.Close()))
		}
		columns[table] = names
		for rows.Next() {
			values := make([]any, len(names))
			pointers := make([]any, len(names))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(errors.Join(err, rows.Close()))
			}
			result[table] = append(result[table], fmt.Sprintf("%#v", values))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	return result
}
