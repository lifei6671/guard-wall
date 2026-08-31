package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

func TestDesiredPlanProviderReconcileKeysPublishesDesiredAndDeduplicates(t *testing.T) {
	target := netip.MustParsePrefix("192.0.2.44/32")
	reader := &desiredStateReaderStub{
		revision: 7,
		targets:  []core.NormalizedTargetEnforcementIntent{targetIntent(target, 3)},
		recovery: core.ReconcileRecoverySnapshot{
			States: []core.PersistedReconcileState{
				{NodeID: testNodeID, Domain: core.ReconcileDomainInfrastructure, InfrastructureRevision: 1},
				{NodeID: testNodeID, Domain: core.ReconcileDomainTarget, Target: target, TargetGeneration: 3},
			},
			ProbeRequirements: []core.PersistedProbeRequirement{
				{NodeID: testNodeID, Domain: core.ReconcileDomainTarget, Target: target, TargetGeneration: 3},
			},
		},
	}
	controller := newTestController(t, fake.NewBackend(), &manualClock{now: time.Unix(1, 0)}, &memoryAudit{})
	provider := newTestDesiredPlanProvider(t, controller, reader)

	keys, err := provider.ReconcileKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []ReconcileKey{
		{Domain: fake.DomainInfrastructure},
		{Domain: fake.DomainPolicy},
		{Domain: fake.DomainTarget, Target: target},
	}
	if len(keys) != len(want) {
		t.Fatalf("got %d startup keys, want %d: %#v", len(keys), len(want), keys)
	}
	for index := range want {
		if keys[index] != want[index] {
			t.Fatalf("startup key %d = %#v, want %#v", index, keys[index], want[index])
		}
	}
	if !controller.hasDesired || controller.desired.SnapshotRevision != 7 {
		t.Fatalf("Desired was not published before startup recovery: %+v", controller.desired)
	}
}

func TestDesiredPlanProviderCurrentTargetPlanUsesFreshSnapshot(t *testing.T) {
	target := netip.MustParsePrefix("198.51.100.8/32")
	reader := &desiredStateReaderStub{
		revision: 11,
		targets: []core.NormalizedTargetEnforcementIntent{{
			NodeID: testNodeID, CanonicalTarget: target, BanMembership: core.BanAbsent,
			TimeoutMode: core.TimeoutNone, Scopes: core.ScopeInput,
			AddressFamily: core.AddressFamilyIPv4, PolicyCoverage: core.PolicyCoverageNone,
			Generation: 4,
		}},
	}
	controller := newTestController(t, fake.NewBackend(), &manualClock{now: time.Unix(1, 0)}, &memoryAudit{})
	provider := newTestDesiredPlanProvider(t, controller, reader)

	plan, ok, err := provider.CurrentPlan(context.Background(), ReconcileKey{Domain: fake.DomainTarget, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("fresh Target Plan was not returned")
	}
	if plan.DesiredTarget.BanMembership != core.BanAbsent || plan.ExpectedTargetGeneration != 4 {
		t.Fatalf("unexpected Target Plan: %+v", plan)
	}
	if plan.Digest != fake.PlanDigest(plan) {
		t.Fatal("Target Plan digest does not bind the fresh payload")
	}
	if controller.desired.SnapshotRevision != 11 {
		t.Fatalf("Controller has snapshot revision %d, want 11", controller.desired.SnapshotRevision)
	}
}

func TestDesiredPlanProviderCurrentTargetPlanReturnsNotFound(t *testing.T) {
	target := netip.MustParsePrefix("203.0.113.9/32")
	controller := newTestController(t, fake.NewBackend(), &manualClock{now: time.Unix(1, 0)}, &memoryAudit{})
	provider := newTestDesiredPlanProvider(t, controller, &desiredStateReaderStub{revision: 1})

	_, ok, err := provider.CurrentPlan(context.Background(), ReconcileKey{Domain: fake.DomainTarget, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("missing Target unexpectedly produced a Plan")
	}
}

func TestDesiredPlanProviderRejectsCrossNodeRecovery(t *testing.T) {
	reader := &desiredStateReaderStub{
		revision: 1,
		recovery: core.ReconcileRecoverySnapshot{States: []core.PersistedReconcileState{{
			NodeID: "ffeeddccbbaa99887766554433221100",
			Domain: core.ReconcileDomainInfrastructure, InfrastructureRevision: 1,
		}}},
	}
	controller := newTestController(t, fake.NewBackend(), &manualClock{now: time.Unix(1, 0)}, &memoryAudit{})
	provider := newTestDesiredPlanProvider(t, controller, reader)

	if _, err := provider.ReconcileKeys(context.Background()); err == nil {
		t.Fatal("cross-node recovery state was accepted")
	}
}

func TestDesiredPlanProviderPropagatesDesiredReadFailure(t *testing.T) {
	want := errors.New("read failed")
	controller := newTestController(t, fake.NewBackend(), &manualClock{now: time.Unix(1, 0)}, &memoryAudit{})
	provider := newTestDesiredPlanProvider(t, controller, &desiredStateReaderStub{desiredErr: want})

	if _, err := provider.ReconcileKeys(context.Background()); !errors.Is(err, want) {
		t.Fatalf("got %v, want desired read failure", err)
	}
}

func TestDesiredPlanProviderWakeRefreshesPastConvergedGeneration(t *testing.T) {
	target := netip.MustParsePrefix("192.0.2.88/32")
	clock := &manualClock{now: time.Unix(1, 0)}
	backend := fake.NewBackend()
	controller := newTestController(t, backend, clock, &memoryAudit{})
	present := targetIntent(target, 1)
	initial := desiredSnapshot(present)
	setDesired(t, controller, initial)
	if _, err := controller.Execute(context.Background(), targetPlan(initial, target)); err != nil {
		t.Fatal(err)
	}
	if _, state, ok := controller.TargetState(target); !ok || state.Status != core.ReconcileConverged {
		t.Fatalf("initial generation did not converge: %+v, %t", state, ok)
	}

	absent := present
	absent.BanMembership = core.BanAbsent
	absent.TimeoutMode = core.TimeoutNone
	absent.EffectiveUntil = nil
	absent.Generation = 2
	reader := &desiredStateReaderStub{revision: 2, targets: []core.NormalizedTargetEnforcementIntent{absent}}
	provider := newTestDesiredPlanProvider(t, controller, reader)
	dispatcher, err := NewDispatcher(controller, provider, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.dispatch(
		context.Background(),
		dispatchWake{key: ReconcileKey{Domain: fake.DomainTarget, Target: target}},
		make(map[ReconcileKey]time.Time),
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Targets[target]; exists {
		t.Fatal("wake for generation 2 was discarded behind converged generation 1")
	}
}

func newTestDesiredPlanProvider(
	t *testing.T,
	controller *Controller,
	reader DesiredStateReader,
) *DesiredPlanProvider {
	t.Helper()
	provider, err := NewDesiredPlanProvider(testNodeID, controller, reader, StaticDesiredFirewallState{
		InfrastructureRevision: 1,
		PolicyRevision:         1,
		Infrastructure: core.ManagedInfrastructureIntent{
			Backend: "fake", OwnerVersion: "v1", Digest: "infra-v1",
		},
		Policy: core.ManagedPolicyIntent{RelationDigest: "policy-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

type desiredStateReaderStub struct {
	revision    core.SnapshotRevision
	targets     []core.NormalizedTargetEnforcementIntent
	recovery    core.ReconcileRecoverySnapshot
	desiredErr  error
	recoveryErr error
}

func (r *desiredStateReaderStub) LoadDesiredTargetState(
	context.Context,
	core.NodeID,
) (core.SnapshotRevision, []core.NormalizedTargetEnforcementIntent, error) {
	if r.desiredErr != nil {
		return 0, nil, r.desiredErr
	}
	return r.revision, append([]core.NormalizedTargetEnforcementIntent(nil), r.targets...), nil
}

func (r *desiredStateReaderStub) LoadReconcileRecovery(
	context.Context,
	core.NodeID,
) (core.ReconcileRecoverySnapshot, error) {
	if r.recoveryErr != nil {
		return core.ReconcileRecoverySnapshot{}, r.recoveryErr
	}
	return r.recovery, nil
}
