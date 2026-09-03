package store

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"gorm.io/gorm"
)

func TestGORMDesiredTargetIntentUsesTransactionSession(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.EnsureNodeIdentity(ctx, testNodeID, time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	uow, err := database.BeginProcessing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cleanupOpenUnitOfWork(t, uow)

	var createTables, queryTables, updateTables []string
	const createName = "guard_wall:test_desired_state_create"
	if err := uow.transactionORM.Callback().Create().After("gorm:create").Register(createName, func(tx *gorm.DB) {
		createTables = append(createTables, tx.Statement.Table)
	}); err != nil {
		t.Fatalf("register GORM Create callback: %v", err)
	}
	t.Cleanup(func() { _ = uow.transactionORM.Callback().Create().Remove(createName) })
	const queryName = "guard_wall:test_desired_state_query"
	if err := uow.transactionORM.Callback().Query().After("gorm:query").Register(queryName, func(tx *gorm.DB) {
		queryTables = append(queryTables, tx.Statement.Table)
	}); err != nil {
		t.Fatalf("register GORM Query callback: %v", err)
	}
	t.Cleanup(func() { _ = uow.transactionORM.Callback().Query().Remove(queryName) })
	const updateName = "guard_wall:test_desired_state_update"
	if err := uow.transactionORM.Callback().Update().After("gorm:update").Register(updateName, func(tx *gorm.DB) {
		updateTables = append(updateTables, tx.Statement.Table)
	}); err != nil {
		t.Fatalf("register GORM Update callback: %v", err)
	}
	t.Cleanup(func() { _ = uow.transactionORM.Callback().Update().Remove(updateName) })

	intent := core.NormalizedTargetEnforcementIntent{
		NodeID: testNodeID, CanonicalTarget: netip.MustParsePrefix("192.0.2.82/32"),
		BanMembership: core.BanPresent, TimeoutMode: core.TimeoutNone,
		Scopes: core.ScopeInput, AddressFamily: core.AddressFamilyIPv4,
		PolicyCoverage: core.PolicyCoverageNone, BackendAttributesDigest: strings.Repeat("a", 64),
		Generation: 1,
	}
	if err := uow.PutTargetEnforcementIntent(ctx, intent); err != nil {
		t.Fatalf("PutTargetEnforcementIntent(): %v", err)
	}
	if _, found, err := uow.FindTargetEnforcementIntent(ctx, testNodeID, intent.CanonicalTarget); err != nil || !found {
		t.Fatalf("FindTargetEnforcementIntent() found=%v, err=%v", found, err)
	}
	if revision, err := uow.AdvanceSnapshotRevision(ctx); err != nil || revision != 1 {
		t.Fatalf("AdvanceSnapshotRevision() = %d, %v", revision, err)
	}
	if !containsGORMDesiredTable(createTables, "enforcement_states") ||
		!containsGORMDesiredTable(queryTables, "enforcement_states") ||
		!containsGORMDesiredTable(updateTables, "desired_firewall_state") {
		t.Fatalf("GORM tables create=%v query=%v update=%v", createTables, queryTables, updateTables)
	}
}

func containsGORMDesiredTable(tables []string, want string) bool {
	for _, table := range tables {
		if table == want {
			return true
		}
	}
	return false
}
