package source

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestDeliveryQueueSealEmptyAndRepeated(t *testing.T) {
	queue, err := NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}

	queue.Seal()
	queue.Seal()

	if err := queue.Enqueue(context.Background(), core.Delivery{}); !errors.Is(err, ErrDeliveryQueueSealed) {
		t.Fatalf("Enqueue() error = %v, want ErrDeliveryQueueSealed", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := queue.Enqueue(cancelled, core.Delivery{}); !errors.Is(err, ErrDeliveryQueueSealed) {
		t.Fatalf("cancelled Enqueue() error = %v, want ErrDeliveryQueueSealed", err)
	}
	if _, err := queue.Dequeue(context.Background()); !errors.Is(err, ErrDeliveryQueueSealed) {
		t.Fatalf("Dequeue() error = %v, want ErrDeliveryQueueSealed", err)
	}
}

func TestDeliveryQueueSealDrainsAcceptedItems(t *testing.T) {
	queue, err := NewDeliveryQueue(2)
	if err != nil {
		t.Fatal(err)
	}
	first := core.Delivery{ID: "first"}
	second := core.Delivery{ID: "second"}
	if err := queue.Enqueue(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	queue.Seal()

	for index, want := range []core.Delivery{first, second} {
		got, err := queue.Dequeue(context.Background())
		if err != nil {
			t.Fatalf("Dequeue(%d) error = %v", index, err)
		}
		if got.ID != want.ID {
			t.Fatalf("Dequeue(%d) ID = %q, want %q", index, got.ID, want.ID)
		}
	}
	if _, err := queue.Dequeue(context.Background()); !errors.Is(err, ErrDeliveryQueueSealed) {
		t.Fatalf("final Dequeue() error = %v, want ErrDeliveryQueueSealed", err)
	}
}

func TestDeliveryQueueSealReleasesBlockedEnqueue(t *testing.T) {
	queue, err := NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	first := core.Delivery{ID: "first"}
	if err := queue.Enqueue(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	observed := make(chan struct{})
	producerContext := &doneObservedContext{Context: context.Background(), observed: observed}
	producerResult := make(chan error, 1)
	go func() {
		producerResult <- queue.Enqueue(producerContext, core.Delivery{ID: "blocked"})
	}()
	<-observed
	select {
	case err := <-producerResult:
		t.Fatalf("blocked Enqueue() returned before Seal: %v", err)
	default:
	}

	queue.Seal()

	if err := <-producerResult; !errors.Is(err, ErrDeliveryQueueSealed) {
		t.Fatalf("blocked Enqueue() error = %v, want ErrDeliveryQueueSealed", err)
	}
	got, err := queue.Dequeue(context.Background())
	if err != nil || got.ID != first.ID {
		t.Fatalf("Dequeue() = %+v, %v, want first accepted delivery", got, err)
	}
	if _, err := queue.Dequeue(context.Background()); !errors.Is(err, ErrDeliveryQueueSealed) {
		t.Fatalf("final Dequeue() error = %v, want ErrDeliveryQueueSealed", err)
	}
}

func TestDeliveryQueueEnqueueSealRaceIsLinearizable(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		queue, err := NewDeliveryQueue(1)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		enqueueResult := make(chan error, 1)
		sealDone := make(chan struct{})
		go func() {
			<-start
			enqueueResult <- queue.Enqueue(context.Background(), core.Delivery{ID: "raced"})
		}()
		go func() {
			<-start
			queue.Seal()
			close(sealDone)
		}()

		close(start)
		enqueueErr := <-enqueueResult
		<-sealDone
		if enqueueErr == nil {
			delivery, err := queue.Dequeue(context.Background())
			if err != nil || delivery.ID != "raced" {
				t.Fatalf("iteration %d accepted delivery = %+v, %v", iteration, delivery, err)
			}
		} else if !errors.Is(enqueueErr, ErrDeliveryQueueSealed) {
			t.Fatalf("iteration %d Enqueue() error = %v", iteration, enqueueErr)
		}
		if _, err := queue.Dequeue(context.Background()); !errors.Is(err, ErrDeliveryQueueSealed) {
			t.Fatalf("iteration %d final Dequeue() error = %v", iteration, err)
		}
	}
}

func TestDeliveryQueueConcurrentRepeatedSeal(t *testing.T) {
	queue, err := NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(context.Background(), core.Delivery{ID: "accepted"}); err != nil {
		t.Fatal(err)
	}

	const callers = 16
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			queue.Seal()
		}()
	}
	wait.Wait()

	if _, err := queue.Dequeue(context.Background()); err != nil {
		t.Fatalf("Dequeue accepted delivery: %v", err)
	}
	if _, err := queue.Dequeue(context.Background()); !errors.Is(err, ErrDeliveryQueueSealed) {
		t.Fatalf("final Dequeue() error = %v, want ErrDeliveryQueueSealed", err)
	}
}

func TestDeliveryQueueZeroValueFailsWithoutBlocking(t *testing.T) {
	var queue DeliveryQueue
	if err := queue.Enqueue(context.Background(), core.Delivery{}); err == nil {
		t.Fatal("zero-value Enqueue() error = nil")
	}
	if _, err := queue.Dequeue(context.Background()); err == nil {
		t.Fatal("zero-value Dequeue() error = nil")
	}
	queue.Seal()
	if stats := queue.Stats(); stats != (QueueStats{}) {
		t.Fatalf("Stats() = %+v, want zero value", stats)
	}
}

func TestDeliveryQueueStatsTracksAcceptedBackpressureAndOldestItem(t *testing.T) {
	queue, err := NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	first := core.Delivery{ID: "first"}
	second := core.Delivery{ID: "second"}
	if err := queue.Enqueue(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	observed := make(chan struct{})
	producerContext := &doneObservedContext{Context: context.Background(), observed: observed}
	producerResult := make(chan error, 1)
	go func() {
		producerResult <- queue.Enqueue(producerContext, second)
	}()
	<-observed
	time.Sleep(time.Millisecond)

	stats := queue.Stats()
	if stats.Capacity != 1 || stats.Depth != 1 {
		t.Fatalf("Stats() = %+v, want capacity=1 depth=1", stats)
	}
	if stats.OldestItemAge <= 0 {
		t.Fatalf("OldestItemAge = %s, want positive", stats.OldestItemAge)
	}
	if stats.RejectedTotal != 0 {
		t.Fatalf("Stats() before blocked admission succeeds = %+v", stats)
	}
	backpressureBefore := stats.BackpressureDuration

	if _, err := queue.Dequeue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-producerResult; err != nil {
		t.Fatalf("blocked Enqueue() error = %v", err)
	}
	stats = queue.Stats()
	if stats.Depth != 1 || stats.BackpressureDuration <= backpressureBefore || stats.RejectedTotal != 0 {
		t.Fatalf("Stats() after blocked admission = %+v", stats)
	}
	if _, err := queue.Dequeue(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats = queue.Stats()
	if stats.Depth != 0 || stats.OldestItemAge != 0 {
		t.Fatalf("Stats() after drain = %+v", stats)
	}
}

func TestDeliveryQueueStatsCountsTerminalRejections(t *testing.T) {
	queue, err := NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := queue.Enqueue(cancelled, core.Delivery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Enqueue() error = %v, want context.Canceled", err)
	}
	queue.Seal()
	if err := queue.Enqueue(context.Background(), core.Delivery{}); !errors.Is(err, ErrDeliveryQueueSealed) {
		t.Fatalf("sealed Enqueue() error = %v, want ErrDeliveryQueueSealed", err)
	}
	stats := queue.Stats()
	if stats.RejectedTotal != 2 || stats.Depth != 0 || stats.OldestItemAge != 0 {
		t.Fatalf("Stats() = %+v, want two terminal rejections and empty queue", stats)
	}
}

func TestDeliveryQueueStatsRemainsBoundedDuringConcurrentUse(t *testing.T) {
	queue, err := NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	producerDone := make(chan struct{})
	consumerDone := make(chan struct{})
	observed := make(chan error, 1)
	go func() {
		defer close(producerDone)
		for index := 0; index < 100; index++ {
			if err := queue.Enqueue(context.Background(), core.Delivery{ID: core.DeliveryID("delivery")}); err != nil {
				observed <- err
				return
			}
		}
		queue.Seal()
	}()
	go func() {
		defer close(consumerDone)
		for {
			if _, err := queue.Dequeue(context.Background()); errors.Is(err, ErrDeliveryQueueSealed) {
				return
			} else if err != nil {
				observed <- err
				return
			}
		}
	}()

	for index := 0; index < 100; index++ {
		stats := queue.Stats()
		if stats.Depth < 0 || stats.Depth > stats.Capacity || stats.OldestItemAge < 0 {
			t.Fatalf("Stats() = %+v, want bounded concurrent observation", stats)
		}
	}
	<-producerDone
	<-consumerDone
	select {
	case err := <-observed:
		t.Fatalf("concurrent queue operation: %v", err)
	default:
	}
	if stats := queue.Stats(); stats.Depth != 0 || stats.OldestItemAge != 0 {
		t.Fatalf("Stats() after concurrent drain = %+v", stats)
	}
}

type doneObservedContext struct {
	context.Context
	once     sync.Once
	observed chan struct{}
}

func (c *doneObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}
