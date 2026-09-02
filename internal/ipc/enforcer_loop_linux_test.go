//go:build linux

package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

const enforcerLoopTestTimeout = 3 * time.Second

var _ func(*UnixListener, context.Context, uint32, EnforcerHandlers, EnforcerServeOptions) error = (*UnixListener).ServeEnforcer

func TestServeEnforcerRejectsInvalidConfigurationBeforeAccept(t *testing.T) {
	complete := completeEnforcerTestHandlers(t)
	tests := []struct {
		name     string
		ctx      context.Context
		handlers EnforcerHandlers
		options  EnforcerServeOptions
		wantCode EnforcerServerErrorCode
	}{
		{
			name:     "zero request timeout",
			ctx:      context.Background(),
			handlers: complete,
			options:  EnforcerServeOptions{OnRequestFailure: func(error) {}},
			wantCode: EnforcerServerErrorCodeTimeoutRequired,
		},
		{
			name:     "negative request timeout",
			ctx:      context.Background(),
			handlers: complete,
			options: EnforcerServeOptions{
				RequestTimeout:   -time.Nanosecond,
				OnRequestFailure: func(error) {},
			},
			wantCode: EnforcerServerErrorCodeTimeoutRequired,
		},
		{
			name:     "failure observer missing",
			ctx:      context.Background(),
			handlers: complete,
			options:  EnforcerServeOptions{RequestTimeout: time.Second},
			wantCode: EnforcerServerErrorCodeObserverRequired,
		},
		{
			name:     "context missing",
			ctx:      nil,
			handlers: complete,
			options: EnforcerServeOptions{
				RequestTimeout:   time.Second,
				OnRequestFailure: func(error) {},
			},
			wantCode: EnforcerServerErrorCodeContextRequired,
		},
		{
			name: "handler missing",
			ctx:  context.Background(),
			handlers: EnforcerHandlers{
				ProbeCapabilities: complete.ProbeCapabilities,
				SnapshotManaged:   complete.SnapshotManaged,
			},
			options: EnforcerServeOptions{
				RequestTimeout:   time.Second,
				OnRequestFailure: func(error) {},
			},
			wantCode: EnforcerServerErrorCodeHandlerRequired,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			listener, socketPath := newMutationServerTestListener(t)
			connection := dialMutationServer(t, socketPath)
			if err := WriteProbeCapabilitiesRequestFrame(connection, NewProbeCapabilitiesRequest()); err != nil {
				t.Fatalf("queue probe request: %v", err)
			}

			assertEnforcerServerCode(
				t,
				listener.ServeEnforcer(test.ctx, uint32(os.Getuid()), test.handlers, test.options),
				test.wantCode,
			)

			serverDone := make(chan error, 1)
			go func() {
				serverDone <- listener.ServeEnforcerOnce(
					context.Background(), uint32(os.Getuid()), complete,
				)
			}()
			if _, err := DecodeProbeCapabilitiesResponseFrame(connection); err != nil {
				t.Fatalf("queued request was consumed before configuration rejection: %v", err)
			}
			_ = connection.Close()
			if err := awaitMutationServer(t, serverDone); err != nil {
				t.Fatalf("ServeEnforcerOnce() after rejection: %v", err)
			}
		})
	}

	var listener *UnixListener
	assertEnforcerServerCode(
		t,
		listener.ServeEnforcer(
			context.Background(), 0, complete,
			EnforcerServeOptions{RequestTimeout: time.Second, OnRequestFailure: func(error) {}},
		),
		EnforcerServerErrorCodeUnavailable,
	)
	assertEnforcerServerCode(
		t,
		(&UnixListener{}).ServeEnforcer(
			context.Background(), 0, complete,
			EnforcerServeOptions{RequestTimeout: time.Second, OnRequestFailure: func(error) {}},
		),
		EnforcerServerErrorCodeUnavailable,
	)
}

func TestServeEnforcerStartsRequestBudgetAfterAccept(t *testing.T) {
	listener, socketPath := newMutationServerTestListener(t)
	const requestTimeout = 120 * time.Millisecond
	response := mustProbeTransportSuccess(t, mustProbeTransportCapabilities(t))
	observerCalls := make(chan error, 4)
	handlers := completeEnforcerTestHandlers(t)
	handlers.ProbeCapabilities = func(context.Context) ProbeCapabilitiesResponse {
		time.Sleep(50 * time.Millisecond)
		return response
	}
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeEnforcer(
			ctx,
			uint32(os.Getuid()),
			handlers,
			EnforcerServeOptions{
				RequestTimeout: requestTimeout,
				OnRequestFailure: func(err error) {
					observerCalls <- err
				},
			},
		)
	}()

	time.Sleep(2*requestTimeout + 30*time.Millisecond)
	select {
	case err := <-observerCalls:
		t.Fatalf("idle listener consumed request budget: %T %v", err, err)
	default:
	}

	clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
	defer cancelClient()
	if _, err := roundTripProbeCapabilitiesAt(clientCtx, socketPath, uint32(os.Getuid())); err != nil {
		t.Fatalf("probe after idle period: %v", err)
	}
	select {
	case err := <-observerCalls:
		t.Fatalf("successful request notified failure observer: %T %v", err, err)
	default:
	}

	cancel()
	if err := awaitMutationServer(t, serverDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeEnforcer() stop error = %T %v, want context canceled", err, err)
	}
}

func TestServeEnforcerContinuesAfterSlowClientTimeout(t *testing.T) {
	listener, socketPath := newMutationServerTestListener(t)
	observerCalls := make(chan error, 4)
	handlers := completeEnforcerTestHandlers(t)
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeEnforcer(
			ctx,
			uint32(os.Getuid()),
			handlers,
			EnforcerServeOptions{
				RequestTimeout: 100 * time.Millisecond,
				OnRequestFailure: func(err error) {
					observerCalls <- err
				},
			},
		)
	}()

	slowConnection := dialMutationServer(t, socketPath)
	select {
	case err := <-observerCalls:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("slow client observer error = %T %v, want deadline", err, err)
		}
	case <-time.After(enforcerLoopTestTimeout):
		t.Fatal("slow client timeout was not observed")
	}
	assertEnforcerLoopConnectionTerminated(t, slowConnection)

	clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
	defer cancelClient()
	if _, err := roundTripProbeCapabilitiesAt(clientCtx, socketPath, uint32(os.Getuid())); err != nil {
		t.Fatalf("valid request after slow client: %v", err)
	}
	select {
	case err := <-observerCalls:
		t.Fatalf("failure observer calls exceeded one: %T %v", err, err)
	default:
	}

	cancel()
	if err := awaitMutationServer(t, serverDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeEnforcer() stop error = %T %v, want context canceled", err, err)
	}
}

func TestServeEnforcerObservesLocalFailureAfterConnectionClose(t *testing.T) {
	listener, socketPath := newMutationServerTestListener(t)
	observerCalls := make(chan enforcerLoopObserverResult, 2)
	connectionReady := make(chan struct{})
	var failedConnection *net.UnixConn
	handlers := completeEnforcerTestHandlers(t)
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeEnforcer(
			ctx,
			uint32(os.Getuid()),
			handlers,
			EnforcerServeOptions{
				RequestTimeout: time.Second,
				OnRequestFailure: func(err error) {
					<-connectionReady
					_ = failedConnection.SetReadDeadline(time.Now().Add(enforcerLoopTestTimeout))
					var one [1]byte
					read, readErr := failedConnection.Read(one[:])
					observerCalls <- enforcerLoopObserverResult{err: err, read: read, readErr: readErr}
				},
			},
		)
	}()

	failedConnection = dialMutationServer(t, socketPath)
	if _, err := failedConnection.Write([]byte{0, 0, 0, 1, '{'}); err != nil {
		t.Fatalf("write invalid frame: %v", err)
	}
	close(connectionReady)
	select {
	case result := <-observerCalls:
		var validation *ValidationError
		if !errors.As(result.err, &validation) || validation.Code() != ErrorCodeInvalidJSON {
			t.Fatalf("observer error = %T %v, want invalid JSON", result.err, result.err)
		}
		var networkError net.Error
		if result.read != 0 || result.readErr == nil ||
			(errors.As(result.readErr, &networkError) && networkError.Timeout()) {
			t.Fatalf("connection state in observer = (%d, %v), want already closed", result.read, result.readErr)
		}
	case <-time.After(enforcerLoopTestTimeout):
		t.Fatal("local failure observer was not called")
	}
	_ = failedConnection.Close()

	clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
	defer cancelClient()
	if _, err := roundTripProbeCapabilitiesAt(clientCtx, socketPath, uint32(os.Getuid())); err != nil {
		t.Fatalf("valid request after invalid frame: %v", err)
	}
	select {
	case result := <-observerCalls:
		t.Fatalf("failure observer calls exceeded one: %T %v", result.err, result.err)
	default:
	}

	cancel()
	if err := awaitMutationServer(t, serverDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeEnforcer() stop error = %T %v, want context canceled", err, err)
	}
}

func TestServeEnforcerStopsOnFatalRequestFailure(t *testing.T) {
	listener, socketPath := newMutationServerTestListener(t)
	requests := mutationClientTestRequests(t)
	wrongDomain := mustApplyMutationResponse(t, func() (ApplyManagedPlanResponse, error) {
		return NewApplyManagedPlanConfirmedResponse(DomainPolicy)
	})
	observerCalls := make(chan error, 1)
	handlers := completeEnforcerTestHandlers(t)
	handlers.Mutation = func(context.Context, MutationRequest) MutationResponse { return wrongDomain }
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeEnforcer(
			context.Background(),
			uint32(os.Getuid()),
			handlers,
			EnforcerServeOptions{
				RequestTimeout: time.Second,
				OnRequestFailure: func(err error) {
					observerCalls <- err
				},
			},
		)
	}()

	connection := dialMutationServer(t, socketPath)
	if err := WriteMutationRequestFrame(connection, requests.infrastructure); err != nil {
		t.Fatalf("write mutation request: %v", err)
	}
	assertMutationServerWroteNothing(t, connection)
	assertEnforcerServerCode(
		t, awaitMutationServer(t, serverDone), EnforcerServerErrorCodeResponseMismatch,
	)
	select {
	case err := <-observerCalls:
		t.Fatalf("fatal failure was reported as request-local: %T %v", err, err)
	default:
	}

	var restartedObserverCalls atomic.Int32
	restartedCtx, cancelRestarted := context.WithCancel(context.Background())
	restartedDone := make(chan error, 1)
	restartedHandlers := completeEnforcerTestHandlers(t)
	go func() {
		restartedDone <- listener.ServeEnforcer(
			restartedCtx,
			uint32(os.Getuid()),
			restartedHandlers,
			EnforcerServeOptions{
				RequestTimeout: time.Second,
				OnRequestFailure: func(error) {
					restartedObserverCalls.Add(1)
				},
			},
		)
	}()
	clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
	if _, err := roundTripProbeCapabilitiesAt(clientCtx, socketPath, uint32(os.Getuid())); err != nil {
		cancelClient()
		cancelRestarted()
		t.Fatalf("persistent loop restart after fatal failure: %v", err)
	}
	cancelClient()
	cancelRestarted()
	if err := awaitMutationServer(t, restartedDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("restarted ServeEnforcer() stop error = %T %v, want context canceled", err, err)
	}
	if got := restartedObserverCalls.Load(); got != 0 {
		t.Fatalf("restarted loop observer calls = %d, want zero", got)
	}
}

func TestServeEnforcerPanicUnwindsAttemptBeforeOwnerReuse(t *testing.T) {
	listener, socketPath := newMutationServerTestListener(t)
	panicValue := &struct{ label string }{label: "handler panic"}
	triggerPanic := make(chan struct{})
	releaseRecover := make(chan struct{})
	handlerStarted := make(chan struct{})
	recoverEntered := make(chan struct {
		value      any
		requestErr error
	}, 1)
	recoverDone := make(chan struct{})
	var capturedRequestCtx context.Context
	var observerCalls atomic.Int32
	panicHandlers := completeEnforcerTestHandlers(t)
	panicHandlers.ProbeCapabilities = func(ctx context.Context) ProbeCapabilitiesResponse {
		capturedRequestCtx = ctx
		close(handlerStarted)
		<-triggerPanic
		panic(panicValue)
	}
	go func() {
		defer func() {
			var requestErr error
			if capturedRequestCtx != nil {
				requestErr = capturedRequestCtx.Err()
			}
			value := recover()
			recoverEntered <- struct {
				value      any
				requestErr error
			}{value: value, requestErr: requestErr}
			<-releaseRecover
			close(recoverDone)
		}()
		_ = listener.ServeEnforcer(
			context.Background(),
			uint32(os.Getuid()),
			panicHandlers,
			EnforcerServeOptions{
				RequestTimeout: time.Hour,
				OnRequestFailure: func(error) {
					observerCalls.Add(1)
				},
			},
		)
	}()
	t.Cleanup(func() {
		select {
		case <-triggerPanic:
		default:
			close(triggerPanic)
		}
		select {
		case <-releaseRecover:
		default:
			close(releaseRecover)
		}
	})

	failedConnection := dialMutationServer(t, socketPath)
	if err := WriteProbeCapabilitiesRequestFrame(failedConnection, NewProbeCapabilitiesRequest()); err != nil {
		t.Fatalf("write panic probe request: %v", err)
	}
	select {
	case <-handlerStarted:
	case <-time.After(enforcerLoopTestTimeout):
		t.Fatal("panic handler did not start")
	}
	if err := capturedRequestCtx.Err(); err != nil {
		t.Fatalf("request context before panic = %v, want active", err)
	}
	close(triggerPanic)

	var recovered struct {
		value      any
		requestErr error
	}
	select {
	case recovered = <-recoverEntered:
	case <-time.After(enforcerLoopTestTimeout):
		t.Fatal("handler panic did not reach recovery boundary")
	}
	if recovered.value != panicValue {
		t.Fatalf("recovered panic = %#v, want injected value", recovered.value)
	}
	if !errors.Is(recovered.requestErr, context.Canceled) {
		t.Fatalf("request context at recovery = %v, want context canceled", recovered.requestErr)
	}
	if err := capturedRequestCtx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("request context after recovery = %v, want context canceled", err)
	}
	assertEnforcerLoopConnectionTerminated(t, failedConnection)
	if got := observerCalls.Load(); got != 0 {
		t.Fatalf("panic observer calls = %d, want zero", got)
	}

	restartedCtx, cancelRestarted := context.WithCancel(context.Background())
	restartedDone := make(chan error, 1)
	restartedHandlers := completeEnforcerTestHandlers(t)
	go func() {
		restartedDone <- listener.ServeEnforcer(
			restartedCtx,
			uint32(os.Getuid()),
			restartedHandlers,
			EnforcerServeOptions{
				RequestTimeout: time.Second,
				OnRequestFailure: func(error) {
					observerCalls.Add(1)
				},
			},
		)
	}()
	clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
	if _, err := roundTripProbeCapabilitiesAt(clientCtx, socketPath, uint32(os.Getuid())); err != nil {
		cancelClient()
		cancelRestarted()
		t.Fatalf("persistent loop after panic unwind: %v", err)
	}
	cancelClient()
	cancelRestarted()
	if err := awaitMutationServer(t, restartedDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("restarted ServeEnforcer() stop error = %T %v, want context canceled", err, err)
	}
	if got := observerCalls.Load(); got != 0 {
		t.Fatalf("observer calls after reuse = %d, want zero", got)
	}

	close(releaseRecover)
	select {
	case <-recoverDone:
	case <-time.After(enforcerLoopTestTimeout):
		t.Fatal("panic recovery goroutine did not finish")
	}
}

func TestServeEnforcerStopsOnParentContextAndPreservesListener(t *testing.T) {
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		want    error
	}{
		{
			name: "cancel",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			want: context.Canceled,
		},
		{
			name: "deadline",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 75*time.Millisecond)
			},
			want: context.DeadlineExceeded,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			listener, socketPath := newMutationServerTestListener(t)
			observerCalls := make(chan error, 1)
			handlers := completeEnforcerTestHandlers(t)
			ctx, cancel := test.context()
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- listener.ServeEnforcer(
					ctx,
					uint32(os.Getuid()),
					handlers,
					EnforcerServeOptions{
						RequestTimeout: time.Second,
						OnRequestFailure: func(err error) {
							observerCalls <- err
						},
					},
				)
			}()
			if test.want == context.Canceled {
				cancel()
			}
			if err := awaitMutationServer(t, serverDone); !errors.Is(err, test.want) {
				t.Fatalf("ServeEnforcer() error = %T %v, want %v", err, err, test.want)
			}
			cancel()
			select {
			case err := <-observerCalls:
				t.Fatalf("parent context stop notified request observer: %T %v", err, err)
			default:
			}
			assertEnforcerLoopListenerReusable(t, listener, socketPath, handlers)
		})
	}
}

func TestServeEnforcerRejectsConcurrentLoopBeforeAcceptAndReusesGate(t *testing.T) {
	listener, socketPath := newMutationServerTestListener(t)
	probeResponse := mustProbeTransportSuccess(t, mustProbeTransportCapabilities(t))
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var firstObserverCalls atomic.Int32
	firstHandlers := completeEnforcerTestHandlers(t)
	firstHandlers.ProbeCapabilities = func(context.Context) ProbeCapabilitiesResponse {
		close(firstStarted)
		<-releaseFirst
		return probeResponse
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- listener.ServeEnforcer(
			firstCtx,
			uint32(os.Getuid()),
			firstHandlers,
			EnforcerServeOptions{
				RequestTimeout: 2 * time.Second,
				OnRequestFailure: func(error) {
					firstObserverCalls.Add(1)
				},
			},
		)
	}()
	t.Cleanup(func() {
		cancelFirst()
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	})

	firstClientDone := make(chan error, 1)
	go func() {
		clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
		defer cancelClient()
		_, err := roundTripProbeCapabilitiesAt(clientCtx, socketPath, uint32(os.Getuid()))
		firstClientDone <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(enforcerLoopTestTimeout):
		t.Fatal("first ServeEnforcer loop did not become active")
	}

	var contenderHandlerCalls atomic.Int32
	var secondObserverCalls atomic.Int32
	secondSnapshotResponse := mustSnapshotTransportFailure(t, SnapshotManagedFailureCodeNotReady)
	secondHandlers := EnforcerHandlers{
		ProbeCapabilities: func(context.Context) ProbeCapabilitiesResponse {
			contenderHandlerCalls.Add(1)
			return probeResponse
		},
		SnapshotManaged: func(context.Context) SnapshotManagedResponse {
			contenderHandlerCalls.Add(1)
			return secondSnapshotResponse
		},
		Mutation: func(context.Context, MutationRequest) MutationResponse {
			contenderHandlerCalls.Add(1)
			return NewRemoveManagedInfrastructureConfirmedResponse()
		},
	}
	contenders := []struct {
		name string
		call func(context.Context) error
	}{
		{
			name: "ServeEnforcer",
			call: func(ctx context.Context) error {
				return listener.ServeEnforcer(
					ctx,
					uint32(os.Getuid()),
					secondHandlers,
					EnforcerServeOptions{
						RequestTimeout: time.Second,
						OnRequestFailure: func(error) {
							secondObserverCalls.Add(1)
						},
					},
				)
			},
		},
		{
			name: "ServeEnforcerOnce",
			call: func(ctx context.Context) error {
				return listener.ServeEnforcerOnce(ctx, uint32(os.Getuid()), secondHandlers)
			},
		},
		{
			name: "ServeProbeCapabilitiesOnce",
			call: func(ctx context.Context) error {
				return listener.ServeProbeCapabilitiesOnce(
					ctx, uint32(os.Getuid()), secondHandlers.ProbeCapabilities,
				)
			},
		},
		{
			name: "ServeSnapshotManagedOnce",
			call: func(ctx context.Context) error {
				return listener.ServeSnapshotManagedOnce(
					ctx, uint32(os.Getuid()), secondHandlers.SnapshotManaged,
				)
			},
		},
		{
			name: "ServeMutationOnce",
			call: func(ctx context.Context) error {
				return listener.ServeMutationOnce(ctx, uint32(os.Getuid()), secondHandlers.Mutation)
			},
		},
		{
			name: "AcceptRequest",
			call: func(ctx context.Context) error {
				connection, request, err := listener.AcceptRequest(ctx, uint32(os.Getuid()))
				if connection != nil {
					_ = connection.Close()
					return errors.New("AcceptRequest returned a connection while listener was owned")
				}
				if request != nil {
					return errors.New("AcceptRequest returned a request while listener was owned")
				}
				return err
			},
		},
	}
	for _, contender := range contenders {
		contender := contender
		t.Run(contender.name, func(t *testing.T) {
			contenderCtx, cancelContender := context.WithCancel(context.Background())
			contenderDone := make(chan error, 1)
			go func() {
				contenderDone <- contender.call(contenderCtx)
			}()
			select {
			case err := <-contenderDone:
				cancelContender()
				assertListenerError(t, err, ListenerErrorCodeAlreadyServing)
			case <-time.After(250 * time.Millisecond):
				cancelContender()
				select {
				case <-contenderDone:
				case <-time.After(enforcerLoopTestTimeout):
					t.Fatal("competing serve operation did not unblock after cancellation")
				}
				t.Fatalf("%s did not reject before Accept", contender.name)
			}
		})
	}
	if handlers, observer := contenderHandlerCalls.Load(), secondObserverCalls.Load(); handlers != 0 || observer != 0 {
		t.Fatalf("contender calls = handlers:%d observer:%d, want zero", handlers, observer)
	}

	close(releaseFirst)
	if err := awaitEnforcerLoopClient(t, firstClientDone); err != nil {
		t.Fatalf("first loop client: %v", err)
	}
	if got := firstObserverCalls.Load(); got != 0 {
		t.Fatalf("first loop observer calls = %d, want zero", got)
	}
	cancelFirst()
	if err := awaitMutationServer(t, firstDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("first ServeEnforcer() stop error = %T %v, want context canceled", err, err)
	}

	var thirdObserverCalls atomic.Int32
	thirdCtx, cancelThird := context.WithCancel(context.Background())
	thirdDone := make(chan error, 1)
	thirdHandlers := completeEnforcerTestHandlers(t)
	go func() {
		thirdDone <- listener.ServeEnforcer(
			thirdCtx,
			uint32(os.Getuid()),
			thirdHandlers,
			EnforcerServeOptions{
				RequestTimeout: time.Second,
				OnRequestFailure: func(error) {
					thirdObserverCalls.Add(1)
				},
			},
		)
	}()
	clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
	if _, err := roundTripProbeCapabilitiesAt(clientCtx, socketPath, uint32(os.Getuid())); err != nil {
		cancelClient()
		t.Fatalf("probe after gate release: %v", err)
	}
	cancelClient()
	cancelThird()
	if err := awaitMutationServer(t, thirdDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("third ServeEnforcer() stop error = %T %v, want context canceled", err, err)
	}
	if got := thirdObserverCalls.Load(); got != 0 {
		t.Fatalf("third loop observer calls = %d, want zero", got)
	}
}

func TestEnforcerLoopClosedFailureClassification(t *testing.T) {
	decodeTests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "UID mismatch", err: &PeerError{code: PeerErrorCodeUIDMismatch}, want: true},
		{name: "peer credentials unavailable", err: &PeerError{code: PeerErrorCodeCredentialUnavailable}},
		{name: "truncated length", err: &FrameError{code: FrameErrorCodeTruncatedLength}, want: true},
		{name: "oversized frame", err: &FrameError{code: FrameErrorCodeFrameTooLarge}, want: true},
		{name: "truncated payload", err: &FrameError{code: FrameErrorCodeTruncatedPayload}, want: true},
		{name: "write failure in decode position", err: &FrameError{code: FrameErrorCodeWriteFailed}},
		{name: "validation rejection", err: validationError(ErrorCodeSchemaRejected), want: true},
		{name: "unknown error", err: errors.New("unknown")},
	}
	for _, test := range decodeTests {
		test := test
		t.Run("decode "+test.name, func(t *testing.T) {
			if got := enforcerDecodeFailureIsRequestLocal(test.err); got != test.want {
				t.Fatalf("enforcerDecodeFailureIsRequestLocal(%T) = %t, want %t", test.err, got, test.want)
			}
		})
	}

	writeTests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "write failure", err: &FrameError{code: FrameErrorCodeWriteFailed}, want: true},
		{name: "frame size invariant", err: &FrameError{code: FrameErrorCodeFrameTooLarge}},
		{name: "response codec invariant", err: mutationResponseError(MutationResponseErrorCodeSchemaRejected)},
		{name: "unknown error", err: errors.New("unknown")},
	}
	for _, test := range writeTests {
		test := test
		t.Run("write "+test.name, func(t *testing.T) {
			if got := enforcerWriteFailureIsRequestLocal(test.err); got != test.want {
				t.Fatalf("enforcerWriteFailureIsRequestLocal(%T) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestServeEnforcerSerializesConnections(t *testing.T) {
	listener, socketPath := newMutationServerTestListener(t)
	probeResponse := mustProbeTransportSuccess(t, mustProbeTransportCapabilities(t))
	snapshot := mustSnapshotTransportSnapshot(t)
	snapshotResponse := mustSnapshotTransportSuccess(t, snapshot)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	handlers := completeEnforcerTestHandlers(t)
	handlers.ProbeCapabilities = func(context.Context) ProbeCapabilitiesResponse {
		recordEnforcerLoopInFlight(&inFlight, &maxInFlight, 1)
		defer inFlight.Add(-1)
		close(firstStarted)
		<-releaseFirst
		return probeResponse
	}
	handlers.SnapshotManaged = func(context.Context) SnapshotManagedResponse {
		recordEnforcerLoopInFlight(&inFlight, &maxInFlight, 1)
		defer inFlight.Add(-1)
		close(secondStarted)
		return snapshotResponse
	}
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeEnforcer(
			ctx,
			uint32(os.Getuid()),
			handlers,
			EnforcerServeOptions{RequestTimeout: 2 * time.Second, OnRequestFailure: func(error) {}},
		)
	}()

	firstDone := make(chan error, 1)
	go func() {
		clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
		defer cancelClient()
		_, err := roundTripProbeCapabilitiesAt(clientCtx, socketPath, uint32(os.Getuid()))
		firstDone <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(enforcerLoopTestTimeout):
		t.Fatal("first handler did not start")
	}

	secondDone := make(chan error, 1)
	go func() {
		clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
		defer cancelClient()
		got, err := roundTripSnapshotManagedAt(clientCtx, socketPath, uint32(os.Getuid()))
		if err == nil && got.Digest() != snapshot.Digest() {
			err = errors.New("snapshot digest mismatch")
		}
		secondDone <- err
	}()
	select {
	case <-secondStarted:
		t.Fatal("second handler started while first request was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := awaitEnforcerLoopClient(t, firstDone); err != nil {
		t.Fatalf("first client: %v", err)
	}
	if err := awaitEnforcerLoopClient(t, secondDone); err != nil {
		t.Fatalf("second client: %v", err)
	}
	if got := maxInFlight.Load(); got != 1 {
		t.Fatalf("maximum handlers in flight = %d, want 1", got)
	}

	cancel()
	if err := awaitMutationServer(t, serverDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeEnforcer() stop error = %T %v, want context canceled", err, err)
	}
}

type enforcerLoopObserverResult struct {
	err     error
	read    int
	readErr error
}

func assertEnforcerLoopConnectionTerminated(t *testing.T, connection *net.UnixConn) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(enforcerLoopTestTimeout))
	var one [1]byte
	read, readErr := connection.Read(one[:])
	_ = connection.Close()
	var networkError net.Error
	if read != 0 || readErr == nil || (errors.As(readErr, &networkError) && networkError.Timeout()) {
		t.Fatalf("connection termination = (%d, %v), want bounded close", read, readErr)
	}
}

func assertEnforcerLoopListenerReusable(
	t *testing.T,
	listener *UnixListener,
	socketPath string,
	handlers EnforcerHandlers,
) {
	t.Helper()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.ServeEnforcerOnce(
			context.Background(), uint32(os.Getuid()), handlers,
		)
	}()
	clientCtx, cancelClient := context.WithTimeout(context.Background(), enforcerLoopTestTimeout)
	defer cancelClient()
	if _, err := roundTripProbeCapabilitiesAt(clientCtx, socketPath, uint32(os.Getuid())); err != nil {
		t.Fatalf("listener reuse probe: %v", err)
	}
	if err := awaitMutationServer(t, serverDone); err != nil {
		t.Fatalf("ServeEnforcerOnce() after loop: %v", err)
	}
}

func recordEnforcerLoopInFlight(current, maximum *atomic.Int32, delta int32) {
	value := current.Add(delta)
	for {
		previous := maximum.Load()
		if value <= previous || maximum.CompareAndSwap(previous, value) {
			return
		}
	}
}

func awaitEnforcerLoopClient(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(enforcerLoopTestTimeout):
		t.Fatal("Enforcer loop client did not finish")
		return nil
	}
}
