package store

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"gorm.io/gorm"
)

func TestGORMDecisionLifecycleUsesTransactionSession(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)

	var createTables, queryTables, updateTables []string
	err := database.RunDecisionTransaction(ctx, func(tx decision.LifecycleTransaction) error {
		uow, ok := tx.(*UnitOfWork)
		if !ok {
			t.Fatalf("LifecycleTransaction = %T, want *UnitOfWork", tx)
		}
		const suffix = "decision_lifecycle_session"
		createName := "guard_wall:test_" + suffix + "_create"
		if err := uow.transactionORM.Callback().Create().After("gorm:create").Register(
			createName, func(tx *gorm.DB) { createTables = append(createTables, tx.Statement.Table) },
		); err != nil {
			t.Fatalf("register GORM Create callback: %v", err)
		}
		t.Cleanup(func() { _ = uow.transactionORM.Callback().Create().Remove(createName) })
		queryName := "guard_wall:test_" + suffix + "_query"
		if err := uow.transactionORM.Callback().Query().After("gorm:query").Register(
			queryName, func(tx *gorm.DB) { queryTables = append(queryTables, tx.Statement.Table) },
		); err != nil {
			t.Fatalf("register GORM Query callback: %v", err)
		}
		t.Cleanup(func() { _ = uow.transactionORM.Callback().Query().Remove(queryName) })
		updateName := "guard_wall:test_" + suffix + "_update"
		if err := uow.transactionORM.Callback().Update().After("gorm:update").Register(
			updateName, func(tx *gorm.DB) { updateTables = append(updateTables, tx.Statement.Table) },
		); err != nil {
			t.Fatalf("register GORM Update callback: %v", err)
		}
		t.Cleanup(func() { _ = uow.transactionORM.Callback().Update().Remove(updateName) })

		if err := uow.RequireNodeIdentity(ctx, testNodeID); err != nil {
			return err
		}
		candidate := core.Decision{
			ID: "gorm-decision-lifecycle", NodeID: testNodeID, Source: core.DecisionSourceManual,
			CanonicalTarget: netip.MustParsePrefix("198.51.100.81/32"),
			CreatedAt:       time.Unix(10_000, 0).UTC(), UpdatedAt: time.Unix(10_000, 0).UTC(),
			LastTriggeredAt: time.Unix(10_000, 0).UTC(), State: core.DecisionActive,
		}
		inserted, err := uow.InsertManualDecision(ctx, candidate)
		if err != nil || !inserted {
			t.Fatalf("InsertManualDecision() = %v, %v", inserted, err)
		}
		found, exists, err := uow.FindActiveManualDecision(ctx, testNodeID, candidate.CanonicalTarget)
		if err != nil || !exists || found.ID != candidate.ID {
			t.Fatalf("FindActiveManualDecision() = %+v, %v, %v", found, exists, err)
		}
		revoked, err := uow.RevokeActiveManualDecision(ctx, candidate.ID, candidate.CreatedAt.Add(time.Minute))
		if err != nil || revoked.State != core.DecisionRevoked {
			t.Fatalf("RevokeActiveManualDecision() = %+v, %v", revoked, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunDecisionTransaction(): %v", err)
	}
	if !containsGORMDecisionLifecycleTable(createTables, "decisions") {
		t.Fatalf("GORM Create tables = %v, want decisions", createTables)
	}
	if !containsGORMDecisionLifecycleTable(queryTables, "decisions") {
		t.Fatalf("GORM Query tables = %v, want decisions", queryTables)
	}
	if !containsGORMDecisionLifecycleTable(updateTables, "node_identity") ||
		!containsGORMDecisionLifecycleTable(updateTables, "decisions") {
		t.Fatalf("GORM Update tables = %v, want node_identity and decisions", updateTables)
	}
}

func containsGORMDecisionLifecycleTable(tables []string, want string) bool {
	for _, table := range tables {
		if table == want {
			return true
		}
	}
	return false
}
