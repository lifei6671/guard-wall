//go:build linux

package enforcer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

var _ func(MutationBackend, *ipc.UnixListener, uint32, ipc.EnforcerServeOptions) (*EnforcerRuntime, error) = NewEnforcerRuntime

func TestNewEnforcerRuntimeRejectsMissingListenerAndBackend(t *testing.T) {
	var listener *ipc.UnixListener
	runtime, err := NewEnforcerRuntime(runtimeTestBackend{}, listener, 17, runtimeTestOptions())
	if !errors.Is(err, ErrEnforcerListenerRequired) {
		t.Fatalf("missing listener error = %v, want ErrEnforcerListenerRequired", err)
	}
	if runtime != nil {
		t.Fatal("missing listener returned a runtime")
	}

	server := &runtimeTestListener{}
	runtime, err = newEnforcerRuntime(nil, server, 17, runtimeTestOptions())
	if !errors.Is(err, ErrMutationBackendRequired) {
		t.Fatalf("missing backend error = %v, want ErrMutationBackendRequired", err)
	}
	if runtime != nil {
		t.Fatal("missing backend returned a runtime")
	}
	if got := server.closeCalls(); got != 0 {
		t.Fatalf("failed construction close calls = %d, want 0", got)
	}
}

func TestEnforcerRuntimeRunsOneClosedHandlerSetThenCloses(t *testing.T) {
	observerErr := errors.New("request failure")
	observerCalls := 0
	options := runtimeTestOptions()
	options.OnRequestFailure = func(err error) {
		if !errors.Is(err, observerErr) {
			t.Errorf("observer error = %v, want %v", err, observerErr)
		}
		observerCalls++
	}
	server := &runtimeTestListener{invokeObserver: true, observerErr: observerErr}
	backend := runtimeTestBackend{}
	constructionCalls := 0
	runtime, err := newEnforcerRuntimeWithFactory(
		backend,
		server,
		73,
		options,
		func(backend MutationBackend) (ipc.EnforcerHandlers, error) {
			constructionCalls++
			return NewEnforcerHandlers(backend)
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := runtime.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	call := server.onlyServeCall(t)
	if call.uid != 73 {
		t.Fatalf("served UID = %d, want 73", call.uid)
	}
	if call.options.RequestTimeout != options.RequestTimeout || call.options.OnRequestFailure == nil {
		t.Fatal("Run() did not forward complete serve options")
	}
	if observerCalls != 1 {
		t.Fatalf("forwarded observer calls = %d, want 1", observerCalls)
	}
	if call.handlers.ProbeCapabilities == nil || call.handlers.SnapshotManaged == nil || call.handlers.Mutation == nil {
		t.Fatal("Run() did not forward one complete closed handler set")
	}
	if constructionCalls != 1 {
		t.Fatalf("handler constructions = %d, want 1", constructionCalls)
	}
	if got := server.events(); !sameRuntimeEvents(got, []string{"serve", "close"}) {
		t.Fatalf("lifecycle events = %v, want [serve close]", got)
	}
	if err := runtime.Run(context.Background()); !errors.Is(err, ErrEnforcerRuntimeStarted) {
		t.Fatalf("second Run() error = %v, want ErrEnforcerRuntimeStarted", err)
	}
}

func TestEnforcerRuntimePreservesTerminalAndCleanupErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		serveErr error
		closeErr error
	}{
		{name: "serve and close", serveErr: context.Canceled, closeErr: errors.New("close failed")},
		{name: "close only", closeErr: errors.New("close failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &runtimeTestListener{serveErr: test.serveErr, closeErr: test.closeErr}
			runtime, err := newEnforcerRuntime(runtimeTestBackend{}, server, 0, runtimeTestOptions())
			if err != nil {
				t.Fatal(err)
			}

			err = runtime.Run(context.Background())
			if (test.serveErr != nil && !errors.Is(err, test.serveErr)) || !errors.Is(err, test.closeErr) {
				t.Fatalf("Run() error = %v, want preserved terminal and cleanup identities", err)
			}
			if got := server.events(); !sameRuntimeEvents(got, []string{"serve", "close"}) {
				t.Fatalf("lifecycle events = %v, want [serve close]", got)
			}
		})
	}
}

func TestEnforcerRuntimeDoesNotCloseAnExternallyOwnedListener(t *testing.T) {
	conflict := runtimeListenerError{code: ipc.ListenerErrorCodeAlreadyServing}
	server := &runtimeTestListener{serveErr: conflict}
	constructionCalls := 0
	runtime, err := newEnforcerRuntimeWithFactory(
		runtimeTestBackend{},
		server,
		0,
		runtimeTestOptions(),
		func(backend MutationBackend) (ipc.EnforcerHandlers, error) {
			constructionCalls++
			return NewEnforcerHandlers(backend)
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := runtime.Run(context.Background()); !errors.Is(err, conflict) {
		t.Fatalf("conflicting Run() error = %v, want external ownership conflict", err)
	}
	if got := server.events(); !sameRuntimeEvents(got, []string{"serve"}) {
		t.Fatalf("conflicting lifecycle events = %v, want [serve]", got)
	}

	server.setServeErr(nil)
	if err := runtime.Run(context.Background()); err != nil {
		t.Fatalf("retry Run() error = %v", err)
	}
	if got := server.events(); !sameRuntimeEvents(got, []string{"serve", "serve", "close"}) {
		t.Fatalf("retry lifecycle events = %v, want [serve serve close]", got)
	}
	if constructionCalls != 1 {
		t.Fatalf("retry handler constructions = %d, want 1", constructionCalls)
	}
}

func TestEnforcerRuntimeConcurrentRunDoesNotCloseActiveLoop(t *testing.T) {
	server := &runtimeTestListener{started: make(chan struct{}), release: make(chan struct{})}
	runtime, err := newEnforcerRuntime(runtimeTestBackend{}, server, 0, runtimeTestOptions())
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- runtime.Run(context.Background()) }()
	select {
	case <-server.started:
	case <-time.After(time.Second):
		t.Fatal("first Run() did not reach ServeEnforcer")
	}
	if err := runtime.Run(context.Background()); !errors.Is(err, ErrEnforcerRuntimeStarted) {
		t.Fatalf("concurrent Run() error = %v, want ErrEnforcerRuntimeStarted", err)
	}
	if got := server.closeCalls(); got != 0 {
		t.Fatalf("concurrent Run() close calls = %d, want 0", got)
	}
	close(server.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first Run() did not complete")
	}
	if got := server.events(); !sameRuntimeEvents(got, []string{"serve", "close"}) {
		t.Fatalf("concurrent lifecycle events = %v, want [serve close]", got)
	}
}

func TestEnforcerRuntimePanicClosesListenerAndStops(t *testing.T) {
	panicValue := errors.New("serve panic")
	server := &runtimeTestListener{panicValue: panicValue}
	runtime, err := newEnforcerRuntime(runtimeTestBackend{}, server, 0, runtimeTestOptions())
	if err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			if got := recover(); got != panicValue {
				t.Fatalf("recovered panic = %#v, want %#v", got, panicValue)
			}
		}()
		_ = runtime.Run(context.Background())
	}()
	if got := server.events(); !sameRuntimeEvents(got, []string{"serve", "close"}) {
		t.Fatalf("panic lifecycle events = %v, want [serve close]", got)
	}
	if err := runtime.Run(context.Background()); !errors.Is(err, ErrEnforcerRuntimeStarted) {
		t.Fatalf("Run() after panic error = %v, want ErrEnforcerRuntimeStarted", err)
	}
}

func runtimeTestOptions() ipc.EnforcerServeOptions {
	return ipc.EnforcerServeOptions{
		RequestTimeout:   time.Second,
		OnRequestFailure: func(error) {},
	}
}

type runtimeTestBackend struct{}

func (runtimeTestBackend) Probe(context.Context) (firewall.FirewallCapabilities, error) {
	return firewall.FirewallCapabilities{}, nil
}

func (runtimeTestBackend) Snapshot(context.Context) (firewall.ManagedSnapshot, error) {
	return firewall.ManagedSnapshot{}, nil
}

func (runtimeTestBackend) Apply(context.Context, firewall.OperationPlan) firewall.MutationResult {
	return firewall.MutationResult{}
}

func (runtimeTestBackend) RemoveManagedInfrastructure(context.Context, firewall.RemovalAuthorization) firewall.MutationResult {
	return firewall.MutationResult{}
}

var _ MutationBackend = runtimeTestBackend{}

type runtimeTestListener struct {
	mu             sync.Mutex
	serveErr       error
	closeErr       error
	panicValue     any
	invokeObserver bool
	observerErr    error
	serveCalls     []runtimeServeCall
	eventLog       []string
	started        chan struct{}
	release        chan struct{}
}

type runtimeServeCall struct {
	uid      uint32
	handlers ipc.EnforcerHandlers
	options  ipc.EnforcerServeOptions
}

func (l *runtimeTestListener) ServeEnforcer(
	_ context.Context,
	uid uint32,
	handlers ipc.EnforcerHandlers,
	options ipc.EnforcerServeOptions,
) error {
	l.mu.Lock()
	l.eventLog = append(l.eventLog, "serve")
	l.serveCalls = append(l.serveCalls, runtimeServeCall{uid: uid, handlers: handlers, options: options})
	panicValue := l.panicValue
	serveErr := l.serveErr
	started := l.started
	release := l.release
	invokeObserver := l.invokeObserver
	observerErr := l.observerErr
	l.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	if invokeObserver {
		options.OnRequestFailure(observerErr)
	}
	if panicValue != nil {
		panic(panicValue)
	}
	return serveErr
}

func (l *runtimeTestListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.eventLog = append(l.eventLog, "close")
	return l.closeErr
}

func (l *runtimeTestListener) closeCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, event := range l.eventLog {
		if event == "close" {
			count++
		}
	}
	return count
}

func (l *runtimeTestListener) events() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.eventLog...)
}

func (l *runtimeTestListener) onlyServeCall(t *testing.T) runtimeServeCall {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.serveCalls) != 1 {
		t.Fatalf("serve calls = %d, want 1", len(l.serveCalls))
	}
	return l.serveCalls[0]
}

func (l *runtimeTestListener) setServeErr(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.serveErr = err
}

type runtimeListenerError struct {
	code ipc.ListenerErrorCode
}

func (e runtimeListenerError) Error() string { return string(e.code) }

func (e runtimeListenerError) Code() ipc.ListenerErrorCode { return e.code }

func sameRuntimeEvents(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
