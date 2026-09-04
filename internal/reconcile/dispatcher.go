package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	appclock "github.com/lifei6671/guard-wall/internal/clock"
	"github.com/lifei6671/guard-wall/internal/core"
)

var (
	ErrDispatcherRunning = errors.New("reconcile dispatcher is already running")
	ErrDispatcherStopped = errors.New("reconcile dispatcher is stopped")
)

const backendHealthProbeTimeout = 5 * time.Second

var backendHealthProbeBackoff = [...]time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
	5 * time.Minute,
	15 * time.Minute,
}

// BackendHealthState is the process-local availability state of the physical Backend.
type BackendHealthState string

const (
	BackendHealthNotReady BackendHealthState = "not_ready"
	BackendHealthHealthy  BackendHealthState = "healthy"
	BackendHealthDegraded BackendHealthState = "degraded"
)

// BackendHealthStatus is the stable Health/Metric read model exposed by Dispatcher.
type BackendHealthStatus struct {
	State               BackendHealthState
	ConsecutiveFailures uint64
	TotalFailures       uint64
}

type backendHealthPolicy struct {
	probeTimeout time.Duration
	backoff      []time.Duration
}

type startupRecoveryGate uint8

const (
	startupRecoveryImmediate startupRecoveryGate = iota
	startupRecoveryHealthyEvent
	startupRecoveryDue
)

// ReconcileKey is the stable queue coalescing key for one failure domain.
// Revision, generation, epoch, and Plan are deliberately re-read after dequeue.
type ReconcileKey struct {
	Domain Domain
	Target netip.Prefix
}

// PlanProvider rebuilds the current Plan after a wakeup reaches the worker.
// ok=false means the key no longer has actionable Desired work.
type PlanProvider interface {
	// ReconcileKeys returns every current Desired key plus durable startup
	// recovery work. Dispatcher probes them as one authoritative snapshot before
	// deciding which domains require mutation.
	ReconcileKeys(context.Context) ([]ReconcileKey, error)
	CurrentPlan(context.Context, ReconcileKey) (plan OperationPlan, ok bool, err error)
}

// Dispatcher owns the bounded keyed wakeup queue and the single retry scheduler.
// Backend health recovery uses BackendHealthy and is intentionally observation-only.
type Dispatcher struct {
	controller    *Controller
	plans         PlanProvider
	clock         appclock.Clock
	healthPolicy  backendHealthPolicy
	queue         chan ReconcileKey
	done          chan struct{}
	healthChanged chan struct{}
	startupReady  chan struct{}

	queueMu           sync.Mutex
	queued            map[ReconcileKey]*wakeReservation
	healthOperationMu sync.Mutex
	healthMu          sync.Mutex
	health            BackendHealthStatus
	nextHealthProbeAt time.Time

	runMu            sync.Mutex
	startupReadyOnce sync.Once
	started          bool
	stopped          atomic.Bool
}

// NewDispatcher constructs a single-worker dispatcher with bounded, cancelable backpressure.
func NewDispatcher(controller *Controller, plans PlanProvider, queueCapacity int) (*Dispatcher, error) {
	return NewDispatcherWithClock(controller, plans, queueCapacity, appclock.NewWallClock())
}

// NewDispatcherWithClock constructs a single-worker dispatcher using
// dispatcherClock for retry deadlines and timers.
func NewDispatcherWithClock(
	controller *Controller,
	plans PlanProvider,
	queueCapacity int,
	dispatcherClock appclock.Clock,
) (*Dispatcher, error) {
	return newDispatcher(controller, plans, queueCapacity, dispatcherClock, defaultBackendHealthPolicy())
}

func newDispatcher(
	controller *Controller,
	plans PlanProvider,
	queueCapacity int,
	dispatcherClock appclock.Clock,
	healthPolicy backendHealthPolicy,
) (*Dispatcher, error) {
	if controller == nil {
		return nil, fmt.Errorf("controller is required")
	}
	if plans == nil {
		return nil, fmt.Errorf("plan provider is required")
	}
	if queueCapacity <= 0 {
		return nil, fmt.Errorf("queue capacity must be positive")
	}
	if dispatcherClock == nil {
		return nil, fmt.Errorf("dispatcher clock is required")
	}
	preparedPolicy, err := prepareBackendHealthPolicy(healthPolicy)
	if err != nil {
		return nil, err
	}
	return &Dispatcher{
		controller:    controller,
		plans:         plans,
		clock:         dispatcherClock,
		healthPolicy:  preparedPolicy,
		queue:         make(chan ReconcileKey, queueCapacity),
		done:          make(chan struct{}),
		healthChanged: make(chan struct{}, 1),
		startupReady:  make(chan struct{}),
		queued:        make(map[ReconcileKey]*wakeReservation),
		health:        BackendHealthStatus{State: BackendHealthNotReady},
	}, nil
}

func defaultBackendHealthPolicy() backendHealthPolicy {
	return backendHealthPolicy{
		probeTimeout: backendHealthProbeTimeout,
		backoff:      backendHealthProbeBackoff[:],
	}
}

func prepareBackendHealthPolicy(policy backendHealthPolicy) (backendHealthPolicy, error) {
	if policy.probeTimeout <= 0 {
		return backendHealthPolicy{}, fmt.Errorf("Backend health Probe timeout must be positive")
	}
	if len(policy.backoff) == 0 {
		return backendHealthPolicy{}, fmt.Errorf("Backend health Probe backoff is required")
	}
	prepared := backendHealthPolicy{probeTimeout: policy.probeTimeout, backoff: append([]time.Duration(nil), policy.backoff...)}
	for index, delay := range prepared.backoff {
		if delay <= 0 {
			return backendHealthPolicy{}, fmt.Errorf("Backend health Probe backoff %d must be positive", index)
		}
		if index != 0 && delay < prepared.backoff[index-1] {
			return backendHealthPolicy{}, fmt.Errorf("Backend health Probe backoff must be nondecreasing")
		}
	}
	return prepared, nil
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

// BackendHealthStatus returns a consistent process-local Health/Metric snapshot.
func (d *Dispatcher) BackendHealthStatus() BackendHealthStatus {
	if d == nil {
		return BackendHealthStatus{State: BackendHealthNotReady}
	}
	d.healthMu.Lock()
	defer d.healthMu.Unlock()
	return d.health
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
	d.runMu.Lock()
	started := d.started
	d.runMu.Unlock()
	if started {
		select {
		case <-d.startupReady:
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-d.done:
			return 0, ErrDispatcherStopped
		}
		if d.stopped.Load() {
			return 0, ErrDispatcherStopped
		}
	}
	d.healthOperationMu.Lock()
	outcome, err := d.controller.probeRecovery(ctx, d.healthPolicy.probeTimeout)
	if ctx.Err() != nil {
		d.healthOperationMu.Unlock()
		return outcome.resolved, ctx.Err()
	}
	if err != nil {
		d.healthOperationMu.Unlock()
		return outcome.resolved, err
	}
	if outcome.backendErr != nil {
		d.recordBackendUnavailable(true)
		d.healthOperationMu.Unlock()
		return outcome.resolved, outcome.backendErr
	}
	d.recordBackendHealthy(true)
	d.healthOperationMu.Unlock()
	for _, key := range outcome.unresolved {
		if err := d.Wake(ctx, key); err != nil {
			return outcome.resolved, err
		}
	}
	return outcome.resolved, nil
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
	defer func() {
		d.stopped.Store(true)
		d.closeStartupReady()
		close(d.done)
	}()

	startupKeys, err := d.plans.ReconcileKeys(ctx)
	if err != nil {
		return fmt.Errorf("load startup reconcile keys: %w", err)
	}
	uniqueStartup := make([]ReconcileKey, 0, len(startupKeys))
	seenStartup := make(map[ReconcileKey]struct{}, len(startupKeys))
	for _, key := range startupKeys {
		if err := validateReconcileKey(key); err != nil {
			return fmt.Errorf("validate startup reconcile key: %w", err)
		}
		if _, exists := seenStartup[key]; exists {
			continue
		}
		seenStartup[key] = struct{}{}
		uniqueStartup = append(uniqueStartup, key)
	}

	deadlines := make(map[ReconcileKey]time.Time)
	ready := make([]dispatchWake, 0, len(uniqueStartup))
	startupPending := false
	if len(uniqueStartup) == 0 {
		// With no startup work, no authoritative Probe has occurred yet. Keep the
		// public health state NotReady until the first real Backend observation.
	} else {
		startupWakes, recovered, err := d.recoverStartup(ctx, uniqueStartup, startupRecoveryImmediate)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		startupPending = !recovered
		ready = append(ready, startupWakes...)
	}
	d.closeStartupReady()

	for {
		if ctx.Err() != nil {
			return nil
		}
		healthDegraded := d.backendHealthState() == BackendHealthDegraded
		if !startupPending && !healthDegraded && len(ready) != 0 {
			d.healthOperationMu.Lock()
			if d.backendHealthState() == BackendHealthDegraded {
				d.healthOperationMu.Unlock()
				continue
			}
			wake := ready[0]
			ready = ready[1:]
			result, err := d.dispatch(ctx, wake, deadlines)
			if err != nil {
				d.healthOperationMu.Unlock()
				return err
			}
			if result.backendUnavailable {
				d.recordBackendUnavailable(false)
			}
			d.healthOperationMu.Unlock()
			if result.rerun {
				if wake.staleReruns != 0 {
					return fmt.Errorf("current Plan for %s remained stale after refresh", reconcileKeyName(wake.key))
				}
				ready = append(ready, dispatchWake{key: wake.key, staleReruns: 1, startup: wake.startup})
			}
			continue
		}

		var retryTimer appclock.Timer
		var retryTimerC <-chan time.Time
		var queueC <-chan ReconcileKey
		if !startupPending && !healthDegraded {
			retryTimer = d.nextTimer(deadlines)
			if retryTimer != nil {
				retryTimerC = retryTimer.C()
			}
			queueC = d.queue
		}
		healthTimer := d.nextHealthTimer()
		var healthTimerC <-chan time.Time
		if healthTimer != nil {
			healthTimerC = healthTimer.C()
		}
		select {
		case <-ctx.Done():
			stopDispatcherTimer(retryTimer)
			stopDispatcherTimer(healthTimer)
			return nil
		case <-d.healthChanged:
			stopDispatcherTimer(retryTimer)
			stopDispatcherTimer(healthTimer)
			if startupPending {
				startupWakes, recovered, err := d.recoverStartup(ctx, uniqueStartup, startupRecoveryHealthyEvent)
				if err != nil {
					return err
				}
				if ctx.Err() != nil {
					return nil
				}
				startupPending = !recovered
				ready = append(ready, startupWakes...)
			}
		case <-healthTimerC:
			stopDispatcherTimer(retryTimer)
			if startupPending {
				startupWakes, recovered, err := d.recoverStartup(ctx, uniqueStartup, startupRecoveryDue)
				if err != nil {
					return err
				}
				if ctx.Err() != nil {
					return nil
				}
				startupPending = !recovered
				ready = append(ready, startupWakes...)
				continue
			}
			healthWakes, err := d.recoverBackendHealth(ctx, true)
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return nil
			}
			ready = append(ready, healthWakes...)
		case key := <-queueC:
			stopDispatcherTimer(retryTimer)
			stopDispatcherTimer(healthTimer)
			d.markDequeued(key)
			ready = append(ready, dispatchWake{key: key})
		case <-retryTimerC:
			stopDispatcherTimer(healthTimer)
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

func (d *Dispatcher) recoverStartup(ctx context.Context, keys []ReconcileKey, gate startupRecoveryGate) ([]dispatchWake, bool, error) {
	d.healthOperationMu.Lock()
	defer d.healthOperationMu.Unlock()
	switch gate {
	case startupRecoveryImmediate:
	case startupRecoveryHealthyEvent:
		if d.backendHealthState() != BackendHealthHealthy {
			return nil, false, nil
		}
	case startupRecoveryDue:
		if !d.healthProbeDue() {
			return nil, false, nil
		}
	default:
		return nil, false, fmt.Errorf("invalid startup recovery gate %d", gate)
	}
	outcome, err := d.controller.probeStartupRecovery(ctx, keys, d.healthPolicy.probeTimeout)
	if ctx.Err() != nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if outcome.backendErr != nil {
		d.recordBackendUnavailable(false)
		return nil, false, nil
	}
	d.recordBackendHealthy(false)
	wakes := make([]dispatchWake, 0, len(outcome.unresolved))
	for _, key := range outcome.unresolved {
		state, exists := d.retryState(key)
		wakes = append(wakes, dispatchWake{
			key:     key,
			startup: exists && state.Status == core.ReconcileConverged,
		})
	}
	return wakes, true, nil
}

func (d *Dispatcher) recoverBackendHealth(ctx context.Context, requireDue bool) ([]dispatchWake, error) {
	d.healthOperationMu.Lock()
	defer d.healthOperationMu.Unlock()
	if requireDue && !d.healthProbeDue() {
		return nil, nil
	}
	outcome, err := d.controller.probeRecovery(ctx, d.healthPolicy.probeTimeout)
	if ctx.Err() != nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if outcome.backendErr != nil {
		d.recordBackendUnavailable(false)
		return nil, nil
	}
	d.recordBackendHealthy(false)
	wakes := make([]dispatchWake, 0, len(outcome.unresolved))
	for _, key := range outcome.unresolved {
		wakes = append(wakes, dispatchWake{key: key})
	}
	return wakes, nil
}

func (d *Dispatcher) recordBackendUnavailable(notify bool) {
	d.healthMu.Lock()
	d.health.State = BackendHealthDegraded
	d.health.ConsecutiveFailures++
	d.health.TotalFailures++
	index := d.health.ConsecutiveFailures - 1
	if index >= uint64(len(d.healthPolicy.backoff)) {
		index = uint64(len(d.healthPolicy.backoff) - 1)
	}
	d.nextHealthProbeAt = d.clock.Now().Add(d.healthPolicy.backoff[index])
	d.healthMu.Unlock()
	if notify {
		d.notifyHealthChanged()
	}
}

func (d *Dispatcher) recordBackendHealthy(notify bool) {
	d.healthMu.Lock()
	d.health.State = BackendHealthHealthy
	d.health.ConsecutiveFailures = 0
	d.nextHealthProbeAt = time.Time{}
	d.healthMu.Unlock()
	if notify {
		d.notifyHealthChanged()
	}
}

func (d *Dispatcher) notifyHealthChanged() {
	select {
	case d.healthChanged <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) closeStartupReady() {
	d.startupReadyOnce.Do(func() { close(d.startupReady) })
}

func (d *Dispatcher) nextHealthTimer() appclock.Timer {
	d.healthMu.Lock()
	state := d.health.State
	nextProbeAt := d.nextHealthProbeAt
	d.healthMu.Unlock()
	if state != BackendHealthDegraded || nextProbeAt.IsZero() {
		return nil
	}
	delay := nextProbeAt.Sub(d.clock.Now())
	if delay < 0 {
		delay = 0
	}
	return d.clock.NewTimer(delay)
}

func (d *Dispatcher) backendHealthState() BackendHealthState {
	d.healthMu.Lock()
	defer d.healthMu.Unlock()
	return d.health.State
}

func (d *Dispatcher) healthProbeDue() bool {
	d.healthMu.Lock()
	state := d.health.State
	nextProbeAt := d.nextHealthProbeAt
	d.healthMu.Unlock()
	return state == BackendHealthDegraded && !nextProbeAt.IsZero() && !nextProbeAt.After(d.clock.Now())
}

func stopDispatcherTimer(timer appclock.Timer) {
	if timer != nil {
		timer.Stop()
	}
}

type dispatchWake struct {
	key         ReconcileKey
	deadline    *time.Time
	staleReruns uint8
	startup     bool
}

type dispatchResult struct {
	rerun              bool
	backendUnavailable bool
}

func (d *Dispatcher) dispatch(ctx context.Context, wake dispatchWake, deadlines map[ReconcileKey]time.Time) (dispatchResult, error) {
	plan, ok, err := d.plans.CurrentPlan(ctx, wake.key)
	if err != nil {
		return dispatchResult{}, fmt.Errorf("load current Plan for %s: %w", reconcileKeyName(wake.key), err)
	}
	if !ok {
		delete(deadlines, wake.key)
		return dispatchResult{}, nil
	}
	if plan.Domain != wake.key.Domain || plan.Target != wake.key.Target {
		return dispatchResult{}, fmt.Errorf("current Plan does not match queued key %s", reconcileKeyName(wake.key))
	}

	// CurrentPlan also publishes the fresh authoritative Desired snapshot. Read
	// retry state only after that publication so a wake for a new Target
	// generation cannot be discarded because the previous generation converged.
	state, exists := d.retryState(wake.key)
	if wake.deadline != nil {
		if !exists || state.Status != core.ReconcileRetryWaiting || state.NextAttemptAt == nil || !state.NextAttemptAt.Equal(*wake.deadline) {
			return dispatchResult{}, nil
		}
	} else if exists && !wake.startup {
		switch state.Status {
		case core.ReconcileConverged, core.ReconcileDegraded:
			delete(deadlines, wake.key)
			return dispatchResult{}, nil
		case core.ReconcileRetryWaiting:
			if state.NextAttemptAt == nil {
				return dispatchResult{}, fmt.Errorf("retry waiting key %s has no deadline", reconcileKeyName(wake.key))
			}
			if state.NextAttemptAt.After(d.clock.Now()) {
				deadlines[wake.key] = *state.NextAttemptAt
				return dispatchResult{}, nil
			}
		}
	}

	execution, executeErr := d.controller.Execute(ctx, plan)
	d.updateDeadline(wake.key, deadlines)
	if executeErr == nil {
		return dispatchResult{backendUnavailable: execution.Apply.Kind == ResultUnknown}, nil
	}
	if errors.Is(executeErr, errTargetIntentExpired) {
		return dispatchResult{}, nil
	}
	if errors.Is(executeErr, ErrStalePlan) || errors.Is(executeErr, ErrStaleCompletion) {
		return dispatchResult{rerun: true}, nil
	}
	if errors.Is(executeErr, ErrRetryNotReady) || errors.Is(executeErr, ErrBudgetExhausted) {
		return dispatchResult{}, nil
	}
	if errors.Is(executeErr, ErrInvalidPlan) {
		return dispatchResult{}, executeErr
	}
	var backendErr *backendOperationError
	if errors.As(executeErr, &backendErr) {
		return dispatchResult{backendUnavailable: true}, nil
	}
	return dispatchResult{}, fmt.Errorf("execute %s: %w", reconcileKeyName(wake.key), executeErr)
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
	case DomainInfrastructure:
		_, state, ok := d.controller.InfrastructureState()
		return state, ok
	case DomainPolicy:
		_, state, ok := d.controller.PolicyState()
		return state, ok
	case DomainTarget:
		_, state, ok := d.controller.TargetState(key.Target)
		return state, ok
	default:
		return core.RetryState{}, false
	}
}

func (d *Dispatcher) nextTimer(deadlines map[ReconcileKey]time.Time) appclock.Timer {
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
	case DomainInfrastructure, DomainPolicy:
		if key.Target.IsValid() {
			return fmt.Errorf("%s key must not include a target", reconcileKeyName(key))
		}
	case DomainTarget:
		if !key.Target.IsValid() || key.Target != key.Target.Masked() {
			return fmt.Errorf("target reconcile key must contain one canonical target")
		}
	default:
		return fmt.Errorf("reconcile key has invalid domain %d", key.Domain)
	}
	return nil
}

func reconcileKeyName(key ReconcileKey) string {
	if key.Domain == DomainTarget {
		return fmt.Sprintf("target/%s", key.Target)
	}
	return fmt.Sprintf("domain/%d", key.Domain)
}
