//go:build linux

package reconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/firewall/nftables"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestIPCBackendInfrastructureBindsFixedDesiredAndObservedLayout(t *testing.T) {
	revision, desired := nftables.FixedDesiredInfrastructure()
	confirmed, err := ipc.NewApplyManagedPlanConfirmedResponse(ipc.DomainInfrastructure)
	if err != nil {
		t.Fatal(err)
	}
	validSnapshot := fixedInfrastructureSnapshotForIPCBackend(t, desired.Digest)
	for _, test := range []struct {
		name              string
		plan              OperationPlan
		snapshot          firewall.ManagedSnapshot
		wantCode          string
		wantMutationCalls int
		nonNative         bool
	}{
		{
			name:              "fixed layout",
			plan:              fixedInfrastructurePlan(revision, desired, validSnapshot.Digest()),
			snapshot:          validSnapshot,
			wantMutationCalls: 1,
		},
		{
			name:     "revision drift",
			plan:     fixedInfrastructurePlan(revision+1, desired, validSnapshot.Digest()),
			snapshot: validSnapshot, wantCode: "invalid_plan",
		},
		{
			name: "desired backend drift",
			plan: fixedInfrastructurePlan(revision, core.ManagedInfrastructureIntent{
				Backend: "fake", OwnerVersion: desired.OwnerVersion, Digest: desired.Digest,
			}, validSnapshot.Digest()),
			snapshot: validSnapshot, wantCode: "invalid_plan",
		},
		{
			name: "desired owner drift",
			plan: fixedInfrastructurePlan(revision, core.ManagedInfrastructureIntent{
				Backend: desired.Backend, OwnerVersion: "guard/v2", Digest: desired.Digest,
			}, validSnapshot.Digest()),
			snapshot: validSnapshot, wantCode: "invalid_plan",
		},
		{
			name: "desired digest drift",
			plan: fixedInfrastructurePlan(revision, core.ManagedInfrastructureIntent{
				Backend: desired.Backend, OwnerVersion: desired.OwnerVersion, Digest: strings.Repeat("b", 64),
			}, validSnapshot.Digest()),
			snapshot: validSnapshot, wantCode: "invalid_plan",
		},
		{
			name:     "observed layout digest drift",
			plan:     fixedInfrastructurePlan(revision, desired, validSnapshot.Digest()),
			snapshot: fixedInfrastructureSnapshotForIPCBackend(t, strings.Repeat("a", 64)),
			wantCode: "ownership_conflict",
		},
		{
			name:              "absent layout with non-native capability",
			plan:              fixedInfrastructurePlan(revision, desired, absentInfrastructureSnapshotForIPCBackend(t).Digest()),
			snapshot:          absentInfrastructureSnapshotForIPCBackend(t),
			wantCode:          "not_ready",
			wantMutationCalls: 0,
			nonNative:         true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capabilities := managedCapabilitiesForIPCBackend(t)
			if test.nonNative {
				capabilities = nonNativeCapabilitiesForIPCBackend(t)
			}
			transport := &ipcTransportStub{
				capabilities: capabilities, snapshot: test.snapshot, response: confirmed,
			}
			result, err := newIPCBackend(transport).Apply(context.Background(), test.plan)
			if err != nil || result.Kind != resultKind(test.wantCode) || result.ErrorCode != test.wantCode {
				t.Fatalf("Apply() result=%#v err=%v", result, err)
			}
			if transport.capabilityCalls != 1 || transport.snapshotCalls != 1 || transport.mutationCalls != test.wantMutationCalls {
				t.Fatalf("calls=%d/%d/%d, want 1/1/%d", transport.capabilityCalls, transport.snapshotCalls, transport.mutationCalls, test.wantMutationCalls)
			}
		})
	}
}

func fixedInfrastructurePlan(revision core.InfrastructureRevision, desired core.ManagedInfrastructureIntent, basis string) OperationPlan {
	plan := OperationPlan{
		Domain: DomainInfrastructure, DesiredInfrastructure: desired,
		ExpectedInfrastructureRevision: revision, BasisSnapshotDigest: basis,
	}
	plan.Digest = PlanDigest(plan)
	return plan
}

func fixedInfrastructureSnapshotForIPCBackend(t *testing.T, digest string) firewall.ManagedSnapshot {
	t.Helper()
	infrastructure, err := firewall.NewInfrastructureObservation(firewall.InfrastructureObservationSpec{
		Backend: firewall.BackendKindNftablesNative, OwnerVersion: firewall.ManagedOwnerVersionV1,
		SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1, Digest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{Infrastructure: &infrastructure})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func absentInfrastructureSnapshotForIPCBackend(t *testing.T) firewall.ManagedSnapshot {
	t.Helper()
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func nonNativeCapabilitiesForIPCBackend(t *testing.T) firewall.FirewallCapabilities {
	t.Helper()
	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend: firewall.BackendKindIptablesNFT, ToolVersion: "iptables-nft-test", IPv4: true, IPv6: true,
		CIDR: true, HostInput: true, Forward: true, OwnershipProven: true, MutationReady: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return capabilities
}

func resultKind(code string) ResultKind {
	if code == "" {
		return ResultConfirmed
	}
	return ResultRejected
}
