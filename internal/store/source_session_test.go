package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

func beginTestSourceSession(t *testing.T, database *Store, sourceID core.SourceID) SourceSession {
	t.Helper()
	ctx := context.Background()
	active, _, _, err := database.LoadSourceSessionState(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewSourceSessionID()
	if err != nil {
		t.Fatal(err)
	}
	session, _, _, err := database.BeginSourceSession(ctx, sourceID, active, id)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSourceSessionHandoffScopesSequenceAndRejectsStaleWrites(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	base := prepareFileSource(t, database, testSourceID, testGeneration, 200)
	a := beginTestSourceSession(t, database, testSourceID)
	if a.SourceID() != testSourceID || !isLowerHex128(string(a.ID())) {
		t.Fatalf("handle=%+v", a)
	}
	if _, found, err := database.LoadSourceCheckpoint(ctx, testSourceID); err != nil || found {
		t.Fatalf("empty checkpoint found=%v err=%v", found, err)
	}
	oldPosition := filePosition(t, testGeneration, 90, 100)
	if err := database.AdvanceSourceCheckpoint(ctx, a, 100, oldPosition, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	baseline, _, err := database.LoadSourceCheckpoint(ctx, testSourceID)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := NewSourceSessionID()
	if err != nil {
		t.Fatal(err)
	}
	b, recovery, found, err := database.BeginSourceSession(ctx, testSourceID, a.ID(), newID)
	if err != nil || !found || recovery != baseline {
		t.Fatalf("Begin recovery=%+v want=%+v found=%v err=%v", recovery, baseline, found, err)
	}
	if _, _, _, err := database.BeginSourceSession(ctx, testSourceID, a.ID(), newID); !errors.Is(err, ErrSourceSessionConflict) {
		t.Fatalf("repeated Begin=%v", err)
	}
	if _, _, _, err := database.BeginSourceSession(ctx, testSourceID, newID, newID); err == nil {
		t.Fatal("Begin reused current identity")
	}
	confirmed, got, found, err := database.ConfirmSourceSession(ctx, testSourceID, newID)
	if err != nil || !found || confirmed != b || got != baseline {
		t.Fatalf("Confirm=%+v/%+v/%v/%v", confirmed, got, found, err)
	}
	before := sourceSessionRows(t, database)
	if err := database.AdvanceSourceCheckpoint(ctx, a, 101, filePosition(t, testGeneration, 100, 110), base.Add(2*time.Second)); !errors.Is(err, ErrStaleSourceSession) {
		t.Fatalf("stale=%v", err)
	}
	if after := sourceSessionRows(t, database); !reflect.DeepEqual(before, after) {
		t.Fatalf("stale write changed rows: %v -> %v", before, after)
	}
	position := filePosition(t, testGeneration, 100, 110)
	stamp := base.Add(3 * time.Second)
	if err := database.AdvanceSourceCheckpoint(ctx, b, 1, position, stamp); err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, b, 1, position, stamp.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := database.LoadSourceCheckpoint(ctx, testSourceID)
	if err != nil || !found || checkpoint.SessionID != b.ID() || checkpoint.DeliverySequence != 1 || checkpoint.Position != position || checkpoint.PersistedAt != stamp {
		t.Fatalf("new checkpoint=%+v err=%v", checkpoint, err)
	}
	before = sourceSessionRows(t, database)
	if err := database.AdvanceSourceCheckpoint(ctx, b, 1, oldPosition, stamp); !errors.Is(err, ErrCheckpointRegression) {
		t.Fatalf("same-sequence conflicting position=%v", err)
	}
	if !reflect.DeepEqual(before, sourceSessionRows(t, database)) {
		t.Fatal("conflict changed persisted rows")
	}
	if _, _, _, err := database.ConfirmSourceSession(ctx, testSourceID, a.ID()); !errors.Is(err, ErrSourceSessionConflict) {
		t.Fatalf("replaced Confirm=%v", err)
	}
	// 大数字与 session 变化都不能替代代际完成资格。
	if err := database.SealFileGeneration(ctx, testSourceID, testGeneration, FileGenerationOpen, 200, 1, stamp); err != nil {
		t.Fatal(err)
	}
	if err := database.AdvanceSourceCheckpoint(ctx, b, 1000, position, stamp); err != nil {
		t.Fatal(err)
	}
	if err := database.RetireFileGeneration(ctx, testSourceID, testGeneration, stamp.Add(time.Second)); !errors.Is(err, ErrFileGenerationNotDurable) {
		t.Fatalf("retire=%v", err)
	}
}

func TestSourceSessionBeginRollbackAndConfirmationFailure(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	prepareFileSource(t, database, testSourceID, testGeneration, 100)
	a := beginTestSourceSession(t, database, testSourceID)
	id, err := NewSourceSessionID()
	if err != nil {
		t.Fatal(err)
	}
	before := sourceSessionRows(t, database)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := database.BeginSourceSession(cancelled, testSourceID, a.ID(), id); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Begin=%v", err)
	}
	if !reflect.DeepEqual(before, sourceSessionRows(t, database)) {
		t.Fatal("cancelled Begin changed state")
	}
	// GORM 查询回调在 CAS 之后注入恢复读回错误，验证实际事务回滚。
	injected := errors.New("injected recovery read failure")
	const callback = "guard_wall:test_session_checkpoint_read_failure"
	if err := database.orm.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "source_checkpoints" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, beginErr := database.BeginSourceSession(ctx, testSourceID, a.ID(), id)
	if err := database.orm.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(beginErr, injected) || !reflect.DeepEqual(before, sourceSessionRows(t, database)) {
		t.Fatalf("rollback error=%v", beginErr)
	}
	if _, _, _, err := database.ConfirmSourceSession(ctx, testSourceID, id); !errors.Is(err, ErrSourceSessionConflict) {
		t.Fatalf("uncommitted Confirm=%v", err)
	}
	// 模拟确认报文丢失：丢弃已成功 Begin 的 handle，再以同一意图读回。
	if _, _, _, err := database.BeginSourceSession(ctx, testSourceID, a.ID(), id); err != nil {
		t.Fatal(err)
	}
	if handle, _, found, err := database.ConfirmSourceSession(ctx, testSourceID, id); err != nil || found || handle.ID() != id {
		t.Fatalf("lost confirmation=%+v/%v/%v", handle, found, err)
	}
	if handle, _, _, err := database.ConfirmSourceSession(cancelled, testSourceID, id); !errors.Is(err, context.Canceled) || handle != (SourceSession{}) {
		t.Fatalf("failed readback handle=%+v err=%v", handle, err)
	}
}

func TestSourceSessionConcurrentBeginAndCommit(t *testing.T) {
	for _, operation := range []string{"begin", "checkpoint"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			database := openTestStore(t)
			base := prepareFileSource(t, database, testSourceID, testGeneration, 200)
			a := beginTestSourceSession(t, database, testSourceID)
			if err := database.AdvanceSourceCheckpoint(ctx, a, 100, filePosition(t, testGeneration, 90, 100), base); err != nil {
				t.Fatal(err)
			}
			other := secondSourceSessionStore(t, database)
			bID, err := NewSourceSessionID()
			if err != nil {
				t.Fatal(err)
			}
			cID, err := NewSourceSessionID()
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				handle     SourceSession
				checkpoint SourceCheckpoint
				err        error
			}
			start := make(chan struct{})
			first, second := make(chan result, 1), make(chan result, 1)
			go func() {
				<-start
				h, cp, _, err := database.BeginSourceSession(ctx, testSourceID, a.ID(), bID)
				first <- result{h, cp, err}
			}()
			go func() {
				<-start
				if operation == "begin" {
					h, cp, _, err := other.BeginSourceSession(ctx, testSourceID, a.ID(), cID)
					second <- result{h, cp, err}
					return
				}
				second <- result{err: other.AdvanceSourceCheckpoint(ctx, a, 101, filePosition(t, testGeneration, 100, 110), base.Add(time.Second))}
			}()
			close(start)
			x, y := <-first, <-second
			active, checkpoint, found, err := database.LoadSourceSessionState(ctx, testSourceID)
			if err != nil || !found {
				t.Fatalf("readback=%v/%v", found, err)
			}
			if operation == "begin" {
				if (x.err == nil) == (y.err == nil) {
					t.Fatalf("exactly one Begin must win: %v/%v", x.err, y.err)
				}
				winner, loser := x, y
				if y.err == nil {
					winner, loser = y, x
				}
				if !errors.Is(loser.err, ErrSourceSessionConflict) || active != winner.handle.ID() || checkpoint != winner.checkpoint {
					t.Fatalf("Begin race=%+v/%+v active=%s checkpoint=%+v", x, y, active, checkpoint)
				}
			} else {
				if x.err != nil || (y.err != nil && !errors.Is(y.err, ErrStaleSourceSession)) || active != bID {
					t.Fatalf("Begin/Commit=%v/%v active=%s", x.err, y.err, active)
				}
				want := core.DeliverySequence(100)
				if y.err == nil {
					want = 101
				}
				if checkpoint != x.checkpoint || checkpoint.DeliverySequence != want || checkpoint.SessionID != a.ID() {
					t.Fatalf("non-serial checkpoint=%+v Begin=%+v", checkpoint, x.checkpoint)
				}
			}
		})
	}
}

func TestSourceSessionReadbackUsesOneSnapshot(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	base := prepareFileSource(t, database, testSourceID, testGeneration, 200)
	a := beginTestSourceSession(t, database, testSourceID)
	if err := database.AdvanceSourceCheckpoint(ctx, a, 100, filePosition(t, testGeneration, 90, 100), base); err != nil {
		t.Fatal(err)
	}
	baseline, _, err := database.LoadSourceCheckpoint(ctx, testSourceID)
	if err != nil {
		t.Fatal(err)
	}
	other := secondSourceSessionStore(t, database)
	bID, err := NewSourceSessionID()
	if err != nil {
		t.Fatal(err)
	}
	const callback = "guard_wall:test_session_snapshot_switch"
	fired := false
	var switchErr error
	if err := database.orm.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "sources" || fired {
			return
		}
		fired = true
		b, _, _, err := other.BeginSourceSession(ctx, testSourceID, a.ID(), bID)
		if err == nil {
			err = other.AdvanceSourceCheckpoint(ctx, b, 1, filePosition(t, testGeneration, 100, 110), base.Add(time.Second))
		}
		switchErr = err
	}); err != nil {
		t.Fatal(err)
	}
	handle, checkpoint, found, readErr := database.ConfirmSourceSession(ctx, testSourceID, a.ID())
	if err := database.orm.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if !fired || switchErr != nil || readErr != nil || !found || handle != a || checkpoint != baseline {
		t.Fatalf("snapshot=%+v/%+v fired=%v switch=%v read=%v", handle, checkpoint, fired, switchErr, readErr)
	}
	if _, _, _, err := database.ConfirmSourceSession(ctx, testSourceID, a.ID()); !errors.Is(err, ErrSourceSessionConflict) {
		t.Fatalf("fresh confirmation=%v", err)
	}
}

func secondSourceSessionStore(t *testing.T, database *Store) *Store {
	t.Helper()
	var sequence int
	var name, path string
	if err := database.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	other, err := Open(context.Background(), path, migrationFileSystem())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
	})
	return other
}

func sourceSessionRows(t *testing.T, database *Store) map[string][]string {
	t.Helper()
	result := make(map[string][]string)
	for _, table := range []string{"sources", "source_checkpoints", "processing_receipts", "parser_terminal_outcomes", "detection_terminal_outcomes", "detection_contributions", "audit_logs", "alerts", "decisions", "desired_ban_projections", "source_file_generations"} {
		rows, err := database.db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(errors.Join(err, rows.Close()))
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
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
