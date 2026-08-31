package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

const probeFailureErrorCode = "backend_probe_failed"

func observedSnapshotHasState(snapshot core.ObservedFirewallSnapshot) bool {
	return snapshot.Infrastructure != nil || snapshot.Policy != nil || len(snapshot.Targets) != 0
}

func (c *Controller) seedObservedClock(snapshot core.ObservedFirewallSnapshot) {
	observe := func(value time.Time) {
		value = value.UTC().Truncate(time.Microsecond)
		if value.After(c.lastObservedAt) {
			c.lastObservedAt = value
		}
	}
	if snapshot.Infrastructure != nil {
		observe(snapshot.Infrastructure.ObservedAt)
	}
	if snapshot.Policy != nil {
		observe(snapshot.Policy.ObservedAt)
	}
	for _, target := range snapshot.Targets {
		observe(target.ObservedAt)
	}
}

// nextObservedAtLocked preserves the Store's strict monotonic observation
// fence even when tests or a restarted process use a non-advancing clock.
func (c *Controller) nextObservedAtLocked() time.Time {
	now := c.clock.Now().UTC().Truncate(time.Microsecond)
	if !now.After(c.lastObservedAt) {
		now = c.lastObservedAt.Add(time.Microsecond)
	}
	c.lastObservedAt = now
	return now
}

func (c *Controller) observedUpdateForSnapshotLocked(snapshot fake.Snapshot) (*core.ObservedFirewallUpdate, error) {
	if c.store == nil || !c.hasDesired {
		return nil, nil
	}
	observedAt := c.nextObservedAtLocked()
	update := core.ObservedFirewallUpdate{NodeID: c.nodeID}
	if snapshot.Infrastructure == nil {
		update.Infrastructure = &core.InfrastructureObservedState{
			Presence: core.ObservedPresenceAbsent, ObservedAt: observedAt,
		}
	} else {
		update.Infrastructure = &core.InfrastructureObservedState{
			Presence:     core.ObservedPresencePresent,
			ObservedAt:   observedAt,
			Backend:      snapshot.Infrastructure.Backend,
			OwnerVersion: snapshot.Infrastructure.OwnerVersion,
			Digest:       snapshot.Infrastructure.Digest,
		}
		if c.snapshotMatchesCurrentDesiredLocked(snapshot, fake.DomainInfrastructure, netip.Prefix{}) {
			update.Infrastructure.ConfirmedRevision = c.desired.InfrastructureRevision
		}
	}
	if snapshot.Policy == nil {
		update.Policy = &core.PolicyObservedState{
			Presence: core.ObservedPresenceAbsent, ObservedAt: observedAt,
		}
	} else {
		update.Policy = &core.PolicyObservedState{
			Presence:       core.ObservedPresencePresent,
			ObservedAt:     observedAt,
			RelationDigest: snapshot.Policy.RelationDigest,
		}
		if c.snapshotMatchesCurrentDesiredLocked(snapshot, fake.DomainPolicy, netip.Prefix{}) {
			update.Policy.ConfirmedRevision = c.desired.PolicyRevision
		}
	}

	targets := make([]netip.Prefix, 0, len(c.desiredTargets))
	for target := range c.desiredTargets {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(left, right int) bool {
		return targets[left].String() < targets[right].String()
	})
	update.Targets = make([]core.TargetObservedState, 0, len(targets))
	for _, target := range targets {
		physical, exists := snapshot.Targets[target]
		if !exists {
			physical = core.PhysicalTargetObserved{
				CanonicalTarget: target,
				BanMembership:   core.ObservedMembershipAbsent,
			}
		} else {
			physical.CanonicalTarget = target
			physical.NativeExpiry = cloneTime(physical.NativeExpiry)
		}
		physical.ObservedAt = observedAt
		observed := core.TargetObservedState{PhysicalTargetObserved: physical}
		if c.snapshotMatchesCurrentDesiredLocked(snapshot, fake.DomainTarget, target) {
			observed.ConfirmedGeneration = c.desiredTargets[target].Generation
		}
		update.Targets = append(update.Targets, observed)
	}
	if err := update.Validate(); err != nil {
		return nil, fmt.Errorf("construct Observed firewall update: %w", err)
	}
	return &update, nil
}

func (c *Controller) unknownObservedUpdateLocked(domain fake.Domain, target netip.Prefix, code string) (*core.ObservedFirewallUpdate, error) {
	if c.store == nil || !c.hasDesired {
		return nil, nil
	}
	if code == "" {
		code = probeFailureErrorCode
	}
	observedAt := c.nextObservedAtLocked()
	update := core.ObservedFirewallUpdate{NodeID: c.nodeID}
	switch domain {
	case fake.DomainInfrastructure:
		update.Infrastructure = &core.InfrastructureObservedState{
			Presence: core.ObservedPresenceUnknown, ObservedAt: observedAt, LastErrorCode: code,
		}
	case fake.DomainPolicy:
		update.Policy = &core.PolicyObservedState{
			Presence: core.ObservedPresenceUnknown, ObservedAt: observedAt, LastErrorCode: code,
		}
	case fake.DomainTarget:
		if _, exists := c.desiredTargets[target]; !exists {
			return nil, fmt.Errorf("construct Target Observed failure: target %s is not Desired", target)
		}
		update.Targets = []core.TargetObservedState{{PhysicalTargetObserved: core.PhysicalTargetObserved{
			CanonicalTarget: target,
			ObservedAt:      observedAt,
			BanMembership:   core.ObservedMembershipUnknown,
			LastErrorCode:   code,
		}}}
	default:
		return nil, fmt.Errorf("construct Observed failure: unknown domain %d", domain)
	}
	if err := update.Validate(); err != nil {
		return nil, fmt.Errorf("construct Observed failure update: %w", err)
	}
	return &update, nil
}

func (c *Controller) allUnknownObservedUpdateLocked(code string) (*core.ObservedFirewallUpdate, error) {
	if c.store == nil || !c.hasDesired {
		return nil, nil
	}
	if code == "" {
		code = probeFailureErrorCode
	}
	observedAt := c.nextObservedAtLocked()
	update := core.ObservedFirewallUpdate{
		NodeID: c.nodeID,
		Infrastructure: &core.InfrastructureObservedState{
			Presence: core.ObservedPresenceUnknown, ObservedAt: observedAt, LastErrorCode: code,
		},
		Policy: &core.PolicyObservedState{
			Presence: core.ObservedPresenceUnknown, ObservedAt: observedAt, LastErrorCode: code,
		},
	}
	targets := make([]netip.Prefix, 0, len(c.desiredTargets))
	for target := range c.desiredTargets {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(left, right int) bool {
		return targets[left].String() < targets[right].String()
	})
	for _, target := range targets {
		update.Targets = append(update.Targets, core.TargetObservedState{PhysicalTargetObserved: core.PhysicalTargetObserved{
			CanonicalTarget: target,
			ObservedAt:      observedAt,
			BanMembership:   core.ObservedMembershipUnknown,
			LastErrorCode:   code,
		}})
	}
	if err := update.Validate(); err != nil {
		return nil, fmt.Errorf("construct failed Probe Observed update: %w", err)
	}
	return &update, nil
}

func (c *Controller) persistObservedLocked(ctx context.Context, update *core.ObservedFirewallUpdate) error {
	if update == nil || c.store == nil {
		return nil
	}
	err := c.store.ApplyObservedFirewallUpdate(ctx, *update)
	if !errors.Is(err, core.ErrReconcileCommitUnknown) {
		return err
	}
	snapshot, loadErr := c.store.LoadObservedFirewallSnapshot(ctx, c.nodeID)
	if loadErr != nil {
		return errors.Join(err, fmt.Errorf("read back indeterminate Observed commit: %w", loadErr))
	}
	if observedUpdateApplied(snapshot, *update) {
		return nil
	}
	return err
}

func (c *Controller) recordAllUnknownObserved(ctx context.Context, code string) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	update, err := c.allUnknownObservedUpdateLocked(code)
	if err != nil {
		return err
	}
	if err := c.persistObservedLocked(ctx, update); err != nil {
		return fmt.Errorf("persist failed Probe observation: %w", err)
	}
	return nil
}

func observedUpdateApplied(snapshot core.ObservedFirewallSnapshot, update core.ObservedFirewallUpdate) bool {
	if snapshot.NodeID != update.NodeID {
		return false
	}
	if update.Infrastructure != nil &&
		(snapshot.Infrastructure == nil || !sameInfrastructureObserved(*snapshot.Infrastructure, *update.Infrastructure)) {
		return false
	}
	if update.Policy != nil &&
		(snapshot.Policy == nil || !samePolicyObserved(*snapshot.Policy, *update.Policy)) {
		return false
	}
	for _, expected := range update.Targets {
		found := false
		for _, actual := range snapshot.Targets {
			if actual.CanonicalTarget == expected.CanonicalTarget {
				found = sameTargetObserved(actual, expected)
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameInfrastructureObserved(left, right core.InfrastructureObservedState) bool {
	return left.Presence == right.Presence && equalObservedTime(left.ObservedAt, right.ObservedAt) &&
		left.Backend == right.Backend && left.OwnerVersion == right.OwnerVersion && left.Digest == right.Digest &&
		left.ConfirmedRevision == right.ConfirmedRevision && left.LastErrorCode == right.LastErrorCode
}

func samePolicyObserved(left, right core.PolicyObservedState) bool {
	return left.Presence == right.Presence && equalObservedTime(left.ObservedAt, right.ObservedAt) &&
		left.RelationDigest == right.RelationDigest && left.ConfirmedRevision == right.ConfirmedRevision &&
		left.LastErrorCode == right.LastErrorCode
}

func sameTargetObserved(left, right core.TargetObservedState) bool {
	return left.CanonicalTarget == right.CanonicalTarget && equalObservedTime(left.ObservedAt, right.ObservedAt) &&
		left.Backend == right.Backend && left.BanMembership == right.BanMembership &&
		left.PolicyCoverage == right.PolicyCoverage && left.PolicyRelationDigest == right.PolicyRelationDigest &&
		left.TimeoutMode == right.TimeoutMode && equalOptionalObservedTime(left.NativeExpiry, right.NativeExpiry) &&
		left.Scopes == right.Scopes && left.AddressFamily == right.AddressFamily &&
		left.OwnerVersion == right.OwnerVersion && left.LastErrorCode == right.LastErrorCode &&
		left.ConfirmedGeneration == right.ConfirmedGeneration
}

func equalObservedTime(left, right time.Time) bool {
	return left.UTC().UnixMicro() == right.UTC().UnixMicro()
}

func equalOptionalObservedTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return equalObservedTime(*left, *right)
}
