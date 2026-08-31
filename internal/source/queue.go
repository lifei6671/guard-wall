package source

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/lifei6671/guard-wall/internal/core"
)

// ErrDeliveryQueueSealed reports that the fixed accepted set has been frozen.
// No delivery is accepted after this error is returned.
var ErrDeliveryQueueSealed = errors.New("delivery queue sealed")

// DeliveryQueue is bounded. Enqueue waits for capacity or context
// cancellation; it never silently drops a delivery.
type DeliveryQueue struct {
	items      chan core.Delivery
	seal       chan struct{}
	sealDone   chan struct{}
	mu         sync.Mutex
	sealed     bool
	admissions sync.WaitGroup
}

func NewDeliveryQueue(capacity int) (*DeliveryQueue, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("queue capacity must be positive")
	}
	return &DeliveryQueue{
		items:    make(chan core.Delivery, capacity),
		seal:     make(chan struct{}),
		sealDone: make(chan struct{}),
	}, nil
}

func (q *DeliveryQueue) Enqueue(ctx context.Context, delivery core.Delivery) error {
	if q == nil || q.items == nil || q.seal == nil || q.sealDone == nil {
		return fmt.Errorf("delivery queue is not initialized")
	}

	q.mu.Lock()
	if q.sealed {
		q.mu.Unlock()
		return ErrDeliveryQueueSealed
	}
	if err := ctx.Err(); err != nil {
		q.mu.Unlock()
		return err
	}
	q.admissions.Add(1)
	q.mu.Unlock()
	defer q.admissions.Done()

	select {
	case q.items <- delivery:
		return nil
	case <-q.seal:
		return ErrDeliveryQueueSealed
	case <-ctx.Done():
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
	case delivery, ok := <-q.items:
		if !ok {
			return core.Delivery{}, ErrDeliveryQueueSealed
		}
		return delivery, nil
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
