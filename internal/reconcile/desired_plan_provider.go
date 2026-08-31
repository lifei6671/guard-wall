package reconcile

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

// DesiredStateReader returns one transaction-consistent Target Desired view
// and the durable reconcile work used during startup recovery.
type DesiredStateReader interface {
	LoadDesiredTargetState(
		context.Context,
		core.NodeID,
	) (core.SnapshotRevision, []core.NormalizedTargetEnforcementIntent, error)
	LoadReconcileRecovery(context.Context, core.NodeID) (core.ReconcileRecoverySnapshot, error)
}

// StaticDesiredFirewallState is the immutable Infrastructure and Policy input
// combined with SQLite-owned Target Desired state by DesiredPlanProvider.
type StaticDesiredFirewallState struct {
	InfrastructureRevision core.InfrastructureRevision
	PolicyRevision         core.PolicyRevision
	Infrastructure         core.ManagedInfrastructureIntent
	Policy                 core.ManagedPolicyIntent
}

// DesiredPlanProvider rebuilds authoritative Plans from current SQLite Target
// Desired state and publishes the same complete snapshot to its Controller.
type DesiredPlanProvider struct {
	nodeID     core.NodeID
	controller *Controller
	reader     DesiredStateReader
	static     StaticDesiredFirewallState
}

var _ PlanProvider = (*DesiredPlanProvider)(nil)

// NewDesiredPlanProvider constructs a node-local production PlanProvider.
func NewDesiredPlanProvider(
	nodeID core.NodeID,
	controller *Controller,
	reader DesiredStateReader,
	static StaticDesiredFirewallState,
) (*DesiredPlanProvider, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("desired Plan provider node id is required")
	}
	if controller == nil {
		return nil, fmt.Errorf("desired Plan provider controller is required")
	}
	if reader == nil {
		return nil, fmt.Errorf("desired Plan provider reader is required")
	}
	if _, err := static.snapshot(1, nil); err != nil {
		return nil, fmt.Errorf("desired Plan provider static state: %w", err)
	}
	return &DesiredPlanProvider{
		nodeID: nodeID, controller: controller, reader: reader, static: static,
	}, nil
}

// ReconcileKeys loads and publishes Desired before Dispatcher startup Probe,
// then returns every current Desired domain plus deduplicated durable recovery
// keys. This guarantees that a clean restart still refreshes Observed state.
func (p *DesiredPlanProvider) ReconcileKeys(ctx context.Context) ([]ReconcileKey, error) {
	if ctx == nil {
		return nil, fmt.Errorf("load startup reconcile keys: context is required")
	}
	desired, err := p.loadAndPublishDesired(ctx)
	if err != nil {
		return nil, err
	}
	recovery, err := p.reader.LoadReconcileRecovery(ctx, p.nodeID)
	if err != nil {
		return nil, fmt.Errorf("load startup reconcile recovery: %w", err)
	}
	keys := make([]ReconcileKey, 0, 2+len(desired.Targets)+len(recovery.States)+len(recovery.ProbeRequirements))
	seen := make(map[ReconcileKey]struct{}, cap(keys))
	appendKey := func(domain core.ReconcileDomain, target netip.Prefix) error {
		key, err := persistedReconcileKey(domain, target)
		if err != nil {
			return err
		}
		if _, duplicate := seen[key]; duplicate {
			return nil
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		return nil
	}
	if err := appendKey(core.ReconcileDomainInfrastructure, netip.Prefix{}); err != nil {
		return nil, err
	}
	if err := appendKey(core.ReconcileDomainPolicy, netip.Prefix{}); err != nil {
		return nil, err
	}
	for _, target := range desired.Targets {
		if err := appendKey(core.ReconcileDomainTarget, target.CanonicalTarget); err != nil {
			return nil, fmt.Errorf("current Desired target: %w", err)
		}
	}
	for _, state := range recovery.States {
		if state.NodeID != p.nodeID {
			return nil, fmt.Errorf("startup reconcile state belongs to another node")
		}
		if err := appendKey(state.Domain, state.Target); err != nil {
			return nil, fmt.Errorf("startup reconcile state: %w", err)
		}
	}
	for _, probe := range recovery.ProbeRequirements {
		if probe.NodeID != p.nodeID {
			return nil, fmt.Errorf("startup Probe requirement belongs to another node")
		}
		if err := appendKey(probe.Domain, probe.Target); err != nil {
			return nil, fmt.Errorf("startup Probe requirement: %w", err)
		}
	}
	return keys, nil
}

// CurrentPlan publishes a fresh complete Desired snapshot and returns the Plan
// for key. ok=false means the Target is no longer materialized.
func (p *DesiredPlanProvider) CurrentPlan(
	ctx context.Context,
	key ReconcileKey,
) (fake.OperationPlan, bool, error) {
	if ctx == nil {
		return fake.OperationPlan{}, false, fmt.Errorf("load current Plan: context is required")
	}
	if err := validateReconcileKey(key); err != nil {
		return fake.OperationPlan{}, false, err
	}
	desired, err := p.loadAndPublishDesired(ctx)
	if err != nil {
		return fake.OperationPlan{}, false, err
	}
	var plan fake.OperationPlan
	switch key.Domain {
	case fake.DomainInfrastructure:
		plan = fake.OperationPlan{
			Domain:                         key.Domain,
			DesiredInfrastructure:          desired.Infrastructure,
			ExpectedInfrastructureRevision: desired.InfrastructureRevision,
			ExpectedSnapshotRevision:       desired.SnapshotRevision,
			FenceSnapshotRevision:          true,
		}
	case fake.DomainPolicy:
		plan = fake.OperationPlan{
			Domain:                 key.Domain,
			DesiredPolicy:          desired.Policy,
			ExpectedPolicyRevision: desired.PolicyRevision,
		}
	case fake.DomainTarget:
		intent, ok := desiredTarget(desired.Targets, key.Target)
		if !ok {
			return fake.OperationPlan{}, false, nil
		}
		plan = fake.OperationPlan{
			Domain:                   key.Domain,
			Target:                   key.Target,
			DesiredTarget:            intent,
			ExpectedTargetGeneration: intent.Generation,
		}
	default:
		return fake.OperationPlan{}, false, fmt.Errorf("load current Plan: unknown domain %d", key.Domain)
	}
	plan.Digest = fake.PlanDigest(plan)
	return plan, true, nil
}

func (p *DesiredPlanProvider) loadAndPublishDesired(
	ctx context.Context,
) (core.DesiredFirewallSnapshot, error) {
	revision, targets, err := p.reader.LoadDesiredTargetState(ctx, p.nodeID)
	if err != nil {
		return core.DesiredFirewallSnapshot{}, fmt.Errorf("load current Target Desired state: %w", err)
	}
	desired, err := p.static.snapshot(revision, targets)
	if err != nil {
		return core.DesiredFirewallSnapshot{}, fmt.Errorf("construct current Desired snapshot: %w", err)
	}
	if err := p.controller.SetDesiredSnapshot(desired); err != nil {
		return core.DesiredFirewallSnapshot{}, fmt.Errorf("publish current Desired snapshot: %w", err)
	}
	return desired, nil
}

func (s StaticDesiredFirewallState) snapshot(
	revision core.SnapshotRevision,
	targets []core.NormalizedTargetEnforcementIntent,
) (core.DesiredFirewallSnapshot, error) {
	return core.NewDesiredFirewallSnapshot(core.DesiredFirewallSnapshot{
		SnapshotRevision:       revision,
		InfrastructureRevision: s.InfrastructureRevision,
		PolicyRevision:         s.PolicyRevision,
		Infrastructure:         s.Infrastructure,
		Policy:                 s.Policy,
		Targets:                targets,
	})
}

func persistedReconcileKey(domain core.ReconcileDomain, target netip.Prefix) (ReconcileKey, error) {
	switch domain {
	case core.ReconcileDomainInfrastructure:
		return ReconcileKey{Domain: fake.DomainInfrastructure}, nil
	case core.ReconcileDomainPolicy:
		return ReconcileKey{Domain: fake.DomainPolicy}, nil
	case core.ReconcileDomainTarget:
		key := ReconcileKey{Domain: fake.DomainTarget, Target: target}
		if err := validateReconcileKey(key); err != nil {
			return ReconcileKey{}, err
		}
		return key, nil
	default:
		return ReconcileKey{}, fmt.Errorf("unknown persisted reconcile domain %d", domain)
	}
}

func desiredTarget(
	targets []core.NormalizedTargetEnforcementIntent,
	target netip.Prefix,
) (core.NormalizedTargetEnforcementIntent, bool) {
	for _, intent := range targets {
		if intent.CanonicalTarget == target {
			return intent, true
		}
	}
	return core.NormalizedTargetEnforcementIntent{}, false
}
