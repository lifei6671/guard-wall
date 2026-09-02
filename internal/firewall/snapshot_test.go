package firewall_test

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

const (
	digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	digestC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	digestD = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestManagedSnapshotCanonicalizesAndOwnsInputs(t *testing.T) {
	expiry := int64(1_700_000_000_000_000)
	scopes := []firewall.ManagedScope{firewall.ManagedScopeForward, firewall.ManagedScopeInput}
	targetOne := mustTargetObservation(t, firewall.TargetObservationSpec{
		Target:                  netip.MustParsePrefix("203.0.113.7/32"),
		TimeoutMode:             firewall.ManagedTimeoutNative,
		EffectiveUntilUnixMicro: &expiry,
		Scopes:                  scopes,
	})
	targetTwo := mustTargetObservation(t, firewall.TargetObservationSpec{
		Target:      netip.MustParsePrefix("192.0.2.0/24"),
		TimeoutMode: firewall.ManagedTimeoutNone,
		Scopes:      []firewall.ManagedScope{firewall.ManagedScopeForward},
	})
	infrastructure := mustInfrastructureObservation(t, firewall.InfrastructureObservationSpec{
		Backend:       firewall.BackendKindNftablesNative,
		OwnerVersion:  firewall.ManagedOwnerVersionV1,
		SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1,
		Digest:        digestA,
	})
	policy := mustPolicyObservation(t, firewall.PolicyObservationSpec{RelationDigest: digestB})
	targets := []firewall.TargetObservation{targetOne, targetTwo}
	state := mustManagedState(t, firewall.ManagedStateSpec{
		Infrastructure: &infrastructure,
		Policy:         &policy,
		Targets:        targets,
	})
	foreign := mustForeignContext(t, firewall.ForeignContextSpec{Digest: digestC})
	snapshot := mustManagedSnapshot(t, firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})

	// Mutate every caller-owned reference after construction.
	expiry = 1
	scopes[0] = firewall.ManagedScope("secret")
	targets[0] = firewall.TargetObservation{}
	infrastructure = firewall.InfrastructureObservation{}
	policy = firewall.PolicyObservation{}

	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Validate() after input mutation: %v", err)
	}
	gotInfrastructure, ok := snapshot.ManagedState().Infrastructure()
	if !ok || gotInfrastructure.Backend() != firewall.BackendKindNftablesNative ||
		gotInfrastructure.OwnerVersion() != firewall.ManagedOwnerVersionV1 ||
		gotInfrastructure.SchemaVersion() != firewall.ManagedInfrastructureSchemaVersionV1 ||
		gotInfrastructure.Digest() != digestA {
		t.Fatalf("Infrastructure() = %#v, %v", gotInfrastructure, ok)
	}
	gotPolicy, ok := snapshot.ManagedState().Policy()
	if !ok || gotPolicy.RelationDigest() != digestB {
		t.Fatalf("Policy() digest = %q, %v", gotPolicy.RelationDigest(), ok)
	}
	gotTargets := snapshot.ManagedState().Targets()
	if len(gotTargets) != 2 || gotTargets[0].Target().String() != "192.0.2.0/24" ||
		gotTargets[1].Target().String() != "203.0.113.7/32" {
		t.Fatalf("Targets() = %#v", gotTargets)
	}
	gotExpiry, ok := gotTargets[1].EffectiveUntilUnixMicro()
	if !ok || gotExpiry != 1_700_000_000_000_000 {
		t.Fatalf("EffectiveUntilUnixMicro() = %d, %v", gotExpiry, ok)
	}
	assertScopes(t, gotTargets[1], firewall.ManagedScopeInput, firewall.ManagedScopeForward)
	if snapshot.ForeignContext().Digest() != digestC {
		t.Fatalf("ForeignContext digest = %q", snapshot.ForeignContext().Digest())
	}
	assertLowerDigest(t, snapshot.Digest())

	// Mutating detached getters must not mutate the authority.
	gotTargets[0] = firewall.TargetObservation{}
	returnedScopes := gotTargets[1].Scopes()
	returnedScopes[0] = firewall.ManagedScope("changed")
	if got := snapshot.ManagedState().Targets(); len(got) != 2 || got[0].Target().String() != "192.0.2.0/24" {
		t.Fatalf("snapshot mutated through Targets getter: %#v", got)
	}
	assertScopes(t, snapshot.ManagedState().Targets()[1], firewall.ManagedScopeInput, firewall.ManagedScopeForward)
}

func TestManagedSnapshotDigestIsCanonicalAndFieldSensitive(t *testing.T) {
	first := snapshotFixture(t, snapshotFixtureSpec{})
	if got, want := first.Digest(), "43f0b2c3e42b92c35760f486c378c39f9203364b8cbd02ab2fe23c19d5f146a0"; got != want {
		t.Fatalf("canonical fixture digest = %q, want %q", got, want)
	}
	reversed := snapshotFixture(t, snapshotFixtureSpec{reverseTargets: true, reverseScopes: true})
	if first.Digest() != reversed.Digest() {
		t.Fatalf("canonical equivalents differ: %s != %s", first.Digest(), reversed.Digest())
	}

	tests := []struct {
		name string
		spec snapshotFixtureSpec
	}{
		{name: "backend", spec: snapshotFixtureSpec{backend: firewall.BackendKindIptablesNFT}},
		{name: "infrastructure digest", spec: snapshotFixtureSpec{infrastructureDigest: digestD}},
		{name: "infrastructure presence", spec: snapshotFixtureSpec{withoutInfrastructure: true}},
		{name: "policy digest", spec: snapshotFixtureSpec{policyDigest: digestD}},
		{name: "policy presence", spec: snapshotFixtureSpec{withoutPolicy: true}},
		{name: "foreign digest", spec: snapshotFixtureSpec{foreignDigest: digestD}},
		{name: "target prefix", spec: snapshotFixtureSpec{firstTarget: "198.51.100.1/32"}},
		{name: "target presence", spec: snapshotFixtureSpec{withoutSecondTarget: true}},
		{name: "timeout mode and expiry", spec: snapshotFixtureSpec{firstNativeExpiry: 42}},
		{name: "scope", spec: snapshotFixtureSpec{firstForwardOnly: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := snapshotFixture(t, test.spec)
			if changed.Digest() == first.Digest() {
				t.Fatalf("digest did not cover %s", test.name)
			}
			assertLowerDigest(t, changed.Digest())
		})
	}
}

func TestManagedStateAllowsIndependentPartialObservations(t *testing.T) {
	infrastructure := validInfrastructure(t)
	policy := mustPolicyObservation(t, firewall.PolicyObservationSpec{RelationDigest: digestB})
	target := mustTargetObservation(t, firewall.TargetObservationSpec{
		Target:      netip.MustParsePrefix("192.0.2.1/32"),
		TimeoutMode: firewall.ManagedTimeoutNone,
		Scopes:      []firewall.ManagedScope{firewall.ManagedScopeInput},
	})
	tests := []struct {
		name string
		spec firewall.ManagedStateSpec
	}{
		{name: "empty", spec: firewall.ManagedStateSpec{}},
		{name: "infrastructure only", spec: firewall.ManagedStateSpec{Infrastructure: &infrastructure}},
		{name: "policy only", spec: firewall.ManagedStateSpec{Policy: &policy}},
		{name: "target only", spec: firewall.ManagedStateSpec{Targets: []firewall.TargetObservation{target}}},
		{name: "policy and target without infrastructure", spec: firewall.ManagedStateSpec{Policy: &policy, Targets: []firewall.TargetObservation{target}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := firewall.NewManagedState(test.spec)
			if err != nil {
				t.Fatalf("NewManagedState() error = %v", err)
			}
			if err := state.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestInfrastructureObservationRejectsInvalidSpecs(t *testing.T) {
	valid := firewall.InfrastructureObservationSpec{
		Backend:       firewall.BackendKindNftablesNative,
		OwnerVersion:  firewall.ManagedOwnerVersionV1,
		SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1,
		Digest:        digestA,
	}
	tests := []struct {
		name   string
		mutate func(*firewall.InfrastructureObservationSpec)
	}{
		{name: "unknown backend", mutate: func(spec *firewall.InfrastructureObservationSpec) { spec.Backend = "shell" }},
		{name: "wrong owner", mutate: func(spec *firewall.InfrastructureObservationSpec) { spec.OwnerVersion = "guard/v2-secret" }},
		{name: "wrong schema", mutate: func(spec *firewall.InfrastructureObservationSpec) { spec.SchemaVersion = 2 }},
		{name: "short digest", mutate: func(spec *firewall.InfrastructureObservationSpec) { spec.Digest = digestA[:63] }},
		{name: "uppercase digest", mutate: func(spec *firewall.InfrastructureObservationSpec) { spec.Digest = strings.ToUpper(digestA) }},
		{name: "non hex digest", mutate: func(spec *firewall.InfrastructureObservationSpec) { spec.Digest = strings.Repeat("z", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := valid
			test.mutate(&spec)
			_, err := firewall.NewInfrastructureObservation(spec)
			assertRedactedInvalidError(t, err)
		})
	}
	if err := (firewall.InfrastructureObservation{}).Validate(); err == nil {
		t.Fatal("zero InfrastructureObservation validated")
	}
}

func TestDigestOnlyObservationsRejectInvalidDigests(t *testing.T) {
	tests := []string{"", digestA[:63], digestA + "a", strings.ToUpper(digestA), strings.Repeat("g", 64)}
	for _, digest := range tests {
		t.Run(digest, func(t *testing.T) {
			_, policyErr := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{RelationDigest: digest})
			assertRedactedInvalidError(t, policyErr)
			_, foreignErr := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: digest})
			assertRedactedInvalidError(t, foreignErr)
		})
	}
	if err := (firewall.PolicyObservation{}).Validate(); err == nil {
		t.Fatal("zero PolicyObservation validated")
	}
	if err := (firewall.ForeignContext{}).Validate(); err == nil {
		t.Fatal("zero ForeignContext validated")
	}
}

func TestTargetObservationRejectsInvalidSpecs(t *testing.T) {
	positive := int64(100)
	zero := int64(0)
	negative := int64(-1)
	tests := []struct {
		name string
		spec firewall.TargetObservationSpec
	}{
		{name: "invalid prefix", spec: firewall.TargetObservationSpec{TimeoutMode: firewall.ManagedTimeoutNone, Scopes: []firewall.ManagedScope{firewall.ManagedScopeInput}}},
		{name: "noncanonical prefix", spec: targetSpec("192.0.2.1/24", firewall.ManagedTimeoutNone, nil, firewall.ManagedScopeInput)},
		{name: "IPv4 loopback overlap", spec: targetSpec("127.0.0.0/7", firewall.ManagedTimeoutNone, nil, firewall.ManagedScopeInput)},
		{name: "IPv6 loopback", spec: targetSpec("::1/128", firewall.ManagedTimeoutNone, nil, firewall.ManagedScopeInput)},
		{name: "unknown timeout", spec: targetSpec("192.0.2.1/32", "userspace", nil, firewall.ManagedScopeInput)},
		{name: "none with expiry", spec: targetSpec("192.0.2.1/32", firewall.ManagedTimeoutNone, &positive, firewall.ManagedScopeInput)},
		{name: "native without expiry", spec: targetSpec("192.0.2.1/32", firewall.ManagedTimeoutNative, nil, firewall.ManagedScopeInput)},
		{name: "native zero expiry", spec: targetSpec("192.0.2.1/32", firewall.ManagedTimeoutNative, &zero, firewall.ManagedScopeInput)},
		{name: "native negative expiry", spec: targetSpec("192.0.2.1/32", firewall.ManagedTimeoutNative, &negative, firewall.ManagedScopeInput)},
		{name: "empty scopes", spec: targetSpec("192.0.2.1/32", firewall.ManagedTimeoutNone, nil)},
		{name: "duplicate scope", spec: targetSpec("192.0.2.1/32", firewall.ManagedTimeoutNone, nil, firewall.ManagedScopeInput, firewall.ManagedScopeInput)},
		{name: "unknown scope", spec: targetSpec("192.0.2.1/32", firewall.ManagedTimeoutNone, nil, "output")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := firewall.NewTargetObservation(test.spec)
			assertRedactedInvalidError(t, err)
		})
	}
	if err := (firewall.TargetObservation{}).Validate(); err == nil {
		t.Fatal("zero TargetObservation validated")
	}
}

func TestManagedStateTargetLimitAndDuplicateBoundary(t *testing.T) {
	targets := make([]firewall.TargetObservation, firewall.MaxManagedSnapshotTargets)
	for index := range targets {
		address := netip.AddrFrom4([4]byte{10, byte(index >> 8), byte(index), 1})
		targets[index] = mustTargetObservation(t, firewall.TargetObservationSpec{
			Target:      netip.PrefixFrom(address, 32),
			TimeoutMode: firewall.ManagedTimeoutNone,
			Scopes:      []firewall.ManagedScope{firewall.ManagedScopeInput},
		})
	}
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{Targets: targets})
	if err != nil || len(state.Targets()) != firewall.MaxManagedSnapshotTargets {
		t.Fatalf("exact limit: len=%d error=%v", len(state.Targets()), err)
	}

	oneOver := append(append([]firewall.TargetObservation(nil), targets...), mustTargetObservation(t, firewall.TargetObservationSpec{
		Target:      netip.MustParsePrefix("198.51.100.1/32"),
		TimeoutMode: firewall.ManagedTimeoutNone,
		Scopes:      []firewall.ManagedScope{firewall.ManagedScopeInput},
	}))
	_, err = firewall.NewManagedState(firewall.ManagedStateSpec{Targets: oneOver})
	assertRedactedInvalidError(t, err)

	_, err = firewall.NewManagedState(firewall.ManagedStateSpec{Targets: []firewall.TargetObservation{targets[0], targets[0]}})
	assertRedactedInvalidError(t, err)
}

func TestManagedSnapshotRejectsZeroAuthorities(t *testing.T) {
	state := mustManagedState(t, firewall.ManagedStateSpec{})
	foreign := mustForeignContext(t, firewall.ForeignContextSpec{Digest: digestA})
	tests := []firewall.ManagedSnapshotSpec{
		{ManagedState: firewall.ManagedState{}, ForeignContext: foreign},
		{ManagedState: state, ForeignContext: firewall.ForeignContext{}},
	}
	for _, spec := range tests {
		_, err := firewall.NewManagedSnapshot(spec)
		assertRedactedInvalidError(t, err)
	}
	if err := (firewall.ManagedState{}).Validate(); err == nil {
		t.Fatal("zero ManagedState validated")
	}
	if err := (firewall.ManagedSnapshot{}).Validate(); err == nil {
		t.Fatal("zero ManagedSnapshot validated")
	}
}

func TestOwnershipConflictErrorIsTypedAndRedacted(t *testing.T) {
	err := firewall.NewOwnershipConflictError()
	if err == nil || err.Error() != "firewall ownership conflict" {
		t.Fatalf("NewOwnershipConflictError() = %v", err)
	}
	var conflict firewall.OwnershipConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("errors.As(%T) = false", err)
	}
	for _, forbidden := range []string{"table", "chain", "command", "secret", "/run/"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: %q", forbidden, err.Error())
		}
	}
}

type snapshotFixtureSpec struct {
	backend               firewall.BackendKind
	infrastructureDigest  string
	policyDigest          string
	foreignDigest         string
	firstTarget           string
	firstNativeExpiry     int64
	firstForwardOnly      bool
	withoutInfrastructure bool
	withoutPolicy         bool
	withoutSecondTarget   bool
	reverseTargets        bool
	reverseScopes         bool
}

func snapshotFixture(t *testing.T, spec snapshotFixtureSpec) firewall.ManagedSnapshot {
	t.Helper()
	if spec.backend == "" {
		spec.backend = firewall.BackendKindNftablesNative
	}
	if spec.infrastructureDigest == "" {
		spec.infrastructureDigest = digestA
	}
	if spec.policyDigest == "" {
		spec.policyDigest = digestB
	}
	if spec.foreignDigest == "" {
		spec.foreignDigest = digestC
	}
	if spec.firstTarget == "" {
		spec.firstTarget = "192.0.2.1/32"
	}
	infrastructure := mustInfrastructureObservation(t, firewall.InfrastructureObservationSpec{
		Backend:       spec.backend,
		OwnerVersion:  firewall.ManagedOwnerVersionV1,
		SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1,
		Digest:        spec.infrastructureDigest,
	})
	policy := mustPolicyObservation(t, firewall.PolicyObservationSpec{RelationDigest: spec.policyDigest})
	stateSpec := firewall.ManagedStateSpec{}
	if !spec.withoutInfrastructure {
		stateSpec.Infrastructure = &infrastructure
	}
	if !spec.withoutPolicy {
		stateSpec.Policy = &policy
	}
	firstScopes := []firewall.ManagedScope{firewall.ManagedScopeInput, firewall.ManagedScopeForward}
	if spec.firstForwardOnly {
		firstScopes = []firewall.ManagedScope{firewall.ManagedScopeForward}
	} else if spec.reverseScopes {
		firstScopes = []firewall.ManagedScope{firewall.ManagedScopeForward, firewall.ManagedScopeInput}
	}
	firstTargetSpec := firewall.TargetObservationSpec{
		Target:      netip.MustParsePrefix(spec.firstTarget),
		TimeoutMode: firewall.ManagedTimeoutNone,
		Scopes:      firstScopes,
	}
	if spec.firstNativeExpiry != 0 {
		firstTargetSpec.TimeoutMode = firewall.ManagedTimeoutNative
		firstTargetSpec.EffectiveUntilUnixMicro = &spec.firstNativeExpiry
	}
	first := mustTargetObservation(t, firstTargetSpec)
	second := mustTargetObservation(t, targetSpec("203.0.113.0/24", firewall.ManagedTimeoutNone, nil, firewall.ManagedScopeForward))
	stateSpec.Targets = []firewall.TargetObservation{first}
	if !spec.withoutSecondTarget {
		stateSpec.Targets = append(stateSpec.Targets, second)
	}
	if spec.reverseTargets && len(stateSpec.Targets) == 2 {
		stateSpec.Targets[0], stateSpec.Targets[1] = stateSpec.Targets[1], stateSpec.Targets[0]
	}
	state := mustManagedState(t, stateSpec)
	foreign := mustForeignContext(t, firewall.ForeignContextSpec{Digest: spec.foreignDigest})
	return mustManagedSnapshot(t, firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
}

func targetSpec(prefix string, timeout firewall.ManagedTimeoutMode, expiry *int64, scopes ...firewall.ManagedScope) firewall.TargetObservationSpec {
	return firewall.TargetObservationSpec{
		Target:                  netip.MustParsePrefix(prefix),
		TimeoutMode:             timeout,
		EffectiveUntilUnixMicro: expiry,
		Scopes:                  scopes,
	}
}

func validInfrastructure(t *testing.T) firewall.InfrastructureObservation {
	t.Helper()
	return mustInfrastructureObservation(t, firewall.InfrastructureObservationSpec{
		Backend:       firewall.BackendKindNftablesNative,
		OwnerVersion:  firewall.ManagedOwnerVersionV1,
		SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1,
		Digest:        digestA,
	})
}

func mustInfrastructureObservation(t *testing.T, spec firewall.InfrastructureObservationSpec) firewall.InfrastructureObservation {
	t.Helper()
	observation, err := firewall.NewInfrastructureObservation(spec)
	if err != nil {
		t.Fatalf("NewInfrastructureObservation() error = %v", err)
	}
	return observation
}

func mustPolicyObservation(t *testing.T, spec firewall.PolicyObservationSpec) firewall.PolicyObservation {
	t.Helper()
	observation, err := firewall.NewPolicyObservation(spec)
	if err != nil {
		t.Fatalf("NewPolicyObservation() error = %v", err)
	}
	return observation
}

func mustTargetObservation(t *testing.T, spec firewall.TargetObservationSpec) firewall.TargetObservation {
	t.Helper()
	observation, err := firewall.NewTargetObservation(spec)
	if err != nil {
		t.Fatalf("NewTargetObservation() error = %v", err)
	}
	return observation
}

func mustManagedState(t *testing.T, spec firewall.ManagedStateSpec) firewall.ManagedState {
	t.Helper()
	state, err := firewall.NewManagedState(spec)
	if err != nil {
		t.Fatalf("NewManagedState() error = %v", err)
	}
	return state
}

func mustForeignContext(t *testing.T, spec firewall.ForeignContextSpec) firewall.ForeignContext {
	t.Helper()
	context, err := firewall.NewForeignContext(spec)
	if err != nil {
		t.Fatalf("NewForeignContext() error = %v", err)
	}
	return context
}

func mustManagedSnapshot(t *testing.T, spec firewall.ManagedSnapshotSpec) firewall.ManagedSnapshot {
	t.Helper()
	snapshot, err := firewall.NewManagedSnapshot(spec)
	if err != nil {
		t.Fatalf("NewManagedSnapshot() error = %v", err)
	}
	return snapshot
}

func assertScopes(t *testing.T, target firewall.TargetObservation, want ...firewall.ManagedScope) {
	t.Helper()
	got := target.Scopes()
	if len(got) != len(want) {
		t.Fatalf("Scopes() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("Scopes() = %v, want %v", got, want)
		}
	}
}

func assertLowerDigest(t *testing.T, digest string) {
	t.Helper()
	if len(digest) != 64 || digest != strings.ToLower(digest) {
		t.Fatalf("digest = %q, want lowercase 64-hex", digest)
	}
	for _, character := range digest {
		if !strings.ContainsRune("0123456789abcdef", character) {
			t.Fatalf("digest = %q, want lowercase 64-hex", digest)
		}
	}
}

func assertRedactedInvalidError(t *testing.T, err error) {
	t.Helper()
	if err == nil || err.Error() != "managed firewall snapshot is invalid" {
		t.Fatalf("error = %v, want stable redacted validation error", err)
	}
}
