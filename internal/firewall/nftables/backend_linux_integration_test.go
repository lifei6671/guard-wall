//go:build linux && integration && nftables

package nftables

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

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
	command := exec.CommandContext(ctx, nftBinary, args...)
	command.Stdin = strings.NewReader(string(input))
	if output, err := command.CombinedOutput(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("isolated nft fixture command failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}
