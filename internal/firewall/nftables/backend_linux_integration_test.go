//go:build linux && integration && nftables

package nftables

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall"
)

// TestNftablesBackendIntegration executes only in a CI-provided isolated Linux
// network namespace. It exercises the real nft JSON shape and the complete
// private-table lifecycle without touching a host firewall.
func TestNftablesBackendIntegration(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GUARD_NFTABLES_INTEGRATION") != "1" || os.Getenv("GUARD_NFTABLES_ISOLATED") != "1" {
		t.Skip("requires the explicit isolated root nftables integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := nftFixture(ctx, nil, "--version"); err != nil {
		t.Skip("nft is unavailable in the isolated fixture")
	}
	if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("isolated fixture already contains inet guard")
	}

	createdForeign := false
	t.Cleanup(func() {
		if createdForeign {
			_ = nftFixture(context.Background(), []byte("delete table inet guard_nftables_fixture_foreign\n"), "--file", "-")
		}
		_ = nftFixture(context.Background(), []byte("delete table inet guard\n"), "--file", "-")
	})
	if err := nftFixture(ctx, []byte("add table inet guard_nftables_fixture_foreign\n"), "--file", "-"); err != nil {
		t.Fatalf("create isolated foreign table: %v", err)
	}
	createdForeign = true

	backend := NewBackend()
	capabilities, err := backend.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe(): %v", err)
	}
	if capabilities.Backend() != firewall.BackendKindNftablesNative || !capabilities.MutationReady() {
		t.Fatalf("Probe() returned non-ready native capabilities")
	}
	snapshot, err := backend.Snapshot(ctx)
	if err != nil {
		t.Fatalf("initial Snapshot(): %v", err)
	}
	if _, present := snapshot.ManagedState().Infrastructure(); present {
		t.Fatal("initial snapshot unexpectedly contains managed infrastructure")
	}
	foreignDigest := snapshot.ForeignContext().Digest()

	infrastructure, err := firewall.AuthorizeInfrastructureMutation(capabilities, snapshot, snapshot.Digest(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := nftFixture(ctx, []byte(infrastructureBatch()), "--check", "--file", "-"); err != nil {
		t.Fatalf("infrastructure batch syntax check: %v", err)
	}
	assertConfirmedWithReadback(t, backend, ctx, snapshot, backend.Apply(ctx, infrastructure), infrastructure.Digest())
	snapshot, err = backend.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := snapshot.ManagedState().Infrastructure(); !present {
		t.Fatal("infrastructure was not observed after confirmed Apply")
	}
	if snapshot.ForeignContext().Digest() != foreignDigest {
		t.Fatal("infrastructure Apply changed foreign snapshot context")
	}

	policy, err := firewall.AuthorizePolicyMutation(
		capabilities, snapshot, snapshot.Digest(), 2,
		[]netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("2001:db8::/64")},
		[]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
	)
	if err != nil {
		t.Fatal(err)
	}
	policyResult := backend.Apply(ctx, policy)
	if policyResult.Status() != firewall.MutationStatusConfirmed || policyResult.MutationDigest() != policy.Digest() {
		after, snapshotErr := backend.Snapshot(ctx)
		if snapshotErr != nil {
			t.Fatalf("policy result = %#v; readback error = %v", policyResult, snapshotErr)
		}
		observed, _ := after.ManagedState().Policy()
		t.Fatalf("policy result = %#v expected_digest=%s actual_digest=%s", policyResult, policyDigest(policyElements(policy)), observed.RelationDigest())
	}
	snapshot, err = backend.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := snapshot.ManagedState().Policy(); !present {
		t.Fatal("policy was not observed after confirmed Apply")
	}
	if snapshot.ForeignContext().Digest() != foreignDigest {
		t.Fatal("policy Apply changed foreign snapshot context")
	}

	target := netip.MustParsePrefix("203.0.113.7/32")
	targetPlan, err := firewall.AuthorizeTargetMutation(
		capabilities, snapshot, snapshot.Digest(), 3, target, firewall.TargetMembershipPresent,
		firewall.ManagedTimeoutNative, time.Now().Add(time.Minute).UnixMicro(), true,
		[]firewall.ManagedScope{firewall.ManagedScopeInput, firewall.ManagedScopeForward},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := nftFixture(ctx, []byte(targetBatch(targetPlan, snapshot.ManagedState().Targets())), "--check", "--file", "-"); err != nil {
		t.Fatalf("target batch syntax check: %v", err)
	}
	assertConfirmedWithReadback(t, backend, ctx, snapshot, backend.Apply(ctx, targetPlan), targetPlan.Digest())
	snapshot, err = backend.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertTarget(t, snapshot.ManagedState().Targets(), target)
	if snapshot.ForeignContext().Digest() != foreignDigest {
		t.Fatal("target Apply changed foreign snapshot context")
	}

	removal, alreadyAbsent, err := firewall.AuthorizeManagedRemoval(capabilities, snapshot, firewall.ManagedOwnerVersionV1)
	if err != nil || alreadyAbsent || removal == nil {
		t.Fatalf("AuthorizeManagedRemoval() = (%v, %v, %v)", removal, alreadyAbsent, err)
	}
	assertConfirmedWithReadback(t, backend, ctx, snapshot, backend.RemoveManagedInfrastructure(ctx, removal), removal.Digest())
	after, err := backend.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := after.ManagedState().Infrastructure(); present {
		t.Fatal("managed table survived successful cleanup")
	}
	if after.ForeignContext().Digest() != foreignDigest {
		t.Fatal("cleanup changed foreign snapshot context")
	}
	if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard_nftables_fixture_foreign"); err != nil {
		t.Fatalf("foreign table was not preserved: %v", err)
	}
}

func TestNftablesBackendIntegrationRepeatedInfrastructureApplyIsIdempotent(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GUARD_NFTABLES_INTEGRATION") != "1" || os.Getenv("GUARD_NFTABLES_ISOLATED") != "1" {
		t.Skip("requires the explicit isolated root nftables integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := nftFixture(ctx, nil, "--version"); err != nil {
		t.Skip("nft is unavailable in the isolated fixture")
	}
	if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("isolated fixture already contains inet guard")
	}

	const foreignTable = "guard_nftables_fixture_repeated_apply_foreign"
	createdForeign := false
	t.Cleanup(func() {
		if createdForeign {
			_ = nftFixture(context.Background(), []byte("delete table inet "+foreignTable+"\n"), "--file", "-")
		}
		_ = nftFixture(context.Background(), []byte("delete table inet guard\n"), "--file", "-")
	})
	if err := nftFixture(ctx, []byte("add table inet "+foreignTable+"\n"), "--file", "-"); err != nil {
		t.Fatalf("create isolated foreign table: %v", err)
	}
	createdForeign = true
	foreignBefore, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", foreignTable)
	if err != nil {
		t.Fatal(err)
	}

	backend := NewBackend()
	capabilities, err := backend.Probe(ctx)
	if err != nil {
		t.Fatalf("initial Probe(): %v", err)
	}
	snapshot, err := backend.Snapshot(ctx)
	if err != nil {
		t.Fatalf("initial Snapshot(): %v", err)
	}
	firstPlan, err := firewall.AuthorizeInfrastructureMutation(capabilities, snapshot, snapshot.Digest(), 1)
	if err != nil {
		t.Fatalf("AuthorizeInfrastructureMutation() first plan: %v", err)
	}
	assertConfirmedWithReadback(t, backend, ctx, snapshot, backend.Apply(ctx, firstPlan), firstPlan.Digest())

	guardBefore, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", "guard")
	if err != nil {
		t.Fatalf("read Guard table before repeated Apply: %v", err)
	}
	beforeRepeat, err := backend.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot() before repeated Apply: %v", err)
	}
	if _, present := beforeRepeat.ManagedState().Infrastructure(); !present {
		t.Fatal("first Apply did not create Guard infrastructure")
	}
	capabilitiesBeforeRepeat, err := backend.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe() before repeated Apply: %v", err)
	}
	if !reflect.DeepEqual(capabilitiesBeforeRepeat, capabilities) {
		t.Fatalf("Probe() capability changed before repeated Apply: got %#v want %#v", capabilitiesBeforeRepeat, capabilities)
	}
	secondPlan, err := firewall.AuthorizeInfrastructureMutation(
		capabilitiesBeforeRepeat, beforeRepeat, beforeRepeat.Digest(), 2,
	)
	if err != nil {
		t.Fatalf("AuthorizeInfrastructureMutation() repeated plan: %v", err)
	}
	result := backend.Apply(ctx, secondPlan)
	if err := result.Validate(); err != nil {
		t.Fatalf("repeated Apply() returned invalid result: %v", err)
	}
	if result.Status() != firewall.MutationStatusConfirmed || result.MutationDigest() != secondPlan.Digest() {
		t.Fatalf("repeated Apply() result = %#v, want confirmed result correlated to repeated plan", result)
	}

	guardAfter, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", "guard")
	if err != nil {
		t.Fatalf("read Guard table after repeated Apply: %v", err)
	}
	if strings.TrimSpace(string(guardAfter)) != strings.TrimSpace(string(guardBefore)) {
		t.Fatal("repeated Apply changed Guard table")
	}
	afterRepeat, err := backend.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot() after repeated Apply: %v", err)
	}
	if afterRepeat.Digest() != beforeRepeat.Digest() || afterRepeat.ForeignContext().Digest() != beforeRepeat.ForeignContext().Digest() {
		t.Fatal("repeated Apply changed managed or foreign snapshot state")
	}
	foreignAfter, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", foreignTable)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(foreignAfter)) != strings.TrimSpace(string(foreignBefore)) {
		t.Fatal("repeated Apply changed foreign table")
	}
}

func TestNftablesBackendIntegrationRejectsSameNameForeignTable(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GUARD_NFTABLES_INTEGRATION") != "1" || os.Getenv("GUARD_NFTABLES_ISOLATED") != "1" {
		t.Skip("requires the explicit isolated root nftables integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := nftFixture(ctx, nil, "--version"); err != nil {
		t.Skip("nft is unavailable in the isolated fixture")
	}
	if err := nftFixture(ctx, []byte("add table inet guard\n"), "--file", "-"); err != nil {
		t.Fatalf("create foreign same-name table: %v", err)
	}
	t.Cleanup(func() {
		_ = nftFixture(context.Background(), []byte("delete table inet guard\n"), "--file", "-")
	})

	before, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", "guard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewBackend().Snapshot(ctx); !errors.Is(err, enforcer.ErrMutationBackendOwnershipConflict) {
		t.Fatalf("Snapshot() same-name foreign table error = %v, want ownership conflict", err)
	}
	after, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", "guard")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(before)) != strings.TrimSpace(string(after)) {
		t.Fatal("same-name foreign table changed after ownership conflict")
	}
}

func TestNftablesBackendIntegrationRejectsOwnerVersionMismatch(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GUARD_NFTABLES_INTEGRATION") != "1" || os.Getenv("GUARD_NFTABLES_ISOLATED") != "1" {
		t.Skip("requires the explicit isolated root nftables integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := nftFixture(ctx, nil, "--version"); err != nil {
		t.Skip("nft is unavailable in the isolated fixture")
	}
	if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("isolated fixture already contains inet guard")
	}

	const mismatchedInfrastructureComment = "guard/v2 infrastructure/v1"
	batch := strings.Replace(infrastructureBatch(), infrastructureComment, mismatchedInfrastructureComment, 1)
	if batch == infrastructureBatch() {
		t.Fatal("infrastructure batch did not contain the fixed owner/version marker")
	}
	if err := nftFixture(ctx, []byte(batch), "--file", "-"); err != nil {
		t.Fatalf("create owner-version mismatch fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = nftFixture(context.Background(), []byte("delete table inet guard\n"), "--file", "-")
	})

	guardBefore, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", "guard")
	if err != nil {
		t.Fatal(err)
	}
	backend := NewBackend()
	if _, err := backend.Probe(ctx); !errors.Is(err, enforcer.ErrMutationBackendOwnershipConflict) {
		t.Fatalf("Probe() owner-version mismatch error = %v, want ownership conflict", err)
	}
	if _, err := backend.Snapshot(ctx); !errors.Is(err, enforcer.ErrMutationBackendOwnershipConflict) {
		t.Fatalf("Snapshot() owner-version mismatch error = %v, want ownership conflict", err)
	}
	guardAfter, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", "guard")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(guardAfter)) != strings.TrimSpace(string(guardBefore)) {
		t.Fatal("owner-version mismatch fixture changed after ownership conflict")
	}
}

func TestNftablesBackendIntegrationKnownManagersFailClosed(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GUARD_NFTABLES_INTEGRATION") != "1" || os.Getenv("GUARD_NFTABLES_ISOLATED") != "1" {
		t.Skip("requires the explicit isolated root nftables integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := nftFixture(ctx, nil, "--version"); err != nil {
		t.Skip("nft is unavailable in the isolated fixture")
	}

	for _, testCase := range []struct {
		name  string
		table string
	}{
		{name: "ufw", table: "ufw_b3_fixture"},
		{name: "docker", table: "docker_b3_fixture"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			create := []byte("add table inet " + testCase.table + "\n")
			if err := nftFixture(ctx, create, "--file", "-"); err != nil {
				t.Fatalf("create %s fixture: %v", testCase.name, err)
			}
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				_ = nftFixture(cleanupCtx, []byte("delete table inet "+testCase.table+"\n"), "--file", "-")
			})
			foreignBefore, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", testCase.table)
			if err != nil {
				t.Fatal(err)
			}

			backend := NewBackend()
			before, err := backend.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() before %s Probe: %v", testCase.name, err)
			}
			if _, present := before.ManagedState().Infrastructure(); present {
				t.Fatal("manager fixture unexpectedly created Guard infrastructure")
			}
			capabilities, err := backend.Probe(ctx)
			if err != nil {
				t.Fatalf("Probe() with %s fixture: %v", testCase.name, err)
			}
			if capabilities.Backend() != firewall.BackendKindNftablesNative || capabilities.ToolVersion() == "" ||
				!capabilities.SupportsIPv4() || !capabilities.SupportsIPv6() || !capabilities.SupportsCIDR() ||
				!capabilities.SupportsNativeSet() || !capabilities.SupportsNativeTimeout() || !capabilities.SupportsCrashSafeExpiry() ||
				!capabilities.SupportsAtomicBatch() || !capabilities.SupportsHostInput() || !capabilities.SupportsForward() ||
				!capabilities.OwnershipProven() {
				t.Fatalf("Probe() with %s fixture lost native capability: %#v", testCase.name, capabilities)
			}
			if capabilities.MutationReady() || capabilities.UFWIntegrationProven() || capabilities.DockerIntegrationProven() {
				t.Fatalf("Probe() with %s fixture = %#v, want fail-closed manager capabilities", testCase.name, capabilities)
			}
			after, err := backend.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() after %s Probe: %v", testCase.name, err)
			}
			if after.ForeignContext().Digest() != before.ForeignContext().Digest() {
				t.Fatalf("Probe() with %s fixture changed foreign context", testCase.name)
			}
			foreignAfter, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", testCase.table)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(foreignAfter)) != strings.TrimSpace(string(foreignBefore)) {
				t.Fatalf("Probe() with %s fixture changed foreign table", testCase.name)
			}
			if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
				t.Fatal("Probe() created Guard infrastructure")
			}
		})
	}
}

func TestNftablesBackendIntegrationRejectsManagerAppearingAfterAuthorization(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GUARD_NFTABLES_INTEGRATION") != "1" || os.Getenv("GUARD_NFTABLES_ISOLATED") != "1" {
		t.Skip("requires the explicit isolated root nftables integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := nftFixture(ctx, nil, "--version"); err != nil {
		t.Skip("nft is unavailable in the isolated fixture")
	}

	for _, testCase := range []struct {
		name  string
		table string
	}{
		{name: "ufw", table: "ufw_b3_toctou_fixture"},
		{name: "docker", table: "docker_b3_toctou_fixture"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
				t.Fatal("isolated fixture already contains inet guard")
			}
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				_ = nftFixture(cleanupCtx, []byte("delete table inet "+testCase.table+"\n"), "--file", "-")
				_ = nftFixture(cleanupCtx, []byte("delete table inet guard\n"), "--file", "-")
			})

			backend := NewBackend()
			capabilities, err := backend.Probe(ctx)
			if err != nil {
				t.Fatalf("Probe() before %s fixture: %v", testCase.name, err)
			}
			if !capabilities.MutationReady() {
				t.Fatalf("Probe() before %s fixture is not mutation-ready: %#v", testCase.name, capabilities)
			}
			basis, err := backend.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() before %s fixture: %v", testCase.name, err)
			}
			if _, present := basis.ManagedState().Infrastructure(); present {
				t.Fatal("initial snapshot unexpectedly contains Guard infrastructure")
			}
			plan, err := firewall.AuthorizeInfrastructureMutation(capabilities, basis, basis.Digest(), 1)
			if err != nil {
				t.Fatalf("AuthorizeInfrastructureMutation(): %v", err)
			}

			if err := nftFixture(ctx, []byte("add table inet "+testCase.table+"\n"), "--file", "-"); err != nil {
				t.Fatalf("create %s fixture after authorization: %v", testCase.name, err)
			}
			foreignBefore, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", testCase.table)
			if err != nil {
				t.Fatal(err)
			}
			managerSnapshot, err := backend.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() with %s fixture: %v", testCase.name, err)
			}
			foreignDigest := managerSnapshot.ForeignContext().Digest()
			managerCapabilities, err := backend.Probe(ctx)
			if err != nil {
				t.Fatalf("Probe() with %s fixture: %v", testCase.name, err)
			}
			if managerCapabilities.MutationReady() || managerCapabilities.UFWIntegrationProven() || managerCapabilities.DockerIntegrationProven() {
				t.Fatalf("Probe() with %s fixture = %#v, want fail-closed manager capabilities", testCase.name, managerCapabilities)
			}

			result := backend.Apply(ctx, plan)
			if err := result.Validate(); err != nil {
				t.Fatalf("Apply() returned invalid result: %v", err)
			}
			if result.Status() != firewall.MutationStatusRejected || result.MutationDigest() != plan.Digest() {
				t.Fatalf("Apply() result = %#v, want rejected result correlated to authorized plan", result)
			}
			if code, ok := result.ErrorCode(); !ok || code != firewall.MutationErrorNotReady {
				t.Fatalf("Apply() error code = (%q, %v), want not_ready", code, ok)
			}
			if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
				t.Fatal("Apply() created Guard infrastructure after manager appeared")
			}
			after, err := backend.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() after Apply with %s fixture: %v", testCase.name, err)
			}
			if _, present := after.ManagedState().Infrastructure(); present {
				t.Fatal("Apply() created Guard infrastructure after manager appeared")
			}
			if after.ForeignContext().Digest() != foreignDigest {
				t.Fatalf("Apply() with %s fixture changed foreign context", testCase.name)
			}
			foreignAfter, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", testCase.table)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(foreignAfter)) != strings.TrimSpace(string(foreignBefore)) {
				t.Fatalf("Apply() with %s fixture changed foreign table", testCase.name)
			}
		})
	}
}

func TestNftablesBackendIntegrationRejectsForeignContextAppearingAfterAuthorization(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GUARD_NFTABLES_INTEGRATION") != "1" || os.Getenv("GUARD_NFTABLES_ISOLATED") != "1" {
		t.Skip("requires the explicit isolated root nftables integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := nftFixture(ctx, nil, "--version"); err != nil {
		t.Skip("nft is unavailable in the isolated fixture")
	}

	const foreignTable = "foreign_b3_apply_toctou_fixture"
	if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("isolated fixture already contains inet guard")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = nftFixture(cleanupCtx, []byte("delete table inet "+foreignTable+"\n"), "--file", "-")
		_ = nftFixture(cleanupCtx, []byte("delete table inet guard\n"), "--file", "-")
	})

	backend := NewBackend()
	capabilities, err := backend.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.MutationReady() {
		t.Fatalf("Probe() before foreign context is not mutation-ready: %#v", capabilities)
	}
	basis, err := backend.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := firewall.AuthorizeInfrastructureMutation(capabilities, basis, basis.Digest(), 1)
	if err != nil {
		t.Fatal(err)
	}

	if err := nftFixture(ctx, []byte("add table inet "+foreignTable+"\n"), "--file", "-"); err != nil {
		t.Fatalf("create neutral foreign table after authorization: %v", err)
	}
	foreignBefore, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", foreignTable)
	if err != nil {
		t.Fatal(err)
	}
	freshCapabilities, err := backend.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !freshCapabilities.MutationReady() || !freshCapabilities.UFWIntegrationProven() || !freshCapabilities.DockerIntegrationProven() {
		t.Fatalf("Probe() with neutral foreign context = %#v, want mutation-ready coexistence", freshCapabilities)
	}
	if !reflect.DeepEqual(freshCapabilities, capabilities) {
		t.Fatalf("Probe() capability drift after neutral foreign context: before=%#v after=%#v", capabilities, freshCapabilities)
	}
	freshSnapshot, err := backend.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foreignDigest := freshSnapshot.ForeignContext().Digest()
	if foreignDigest == basis.ForeignContext().Digest() {
		t.Fatal("neutral foreign table did not change foreign context")
	}

	result := backend.Apply(ctx, plan)
	if err := result.Validate(); err != nil {
		t.Fatalf("Apply() returned invalid result: %v", err)
	}
	if result.Status() != firewall.MutationStatusRejected || result.MutationDigest() != plan.Digest() {
		t.Fatalf("Apply() result = %#v, want rejected result correlated to authorized plan", result)
	}
	if code, ok := result.ErrorCode(); !ok || code != firewall.MutationErrorNotReady {
		t.Fatalf("Apply() error code = (%q, %v), want not_ready", code, ok)
	}
	if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("Apply() created Guard infrastructure after foreign context changed")
	}
	after, err := backend.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := after.ManagedState().Infrastructure(); present {
		t.Fatal("Apply() created Guard infrastructure after foreign context changed")
	}
	if after.ForeignContext().Digest() != foreignDigest {
		t.Fatal("Apply() changed foreign context after rejecting stale authorization")
	}
	foreignAfter, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", foreignTable)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(foreignAfter)) != strings.TrimSpace(string(foreignBefore)) {
		t.Fatal("Apply() changed neutral foreign table after rejecting stale authorization")
	}
}

func TestNftablesBackendIntegrationRejectsManagerAppearingAfterRemovalAuthorization(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GUARD_NFTABLES_INTEGRATION") != "1" || os.Getenv("GUARD_NFTABLES_ISOLATED") != "1" {
		t.Skip("requires the explicit isolated root nftables integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := nftFixture(ctx, nil, "--version"); err != nil {
		t.Skip("nft is unavailable in the isolated fixture")
	}

	for _, testCase := range []struct {
		name  string
		table string
	}{
		{name: "ufw", table: "ufw_b3_remove_toctou_fixture"},
		{name: "docker", table: "docker_b3_remove_toctou_fixture"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := nftFixture(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
				t.Fatal("isolated fixture already contains inet guard")
			}
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				_ = nftFixture(cleanupCtx, []byte("delete table inet "+testCase.table+"\n"), "--file", "-")
				_ = nftFixture(cleanupCtx, []byte("delete table inet guard\n"), "--file", "-")
			})

			backend := NewBackend()
			capabilities, err := backend.Probe(ctx)
			if err != nil {
				t.Fatalf("Probe() before %s fixture: %v", testCase.name, err)
			}
			if !capabilities.MutationReady() {
				t.Fatalf("Probe() before %s fixture is not mutation-ready: %#v", testCase.name, capabilities)
			}
			basis, err := backend.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() before %s fixture: %v", testCase.name, err)
			}
			plan, err := firewall.AuthorizeInfrastructureMutation(capabilities, basis, basis.Digest(), 1)
			if err != nil {
				t.Fatalf("AuthorizeInfrastructureMutation(): %v", err)
			}
			assertConfirmedWithReadback(t, backend, ctx, basis, backend.Apply(ctx, plan), plan.Digest())

			snapshot, err := backend.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() before removal authorization: %v", err)
			}
			if _, present := snapshot.ManagedState().Infrastructure(); !present {
				t.Fatal("infrastructure was not observed before removal authorization")
			}
			guardBefore, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", "guard")
			if err != nil {
				t.Fatal(err)
			}
			removal, alreadyAbsent, err := firewall.AuthorizeManagedRemoval(capabilities, snapshot, firewall.ManagedOwnerVersionV1)
			if err != nil || alreadyAbsent || removal == nil {
				t.Fatalf("AuthorizeManagedRemoval() = (%v, %v, %v)", removal, alreadyAbsent, err)
			}

			if err := nftFixture(ctx, []byte("add table inet "+testCase.table+"\n"), "--file", "-"); err != nil {
				t.Fatalf("create %s fixture after removal authorization: %v", testCase.name, err)
			}
			foreignBefore, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", testCase.table)
			if err != nil {
				t.Fatal(err)
			}
			managerSnapshot, err := backend.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() with %s fixture: %v", testCase.name, err)
			}
			foreignDigest := managerSnapshot.ForeignContext().Digest()
			managerCapabilities, err := backend.Probe(ctx)
			if err != nil {
				t.Fatalf("Probe() with %s fixture: %v", testCase.name, err)
			}
			if managerCapabilities.MutationReady() || managerCapabilities.UFWIntegrationProven() || managerCapabilities.DockerIntegrationProven() {
				t.Fatalf("Probe() with %s fixture = %#v, want fail-closed manager capabilities", testCase.name, managerCapabilities)
			}

			result := backend.RemoveManagedInfrastructure(ctx, removal)
			if err := result.Validate(); err != nil {
				t.Fatalf("RemoveManagedInfrastructure() returned invalid result: %v", err)
			}
			if result.Status() != firewall.MutationStatusRejected || result.MutationDigest() != removal.Digest() {
				t.Fatalf("RemoveManagedInfrastructure() result = %#v, want rejected result correlated to removal authorization", result)
			}
			if code, ok := result.ErrorCode(); !ok || code != firewall.MutationErrorNotReady {
				t.Fatalf("RemoveManagedInfrastructure() error code = (%q, %v), want not_ready", code, ok)
			}

			guardAfter, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", "guard")
			if err != nil {
				t.Fatalf("Guard infrastructure disappeared after %s manager appeared: %v", testCase.name, err)
			}
			if strings.TrimSpace(string(guardAfter)) != strings.TrimSpace(string(guardBefore)) {
				t.Fatalf("RemoveManagedInfrastructure() with %s fixture changed Guard table", testCase.name)
			}
			after, err := backend.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() after RemoveManagedInfrastructure() with %s fixture: %v", testCase.name, err)
			}
			if _, present := after.ManagedState().Infrastructure(); !present {
				t.Fatal("RemoveManagedInfrastructure() removed Guard infrastructure after manager appeared")
			}
			if after.ForeignContext().Digest() != foreignDigest {
				t.Fatalf("RemoveManagedInfrastructure() with %s fixture changed foreign context", testCase.name)
			}
			foreignAfter, err := nftFixtureOutput(ctx, nil, "--json", "list", "table", "inet", testCase.table)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(foreignAfter)) != strings.TrimSpace(string(foreignBefore)) {
				t.Fatalf("RemoveManagedInfrastructure() with %s fixture changed foreign table", testCase.name)
			}
		})
	}
}

func assertConfirmedWithReadback(t *testing.T, backend *Backend, ctx context.Context, before firewall.ManagedSnapshot, result firewall.MutationResult, digest string) {
	t.Helper()
	if result.Status() == firewall.MutationStatusConfirmed && result.MutationDigest() == digest {
		return
	}
	after, err := backend.Snapshot(ctx)
	if err != nil {
		t.Fatalf("mutation result = %#v; readback error = %v", result, err)
	}
	_, infrastructure := after.ManagedState().Infrastructure()
	policy, policyPresent := after.ManagedState().Policy()
	t.Fatalf("mutation result = %#v; readback infrastructure=%v policy=%v policy_digest=%s targets=%d foreign_unchanged=%v", result, infrastructure, policyPresent, policy.RelationDigest(), len(after.ManagedState().Targets()), after.ForeignContext().Digest() == before.ForeignContext().Digest())
}

func assertTarget(t *testing.T, targets []firewall.TargetObservation, target netip.Prefix) {
	t.Helper()
	for _, observed := range targets {
		if observed.Target() != target {
			continue
		}
		if observed.TimeoutMode() != firewall.ManagedTimeoutNative || len(observed.Scopes()) != 2 {
			t.Fatalf("target observation = %#v", observed)
		}
		if expiry, ok := observed.EffectiveUntilUnixMicro(); !ok || expiry <= time.Now().UnixMicro() {
			t.Fatalf("target expiry = (%d, %v)", expiry, ok)
		}
		return
	}
	t.Fatalf("target %s was not observed", target)
}

func nftFixture(ctx context.Context, input []byte, args ...string) error {
	_, err := nftFixtureOutput(ctx, input, args...)
	return err
}

func nftFixtureOutput(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, nftBinary, args...)
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("isolated nft fixture command failed: %s", strings.TrimSpace(string(output)))
	}
	return output, nil
}
