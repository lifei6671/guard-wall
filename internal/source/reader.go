package source

import (
	"context"

	"github.com/lifei6671/guard-wall/internal/core"
)

// DeliverySink accepts one Source delivery for processing.
type DeliverySink interface {
	Deliver(context.Context, core.Delivery) error
}

// Reader owns one Source intake loop. It must stop all DeliverySink calls
// before Read returns, and it must return promptly after its context is
// canceled so its runtime can freeze the accepted set safely.
type Reader interface {
	Read(context.Context, DeliverySink) error
}
