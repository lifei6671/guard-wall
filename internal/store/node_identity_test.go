package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestLoadNodeIdentityDistinguishesMissingAndPersisted(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if nodeID, found, err := store.LoadNodeIdentity(ctx); err != nil || found || nodeID != "" {
		t.Fatalf("LoadNodeIdentity() = (%q, %t, %v), want empty missing identity", nodeID, found, err)
	}
	want := core.NodeID("00112233445566778899aabbccddeeff")
	if err := store.EnsureNodeIdentity(ctx, want, time.Unix(1_700_000_000, 0).UTC()); err != nil {
		t.Fatalf("EnsureNodeIdentity(): %v", err)
	}
	if nodeID, found, err := store.LoadNodeIdentity(ctx); err != nil || !found || nodeID != want {
		t.Fatalf("LoadNodeIdentity() = (%q, %t, %v), want (%q, true, nil)", nodeID, found, err, want)
	}
}

func TestCreateNodeIdentityReturnsPersistentWinner(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first := core.NodeID("00112233445566778899aabbccddeeff")
	second := core.NodeID("ffeeddccbbaa99887766554433221100")
	now := time.Unix(1_700_000_000, 0).UTC()

	if got, err := store.CreateNodeIdentity(ctx, first, now); err != nil || got != first {
		t.Fatalf("CreateNodeIdentity(first) = (%q, %v), want (%q, nil)", got, err, first)
	}
	if got, err := store.CreateNodeIdentity(ctx, second, now.Add(time.Hour)); err != nil || got != first {
		t.Fatalf("CreateNodeIdentity(second) = (%q, %v), want persisted (%q, nil)", got, err, first)
	}
}

func TestCreateNodeIdentityConcurrentBootstrapConverges(t *testing.T) {
	store := openTestStore(t)
	store.db.SetMaxOpenConns(8)
	store.db.SetMaxIdleConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const contenders = 8
	start := make(chan struct{})
	results := make(chan core.NodeID, contenders)
	errors := make(chan error, contenders)
	var workers sync.WaitGroup
	for index := 0; index < contenders; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			nodeID := core.NodeID(fmt.Sprintf("%032x", index+1))
			persisted, err := store.CreateNodeIdentity(ctx, nodeID, time.Unix(1_700_000_000, 0).UTC())
			if err != nil {
				errors <- err
				return
			}
			results <- persisted
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Errorf("CreateNodeIdentity() error = %v", err)
	}
	var winner core.NodeID
	for persisted := range results {
		if winner == "" {
			winner = persisted
			continue
		}
		if persisted != winner {
			t.Errorf("persisted NodeID = %q, want winner %q", persisted, winner)
		}
	}
	if winner == "" {
		t.Fatal("no bootstrap contender returned a NodeID")
	}
}

func TestBootstrapPersistentNodeIDSurvivesSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "guard.db")
	first, err := Open(ctx, databasePath, migrationFileSystem())
	if err != nil {
		t.Fatalf("Open(first): %v", err)
	}
	firstNodeID, err := core.BootstrapPersistentNodeID(ctx, first)
	if err != nil {
		t.Fatalf("BootstrapPersistentNodeID(first): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	second, err := Open(ctx, databasePath, migrationFileSystem())
	if err != nil {
		t.Fatalf("Open(second): %v", err)
	}
	t.Cleanup(func() { closeStore(t, second) })
	secondNodeID, err := core.BootstrapPersistentNodeID(ctx, second)
	if err != nil {
		t.Fatalf("BootstrapPersistentNodeID(second): %v", err)
	}
	if secondNodeID != firstNodeID {
		t.Fatalf("NodeID after reopen = %q, want first bootstrap value %q", secondNodeID, firstNodeID)
	}
}

var _ core.NodeIdentityStore = (*Store)(nil)
