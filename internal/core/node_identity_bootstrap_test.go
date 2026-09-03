package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestBootstrapPersistentNodeIDCreates128BitLowerHexIdentity(t *testing.T) {
	store := &memoryNodeIdentityStore{}
	now := time.Unix(1_700_000_000, 0).UTC()

	nodeID, err := bootstrapPersistentNodeID(
		context.Background(), store,
		bytes.NewReader([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}),
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("bootstrapPersistentNodeID(): %v", err)
	}
	if want := NodeID("000102030405060708090a0b0c0d0e0f"); nodeID != want {
		t.Fatalf("NodeID = %q, want %q", nodeID, want)
	}
	if store.createdAt != now || store.createCalls != 1 {
		t.Fatalf("store state = createdAt:%s createCalls:%d, want %s and 1", store.createdAt, store.createCalls, now)
	}
}

func TestBootstrapPersistentNodeIDReusesExistingWithoutReadingEntropy(t *testing.T) {
	stored := NodeID("00112233445566778899aabbccddeeff")
	store := &memoryNodeIdentityStore{nodeID: stored, found: true}

	nodeID, err := bootstrapPersistentNodeID(
		context.Background(), store, failingNodeIdentityEntropy{}, time.Now,
	)
	if err != nil {
		t.Fatalf("bootstrapPersistentNodeID(): %v", err)
	}
	if nodeID != stored {
		t.Fatalf("NodeID = %q, want persisted %q", nodeID, stored)
	}
	if store.createCalls != 0 {
		t.Fatalf("CreateNodeIdentity calls = %d, want 0", store.createCalls)
	}
}

func TestBootstrapPersistentNodeIDFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		ctx     context.Context
		store   NodeIdentityStore
		entropy io.Reader
		now     func() time.Time
	}{
		{name: "missing context", store: &memoryNodeIdentityStore{}, entropy: bytes.NewReader(make([]byte, 16)), now: time.Now},
		{name: "missing store", ctx: context.Background(), entropy: bytes.NewReader(make([]byte, 16)), now: time.Now},
		{name: "missing entropy", ctx: context.Background(), store: &memoryNodeIdentityStore{}, now: time.Now},
		{name: "missing clock", ctx: context.Background(), store: &memoryNodeIdentityStore{}, entropy: bytes.NewReader(make([]byte, 16))},
		{name: "entropy failure", ctx: context.Background(), store: &memoryNodeIdentityStore{}, entropy: bytes.NewReader(nil), now: time.Now},
		{name: "load failure", ctx: context.Background(), store: &memoryNodeIdentityStore{loadErr: errors.New("load failed")}, entropy: bytes.NewReader(make([]byte, 16)), now: time.Now},
		{name: "invalid persisted identity", ctx: context.Background(), store: &memoryNodeIdentityStore{nodeID: "invalid", found: true}, entropy: bytes.NewReader(make([]byte, 16)), now: time.Now},
		{name: "create failure", ctx: context.Background(), store: &memoryNodeIdentityStore{createErr: errors.New("create failed")}, entropy: bytes.NewReader(make([]byte, 16)), now: time.Now},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := bootstrapPersistentNodeID(test.ctx, test.store, test.entropy, test.now); err == nil {
				t.Fatal("bootstrapPersistentNodeID() error = nil, want failure")
			}
		})
	}
}

type memoryNodeIdentityStore struct {
	nodeID      NodeID
	found       bool
	loadErr     error
	createErr   error
	createdAt   time.Time
	createCalls int
}

func (s *memoryNodeIdentityStore) LoadNodeIdentity(context.Context) (NodeID, bool, error) {
	return s.nodeID, s.found, s.loadErr
}

func (s *memoryNodeIdentityStore) CreateNodeIdentity(_ context.Context, nodeID NodeID, createdAt time.Time) (NodeID, error) {
	s.createCalls++
	if s.createErr != nil {
		return "", s.createErr
	}
	s.nodeID = nodeID
	s.found = true
	s.createdAt = createdAt
	return nodeID, nil
}

type failingNodeIdentityEntropy struct{}

func (failingNodeIdentityEntropy) Read([]byte) (int, error) {
	return 0, errors.New("entropy should not be read")
}
