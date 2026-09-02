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
)

const mutationServerTestTimeout = 3 * time.Second

var _ func(*UnixListener, context.Context, uint32, MutationHandler) error = (*UnixListener).ServeMutationOnce

func TestServeMutationOnceRoundTripsClosedRequestsAndResponses(t *testing.T) {
	requests := mutationClientTestRequests(t)
	tests := []struct {
		name       string
		request    MutationRequest
		response   MutationResponse
		wantDomain Domain
		wantKind   string
	}{
		{
			name: "infrastructure confirmed", request: requests.infrastructure,
			response: mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
				return NewApplyManagedPlanConfirmedResponse(DomainInfrastructure)
			}),
			wantDomain: DomainInfrastructure, wantKind: "apply",
		},
		{
			name: "policy rejected", request: requests.policy,
			response: mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
				return NewApplyManagedPlanRejectedResponse(DomainPolicy, MutationErrorCodeBackendRejected)
			}),
			wantDomain: DomainPolicy, wantKind: "apply",
		},
		{
			name: "target unknown", request: requests.target,
			response: mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
				return NewApplyManagedPlanUnknownResponse(DomainTarget)
			}),
			wantDomain: DomainTarget, wantKind: "apply",
		},
		{
			name: "remove confirmed", request: requests.remove,
			response: NewRemoveManagedInfrastructureConfirmedResponse(), wantKind: "remove",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			listener, socketPath := newMutationServerTestListener(t)
			var calls atomic.Int32
			received := make(chan MutationRequest, 1)
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- listener.ServeMutationOnce(
					context.Background(),
					uint32(os.Getuid()),
					func(_ context.Context, request MutationRequest) MutationResponse {
						calls.Add(1)
						received <- request
						return test.response
					},
				)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), mutationServerTestTimeout)
			defer cancel()
			response, err := roundTripMutationAt(ctx, socketPath, uint32(os.Getuid()), test.request)
			if err != nil {
				t.Fatalf("roundTripMutationAt(): %v", err)
			}
			if err := awaitMutationServer(t, serverDone); err != nil {
				t.Fatalf("ServeMutationOnce(): %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("handler calls = %d, want 1", calls.Load())
			}
			select {
			case got := <-received:
				if !sameMutationRequest(test.request, got) {
					t.Fatalf("handler request = %#v, want %#v", got, test.request)
				}
			default:
				t.Fatal("handler request was not captured")
			}
			assertMutationClientResponse(
				t, response, test.request.Operation(), test.response.Status(), test.wantDomain,
				mutationResponseCode(test.response), mutationResponseHasCode(test.response), test.wantKind,
			)
		})
	}
}

func TestServeMutationOnceRejectsBeforeHandlerOrWrite(t *testing.T) {
	t.Run("peer mismatch", func(t *testing.T) {
		listener, socketPath := newMutationServerTestListener(t)
		var calls atomic.Int32
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeMutationOnce(
				context.Background(),
				differentUID(uint32(os.Getuid())),
				func(context.Context, MutationRequest) MutationResponse {
					calls.Add(1)
					return NewRemoveManagedInfrastructureConfirmedResponse()
				},
			)
		}()

		connection := dialMutationServer(t, socketPath)
		_ = connection.Close()
		err := awaitMutationServer(t, serverDone)
		assertPeerErrorCode(t, err, PeerErrorCodeUIDMismatch)
		if calls.Load() != 0 {
			t.Fatalf("handler calls = %d, want 0", calls.Load())
		}
	})

	t.Run("unexpected observation operation", func(t *testing.T) {
		listener, socketPath := newMutationServerTestListener(t)
		var calls atomic.Int32
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeMutationOnce(
				context.Background(),
				uint32(os.Getuid()),
				func(context.Context, MutationRequest) MutationResponse {
					calls.Add(1)
					return NewRemoveManagedInfrastructureConfirmedResponse()
				},
			)
		}()

		connection := dialMutationServer(t, socketPath)
		if err := WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest()); err != nil {
			t.Fatalf("WriteProbeCapabilitiesRequestFrame(): %v", err)
		}
		assertMutationServerWroteNothing(t, connection)
		assertMutationServerErrorCode(t, awaitMutationServer(t, serverDone), MutationServerErrorCodeUnexpectedOperation)
		if calls.Load() != 0 {
			t.Fatalf("handler calls = %d, want 0", calls.Load())
		}
	})

	requests := mutationClientTestRequests(t)
	wrongDomain := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
		return NewApplyManagedPlanConfirmedResponse(DomainPolicy)
	})
	matchingApply := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
		return NewApplyManagedPlanConfirmedResponse(DomainInfrastructure)
	})
	var nilApply *applyManagedPlanResponse
	var nilRemove *removeManagedInfrastructureResponse
	correlationTests := []struct {
		name     string
		request  MutationRequest
		response MutationResponse
	}{
		{name: "response domain mismatch", request: requests.infrastructure, response: wrongDomain},
		{
			name: "apply response operation mismatch", request: requests.infrastructure,
			response: NewRemoveManagedInfrastructureConfirmedResponse(),
		},
		{name: "remove response operation mismatch", request: requests.remove, response: matchingApply},
		{name: "nil response", request: requests.remove, response: nil},
		{name: "typed nil apply response", request: requests.infrastructure, response: nilApply},
		{name: "typed nil remove response", request: requests.remove, response: nilRemove},
	}
	for _, test := range correlationTests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			listener, socketPath := newMutationServerTestListener(t)
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- listener.ServeMutationOnce(
					context.Background(),
					uint32(os.Getuid()),
					func(context.Context, MutationRequest) MutationResponse { return test.response },
				)
			}()

			connection := dialMutationServer(t, socketPath)
			if err := WriteMutationRequestFrame(connection, test.request); err != nil {
				t.Fatalf("WriteMutationRequestFrame(): %v", err)
			}
			assertMutationServerWroteNothing(t, connection)
			assertMutationServerErrorCode(
				t,
				awaitMutationServer(t, serverDone),
				MutationServerErrorCodeResponseMismatch,
			)
		})
	}
}

func TestServeMutationOnceProcessesOneRequestAndClosesConnection(t *testing.T) {
	listener, socketPath := newMutationServerTestListener(t)
	request := mutationClientTestRequests(t).remove
	response := NewRemoveManagedInfrastructureConfirmedResponse()
	var calls atomic.Int32
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeMutationOnce(
			context.Background(),
			uint32(os.Getuid()),
			func(context.Context, MutationRequest) MutationResponse {
				calls.Add(1)
				return response
			},
		)
	}()

	connection := dialMutationServer(t, socketPath)
	if err := WriteMutationRequestFrame(connection, request); err != nil {
		t.Fatalf("first WriteMutationRequestFrame(): %v", err)
	}
	// The server may close while this extra frame is being written. Either a
	// successful write or a terminal write error is consistent with serving only
	// the first request, so the observable contract is asserted below.
	_ = WriteMutationRequestFrame(connection, request)
	decoded, err := DecodeMutationResponseFrame(connection)
	if err != nil {
		t.Fatalf("DecodeMutationResponseFrame(): %v", err)
	}
	assertMutationClientResponse(
		t, decoded, OperationRemoveManagedInfrastructure, MutationStatusConfirmed,
		"", "", false, "remove",
	)
	_ = connection.SetReadDeadline(time.Now().Add(mutationServerTestTimeout))
	var trailing [1]byte
	read, readErr := connection.Read(trailing[:])
	_ = connection.Close()
	var networkError net.Error
	if read != 0 || readErr == nil || (errors.As(readErr, &networkError) && networkError.Timeout()) {
		t.Fatalf("read after response = (%d, %v), want bounded connection termination", read, readErr)
	}
	if err := awaitMutationServer(t, serverDone); err != nil {
		t.Fatalf("ServeMutationOnce(): %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestServeMutationOnceValidatesLifecycleBoundaries(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var listener *UnixListener
		err := listener.ServeMutationOnce(context.Background(), 0, func(context.Context, MutationRequest) MutationResponse {
			return NewRemoveManagedInfrastructureConfirmedResponse()
		})
		assertMutationServerErrorCode(t, err, MutationServerErrorCodeUnavailable)
	})

	t.Run("nil handler", func(t *testing.T) {
		assertMutationServerErrorCode(
			t,
			(&UnixListener{}).ServeMutationOnce(context.Background(), 0, nil),
			MutationServerErrorCodeHandlerRequired,
		)
	})

	t.Run("canceled before accept", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		listener := &UnixListener{}
		err := listener.ServeMutationOnce(ctx, 0, func(context.Context, MutationRequest) MutationResponse {
			return NewRemoveManagedInfrastructureConfirmedResponse()
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeMutationOnce() error = %T %v, want context canceled", err, err)
		}
	})

	t.Run("canceled by handler writes nothing", func(t *testing.T) {
		listener, socketPath := newMutationServerTestListener(t)
		ctx, cancel := context.WithCancel(context.Background())
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeMutationOnce(
				ctx,
				uint32(os.Getuid()),
				func(context.Context, MutationRequest) MutationResponse {
					cancel()
					return NewRemoveManagedInfrastructureConfirmedResponse()
				},
			)
		}()

		connection := dialMutationServer(t, socketPath)
		if err := WriteMutationRequestFrame(connection, mutationClientTestRequests(t).remove); err != nil {
			t.Fatalf("WriteMutationRequestFrame(): %v", err)
		}
		assertMutationServerWroteNothing(t, connection)
		if err := awaitMutationServer(t, serverDone); !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeMutationOnce() error = %T %v, want context canceled", err, err)
		}
	})
}

func TestMutationServerWriteCancellationLinearization(t *testing.T) {
	t.Run("incomplete write preserves cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writer := &mutationServerCancellationWriter{cancel: cancel, failAfterCancel: true}
		err := writeMutationServerPayload(ctx, writer, []byte("mutation-response"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write error = %T %v, want context canceled", err, err)
		}
	})

	t.Run("complete frame wins later cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writer := &mutationServerCancellationWriter{cancel: cancel}
		if err := writeMutationServerPayload(ctx, writer, []byte("mutation-response")); err != nil {
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

type mutationServerCancellationWriter struct {
	cancel          context.CancelFunc
	failAfterCancel bool
	writes          int
}

func (w *mutationServerCancellationWriter) Write(value []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		w.cancel()
		if w.failAfterCancel {
			return 0, io.ErrClosedPipe
		}
	}
	return len(value), nil
}

func newMutationServerTestListener(t *testing.T) (*UnixListener, string) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "mutation-server.sock")
	raw, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix(%q): %v", socketPath, err)
	}
	raw.SetUnlinkOnClose(true)
	t.Cleanup(func() { _ = raw.Close() })
	return &UnixListener{listener: raw, socketPath: socketPath}, socketPath
}

func dialMutationServer(t *testing.T, socketPath string) *net.UnixConn {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix(%q): %v", socketPath, err)
	}
	return connection
}

func awaitMutationServer(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(mutationServerTestTimeout):
		t.Fatal("mutation server did not finish")
		return nil
	}
}

func assertMutationServerWroteNothing(t *testing.T, connection *net.UnixConn) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(mutationServerTestTimeout))
	value, err := io.ReadAll(connection)
	_ = connection.Close()
	if len(value) != 0 {
		t.Fatalf("server wrote %d bytes, want 0", len(value))
	}
	if err != nil {
		t.Fatalf("read server connection: %v", err)
	}
}

func assertMutationServerErrorCode(t *testing.T, err error, want MutationServerErrorCode) {
	t.Helper()
	var typed *MutationServerError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %T %v, want *MutationServerError", err, err)
	}
	if got := typed.Code(); got != want {
		t.Fatalf("error code = %q, want %q", got, want)
	}
	if got := err.Error(); got != "ipc mutation server failed: "+string(want) {
		t.Fatalf("error text = %q, want stable classification", got)
	}
}

func sameMutationRequest(left, right MutationRequest) bool {
	leftPayload, leftErr := EncodeMutationRequest(left)
	rightPayload, rightErr := EncodeMutationRequest(right)
	return leftErr == nil && rightErr == nil && string(leftPayload) == string(rightPayload)
}

func mutationResponseCode(response MutationResponse) MutationErrorCode {
	code, _ := response.ErrorCode()
	return code
}

func mutationResponseHasCode(response MutationResponse) bool {
	_, present := response.ErrorCode()
	return present
}
