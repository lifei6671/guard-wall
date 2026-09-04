package core

import (
	"net/netip"
	"testing"
	"time"
)

func TestSourcePositionIsClosedUnion(t *testing.T) {
	tests := []struct {
		name    string
		build   func() (SourcePosition, error)
		wantErr bool
		isFile  bool
	}{
		{
			name: "file",
			build: func() (SourcePosition, error) {
				return NewFilePosition(FilePosition{Generation: "00112233445566778899aabbccddeeff", StartOffset: 10, EndOffset: 20})
			},
			isFile: true,
		},
		{
			name: "invalid file range",
			build: func() (SourcePosition, error) {
				return NewFilePosition(FilePosition{Generation: "00112233445566778899aabbccddeeff", StartOffset: 20, EndOffset: 10})
			},
			wantErr: true,
		},
		{
			name: "journald",
			build: func() (SourcePosition, error) {
				return NewJournaldPosition("s=cursor")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			position, err := test.build()
			if (err != nil) != test.wantErr {
				t.Fatalf("build error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if !position.Valid() {
				t.Fatal("position is not valid")
			}
			_, isFile := position.File()
			if isFile != test.isFile {
				t.Fatalf("file variant = %v, want %v", isFile, test.isFile)
			}
		})
	}
}

func TestDecisionValidate(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	rule := RuleID("rule-1")
	reason := EndReasonExpired
	tests := []struct {
		name     string
		decision Decision
		wantErr  bool
	}{
		{
			name: "active automatic",
			decision: Decision{
				ID: "decision-1", NodeID: "00112233445566778899aabbccddeeff", Source: DecisionSourceAutomatic,
				RuleID: &rule, CanonicalTarget: netip.MustParsePrefix("192.0.2.1/32"),
				CreatedAt: now, UpdatedAt: now, LastTriggeredAt: now, State: DecisionActive,
			},
		},
		{
			name: "automatic without rule",
			decision: Decision{
				ID: "decision-2", NodeID: "00112233445566778899aabbccddeeff", Source: DecisionSourceAutomatic,
				CanonicalTarget: netip.MustParsePrefix("192.0.2.1/32"), State: DecisionActive,
				CreatedAt: now, UpdatedAt: now, LastTriggeredAt: now,
			},
			wantErr: true,
		},
		{
			name: "expired",
			decision: Decision{
				ID: "decision-3", NodeID: "00112233445566778899aabbccddeeff", Source: DecisionSourceManual,
				CanonicalTarget: netip.MustParsePrefix("2001:db8::/64"), State: DecisionExpired,
				CreatedAt: now, UpdatedAt: now, LastTriggeredAt: now,
				ExpiresAt: &now, EndReason: &reason, EndedAt: &now,
			},
		},
		{
			name: "expired without expiry",
			decision: Decision{
				ID: "decision-4", NodeID: "00112233445566778899aabbccddeeff", Source: DecisionSourceManual,
				CanonicalTarget: netip.MustParsePrefix("2001:db8::/64"), State: DecisionExpired,
				CreatedAt: now, UpdatedAt: now, LastTriggeredAt: now,
				EndReason: &reason, EndedAt: &now,
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.decision.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestDeliveryAndReceiptValidate(t *testing.T) {
	observed := time.Unix(1_700_000_000, 0).UTC()
	position, err := NewFilePosition(FilePosition{
		Generation:  "00112233445566778899aabbccddeeff",
		StartOffset: 10,
		EndOffset:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveryID, err := FileDeliveryID("source-1", FilePosition{
		Generation:  "00112233445566778899aabbccddeeff",
		StartOffset: 10,
		EndOffset:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	delivery := Delivery{
		ID:       deliveryID,
		Sequence: 1,
		Record: RawRecord{
			SourceID:   "source-1",
			ObservedAt: observed,
			Position:   position,
		},
	}
	if err := delivery.Validate(); err != nil {
		t.Fatalf("valid delivery rejected: %v", err)
	}
	otherID, err := JournaldDeliveryID("source-1", "other")
	if err != nil {
		t.Fatal(err)
	}
	delivery.ID = otherID
	if err := delivery.Validate(); err == nil {
		t.Fatal("delivery id bound to another position was accepted")
	}
	delivery.ID = deliveryID

	receipt := ProcessingReceipt{
		DeliveryID: deliveryID,
		SourceID:   "source-1",
		Position:   position,
		Kind:       ReceiptSuccess,
		Committed:  observed,
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	receipt.Kind = ReceiptRecordPermanent
	if err := receipt.Validate(); err == nil {
		t.Fatal("permanent receipt without failure was accepted")
	}
	receipt.Kind = ReceiptSuccess
	receipt.Failure = &PermanentFailure{Stage: "parser", Code: "bad", Action: "drop", OccurredAt: observed}
	if err := receipt.Validate(); err == nil {
		t.Fatal("success receipt with failure was accepted")
	}
	receipt.Kind = ReceiptRecordPermanent
	if err := receipt.Validate(); err == nil {
		t.Fatal("permanent receipt without sanitized error was accepted")
	}
	receipt.Failure.SanitizedError = "invalid record"
	if err := receipt.Validate(); err != nil {
		t.Fatalf("valid permanent receipt rejected: %v", err)
	}
}

func TestProjectionAndIntentValidate(t *testing.T) {
	target := netip.MustParsePrefix("192.0.2.4/32")
	projection := DesiredBanProjection{
		NodeID:          "00112233445566778899aabbccddeeff",
		CanonicalTarget: target,
		State:           BanProjectionPresent,
		ActiveCount:     1,
		Revision:        1,
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("valid projection rejected: %v", err)
	}
	projection.ActiveCount = 0
	if err := projection.Validate(); err == nil {
		t.Fatal("present projection without decisions was accepted")
	}

	intent := NormalizedTargetEnforcementIntent{
		NodeID:          "00112233445566778899aabbccddeeff",
		CanonicalTarget: target,
		BanMembership:   BanPresent,
		TimeoutMode:     TimeoutNone,
		Scopes:          ScopeInput,
		AddressFamily:   AddressFamilyIPv4,
		PolicyCoverage:  PolicyCoverageNone,
		Generation:      1,
	}
	if err := intent.Validate(); err != nil {
		t.Fatalf("valid intent rejected: %v", err)
	}
	intent.TimeoutMode = TimeoutNative
	if err := intent.Validate(); err == nil {
		t.Fatal("native timeout without expiry was accepted")
	}
}

func TestDesiredFirewallSnapshotConstructsStableClone(t *testing.T) {
	nodeID := NodeID("00112233445566778899aabbccddeeff")
	targetA := netip.MustParsePrefix("192.0.2.2/32")
	targetB := netip.MustParsePrefix("192.0.2.1/32")
	snapshot := DesiredFirewallSnapshot{
		SnapshotRevision:       1,
		InfrastructureRevision: 1,
		PolicyRevision:         1,
		Infrastructure: ManagedInfrastructureIntent{
			Backend: "fake", OwnerVersion: "v1", Digest: "infra-digest",
		},
		Policy: ManagedPolicyIntent{RelationDigest: "policy-digest"},
		Targets: []NormalizedTargetEnforcementIntent{
			validIntent(nodeID, targetA, 1),
			validIntent(nodeID, targetB, 1),
		},
	}
	prepared, err := NewDesiredFirewallSnapshot(snapshot)
	if err != nil {
		t.Fatalf("NewDesiredFirewallSnapshot(): %v", err)
	}
	if prepared.Targets[0].CanonicalTarget != targetB {
		t.Fatalf("targets not sorted: first = %s", prepared.Targets[0].CanonicalTarget)
	}
	snapshot.Targets[0].Generation = 9
	if prepared.Targets[1].Generation != 1 {
		t.Fatal("prepared snapshot aliases caller target slice")
	}
}

func TestManagedPolicyIntentBindsCanonicalPayload(t *testing.T) {
	allowlist := []netip.Prefix{netip.MustParsePrefix("2001:db8::/64"), netip.MustParsePrefix("192.0.2.0/24")}
	protectedTargets := []netip.Prefix{netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("127.0.0.0/8")}
	intent, err := NewManagedPolicyIntent(allowlist, protectedTargets)
	if err != nil {
		t.Fatalf("NewManagedPolicyIntent(): %v", err)
	}
	if err := intent.ValidateComplete(); err != nil {
		t.Fatalf("ValidateComplete(): %v", err)
	}
	if intent.Allowlist[0] != netip.MustParsePrefix("192.0.2.0/24") {
		t.Fatalf("allowlist was not canonicalized: %#v", intent.Allowlist)
	}
	allowlist[0] = netip.MustParsePrefix("2001:db8:1::/64")
	if intent.Allowlist[1] != netip.MustParsePrefix("2001:db8::/64") {
		t.Fatal("managed policy aliases caller allowlist")
	}
	intent.Allowlist[0] = netip.MustParsePrefix("198.51.100.0/24")
	if err := intent.ValidateComplete(); err == nil {
		t.Fatal("payload/digest mismatch was accepted")
	}
}

func TestManagedPolicyIntentRejectsIncompleteProtectedTargets(t *testing.T) {
	_, err := NewManagedPolicyIntent(nil, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	if err == nil {
		t.Fatal("policy without IPv6 loopback protected target was accepted")
	}
}

func TestManagedPolicyIntentAllowsAnEmptyAllowlist(t *testing.T) {
	intent, err := NewManagedPolicyIntent(nil, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	})
	if err != nil {
		t.Fatalf("NewManagedPolicyIntent(): %v", err)
	}
	if err := intent.ValidateComplete(); err != nil {
		t.Fatalf("ValidateComplete(): %v", err)
	}
}

func TestDesiredFirewallSnapshotRejectsPolicyPayloadDigestMismatch(t *testing.T) {
	policy, err := NewManagedPolicyIntent(nil, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy.RelationDigest = "not-the-payload-digest"
	_, err = NewDesiredFirewallSnapshot(DesiredFirewallSnapshot{
		SnapshotRevision:       1,
		InfrastructureRevision: 1,
		PolicyRevision:         1,
		Infrastructure: ManagedInfrastructureIntent{
			Backend: "nftables", OwnerVersion: "v1", Digest: "infra-digest",
		},
		Policy: policy,
	})
	if err == nil {
		t.Fatal("snapshot accepted a policy payload with an unrelated digest")
	}
}

func TestDesiredFirewallSnapshotRejectsIncompletePolicyPayload(t *testing.T) {
	_, err := NewDesiredFirewallSnapshot(DesiredFirewallSnapshot{
		SnapshotRevision:       1,
		InfrastructureRevision: 1,
		PolicyRevision:         1,
		Infrastructure: ManagedInfrastructureIntent{
			Backend: "nftables", OwnerVersion: "v1", Digest: "infra-digest",
		},
		Policy: ManagedPolicyIntent{
			RelationDigest: "unbound", Allowlist: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		},
	})
	if err == nil {
		t.Fatal("snapshot accepted an allowlist without protected targets")
	}
}

func validIntent(nodeID NodeID, target netip.Prefix, generation TargetEnforcementGeneration) NormalizedTargetEnforcementIntent {
	family := AddressFamilyIPv6
	if target.Addr().Is4() {
		family = AddressFamilyIPv4
	}
	return NormalizedTargetEnforcementIntent{
		NodeID: nodeID, CanonicalTarget: target, BanMembership: BanPresent,
		TimeoutMode: TimeoutNone, Scopes: ScopeInput, AddressFamily: family,
		PolicyCoverage: PolicyCoverageFull, PolicyRelationDigest: "policy-digest",
		BackendAttributesDigest: "backend-digest", Generation: generation,
	}
}
