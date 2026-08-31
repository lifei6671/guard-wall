package core

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestObservedFirewallUpdateValidation(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	nodeID := NodeID("00112233445566778899aabbccddeeff")
	target := netip.MustParsePrefix("192.0.2.8/32")
	tests := []struct {
		name    string
		update  ObservedFirewallUpdate
		wantErr string
	}{
		{name: "complete present", update: ObservedFirewallUpdate{
			NodeID: nodeID,
			Infrastructure: &InfrastructureObservedState{
				Presence: ObservedPresencePresent, ObservedAt: now,
				Backend: "fake", OwnerVersion: "v1", Digest: "infra", ConfirmedRevision: 2,
			},
			Policy: &PolicyObservedState{
				Presence: ObservedPresencePresent, ObservedAt: now,
				RelationDigest: "policy", ConfirmedRevision: 3,
			},
			Targets: []TargetObservedState{{
				PhysicalTargetObserved: PhysicalTargetObserved{
					CanonicalTarget: target, ObservedAt: now, Backend: "fake",
					BanMembership: ObservedMembershipPresent, PolicyCoverage: ObservedPolicyNone,
					TimeoutMode: TimeoutNone, Scopes: ScopeInput,
					AddressFamily: AddressFamilyIPv4, OwnerVersion: "v1",
				},
				ConfirmedGeneration: 4,
			}},
		}},
		{name: "unknown requires error", update: ObservedFirewallUpdate{
			NodeID: nodeID,
			Infrastructure: &InfrastructureObservedState{
				Presence: ObservedPresenceUnknown, ObservedAt: now,
			},
		}, wantErr: "requires an error code"},
		{name: "duplicate target", update: ObservedFirewallUpdate{
			NodeID: nodeID,
			Targets: []TargetObservedState{
				{PhysicalTargetObserved: PhysicalTargetObserved{CanonicalTarget: target, ObservedAt: now, BanMembership: ObservedMembershipAbsent}},
				{PhysicalTargetObserved: PhysicalTargetObserved{CanonicalTarget: target, ObservedAt: now, BanMembership: ObservedMembershipAbsent}},
			},
		}, wantErr: "duplicated"},
		{name: "present target family must match", update: ObservedFirewallUpdate{
			NodeID: nodeID,
			Targets: []TargetObservedState{{PhysicalTargetObserved: PhysicalTargetObserved{
				CanonicalTarget: target, ObservedAt: now, Backend: "fake",
				BanMembership: ObservedMembershipPresent, PolicyCoverage: ObservedPolicyNone,
				Scopes: ScopeInput, AddressFamily: AddressFamilyIPv6, OwnerVersion: "v1",
			}}},
		}, wantErr: "address family"},
		{name: "empty update", update: ObservedFirewallUpdate{NodeID: nodeID}, wantErr: "at least one"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.update.Validate()
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
