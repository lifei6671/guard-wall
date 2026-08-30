package source

import (
	"context"
	"fmt"

	"github.com/lifei6671/guard-wall/internal/core"
)

// DeliveryQueue is bounded. Enqueue waits for capacity or context
// cancellation; it never silently drops a delivery.
type DeliveryQueue struct {
	items chan core.Delivery
}

func NewDeliveryQueue(capacity int) (*DeliveryQueue, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("queue capacity must be positive")
	}
	return &DeliveryQueue{items: make(chan core.Delivery, capacity)}, nil
}

func (q *DeliveryQueue) Enqueue(ctx context.Context, delivery core.Delivery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case q.items <- delivery:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *DeliveryQueue) Dequeue(ctx context.Context) (core.Delivery, error) {
	if err := ctx.Err(); err != nil {
		return core.Delivery{}, err
	}
	select {
	case delivery := <-q.items:
		return delivery, nil
	case <-ctx.Done():
		return core.Delivery{}, ctx.Err()
	}
}

func (q *DeliveryQueue) Len() int {
	return len(q.items)
}

func (q *DeliveryQueue) Capacity() int {
	return cap(q.items)
}
