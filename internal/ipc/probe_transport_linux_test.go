//go:build linux

package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

const probeTransportTestTimeout = 3 * time.Second

var _ func(context.Context) (firewall.FirewallCapabilities, error) = RoundTripProbeCapabilities

func TestProbeCapabilitiesTransportContractConstants(t *testing.T) {
	clientCodes := map[ProbeClientErrorCode]bool{
		ProbeClientErrorCodeDialFailed:              true,
		ProbeClientErrorCodeDeadlineFailed:          true,
		ProbeClientErrorCodeContextCanceled:         true,
		ProbeClientErrorCodeContextDeadlineExceeded: true,
		ProbeClientErrorCodeResponseMismatch:        true,
	}
	if len(clientCodes) != 5 {
		t.Fatalf("unique client error codes = %d, want 5", len(clientCodes))
	}
	serverCodes := map[ProbeServerErrorCode]bool{
		ProbeServerErrorCodeUnavailable:         true,
		ProbeServerErrorCodeHandlerRequired:     true,
		ProbeServerErrorCodeUnexpectedOperation: true,
	}
	if len(serverCodes) != 3 {
		t.Fatalf("unique server error codes = %d, want 3", len(serverCodes))
	}
}

func TestProbeCapabilitiesRealUnixSocketEndToEnd(t *testing.T) {
	listener, socketPath := newProbeTransportListener(t)
	want := mustProbeTransportCapabilities(t)
	response := mustProbeTransportSuccess(t, want)
	var calls atomic.Int32
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeProbeCapabilitiesOnce(
			context.Background(),
			uint32(os.Getuid()),
			func(context.Context) ProbeCapabilitiesResponse {
				calls.Add(1)
				return response
			},
		)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), probeTransportTestTimeout)
	defer cancel()
	got, err := roundTripProbeCapabilitiesAt(ctx, socketPath, uint32(os.Getuid()))
	if err != nil {
		t.Fatalf("roundTripProbeCapabilitiesAt(): %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("returned capabilities invalid: %v", err)
	}
	assertProbeTransportCapabilities(t, got, want)
	if err := awaitProbeTransportServer(t, serverDone); err != nil {
		t.Fatalf("ServeProbeCapabilitiesOnce(): %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestProbeCapabilitiesClosedRemoteFailures(t *testing.T) {
	for _, code := range []ProbeCapabilitiesFailureCode{
		ProbeCapabilitiesFailureCodeUnsupported,
		ProbeCapabilitiesFailureCodeNotReady,
	} {
		t.Run(string(code), func(t *testing.T) {
			listener, socketPath := newProbeTransportListener(t)
			response := mustProbeTransportFailure(t, code)
			var calls atomic.Int32
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- listener.ServeProbeCapabilitiesOnce(
					context.Background(),
					uint32(os.Getuid()),
					func(context.Context) ProbeCapabilitiesResponse {
						calls.Add(1)
						return response
					},
				)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), probeTransportTestTimeout)
			defer cancel()
			capabilities, err := roundTripProbeCapabilitiesAt(ctx, socketPath, uint32(os.Getuid()))
			if capabilities != (firewall.FirewallCapabilities{}) {
				t.Fatalf("failure returned capabilities = %#v, want zero", capabilities)
			}
			var remote *ProbeCapabilitiesRemoteError
			if !errors.As(err, &remote) || remote.Code() != code {
				t.Fatalf("error = %T %v, want remote code %q", err, err, code)
			}
			if err := awaitProbeTransportServer(t, serverDone); err != nil {
				t.Fatalf("ServeProbeCapabilitiesOnce(): %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("handler calls = %d, want 1", calls.Load())
			}
		})
	}
}

func TestProbeCapabilitiesClientAuthenticatesServerBeforeWrite(t *testing.T) {
	rawListener, socketPath := newRawProbeTransportListener(t)
	serverDone := make(chan probeTransportReadResult, 1)
	go func() {
		connection, err := rawListener.AcceptUnix()
		if err != nil {
			serverDone <- probeTransportReadResult{err: err}
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(probeTransportTestTimeout))
		var one [1]byte
		n, readErr := connection.Read(one[:])
		serverDone <- probeTransportReadResult{bytes: n, err: readErr}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), probeTransportTestTimeout)
	defer cancel()
	_, err := roundTripProbeCapabilitiesAt(ctx, socketPath, differentUID(uint32(os.Getuid())))
	var peer *PeerError
	if !errors.As(err, &peer) || peer.Code() != PeerErrorCodeUIDMismatch {
		t.Fatalf("error = %T %v, want peer UID mismatch", err, err)
	}

	select {
	case result := <-serverDone:
		if result.bytes != 0 {
			t.Fatalf("server received %d request bytes before peer authentication", result.bytes)
		}
	case <-time.After(probeTransportTestTimeout):
		t.Fatal("server did not observe authenticated client close")
	}
}

func TestProbeCapabilitiesServerAuthenticatesClientBeforeHandler(t *testing.T) {
	listener, socketPath := newProbeTransportListener(t)
	response := mustProbeTransportSuccess(t, mustProbeTransportCapabilities(t))
	var calls atomic.Int32
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeProbeCapabilitiesOnce(
			context.Background(),
			differentUID(uint32(os.Getuid())),
			func(context.Context) ProbeCapabilitiesResponse {
				calls.Add(1)
				return response
			},
		)
	}()

	connection := dialRawProbeTransport(t, socketPath)
	// The authenticated server may close before the client's frame write
	// completes. Either outcome is valid; the invariant is that no request byte
	// is consumed and the handler is never called after UID rejection.
	_ = WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest())
	_ = connection.Close()

	err := awaitProbeTransportServer(t, serverDone)
	var peer *PeerError
	if !errors.As(err, &peer) || peer.Code() != PeerErrorCodeUIDMismatch {
		t.Fatalf("server error = %T %v, want peer UID mismatch", err, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("handler calls after peer rejection = %d, want 0", calls.Load())
	}
}

func TestProbeCapabilitiesServerRejectsOtherOperationsWithoutCallingHandler(t *testing.T) {
	listener, socketPath := newProbeTransportListener(t)
	response := mustProbeTransportSuccess(t, mustProbeTransportCapabilities(t))
	var calls atomic.Int32
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeProbeCapabilitiesOnce(
			context.Background(),
			uint32(os.Getuid()),
			func(context.Context) ProbeCapabilitiesResponse {
				calls.Add(1)
				return response
			},
		)
	}()

	connection := dialRawProbeTransport(t, socketPath)
	if err := writeFramePayload(connection, []byte(`{"version":1,"operation":"SnapshotManaged","payload":{}}`)); err != nil {
		t.Fatalf("write Snapshot frame: %v", err)
	}
	_ = connection.Close()

	err := awaitProbeTransportServer(t, serverDone)
	var server *ProbeServerError
	if !errors.As(err, &server) || server.Code() != ProbeServerErrorCodeUnexpectedOperation {
		t.Fatalf("server error = %T %v, want unexpected operation", err, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("handler calls for SnapshotManaged = %d, want 0", calls.Load())
	}
}

func TestProbeCapabilitiesServerCancellationAfterHandlerWritesNothing(t *testing.T) {
	listener, socketPath := newProbeTransportListener(t)
	serverCtx, cancelServer := context.WithCancel(context.Background())
	response := mustProbeTransportSuccess(t, mustProbeTransportCapabilities(t))
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeProbeCapabilitiesOnce(
			serverCtx,
			uint32(os.Getuid()),
			func(context.Context) ProbeCapabilitiesResponse {
				cancelServer()
				return response
			},
		)
	}()

	connection := dialRawProbeTransport(t, socketPath)
	if err := WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest()); err != nil {
		t.Fatalf("WriteProbeCapabilitiesRequestFrame(): %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(probeTransportTestTimeout))
	responseBytes, readErr := io.ReadAll(connection)
	_ = connection.Close()
	if len(responseBytes) != 0 {
		t.Fatalf("server wrote %d bytes after handler cancellation", len(responseBytes))
	}
	if readErr != nil {
		t.Fatalf("read canceled server connection: %v", readErr)
	}
	if err := awaitProbeTransportServer(t, serverDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("server error = %T %v, want context canceled", err, err)
	}
}

func TestProbeCapabilitiesServerWriteCancellationLinearization(t *testing.T) {
	t.Run("incomplete write preserves context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writer := &probeServerCancellationWriter{cancel: cancel, failAfterCancel: true}
		err := writeProbeCapabilitiesServerPayload(ctx, writer, []byte("probe-response"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write error = %T %v, want context canceled", err, err)
		}
	})

	t.Run("complete frame wins subsequent cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writer := &probeServerCancellationWriter{cancel: cancel}
		if err := writeProbeCapabilitiesServerPayload(ctx, writer, []byte("probe-response")); err != nil {
			t.Fatalf("complete write error = %v, want nil", err)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("context error = %v, want canceled proof", ctx.Err())
		}
		if writer.writes != 2 {
			t.Fatalf("write calls = %d, want header and payload", writer.writes)
		}
	})
}

func TestProbeCapabilitiesClientDeadlineAndNoRetry(t *testing.T) {
	rawListener, socketPath := newRawProbeTransportListener(t)
	serverDone := make(chan probeTransportAcceptResult, 1)
	go func() {
		result := probeTransportAcceptResult{}
		connection, err := rawListener.AcceptUnix()
		if err != nil {
			result.err = err
			serverDone <- result
			return
		}
		result.accepts++
		_, result.err = DecodeFrame(connection)
		if result.err == nil {
			var one [1]byte
			_, _ = connection.Read(one[:])
		}
		_ = connection.Close()
		_ = rawListener.SetDeadline(time.Now().Add(150 * time.Millisecond))
		second, secondErr := rawListener.AcceptUnix()
		if second != nil {
			result.accepts++
			_ = second.Close()
		}
		if secondErr == nil {
			result.err = errors.New("second accept unexpectedly succeeded")
		}
		serverDone <- result
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := roundTripProbeCapabilitiesAt(ctx, socketPath, uint32(os.Getuid()))
	var client *ProbeClientError
	if !errors.As(err, &client) || client.Code() != ProbeClientErrorCodeContextDeadlineExceeded ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("client error = %T %v, want context deadline", err, err)
	}

	select {
	case result := <-serverDone:
		if result.err != nil {
			t.Fatalf("server result: %v", result.err)
		}
		if result.accepts != 1 {
			t.Fatalf("accepted connections = %d, want exactly 1", result.accepts)
		}
	case <-time.After(probeTransportTestTimeout):
		t.Fatal("server did not finish no-retry observation")
	}
}

func TestProbeCapabilitiesPreCanceledClientDoesNotDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := roundTripProbeCapabilitiesAt(ctx, filepath.Join(t.TempDir(), "absent.sock"), uint32(os.Getuid()))
	var client *ProbeClientError
	if !errors.As(err, &client) || client.Code() != ProbeClientErrorCodeContextCanceled ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("client error = %T %v, want context canceled", err, err)
	}
}

func TestProbeCapabilitiesServerRejectsNilDependenciesBeforeAccept(t *testing.T) {
	var listener *UnixListener
	err := listener.ServeProbeCapabilitiesOnce(context.Background(), uint32(os.Getuid()), func(context.Context) ProbeCapabilitiesResponse {
		return nil
	})
	assertProbeServerCode(t, err, ProbeServerErrorCodeUnavailable)

	realListener, _ := newProbeTransportListener(t)
	err = realListener.ServeProbeCapabilitiesOnce(context.Background(), uint32(os.Getuid()), nil)
	assertProbeServerCode(t, err, ProbeServerErrorCodeHandlerRequired)
}

type probeTransportReadResult struct {
	bytes int
	err   error
}

type probeTransportAcceptResult struct {
	accepts int
	err     error
}

type probeServerCancellationWriter struct {
	cancel          context.CancelFunc
	failAfterCancel bool
	writes          int
}

func (w *probeServerCancellationWriter) Write(value []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		w.cancel()
		if w.failAfterCancel {
			return 0, io.ErrClosedPipe
		}
	}
	return len(value), nil
}

func newProbeTransportListener(t *testing.T) (*UnixListener, string) {
	t.Helper()
	raw, path := newRawProbeTransportListener(t)
	return &UnixListener{listener: raw, socketPath: path}, path
}

func newRawProbeTransportListener(t *testing.T) (*net.UnixListener, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.sock")
	raw, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix(%q): %v", path, err)
	}
	raw.SetUnlinkOnClose(true)
	t.Cleanup(func() {
		_ = raw.Close()
	})
	return raw, path
}

func dialRawProbeTransport(t *testing.T, socketPath string) *net.UnixConn {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix(%q): %v", socketPath, err)
	}
	return connection
}

func awaitProbeTransportServer(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(probeTransportTestTimeout):
		t.Fatal("ProbeCapabilities server did not finish")
		return nil
	}
}

func differentUID(uid uint32) uint32 {
	if uid == ^uint32(0) {
		return uid - 1
	}
	return uid + 1
}

func mustProbeTransportCapabilities(t *testing.T) firewall.FirewallCapabilities {
	t.Helper()
	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend:                 firewall.BackendKindNftablesNative,
		ToolVersion:             "nftables v1.0.9",
		IPv4:                    true,
		IPv6:                    true,
		CIDR:                    true,
		NativeSet:               true,
		NativeTimeout:           true,
		CrashSafeExpiry:         true,
		AtomicBatch:             true,
		HostInput:               true,
		Forward:                 true,
		UFWIntegrationProven:    false,
		DockerIntegrationProven: true,
		OwnershipProven:         true,
		MutationReady:           true,
	})
	if err != nil {
		t.Fatalf("NewFirewallCapabilities(): %v", err)
	}
	return capabilities
}

func mustProbeTransportSuccess(
	t *testing.T,
	capabilities firewall.FirewallCapabilities,
) ProbeCapabilitiesSuccessResponse {
	t.Helper()
	response, err := NewProbeCapabilitiesSuccessResponse(capabilities)
	if err != nil {
		t.Fatalf("NewProbeCapabilitiesSuccessResponse(): %v", err)
	}
	return response
}

func mustProbeTransportFailure(
	t *testing.T,
	code ProbeCapabilitiesFailureCode,
) ProbeCapabilitiesFailureResponse {
	t.Helper()
	response, err := NewProbeCapabilitiesFailureResponse(code)
	if err != nil {
		t.Fatalf("NewProbeCapabilitiesFailureResponse(%q): %v", code, err)
	}
	return response
}

func assertProbeTransportCapabilities(
	t *testing.T,
	got firewall.FirewallCapabilities,
	want firewall.FirewallCapabilities,
) {
	t.Helper()
	if got.Backend() != want.Backend() ||
		got.ToolVersion() != want.ToolVersion() ||
		got.SupportsIPv4() != want.SupportsIPv4() ||
		got.SupportsIPv6() != want.SupportsIPv6() ||
		got.SupportsCIDR() != want.SupportsCIDR() ||
		got.SupportsNativeSet() != want.SupportsNativeSet() ||
		got.SupportsNativeTimeout() != want.SupportsNativeTimeout() ||
		got.SupportsCrashSafeExpiry() != want.SupportsCrashSafeExpiry() ||
		got.SupportsAtomicBatch() != want.SupportsAtomicBatch() ||
		got.SupportsHostInput() != want.SupportsHostInput() ||
		got.SupportsForward() != want.SupportsForward() ||
		got.UFWIntegrationProven() != want.UFWIntegrationProven() ||
		got.DockerIntegrationProven() != want.DockerIntegrationProven() ||
		got.OwnershipProven() != want.OwnershipProven() ||
		got.MutationReady() != want.MutationReady() {
		t.Fatalf("capability round trip mismatch: got=%#v want=%#v", got, want)
	}
}

func assertProbeServerCode(t *testing.T, err error, want ProbeServerErrorCode) {
	t.Helper()
	var server *ProbeServerError
	if !errors.As(err, &server) || server.Code() != want {
		t.Fatalf("server error = %T %v, want code %q", err, err, want)
	}
}
