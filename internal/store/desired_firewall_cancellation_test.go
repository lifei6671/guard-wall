package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestSQLiteLoadDesiredFirewallStateCommitCancellation(t *testing.T) {
	queryFailure := errors.New("injected snapshot query failure")
	for _, mode := range []string{"automatic rollback", "deadline rollback", "active context rollback", "query failure during cancellation"} {
		t.Run(mode, func(t *testing.T) {
			database := openTestStore(t)
			if err := database.EnsureNodeIdentity(context.Background(), testNodeID, time.Unix(100, 0)); err != nil {
				t.Fatal(err)
			}
			seedDesiredFirewallPolicyRows(t, database, []desiredFirewallPolicyRow{
				{table: "protected_targets", target: "127.0.0.0/8", enabled: 1, revision: 7},
				{table: "protected_targets", target: "::1/128", enabled: 1, revision: 7},
			})
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline rollback" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			}
			defer cancel()
			reached := false
			const callbackName = "test:snapshot_commit_cancellation"
			if err := database.orm.Callback().Query().After("gorm:query").Register(callbackName, func(query *gorm.DB) {
				if _, ok := query.Statement.Dest.(*[]targetEnforcementStateRow); !ok || query.Error != nil {
					return
				}
				reached = true
				pool, ok := query.Statement.ConnPool.(*nonFinalizingTxConnPool)
				if !ok {
					t.Fatalf("unexpected transaction pool %T", query.Statement.ConnPool)
				}
				if mode == "active context rollback" {
					if err := pool.tx.Rollback(); err != nil {
						t.Fatal(err)
					}
					return
				}
				if mode == "deadline rollback" {
					<-ctx.Done()
				} else {
					cancel()
				}
				// 等待连接归还，确保自动回滚已完成，而不是依赖调度时机。
				deadline := time.Now().Add(5 * time.Second)
				for database.db.Stats().InUse != 0 {
					if time.Now().After(deadline) {
						t.Fatal("automatic rollback did not release the connection")
					}
					time.Sleep(time.Millisecond)
				}
				if err := pool.tx.Commit(); !errors.Is(err, sql.ErrTxDone) {
					t.Fatalf("commit after automatic rollback = %v, want ErrTxDone", err)
				}
				if mode == "query failure during cancellation" {
					query.AddError(queryFailure)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := database.orm.Callback().Query().Remove(callbackName); err != nil {
					t.Error(err)
				}
			})
			state, err := database.LoadDesiredFirewallState(ctx, testNodeID)
			if !reached {
				t.Fatal("final snapshot query was not reached")
			}
			if !reflect.DeepEqual(state, DesiredFirewallState{}) {
				t.Fatalf("failed snapshot returned usable state: %+v", state)
			}
			if mode == "query failure during cancellation" {
				if !errors.Is(err, queryFailure) || errors.Is(err, context.Canceled) {
					t.Fatalf("query error = %v, want original failure without cancellation identity", err)
				}
				return
			}
			if !errors.Is(err, sql.ErrTxDone) || !strings.Contains(err.Error(), "commit read transaction") {
				t.Fatalf("error = %v, want commit ErrTxDone", err)
			}
			if got, want := errors.Is(err, context.Canceled), mode == "automatic rollback"; got != want {
				t.Fatalf("cancellation identity = %v, want %v; error: %v", got, want, err)
			}
			if got, want := errors.Is(err, context.DeadlineExceeded), mode == "deadline rollback"; got != want {
				t.Fatalf("deadline identity = %v, want %v; error: %v", got, want, err)
			}
		})
	}
}
