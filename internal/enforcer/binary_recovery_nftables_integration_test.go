//go:build linux && integration && nftables

package enforcer_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/enforcement"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/firewall/nftables"
	"github.com/lifei6671/guard-wall/internal/ipc"
	"github.com/lifei6671/guard-wall/internal/store"
	"github.com/lifei6671/guard-wall/migrations"
)

const b4BinaryRecoveryTimeout = 15 * time.Second

const b4IntegrationOperationLogEnv = "GUARD_INTEGRATION_OPERATION_LOG"

const (
	b4TargetLifecycleEnv       = "GUARD_B4_TARGET_LIFECYCLE"
	b4TargetSnapshotStateEnv   = "GUARD_B4_TARGET_SNAPSHOT_STATE"
	b4TargetStoreStateEnv      = "GUARD_B4_TARGET_STORE_STATE"
	b4TargetLogicalExpiryEnv   = "GUARD_B4_TARGET_LOGICAL_EXPIRY"
	b4TargetLifecycleBan       = "ban"
	b4TargetLifecycleExpire    = "expire"
	b4TargetStoreStatePresent  = "present"
	b4TargetStoreStateAbsent   = "absent"
	b4NativeTimeoutTarget      = "203.0.113.77/32"
	b4NativeExpiryTolerance    = 3 * time.Second
	b4NativeTimeoutDecisionID  = "b4-native-timeout-target"
	b4NativeTimeoutDecisionTTL = 90 * time.Second
)

type b4GuardIdentity struct {
	uid uint32
	gid uint32
}

type b4ManagedProcess struct {
	name       string
	command    *exec.Cmd
	credential *b4GuardIdentity
	done       chan error
	output     bytes.Buffer
	finished   bool
}

// b4RunBinaryRecovery proves the clean-target process boundary with the
// production binaries. It runs only inside the dedicated disposable nftables
// namespace selected by TestEnforcerRuntimeNftablesIntegration.
func b4RunBinaryRecovery(t *testing.T) {
	t.Helper()
	if err := b4NftablesCommand(context.Background(), nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("binary recovery fixture already contains inet guard")
	}

	identity := b4LookupGuardIdentity(t)
	fixture := b4NewRecoveryFixture(t, identity)
	processes := make([]*b4ManagedProcess, 0, 4)
	t.Cleanup(func() {
		for index := len(processes) - 1; index >= 0; index-- {
			b4StopProcess(t, processes[index])
		}
		if err := b4NftablesCommand(context.Background(), []byte("delete table inet guard\n"), "--file", "-"); err != nil {
			t.Errorf("remove binary recovery Guard table: %v", err)
		}
		if err := os.Remove("/run/guard"); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove binary recovery socket directory: %v", err)
		}
	})

	enforcer := b4StartProcess(t, "guard-enforcer", fixture.enforcerPath, nil, b4GuardIdentity{})
	processes = append(processes, enforcer)
	b4WaitForSocket(t, identity, 0)
	agent := b4StartProcess(t, "guard-agent", fixture.agentPath, []string{"--config", fixture.configPath}, identity)
	processes = append(processes, agent)
	b4WaitForLiveRuntime(t, fixture.databasePath, identity, agent, enforcer)
	initialSignature := b4StoreSignature(t, fixture.readbackPath, fixture.databasePath, identity)

	b4KillAndAssertSIGKILL(t, agent)
	b4AssertSQLiteReopen(t, fixture.readbackPath, fixture.databasePath, identity)
	b4AssertSocket(t, identity, 0)
	b4AssertGuardTable(t)

	agent = b4StartProcess(t, "guard-agent replacement", fixture.agentPath, []string{"--config", fixture.configPath}, identity)
	processes = append(processes, agent)
	b4WaitForReplacementAgentReconcile(t, fixture.readbackPath, fixture.databasePath, identity, agent, enforcer, initialSignature)
	b4AssertSQLiteReopen(t, fixture.readbackPath, fixture.databasePath, identity)

	socketInode := b4SocketInode(t)
	b4KillAndAssertSIGKILL(t, enforcer)
	b4AssertStaleSocket(t, identity, socketInode)
	b4AssertGuardTable(t)
	b4StopProcess(t, agent)

	enforcer = b4StartProcess(t, "guard-enforcer replacement", fixture.enforcerPath, nil, b4GuardIdentity{})
	processes = append(processes, enforcer)
	b4WaitForSocket(t, identity, socketInode)
	beforeRecoveryAgent := b4StoreSignature(t, fixture.readbackPath, fixture.databasePath, identity)
	agent = b4StartProcess(t, "guard-agent after enforcer recovery", fixture.agentPath, []string{"--config", fixture.configPath}, identity)
	processes = append(processes, agent)
	b4WaitForReplacementAgentReconcile(t, fixture.readbackPath, fixture.databasePath, identity, agent, enforcer, beforeRecoveryAgent)
	b4AssertSQLiteReopen(t, fixture.readbackPath, fixture.databasePath, identity)
	b4AssertGuardTable(t)
	digest := sha256.Sum256([]byte(b4StoreSignature(t, fixture.readbackPath, fixture.databasePath, identity)))
	t.Logf("M0_FINAL_STATE_DIGEST=%x", digest)
}

// b4RunIPCHealthSourceRecovery proves that a live Agent observes an Enforcer
// outage and uses the production health source to recover through IPC without
// issuing a mutation. It runs only in the disposable nftables namespace.
func b4RunIPCHealthSourceRecovery(t *testing.T) {
	t.Helper()
	if err := b4NftablesCommand(context.Background(), nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("IPC health fixture already contains inet guard")
	}

	identity := b4LookupGuardIdentity(t)
	fixture := b4NewRecoveryFixture(t, identity)
	processes := make([]*b4ManagedProcess, 0, 3)
	t.Cleanup(func() {
		for index := len(processes) - 1; index >= 0; index-- {
			b4StopProcess(t, processes[index])
		}
		if err := b4NftablesCommand(context.Background(), []byte("delete table inet guard\n"), "--file", "-"); err != nil {
			t.Errorf("remove IPC health Guard table: %v", err)
		}
		if err := os.Remove("/run/guard"); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove IPC health socket directory: %v", err)
		}
	})

	operationLogEnv := []string{b4IntegrationOperationLogEnv + "=" + fixture.operationLogPath}
	enforcer := b4StartProcessWithEnvironment(t, "guard-enforcer", fixture.enforcerPath, nil, b4GuardIdentity{}, operationLogEnv)
	processes = append(processes, enforcer)
	b4WaitForSocket(t, identity, 0)
	agent := b4StartProcess(t, "guard-agent", fixture.agentPath, []string{"--config", fixture.configPath}, identity)
	processes = append(processes, agent)
	b4WaitForLiveRuntime(t, fixture.databasePath, identity, agent, enforcer)
	b4WaitForHealthSourceReady(t, fixture.operationLogPath, enforcer.command.Process.Pid, agent, enforcer)

	baselineSignature := b4StoreSignature(t, fixture.readbackPath, fixture.databasePath, identity)
	guardTable := b4GuardTableJSON(t)
	socketInode := b4SocketInode(t)
	b4KillAndAssertSIGKILL(t, enforcer)
	b4AssertStaleSocket(t, identity, socketInode)
	b4AssertGuardTable(t)
	b4AssertProcessAlive(t, agent)
	b4WaitForHealthSourceUnavailability(t, agent)

	enforcer = b4StartProcessWithEnvironment(t, "guard-enforcer replacement", fixture.enforcerPath, nil, b4GuardIdentity{}, operationLogEnv)
	processes = append(processes, enforcer)
	b4WaitForSocket(t, identity, socketInode)
	b4WaitForHealthSourceRecovery(t, fixture, identity, agent, enforcer, baselineSignature)
	if got := b4GuardTableJSON(t); !bytes.Equal(got, guardTable) {
		t.Fatal("Guard table changed during observation-only IPC health recovery")
	}
	b4AssertSQLiteReopen(t, fixture.readbackPath, fixture.databasePath, identity)
}

// b4RunTargetNativeTimeout proves a test-only Manual Target Intent reaches the
// live Agent, authenticated IPC, and native nftables backend. It verifies the
// physical timeout includes the contract SafetyGrace, then expires the Manual
// Decision through the production lifecycle before a replacement Agent removes
// the Target.
func b4RunTargetNativeTimeout(t *testing.T) {
	t.Helper()
	if err := b4NftablesCommand(context.Background(), nil, "--json", "list", "table", "inet", "guard"); err == nil {
		t.Fatal("Target native timeout fixture already contains inet guard")
	}

	identity := b4LookupGuardIdentity(t)
	fixture := b4NewRecoveryFixture(t, identity)
	processes := make([]*b4ManagedProcess, 0, 4)
	t.Cleanup(func() {
		for index := len(processes) - 1; index >= 0; index-- {
			b4StopProcess(t, processes[index])
		}
		if err := b4NftablesCommand(context.Background(), []byte("delete table inet guard\n"), "--file", "-"); err != nil {
			t.Errorf("remove Target native timeout Guard table: %v", err)
		}
		if err := os.Remove("/run/guard"); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove Target native timeout socket directory: %v", err)
		}
	})

	operationLogEnv := []string{b4IntegrationOperationLogEnv + "=" + fixture.operationLogPath}
	enforcer := b4StartProcessWithEnvironment(t, "guard-enforcer", fixture.enforcerPath, nil, b4GuardIdentity{}, operationLogEnv)
	processes = append(processes, enforcer)
	b4WaitForSocket(t, identity, 0)
	agent := b4StartProcess(t, "guard-agent bootstrap", fixture.agentPath, []string{"--config", fixture.configPath}, identity)
	processes = append(processes, agent)
	b4WaitForLiveRuntime(t, fixture.databasePath, identity, agent, enforcer)
	b4StopProcess(t, agent)

	logicalExpiry := b4RunTargetLifecycleHelper(t, fixture, identity, b4TargetLifecycleBan)
	b4ClearCompletedOperationLog(t, fixture.operationLogPath)
	agent = b4StartProcess(t, "guard-agent target apply", fixture.agentPath, []string{"--config", fixture.configPath}, identity)
	processes = append(processes, agent)
	b4WaitForTargetPresent(t, fixture, identity, agent, enforcer, logicalExpiry)
	b4WaitForTargetStoreReadback(t, fixture, identity, agent, enforcer, b4TargetStoreStatePresent, logicalExpiry)

	b4StopProcess(t, agent)
	b4RunTargetLifecycleHelper(t, fixture, identity, b4TargetLifecycleExpire)
	b4ClearCompletedOperationLog(t, fixture.operationLogPath)
	agent = b4StartProcess(t, "guard-agent target removal", fixture.agentPath, []string{"--config", fixture.configPath}, identity)
	processes = append(processes, agent)
	b4WaitForTargetAbsent(t, fixture, identity, agent, enforcer)
	b4WaitForTargetStoreReadback(t, fixture, identity, agent, enforcer, b4TargetStoreStateAbsent, logicalExpiry)
}

type b4RecoveryFixture struct {
	agentPath        string
	enforcerPath     string
	configPath       string
	databasePath     string
	readbackPath     string
	operationLogPath string
}

func b4NewRecoveryFixture(t *testing.T, identity b4GuardIdentity) b4RecoveryFixture {
	t.Helper()
	root := b4ProjectRoot(t)
	fixtureRoot, err := os.MkdirTemp("/tmp", "guard-binary-recovery-")
	if err != nil {
		t.Fatalf("create binary recovery fixture: %v", err)
	}
	if err := os.Chmod(fixtureRoot, 0o733); err != nil {
		t.Fatalf("make binary recovery fixture accessible to guard: %v", err)
	}
	stateDirectory := filepath.Join(fixtureRoot, "state")
	t.Cleanup(func() {
		command := exec.Command("sh", "-c", "rm -rf \"$1\"", "guard-cleanup", stateDirectory)
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: identity.uid, Gid: identity.gid}}
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("remove binary recovery state: %v; output: %s", err, bytes.TrimSpace(output))
		}
		if err := os.RemoveAll(fixtureRoot); err != nil {
			t.Errorf("remove binary recovery fixture: %v", err)
		}
	})
	b4RunAsGuard(t, identity, nil, "mkdir", "-m", "755", stateDirectory)
	databasePath := filepath.Join(stateDirectory, "guard.db")
	configPath := filepath.Join(fixtureRoot, "guard.yaml")
	config := []byte("store:\n  database_path: " + databasePath + "\nruntime:\n  reconcile_queue_capacity: 1\n")
	b4RunAsGuard(t, identity, config, "sh", "-c", "umask 077; cat > \"$1\"", "guard-config", configPath)

	binDirectory := filepath.Join(fixtureRoot, "bin")
	if err := os.Mkdir(binDirectory, 0o755); err != nil {
		t.Fatalf("create binary recovery bin directory: %v", err)
	}
	enforcerPath := filepath.Join(binDirectory, "guard-enforcer")
	agentPath := filepath.Join(binDirectory, "guard-agent")
	readbackPath := filepath.Join(binDirectory, "guard-store-readback")
	b4BuildBinary(t, root, enforcerPath, "./cmd/guard-enforcer", "integration,nftables")
	b4BuildBinary(t, root, agentPath, "./cmd/guard-agent", "")
	b4CopyReadbackHelper(t, os.Args[0], readbackPath, identity)
	return b4RecoveryFixture{
		agentPath: agentPath, enforcerPath: enforcerPath, configPath: configPath, databasePath: databasePath,
		readbackPath: readbackPath, operationLogPath: filepath.Join(fixtureRoot, "completed-operations.log"),
	}
}

func b4CopyReadbackHelper(t *testing.T, source, destination string, identity b4GuardIdentity) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatalf("open Store readback helper: %v", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatalf("create Store readback helper: %v", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatalf("copy Store readback helper: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close Store readback helper: %v", err)
	}
	if err := os.Chown(destination, int(identity.uid), int(identity.gid)); err != nil {
		t.Fatalf("assign Store readback helper to guard: %v", err)
	}
}

func b4RunAsGuard(t *testing.T, identity b4GuardIdentity, input []byte, program string, arguments ...string) {
	t.Helper()
	command := exec.Command(program, arguments...)
	command.Stdin = bytes.NewReader(input)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: identity.uid, Gid: identity.gid}}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run %s as guard: %v; output: %s", program, err, bytes.TrimSpace(output))
	}
}

func b4ProjectRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve binary recovery source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func b4BuildBinary(t *testing.T, root, output, packagePath, buildTags string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), b4BinaryRecoveryTimeout)
	defer cancel()
	arguments := []string{"build", "-o", output}
	if buildTags != "" {
		arguments = append(arguments, "-tags", buildTags)
	}
	arguments = append(arguments, packagePath)
	command := exec.CommandContext(ctx, "go", arguments...)
	command.Dir = root
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v; output: %s", packagePath, err, bytes.TrimSpace(result))
	}
}

func b4LookupGuardIdentity(t *testing.T) b4GuardIdentity {
	t.Helper()
	account, err := user.Lookup("guard")
	if err != nil {
		t.Fatalf("lookup fixture guard identity: %v", err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		t.Fatalf("parse fixture guard UID: %v", err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		t.Fatalf("parse fixture guard GID: %v", err)
	}
	return b4GuardIdentity{uid: uint32(uid), gid: uint32(gid)}
}

func b4StartProcess(t *testing.T, name, binary string, arguments []string, identity b4GuardIdentity) *b4ManagedProcess {
	return b4StartProcessWithEnvironment(t, name, binary, arguments, identity, nil)
}

func b4StartProcessWithEnvironment(t *testing.T, name, binary string, arguments []string, identity b4GuardIdentity, environment []string) *b4ManagedProcess {
	t.Helper()
	process := &b4ManagedProcess{name: name, done: make(chan error, 1)}
	process.command = exec.Command(binary, arguments...)
	process.command.Stdout = &process.output
	process.command.Stderr = &process.output
	if len(environment) != 0 {
		process.command.Env = append(os.Environ(), environment...)
	}
	if identity.uid != 0 || identity.gid != 0 {
		process.credential = &identity
		process.command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: identity.uid, Gid: identity.gid}}
	}
	if err := process.command.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	go func() { process.done <- process.command.Wait() }()
	return process
}

func b4KillAndAssertSIGKILL(t *testing.T, process *b4ManagedProcess) {
	t.Helper()
	if err := b4SignalProcess(process, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL %s: %v", process.name, err)
	}
	err := b4WaitProcess(t, process)
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("%s exit after SIGKILL = %v, want signal", process.name, err)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("%s wait status after SIGKILL = %v", process.name, exitError.Sys())
	}
}

func b4StopProcess(t *testing.T, process *b4ManagedProcess) {
	t.Helper()
	if process == nil || process.finished {
		return
	}
	if err := b4SignalProcess(process, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("stop %s: %v", process.name, err)
	}
	select {
	case <-process.done:
		process.finished = true
	case <-time.After(5 * time.Second):
		_ = b4SignalProcess(process, syscall.SIGKILL)
		select {
		case <-process.done:
			process.finished = true
		case <-time.After(5 * time.Second):
			t.Errorf("%s did not stop", process.name)
		}
	}
}

func b4SignalProcess(process *b4ManagedProcess, signal syscall.Signal) error {
	if process.credential == nil {
		return process.command.Process.Signal(signal)
	}
	command := exec.Command("sh", "-c", "kill -\"$1\" \"$2\"", "guard-signal", strconv.Itoa(int(signal)), strconv.Itoa(process.command.Process.Pid))
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: process.credential.uid, Gid: process.credential.gid}}
	return command.Run()
}

func b4WaitProcess(t *testing.T, process *b4ManagedProcess) error {
	t.Helper()
	select {
	case err := <-process.done:
		process.finished = true
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not exit; output: %s", process.name, bytes.TrimSpace(process.output.Bytes()))
		return nil
	}
}

func b4WaitForLiveRuntime(t *testing.T, databasePath string, identity b4GuardIdentity, agent, enforcer *b4ManagedProcess) {
	t.Helper()
	deadline := time.Now().Add(b4BinaryRecoveryTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(databasePath); err == nil {
			if err := b4NftablesCommand(context.Background(), nil, "--json", "list", "table", "inet", "guard"); err == nil {
				b4AssertSocket(t, identity, 0)
				b4AssertProcessAlive(t, agent)
				b4AssertProcessAlive(t, enforcer)
				time.Sleep(100 * time.Millisecond)
				b4AssertProcessAlive(t, agent)
				b4AssertProcessAlive(t, enforcer)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("guard-agent runtime did not become live: database %s; agent output: %s; enforcer output: %s", databasePath, bytes.TrimSpace(agent.output.Bytes()), bytes.TrimSpace(enforcer.output.Bytes()))
}

func b4AssertProcessAlive(t *testing.T, process *b4ManagedProcess) {
	t.Helper()
	select {
	case err := <-process.done:
		process.finished = true
		t.Fatalf("%s exited before recovery check: %v; output: %s", process.name, err, bytes.TrimSpace(process.output.Bytes()))
	default:
	}
}

func b4WaitForReplacementAgentReconcile(t *testing.T, helperPath, databasePath string, identity b4GuardIdentity, agent, enforcer *b4ManagedProcess, previousSignature string) {
	t.Helper()
	b4WaitForLiveRuntime(t, databasePath, identity, agent, enforcer)
	deadline := time.Now().Add(b4BinaryRecoveryTimeout)
	for time.Now().Before(deadline) {
		b4AssertProcessAlive(t, agent)
		b4AssertProcessAlive(t, enforcer)
		if signature := b4StoreSignature(t, helperPath, databasePath, identity); signature != previousSignature {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not persist a fresh reconcile state", agent.name)
}

type b4CompletedOperation struct {
	pid       int
	operation string
	domain    string
}

func b4WaitForHealthSourceReady(t *testing.T, logPath string, enforcerPID int, agent, enforcer *b4ManagedProcess) {
	t.Helper()
	deadline := time.Now().Add(b4BinaryRecoveryTimeout)
	for time.Now().Before(deadline) {
		operations := b4CompletedOperations(t, logPath, enforcerPID)
		lastSnapshot := -1
		for index, operation := range operations {
			if operation.operation == "SnapshotManaged" {
				lastSnapshot = index
			}
		}
		if lastSnapshot >= 0 && lastSnapshot+1 < len(operations) && operations[lastSnapshot+1].operation == "ProbeCapabilities" {
			b4AssertProcessAlive(t, agent)
			b4AssertProcessAlive(t, enforcer)
			return
		}
		b4AssertProcessAlive(t, agent)
		b4AssertProcessAlive(t, enforcer)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Agent did not complete a standalone IPC health observation")
}

func b4WaitForHealthSourceUnavailability(t *testing.T, agent *b4ManagedProcess) {
	t.Helper()
	// The production source polls every second. Two complete intervals prevent
	// a replacement socket from racing the first unavailable observation.
	deadline := time.Now().Add(2*time.Second + 200*time.Millisecond)
	for time.Now().Before(deadline) {
		b4AssertProcessAlive(t, agent)
		time.Sleep(50 * time.Millisecond)
	}
}

func b4WaitForHealthSourceRecovery(
	t *testing.T,
	fixture b4RecoveryFixture,
	identity b4GuardIdentity,
	agent, enforcer *b4ManagedProcess,
	previousSignature string,
) {
	t.Helper()
	want := []string{"ProbeCapabilities", "ProbeCapabilities", "SnapshotManaged"}
	deadline := time.Now().Add(b4BinaryRecoveryTimeout)
	for time.Now().Before(deadline) {
		operations := b4CompletedOperations(t, fixture.operationLogPath, enforcer.command.Process.Pid)
		if len(operations) >= len(want) {
			for index, expected := range want {
				if operations[index].operation != expected {
					t.Fatalf("recovery operation %d = %q, want %q", index, operations[index].operation, expected)
				}
			}
			for _, operation := range operations {
				if operation.operation == "ApplyManagedPlan" || operation.operation == "RemoveManagedInfrastructure" {
					t.Fatalf("observation-only recovery performed %s", operation.operation)
				}
			}
			if signature := b4StoreSignature(t, fixture.readbackPath, fixture.databasePath, identity); signature != previousSignature {
				b4AssertProcessAlive(t, agent)
				b4AssertProcessAlive(t, enforcer)
				return
			}
		}
		b4AssertProcessAlive(t, agent)
		b4AssertProcessAlive(t, enforcer)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("IPC health recovery did not persist one authoritative observation")
}

func b4RunTargetLifecycleHelper(t *testing.T, fixture b4RecoveryFixture, identity b4GuardIdentity, action string) time.Time {
	t.Helper()
	command := exec.Command(fixture.readbackPath, "-test.run", "^TestB4TargetLifecycleHelper$")
	command.Env = append(os.Environ(),
		"GUARD_B4_STORE_DATABASE="+fixture.databasePath,
		b4TargetLifecycleEnv+"="+action,
	)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: identity.uid, Gid: identity.gid}}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run Target lifecycle helper %q: %v; output: %s", action, err, bytes.TrimSpace(output))
	}
	if action != b4TargetLifecycleBan {
		return time.Time{}
	}
	for _, line := range strings.Split(string(output), "\n") {
		if value, found := strings.CutPrefix(line, "B4_TARGET_LOGICAL_EXPIRY="); found {
			microseconds, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || microseconds <= 0 {
				t.Fatalf("Target lifecycle logical expiry = %q", value)
			}
			return time.UnixMicro(microseconds).UTC()
		}
	}
	t.Fatalf("Target lifecycle helper did not emit logical expiry: %s", bytes.TrimSpace(output))
	return time.Time{}
}

func b4ClearCompletedOperationLog(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("clear completed IPC operation log: %v", err)
	}
}

func b4AssertTargetSnapshot(t *testing.T, fixture b4RecoveryFixture, identity b4GuardIdentity, state string, logicalExpiry time.Time) {
	t.Helper()
	command := exec.Command(fixture.readbackPath, "-test.run", "^TestB4TargetSnapshotHelper$")
	command.Env = append(os.Environ(),
		b4TargetSnapshotStateEnv+"="+state,
		b4TargetLogicalExpiryEnv+"="+strconv.FormatInt(logicalExpiry.UnixMicro(), 10),
	)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: identity.uid, Gid: identity.gid}}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Target IPC snapshot %q: %v; output: %s", state, err, bytes.TrimSpace(output))
	}
}

func b4WaitForTargetPresent(
	t *testing.T,
	fixture b4RecoveryFixture,
	identity b4GuardIdentity,
	agent, enforcer *b4ManagedProcess,
	logicalExpiry time.Time,
) {
	t.Helper()
	target := netip.MustParsePrefix(b4NativeTimeoutTarget)
	deadline := time.Now().Add(b4BinaryRecoveryTimeout)
	for time.Now().Before(deadline) {
		operations := b4CompletedOperations(t, fixture.operationLogPath, enforcer.command.Process.Pid)
		if b4HasTargetApply(operations) {
			b4AssertTargetSnapshot(t, fixture, identity, b4TargetStoreStatePresent, logicalExpiry)
			b4AssertNftTargetPresent(t, target)
			b4AssertProcessAlive(t, agent)
			b4AssertProcessAlive(t, enforcer)
			return
		}
		b4AssertProcessAlive(t, agent)
		b4AssertProcessAlive(t, enforcer)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("guard-agent did not issue a Target ApplyManagedPlan")
}

func b4WaitForTargetAbsent(t *testing.T, fixture b4RecoveryFixture, identity b4GuardIdentity, agent, enforcer *b4ManagedProcess) {
	t.Helper()
	target := netip.MustParsePrefix(b4NativeTimeoutTarget)
	deadline := time.Now().Add(b4BinaryRecoveryTimeout)
	for time.Now().Before(deadline) {
		operations := b4CompletedOperations(t, fixture.operationLogPath, enforcer.command.Process.Pid)
		if b4HasTargetApply(operations) {
			b4AssertTargetSnapshot(t, fixture, identity, b4TargetStoreStateAbsent, time.Time{})
			b4AssertNftTargetAbsent(t, target)
			b4AssertProcessAlive(t, agent)
			b4AssertProcessAlive(t, enforcer)
			return
		}
		b4AssertProcessAlive(t, agent)
		b4AssertProcessAlive(t, enforcer)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("guard-agent did not issue a Target removal ApplyManagedPlan")
}

func b4HasTargetApply(operations []b4CompletedOperation) bool {
	for _, operation := range operations {
		if operation.operation == "ApplyManagedPlan" && operation.domain == string(ipc.DomainTarget) {
			return true
		}
	}
	return false
}

func b4AssertRuntimeTargetNativeTimeout(t *testing.T, targets []firewall.TargetObservation, target netip.Prefix, logicalExpiry time.Time) {
	t.Helper()
	want := logicalExpiry.Add(enforcement.M0SafetyGrace)
	for _, observed := range targets {
		if observed.Target() != target {
			continue
		}
		if observed.TimeoutMode() != firewall.ManagedTimeoutNative || len(observed.Scopes()) != 2 {
			t.Fatalf("native target observation = %#v", observed)
		}
		expiry, found := observed.EffectiveUntilUnixMicro()
		if !found {
			t.Fatal("native target observation has no physical expiry")
		}
		got := time.UnixMicro(expiry).UTC()
		if delta := got.Sub(want); delta < -b4NativeExpiryTolerance || delta > b4NativeExpiryTolerance {
			t.Fatalf("native target expiry = %s, want %s within %s", got, want, b4NativeExpiryTolerance)
		}
		return
	}
	t.Fatalf("native target %s was not observed", target)
}

func b4AssertRuntimeTargetAbsent(t *testing.T, targets []firewall.TargetObservation, target netip.Prefix) {
	t.Helper()
	for _, observed := range targets {
		if observed.Target() == target {
			t.Fatalf("removed target survived IPC snapshot: %#v", observed)
		}
	}
}

func b4AssertNftTargetPresent(t *testing.T, target netip.Prefix) {
	t.Helper()
	if count := bytes.Count(b4GuardTableJSON(t), []byte(target.Addr().String())); count != 2 {
		t.Fatalf("native target %s occurrence count = %d, want 2 INPUT/FORWARD elements", target, count)
	}
}

func b4AssertNftTargetAbsent(t *testing.T, target netip.Prefix) {
	t.Helper()
	if count := bytes.Count(b4GuardTableJSON(t), []byte(target.Addr().String())); count != 0 {
		t.Fatalf("removed target %s occurrence count = %d, want 0", target, count)
	}
}

func b4WaitForTargetStoreReadback(
	t *testing.T,
	fixture b4RecoveryFixture,
	identity b4GuardIdentity,
	agent, enforcer *b4ManagedProcess,
	state string,
	logicalExpiry time.Time,
) {
	t.Helper()
	deadline := time.Now().Add(b4BinaryRecoveryTimeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	var lastErr error
	for time.Now().Before(deadline) {
		if err := b4TargetStoreReadback(ctx, fixture, identity, state, logicalExpiry); err == nil {
			return
		} else {
			lastErr = err
		}
		b4AssertProcessAlive(t, agent)
		b4AssertProcessAlive(t, enforcer)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Target Store readback %q did not converge: %v", state, lastErr)
}

func b4TargetStoreReadback(ctx context.Context, fixture b4RecoveryFixture, identity b4GuardIdentity, state string, logicalExpiry time.Time) error {
	command := exec.CommandContext(ctx, fixture.readbackPath, "-test.run", "^TestB4StoreReadbackHelper$")
	command.Env = append(os.Environ(),
		"GUARD_B4_STORE_READBACK=1",
		"GUARD_B4_STORE_DATABASE="+fixture.databasePath,
		b4TargetStoreStateEnv+"="+state,
		b4TargetLogicalExpiryEnv+"="+strconv.FormatInt(logicalExpiry.UnixMicro(), 10),
	)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: identity.uid, Gid: identity.gid}}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%w; output: %s", err, bytes.TrimSpace(output))
	}
	return nil
}

func b4CompletedOperations(t *testing.T, logPath string, wantPID int) []b4CompletedOperation {
	t.Helper()
	content, err := os.ReadFile(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read completed IPC operation log: %v", err)
	}
	lines := strings.Split(string(content), "\n")
	result := make([]b4CompletedOperation, 0, len(lines))
	for index, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 && len(fields) != 3 {
			if index == len(lines)-1 {
				continue
			}
			t.Fatalf("completed IPC operation log line = %q", line)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			t.Fatalf("completed IPC operation PID = %q", fields[0])
		}
		if pid == wantPID {
			entry := b4CompletedOperation{pid: pid, operation: fields[1]}
			if len(fields) == 3 {
				entry.domain = fields[2]
			}
			result = append(result, entry)
		}
	}
	return result
}

func b4WaitForSocket(t *testing.T, identity b4GuardIdentity, previousInode uint64) {
	t.Helper()
	deadline := time.Now().Add(b4BinaryRecoveryTimeout)
	for time.Now().Before(deadline) {
		info, err := os.Lstat(ipc.EnforcerSocketPath)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			inode := b4SocketInode(t)
			if previousInode == 0 || inode != previousInode {
				b4AssertSocket(t, identity, previousInode)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("guard-enforcer socket did not become ready")
}

func b4AssertSocket(t *testing.T, identity b4GuardIdentity, previousInode uint64) {
	t.Helper()
	info, err := os.Lstat(ipc.EnforcerSocketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o660 {
		t.Fatalf("enforcer socket = info=%v err=%v", info, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != identity.gid {
		t.Fatalf("enforcer socket ownership = %#v", info.Sys())
	}
	if previousInode != 0 && uint64(stat.Ino) == previousInode {
		t.Fatal("enforcer socket inode was not replaced after restart")
	}
}

func b4SocketInode(t *testing.T) uint64 {
	t.Helper()
	info, err := os.Lstat(ipc.EnforcerSocketPath)
	if err != nil {
		t.Fatalf("stat enforcer socket: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("enforcer socket stat = %#v", info.Sys())
	}
	return uint64(stat.Ino)
}

func b4AssertStaleSocket(t *testing.T, identity b4GuardIdentity, inode uint64) {
	t.Helper()
	b4AssertSocket(t, identity, 0)
	if b4SocketInode(t) != inode {
		t.Fatal("stale enforcer socket inode changed before replacement")
	}
	connection, err := net.DialTimeout("unix", ipc.EnforcerSocketPath, 250*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("enforcer socket remained active after SIGKILL")
	}
}

func b4AssertGuardTable(t *testing.T) {
	t.Helper()
	_ = b4GuardTableJSON(t)
	command := exec.Command("nft", "list", "tables")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list nftables tables: %v", err)
	}
	if bytes.Count(output, []byte("table inet guard\n")) != 1 {
		t.Fatalf("Guard table identity count = %d; tables: %s", bytes.Count(output, []byte("table inet guard\n")), bytes.TrimSpace(output))
	}
}

func b4GuardTableJSON(t *testing.T) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "nft", "--json", "list", "table", "inet", "guard")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("read Guard table: %v", err)
	}
	return output
}

func b4AssertSQLiteReopen(t *testing.T, helperPath, databasePath string, identity b4GuardIdentity) {
	t.Helper()
	_ = b4RunStoreReadbackHelper(t, helperPath, databasePath, identity, false)
}

func b4StoreSignature(t *testing.T, helperPath, databasePath string, identity b4GuardIdentity) string {
	t.Helper()
	output := b4RunStoreReadbackHelper(t, helperPath, databasePath, identity, true)
	for _, line := range strings.Split(string(output), "\n") {
		if signature, found := strings.CutPrefix(line, "B4_STORE_SIGNATURE="); found {
			return signature
		}
	}
	t.Fatalf("Store readback helper did not emit a signature: %s", bytes.TrimSpace(output))
	return ""
}

func b4RunStoreReadbackHelper(t *testing.T, helperPath, databasePath string, identity b4GuardIdentity, wantSignature bool) []byte {
	t.Helper()
	command := exec.Command(helperPath, "-test.run", "^TestB4StoreReadbackHelper$")
	command.Env = append(os.Environ(),
		"GUARD_B4_STORE_READBACK=1",
		"GUARD_B4_STORE_DATABASE="+databasePath,
	)
	if wantSignature {
		command.Env = append(command.Env, "GUARD_B4_STORE_SIGNATURE=1")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: identity.uid, Gid: identity.gid}}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("reopen SQLite state as guard: %v; output: %s", err, bytes.TrimSpace(output))
		return nil
	}
	return output
}

// TestB4StoreReadbackHelper runs in a fresh guard-owned test process. It
// proves that the killed process's SQLite file can be opened through the
// production Store and that the clean-target baseline can be decoded.
func TestB4StoreReadbackHelper(t *testing.T) {
	if os.Getenv("GUARD_B4_STORE_READBACK") != "1" {
		t.Skip("helper process only")
	}
	databasePath := os.Getenv("GUARD_B4_STORE_DATABASE")
	if databasePath == "" {
		t.Fatal("missing helper database path")
	}
	database, err := store.Open(context.Background(), databasePath, migrations.FS)
	if err != nil {
		t.Fatalf("Store.Open(): %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Errorf("Store.Close(): %v", err)
		}
	}()
	nodeID, found, err := database.LoadNodeIdentity(context.Background())
	if err != nil || !found {
		t.Fatalf("LoadNodeIdentity() = (%q, %v, %v), want persisted identity", nodeID, found, err)
	}
	if state := os.Getenv(b4TargetStoreStateEnv); state != "" {
		b4AssertTargetStoreState(t, database, nodeID, state)
		return
	}
	desired, err := database.LoadDesiredFirewallState(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("LoadDesiredFirewallState(): %v", err)
	}
	if desired.PolicyRevision == 0 || desired.Policy.ValidateComplete() != nil || len(desired.Targets) != 0 {
		t.Fatalf("clean-target desired state = %#v", desired)
	}
	observed, err := database.LoadObservedFirewallSnapshot(context.Background(), nodeID)
	if err != nil || observed.NodeID != nodeID || observed.Infrastructure == nil || observed.Policy == nil || len(observed.Targets) != 0 {
		t.Fatalf("clean-target observed state = %#v, err=%v", observed, err)
	}
	if err := observed.Infrastructure.Validate(); err != nil {
		t.Fatalf("clean-target infrastructure observation: %v", err)
	}
	if err := observed.Policy.Validate(); err != nil {
		t.Fatalf("clean-target policy observation: %v", err)
	}
	recovery, err := database.LoadReconcileRecovery(context.Background(), nodeID)
	if err != nil || len(recovery.States) != 2 || len(recovery.ProbeRequirements) != 0 {
		t.Fatalf("clean-target recovery state = %#v, err=%v", recovery, err)
	}
	if os.Getenv("GUARD_B4_STORE_SIGNATURE") == "1" {
		fmt.Printf("B4_STORE_SIGNATURE=%d:%d:%d:%d\n",
			observed.Infrastructure.ObservedAt.UnixMicro(), observed.Policy.ObservedAt.UnixMicro(),
			recovery.States[0].UpdatedAt.UnixMicro(), recovery.States[1].UpdatedAt.UnixMicro())
	}
}

func b4AssertTargetStoreState(t *testing.T, database *store.Store, nodeID core.NodeID, state string) {
	t.Helper()
	target := netip.MustParsePrefix(b4NativeTimeoutTarget)
	desired, err := database.LoadDesiredFirewallState(context.Background(), nodeID)
	if err != nil || desired.PolicyRevision == 0 || desired.Policy.ValidateComplete() != nil || len(desired.Targets) != 1 {
		t.Fatalf("target desired state = %#v, err=%v", desired, err)
	}
	intent := desired.Targets[0]
	if intent.CanonicalTarget != target || intent.Scopes != core.ScopeInput|core.ScopeForward || intent.AddressFamily != core.AddressFamilyIPv4 {
		t.Fatalf("target desired intent = %#v", intent)
	}
	observed, err := database.LoadObservedFirewallSnapshot(context.Background(), nodeID)
	if err != nil || observed.NodeID != nodeID || len(observed.Targets) != 1 {
		t.Fatalf("target observed state = %#v, err=%v", observed, err)
	}
	physical := observed.Targets[0]
	recovery, err := database.LoadReconcileRecovery(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("target recovery state: %v", err)
	}
	var targetState *core.PersistedReconcileState
	for index := range recovery.States {
		candidate := &recovery.States[index]
		if candidate.Domain == core.ReconcileDomainTarget && candidate.Target == target {
			targetState = candidate
			break
		}
	}
	if targetState == nil || targetState.RetryState.Status != core.ReconcileConverged {
		t.Fatalf("target recovery state = %#v", recovery)
	}
	for _, probe := range recovery.ProbeRequirements {
		if probe.Domain == core.ReconcileDomainTarget && probe.Target == target {
			t.Fatalf("target Probe requirement survived convergence: %#v", probe)
		}
	}

	switch state {
	case b4TargetStoreStatePresent:
		logicalExpiry, err := strconv.ParseInt(os.Getenv(b4TargetLogicalExpiryEnv), 10, 64)
		if err != nil || logicalExpiry <= 0 || intent.BanMembership != core.BanPresent ||
			intent.TimeoutMode != core.TimeoutNative || intent.EffectiveUntil == nil ||
			intent.EffectiveUntil.UTC().UnixMicro() != logicalExpiry || intent.Generation != 1 ||
			targetState.TargetGeneration != 1 || physical.BanMembership != core.ObservedMembershipPresent ||
			physical.TimeoutMode != core.TimeoutNative || physical.ConfirmedGeneration != 1 {
			t.Fatalf("present Target state = intent=%#v observed=%#v recovery=%#v", intent, physical, targetState)
		}
		want := time.UnixMicro(logicalExpiry).UTC().Add(enforcement.M0SafetyGrace)
		if physical.NativeExpiry == nil || physical.Scopes != core.ScopeInput|core.ScopeForward {
			t.Fatalf("present Target physical state = %#v", physical)
		}
		if delta := physical.NativeExpiry.Sub(want); delta < -b4NativeExpiryTolerance || delta > b4NativeExpiryTolerance {
			t.Fatalf("Store native expiry = %s, want %s within %s", physical.NativeExpiry.UTC(), want, b4NativeExpiryTolerance)
		}
	case b4TargetStoreStateAbsent:
		if intent.BanMembership != core.BanAbsent || intent.TimeoutMode != core.TimeoutNone || intent.EffectiveUntil != nil ||
			intent.Generation != 2 || targetState.TargetGeneration != 2 || physical.BanMembership != core.ObservedMembershipAbsent ||
			physical.ConfirmedGeneration != 2 {
			t.Fatalf("absent Target state = intent=%#v observed=%#v recovery=%#v", intent, physical, targetState)
		}
	default:
		t.Fatalf("unsupported Target Store state %q", state)
	}
}

// TestB4TargetLifecycleHelper runs as the guard identity while no Agent owns
// the Store. It uses the production Decision lifecycle to materialize the
// test-only Target intent, then later expires that Decision for removal.
func TestB4TargetLifecycleHelper(t *testing.T) {
	action := os.Getenv(b4TargetLifecycleEnv)
	if action == "" {
		t.Skip("helper process only")
	}
	databasePath := os.Getenv("GUARD_B4_STORE_DATABASE")
	if databasePath == "" {
		t.Fatal("missing helper database path")
	}
	database, err := store.Open(context.Background(), databasePath, migrations.FS)
	if err != nil {
		t.Fatalf("Store.Open(): %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Errorf("Store.Close(): %v", err)
		}
	}()
	nodeID, found, err := database.LoadNodeIdentity(context.Background())
	if err != nil || !found {
		t.Fatalf("LoadNodeIdentity() = (%q, %v, %v), want persisted identity", nodeID, found, err)
	}
	resolver, err := nftables.NewFixedManagedPolicyTargetResolver()
	if err != nil {
		t.Fatalf("NewFixedManagedPolicyTargetResolver(): %v", err)
	}
	finalizer, err := decision.NewDesiredStateFinalizer(resolver)
	if err != nil {
		t.Fatalf("NewDesiredStateFinalizer(): %v", err)
	}
	lifecycle, err := decision.NewLifecycleService(nodeID, database, finalizer,
		decision.TargetWakeSinkFunc(func(context.Context, core.NodeID, netip.Prefix) error { return nil }))
	if err != nil {
		t.Fatalf("NewLifecycleService(): %v", err)
	}
	target := netip.MustParsePrefix(b4NativeTimeoutTarget)
	switch action {
	case b4TargetLifecycleBan:
		createdAt := time.Now().UTC().Truncate(time.Microsecond)
		expiresAt := createdAt.Add(b4NativeTimeoutDecisionTTL)
		result, err := lifecycle.BanManual(context.Background(), decision.ManualRequest{
			DecisionID: b4NativeTimeoutDecisionID, NodeID: nodeID, Target: target, CreatedAt: createdAt, ExpiresAt: &expiresAt,
		}, false)
		if err != nil || len(result.EnforcementChanges) != 1 || result.EnforcementChanges[0].Target != target || result.EnforcementChanges[0].Generation != 1 {
			t.Fatalf("BanManual() = %#v, %v", result, err)
		}
		fmt.Printf("B4_TARGET_LOGICAL_EXPIRY=%d\n", expiresAt.UnixMicro())
	case b4TargetLifecycleExpire:
		result, err := lifecycle.Expire(context.Background(), time.Now().UTC().Add(2*b4NativeTimeoutDecisionTTL))
		if err != nil || len(result.Expired) != 1 || len(result.EnforcementChanges) != 1 ||
			result.EnforcementChanges[0].Target != target || result.EnforcementChanges[0].Generation != 2 {
			t.Fatalf("Expire() = %#v, %v", result, err)
		}
	default:
		t.Fatalf("unsupported Target lifecycle action %q", action)
	}
}

// TestB4TargetSnapshotHelper runs as the guard identity so the production
// Enforcer validates the same peer credentials used by guard-agent.
func TestB4TargetSnapshotHelper(t *testing.T) {
	state := os.Getenv(b4TargetSnapshotStateEnv)
	if state == "" {
		t.Skip("helper process only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), b4BinaryRecoveryTimeout)
	defer cancel()
	snapshot, err := ipc.RoundTripSnapshotManaged(ctx)
	if err != nil {
		t.Fatalf("RoundTripSnapshotManaged(): %v", err)
	}
	target := netip.MustParsePrefix(b4NativeTimeoutTarget)
	switch state {
	case b4TargetStoreStatePresent:
		logicalExpiry, err := strconv.ParseInt(os.Getenv(b4TargetLogicalExpiryEnv), 10, 64)
		if err != nil || logicalExpiry <= 0 {
			t.Fatalf("Target logical expiry = %q", os.Getenv(b4TargetLogicalExpiryEnv))
		}
		b4AssertRuntimeTargetNativeTimeout(t, snapshot.ManagedState().Targets(), target, time.UnixMicro(logicalExpiry).UTC())
	case b4TargetStoreStateAbsent:
		b4AssertRuntimeTargetAbsent(t, snapshot.ManagedState().Targets(), target)
	default:
		t.Fatalf("unsupported Target snapshot state %q", state)
	}
}
