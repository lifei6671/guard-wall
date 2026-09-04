package reconcile

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
)

// DispatcherTargetWakeSink binds the node-aware Decision wake contract to one
// node-local Dispatcher. The Dispatcher always re-reads a fresh Plan.
type DispatcherTargetWakeSink struct {
	nodeID     core.NodeID
	dispatcher *Dispatcher
}

var _ decision.TargetWakeSink = (*DispatcherTargetWakeSink)(nil)

// DispatcherPolicyWakeSink binds the node-aware Policy wake contract to one
// node-local Dispatcher. It queues only the Policy key, never a Target key.
type DispatcherPolicyWakeSink struct {
	nodeID     core.NodeID
	dispatcher *Dispatcher
}

var _ decision.PolicyWakeSink = (*DispatcherPolicyWakeSink)(nil)

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
	return s.dispatcher.Wake(ctx, ReconcileKey{Domain: DomainTarget, Target: target})
}

// NewDispatcherPolicyWakeSink constructs the node-local Policy wake adapter.
func NewDispatcherPolicyWakeSink(
	nodeID core.NodeID,
	dispatcher *Dispatcher,
) (*DispatcherPolicyWakeSink, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("dispatcher policy wake node id is required")
	}
	if dispatcher == nil {
		return nil, fmt.Errorf("dispatcher policy wake dispatcher is required")
	}
	return &DispatcherPolicyWakeSink{nodeID: nodeID, dispatcher: dispatcher}, nil
}

// WakePolicy rejects cross-node routing and queues only the Policy key.
func (s *DispatcherPolicyWakeSink) WakePolicy(ctx context.Context, nodeID core.NodeID) error {
	if s == nil || s.dispatcher == nil {
		return fmt.Errorf("dispatcher policy wake sink is not initialized")
	}
	if nodeID != s.nodeID {
		return fmt.Errorf("dispatcher policy wake node mismatch")
	}
	return s.dispatcher.Wake(ctx, ReconcileKey{Domain: DomainPolicy})
}
