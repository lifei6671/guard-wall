//go:build linux && integration

package enforcer_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

const b4L18ClientModeEnv = "GUARD_B4_L18_CLIENT_MODE"

func TestEnforcerRuntimeLinuxRootGuardCrossUID(t *testing.T) {
	if os.Getenv(b4L18ClientModeEnv) == "guard" {
		testB4L18GuardProbe(t)
		return
	}
	if os.Getenv(b4L18ClientModeEnv) != "" {
		t.Fatalf("unknown %s", b4L18ClientModeEnv)
	}

	guardUID := b4L18EnvUint32(t, "GUARD_B4_L18_GUARD_UID")
	guardGID := b4L18EnvUint32(t, "GUARD_B4_L18_GUARD_GID")
	if os.Geteuid() != 0 {
		t.Fatal("B4-l18 integration test must run as root")
	}

	listener, err := ipc.ListenUnix(guardGID)
	if err != nil {
		t.Fatalf("ListenUnix(): %v", err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("listener cleanup: %v", err)
		}
	})
	capabilities := b4L18Capabilities(t)
	backend := &b4L18Backend{capabilities: capabilities}
	failureEvents := make(chan error, 2)
	runtime, err := enforcer.NewEnforcerRuntime(backend, listener, guardUID, ipc.EnforcerServeOptions{
		RequestTimeout: 3 * time.Second,
		OnRequestFailure: func(err error) {
			failureEvents <- err
		},
	})
	if err != nil {
		t.Fatalf("NewEnforcerRuntime(): %v", err)
	}

	assertB4L18Node(t, filepath.Dir(ipc.EnforcerSocketPath), os.ModeDir, 0o750, 0, guardGID)
	assertB4L18Node(t, ipc.EnforcerSocketPath, os.ModeSocket, 0o660, 0, guardGID)
	t.Logf("active fixture: %s and %s are root:%d", filepath.Dir(ipc.EnforcerSocketPath), ipc.EnforcerSocketPath, guardGID)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- runtime.Run(ctx) }()
	runtimeStopped := false
	t.Cleanup(func() {
		if runtimeStopped {
			return
		}
		cancel()
		select {
		case <-runDone:
		case <-time.After(3 * time.Second):
			t.Error("runtime did not stop during cleanup")
		}
	})

	rootCtx, cancelRoot := context.WithTimeout(context.Background(), 3*time.Second)
	_, err = ipc.RoundTripProbeCapabilities(rootCtx)
	cancelRoot()
	var frameError *ipc.FrameError
	if !errors.As(err, &frameError) ||
		(frameError.Code() != ipc.FrameErrorCodeTruncatedLength && frameError.Code() != ipc.FrameErrorCodeWriteFailed) {
		t.Fatalf("root probe error = %T %v, want closed response frame", err, err)
	}
	select {
	case observed := <-failureEvents:
		var peerError *ipc.PeerError
		if !errors.As(observed, &peerError) || peerError.Code() != ipc.PeerErrorCodeUIDMismatch {
			t.Fatalf("root peer rejection = %T %v, want peer_uid_mismatch", observed, observed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("root peer rejection was not observed")
	}
	if probes, snapshots, applies, removals := backend.counts(); probes != 0 || snapshots != 0 || applies != 0 || removals != 0 {
		t.Fatalf("backend calls after root request = (%d, %d, %d, %d), want zero", probes, snapshots, applies, removals)
	}

	guardCtx, cancelGuard := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelGuard()
	command := exec.CommandContext(
		guardCtx,
		"runuser", "-u", "guard", "--", "env", b4L18ClientModeEnv+"=guard",
		os.Args[0], "-test.run=^TestEnforcerRuntimeLinuxRootGuardCrossUID$", "-test.count=1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("guard probe: %v: %s", err, output)
	}
	if probes, snapshots, applies, removals := backend.counts(); probes != 1 || snapshots != 0 || applies != 0 || removals != 0 {
		t.Fatalf("backend calls after guard request = (%d, %d, %d, %d), want (1, 0, 0, 0)", probes, snapshots, applies, removals)
	}
	select {
	case observed := <-failureEvents:
		t.Fatalf("guard probe produced request failure: %T %v", observed, observed)
	default:
	}

	cancel()
	var runErr error
	select {
	case runErr = <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	runtimeStopped = true
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run() error = %T %v, want context canceled", runErr, runErr)
	}
	if _, err := os.Lstat(ipc.EnforcerSocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket cleanup error = %v, want not exist", err)
	}
}

func testB4L18GuardProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	capabilities, err := ipc.RoundTripProbeCapabilities(ctx)
	if err != nil {
		t.Fatalf("RoundTripProbeCapabilities(): %v", err)
	}
	if capabilities.Backend() != firewall.BackendKindNftablesNative || capabilities.ToolVersion() != "b4-l18-fixture" {
		t.Fatalf("Probe capabilities = (%q, %q), want nftables-native fixture", capabilities.Backend(), capabilities.ToolVersion())
	}
}

type b4L18Backend struct {
	capabilities firewall.FirewallCapabilities
	probes       atomic.Int32
	snapshots    atomic.Int32
	applies      atomic.Int32
	removals     atomic.Int32
}

func (b *b4L18Backend) Probe(ctx context.Context) (firewall.FirewallCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return firewall.FirewallCapabilities{}, err
	}
	b.probes.Add(1)
	return b.capabilities, nil
}

func (b *b4L18Backend) Snapshot(context.Context) (firewall.ManagedSnapshot, error) {
	b.snapshots.Add(1)
	return firewall.ManagedSnapshot{}, errors.New("snapshot is outside B4-l18")
}

func (b *b4L18Backend) Apply(context.Context, firewall.OperationPlan) firewall.MutationResult {
	b.applies.Add(1)
	return firewall.MutationResult{}
}

func (b *b4L18Backend) RemoveManagedInfrastructure(context.Context, firewall.RemovalAuthorization) firewall.MutationResult {
	b.removals.Add(1)
	return firewall.MutationResult{}
}

func (b *b4L18Backend) counts() (int32, int32, int32, int32) {
	return b.probes.Load(), b.snapshots.Load(), b.applies.Load(), b.removals.Load()
}

func b4L18Capabilities(t *testing.T) firewall.FirewallCapabilities {
	t.Helper()
	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend:         firewall.BackendKindNftablesNative,
		ToolVersion:     "b4-l18-fixture",
		IPv4:            true,
		CIDR:            true,
		NativeSet:       true,
		AtomicBatch:     true,
		HostInput:       true,
		Forward:         true,
		OwnershipProven: true,
	})
	if err != nil {
		t.Fatalf("NewFirewallCapabilities(): %v", err)
	}
	return capabilities
}

func b4L18EnvUint32(t *testing.T, key string) uint32 {
	t.Helper()
	value, err := strconv.ParseUint(os.Getenv(key), 10, 32)
	if err != nil {
		t.Fatalf("%s is invalid: %v", key, err)
	}
	return uint32(value)
}

func assertB4L18Node(t *testing.T, path string, wantType os.FileMode, wantPermissions os.FileMode, wantUID, wantGID uint32) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q): %v", path, err)
	}
	if info.Mode().Type() != wantType || info.Mode().Perm() != wantPermissions {
		t.Fatalf("node %q mode = %v, want type %v permissions %#o", path, info.Mode(), wantType, wantPermissions)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != wantUID || stat.Gid != wantGID {
		t.Fatalf("node %q owner = %v, want %d:%d", path, info.Sys(), wantUID, wantGID)
	}
}
