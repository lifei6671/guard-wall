package reconcile

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

func TestExpirationRuntimePreparesBeforeDispatcherStartup(t *testing.T) {
	var prepared atomic.Bool
	provider := &runtimePlanProvider{
		reconcileKeys: func(context.Context) ([]ReconcileKey, error) {
			if !prepared.Load() {
				return nil, errors.New("dispatcher started before expiration preparation")
			}
			return nil, nil
		},
		started: make(chan struct{}),
	}
	scheduler := &runtimeSchedulerStub{
		prepare: func(context.Context) (time.Time, error) {
			prepared.Store(true)
			return time.Unix(1, 0), nil
		},
		run: func(ctx context.Context, startedAt time.Time) error {
			if !startedAt.Equal(time.Unix(1, 0)) {
				return errors.New("runtime did not preserve startup sweep time")
			}
			<-ctx.Done()
			return nil
		},
	}
	runtime := newTestExpirationRuntime(t, scheduler, provider)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("Dispatcher did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
}

func TestExpirationRuntimePrepareFailureDoesNotStartDispatcher(t *testing.T) {
	want := errors.New("prepare failed")
	provider := &runtimePlanProvider{started: make(chan struct{})}
	runtime := newTestExpirationRuntime(t, &runtimeSchedulerStub{
		prepare: func(context.Context) (time.Time, error) { return time.Time{}, want },
	}, provider)

	err := runtime.Run(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want prepare failure", err)
	}
	select {
	case <-provider.started:
		t.Fatal("Dispatcher started after preparation failed")
	default:
	}
}

func TestExpirationRuntimeRejectsMissingStartupSweepTime(t *testing.T) {
	provider := &runtimePlanProvider{started: make(chan struct{})}
	runtime := newTestExpirationRuntime(t, &runtimeSchedulerStub{
		prepare: func(context.Context) (time.Time, error) { return time.Time{}, nil },
	}, provider)

	err := runtime.Run(context.Background())
	if err == nil {
		t.Fatal("runtime accepted a missing startup sweep time")
	}
	select {
	case <-provider.started:
		t.Fatal("Dispatcher started without a startup sweep time")
	default:
	}
}

func TestExpirationRuntimeSchedulerFailureStopsDispatcher(t *testing.T) {
	want := errors.New("scheduler failed")
	provider := &runtimePlanProvider{started: make(chan struct{})}
	runtime := newTestExpirationRuntime(t, &runtimeSchedulerStub{
		prepare: func(context.Context) (time.Time, error) { return time.Unix(1, 0), nil },
		run:     func(context.Context, time.Time) error { return want },
	}, provider)

	err := runtime.Run(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want scheduler failure", err)
	}
}

func TestExpirationRuntimeTreatsSpontaneousComponentCancellationAsFailure(t *testing.T) {
	provider := &runtimePlanProvider{started: make(chan struct{})}
	runtime := newTestExpirationRuntime(t, &runtimeSchedulerStub{
		prepare: func(context.Context) (time.Time, error) { return time.Unix(1, 0), nil },
		run:     func(context.Context, time.Time) error { return context.Canceled },
	}, provider)

	err := runtime.Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runtime error = %v, want spontaneous component cancellation", err)
	}
}

func TestExpirationRuntimePreservesBothComponentFailures(t *testing.T) {
	schedulerFailure := errors.New("scheduler failed")
	dispatcherFailure := errors.New("dispatcher failed")
	provider := &runtimePlanProvider{
		started: make(chan struct{}),
		reconcileKeys: func(context.Context) ([]ReconcileKey, error) {
			return nil, dispatcherFailure
		},
	}
	runtime := newTestExpirationRuntime(t, &runtimeSchedulerStub{
		prepare: func(context.Context) (time.Time, error) { return time.Unix(1, 0), nil },
		run:     func(context.Context, time.Time) error { return schedulerFailure },
	}, provider)

	err := runtime.Run(context.Background())
	if !errors.Is(err, schedulerFailure) || !errors.Is(err, dispatcherFailure) {
		t.Fatalf("runtime error = %v, want both component failures", err)
	}
}

func TestExpirationRuntimeDoesNotHideFailureAfterUnexpectedStop(t *testing.T) {
	dispatcherFailure := errors.New("dispatcher failed")
	provider := &runtimePlanProvider{
		started: make(chan struct{}),
		reconcileKeys: func(context.Context) ([]ReconcileKey, error) {
			return nil, dispatcherFailure
		},
	}
	runtime := newTestExpirationRuntime(t, &runtimeSchedulerStub{
		prepare: func(context.Context) (time.Time, error) { return time.Unix(1, 0), nil },
		run:     func(context.Context, time.Time) error { return nil },
	}, provider)

	err := runtime.Run(context.Background())
	if !errors.Is(err, dispatcherFailure) {
		t.Fatalf("runtime error = %v, want Dispatcher failure", err)
	}
}

func newTestExpirationRuntime(
	t *testing.T,
	scheduler ExpirationRuntimeScheduler,
	provider PlanProvider,
) *ExpirationRuntime {
	t.Helper()
	controller := newTestController(t, fake.NewBackend(), &manualClock{now: time.Unix(1, 0)}, &memoryAudit{})
	dispatcher, err := NewDispatcher(controller, provider, 1)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewExpirationRuntime(scheduler, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

type runtimeSchedulerStub struct {
	prepare func(context.Context) (time.Time, error)
	run     func(context.Context, time.Time) error
}

func (s *runtimeSchedulerStub) PrepareExpirationStartup(ctx context.Context) (time.Time, error) {
	if s.prepare == nil {
		return time.Unix(1, 0), nil
	}
	return s.prepare(ctx)
}

func (s *runtimeSchedulerStub) RunExpirationSchedulerAfterStartup(ctx context.Context, startedAt time.Time) error {
	if s.run == nil {
		<-ctx.Done()
		return nil
	}
	return s.run(ctx, startedAt)
}

type runtimePlanProvider struct {
	reconcileKeys func(context.Context) ([]ReconcileKey, error)
	started       chan struct{}
	startedOnce   atomic.Bool
}

func (p *runtimePlanProvider) ReconcileKeys(ctx context.Context) ([]ReconcileKey, error) {
	if p.startedOnce.CompareAndSwap(false, true) {
		close(p.started)
	}
	if p.reconcileKeys == nil {
		return nil, nil
	}
	return p.reconcileKeys(ctx)
}

func (*runtimePlanProvider) CurrentPlan(
	context.Context,
	ReconcileKey,
) (fake.OperationPlan, bool, error) {
	return fake.OperationPlan{}, false, nil
}
