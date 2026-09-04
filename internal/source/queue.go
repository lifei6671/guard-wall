package source

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

// ErrDeliveryQueueSealed reports that the fixed accepted set has been frozen.
// No delivery is accepted after this error is returned.
var ErrDeliveryQueueSealed = errors.New("delivery queue sealed")

// QueueStats is a read-only observation of one delivery queue.
type QueueStats struct {
	Capacity             int
	Depth                int
	BackpressureDuration time.Duration
	RejectedTotal        uint64
	OldestItemAge        time.Duration
}

type queuedDelivery struct {
	delivery core.Delivery
}

// DeliveryQueue is bounded. Enqueue waits for capacity or context
// cancellation; it never silently drops a delivery.
type DeliveryQueue struct {
	items      chan queuedDelivery
	slots      chan struct{}
	seal       chan struct{}
	sealDone   chan struct{}
	mu         sync.Mutex
	sealed     bool
	admissions sync.WaitGroup

	statsMu              sync.Mutex
	acceptedAt           []time.Time
	backpressureDuration time.Duration
	rejectedTotal        uint64
}

func NewDeliveryQueue(capacity int) (*DeliveryQueue, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("queue capacity must be positive")
	}
	slots := make(chan struct{}, capacity)
	for range capacity {
		slots <- struct{}{}
	}
	return &DeliveryQueue{
		items:    make(chan queuedDelivery, capacity),
		slots:    slots,
		seal:     make(chan struct{}),
		sealDone: make(chan struct{}),
	}, nil
}

func (q *DeliveryQueue) Enqueue(ctx context.Context, delivery core.Delivery) error {
	if q == nil || q.items == nil || q.slots == nil || q.seal == nil || q.sealDone == nil {
		return fmt.Errorf("delivery queue is not initialized")
	}

	q.mu.Lock()
	if q.sealed {
		q.mu.Unlock()
		q.recordRejected()
		return ErrDeliveryQueueSealed
	}
	if err := ctx.Err(); err != nil {
		q.mu.Unlock()
		q.recordRejected()
		return err
	}
	q.admissions.Add(1)
	q.mu.Unlock()
	defer q.admissions.Done()

	startedAt := time.Now()
	select {
	case <-q.slots:
		q.recordAccepted(delivery, time.Since(startedAt))
		return nil
	case <-q.seal:
		q.recordRejected()
		return ErrDeliveryQueueSealed
	case <-ctx.Done():
		q.recordRejected()
		return ctx.Err()
	}
}

func (q *DeliveryQueue) Dequeue(ctx context.Context) (core.Delivery, error) {
	if q == nil || q.items == nil {
		return core.Delivery{}, fmt.Errorf("delivery queue is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return core.Delivery{}, err
	}
	select {
	case item, ok := <-q.items:
		if !ok {
			return core.Delivery{}, ErrDeliveryQueueSealed
		}
		q.recordDequeued()
		q.slots <- struct{}{}
		return item.delivery, nil
	case <-ctx.Done():
		return core.Delivery{}, ctx.Err()
	}
}

// Seal atomically stops new admissions and freezes the fixed accepted set.
// Calls concurrent with Seal either complete their admission or return
// ErrDeliveryQueueSealed. Accepted deliveries remain available to Dequeue;
// after they are drained, Dequeue returns ErrDeliveryQueueSealed. Seal is
// idempotent and waits for the first sealing operation to finish.
func (q *DeliveryQueue) Seal() {
	if q == nil || q.items == nil || q.seal == nil || q.sealDone == nil {
		return
	}

	q.mu.Lock()
	if q.sealed {
		done := q.sealDone
		q.mu.Unlock()
		<-done
		return
	}
	q.sealed = true
	close(q.seal)
	done := q.sealDone
	q.mu.Unlock()

	q.admissions.Wait()
	close(q.items)
	close(done)
}

func (q *DeliveryQueue) Len() int {
	return len(q.items)
}

func (q *DeliveryQueue) Capacity() int {
	return cap(q.items)
}

// Stats returns the current queue observation. Concurrent producers and
// consumers may advance the queue while the snapshot is assembled.
func (q *DeliveryQueue) Stats() QueueStats {
	if q == nil || q.items == nil || q.slots == nil {
		return QueueStats{}
	}

	stats := QueueStats{
		Capacity: cap(q.items),
	}
	q.statsMu.Lock()
	stats.Depth = len(q.acceptedAt)
	stats.BackpressureDuration = q.backpressureDuration
	stats.RejectedTotal = q.rejectedTotal
	var oldestAcceptedAt time.Time
	if len(q.acceptedAt) > 0 {
		oldestAcceptedAt = q.acceptedAt[0]
	}
	q.statsMu.Unlock()
	if !oldestAcceptedAt.IsZero() {
		stats.OldestItemAge = time.Since(oldestAcceptedAt)
		if stats.OldestItemAge < 0 {
			stats.OldestItemAge = 0
		}
	}
	return stats
}

func (q *DeliveryQueue) recordAccepted(delivery core.Delivery, wait time.Duration) {
	q.statsMu.Lock()
	defer q.statsMu.Unlock()
	q.items <- queuedDelivery{delivery: delivery}
	q.acceptedAt = append(q.acceptedAt, time.Now())
	q.backpressureDuration += wait
}

func (q *DeliveryQueue) recordDequeued() {
	q.statsMu.Lock()
	defer q.statsMu.Unlock()
	if len(q.acceptedAt) == 0 {
		return
	}
	q.acceptedAt = q.acceptedAt[1:]
}

func (q *DeliveryQueue) recordRejected() {
	q.statsMu.Lock()
	q.rejectedTotal++
	q.statsMu.Unlock()
}
