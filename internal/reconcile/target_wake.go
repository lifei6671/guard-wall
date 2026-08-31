package reconcile

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

// DispatcherTargetWakeSink binds the node-aware Decision wake contract to one
// node-local Dispatcher. The Dispatcher always re-reads a fresh Plan.
type DispatcherTargetWakeSink struct {
	nodeID     core.NodeID
	dispatcher *Dispatcher
}

var _ decision.TargetWakeSink = (*DispatcherTargetWakeSink)(nil)

// NewDispatcherTargetWakeSink constructs the node-local Decision wake adapter.
func NewDispatcherTargetWakeSink(
	nodeID core.NodeID,
	dispatcher *Dispatcher,
) (*DispatcherTargetWakeSink, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("dispatcher target wake node id is required")
	}
	if dispatcher == nil {
		return nil, fmt.Errorf("dispatcher target wake dispatcher is required")
	}
	return &DispatcherTargetWakeSink{nodeID: nodeID, dispatcher: dispatcher}, nil
}

// WakeTarget rejects cross-node routing and queues only the Target key.
func (s *DispatcherTargetWakeSink) WakeTarget(
	ctx context.Context,
	nodeID core.NodeID,
	target netip.Prefix,
) error {
	if s == nil || s.dispatcher == nil {
		return fmt.Errorf("dispatcher target wake sink is not initialized")
	}
	if nodeID != s.nodeID {
		return fmt.Errorf("dispatcher target wake node mismatch")
	}
	return s.dispatcher.Wake(ctx, ReconcileKey{Domain: fake.DomainTarget, Target: target})
}
