//go:build linux

package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

var _ func(*UnixListener, context.Context, uint32, EnforcerHandlers) error = (*UnixListener).ServeEnforcerOnce

func TestServeEnforcerOnceRoutesClosedOperations(t *testing.T) {
	requests := mutationClientTestRequests(t)
	probeResponse := mustProbeTransportSuccess(t, mustProbeTransportCapabilities(t))
	snapshotResponse := mustSnapshotTransportSuccess(t, mustSnapshotTransportSnapshot(t))
	applyResponse := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
		return NewApplyManagedPlanConfirmedResponse(DomainInfrastructure)
	})
	policyResponse := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
		return NewApplyManagedPlanConfirmedResponse(DomainPolicy)
	})
	targetResponse := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
		return NewApplyManagedPlanConfirmedResponse(DomainTarget)
	})
	removeResponse := NewRemoveManagedInfrastructureConfirmedResponse()

	tests := []struct {
		name         string
		writeRequest func(*net.UnixConn) error
		readResponse func(*net.UnixConn) error
		wantProbe    int32
		wantSnapshot int32
		wantMutation int32
	}{
		{
			name: "probe capabilities",
			writeRequest: func(connection *net.UnixConn) error {
				return WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest())
			},
			readResponse: func(connection *net.UnixConn) error {
				response, err := DecodeProbeCapabilitiesResponseFrame(connection)
				if err != nil {
					return err
				}
				return requireSameProbeResponse(probeResponse, response)
			},
			wantProbe: 1,
		},
		{
			name: "snapshot managed",
			writeRequest: func(connection *net.UnixConn) error {
				return WriteSnapshotManagedRequestFrame(connection, NewSnapshotManagedRequest())
			},
			readResponse: func(connection *net.UnixConn) error {
				response, err := DecodeSnapshotManagedResponseFrame(connection)
				if err != nil {
					return err
				}
				return requireSameSnapshotResponse(snapshotResponse, response)
			},
			wantSnapshot: 1,
		},
		{
			name: "apply managed plan",
			writeRequest: func(connection *net.UnixConn) error {
				return WriteMutationRequestFrame(connection, requests.infrastructure)
			},
			readResponse: func(connection *net.UnixConn) error {
				response, err := DecodeMutationResponseFrame(connection)
				if err != nil {
					return err
				}
				return requireSameMutationResponse(applyResponse, response)
			},
			wantMutation: 1,
		},
		{
			name: "apply policy plan",
			writeRequest: func(connection *net.UnixConn) error {
				return WriteMutationRequestFrame(connection, requests.policy)
			},
			readResponse: func(connection *net.UnixConn) error {
				response, err := DecodeMutationResponseFrame(connection)
				if err != nil {
					return err
				}
				return requireSameMutationResponse(policyResponse, response)
			},
			wantMutation: 1,
		},
		{
			name: "apply target plan",
			writeRequest: func(connection *net.UnixConn) error {
				return WriteMutationRequestFrame(connection, requests.target)
			},
			readResponse: func(connection *net.UnixConn) error {
				response, err := DecodeMutationResponseFrame(connection)
				if err != nil {
					return err
				}
				return requireSameMutationResponse(targetResponse, response)
			},
			wantMutation: 1,
		},
		{
			name: "remove managed infrastructure",
			writeRequest: func(connection *net.UnixConn) error {
				return WriteMutationRequestFrame(connection, requests.remove)
			},
			readResponse: func(connection *net.UnixConn) error {
				response, err := DecodeMutationResponseFrame(connection)
				if err != nil {
					return err
				}
				return requireSameMutationResponse(removeResponse, response)
			},
			wantMutation: 1,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			listener, socketPath := newMutationServerTestListener(t)
			var probeCalls atomic.Int32
			var snapshotCalls atomic.Int32
			var mutationCalls atomic.Int32
			handlers := EnforcerHandlers{
				ProbeCapabilities: func(context.Context) ProbeCapabilitiesResponse {
					probeCalls.Add(1)
					return probeResponse
				},
				SnapshotManaged: func(context.Context) SnapshotManagedResponse {
					snapshotCalls.Add(1)
					return snapshotResponse
				},
				Mutation: func(_ context.Context, request MutationRequest) MutationResponse {
					mutationCalls.Add(1)
					if apply, ok := request.(ApplyManagedPlanRequest); ok {
						response, responseErr := NewApplyManagedPlanConfirmedResponse(apply.Plan().Domain())
						if responseErr != nil {
							t.Errorf("construct apply response: %v", responseErr)
							return nil
						}
						return response
					}
					return removeResponse
				},
			}
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- listener.ServeEnforcerOnce(context.Background(), uint32(os.Getuid()), handlers)
			}()

			connection := dialMutationServer(t, socketPath)
			if err := test.writeRequest(connection); err != nil {
				t.Fatalf("write request: %v", err)
			}
			if err := test.readResponse(connection); err != nil {
				t.Fatalf("read response: %v", err)
			}
			_ = connection.Close()
			if err := awaitMutationServer(t, serverDone); err != nil {
				t.Fatalf("ServeEnforcerOnce(): %v", err)
			}
			if got := probeCalls.Load(); got != test.wantProbe {
				t.Fatalf("probe handler calls = %d, want %d", got, test.wantProbe)
			}
			if got := snapshotCalls.Load(); got != test.wantSnapshot {
				t.Fatalf("snapshot handler calls = %d, want %d", got, test.wantSnapshot)
			}
			if got := mutationCalls.Load(); got != test.wantMutation {
				t.Fatalf("mutation handler calls = %d, want %d", got, test.wantMutation)
			}
		})
	}
}

func TestServeEnforcerOnceRejectsIncompleteHandlersBeforeAccept(t *testing.T) {
	complete := completeEnforcerTestHandlers(t)
	tests := []struct {
		name     string
		handlers EnforcerHandlers
	}{
		{name: "all missing"},
		{name: "probe missing", handlers: EnforcerHandlers{
			SnapshotManaged: complete.SnapshotManaged, Mutation: complete.Mutation,
		}},
		{name: "snapshot missing", handlers: EnforcerHandlers{
			ProbeCapabilities: complete.ProbeCapabilities, Mutation: complete.Mutation,
		}},
		{name: "mutation missing", handlers: EnforcerHandlers{
			ProbeCapabilities: complete.ProbeCapabilities, SnapshotManaged: complete.SnapshotManaged,
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			assertEnforcerServerCode(
				t,
				(&UnixListener{}).ServeEnforcerOnce(context.Background(), 0, test.handlers),
				EnforcerServerErrorCodeHandlerRequired,
			)
		})
	}

	var listener *UnixListener
	assertEnforcerServerCode(
		t,
		listener.ServeEnforcerOnce(context.Background(), 0, complete),
		EnforcerServerErrorCodeUnavailable,
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&UnixListener{}).ServeEnforcerOnce(ctx, 0, complete); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled ServeEnforcerOnce() error = %T %v, want context canceled", err, err)
	}
}

func TestServeEnforcerOnceRejectsBeforeHandlerOrWrite(t *testing.T) {
	t.Run("peer mismatch", func(t *testing.T) {
		listener, socketPath := newMutationServerTestListener(t)
		handlers, calls := countedEnforcerTestHandlers(t)
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeEnforcerOnce(
				context.Background(), differentUID(uint32(os.Getuid())), handlers,
			)
		}()

		connection := dialMutationServer(t, socketPath)
		_ = WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest())
		_ = connection.Close()
		assertPeerErrorCode(t, awaitMutationServer(t, serverDone), PeerErrorCodeUIDMismatch)
		calls.assertZero(t)
	})

	t.Run("invalid frame", func(t *testing.T) {
		listener, socketPath := newMutationServerTestListener(t)
		handlers, calls := countedEnforcerTestHandlers(t)
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeEnforcerOnce(context.Background(), uint32(os.Getuid()), handlers)
		}()

		connection := dialMutationServer(t, socketPath)
		if _, err := connection.Write([]byte{0, 0, 0, 1, '{'}); err != nil {
			t.Fatalf("write invalid frame: %v", err)
		}
		assertMutationServerWroteNothing(t, connection)
		if err := awaitMutationServer(t, serverDone); err == nil {
			t.Fatal("ServeEnforcerOnce() accepted invalid frame")
		}
		calls.assertZero(t)
	})

	t.Run("mutation response mismatch", func(t *testing.T) {
		listener, socketPath := newMutationServerTestListener(t)
		requests := mutationClientTestRequests(t)
		wrongDomain := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
			return NewApplyManagedPlanConfirmedResponse(DomainPolicy)
		})
		handlers := completeEnforcerTestHandlers(t)
		handlers.Mutation = func(context.Context, MutationRequest) MutationResponse { return wrongDomain }
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeEnforcerOnce(context.Background(), uint32(os.Getuid()), handlers)
		}()

		connection := dialMutationServer(t, socketPath)
		if err := WriteMutationRequestFrame(connection, requests.infrastructure); err != nil {
			t.Fatalf("write mutation request: %v", err)
		}
		assertMutationServerWroteNothing(t, connection)
		assertEnforcerServerCode(
			t, awaitMutationServer(t, serverDone), EnforcerServerErrorCodeResponseMismatch,
		)
	})

	t.Run("typed nil responses", func(t *testing.T) {
		tests := []struct {
			name     string
			serve    func(EnforcerHandlers, *net.UnixConn) error
			handlers func(EnforcerHandlers) EnforcerHandlers
		}{
			{
				name: "probe",
				serve: func(_ EnforcerHandlers, connection *net.UnixConn) error {
					return WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest())
				},
				handlers: func(handlers EnforcerHandlers) EnforcerHandlers {
					var response *probeCapabilitiesSuccessResponse
					handlers.ProbeCapabilities = func(context.Context) ProbeCapabilitiesResponse { return response }
					return handlers
				},
			},
			{
				name: "snapshot",
				serve: func(_ EnforcerHandlers, connection *net.UnixConn) error {
					return WriteSnapshotManagedRequestFrame(connection, NewSnapshotManagedRequest())
				},
				handlers: func(handlers EnforcerHandlers) EnforcerHandlers {
					var response *snapshotManagedSuccessResponse
					handlers.SnapshotManaged = func(context.Context) SnapshotManagedResponse { return response }
					return handlers
				},
			},
			{
				name: "mutation",
				serve: func(_ EnforcerHandlers, connection *net.UnixConn) error {
					return WriteMutationRequestFrame(connection, mutationClientTestRequests(t).remove)
				},
				handlers: func(handlers EnforcerHandlers) EnforcerHandlers {
					var response *removeManagedInfrastructureResponse
					handlers.Mutation = func(context.Context, MutationRequest) MutationResponse { return response }
					return handlers
				},
			},
		}
		for _, test := range tests {
			test := test
			t.Run(test.name, func(t *testing.T) {
				listener, socketPath := newMutationServerTestListener(t)
				handlers := test.handlers(completeEnforcerTestHandlers(t))
				serverDone := make(chan error, 1)
				go func() {
					serverDone <- listener.ServeEnforcerOnce(context.Background(), uint32(os.Getuid()), handlers)
				}()
				connection := dialMutationServer(t, socketPath)
				if err := test.serve(handlers, connection); err != nil {
					t.Fatalf("write request: %v", err)
				}
				assertMutationServerWroteNothing(t, connection)
				if err := awaitMutationServer(t, serverDone); err == nil {
					t.Fatal("ServeEnforcerOnce() accepted typed nil response")
				}
			})
		}
	})
}

func TestServeEnforcerOnceContextAndConnectionLifecycle(t *testing.T) {
	t.Run("handler cancellation writes nothing", func(t *testing.T) {
		listener, socketPath := newMutationServerTestListener(t)
		ctx, cancel := context.WithCancel(context.Background())
		handlers := completeEnforcerTestHandlers(t)
		response := mustProbeTransportSuccess(t, mustProbeTransportCapabilities(t))
		handlers.ProbeCapabilities = func(context.Context) ProbeCapabilitiesResponse {
			cancel()
			return response
		}
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- listener.ServeEnforcerOnce(ctx, uint32(os.Getuid()), handlers)
		}()

		connection := dialMutationServer(t, socketPath)
		if err := WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest()); err != nil {
			t.Fatalf("write probe request: %v", err)
		}
		assertMutationServerWroteNothing(t, connection)
		if err := awaitMutationServer(t, serverDone); !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeEnforcerOnce() error = %T %v, want context canceled", err, err)
		}
	})

	t.Run("one frame closes connection and listener remains usable", func(t *testing.T) {
		listener, socketPath := newMutationServerTestListener(t)
		handlers := completeEnforcerTestHandlers(t)
		for attempt := 0; attempt < 2; attempt++ {
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- listener.ServeEnforcerOnce(context.Background(), uint32(os.Getuid()), handlers)
			}()
			connection := dialMutationServer(t, socketPath)
			if err := WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest()); err != nil {
				t.Fatalf("attempt %d write first frame: %v", attempt, err)
			}
			_ = WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest())
			if _, err := DecodeProbeCapabilitiesResponseFrame(connection); err != nil {
				t.Fatalf("attempt %d decode response: %v", attempt, err)
			}
			_ = connection.SetReadDeadline(time.Now().Add(mutationServerTestTimeout))
			var trailing [1]byte
			read, readErr := connection.Read(trailing[:])
			_ = connection.Close()
			var networkError net.Error
			if read != 0 || readErr == nil || (errors.As(readErr, &networkError) && networkError.Timeout()) {
				t.Fatalf("attempt %d termination = (%d, %v), want bounded close", attempt, read, readErr)
			}
			if err := awaitMutationServer(t, serverDone); err != nil {
				t.Fatalf("attempt %d ServeEnforcerOnce(): %v", attempt, err)
			}
		}
	})
}

func TestEnforcerServerWriteCancellationLinearization(t *testing.T) {
	t.Run("incomplete write preserves cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writer := &enforcerServerCancellationWriter{cancel: cancel, failAfterCancel: true}
		err := writeEnforcerServerPayload(ctx, writer, []byte("enforcer-response"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write error = %T %v, want context canceled", err, err)
		}
	})

	t.Run("complete frame wins later cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writer := &enforcerServerCancellationWriter{cancel: cancel}
		if err := writeEnforcerServerPayload(ctx, writer, []byte("enforcer-response")); err != nil {
			t.Fatalf("complete write error = %v", err)
		}
		if !errors.Is(ctx.Err(), context.Canceled) || writer.writes != 2 {
			t.Fatalf("context/writes = %v/%d, want canceled/2", ctx.Err(), writer.writes)
		}
	})
}

type enforcerHandlerCalls struct {
	probe    atomic.Int32
	snapshot atomic.Int32
	mutation atomic.Int32
}

func (c *enforcerHandlerCalls) assertZero(t *testing.T) {
	t.Helper()
	if probe, snapshot, mutation := c.probe.Load(), c.snapshot.Load(), c.mutation.Load(); probe != 0 || snapshot != 0 || mutation != 0 {
		t.Fatalf("handler calls = probe:%d snapshot:%d mutation:%d, want all zero", probe, snapshot, mutation)
	}
}

func countedEnforcerTestHandlers(t *testing.T) (EnforcerHandlers, *enforcerHandlerCalls) {
	t.Helper()
	calls := &enforcerHandlerCalls{}
	probeResponse := mustProbeTransportSuccess(t, mustProbeTransportCapabilities(t))
	snapshotResponse := mustSnapshotTransportSuccess(t, mustSnapshotTransportSnapshot(t))
	return EnforcerHandlers{
		ProbeCapabilities: func(context.Context) ProbeCapabilitiesResponse {
			calls.probe.Add(1)
			return probeResponse
		},
		SnapshotManaged: func(context.Context) SnapshotManagedResponse {
			calls.snapshot.Add(1)
			return snapshotResponse
		},
		Mutation: func(_ context.Context, request MutationRequest) MutationResponse {
			calls.mutation.Add(1)
			if apply, ok := request.(ApplyManagedPlanRequest); ok {
				response, err := NewApplyManagedPlanConfirmedResponse(apply.Plan().Domain())
				if err != nil {
					t.Errorf("construct apply response: %v", err)
					return nil
				}
				return response
			}
			return NewRemoveManagedInfrastructureConfirmedResponse()
		},
	}, calls
}

func completeEnforcerTestHandlers(t *testing.T) EnforcerHandlers {
	t.Helper()
	handlers, _ := countedEnforcerTestHandlers(t)
	return handlers
}

func requireSameProbeResponse(want, got ProbeCapabilitiesResponse) error {
	wantPayload, err := EncodeProbeCapabilitiesResponse(want)
	if err != nil {
		return err
	}
	gotPayload, err := EncodeProbeCapabilitiesResponse(got)
	if err != nil {
		return err
	}
	if string(wantPayload) != string(gotPayload) {
		return errors.New("probe response mismatch")
	}
	return nil
}

func requireSameSnapshotResponse(want, got SnapshotManagedResponse) error {
	wantPayload, err := EncodeSnapshotManagedResponse(want)
	if err != nil {
		return err
	}
	gotPayload, err := EncodeSnapshotManagedResponse(got)
	if err != nil {
		return err
	}
	if string(wantPayload) != string(gotPayload) {
		return errors.New("snapshot response mismatch")
	}
	return nil
}

func requireSameMutationResponse(want, got MutationResponse) error {
	wantPayload, err := EncodeMutationResponse(want)
	if err != nil {
		return err
	}
	gotPayload, err := EncodeMutationResponse(got)
	if err != nil {
		return err
	}
	if string(wantPayload) != string(gotPayload) {
		return errors.New("mutation response mismatch")
	}
	return nil
}

func assertEnforcerServerCode(t *testing.T, err error, want EnforcerServerErrorCode) {
	t.Helper()
	var typed *EnforcerServerError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %T %v, want *EnforcerServerError", err, err)
	}
	if got := typed.Code(); got != want {
		t.Fatalf("error code = %q, want %q", got, want)
	}
	if got := err.Error(); got != "ipc enforcer server failed: "+string(want) {
		t.Fatalf("error text = %q, want stable classification", got)
	}
}

type enforcerServerCancellationWriter struct {
	cancel          context.CancelFunc
	failAfterCancel bool
	writes          int
}

func (w *enforcerServerCancellationWriter) Write(value []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		w.cancel()
		if w.failAfterCancel {
			return 0, io.ErrClosedPipe
		}
	}
	return len(value), nil
}
