// Package source contains Source delivery coordination primitives.
package source

import (
	"context"
	"fmt"
	"sync"

	"github.com/lifei6671/guard-wall/internal/core"
)

type checkpointCandidate struct {
	SourceID        core.SourceID
	ThroughSequence core.DeliverySequence
	Position        core.SourcePosition
}

// CompletionTracker advances only across the highest contiguous completion in
// one Source processing session.
type CompletionTracker struct {
	mu        sync.Mutex
	sourceID  core.SourceID
	next      core.DeliverySequence
	completed map[core.DeliverySequence]core.SourcePosition
	exhausted bool
}

func NewCompletionTracker(sourceID core.SourceID, next core.DeliverySequence) (*CompletionTracker, error) {
	if sourceID == "" {
		return nil, fmt.Errorf("source id is required")
	}
	if next == 0 {
		return nil, fmt.Errorf("next delivery sequence must be positive")
	}
	return &CompletionTracker{
		sourceID:  sourceID,
		next:      next,
		completed: make(map[core.DeliverySequence]core.SourcePosition),
	}, nil
}

func (t *CompletionTracker) Mark(completion core.DurableCompletion) (*checkpointCandidate, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if completion.SourceID != t.sourceID {
		return nil, fmt.Errorf("completion source id mismatch")
	}
	if !core.ValidDeliveryID(completion.DeliveryID) || completion.Sequence == 0 || !completion.Position.Valid() {
		return nil, fmt.Errorf("invalid durable completion")
	}
	if t.exhausted {
		return nil, fmt.Errorf("delivery sequence space is exhausted")
	}
	if completion.Sequence < t.next {
		return nil, nil
	}
	if existing, found := t.completed[completion.Sequence]; found {
		if existing != completion.Position {
			return nil, fmt.Errorf("delivery sequence %d has conflicting positions", completion.Sequence)
		}
		return nil, nil
	}
	t.completed[completion.Sequence] = completion.Position

	var candidate *checkpointCandidate
	for {
		position, found := t.completed[t.next]
		if !found {
			break
		}
		candidate = &checkpointCandidate{
			SourceID:        t.sourceID,
			ThroughSequence: t.next,
			Position:        position,
		}
		delete(t.completed, t.next)
		if t.next == ^core.DeliverySequence(0) {
			t.exhausted = true
			break
		}
		t.next++
	}
	return candidate, nil
}

type checkpointStore interface {
	saveCheckpoint(context.Context, core.SourceID, core.DeliverySequence, core.SourcePosition) error
}

// CheckpointManager retains a failed checkpoint candidate so a later Flush can
// retry it without changing the already durable processing receipt.
type CheckpointManager struct {
	mu      sync.Mutex
	flushMu sync.Mutex
	tracker *CompletionTracker
	store   checkpointStore
	pending *checkpointCandidate
}

func NewCheckpointManager(tracker *CompletionTracker, store checkpointStore) *CheckpointManager {
	return &CheckpointManager{tracker: tracker, store: store}
}

func (m *CheckpointManager) Complete(ctx context.Context, completion core.DurableCompletion) error {
	candidate, err := m.tracker.Mark(completion)
	if err != nil {
		return err
	}
	if candidate != nil {
		m.mu.Lock()
		if m.pending == nil || candidate.ThroughSequence > m.pending.ThroughSequence {
			m.pending = candidate
		}
		m.mu.Unlock()
	}
	return m.Flush(ctx)
}

func (m *CheckpointManager) Flush(ctx context.Context) error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()

	m.mu.Lock()
	pending := m.pending
	m.mu.Unlock()
	if pending == nil {
		return nil
	}
	if err := m.store.saveCheckpoint(ctx, pending.SourceID, pending.ThroughSequence, pending.Position); err != nil {
		return fmt.Errorf("save source checkpoint: %w", err)
	}

	m.mu.Lock()
	if m.pending != nil && m.pending.ThroughSequence == pending.ThroughSequence {
		m.pending = nil
	}
	m.mu.Unlock()
	return nil
}
