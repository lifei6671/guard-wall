package source

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

type coverageCheckpointStore struct {
	mu      sync.Mutex
	calls   []checkpointCandidate
	fail    bool
	entered chan struct{}
	release chan struct{}
}

func (s *coverageCheckpointStore) saveCheckpoint(ctx context.Context, id core.SourceID, sequence core.DeliverySequence, position core.SourcePosition, ranges ...core.FilePosition) error {
	s.mu.Lock()
	s.calls = append(s.calls, checkpointCandidate{SourceID: id, ThroughSequence: sequence, Position: position, FileRanges: append([]core.FilePosition(nil), ranges...)})
	first, fail := len(s.calls) == 1, s.fail
	s.mu.Unlock()
	if first && s.entered != nil {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		return errCheckpoint
	}
	return nil
}

func generationCompletion(t *testing.T, seq core.DeliverySequence, generation string, start, end uint64) core.DurableCompletion {
	t.Helper()
	f := core.FilePosition{Generation: generation, DeviceID: 1, Inode: 2, StartOffset: start, EndOffset: end}
	p, err := core.NewFilePosition(f)
	if err != nil {
		t.Fatal(err)
	}
	id, err := core.FileDeliveryID("source-1", f)
	if err != nil {
		t.Fatal(err)
	}
	return core.DurableCompletion{SourceID: "source-1", Sequence: seq, Position: p, DeliveryID: id}
}

func TestCheckpointCoverageRetainsEveryGenerationAcrossFailedFlush(t *testing.T) {
	ctx := context.Background()
	tracker, err := NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	s := &coverageCheckpointStore{fail: true}
	m := NewCheckpointManager(tracker, s)
	a, b := "00112233445566778899aabbccddeeff", "11112233445566778899aabbccddeeff"
	first := generationCompletion(t, 1, a, 0, 10)
	second := generationCompletion(t, 2, b, 0, 10)
	third := generationCompletion(t, 3, a, 10, 20)
	if err := m.Complete(ctx, second); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 0 {
		t.Fatal("sequence hole wrote coverage")
	}
	if err := m.Complete(ctx, first); !errors.Is(err, errCheckpoint) {
		t.Fatalf("first: %v", err)
	}
	if err := m.Complete(ctx, third); !errors.Is(err, errCheckpoint) {
		t.Fatalf("third: %v", err)
	}
	s.fail = false
	if err := m.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	want := []core.FilePosition{{Generation: a, DeviceID: 1, Inode: 2, StartOffset: 0, EndOffset: 20}, {Generation: b, DeviceID: 1, Inode: 2, StartOffset: 0, EndOffset: 10}}
	last := s.calls[len(s.calls)-1]
	if last.ThroughSequence != 3 || last.Position != third.Position || !reflect.DeepEqual(last.FileRanges, want) {
		t.Fatalf("candidate = %#v", last)
	}
	if err := m.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 3 {
		t.Fatalf("writes = %d", len(s.calls))
	}
}

func TestCheckpointCoverageConcurrentFlushPreservesNewRanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tracker, err := NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	s := &coverageCheckpointStore{entered: make(chan struct{}), release: make(chan struct{})}
	m := NewCheckpointManager(tracker, s)
	a, b := "00112233445566778899aabbccddeeff", "11112233445566778899aabbccddeeff"
	results := make(chan error, 2)
	go func() { results <- m.Complete(ctx, generationCompletion(t, 1, a, 0, 10)) }()
	select {
	case <-s.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { results <- m.Complete(ctx, generationCompletion(t, 2, b, 0, 10)) }()
	// 等到第二候选已合并，确保覆盖真正的 Flush/Complete 交错。
	for {
		m.mu.Lock()
		ready := m.pending != nil && m.pending.ThroughSequence == 2
		m.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	close(s.release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) != 2 || len(s.calls[0].FileRanges) != 1 || len(s.calls[1].FileRanges) != 2 || s.calls[1].ThroughSequence != 2 {
		t.Fatalf("calls = %#v", s.calls)
	}
}

func TestCheckpointCoverageRejectsByteHoleWithoutLosingPending(t *testing.T) {
	tracker, err := NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	s := &coverageCheckpointStore{fail: true}
	m := NewCheckpointManager(tracker, s)
	a := "00112233445566778899aabbccddeeff"
	if err := m.Complete(context.Background(), generationCompletion(t, 1, a, 0, 10)); !errors.Is(err, errCheckpoint) {
		t.Fatal(err)
	}
	if err := m.Complete(context.Background(), generationCompletion(t, 2, a, 11, 20)); err == nil {
		t.Fatal("accepted byte hole")
	}
	s.fail = false
	if err := m.Flush(context.Background()); err == nil {
		t.Fatal("invalid range stream resumed")
	}
	if len(s.calls) != 1 || m.pending.ThroughSequence != 1 {
		t.Fatalf("lost pending or wrote invalid candidate: %#v", m.pending)
	}
}
