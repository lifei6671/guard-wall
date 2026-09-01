//go:build linux

package ipc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const mutationClientTestTimeout = 3 * time.Second

var _ func(context.Context, MutationRequest) (MutationResponse, error) = RoundTripMutation

func TestRoundTripMutationFourRequestKindsAndTypedResponses(t *testing.T) {
	requests := mutationClientTestRequests(t)
	policyRejected := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
		return NewApplyManagedPlanRejectedResponse(DomainPolicy, MutationErrorCodeBackendRejected)
	})
	targetUnknown := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
		return NewApplyManagedPlanUnknownResponse(DomainTarget)
	})

	tests := []struct {
		name         string
		request      MutationRequest
		response     MutationResponse
		wantStatus   MutationStatus
		wantDomain   Domain
		wantCode     MutationErrorCode
		wantCodeSet  bool
		wantResponse string
	}{
		{
			name: "apply infrastructure confirmed", request: requests.infrastructure,
			response: mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
				return NewApplyManagedPlanConfirmedResponse(DomainInfrastructure)
			}),
			wantStatus: MutationStatusConfirmed, wantDomain: DomainInfrastructure,
			wantResponse: "apply",
		},
		{
			name: "apply policy rejected", request: requests.policy,
			response: policyRejected, wantStatus: MutationStatusRejected,
			wantDomain: DomainPolicy, wantCode: MutationErrorCodeBackendRejected,
			wantCodeSet: true, wantResponse: "apply",
		},
		{
			name: "apply target typed unknown", request: requests.target,
			response: targetUnknown, wantStatus: MutationStatusUnknown,
			wantDomain: DomainTarget, wantCode: MutationErrorCodeUnknownResult,
			wantCodeSet: true, wantResponse: "apply",
		},
		{
			name: "remove typed unknown", request: requests.remove,
			response:   NewRemoveManagedInfrastructureUnknownResponse(),
			wantStatus: MutationStatusUnknown, wantCode: MutationErrorCodeUnknownResult,
			wantCodeSet: true, wantResponse: "remove",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			server := startMutationClientServer(t, func(connection *net.UnixConn, _ MutationRequest) error {
				return WriteMutationResponseFrame(connection, test.response)
			})
			ctx, cancel := context.WithTimeout(context.Background(), mutationClientTestTimeout)
			defer cancel()

			response, err := roundTripMutationAt(ctx, server.path, uint32(os.Getuid()), test.request)
			if err != nil {
				t.Fatalf("roundTripMutationAt(): %v", err)
			}
			assertMutationClientResponse(
				t, response, test.request.Operation(), test.wantStatus, test.wantDomain,
				test.wantCode, test.wantCodeSet, test.wantResponse,
			)

			result := awaitMutationClientServer(t, server)
			wantPayload, err := EncodeMutationRequest(test.request)
			if err != nil {
				t.Fatalf("EncodeMutationRequest(want): %v", err)
			}
			if !bytes.Equal(result.requestPayload, wantPayload) {
				t.Fatalf("server request payload = %s, want exact %s", result.requestPayload, wantPayload)
			}
			assertMutationClientSingleConnection(t, result)
		})
	}
}

func TestRoundTripMutationRejectsWrongOperationOrDomain(t *testing.T) {
	requests := mutationClientTestRequests(t)
	tests := []struct {
		name     string
		request  MutationRequest
		response MutationResponse
	}{
		{
			name: "wrong operation for apply", request: requests.infrastructure,
			response: NewRemoveManagedInfrastructureConfirmedResponse(),
		},
		{
			name: "wrong domain for apply", request: requests.infrastructure,
			response: mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
				return NewApplyManagedPlanConfirmedResponse(DomainPolicy)
			}),
		},
		{
			name: "wrong operation for remove", request: requests.remove,
			response: mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
				return NewApplyManagedPlanConfirmedResponse(DomainInfrastructure)
			}),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			server := startMutationClientServer(t, func(connection *net.UnixConn, _ MutationRequest) error {
				return WriteMutationResponseFrame(connection, test.response)
			})
			ctx, cancel := context.WithTimeout(context.Background(), mutationClientTestTimeout)
			defer cancel()

			response, err := roundTripMutationAt(ctx, server.path, uint32(os.Getuid()), test.request)
			if response != nil {
				t.Fatalf("roundTripMutationAt() response = %#v, want nil", response)
			}
			assertMutationClientErrorCode(t, err, MutationClientErrorCodeResponseMismatch)
			assertMutationClientErrorSanitized(t, err, server.path)
			assertMutationClientSingleConnection(t, awaitMutationClientServer(t, server))
		})
	}
}

func TestRoundTripMutationCompleteCorrelatedResponseWinsOverCancellation(t *testing.T) {
	requests := mutationClientTestRequests(t)
	request, ok := requests.infrastructure.(*applyManagedPlanRequest)
	if !ok {
		t.Fatalf("infrastructure request type = %T, want *applyManagedPlanRequest", requests.infrastructure)
	}
	response := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
		return NewApplyManagedPlanConfirmedResponse(DomainInfrastructure)
	})
	requestConsumed := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := startMutationClientServer(t, func(connection *net.UnixConn, _ MutationRequest) error {
		close(requestConsumed)
		select {
		case <-releaseResponse:
		case <-time.After(mutationClientTestTimeout):
			return errors.New("timed out waiting to release response")
		}
		return WriteMutationResponseFrame(connection, response)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callDone := make(chan mutationClientCallResult, 1)
	go func() {
		got, err := roundTripMutationAt(ctx, server.path, uint32(os.Getuid()), request)
		callDone <- mutationClientCallResult{response: got, err: err}
	}()

	select {
	case <-requestConsumed:
	case <-time.After(mutationClientTestTimeout):
		t.Fatal("timed out waiting for server to consume request")
	}
	request.plan = &mutationClientCancelOnDomainPlan{
		ManagedPlan: request.plan,
		cancel:      cancel,
	}
	close(releaseResponse)

	call := awaitMutationClientCall(t, callDone)
	if call.err != nil {
		t.Fatalf("roundTripMutationAt() error = %v, want correlated response", call.err)
	}
	assertMutationClientResponse(
		t, call.response, OperationApplyManagedPlan, MutationStatusConfirmed,
		DomainInfrastructure, "", false, "apply",
	)
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error after correlation = %v, want context.Canceled", ctx.Err())
	}
	assertMutationClientSingleConnection(t, awaitMutationClientServer(t, server))
}

func TestRoundTripMutationAuthenticatesPeerBeforeWriting(t *testing.T) {
	server := startMutationClientPeerObserver(t)
	actualUID := uint32(os.Getuid())
	expectedUID := actualUID + 1
	if expectedUID == actualUID {
		expectedUID = actualUID - 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), mutationClientTestTimeout)
	defer cancel()

	response, err := roundTripMutationAt(
		ctx, server.path, expectedUID, mutationClientTestRequests(t).remove,
	)
	if response != nil {
		t.Fatalf("roundTripMutationAt() response = %#v, want nil", response)
	}
	assertPeerErrorCode(t, err, PeerErrorCodeUIDMismatch)
	assertMutationClientErrorSanitized(t, err, server.path)

	result := awaitMutationClientServer(t, server)
	if result.readAfterResponseN != 0 {
		t.Fatalf("UID mismatch wrote %d bytes before rejection, want 0", result.readAfterResponseN)
	}
	assertMutationClientSingleConnection(t, result)
}

func TestRoundTripMutationDialCancelAndDeadline(t *testing.T) {
	request := mutationClientTestRequests(t).remove

	t.Run("dial failure is stable and sanitized", func(t *testing.T) {
		const secret = "dial-secret-6f3df0"
		path := filepath.Join(t.TempDir(), secret+".sock")
		ctx, cancel := context.WithTimeout(context.Background(), mutationClientTestTimeout)
		defer cancel()

		response, err := roundTripMutationAt(ctx, path, uint32(os.Getuid()), request)
		if response != nil {
			t.Fatalf("roundTripMutationAt() response = %#v, want nil", response)
		}
		assertMutationClientErrorCode(t, err, MutationClientErrorCodeDialFailed)
		assertMutationClientErrorSanitized(t, err, secret, path)
	})

	t.Run("pre-canceled context preserves identity", func(t *testing.T) {
		const secret = "cancel-secret-c72c19"
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		response, err := roundTripMutationAt(ctx, secret, uint32(os.Getuid()), request)
		if response != nil {
			t.Fatalf("roundTripMutationAt() response = %#v, want nil", response)
		}
		assertMutationClientErrorCode(t, err, MutationClientErrorCodeContextCanceled)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("roundTripMutationAt() error = %v, want errors.Is(context.Canceled)", err)
		}
		assertMutationClientErrorSanitized(t, err, secret)
	})

	t.Run("cancellation after dial writes zero bytes", func(t *testing.T) {
		server := startMutationClientPeerObserver(t)
		baseContext, cancel := context.WithCancel(context.Background())
		ctx := &mutationClientCancelAfterDialContext{
			Context: baseContext,
			cancel:  cancel,
		}
		defer cancel()

		response, err := roundTripMutationAt(ctx, server.path, uint32(os.Getuid()), request)
		if response != nil {
			t.Fatalf("roundTripMutationAt() response = %#v, want nil", response)
		}
		assertMutationClientErrorCode(t, err, MutationClientErrorCodeContextCanceled)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("roundTripMutationAt() error = %v, want errors.Is(context.Canceled)", err)
		}
		result := awaitMutationClientServer(t, server)
		if result.readAfterResponseN != 0 {
			t.Fatalf("post-dial cancellation wrote %d bytes, want 0", result.readAfterResponseN)
		}
		assertMutationClientSingleConnection(t, result)
	})

	t.Run("cancellation interrupts response read", func(t *testing.T) {
		server := startMutationClientServer(t, func(*net.UnixConn, MutationRequest) error { return nil })
		ctx, cancel := context.WithCancel(context.Background())
		callDone := make(chan mutationClientCallResult, 1)
		go func() {
			response, err := roundTripMutationAt(ctx, server.path, uint32(os.Getuid()), request)
			callDone <- mutationClientCallResult{response: response, err: err}
		}()
		awaitMutationClientRequestSeen(t, server)
		cancel()

		call := awaitMutationClientCall(t, callDone)
		if call.response != nil {
			t.Fatalf("roundTripMutationAt() response = %#v, want nil", call.response)
		}
		assertMutationClientErrorCode(t, call.err, MutationClientErrorCodeContextCanceled)
		if !errors.Is(call.err, context.Canceled) {
			t.Fatalf("roundTripMutationAt() error = %v, want errors.Is(context.Canceled)", call.err)
		}
		assertMutationClientSingleConnection(t, awaitMutationClientServer(t, server))
	})

	t.Run("deadline interrupts response read", func(t *testing.T) {
		server := startMutationClientServer(t, func(*net.UnixConn, MutationRequest) error { return nil })
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		response, err := roundTripMutationAt(ctx, server.path, uint32(os.Getuid()), request)
		if response != nil {
			t.Fatalf("roundTripMutationAt() response = %#v, want nil", response)
		}
		assertMutationClientErrorCode(t, err, MutationClientErrorCodeContextDeadlineExceeded)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("roundTripMutationAt() error = %v, want errors.Is(context.DeadlineExceeded)", err)
		}
		assertMutationClientSingleConnection(t, awaitMutationClientServer(t, server))
	})
}

func TestRoundTripMutationRejectsTruncatedOrInvalidResponses(t *testing.T) {
	const secret = "response-secret-d599b3"
	request := mutationClientTestRequests(t).target
	tests := []struct {
		name       string
		write      func(*net.UnixConn) error
		assertCode func(*testing.T, error)
	}{
		{
			name: "truncated length",
			write: func(connection *net.UnixConn) error {
				if _, err := connection.Write([]byte{0, 0}); err != nil {
					return err
				}
				return connection.CloseWrite()
			},
			assertCode: func(t *testing.T, err error) {
				assertFrameErrorCodeForClient(t, err, FrameErrorCodeTruncatedLength)
			},
		},
		{
			name: "truncated payload",
			write: func(connection *net.UnixConn) error {
				var header [4]byte
				binary.BigEndian.PutUint32(header[:], 8)
				if _, err := connection.Write(append(header[:], []byte("{}")...)); err != nil {
					return err
				}
				return connection.CloseWrite()
			},
			assertCode: func(t *testing.T, err error) {
				assertFrameErrorCodeForClient(t, err, FrameErrorCodeTruncatedPayload)
			},
		},
		{
			name: "invalid response",
			write: func(connection *net.UnixConn) error {
				return writeFramePayload(connection, []byte(`{"version":1,"operation":"`+secret+`"}`))
			},
			assertCode: func(t *testing.T, err error) {
				assertMutationResponseErrorCodeForClient(t, err, MutationResponseErrorCodeSchemaRejected)
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			server := startMutationClientServer(t, func(connection *net.UnixConn, _ MutationRequest) error {
				return test.write(connection)
			})
			ctx, cancel := context.WithTimeout(context.Background(), mutationClientTestTimeout)
			defer cancel()

			response, err := roundTripMutationAt(ctx, server.path, uint32(os.Getuid()), request)
			if response != nil {
				t.Fatalf("roundTripMutationAt() response = %#v, want nil", response)
			}
			test.assertCode(t, err)
			assertMutationClientErrorSanitized(t, err, secret, server.path)
			assertMutationClientSingleConnection(t, awaitMutationClientServer(t, server))
		})
	}
}

type mutationClientTestRequestSet struct {
	infrastructure MutationRequest
	policy         MutationRequest
	target         MutationRequest
	remove         MutationRequest
}

func mutationClientTestRequests(t *testing.T) mutationClientTestRequestSet {
	t.Helper()
	digest := strings.Repeat("a", 64)
	infrastructure, err := NewApplyInfrastructureRequest(digest, 11)
	if err != nil {
		t.Fatalf("NewApplyInfrastructureRequest(): %v", err)
	}
	policy, err := NewApplyPolicyRequest(
		digest,
		12,
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		[]netip.Prefix{
			netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
		},
	)
	if err != nil {
		t.Fatalf("NewApplyPolicyRequest(): %v", err)
	}
	target, err := NewApplyTargetRequest(
		digest,
		13,
		netip.MustParsePrefix("192.0.2.0/24"),
		MembershipPresent,
		TimeoutModeNative,
		1_900_000_000_000_000,
		true,
		[]Scope{ScopeInput, ScopeForward},
	)
	if err != nil {
		t.Fatalf("NewApplyTargetRequest(): %v", err)
	}
	return mutationClientTestRequestSet{
		infrastructure: infrastructure,
		policy:         policy,
		target:         target,
		remove:         NewRemoveManagedInfrastructureRequest(),
	}
}

func mustApplyMutationResponse(
	t *testing.T,
	construct func() (ApplyManagedPlanResponse, error),
) ApplyManagedPlanResponse {
	t.Helper()
	response, err := construct()
	if err != nil {
		t.Fatalf("construct ApplyManagedPlanResponse: %v", err)
	}
	return response
}

func assertMutationClientResponse(
	t *testing.T,
	response MutationResponse,
	wantOperation Operation,
	wantStatus MutationStatus,
	wantDomain Domain,
	wantCode MutationErrorCode,
	wantCodeSet bool,
	wantResponse string,
) {
	t.Helper()
	if response == nil {
		t.Fatal("response = nil")
	}
	if got := response.Operation(); got != wantOperation {
		t.Fatalf("response operation = %q, want %q", got, wantOperation)
	}
	if got := response.Status(); got != wantStatus {
		t.Fatalf("response status = %q, want %q", got, wantStatus)
	}
	if got, present := response.ErrorCode(); got != wantCode || present != wantCodeSet {
		t.Fatalf("response error code = (%q, %t), want (%q, %t)", got, present, wantCode, wantCodeSet)
	}
	switch wantResponse {
	case "apply":
		apply, ok := response.(ApplyManagedPlanResponse)
		if !ok {
			t.Fatalf("response type = %T, want ApplyManagedPlanResponse", response)
		}
		if got := apply.Domain(); got != wantDomain {
			t.Fatalf("response domain = %q, want %q", got, wantDomain)
		}
	case "remove":
		if _, ok := response.(RemoveManagedInfrastructureResponse); !ok {
			t.Fatalf("response type = %T, want RemoveManagedInfrastructureResponse", response)
		}
	default:
		t.Fatalf("unknown expected response type %q", wantResponse)
	}
}

func assertMutationClientErrorCode(t *testing.T, err error, want MutationClientErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatal("mutation client error = nil")
	}
	var clientError *MutationClientError
	if !errors.As(err, &clientError) {
		t.Fatalf("mutation client error type = %T, want *MutationClientError", err)
	}
	if got := clientError.Code(); got != want {
		t.Fatalf("mutation client error code = %q, want %q", got, want)
	}
}

func assertPeerErrorCode(t *testing.T, err error, want PeerErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatal("peer error = nil")
	}
	var peerError *PeerError
	if !errors.As(err, &peerError) {
		t.Fatalf("peer error type = %T, want *PeerError", err)
	}
	if got := peerError.Code(); got != want {
		t.Fatalf("peer error code = %q, want %q", got, want)
	}
}

func assertFrameErrorCodeForClient(t *testing.T, err error, want FrameErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatal("frame error = nil")
	}
	var frameError *FrameError
	if !errors.As(err, &frameError) {
		t.Fatalf("frame error type = %T, want *FrameError", err)
	}
	if got := frameError.Code(); got != want {
		t.Fatalf("frame error code = %q, want %q", got, want)
	}
}

func assertMutationResponseErrorCodeForClient(
	t *testing.T,
	err error,
	want MutationResponseErrorCode,
) {
	t.Helper()
	if err == nil {
		t.Fatal("mutation response error = nil")
	}
	var responseError *MutationResponseError
	if !errors.As(err, &responseError) {
		t.Fatalf("mutation response error type = %T, want *MutationResponseError", err)
	}
	if got := responseError.Code(); got != want {
		t.Fatalf("mutation response error code = %q, want %q", got, want)
	}
}

func assertMutationClientErrorSanitized(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil")
	}
	for _, marker := range forbidden {
		if marker != "" && strings.Contains(err.Error(), marker) {
			t.Fatalf("error leaked forbidden value %q: %q", marker, err)
		}
	}
}

type mutationClientServer struct {
	path        string
	requestSeen <-chan struct{}
	done        <-chan mutationClientServerResult
}

type mutationClientServerResult struct {
	requestPayload       []byte
	accepts              int
	readAfterResponseN   int
	readAfterResponseErr error
	secondAcceptErr      error
	err                  error
}

type mutationClientCallResult struct {
	response MutationResponse
	err      error
}

type mutationClientCancelOnDomainPlan struct {
	ManagedPlan
	cancel context.CancelFunc
}

func (p *mutationClientCancelOnDomainPlan) Domain() Domain {
	p.cancel()
	return p.ManagedPlan.Domain()
}

// mutationClientCancelAfterDialContext cancels specifically on the deadline
// lookup made while arming connection I/O. This avoids scheduling races while
// proving the synchronous post-arm context guard prevents the first write.
type mutationClientCancelAfterDialContext struct {
	context.Context
	cancel context.CancelFunc
}

func (c *mutationClientCancelAfterDialContext) Deadline() (time.Time, bool) {
	callers := make([]uintptr, 16)
	count := runtime.Callers(2, callers)
	frames := runtime.CallersFrames(callers[:count])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, ".armContextDeadline") {
			c.cancel()
			break
		}
		if !more {
			break
		}
	}
	return time.Now().Add(mutationClientTestTimeout), true
}

func startMutationClientServer(
	t *testing.T,
	handle func(*net.UnixConn, MutationRequest) error,
) mutationClientServer {
	t.Helper()
	listener, path := listenMutationClientUnix(t)
	requestSeen := make(chan struct{})
	done := make(chan mutationClientServerResult, 1)
	go func() {
		result := mutationClientServerResult{}
		connection, err := listener.AcceptUnix()
		if err != nil {
			result.err = err
			done <- result
			return
		}
		result.accepts++
		defer connection.Close()
		if err := connection.SetDeadline(time.Now().Add(mutationClientTestTimeout)); err != nil {
			result.err = err
			done <- result
			return
		}
		decoded, err := DecodeFrame(connection)
		if err != nil {
			result.err = err
			done <- result
			return
		}
		request, ok := decoded.(MutationRequest)
		if !ok {
			result.err = errors.New("server decoded a non-mutation request")
			done <- result
			return
		}
		result.requestPayload, err = EncodeMutationRequest(request)
		if err != nil {
			result.err = err
			done <- result
			return
		}
		close(requestSeen)
		if err := handle(connection, request); err != nil {
			result.err = err
			done <- result
			return
		}
		if err := connection.SetReadDeadline(time.Now().Add(mutationClientTestTimeout)); err != nil {
			result.err = err
			done <- result
			return
		}
		var trailing [1]byte
		result.readAfterResponseN, result.readAfterResponseErr = connection.Read(trailing[:])
		result.secondAcceptErr = observeMutationClientRetry(listener, &result.accepts)
		done <- result
	}()
	return mutationClientServer{path: path, requestSeen: requestSeen, done: done}
}

func startMutationClientPeerObserver(t *testing.T) mutationClientServer {
	t.Helper()
	listener, path := listenMutationClientUnix(t)
	done := make(chan mutationClientServerResult, 1)
	go func() {
		result := mutationClientServerResult{}
		connection, err := listener.AcceptUnix()
		if err != nil {
			result.err = err
			done <- result
			return
		}
		result.accepts++
		defer connection.Close()
		if err := connection.SetReadDeadline(time.Now().Add(mutationClientTestTimeout)); err != nil {
			result.err = err
			done <- result
			return
		}
		var contents [1]byte
		result.readAfterResponseN, result.readAfterResponseErr = connection.Read(contents[:])
		result.secondAcceptErr = observeMutationClientRetry(listener, &result.accepts)
		done <- result
	}()
	return mutationClientServer{path: path, done: done}
}

func listenMutationClientUnix(t *testing.T) (*net.UnixListener, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ipc.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("net.ListenUnix(): %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	return listener, path
}

func observeMutationClientRetry(listener *net.UnixListener, accepts *int) error {
	if err := listener.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		return err
	}
	connection, err := listener.AcceptUnix()
	if err == nil {
		*accepts++
		_ = connection.Close()
	}
	return err
}

func awaitMutationClientRequestSeen(t *testing.T, server mutationClientServer) {
	t.Helper()
	select {
	case <-server.requestSeen:
	case <-time.After(mutationClientTestTimeout):
		t.Fatal("timed out waiting for server to decode request")
	}
}

func awaitMutationClientCall(
	t *testing.T,
	done <-chan mutationClientCallResult,
) mutationClientCallResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(mutationClientTestTimeout):
		t.Fatal("timed out waiting for mutation client call")
		return mutationClientCallResult{}
	}
}

func awaitMutationClientServer(t *testing.T, server mutationClientServer) mutationClientServerResult {
	t.Helper()
	select {
	case result := <-server.done:
		if result.err != nil {
			t.Fatalf("mutation test server: %v", result.err)
		}
		return result
	case <-time.After(mutationClientTestTimeout + time.Second):
		t.Fatal("timed out waiting for mutation test server")
		return mutationClientServerResult{}
	}
}

func assertMutationClientSingleConnection(t *testing.T, result mutationClientServerResult) {
	t.Helper()
	if result.accepts != 1 {
		t.Fatalf("server accepts = %d, want exactly 1", result.accepts)
	}
	if result.readAfterResponseN != 0 || !errors.Is(result.readAfterResponseErr, io.EOF) {
		t.Fatalf(
			"server read after client result = (%d, %v), want (0, EOF)",
			result.readAfterResponseN, result.readAfterResponseErr,
		)
	}
	var networkError net.Error
	if !errors.As(result.secondAcceptErr, &networkError) || !networkError.Timeout() {
		t.Fatalf("second accept error = %v, want timeout proving zero retry", result.secondAcceptErr)
	}
}
