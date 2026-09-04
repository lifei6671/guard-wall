//go:build linux && integration && nftables

package enforcer_test

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

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/firewall/nftables"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

const (
	b4NftablesIntegrationEnv = "GUARD_NFTABLES_INTEGRATION"
	b4NftablesIsolatedEnv    = "GUARD_NFTABLES_ISOLATED"
	b4NftablesForeignTable   = "guard_runtime_fixture_foreign"
)

// TestEnforcerRuntimeNftablesIntegration exercises the production fixed-socket
// clients through the persistent Enforcer runtime and the native nftables
// backend. The runner must provide a disposable network namespace; this test
// never treats the host namespace as an acceptable fixture.
func TestEnforcerRuntimeNftablesIntegration(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv(b4NftablesIntegrationEnv) != "1" || os.Getenv(b4NftablesIsolatedEnv) != "1" {
		t.Skip("requires the explicit isolated root nftables integration fixture")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b4NftablesCommand(ctx, nil, "--version"); err != nil {
		t.Skipf("nft is unavailable in the isolated fixture: %v", err)
	}
	if err := b4NftablesCommand(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("isolated fixture already contains inet guard")
	}
	if err := b4NftablesCommand(ctx, nil, "--json", "list", "table", "inet", b4NftablesForeignTable); err == nil {
		t.Fatalf("isolated fixture already contains inet %s", b4NftablesForeignTable)
	}

	foreignCreated := false
	managedMayExist := false
	t.Cleanup(func() {
		if managedMayExist {
			_ = b4NftablesCommand(context.Background(), []byte("delete table inet guard\n"), "--file", "-")
		}
		if foreignCreated {
			_ = b4NftablesCommand(context.Background(), []byte("delete table inet "+b4NftablesForeignTable+"\n"), "--file", "-")
		}
	})
	if err := b4NftablesCommand(ctx, []byte("add table inet "+b4NftablesForeignTable+"\n"), "--file", "-"); err != nil {
		t.Fatalf("create isolated foreign table: %v", err)
	}
	foreignCreated = true

	listener, err := ipc.ListenUnix(uint32(os.Getgid()))
	if err != nil {
		t.Fatalf("ListenUnix(): %v", err)
	}
	runtime, err := enforcer.NewEnforcerRuntime(nftables.NewBackend(), listener, uint32(os.Getuid()), ipc.EnforcerServeOptions{
		RequestTimeout:   5 * time.Second,
		OnRequestFailure: func(error) {},
	})
	if err != nil {
		_ = listener.Close()
		t.Fatalf("NewEnforcerRuntime(): %v", err)
	}
	runtimeCtx, stopRuntime := context.WithCancel(context.Background())
	runtimeDone := make(chan error, 1)
	go func() { runtimeDone <- runtime.Run(runtimeCtx) }()
	runtimeStopped := false
	t.Cleanup(func() {
		if runtimeStopped {
			return
		}
		stopRuntime()
		select {
		case err := <-runtimeDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("EnforcerRuntime.Run(): %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("EnforcerRuntime.Run() did not stop")
		}
	})

	capabilities := b4RuntimeProbe(t, ctx)
	if capabilities.Backend() != firewall.BackendKindNftablesNative || !capabilities.MutationReady() {
		t.Fatalf("Probe capabilities = (%q, ready=%v), want ready nftables-native", capabilities.Backend(), capabilities.MutationReady())
	}
	initial := b4RuntimeSnapshot(t, ctx)
	if _, present := initial.ManagedState().Infrastructure(); present {
		t.Fatal("initial snapshot unexpectedly contains managed infrastructure")
	}
	foreignDigest := initial.ForeignContext().Digest()

	infrastructure, err := ipc.NewApplyInfrastructureRequest(initial.Digest(), 1)
	if err != nil {
		t.Fatalf("NewApplyInfrastructureRequest(): %v", err)
	}
	b4RuntimeApplyConfirmed(t, ctx, infrastructure, ipc.DomainInfrastructure)
	managedMayExist = true
	afterInfrastructure := b4RuntimeSnapshot(t, ctx)
	b4AssertForeignDigest(t, afterInfrastructure, foreignDigest, "infrastructure")
	if _, present := afterInfrastructure.ManagedState().Infrastructure(); !present {
		t.Fatal("infrastructure was not observed after confirmed IPC Apply")
	}

	policy, err := ipc.NewApplyPolicyRequest(
		afterInfrastructure.Digest(),
		2,
		[]netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("2001:db8::/64")},
		[]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
	)
	if err != nil {
		t.Fatalf("NewApplyPolicyRequest(): %v", err)
	}
	b4RuntimeApplyConfirmed(t, ctx, policy, ipc.DomainPolicy)
	afterPolicy := b4RuntimeSnapshot(t, ctx)
	b4AssertForeignDigest(t, afterPolicy, foreignDigest, "policy")
	if _, present := afterPolicy.ManagedState().Policy(); !present {
		t.Fatal("policy was not observed after confirmed IPC Apply")
	}

	target := netip.MustParsePrefix("203.0.113.7/32")
	targetPlan, err := ipc.NewApplyTargetRequest(
		afterPolicy.Digest(),
		3,
		target,
		ipc.MembershipPresent,
		ipc.TimeoutModeNative,
		time.Now().Add(time.Minute).UnixMicro(),
		true,
		[]ipc.Scope{ipc.ScopeInput, ipc.ScopeForward},
	)
	if err != nil {
		t.Fatalf("NewApplyTargetRequest(): %v", err)
	}
	b4RuntimeApplyConfirmed(t, ctx, targetPlan, ipc.DomainTarget)
	afterTarget := b4RuntimeSnapshot(t, ctx)
	b4AssertForeignDigest(t, afterTarget, foreignDigest, "target")
	b4AssertRuntimeTarget(t, afterTarget.ManagedState().Targets(), target)

	removeResponse, err := ipc.RoundTripMutation(ctx, ipc.NewRemoveManagedInfrastructureRequest())
	if err != nil {
		t.Fatalf("RoundTripMutation(RemoveManagedInfrastructure): %v", err)
	}
	remove, ok := removeResponse.(ipc.RemoveManagedInfrastructureResponse)
	if !ok || remove.Status() != ipc.MutationStatusConfirmed {
		t.Fatalf("RemoveManagedInfrastructure response = %#v, want confirmed typed response", removeResponse)
	}
	managedMayExist = false
	afterRemoval := b4RuntimeSnapshot(t, ctx)
	b4AssertForeignDigest(t, afterRemoval, foreignDigest, "remove")
	state := afterRemoval.ManagedState()
	if _, present := state.Infrastructure(); present {
		t.Fatal("managed infrastructure survived confirmed Remove")
	}
	if _, present := state.Policy(); present || len(state.Targets()) != 0 {
		t.Fatal("managed policy or targets survived confirmed Remove")
	}
	if err := b4NftablesCommand(ctx, nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("Guard table survived confirmed Remove")
	}
	if err := b4NftablesCommand(ctx, nil, "--json", "list", "table", "inet", b4NftablesForeignTable); err != nil {
		t.Fatalf("foreign table was not preserved: %v", err)
	}

	stopRuntime()
	select {
	case err := <-runtimeDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("EnforcerRuntime.Run() stop error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EnforcerRuntime.Run() did not stop before binary recovery test")
	}
	runtimeStopped = true
	if err := b4NftablesCommand(ctx, []byte("delete table inet "+b4NftablesForeignTable+"\n"), "--file", "-"); err != nil {
		t.Fatalf("remove library fixture foreign table: %v", err)
	}
	foreignCreated = false
	if err := os.Remove("/run/guard"); err != nil {
		t.Fatalf("remove library fixture socket directory: %v", err)
	}
	t.Run("M0-RECOVERY-004", b4RunBinaryRecovery)
	t.Run("C2-IPC-HEALTH-001", b4RunIPCHealthSourceRecovery)
	t.Run("C2-TARGET-NATIVE-TIMEOUT-001", b4RunTargetNativeTimeout)
}

func b4RuntimeProbe(t *testing.T, ctx context.Context) firewall.FirewallCapabilities {
	t.Helper()
	capabilities, err := ipc.RoundTripProbeCapabilities(ctx)
	if err != nil {
		t.Fatalf("RoundTripProbeCapabilities(): %v", err)
	}
	return capabilities
}

func b4RuntimeSnapshot(t *testing.T, ctx context.Context) firewall.ManagedSnapshot {
	t.Helper()
	snapshot, err := ipc.RoundTripSnapshotManaged(ctx)
	if err != nil {
		t.Fatalf("RoundTripSnapshotManaged(): %v", err)
	}
	return snapshot
}

func b4RuntimeApplyConfirmed(t *testing.T, ctx context.Context, request ipc.MutationRequest, domain ipc.Domain) {
	t.Helper()
	response, err := ipc.RoundTripMutation(ctx, request)
	if err != nil {
		t.Fatalf("RoundTripMutation(%s): %v", domain, err)
	}
	apply, ok := response.(ipc.ApplyManagedPlanResponse)
	if !ok || apply.Domain() != domain || apply.Status() != ipc.MutationStatusConfirmed {
		t.Fatalf("Apply %s response = %#v, want confirmed typed response", domain, response)
	}
	if code, present := apply.ErrorCode(); present {
		t.Fatalf("Apply %s error code = %q, want absent", domain, code)
	}
}

func b4AssertRuntimeTarget(t *testing.T, targets []firewall.TargetObservation, target netip.Prefix) {
	t.Helper()
	for _, observed := range targets {
		if observed.Target() == target {
			if observed.TimeoutMode() != firewall.ManagedTimeoutNative || len(observed.Scopes()) != 2 {
				t.Fatalf("target observation = %#v", observed)
			}
			if expiry, ok := observed.EffectiveUntilUnixMicro(); !ok || expiry <= time.Now().UnixMicro() {
				t.Fatalf("target expiry = (%d, %v), want future native timeout", expiry, ok)
			}
			return
		}
	}
	t.Fatalf("target %s was not observed", target)
}

func b4AssertForeignDigest(t *testing.T, snapshot firewall.ManagedSnapshot, want, stage string) {
	t.Helper()
	if got := snapshot.ForeignContext().Digest(); got != want {
		t.Fatalf("%s changed foreign snapshot context: got %s want %s", stage, got, want)
	}
}

func b4NftablesCommand(ctx context.Context, input []byte, args ...string) error {
	command := exec.CommandContext(ctx, "nft", args...)
	command.Stdin = strings.NewReader(string(input))
	if output, err := command.CombinedOutput(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("isolated nft fixture command failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}
