package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

// NodeIdentityStore is the durable singleton boundary required before any
// NodeID-scoped state is constructed.
type NodeIdentityStore interface {
	LoadNodeIdentity(context.Context) (NodeID, bool, error)
	CreateNodeIdentity(context.Context, NodeID, time.Time) (NodeID, error)
}

// BootstrapPersistentNodeID returns the existing persistent NodeID, or creates
// a cryptographically random 128-bit value on first bootstrap. It does not own
// database or process lifecycle; a later runtime composes that ownership.
func BootstrapPersistentNodeID(ctx context.Context, store NodeIdentityStore) (NodeID, error) {
	return bootstrapPersistentNodeID(ctx, store, rand.Reader, time.Now)
}

func bootstrapPersistentNodeID(
	ctx context.Context,
	store NodeIdentityStore,
	entropy io.Reader,
	now func() time.Time,
) (NodeID, error) {
	if ctx == nil {
		return "", fmt.Errorf("bootstrap node identity: context is required")
	}
	if store == nil {
		return "", fmt.Errorf("bootstrap node identity: store is required")
	}
	if entropy == nil {
		return "", fmt.Errorf("bootstrap node identity: entropy source is required")
	}
	if now == nil {
		return "", fmt.Errorf("bootstrap node identity: clock is required")
	}

	persisted, found, err := store.LoadNodeIdentity(ctx)
	if err != nil {
		return "", fmt.Errorf("bootstrap node identity: load: %w", err)
	}
	if found {
		if !isLowerHex128(string(persisted)) {
			return "", fmt.Errorf("bootstrap node identity: persisted node id is invalid")
		}
		return persisted, nil
	}

	var bytes [16]byte
	if _, err := io.ReadFull(entropy, bytes[:]); err != nil {
		return "", fmt.Errorf("bootstrap node identity: random bytes: %w", err)
	}
	candidate := NodeID(hex.EncodeToString(bytes[:]))
	persisted, err = store.CreateNodeIdentity(ctx, candidate, now().UTC())
	if err != nil {
		return "", fmt.Errorf("bootstrap node identity: create: %w", err)
	}
	if !isLowerHex128(string(persisted)) {
		return "", fmt.Errorf("bootstrap node identity: created node id is invalid")
	}
	return persisted, nil
}
