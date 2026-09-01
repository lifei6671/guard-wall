//go:build linux

package ipc

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const listenerTestTimeout = 5 * time.Second

func TestListenerContractConstants(t *testing.T) {
	if EnforcerSocketPath != "/run/guard/enforcer.sock" {
		t.Fatalf("EnforcerSocketPath = %q, want /run/guard/enforcer.sock", EnforcerSocketPath)
	}

	tests := []struct {
		name string
		got  ListenerErrorCode
		want string
	}{
		{name: "directory invalid", got: ListenerErrorCodeDirectoryInvalid, want: "directory_invalid"},
		{name: "socket invalid", got: ListenerErrorCodeSocketInvalid, want: "socket_invalid"},
		{name: "socket active", got: ListenerErrorCodeSocketActive, want: "socket_active"},
		{name: "socket probe failed", got: ListenerErrorCodeSocketProbeFailed, want: "socket_probe_failed"},
		{name: "listen failed", got: ListenerErrorCodeListenFailed, want: "listen_failed"},
		{name: "socket configure failed", got: ListenerErrorCodeSocketConfigureFailed, want: "socket_configure_failed"},
		{name: "accept failed", got: ListenerErrorCodeAcceptFailed, want: "accept_failed"},
		{name: "context canceled", got: ListenerErrorCodeContextCanceled, want: "context_canceled"},
		{name: "context deadline exceeded", got: ListenerErrorCodeContextDeadlineExceeded, want: "context_deadline_exceeded"},
		{name: "close failed", got: ListenerErrorCodeCloseFailed, want: "close_failed"},
		{name: "cleanup failed", got: ListenerErrorCodeCleanupFailed, want: "cleanup_failed"},
		{name: "socket replaced", got: ListenerErrorCodeSocketReplaced, want: "socket_replaced"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(test.got); got != test.want {
				t.Fatalf("constant = %q, want %q", got, test.want)
			}
		})
	}
}

func TestListenUnixAtCreatesAndVerifiesSocketPermissions(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "guard")
	path := filepath.Join(parent, "enforcer.sock")
	listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatalf("listenUnixAt() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	assertUnixObject(t, parent, os.ModeDir, 0o750, uint32(os.Getuid()), uint32(os.Getgid()))
	assertUnixObject(t, path, os.ModeSocket, 0o660, uint32(os.Getuid()), uint32(os.Getgid()))
}

func TestEnsureSocketDirectoryWithOpsRollsBackOnlyCreatedDirectory(t *testing.T) {
	sentinel := errors.New("injected directory setup failure")
	tests := []struct {
		name string
		ops  directorySetupOps
	}{
		{
			name: "chown failure",
			ops: directorySetupOps{
				chown: func(string, int, int) error { return sentinel },
				chmod: os.Chmod,
			},
		},
		{
			name: "chmod failure",
			ops: directorySetupOps{
				chown: func(string, int, int) error { return nil },
				chmod: func(string, os.FileMode) error { return sentinel },
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			directory := filepath.Join(parent, "guard")
			err := ensureSocketDirectoryWithOps(directory, os.Getuid(), os.Getgid(), test.ops)
			assertListenerError(t, err, ListenerErrorCodeDirectoryInvalid)
			if _, statErr := os.Lstat(directory); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("created directory survived setup failure: %v", statErr)
			}
			if info := mustLstat(t, parent); !info.IsDir() {
				t.Fatalf("pre-existing parent mode = %v, want retained directory", info.Mode())
			}
		})
	}

	t.Run("replacement inode survives", func(t *testing.T) {
		parent := t.TempDir()
		directory := filepath.Join(parent, "guard")
		markerPath := filepath.Join(directory, "replacement-marker")
		var replacement os.FileInfo
		err := ensureSocketDirectoryWithOps(directory, os.Getuid(), os.Getgid(), directorySetupOps{
			chown: func(path string, _, _ int) error {
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove created directory in hook: %v", err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("create replacement directory in hook: %v", err)
				}
				if err := os.WriteFile(markerPath, []byte("replacement"), 0o600); err != nil {
					t.Fatalf("write replacement marker in hook: %v", err)
				}
				replacement = mustLstat(t, path)
				return sentinel
			},
			chmod: os.Chmod,
		})
		assertListenerError(t, err, ListenerErrorCodeCleanupFailed)
		if replacement == nil {
			t.Fatal("replacement hook did not record replacement directory")
		}
		current := mustLstat(t, directory)
		if !os.SameFile(replacement, current) {
			t.Fatal("replacement directory identity changed during rollback")
		}
		contents, readErr := os.ReadFile(markerPath)
		if readErr != nil || string(contents) != "replacement" {
			t.Fatalf("replacement marker = %q, %v, want retained", contents, readErr)
		}
	})
}

func TestUnixListenerAcceptRequestTransfersValidConnectionToCaller(t *testing.T) {
	listener, path := newTestUnixListener(t)
	result := acceptRequestAsync(listener, context.Background(), uint32(os.Getuid()))
	client := dialTestUnix(t, path)
	writeTestFrame(t, client, probeCapabilitiesFrame())

	accepted := awaitAcceptResult(t, result)
	if accepted.err != nil {
		t.Fatalf("AcceptRequest() error = %v", accepted.err)
	}
	if accepted.connection == nil {
		t.Fatal("AcceptRequest() connection = nil, want caller-owned connection")
	}
	t.Cleanup(func() { _ = accepted.connection.Close() })
	if accepted.request == nil || accepted.request.Operation() != OperationProbeCapabilities {
		t.Fatalf("request = %T/%v, want ProbeCapabilities", accepted.request, accepted.request)
	}

	const marker = byte(0x5a)
	if _, err := client.Write([]byte{marker}); err != nil {
		t.Fatalf("write ownership marker: %v", err)
	}
	if err := accepted.connection.SetReadDeadline(time.Now().Add(listenerTestTimeout)); err != nil {
		t.Fatalf("set accepted connection deadline: %v", err)
	}
	var got [1]byte
	if _, err := io.ReadFull(accepted.connection, got[:]); err != nil {
		t.Fatalf("read ownership marker: %v", err)
	}
	if got[0] != marker {
		t.Fatalf("ownership marker = %#x, want %#x", got[0], marker)
	}
}

func TestUnixListenerCanceledAcceptCanAcceptAgain(t *testing.T) {
	listener, path := newTestUnixListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	observed := make(chan struct{})
	trackedCtx := &observedDoneContext{Context: ctx, observed: observed}
	result := acceptRequestAsync(listener, trackedCtx, uint32(os.Getuid()))
	select {
	case <-observed:
	case <-time.After(listenerTestTimeout):
		t.Fatal("AcceptRequest() did not arm context cancellation")
	}
	cancel()

	canceled := awaitAcceptResult(t, result)
	if canceled.connection != nil || canceled.request != nil {
		t.Fatalf("canceled AcceptRequest() = (%v, %T), want nil, nil", canceled.connection, canceled.request)
	}
	assertListenerError(t, canceled.err, ListenerErrorCodeContextCanceled)
	if !errors.Is(canceled.err, context.Canceled) {
		t.Fatalf("canceled AcceptRequest() error = %v, want errors.Is(context.Canceled)", canceled.err)
	}

	next := acceptRequestAsync(listener, context.Background(), uint32(os.Getuid()))
	client := dialTestUnix(t, path)
	writeTestFrame(t, client, probeCapabilitiesFrame())
	accepted := awaitAcceptResult(t, next)
	if accepted.err != nil {
		t.Fatalf("AcceptRequest() after cancellation error = %v", accepted.err)
	}
	if accepted.connection == nil || accepted.request == nil {
		t.Fatalf("AcceptRequest() after cancellation = (%v, %T), want connection and request", accepted.connection, accepted.request)
	}
	_ = accepted.connection.Close()
}

func TestUnixListenerDeadlineExceededAcceptCanAcceptAgain(t *testing.T) {
	listener, path := newTestUnixListener(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	observed := make(chan struct{})
	trackedCtx := &observedDoneContext{Context: ctx, observed: observed}
	result := acceptRequestAsync(listener, trackedCtx, uint32(os.Getuid()))
	select {
	case <-observed:
	case <-time.After(listenerTestTimeout):
		t.Fatal("AcceptRequest() did not arm context deadline")
	}

	expired := awaitAcceptResult(t, result)
	if expired.connection != nil || expired.request != nil {
		t.Fatalf("deadline AcceptRequest() = (%v, %T), want nil, nil", expired.connection, expired.request)
	}
	assertListenerError(t, expired.err, ListenerErrorCodeContextDeadlineExceeded)
	if !errors.Is(expired.err, context.DeadlineExceeded) {
		t.Fatalf("deadline AcceptRequest() error = %v, want errors.Is(context.DeadlineExceeded)", expired.err)
	}

	next := acceptRequestAsync(listener, context.Background(), uint32(os.Getuid()))
	client := dialTestUnix(t, path)
	writeTestFrame(t, client, probeCapabilitiesFrame())
	accepted := awaitAcceptResult(t, next)
	if accepted.err != nil {
		t.Fatalf("AcceptRequest() after deadline error = %v", accepted.err)
	}
	if accepted.connection == nil || accepted.request == nil {
		t.Fatalf("AcceptRequest() after deadline = (%v, %T), want connection and request", accepted.connection, accepted.request)
	}
	_ = accepted.connection.Close()
}

func TestUnixListenerContextTerminationClosesAcceptedPartialFrame(t *testing.T) {
	tests := []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc)
		terminate  func(context.CancelFunc)
		wantCode   ListenerErrorCode
		wantCause  error
	}{
		{
			name: "canceled",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			terminate: func(cancel context.CancelFunc) { cancel() },
			wantCode:  ListenerErrorCodeContextCanceled,
			wantCause: context.Canceled,
		},
		{
			name: "deadline exceeded",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 250*time.Millisecond)
			},
			terminate: func(context.CancelFunc) {},
			wantCode:  ListenerErrorCodeContextDeadlineExceeded,
			wantCause: context.DeadlineExceeded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, path := newTestUnixListener(t)
			ctx, cancel := test.newContext()
			defer cancel()
			phases := make(chan struct{}, 4)
			trackedCtx := &observedArmContext{Context: ctx, phases: phases}
			result := acceptRequestAsync(listener, trackedCtx, uint32(os.Getuid()))
			awaitContextArm(t, phases, "accept")

			client := dialTestUnix(t, path)
			writeTestFrame(t, client, testFrameHeader(32))
			awaitContextArm(t, phases, "decode")
			test.terminate(cancel)

			terminated := awaitAcceptResult(t, result)
			if terminated.connection != nil || terminated.request != nil {
				t.Fatalf("terminated AcceptRequest() = (%v, %T), want nil, nil", terminated.connection, terminated.request)
			}
			assertListenerError(t, terminated.err, test.wantCode)
			if !errors.Is(terminated.err, test.wantCause) {
				t.Fatalf("terminated AcceptRequest() error = %v, want errors.Is(%v)", terminated.err, test.wantCause)
			}
			assertPeerClosed(t, client)
		})
	}
}

func TestUnixListenerRejectsPeerAndInvalidFrameWithoutReturningConnection(t *testing.T) {
	tests := []struct {
		name        string
		expectedUID uint32
		contents    []byte
		writeFrame  bool
		assertError func(*testing.T, error)
	}{
		{
			name:        "UID mismatch",
			expectedUID: otherUID(uint32(os.Getuid())),
			assertError: func(t *testing.T, err error) {
				var peerError *PeerError
				if !errors.As(err, &peerError) || peerError.Code() != PeerErrorCodeUIDMismatch {
					t.Fatalf("error = %T/%v, want UID mismatch PeerError", err, err)
				}
			},
		},
		{
			name:        "invalid frame",
			expectedUID: uint32(os.Getuid()),
			contents:    testFrame([]byte("{")),
			writeFrame:  true,
			assertError: func(t *testing.T, err error) {
				var validationError *ValidationError
				if !errors.As(err, &validationError) || validationError.Code() != ErrorCodeInvalidJSON {
					t.Fatalf("error = %T/%v, want invalid JSON ValidationError", err, err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, path := newTestUnixListener(t)
			result := acceptRequestAsync(listener, context.Background(), test.expectedUID)
			client := dialTestUnix(t, path)
			if test.writeFrame {
				writeTestFrame(t, client, test.contents)
			}

			accepted := awaitAcceptResult(t, result)
			if accepted.connection != nil || accepted.request != nil {
				t.Fatalf("rejected AcceptRequest() = (%v, %T), want nil, nil", accepted.connection, accepted.request)
			}
			test.assertError(t, accepted.err)
			assertPeerClosed(t, client)
		})
	}
}

func TestListenUnixAtRejectsActiveSocketWithoutTakingItOver(t *testing.T) {
	parent, path := preparedSocketPath(t)
	original, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create active Unix socket: %v", err)
	}
	original.SetUnlinkOnClose(false)
	t.Cleanup(func() {
		_ = original.Close()
		_ = os.Remove(path)
	})
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatalf("chmod active Unix socket: %v", err)
	}
	assertUnixObject(t, parent, os.ModeDir, 0o750, uint32(os.Getuid()), uint32(os.Getgid()))
	before := mustLstat(t, path)

	listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
	if listener != nil {
		_ = listener.Close()
		t.Fatal("listenUnixAt() returned listener for active socket")
	}
	assertListenerError(t, err, ListenerErrorCodeSocketActive)
	after := mustLstat(t, path)
	if !os.SameFile(before, after) {
		t.Fatal("active socket was replaced")
	}

	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("original listener unusable after takeover rejection: %v", err)
	}
	_ = client.Close()
}

func TestListenUnixAtConcurrentReentryKeepsWinnerUsable(t *testing.T) {
	_, path := preparedSocketPath(t)
	start := make(chan struct{})
	results := make(chan struct {
		listener *UnixListener
		err      error
	}, 2)
	for range 2 {
		go func() {
			<-start
			listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
			results <- struct {
				listener *UnixListener
				err      error
			}{listener: listener, err: err}
		}()
	}
	close(start)

	var winner *UnixListener
	losers := 0
	for range 2 {
		select {
		case result := <-results:
			if result.err == nil {
				if winner != nil || result.listener == nil {
					t.Fatalf("concurrent listen successes include invalid/duplicate winner: %#v", result.listener)
				}
				winner = result.listener
				continue
			}
			if result.listener != nil {
				_ = result.listener.Close()
				t.Fatalf("failed concurrent listen returned listener: %v", result.err)
			}
			assertListenerError(t, result.err, ListenerErrorCodeSocketActive)
			losers++
		case <-time.After(listenerTestTimeout):
			t.Fatal("concurrent listenUnixAt() did not complete")
		}
	}
	if winner == nil || losers != 1 {
		if winner != nil {
			_ = winner.Close()
		}
		t.Fatalf("concurrent listen result = winner %v, losers %d; want one each", winner != nil, losers)
	}

	requestResult := acceptRequestAsync(winner, context.Background(), uint32(os.Getuid()))
	client := dialTestUnix(t, path)
	writeTestFrame(t, client, probeCapabilitiesFrame())
	accepted := awaitAcceptResult(t, requestResult)
	if accepted.err != nil || accepted.connection == nil || accepted.request == nil {
		_ = winner.Close()
		t.Fatalf("winning listener unusable: connection=%v request=%T error=%v", accepted.connection, accepted.request, accepted.err)
	}
	_ = accepted.connection.Close()
	if err := winner.Close(); err != nil {
		t.Fatalf("winning listener Close() error = %v", err)
	}

	recreated, err := listenUnixAt(path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatalf("listenUnixAt() after winner Close() error = %v", err)
	}
	if err := recreated.Close(); err != nil {
		t.Fatalf("recreated listener Close() error = %v", err)
	}
}

func TestListenUnixAtReplacesValidStaleSocket(t *testing.T) {
	_, path := preparedSocketPath(t)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale Unix socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o660); err != nil {
		_ = stale.Close()
		t.Fatalf("chmod stale Unix socket: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale Unix socket: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatalf("listenUnixAt() stale replacement error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client := dialTestUnix(t, path)
	_ = client.Close()
}

func TestListenUnixAtRejectsUnsafeObjectsWithoutChangingThem(t *testing.T) {
	t.Run("ordinary socket-path file", func(t *testing.T) {
		_, path := preparedSocketPath(t)
		const contents = "do-not-replace"
		if err := os.WriteFile(path, []byte(contents), 0o660); err != nil {
			t.Fatalf("write ordinary file: %v", err)
		}
		before := mustLstat(t, path)
		listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
		if listener != nil {
			_ = listener.Close()
			t.Fatal("listenUnixAt() returned listener for ordinary file")
		}
		assertListenerError(t, err, ListenerErrorCodeSocketInvalid)
		after := mustLstat(t, path)
		if !os.SameFile(before, after) {
			t.Fatal("ordinary file was replaced")
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != contents {
			t.Fatalf("ordinary file = %q, %v, want unchanged", got, readErr)
		}
	})

	t.Run("symlink parent", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o750); err != nil {
			t.Fatalf("mkdir target parent: %v", err)
		}
		link := filepath.Join(root, "guard")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink parent: %v", err)
		}
		path := filepath.Join(link, "enforcer.sock")
		listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
		if listener != nil {
			_ = listener.Close()
			t.Fatal("listenUnixAt() followed symlink parent")
		}
		assertListenerError(t, err, ListenerErrorCodeDirectoryInvalid)
		if _, statErr := os.Lstat(filepath.Join(target, "enforcer.sock")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("symlink target modified: %v", statErr)
		}
	})

	t.Run("parent permission drift", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "guard")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatalf("mkdir drifted parent: %v", err)
		}
		path := filepath.Join(parent, "enforcer.sock")
		listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
		if listener != nil {
			_ = listener.Close()
			t.Fatal("listenUnixAt() accepted drifted parent")
		}
		assertListenerError(t, err, ListenerErrorCodeDirectoryInvalid)
		if got := mustLstat(t, parent).Mode().Perm(); got != 0o700 {
			t.Fatalf("parent mode = %#o, want retained 0700", got)
		}
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("socket created under drifted parent: %v", statErr)
		}
	})

	t.Run("stale socket permission drift", func(t *testing.T) {
		_, path := preparedSocketPath(t)
		stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatalf("create stale Unix socket: %v", err)
		}
		stale.SetUnlinkOnClose(false)
		if err := os.Chmod(path, 0o600); err != nil {
			_ = stale.Close()
			t.Fatalf("chmod stale Unix socket: %v", err)
		}
		if err := stale.Close(); err != nil {
			t.Fatalf("close stale Unix socket: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(path) })
		before := mustLstat(t, path)
		listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
		if listener != nil {
			_ = listener.Close()
			t.Fatal("listenUnixAt() accepted drifted stale socket")
		}
		assertListenerError(t, err, ListenerErrorCodeSocketInvalid)
		after := mustLstat(t, path)
		if !os.SameFile(before, after) || after.Mode().Perm() != 0o600 {
			t.Fatalf("drifted stale socket changed: same=%v mode=%#o", os.SameFile(before, after), after.Mode().Perm())
		}
	})
}

func TestUnixListenerCloseRemovesOnlyOwnedSocket(t *testing.T) {
	t.Run("owned socket", func(t *testing.T) {
		listener, path := newTestUnixListener(t)
		parent := filepath.Dir(path)
		if err := listener.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("socket still exists after Close(): %v", err)
		}
		info := mustLstat(t, parent)
		if !info.IsDir() {
			t.Fatalf("parent mode = %v, want directory retained", info.Mode())
		}
	})

	t.Run("replacement", func(t *testing.T) {
		listener, path := newTestUnixListener(t)
		if err := os.Remove(path); err != nil {
			t.Fatalf("unlink owned socket: %v", err)
		}
		const contents = "replacement-must-survive"
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("create replacement: %v", err)
		}
		err := listener.Close()
		assertListenerError(t, err, ListenerErrorCodeSocketReplaced)
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != contents {
			t.Fatalf("replacement = %q, %v, want unchanged", got, readErr)
		}
	})
}

func TestListenerErrorsAreStableAndSanitized(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "guard-sensitive-marker-7219")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("mkdir drifted parent: %v", err)
	}
	path := filepath.Join(parent, "enforcer.sock")
	listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
	if listener != nil {
		_ = listener.Close()
		t.Fatal("listenUnixAt() accepted drifted parent")
	}
	assertListenerError(t, err, ListenerErrorCodeDirectoryInvalid)

	want := "ipc listener failed: directory_invalid"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err, want)
	}
	for _, secret := range []string{
		path,
		"guard-sensitive-marker-7219",
		strconv.Itoa(os.Getuid()),
		strconv.Itoa(os.Getgid()),
		"permission denied",
	} {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("listener error disclosed %q: %q", secret, err)
		}
	}
}

type acceptResult struct {
	connection *net.UnixConn
	request    Request
	err        error
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

type observedArmContext struct {
	context.Context
	phases chan<- struct{}
}

func (c *observedArmContext) Done() <-chan struct{} {
	c.phases <- struct{}{}
	return c.Context.Done()
}

func awaitContextArm(t *testing.T, phases <-chan struct{}, name string) {
	t.Helper()
	for range 2 {
		select {
		case <-phases:
		case <-time.After(listenerTestTimeout):
			t.Fatalf("AcceptRequest() did not arm %s phase", name)
		}
	}
}

func acceptRequestAsync(listener *UnixListener, ctx context.Context, expectedUID uint32) <-chan acceptResult {
	result := make(chan acceptResult, 1)
	go func() {
		connection, request, err := listener.AcceptRequest(ctx, expectedUID)
		result <- acceptResult{connection: connection, request: request, err: err}
	}()
	return result
}

func awaitAcceptResult(t *testing.T, result <-chan acceptResult) acceptResult {
	t.Helper()
	select {
	case accepted := <-result:
		return accepted
	case <-time.After(listenerTestTimeout):
		t.Fatal("AcceptRequest() did not complete")
		return acceptResult{}
	}
}

func newTestUnixListener(t *testing.T) (*UnixListener, string) {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "guard")
	path := filepath.Join(parent, "enforcer.sock")
	listener, err := listenUnixAt(path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatalf("listenUnixAt() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener, path
}

func preparedSocketPath(t *testing.T) (string, string) {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "guard")
	if err := os.Mkdir(parent, 0o750); err != nil {
		t.Fatalf("mkdir socket parent: %v", err)
	}
	if err := os.Chmod(parent, 0o750); err != nil {
		t.Fatalf("chmod socket parent: %v", err)
	}
	return parent, filepath.Join(parent, "enforcer.sock")
}

func dialTestUnix(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("dial Unix socket: %v", err)
	}
	if err := connection.SetDeadline(time.Now().Add(listenerTestTimeout)); err != nil {
		_ = connection.Close()
		t.Fatalf("set client deadline: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func writeTestFrame(t *testing.T, connection *net.UnixConn, contents []byte) {
	t.Helper()
	written, err := connection.Write(contents)
	if err != nil {
		t.Fatalf("write Unix frame: %v", err)
	}
	if written != len(contents) {
		t.Fatalf("wrote %d bytes, want %d", written, len(contents))
	}
}

func assertPeerClosed(t *testing.T, connection *net.UnixConn) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(listenerTestTimeout)); err != nil {
		t.Fatalf("set peer-close deadline: %v", err)
	}
	var value [1]byte
	if _, err := connection.Read(value[:]); err == nil {
		t.Fatal("rejected peer remained open")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatalf("rejected peer was not closed before deadline: %v", err)
	}
}

func assertListenerError(t *testing.T, err error, want ListenerErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want ListenerError code %q", want)
	}
	var listenerError *ListenerError
	if !errors.As(err, &listenerError) {
		t.Fatalf("error type = %T, want *ListenerError", err)
	}
	if got := listenerError.Code(); got != want {
		t.Fatalf("listener error code = %q, want %q", got, want)
	}
	wantText := "ipc listener failed: " + string(want)
	if err.Error() != wantText {
		t.Fatalf("listener error text = %q, want %q", err, wantText)
	}
}

func assertUnixObject(t *testing.T, path string, wantType os.FileMode, wantPerm os.FileMode, wantUID, wantGID uint32) {
	t.Helper()
	info := mustLstat(t, path)
	if got := info.Mode() & os.ModeType; got != wantType {
		t.Fatalf("%s type = %v, want %v", filepath.Base(path), got, wantType)
	}
	if got := info.Mode().Perm(); got != wantPerm {
		t.Fatalf("%s mode = %#o, want %#o", filepath.Base(path), got, wantPerm)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s stat type = %T, want *syscall.Stat_t", filepath.Base(path), info.Sys())
	}
	if stat.Uid != wantUID || stat.Gid != wantGID {
		t.Fatalf("%s owner = %d:%d, want %d:%d", filepath.Base(path), stat.Uid, stat.Gid, wantUID, wantGID)
	}
}

func mustLstat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", filepath.Base(path), err)
	}
	return info
}

func probeCapabilitiesFrame() []byte {
	return testFrame([]byte(`{"version":1,"operation":"ProbeCapabilities","payload":{}}`))
}

func testFrame(payload []byte) []byte {
	result := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(result[:4], uint32(len(payload)))
	copy(result[4:], payload)
	return result
}

func testFrameHeader(length uint32) []byte {
	result := make([]byte, 4)
	binary.BigEndian.PutUint32(result, length)
	return result
}

func otherUID(uid uint32) uint32 {
	if uid == ^uint32(0) {
		return uid - 1
	}
	return uid + 1
}
