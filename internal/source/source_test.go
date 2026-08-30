package source

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

var errCheckpoint = errors.New("injected checkpoint failure")

type fakeCheckpointStore struct {
	fail      bool
	writes    int
	sourceID  core.SourceID
	positions []core.SourcePosition
}

func (s *fakeCheckpointStore) saveCheckpoint(_ context.Context, sourceID core.SourceID, _ core.DeliverySequence, position core.SourcePosition) error {
	s.writes++
	if s.fail {
		return errCheckpoint
	}
	s.sourceID = sourceID
	s.positions = append(s.positions, position)
	return nil
}

func TestCheckpoint_OutOfOrderCompletionDoesNotSkipHole(t *testing.T) {
	tracker, err := NewCompletionTracker(core.SourceID("source-1"), 1)
	if err != nil {
		t.Fatalf("NewCompletionTracker() error = %v", err)
	}
	store := &fakeCheckpointStore{}
	manager := NewCheckpointManager(tracker, store)
	second := durableCompletion(t, 2, 20, 30)
	first := durableCompletion(t, 1, 10, 20)

	if err := manager.Complete(context.Background(), second); err != nil {
		t.Fatalf("Complete(second) error = %v", err)
	}
	if store.writes != 0 {
		t.Fatalf("checkpoint writes after sequence 2 = %d, want 0", store.writes)
	}
	if err := manager.Complete(context.Background(), first); err != nil {
		t.Fatalf("Complete(first) error = %v", err)
	}
	if len(store.positions) != 1 {
		t.Fatalf("persisted positions = %d, want 1", len(store.positions))
	}
	file, ok := store.positions[0].File()
	if !ok || file.EndOffset != 30 {
		t.Fatalf("checkpoint position = %#v, want sequence 2 end offset", store.positions[0])
	}
}

func TestCheckpoint_SaveFailureRetainsCandidateForFlush(t *testing.T) {
	tracker, err := NewCompletionTracker(core.SourceID("source-1"), 1)
	if err != nil {
		t.Fatalf("NewCompletionTracker() error = %v", err)
	}
	store := &fakeCheckpointStore{fail: true}
	manager := NewCheckpointManager(tracker, store)

	if err := manager.Complete(context.Background(), durableCompletion(t, 1, 10, 20)); !errors.Is(err, errCheckpoint) {
		t.Fatalf("Complete() error = %v, want checkpoint failure", err)
	}
	store.fail = false
	if err := manager.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if len(store.positions) != 1 {
		t.Fatalf("persisted positions = %d, want 1", len(store.positions))
	}
}

func TestDeliveryQueue_FullWaitsForCancellation(t *testing.T) {
	queue, err := NewDeliveryQueue(1)
	if err != nil {
		t.Fatalf("NewDeliveryQueue() error = %v", err)
	}
	if err := queue.Enqueue(context.Background(), core.Delivery{ID: core.DeliveryID("first")}); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err = queue.Enqueue(ctx, core.Delivery{ID: core.DeliveryID("second")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Enqueue() error = %v, want deadline exceeded", err)
	}
	if queue.Len() != 1 {
		t.Fatalf("queue length = %d, want 1", queue.Len())
	}
}

func durableCompletion(t *testing.T, sequence core.DeliverySequence, start, end uint64) core.DurableCompletion {
	t.Helper()
	position, err := core.NewFilePosition(core.FilePosition{
		Generation:  "00112233445566778899aabbccddeeff",
		DeviceID:    1,
		Inode:       2,
		StartOffset: start,
		EndOffset:   end,
	})
	if err != nil {
		t.Fatalf("NewFilePosition() error = %v", err)
	}
	deliveryID, err := core.FileDeliveryID("source-1", core.FilePosition{
		Generation:  "00112233445566778899aabbccddeeff",
		StartOffset: start,
		EndOffset:   end,
	})
	if err != nil {
		t.Fatalf("FileDeliveryID() error = %v", err)
	}
	return core.DurableCompletion{
		SourceID:   core.SourceID("source-1"),
		DeliveryID: deliveryID,
		Sequence:   sequence,
		Position:   position,
	}
}
