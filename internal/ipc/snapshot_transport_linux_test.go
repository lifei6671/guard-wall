//go:build linux

package ipc

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/firewall"
)

var _ func(context.Context) (firewall.ManagedSnapshot, error) = RoundTripSnapshotManaged

func TestSnapshotManagedRealUnixSocketEndToEnd(t *testing.T) {
	listener, socketPath := newProbeTransportListener(t)
	want := mustSnapshotTransportSnapshot(t)
	response := mustSnapshotTransportSuccess(t, want)
	var calls atomic.Int32
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeSnapshotManagedOnce(
			context.Background(),
			uint32(os.Getuid()),
			func(context.Context) SnapshotManagedResponse {
				calls.Add(1)
				return response
			},
		)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), probeTransportTestTimeout)
	defer cancel()
	got, err := roundTripSnapshotManagedAt(ctx, socketPath, uint32(os.Getuid()))
	if err != nil {
		t.Fatalf("roundTripSnapshotManagedAt(): %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("returned snapshot invalid: %v", err)
	}
	if got.Digest() != want.Digest() {
		t.Fatalf("snapshot digest = %q, want %q", got.Digest(), want.Digest())
	}
	if err := awaitProbeTransportServer(t, serverDone); err != nil {
		t.Fatalf("ServeSnapshotManagedOnce(): %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestSnapshotManagedClosedRemoteFailures(t *testing.T) {
	for _, code := range []SnapshotManagedFailureCode{
		SnapshotManagedFailureCodeUnsupported,
		SnapshotManagedFailureCodeNotReady,
		SnapshotManagedFailureCodeOwnershipConflict,
	} {
		t.Run(string(code), func(t *testing.T) {
			listener, socketPath := newProbeTransportListener(t)
			response := mustSnapshotTransportFailure(t, code)
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- listener.ServeSnapshotManagedOnce(
					context.Background(), uint32(os.Getuid()),
					func(context.Context) SnapshotManagedResponse { return response },
				)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), probeTransportTestTimeout)
			defer cancel()
			snapshot, err := roundTripSnapshotManagedAt(ctx, socketPath, uint32(os.Getuid()))
			if snapshot.Digest() != "" {
				t.Fatalf("failure returned non-zero snapshot")
			}
			var remote *SnapshotManagedRemoteError
			if !errors.As(err, &remote) || remote.Code() != code {
				t.Fatalf("error = %T %v, want remote code %q", err, err, code)
			}
			if err := awaitProbeTransportServer(t, serverDone); err != nil {
				t.Fatalf("server error: %v", err)
			}
		})
	}
}

func TestSnapshotManagedClientAuthenticatesServerBeforeWrite(t *testing.T) {
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
	_, err := roundTripSnapshotManagedAt(ctx, socketPath, differentUID(uint32(os.Getuid())))
	var peer *PeerError
	if !errors.As(err, &peer) || peer.Code() != PeerErrorCodeUIDMismatch {
		t.Fatalf("error = %T %v, want peer UID mismatch", err, err)
	}
	select {
	case result := <-serverDone:
		if result.bytes != 0 {
			t.Fatalf("server received %d bytes before authentication", result.bytes)
		}
	case <-time.After(probeTransportTestTimeout):
		t.Fatal("server did not observe client close")
	}
}

func TestSnapshotManagedServerAuthenticatesAndRoutesBeforeHandler(t *testing.T) {
	t.Run("peer mismatch", func(t *testing.T) {
		listener, socketPath := newProbeTransportListener(t)
		var calls atomic.Int32
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeSnapshotManagedOnce(
				context.Background(), differentUID(uint32(os.Getuid())),
				func(context.Context) SnapshotManagedResponse {
					calls.Add(1)
					return mustSnapshotTransportSuccess(t, mustSnapshotTransportSnapshot(t))
				},
			)
		}()
		connection := dialRawProbeTransport(t, socketPath)
		_ = WriteSnapshotManagedRequestFrame(connection, NewSnapshotManagedRequest())
		_ = connection.Close()
		err := awaitProbeTransportServer(t, serverDone)
		var peer *PeerError
		if !errors.As(err, &peer) || peer.Code() != PeerErrorCodeUIDMismatch || calls.Load() != 0 {
			t.Fatalf("server error/calls = %T %v/%d", err, err, calls.Load())
		}
	})

	t.Run("other operation", func(t *testing.T) {
		listener, socketPath := newProbeTransportListener(t)
		var calls atomic.Int32
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeSnapshotManagedOnce(
				context.Background(), uint32(os.Getuid()),
				func(context.Context) SnapshotManagedResponse {
					calls.Add(1)
					return mustSnapshotTransportFailure(t, SnapshotManagedFailureCodeNotReady)
				},
			)
		}()
		connection := dialRawProbeTransport(t, socketPath)
		if err := WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest()); err != nil {
			t.Fatalf("write Probe request: %v", err)
		}
		_ = connection.Close()
		err := awaitProbeTransportServer(t, serverDone)
		var server *SnapshotServerError
		if !errors.As(err, &server) || server.Code() != SnapshotServerErrorCodeUnexpectedOperation || calls.Load() != 0 {
			t.Fatalf("server error/calls = %T %v/%d", err, err, calls.Load())
		}
	})
}

func TestSnapshotManagedCancellationAndDeliveryPoint(t *testing.T) {
	t.Run("handler cancellation writes nothing", func(t *testing.T) {
		listener, socketPath := newProbeTransportListener(t)
		serverCtx, cancelServer := context.WithCancel(context.Background())
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeSnapshotManagedOnce(
				serverCtx, uint32(os.Getuid()),
				func(context.Context) SnapshotManagedResponse {
					cancelServer()
					return mustSnapshotTransportSuccess(t, mustSnapshotTransportSnapshot(t))
				},
			)
		}()
		connection := dialRawProbeTransport(t, socketPath)
		if err := WriteSnapshotManagedRequestFrame(connection, NewSnapshotManagedRequest()); err != nil {
			t.Fatal(err)
		}
		_ = connection.SetReadDeadline(time.Now().Add(probeTransportTestTimeout))
		responseBytes, readErr := io.ReadAll(connection)
		_ = connection.Close()
		if len(responseBytes) != 0 || readErr != nil {
			t.Fatalf("response bytes/read error = %d/%v", len(responseBytes), readErr)
		}
		if err := awaitProbeTransportServer(t, serverDone); !errors.Is(err, context.Canceled) {
			t.Fatalf("server error = %T %v, want canceled", err, err)
		}
	})

	t.Run("incomplete write preserves cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writer := &probeServerCancellationWriter{cancel: cancel, failAfterCancel: true}
		err := writeSnapshotManagedServerPayload(ctx, writer, []byte("snapshot-response"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write error = %T %v, want canceled", err, err)
		}
	})

	t.Run("complete frame wins later cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writer := &probeServerCancellationWriter{cancel: cancel}
		if err := writeSnapshotManagedServerPayload(ctx, writer, []byte("snapshot-response")); err != nil {
			t.Fatalf("complete write error: %v", err)
		}
		if !errors.Is(ctx.Err(), context.Canceled) || writer.writes != 2 {
			t.Fatalf("context/writes = %v/%d", ctx.Err(), writer.writes)
		}
	})
}

func TestSnapshotManagedClientDeadlineNoRetryAndPreCancel(t *testing.T) {
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
	_, err := roundTripSnapshotManagedAt(ctx, socketPath, uint32(os.Getuid()))
	var client *SnapshotClientError
	if !errors.As(err, &client) || client.Code() != SnapshotClientErrorCodeContextDeadlineExceeded ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("client error = %T %v, want deadline", err, err)
	}
	select {
	case result := <-serverDone:
		if result.err != nil || result.accepts != 1 {
			t.Fatalf("server result = %+v", result)
		}
	case <-time.After(probeTransportTestTimeout):
		t.Fatal("server did not finish no-retry proof")
	}

	preCanceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	_, err = roundTripSnapshotManagedAt(preCanceled, filepath.Join(t.TempDir(), "absent.sock"), uint32(os.Getuid()))
	if !errors.As(err, &client) || client.Code() != SnapshotClientErrorCodeContextCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %T %v", err, err)
	}
}

func TestSnapshotManagedServerRejectsNilDependencies(t *testing.T) {
	var listener *UnixListener
	err := listener.ServeSnapshotManagedOnce(context.Background(), uint32(os.Getuid()), func(context.Context) SnapshotManagedResponse { return nil })
	assertSnapshotServerCode(t, err, SnapshotServerErrorCodeUnavailable)

	realListener, _ := newProbeTransportListener(t)
	err = realListener.ServeSnapshotManagedOnce(context.Background(), uint32(os.Getuid()), nil)
	assertSnapshotServerCode(t, err, SnapshotServerErrorCodeHandlerRequired)
}

func mustSnapshotTransportSnapshot(t *testing.T) firewall.ManagedSnapshot {
	t.Helper()
	infrastructure, err := firewall.NewInfrastructureObservation(firewall.InfrastructureObservationSpec{
		Backend: firewall.BackendKindNftablesNative, OwnerVersion: firewall.ManagedOwnerVersionV1,
		SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1, Digest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{RelationDigest: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	target, err := firewall.NewTargetObservation(firewall.TargetObservationSpec{
		Target: netip.MustParsePrefix("192.0.2.1/32"), TimeoutMode: firewall.ManagedTimeoutNone,
		Scopes: []firewall.ManagedScope{firewall.ManagedScopeInput},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := firewall.NewManagedState(firewall.ManagedStateSpec{Infrastructure: &infrastructure, Policy: &policy, Targets: []firewall.TargetObservation{target}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: state, ForeignContext: foreign})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func mustSnapshotTransportSuccess(t *testing.T, snapshot firewall.ManagedSnapshot) SnapshotManagedSuccessResponse {
	t.Helper()
	response, err := NewSnapshotManagedSuccessResponse(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func mustSnapshotTransportFailure(t *testing.T, code SnapshotManagedFailureCode) SnapshotManagedFailureResponse {
	t.Helper()
	response, err := NewSnapshotManagedFailureResponse(code)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertSnapshotServerCode(t *testing.T, err error, want SnapshotServerErrorCode) {
	t.Helper()
	var server *SnapshotServerError
	if !errors.As(err, &server) || server.Code() != want {
		t.Fatalf("server error = %T %v, want %q", err, err, want)
	}
}
