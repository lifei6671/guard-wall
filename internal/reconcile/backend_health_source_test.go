package reconcile

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackendHealthSourceReportsOnlyUnavailableRecovery(t *testing.T) {
	prober := &healthSourceProbeStub{}
	observer := &healthSourceObserverStub{}
	source, err := newBackendHealthSource(prober, observer, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- source.Run(ctx) }()

	waitForCounter(t, &prober.calls, 3)
	if notifications := observer.calls.Load(); notifications != 0 {
		t.Fatalf("initial reachable probes notified Dispatcher %d times", notifications)
	}

	prober.unreachable.Store(true)
	waitForCounter(t, &prober.failures, 1)
	prober.unreachable.Store(false)
	waitForCounter(t, &observer.calls, 1)
	waitForCounter(t, &prober.calls, prober.calls.Load()+3)
	if notifications := observer.calls.Load(); notifications != 1 {
		t.Fatalf("repeated reachable probes notified Dispatcher %d times", notifications)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Backend health source did not stop after cancellation")
	}
}

func TestBackendHealthSourceCancellationInterruptsProbe(t *testing.T) {
	prober := &blockingHealthSourceProbe{enteredCh: make(chan struct{}), exitedCh: make(chan struct{})}
	source, err := newBackendHealthSource(prober, &healthSourceObserverStub{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- source.Run(ctx) }()
	select {
	case <-prober.enteredCh:
	case <-time.After(time.Second):
		t.Fatal("Backend health source did not start Probe")
	}
	cancel()
	select {
	case <-prober.exitedCh:
	case <-time.After(time.Second):
		t.Fatal("Backend health Probe did not receive cancellation")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Backend health source did not stop after cancellation")
	}
}

func TestBackendHealthSourceBoundsEachProbe(t *testing.T) {
	prober := &blockingHealthSourceProbe{enteredCh: make(chan struct{}), exitedCh: make(chan struct{})}
	source, err := newBackendHealthSource(prober, &healthSourceObserverStub{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	source.timeout = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- source.Run(ctx) }()
	select {
	case <-prober.enteredCh:
	case <-time.After(time.Second):
		t.Fatal("Backend health source did not start Probe")
	}
	select {
	case <-prober.exitedCh:
	case <-time.After(time.Second):
		t.Fatal("Backend health source Probe did not honor its deadline")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Backend health source did not stop after its bounded Probe")
	}
}

func TestBackendHealthSourceStopsWhenDispatcherStops(t *testing.T) {
	prober := &healthSourceProbeStub{}
	prober.unreachable.Store(true)
	observer := &healthSourceObserverStub{err: ErrDispatcherStopped}
	source, err := newBackendHealthSource(prober, observer, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- source.Run(ctx) }()
	waitForCounter(t, &prober.calls, 1)
	prober.unreachable.Store(false)
	select {
	case err := <-done:
		if !errors.Is(err, ErrDispatcherStopped) {
			t.Fatalf("Run() = %v, want dispatcher stopped", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Backend health source did not stop when Dispatcher stopped")
	}
}

func TestBackendHealthSourcePropagatesAuthoritativeRecoveryFailure(t *testing.T) {
	prober := &healthSourceProbeStub{}
	prober.unreachable.Store(true)
	fatal := errors.New("persist recovery observation")
	observer := &healthSourceObserverStub{err: fatal}
	source, err := newBackendHealthSource(prober, observer, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- source.Run(ctx) }()
	waitForCounter(t, &prober.failures, 1)
	prober.unreachable.Store(false)
	select {
	case err := <-done:
		if !errors.Is(err, fatal) {
			t.Fatalf("Run() = %v, want authoritative recovery failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Backend health source swallowed authoritative recovery failure")
	}
}

func TestBackendHealthSourceDefersRecoverableAuthoritativeUnavailability(t *testing.T) {
	prober := &healthSourceProbeStub{}
	prober.unreachable.Store(true)
	observer := &healthSourceObserverStub{err: &backendHealthUnavailableError{cause: errors.New("Backend unavailable")}}
	source, err := newBackendHealthSource(prober, observer, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- source.Run(ctx) }()
	waitForCounter(t, &prober.failures, 1)
	prober.unreachable.Store(false)
	waitForCounter(t, &observer.calls, 1)
	waitForCounter(t, &prober.calls, prober.calls.Load()+3)
	if notifications := observer.calls.Load(); notifications != 1 {
		t.Fatalf("recoverable unavailability notified Dispatcher %d times", notifications)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() after cancellation: %v", err)
	}
}

func TestNewBackendHealthSourceRejectsInvalidDependencies(t *testing.T) {
	for _, test := range []struct {
		name string
		make func() (BackendHealthProber, backendHealthRecoveryObserver, time.Duration)
	}{
		{name: "prober", make: func() (BackendHealthProber, backendHealthRecoveryObserver, time.Duration) {
			return nil, &healthSourceObserverStub{}, time.Second
		}},
		{name: "observer", make: func() (BackendHealthProber, backendHealthRecoveryObserver, time.Duration) {
			return &healthSourceProbeStub{}, nil, time.Second
		}},
		{name: "interval", make: func() (BackendHealthProber, backendHealthRecoveryObserver, time.Duration) {
			return &healthSourceProbeStub{}, &healthSourceObserverStub{}, 0
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prober, observer, interval := test.make()
			if _, err := newBackendHealthSource(prober, observer, interval); err == nil {
				t.Fatal("newBackendHealthSource accepted invalid dependencies")
			}
		})
	}
}

type healthSourceProbeStub struct {
	unreachable atomic.Bool
	calls       atomic.Uint64
	failures    atomic.Uint64
}

func (p *healthSourceProbeStub) ProbeHealth(context.Context) error {
	p.calls.Add(1)
	if p.unreachable.Load() {
		p.failures.Add(1)
		return errors.New("backend unavailable")
	}
	return nil
}

type blockingHealthSourceProbe struct {
	entered sync.Once
	exited  sync.Once

	enteredCh chan struct{}
	exitedCh  chan struct{}
}

func (p *blockingHealthSourceProbe) ProbeHealth(ctx context.Context) error {
	p.entered.Do(func() { close(p.enteredCh) })
	<-ctx.Done()
	p.exited.Do(func() { close(p.exitedCh) })
	return ctx.Err()
}

type healthSourceObserverStub struct {
	calls atomic.Uint64
	err   error
}

func (o *healthSourceObserverStub) BackendHealthy(context.Context) (int, error) {
	o.calls.Add(1)
	return 0, o.err
}
