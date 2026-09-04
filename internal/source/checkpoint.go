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
	FileRanges      []core.FilePosition
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
	next := t.next
	for {
		position, found := t.completed[next]
		if !found {
			break
		}
		if candidate == nil {
			candidate = &checkpointCandidate{SourceID: t.sourceID}
		}
		candidate.ThroughSequence, candidate.Position = next, position
		if file, ok := position.File(); ok {
			var err error
			candidate.FileRanges, err = mergeFileRanges(candidate.FileRanges, []core.FilePosition{file})
			if err != nil {
				return nil, err
			}
		}
		if next == ^core.DeliverySequence(0) {
			break
		}
		next++
	}
	if candidate != nil {
		for {
			delete(t.completed, t.next)
			if t.next == ^core.DeliverySequence(0) {
				t.exhausted = true
				break
			}
			t.next++
			if t.next > candidate.ThroughSequence {
				break
			}
		}
	}
	return candidate, nil
}

type checkpointStore interface {
	saveCheckpoint(context.Context, core.SourceID, core.DeliverySequence, core.SourcePosition, ...core.FilePosition) error
}

// CheckpointManager retains a failed checkpoint candidate so a later Flush can
// retry it without changing the already durable processing receipt.
type CheckpointManager struct {
	mu      sync.Mutex
	flushMu sync.Mutex
	tracker *CompletionTracker
	store   checkpointStore
	pending *checkpointCandidate
	invalid error
}

func NewCheckpointManager(tracker *CompletionTracker, store checkpointStore) *CheckpointManager {
	return &CheckpointManager{tracker: tracker, store: store}
}

func (m *CheckpointManager) Complete(ctx context.Context, completion core.DurableCompletion) error {
	// Mark 和 pending 合并共用顺序，防止较晚候选先入队而遗漏较早代际。
	m.mu.Lock()
	if m.invalid != nil {
		err := m.invalid
		m.mu.Unlock()
		return err
	}
	candidate, err := m.tracker.Mark(completion)
	if err != nil {
		m.invalid = err
		m.mu.Unlock()
		return err
	}
	if candidate != nil {
		if m.pending != nil {
			candidate.FileRanges, err = mergeFileRanges(m.pending.FileRanges, candidate.FileRanges)
			if err != nil {
				m.invalid = err
				m.mu.Unlock()
				return err
			}
		}
		m.pending = candidate
	}
	m.mu.Unlock()
	return m.Flush(ctx)
}

func (m *CheckpointManager) Flush(ctx context.Context) error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()

	m.mu.Lock()
	pending := m.pending
	invalid := m.invalid
	m.mu.Unlock()
	if invalid != nil {
		return invalid
	}
	if pending == nil {
		return nil
	}
	if err := m.store.saveCheckpoint(ctx, pending.SourceID, pending.ThroughSequence, pending.Position, pending.FileRanges...); err != nil {
		return fmt.Errorf("save source checkpoint: %w", err)
	}

	m.mu.Lock()
	if m.pending != nil && m.pending.ThroughSequence == pending.ThroughSequence {
		m.pending = nil
	}
	m.mu.Unlock()
	return nil
}

// mergeFileRanges 保留全部代际，只压缩字节严格相邻且身份一致的范围。
// 返回独立切片，Flush 持有的快照不会被并发 Complete 改写。
func mergeFileRanges(previous, next []core.FilePosition) ([]core.FilePosition, error) {
	merged := append([]core.FilePosition(nil), previous...)
	indices := make(map[string]int, len(merged))
	for i, file := range merged {
		indices[file.Generation] = i
	}
	for _, file := range next {
		if i, found := indices[file.Generation]; found {
			last := &merged[i]
			if last.DeviceID != file.DeviceID || last.Inode != file.Inode || last.EndOffset != file.StartOffset {
				return nil, fmt.Errorf("generation %q completion range is not contiguous", file.Generation)
			}
			last.EndOffset = file.EndOffset
		} else {
			indices[file.Generation] = len(merged)
			merged = append(merged, file)
		}
	}
	return merged, nil
}
