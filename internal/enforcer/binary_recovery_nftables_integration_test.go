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

	"github.com/lifei6671/guard-wall/internal/ipc"
	"github.com/lifei6671/guard-wall/internal/store"
	"github.com/lifei6671/guard-wall/migrations"
)

const b4BinaryRecoveryTimeout = 15 * time.Second

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

type b4RecoveryFixture struct {
	agentPath    string
	enforcerPath string
	configPath   string
	databasePath string
	readbackPath string
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
	b4BuildBinary(t, root, enforcerPath, "./cmd/guard-enforcer")
	b4BuildBinary(t, root, agentPath, "./cmd/guard-agent")
	b4CopyReadbackHelper(t, os.Args[0], readbackPath, identity)
	return b4RecoveryFixture{agentPath: agentPath, enforcerPath: enforcerPath, configPath: configPath, databasePath: databasePath, readbackPath: readbackPath}
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

func b4BuildBinary(t *testing.T, root, output, packagePath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), b4BinaryRecoveryTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", output, packagePath)
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
	t.Helper()
	process := &b4ManagedProcess{name: name, done: make(chan error, 1)}
	process.command = exec.Command(binary, arguments...)
	process.command.Stdout = &process.output
	process.command.Stderr = &process.output
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
	if err := b4NftablesCommand(context.Background(), nil, "--json", "list", "table", "inet", "guard"); err != nil {
		t.Fatalf("read Guard table: %v", err)
	}
	command := exec.Command("nft", "list", "tables")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list nftables tables: %v", err)
	}
	if bytes.Count(output, []byte("table inet guard\n")) != 1 {
		t.Fatalf("Guard table identity count = %d; tables: %s", bytes.Count(output, []byte("table inet guard\n")), bytes.TrimSpace(output))
	}
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
