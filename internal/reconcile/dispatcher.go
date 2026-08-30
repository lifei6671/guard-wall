package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

var (
	ErrDispatcherRunning = errors.New("reconcile dispatcher is already running")
	ErrDispatcherStopped = errors.New("reconcile dispatcher is stopped")
)

// ReconcileKey is the stable queue coalescing key for one failure domain.
// Revision, generation, epoch, and Plan are deliberately re-read after dequeue.
type ReconcileKey struct {
	Domain fake.Domain
	Target netip.Prefix
}

// PlanProvider rebuilds the current Plan after a wakeup reaches the worker.
// ok=false means the key no longer has actionable Desired work.
type PlanProvider interface {
	// ReconcileKeys returns current startup recovery work. It may include a
	// hydrated Converged key whose physical state has not yet been confirmed,
	// but must omit ordinary Converged keys after startup observation.
	ReconcileKeys(context.Context) ([]ReconcileKey, error)
	CurrentPlan(context.Context, ReconcileKey) (plan fake.OperationPlan, ok bool, err error)
}

// Dispatcher owns the bounded keyed wakeup queue and the single retry scheduler.
// Backend health recovery uses BackendHealthy and is intentionally observation-only.
type Dispatcher struct {
	controller *Controller
	plans      PlanProvider
	clock      dispatcherClock
	queue      chan ReconcileKey
	done       chan struct{}

	queueMu sync.Mutex
	queued  map[ReconcileKey]*wakeReservation

	runMu   sync.Mutex
	started bool
	stopped atomic.Bool
}

// NewDispatcher constructs a single-worker dispatcher with bounded, cancelable backpressure.
func NewDispatcher(controller *Controller, plans PlanProvider, queueCapacity int) (*Dispatcher, error) {
	return newDispatcher(controller, plans, queueCapacity, systemDispatcherClock{})
}

func newDispatcher(controller *Controller, plans PlanProvider, queueCapacity int, clock dispatcherClock) (*Dispatcher, error) {
	if controller == nil {
		return nil, fmt.Errorf("controller is required")
	}
	if plans == nil {
		return nil, fmt.Errorf("plan provider is required")
	}
	if queueCapacity <= 0 {
		return nil, fmt.Errorf("queue capacity must be positive")
	}
	if clock == nil {
		return nil, fmt.Errorf("dispatcher clock is required")
	}
	return &Dispatcher{
		controller: controller,
		plans:      plans,
		clock:      clock,
		queue:      make(chan ReconcileKey, queueCapacity),
		done:       make(chan struct{}),
		queued:     make(map[ReconcileKey]*wakeReservation),
	}, nil
}

// Wake coalesces a key already waiting in the queue. A distinct key applies
// cancelable backpressure when the configured capacity is full.
func (d *Dispatcher) Wake(ctx context.Context, key ReconcileKey) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if err := validateReconcileKey(key); err != nil {
		return err
	}
	if d.stopped.Load() {
		return ErrDispatcherStopped
	}

	for {
		d.queueMu.Lock()
		if reservation, exists := d.queued[key]; exists {
			d.queueMu.Unlock()
			select {
			case <-reservation.done:
				if reservation.err == nil {
					return nil
				}
				continue
			case <-ctx.Done():
				return ctx.Err()
			case <-d.done:
				return ErrDispatcherStopped
			}
		}
		reservation := &wakeReservation{done: make(chan struct{})}
		d.queued[key] = reservation
		d.queueMu.Unlock()

		var err error
		select {
		case d.queue <- key:
		case <-ctx.Done():
			err = ctx.Err()
		case <-d.done:
			err = ErrDispatcherStopped
		}
		d.queueMu.Lock()
		if current := d.queued[key]; current == reservation && err != nil {
			delete(d.queued, key)
		}
		reservation.err = err
		close(reservation.done)
		d.queueMu.Unlock()
		return err
	}
}

type wakeReservation struct {
	done chan struct{}
	err  error
}

// BackendHealthy starts with one authoritative Probe. Matching physical state
// converges observation-only; unresolved keys are woken only after the Probe and
// continue under their existing absolute deadline, attempt count, and retry epoch.
func (d *Dispatcher) BackendHealthy(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, fmt.Errorf("context is required")
	}
	if d.stopped.Load() {
		return 0, ErrDispatcherStopped
	}
	resolved, unresolved, err := d.controller.probeRecovery(ctx)
	if err != nil {
		return resolved, err
	}
	for _, key := range unresolved {
		if err := d.Wake(ctx, key); err != nil {
			return resolved, err
		}
	}
	return resolved, nil
}

// Run processes wakeups with one worker and one absolute-deadline timer.
func (d *Dispatcher) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	d.runMu.Lock()
	if d.started {
		d.runMu.Unlock()
		return ErrDispatcherRunning
	}
	d.started = true
	d.runMu.Unlock()
	startupKeys, err := d.plans.ReconcileKeys(ctx)
	if err != nil {
		d.stopped.Store(true)
		close(d.done)
		return fmt.Errorf("load startup reconcile keys: %w", err)
	}
	uniqueStartup := make([]ReconcileKey, 0, len(startupKeys))
	seenStartup := make(map[ReconcileKey]struct{}, len(startupKeys))
	for _, key := range startupKeys {
		if err := validateReconcileKey(key); err != nil {
			d.stopped.Store(true)
			close(d.done)
			return fmt.Errorf("validate startup reconcile key: %w", err)
		}
		if _, exists := seenStartup[key]; exists {
			continue
		}
		seenStartup[key] = struct{}{}
		uniqueStartup = append(uniqueStartup, key)
	}
	if len(uniqueStartup) != 0 {
		uniqueStartup, err = d.controller.probeStartupRecovery(ctx, uniqueStartup)
		if err != nil {
			d.stopped.Store(true)
			close(d.done)
			return err
		}
	}
	startupWakes := make([]dispatchWake, 0, len(uniqueStartup))
	for _, key := range uniqueStartup {
		state, exists := d.retryState(key)
		startupWakes = append(startupWakes, dispatchWake{
			key:     key,
			startup: exists && state.Status == core.ReconcileConverged,
		})
	}
	startupCtx, cancelStartup := context.WithCancel(ctx)
	var startupWG sync.WaitGroup
	startupWake := make(chan dispatchWake)
	startupWG.Add(1)
	go func() {
		defer startupWG.Done()
		defer close(startupWake)
		for _, wake := range startupWakes {
			select {
			case startupWake <- wake:
			case <-startupCtx.Done():
				return
			case <-d.done:
				return
			}
		}
	}()
	defer func() {
		cancelStartup()
		d.stopped.Store(true)
		close(d.done)
		startupWG.Wait()
	}()

	deadlines := make(map[ReconcileKey]time.Time)
	ready := make([]dispatchWake, 0, 1)
	startupC := (<-chan dispatchWake)(startupWake)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if len(ready) != 0 {
			wake := ready[0]
			ready = ready[1:]
			rerun, err := d.dispatch(ctx, wake, deadlines)
			if err != nil {
				return err
			}
			if rerun {
				if wake.staleReruns != 0 {
					return fmt.Errorf("current Plan for %s remained stale after refresh", reconcileKeyName(wake.key))
				}
				ready = append(ready, dispatchWake{key: wake.key, staleReruns: 1, startup: wake.startup})
			}
			continue
		}

		timer := d.nextTimer(deadlines)
		var timerC <-chan time.Time
		if timer != nil {
			timerC = timer.C()
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return nil
		case wake, ok := <-startupC:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				startupC = nil
				continue
			}
			ready = append(ready, wake)
		case key := <-d.queue:
			if timer != nil {
				timer.Stop()
			}
			d.markDequeued(key)
			ready = append(ready, dispatchWake{key: key})
		case <-timerC:
			now := d.clock.Now()
			for key, deadline := range deadlines {
				if deadline.After(now) {
					continue
				}
				delete(deadlines, key)
				copyDeadline := deadline
				ready = append(ready, dispatchWake{key: key, deadline: &copyDeadline})
			}
		}
	}
}

type dispatchWake struct {
	key         ReconcileKey
	deadline    *time.Time
	staleReruns uint8
	startup     bool
}

func (d *Dispatcher) dispatch(ctx context.Context, wake dispatchWake, deadlines map[ReconcileKey]time.Time) (bool, error) {
	state, exists := d.retryState(wake.key)
	if wake.deadline != nil {
		if !exists || state.Status != core.ReconcileRetryWaiting || state.NextAttemptAt == nil || !state.NextAttemptAt.Equal(*wake.deadline) {
			return false, nil
		}
	} else if exists && !wake.startup {
		switch state.Status {
		case core.ReconcileConverged, core.ReconcileDegraded:
			delete(deadlines, wake.key)
			return false, nil
		case core.ReconcileRetryWaiting:
			if state.NextAttemptAt == nil {
				return false, fmt.Errorf("retry waiting key %s has no deadline", reconcileKeyName(wake.key))
			}
			if state.NextAttemptAt.After(d.clock.Now()) {
				deadlines[wake.key] = *state.NextAttemptAt
				return false, nil
			}
		}
	}

	plan, ok, err := d.plans.CurrentPlan(ctx, wake.key)
	if err != nil {
		return false, fmt.Errorf("load current Plan for %s: %w", reconcileKeyName(wake.key), err)
	}
	if !ok {
		delete(deadlines, wake.key)
		return false, nil
	}
	if plan.Domain != wake.key.Domain || plan.Target != wake.key.Target {
		return false, fmt.Errorf("current Plan does not match queued key %s", reconcileKeyName(wake.key))
	}

	_, executeErr := d.controller.Execute(ctx, plan)
	d.updateDeadline(wake.key, deadlines)
	if executeErr == nil {
		return false, nil
	}
	if errors.Is(executeErr, ErrStalePlan) || errors.Is(executeErr, ErrStaleCompletion) {
		return true, nil
	}
	if errors.Is(executeErr, ErrRetryNotReady) || errors.Is(executeErr, ErrBudgetExhausted) {
		return false, nil
	}
	if errors.Is(executeErr, ErrInvalidPlan) {
		return false, executeErr
	}
	state, exists = d.retryState(wake.key)
	if exists && (state.Status == core.ReconcileRetryWaiting || state.Status == core.ReconcileDegraded) && state.LastErrorCode != "" {
		return false, nil
	}
	return false, fmt.Errorf("execute %s: %w", reconcileKeyName(wake.key), executeErr)
}

func (d *Dispatcher) updateDeadline(key ReconcileKey, deadlines map[ReconcileKey]time.Time) {
	state, ok := d.retryState(key)
	if !ok || state.Status != core.ReconcileRetryWaiting || state.NextAttemptAt == nil || !state.NextAttemptAt.After(d.clock.Now()) {
		delete(deadlines, key)
		return
	}
	deadlines[key] = *state.NextAttemptAt
}

func (d *Dispatcher) retryState(key ReconcileKey) (core.RetryState, bool) {
	switch key.Domain {
	case fake.DomainInfrastructure:
		_, state, ok := d.controller.InfrastructureState()
		return state, ok
	case fake.DomainPolicy:
		_, state, ok := d.controller.PolicyState()
		return state, ok
	case fake.DomainTarget:
		_, state, ok := d.controller.TargetState(key.Target)
		return state, ok
	default:
		return core.RetryState{}, false
	}
}

func (d *Dispatcher) nextTimer(deadlines map[ReconcileKey]time.Time) dispatcherTimer {
	var earliest time.Time
	for _, deadline := range deadlines {
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	if earliest.IsZero() {
		return nil
	}
	delay := earliest.Sub(d.clock.Now())
	if delay < 0 {
		delay = 0
	}
	return d.clock.NewTimer(delay)
}

func (d *Dispatcher) markDequeued(key ReconcileKey) {
	d.queueMu.Lock()
	if reservation := d.queued[key]; reservation != nil {
		delete(d.queued, key)
	}
	d.queueMu.Unlock()
}

func validateReconcileKey(key ReconcileKey) error {
	switch key.Domain {
	case fake.DomainInfrastructure, fake.DomainPolicy:
		if key.Target.IsValid() {
			return fmt.Errorf("%s key must not include a target", reconcileKeyName(key))
		}
	case fake.DomainTarget:
		if !key.Target.IsValid() || key.Target != key.Target.Masked() {
			return fmt.Errorf("target reconcile key must contain one canonical target")
		}
	default:
		return fmt.Errorf("reconcile key has invalid domain %d", key.Domain)
	}
	return nil
}

func reconcileKeyName(key ReconcileKey) string {
	if key.Domain == fake.DomainTarget {
		return fmt.Sprintf("target/%s", key.Target)
	}
	return fmt.Sprintf("domain/%d", key.Domain)
}

type dispatcherClock interface {
	Now() time.Time
	NewTimer(time.Duration) dispatcherTimer
}

type dispatcherTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type systemDispatcherClock struct{}

func (systemDispatcherClock) Now() time.Time { return time.Now() }

func (systemDispatcherClock) NewTimer(delay time.Duration) dispatcherTimer {
	return systemDispatcherTimer{Timer: time.NewTimer(delay)}
}

type systemDispatcherTimer struct {
	*time.Timer
}

func (t systemDispatcherTimer) C() <-chan time.Time { return t.Timer.C }
