//go:build linux

package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// EnforcerSocketPath is the production Agent-to-Enforcer Unix socket path.
const EnforcerSocketPath = "/run/guard/enforcer.sock"

const staleSocketProbeTimeout = 250 * time.Millisecond

// ListenerErrorCode classifies a Unix listener lifecycle failure.
type ListenerErrorCode string

const (
	ListenerErrorCodeDirectoryInvalid        ListenerErrorCode = "directory_invalid"
	ListenerErrorCodeSocketInvalid           ListenerErrorCode = "socket_invalid"
	ListenerErrorCodeSocketActive            ListenerErrorCode = "socket_active"
	ListenerErrorCodeSocketProbeFailed       ListenerErrorCode = "socket_probe_failed"
	ListenerErrorCodeListenFailed            ListenerErrorCode = "listen_failed"
	ListenerErrorCodeSocketConfigureFailed   ListenerErrorCode = "socket_configure_failed"
	ListenerErrorCodeAcceptFailed            ListenerErrorCode = "accept_failed"
	ListenerErrorCodeAlreadyServing          ListenerErrorCode = "already_serving"
	ListenerErrorCodeContextCanceled         ListenerErrorCode = "context_canceled"
	ListenerErrorCodeContextDeadlineExceeded ListenerErrorCode = "context_deadline_exceeded"
	ListenerErrorCodeCloseFailed             ListenerErrorCode = "close_failed"
	ListenerErrorCodeCleanupFailed           ListenerErrorCode = "cleanup_failed"
	ListenerErrorCodeSocketReplaced          ListenerErrorCode = "socket_replaced"
)

// ListenerError reports only a stable listener failure classification. It
// never includes filesystem paths, credentials, or operating-system errors.
type ListenerError struct {
	code  ListenerErrorCode
	cause error
}

func (e *ListenerError) Error() string {
	return "ipc listener failed: " + string(e.code)
}

// Code returns the listener failure classification.
func (e *ListenerError) Code() ListenerErrorCode {
	return e.code
}

// Unwrap preserves only context cancellation identity. Operating-system
// errors remain intentionally hidden.
func (e *ListenerError) Unwrap() error {
	return e.cause
}

type socketIdentity struct {
	device uint64
	inode  uint64
}

// UnixListener owns the production Unix listener and the socket filesystem
// object it created. A successful AcceptRequest transfers connection ownership
// to the caller.
type UnixListener struct {
	listener       *net.UnixListener
	socketPath     string
	socketIdentity socketIdentity
	directoryFD    int
	acceptMu       sync.Mutex
	closeOnce      sync.Once
	closeErr       error
	serveOwned     atomic.Bool
}

// ListenUnix creates the production Enforcer socket as root:guard. The guard
// group ID must be resolved once at Enforcer startup and injected here.
func ListenUnix(expectedGuardGID uint32) (*UnixListener, error) {
	return listenUnixAt(EnforcerSocketPath, 0, int(expectedGuardGID))
}

func listenUnixAt(socketPath string, ownerUID, ownerGID int) (*UnixListener, error) {
	directory := filepath.Dir(socketPath)
	if err := ensureSocketDirectory(directory, ownerUID, ownerGID); err != nil {
		return nil, err
	}
	directoryFD, err := lockSocketDirectory(directory, ownerUID, ownerGID)
	if err != nil {
		return nil, err
	}
	releaseDirectoryFD := true
	defer func() {
		if releaseDirectoryFD {
			_ = releaseDirectoryLock(directoryFD)
		}
	}()

	if err := removeStaleSocket(socketPath, ownerUID, ownerGID); err != nil {
		return nil, err
	}

	address := &net.UnixAddr{Name: socketPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return nil, listenerError(ListenerErrorCodeListenFailed)
	}
	listener.SetUnlinkOnClose(false)

	identity, err := socketNodeIdentity(socketPath)
	if err != nil {
		return nil, failListenerSetup(listener, socketPath, identity, ListenerErrorCodeSocketConfigureFailed)
	}
	if err := os.Chown(socketPath, ownerUID, ownerGID); err != nil {
		return nil, failListenerSetup(listener, socketPath, identity, ListenerErrorCodeSocketConfigureFailed)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return nil, failListenerSetup(listener, socketPath, identity, ListenerErrorCodeSocketConfigureFailed)
	}
	verifiedIdentity, err := validateSocketNode(socketPath, ownerUID, ownerGID)
	if err != nil || verifiedIdentity != identity {
		return nil, failListenerSetup(listener, socketPath, identity, ListenerErrorCodeSocketConfigureFailed)
	}

	releaseDirectoryFD = false
	return &UnixListener{
		listener:       listener,
		socketPath:     socketPath,
		socketIdentity: identity,
		directoryFD:    directoryFD,
	}, nil
}

func ensureSocketDirectory(directory string, ownerUID, ownerGID int) error {
	return ensureSocketDirectoryWithOps(directory, ownerUID, ownerGID, directorySetupOps{
		chown: os.Chown,
		chmod: os.Chmod,
	})
}

type directorySetupOps struct {
	chown func(string, int, int) error
	chmod func(string, os.FileMode) error
}

func ensureSocketDirectoryWithOps(directory string, ownerUID, ownerGID int, ops directorySetupOps) error {
	var createdIdentity socketIdentity
	created := false
	_, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		mkdirErr := os.Mkdir(directory, 0o750)
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return listenerError(ListenerErrorCodeDirectoryInvalid)
		}
		if mkdirErr == nil {
			identity, identityErr := directoryNodeIdentity(directory)
			if identityErr != nil {
				return listenerError(ListenerErrorCodeDirectoryInvalid)
			}
			createdIdentity = identity
			created = true
			if err := ops.chown(directory, ownerUID, ownerGID); err != nil {
				return failDirectorySetup(directory, identity)
			}
			if err := ops.chmod(directory, 0o750); err != nil {
				return failDirectorySetup(directory, identity)
			}
		}
	} else if err != nil {
		return listenerError(ListenerErrorCodeDirectoryInvalid)
	}

	if _, err := validateDirectoryNode(directory, ownerUID, ownerGID); err != nil {
		if created {
			return failDirectorySetup(directory, createdIdentity)
		}
		return listenerError(ListenerErrorCodeDirectoryInvalid)
	}
	return nil
}

func lockSocketDirectory(directory string, ownerUID, ownerGID int) (int, error) {
	directoryFD, err := syscall.Open(
		directory,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC,
		0,
	)
	if err != nil {
		return -1, listenerError(ListenerErrorCodeDirectoryInvalid)
	}
	releaseDirectoryFD := true
	defer func() {
		if releaseDirectoryFD {
			_ = syscall.Close(directoryFD)
		}
	}()

	if err := syscall.Flock(directoryFD, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return -1, listenerError(ListenerErrorCodeSocketActive)
		}
		return -1, listenerError(ListenerErrorCodeListenFailed)
	}

	var fdStat syscall.Stat_t
	if err := syscall.Fstat(directoryFD, &fdStat); err != nil ||
		fdStat.Mode&syscall.S_IFMT != syscall.S_IFDIR ||
		fdStat.Mode&0o7777 != 0o750 ||
		fdStat.Uid != uint32(ownerUID) ||
		fdStat.Gid != uint32(ownerGID) {
		_ = syscall.Flock(directoryFD, syscall.LOCK_UN)
		return -1, listenerError(ListenerErrorCodeDirectoryInvalid)
	}

	pathIdentity, err := validateDirectoryNode(directory, ownerUID, ownerGID)
	fdIdentity := socketIdentity{device: uint64(fdStat.Dev), inode: fdStat.Ino}
	if err != nil || pathIdentity != fdIdentity {
		_ = syscall.Flock(directoryFD, syscall.LOCK_UN)
		return -1, listenerError(ListenerErrorCodeDirectoryInvalid)
	}

	releaseDirectoryFD = false
	return directoryFD, nil
}

func releaseDirectoryLock(directoryFD int) error {
	unlockErr := syscall.Flock(directoryFD, syscall.LOCK_UN)
	closeErr := syscall.Close(directoryFD)
	if unlockErr != nil || closeErr != nil {
		return listenerError(ListenerErrorCodeCloseFailed)
	}
	return nil
}

func failDirectorySetup(directory string, identity socketIdentity) error {
	if err := removeMatchingDirectory(directory, identity); err != nil {
		return err
	}
	return listenerError(ListenerErrorCodeDirectoryInvalid)
}

func removeMatchingDirectory(directory string, expected socketIdentity) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return listenerError(ListenerErrorCodeCleanupFailed)
	}
	identity, ok := identityFromInfo(info)
	if !ok || identity != expected {
		return listenerError(ListenerErrorCodeCleanupFailed)
	}
	if err := os.Remove(directory); err != nil {
		return listenerError(ListenerErrorCodeCleanupFailed)
	}
	return nil
}

func validateDirectoryNode(directory string, ownerUID, ownerGID int) (socketIdentity, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || exactPermissions(info.Mode()) != 0o750 || !hasOwner(info, ownerUID, ownerGID) {
		return socketIdentity{}, listenerError(ListenerErrorCodeDirectoryInvalid)
	}
	identity, ok := identityFromInfo(info)
	if !ok {
		return socketIdentity{}, listenerError(ListenerErrorCodeDirectoryInvalid)
	}
	return identity, nil
}

func directoryNodeIdentity(directory string) (socketIdentity, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return socketIdentity{}, listenerError(ListenerErrorCodeDirectoryInvalid)
	}
	identity, ok := identityFromInfo(info)
	if !ok {
		return socketIdentity{}, listenerError(ListenerErrorCodeDirectoryInvalid)
	}
	return identity, nil
}

func removeStaleSocket(socketPath string, ownerUID, ownerGID int) error {
	identity, err := validateSocketNode(socketPath, ownerUID, ownerGID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return listenerError(ListenerErrorCodeSocketInvalid)
	}

	connection, dialErr := net.DialTimeout("unix", socketPath, staleSocketProbeTimeout)
	if dialErr == nil {
		_ = connection.Close()
		return listenerError(ListenerErrorCodeSocketActive)
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return listenerError(ListenerErrorCodeSocketProbeFailed)
	}

	currentIdentity, err := validateSocketNode(socketPath, ownerUID, ownerGID)
	if err != nil || currentIdentity != identity {
		return listenerError(ListenerErrorCodeSocketReplaced)
	}
	if err := os.Remove(socketPath); err != nil {
		return listenerError(ListenerErrorCodeCleanupFailed)
	}
	return nil
}

// AcceptRequest accepts, authenticates, and decodes one request. On success
// the caller owns the returned connection. Every failure closes the accepted
// connection before returning. It fails while a high-level serve method owns
// the listener; callers must not overlap this low-level ownership transfer with
// another serve operation on the same listener.
func (l *UnixListener) AcceptRequest(ctx context.Context, expectedGuardUID uint32) (*net.UnixConn, Request, error) {
	releaseServeOwner, err := l.acquireServeOwner()
	if err != nil {
		return nil, nil, err
	}
	defer releaseServeOwner()

	return l.acceptRequest(ctx, expectedGuardUID)
}

func (l *UnixListener) acceptRequest(ctx context.Context, expectedGuardUID uint32) (*net.UnixConn, Request, error) {
	connection, err := l.acceptUnix(ctx)
	if err != nil {
		return nil, nil, err
	}

	stopWatch, err := armContextDeadline(ctx, connection.SetDeadline)
	if err != nil {
		_ = connection.Close()
		return nil, nil, listenerError(ListenerErrorCodeAcceptFailed)
	}
	request, decodeErr := DecodeUnixFrame(connection, expectedGuardUID)
	stopWatch()
	resetErr := connection.SetDeadline(time.Time{})

	if contextErr := contextTerminationError(ctx); contextErr != nil {
		_ = connection.Close()
		return nil, nil, contextErr
	}
	if decodeErr != nil {
		_ = connection.Close()
		return nil, nil, decodeErr
	}
	if resetErr != nil {
		_ = connection.Close()
		return nil, nil, listenerError(ListenerErrorCodeAcceptFailed)
	}
	return connection, request, nil
}

func (l *UnixListener) acquireServeOwner() (func(), error) {
	if !l.serveOwned.CompareAndSwap(false, true) {
		return nil, listenerError(ListenerErrorCodeAlreadyServing)
	}
	return func() { l.serveOwned.Store(false) }, nil
}

func (l *UnixListener) acceptUnix(ctx context.Context) (*net.UnixConn, error) {
	l.acceptMu.Lock()
	defer l.acceptMu.Unlock()

	if err := contextTerminationError(ctx); err != nil {
		return nil, err
	}
	stopWatch, err := armContextDeadline(ctx, l.listener.SetDeadline)
	if err != nil {
		return nil, listenerError(ListenerErrorCodeAcceptFailed)
	}
	connection, acceptErr := l.listener.AcceptUnix()
	stopWatch()
	resetErr := l.listener.SetDeadline(time.Time{})

	if contextErr := contextTerminationError(ctx); contextErr != nil {
		if connection != nil {
			_ = connection.Close()
		}
		return nil, contextErr
	}
	if acceptErr != nil || resetErr != nil {
		if connection != nil {
			_ = connection.Close()
		}
		return nil, listenerError(ListenerErrorCodeAcceptFailed)
	}
	return connection, nil
}

func armContextDeadline(ctx context.Context, setDeadline func(time.Time) error) (func(), error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Time{}
	}
	if err := setDeadline(deadline); err != nil {
		return nil, err
	}
	if ctx.Done() == nil {
		return func() {}, nil
	}

	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = setDeadline(time.Now())
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		<-stopped
	}, nil
}

// Close stops accepting connections and removes only the socket object created
// by this listener. A replacement path is preserved and reported.
func (l *UnixListener) Close() error {
	l.closeOnce.Do(func() {
		closeFailed := l.listener.Close() != nil
		cleanupErr := removeMatchingSocket(l.socketPath, l.socketIdentity)
		lockErr := releaseDirectoryLock(l.directoryFD)
		if cleanupErr != nil {
			l.closeErr = cleanupErr
		} else if closeFailed {
			l.closeErr = listenerError(ListenerErrorCodeCloseFailed)
		} else if lockErr != nil {
			l.closeErr = lockErr
		}
	})
	return l.closeErr
}

func failListenerSetup(listener *net.UnixListener, socketPath string, identity socketIdentity, code ListenerErrorCode) error {
	closeFailed := listener.Close() != nil
	cleanupErr := removeMatchingSocket(socketPath, identity)
	if cleanupErr != nil {
		return cleanupErr
	}
	if closeFailed {
		return listenerError(ListenerErrorCodeCloseFailed)
	}
	return listenerError(code)
}

func removeMatchingSocket(socketPath string, expected socketIdentity) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode().Type() != os.ModeSocket {
		return listenerError(ListenerErrorCodeSocketReplaced)
	}
	identity, ok := identityFromInfo(info)
	if !ok || identity != expected {
		return listenerError(ListenerErrorCodeSocketReplaced)
	}
	if err := os.Remove(socketPath); err != nil {
		return listenerError(ListenerErrorCodeCleanupFailed)
	}
	return nil
}

func validateSocketNode(socketPath string, ownerUID, ownerGID int) (socketIdentity, error) {
	info, err := os.Lstat(socketPath)
	if err != nil {
		return socketIdentity{}, err
	}
	if info.Mode().Type() != os.ModeSocket || exactPermissions(info.Mode()) != 0o660 || !hasOwner(info, ownerUID, ownerGID) {
		return socketIdentity{}, listenerError(ListenerErrorCodeSocketInvalid)
	}
	identity, ok := identityFromInfo(info)
	if !ok {
		return socketIdentity{}, listenerError(ListenerErrorCodeSocketInvalid)
	}
	return identity, nil
}

func socketNodeIdentity(socketPath string) (socketIdentity, error) {
	info, err := os.Lstat(socketPath)
	if err != nil || info.Mode().Type() != os.ModeSocket {
		return socketIdentity{}, listenerError(ListenerErrorCodeSocketInvalid)
	}
	identity, ok := identityFromInfo(info)
	if !ok {
		return socketIdentity{}, listenerError(ListenerErrorCodeSocketInvalid)
	}
	return identity, nil
}

func hasOwner(info os.FileInfo, ownerUID, ownerGID int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(ownerUID) && stat.Gid == uint32(ownerGID)
}

func exactPermissions(mode os.FileMode) os.FileMode {
	return mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

func identityFromInfo(info os.FileInfo) (socketIdentity, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return socketIdentity{}, false
	}
	return socketIdentity{device: uint64(stat.Dev), inode: stat.Ino}, true
}

func listenerError(code ListenerErrorCode) error {
	return &ListenerError{code: code}
}

func contextListenerError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &ListenerError{code: ListenerErrorCodeContextDeadlineExceeded, cause: context.DeadlineExceeded}
	}
	return &ListenerError{code: ListenerErrorCodeContextCanceled, cause: context.Canceled}
}

func contextTerminationError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return contextListenerError(err)
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return contextListenerError(context.DeadlineExceeded)
	}
	return nil
}
