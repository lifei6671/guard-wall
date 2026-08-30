package fake

import (
	"context"
	"net/netip"
	"testing"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestBackendConfirmedRejectedAndUnknown(t *testing.T) {
	backend := NewBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	intent := targetIntent(target, 1)
	plan := targetPlan(target, intent)

	result, err := backend.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != ResultConfirmed {
		t.Fatalf("default result = %v, want confirmed", result.Kind)
	}
	snapshot, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observed, exists := snapshot.Targets[target]
	if !exists || observed.BanMembership != core.ObservedMembershipPresent {
		t.Fatalf("confirmed target was not materialized: %+v", observed)
	}

	if err := backend.QueueOutcome(DomainTarget, QueuedOutcome{Kind: ResultRejected, ErrorCode: "transient"}); err != nil {
		t.Fatal(err)
	}
	changed := intent
	changed.Scopes = core.ScopeForward
	changed.Generation++
	result, err = backend.Apply(context.Background(), targetPlan(target, changed))
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != ResultRejected {
		t.Fatalf("queued result = %v, want rejected", result.Kind)
	}
	snapshot, _ = backend.Probe(context.Background())
	if snapshot.Targets[target].Scopes != core.ScopeInput {
		t.Fatal("rejected result changed physical state")
	}

	if err := backend.QueueOutcome(DomainTarget, QueuedOutcome{Kind: ResultUnknown, Mutate: true, ErrorCode: "timeout"}); err != nil {
		t.Fatal(err)
	}
	result, err = backend.Apply(context.Background(), targetPlan(target, changed))
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != ResultUnknown {
		t.Fatalf("queued result = %v, want unknown", result.Kind)
	}
	snapshot, _ = backend.Probe(context.Background())
	if snapshot.Targets[target].Scopes != core.ScopeForward {
		t.Fatal("unknown-after-dispatch fixture did not model an ambiguous applied mutation")
	}
}

func TestBackendModelsInfrastructureAndPolicyPostconditions(t *testing.T) {
	backend := NewBackend()
	infrastructure := infrastructurePlan(1)
	policy := policyPlan(1)
	if _, err := backend.Apply(context.Background(), infrastructure); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Apply(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	snapshot, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Infrastructure == nil || snapshot.Infrastructure.Digest != infrastructure.DesiredInfrastructure.Digest {
		t.Fatalf("infrastructure postcondition missing: %+v", snapshot.Infrastructure)
	}
	if snapshot.Policy == nil || snapshot.Policy.RelationDigest != policy.DesiredPolicy.RelationDigest {
		t.Fatalf("policy postcondition missing: %+v", snapshot.Policy)
	}
}

func TestBackendInvalidPlanIsStableRejectedWithoutMutation(t *testing.T) {
	backend := NewBackend()
	plan := targetPlan(netip.MustParsePrefix("192.0.2.4/32"), targetIntent(netip.MustParsePrefix("192.0.2.4/32"), 1))
	plan.Digest = "tampered"
	result, err := backend.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != ResultRejected || result.ErrorCode != "invalid_plan" {
		t.Fatalf("invalid plan result = %+v", result)
	}
	_, applies := backend.Counts()
	if applies != 0 {
		t.Fatalf("invalid plan reached mutation path %d times", applies)
	}
}

func TestBackendSnapshotsAreIndependent(t *testing.T) {
	backend := NewBackend()
	target := netip.MustParsePrefix("192.0.2.4/32")
	observed := core.PhysicalTargetObserved{
		CanonicalTarget: target,
		BanMembership:   core.ObservedMembershipPresent,
		PolicyCoverage:  core.ObservedPolicyNone,
		Scopes:          core.ScopeInput,
		AddressFamily:   core.AddressFamilyIPv4,
	}
	if err := backend.SetPhysicalTarget(observed); err != nil {
		t.Fatal(err)
	}
	snapshot, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	delete(snapshot.Targets, target)
	next, _ := backend.Probe(context.Background())
	if _, exists := next.Targets[target]; !exists {
		t.Fatal("caller mutated backend through a snapshot map")
	}
}

func targetIntent(target netip.Prefix, generation core.TargetEnforcementGeneration) core.NormalizedTargetEnforcementIntent {
	return core.NormalizedTargetEnforcementIntent{
		NodeID:          testNodeID,
		CanonicalTarget: target,
		BanMembership:   core.BanPresent,
		Scopes:          core.ScopeInput,
		AddressFamily:   core.AddressFamilyIPv4,
		Generation:      generation,
	}
}

func targetPlan(target netip.Prefix, intent core.NormalizedTargetEnforcementIntent) OperationPlan {
	plan := OperationPlan{
		Domain:                   DomainTarget,
		Target:                   target,
		DesiredTarget:            intent,
		ExpectedTargetGeneration: intent.Generation,
	}
	plan.Digest = PlanDigest(plan)
	return plan
}

func infrastructurePlan(revision core.InfrastructureRevision) OperationPlan {
	plan := OperationPlan{
		Domain:                         DomainInfrastructure,
		DesiredInfrastructure:          core.ManagedInfrastructureIntent{Backend: "fake", OwnerVersion: "v1", Digest: "infra-v1"},
		ExpectedInfrastructureRevision: revision,
	}
	plan.Digest = PlanDigest(plan)
	return plan
}

func policyPlan(revision core.PolicyRevision) OperationPlan {
	plan := OperationPlan{
		Domain:                 DomainPolicy,
		DesiredPolicy:          core.ManagedPolicyIntent{RelationDigest: "policy-v1"},
		ExpectedPolicyRevision: revision,
	}
	plan.Digest = PlanDigest(plan)
	return plan
}

const testNodeID core.NodeID = "00112233445566778899aabbccddeeff"
