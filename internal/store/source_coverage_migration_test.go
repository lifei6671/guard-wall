package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestSourceCoverageMigrationPreservesV7RowsAndUnknownPrefix(t *testing.T) {
	ctx := context.Background()
	legacyFS := fstest.MapFS{}
	entries, err := fs.ReadDir(migrationFileSystem(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "0008" {
			continue
		}
		content, err := fs.ReadFile(migrationFileSystem(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		legacyFS[entry.Name()] = &fstest.MapFile{Data: content}
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
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sources(source_id,node_id,kind,created_at_us,updated_at_us,active_session_id) VALUES (?,?,'file',?,?,?)`, []any{processingSourceID, testNodeID, now, now, strings.Repeat("a", 32)}},
		{`INSERT INTO source_file_generations(source_id,generation,device_id,inode,path,state,observed_size,opened_at_us,final_eof,max_delivery_sequence,sealed_at_us) VALUES (?,?,1,2,'/var/log/processing.log','sealed',10,?,10,100,?)`, []any{processingSourceID, processingGeneration, now, now}},
		{`INSERT INTO source_checkpoints(source_id,delivery_sequence,position_kind,generation,device_id,inode,start_offset,end_offset,persisted_at_us,checkpoint_session_id) VALUES (?,100,'file',?,1,2,0,10,?,?)`, []any{processingSourceID, processingGeneration, now, strings.Repeat("a", 32)}},
	} {
		if _, err := database.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	unit, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := core.FileDeliveryID(processingSourceID, coverageSpan(processingGeneration, 0, 10))
	if err != nil {
		t.Fatal(err)
	}
	if err := unit.PutReceipt(ctx, core.ProcessingReceipt{DeliveryID: id, SourceID: processingSourceID, Position: fixture.position, Kind: core.ReceiptSuccess, Committed: fixture.now}); err != nil {
		t.Fatal(err)
	}
	if err := unit.Commit(); err != nil {
		t.Fatal(err)
	}
	columns := map[string][]string{}
	before := sourceSessionMigrationRows(t, database.db, columns)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path, migrationFileSystem())
	if err != nil {
		t.Fatal(err)
	}
	if after := sourceSessionMigrationRows(t, database.db, columns); !reflect.DeepEqual(before, after) {
		t.Fatalf("v7 rows changed: %v -> %v", before, after)
	}
	active, checkpoint, found, generations, err := database.LoadSourceCoverageState(ctx, processingSourceID)
	if err != nil || !found || active != SourceSessionID(strings.Repeat("a", 32)) || checkpoint.SessionID != active || checkpoint.DeliverySequence != 100 || len(generations) != 1 || generations[0].DurableEndOffset != nil || generations[0].CoverageSessionID != nil || generations[0].CoverageComplete() {
		t.Fatalf("migration state=%s/%+v/%+v/%v", active, checkpoint, generations, err)
	}
}

func TestSourceCoverageMigrationChecksPairedFieldsAndSealedRange(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	prepareFileSource(t, database, testSourceID, testGeneration, 10)
	for _, values := range []struct {
		end     any
		session any
	}{
		{0, nil}, {nil, strings.Repeat("a", 32)}, {-1, strings.Repeat("a", 32)}, {0, ""}, {0, strings.Repeat("A", 32)}, {0, strings.Repeat("g", 32)}, {0, strings.Repeat("a", 31)},
	} {
		if _, err := database.db.ExecContext(ctx, `UPDATE source_file_generations SET durable_end_offset=?,coverage_session_id=? WHERE source_id=?`, values.end, values.session, testSourceID); err == nil {
			t.Fatalf("accepted invalid coverage %+v", values)
		}
	}
	session := beginTestSourceSession(t, database, testSourceID)
	if err := database.InitializeFileGenerationCoverage(ctx, session, testGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE source_file_generations SET durable_end_offset=10 WHERE source_id=?`, testSourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE source_file_generations SET final_eof=9,max_delivery_sequence=1,sealed_at_us=opened_at_us,state='sealed' WHERE source_id=?`, testSourceID); err == nil {
		t.Fatal("seal below proven end accepted")
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE source_file_generations SET final_eof=10,max_delivery_sequence=1,sealed_at_us=opened_at_us,state='sealed' WHERE source_id=?`, testSourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE source_file_generations SET durable_end_offset=11 WHERE source_id=?`, testSourceID); err == nil {
		t.Fatal("coverage beyond seal accepted")
	}
}
