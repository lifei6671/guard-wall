package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lifei6671/guard-wall/internal/clock"
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
)

var (
	// ErrReconcileRuntimeRunning reports a concurrent attempt to own the same runtime.
	ErrReconcileRuntimeRunning = errors.New("reconcile runtime is already running")
	// ErrReconcileRuntimeStopped reports an attempt to reuse a runtime after Store closure.
	ErrReconcileRuntimeStopped = errors.New("reconcile runtime is stopped")
)

// RuntimeStore is the complete SQLite boundary owned by one ReconcileRuntime.
// Run closes it only after the expiration scheduler and Dispatcher have returned.
type RuntimeStore interface {
	PersistentStateStore
	DesiredStateReader
	decision.TransactionRunner
	decision.PolicyTransactionRunner
	decision.PolicyStateReader
	Close() error
}

// RuntimeDependencies contains every authority required to compose Reconcile.
// Static and TargetPolicies are injected because this runtime must not infer
// Desired infrastructure or target-policy inputs from Firewall observations.
type RuntimeDependencies struct {
	NodeID         core.NodeID
	Backend        Backend
	Store          RuntimeStore
	Audit          CriticalAuditWriter
	Clock          clock.Clock
	Static         StaticDesiredFirewallState
	TargetPolicies decision.TargetPolicyResolver
	QueueCapacity  int
}

// NodeBoundPolicyService restricts authoritative Policy writes to the node
// owned by one ReconcileRuntime before a transaction can begin.
type NodeBoundPolicyService struct {
	nodeID  core.NodeID
	service *decision.PolicyService
}

// Replace applies one Policy replacement only when its node matches the
// runtime owner. The underlying service retains transaction and wake ordering.
func (s *NodeBoundPolicyService) Replace(ctx context.Context, request decision.PolicyWriteRequest) (decision.PolicyChange, error) {
	if s == nil || s.service == nil {
		return decision.PolicyChange{}, fmt.Errorf("node-bound policy service is not initialized")
	}
	if request.NodeID != s.nodeID {
		return decision.PolicyChange{}, fmt.Errorf("policy write node id does not match runtime node id")
	}
	return s.service.Replace(ctx, request)
}

// BootstrapInitialManagedPolicy writes the contract-defined initial Policy
// for this runtime node only before Dispatcher startup. Dispatcher startup
// then performs the first complete reconcile from durable Desired state.
func (s *NodeBoundPolicyService) BootstrapInitialManagedPolicy(
	ctx context.Context,
	updatedAt time.Time,
) (decision.PolicyChange, error) {
	if s == nil || s.service == nil {
		return decision.PolicyChange{}, fmt.Errorf("node-bound policy service is not initialized")
	}
	return s.service.BootstrapInitialManagedPolicy(ctx, s.nodeID, updatedAt)
}

// ReconcileRuntime owns one node-local Controller, DesiredPlanProvider,
// LifecycleService, ExpirationRuntime, Dispatcher and post-commit wake
// adapters. It does not create a Backend, start an Enforcer, or select any
// Desired authority.
type ReconcileRuntime struct {
	controller    *Controller
	plans         *DesiredPlanProvider
	dispatcher    *Dispatcher
	lifecycle     *decision.LifecycleService
	expiration    *ExpirationRuntime
	targetWake    *DispatcherTargetWakeSink
	policyWake    *DispatcherPolicyWakeSink
	finalizer     *decision.DesiredStateFinalizer
	policyService *NodeBoundPolicyService
	healthSource  *BackendHealthSource
	store         RuntimeStore

	runMu   sync.Mutex
	running bool
	stopped bool
}

// NewReconcileRuntime constructs the complete node-local Reconcile graph. It
// performs durable recovery reads but starts no goroutine or Firewall operation.
// The caller retains Store ownership until Run begins successfully.
func NewReconcileRuntime(ctx context.Context, dependencies RuntimeDependencies) (*ReconcileRuntime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("reconcile runtime context is required")
	}
	if dependencies.NodeID == "" {
		return nil, fmt.Errorf("reconcile runtime node id is required")
	}
	if dependencies.Backend == nil {
		return nil, fmt.Errorf("reconcile runtime backend is required")
	}
	if dependencies.Store == nil {
		return nil, fmt.Errorf("reconcile runtime store is required")
	}
	if dependencies.Audit == nil {
		return nil, fmt.Errorf("reconcile runtime audit writer is required")
	}
	if dependencies.Clock == nil {
		return nil, fmt.Errorf("reconcile runtime clock is required")
	}
	if dependencies.TargetPolicies == nil {
		return nil, fmt.Errorf("reconcile runtime target policy resolver is required")
	}
	if dependencies.QueueCapacity <= 0 {
		return nil, fmt.Errorf("reconcile runtime queue capacity must be positive")
	}
	if dependencies.Static.InfrastructureRevision == 0 || dependencies.Static.Infrastructure.Backend == "" ||
		dependencies.Static.Infrastructure.OwnerVersion == "" || dependencies.Static.Infrastructure.Digest == "" {
		return nil, fmt.Errorf("reconcile runtime static Infrastructure state is incomplete")
	}

	controller, err := NewPersistentController(
		ctx,
		dependencies.NodeID,
		dependencies.Backend,
		dependencies.Clock,
		dependencies.Audit,
		dependencies.Store,
	)
	if err != nil {
		return nil, fmt.Errorf("construct persistent Controller: %w", err)
	}
	plans, err := NewDesiredPlanProvider(dependencies.NodeID, controller, dependencies.Store, dependencies.Static)
	if err != nil {
		return nil, fmt.Errorf("construct Desired Plan provider: %w", err)
	}
	dispatcher, err := NewDispatcherWithClock(controller, plans, dependencies.QueueCapacity, dependencies.Clock)
	if err != nil {
		return nil, fmt.Errorf("construct Dispatcher: %w", err)
	}
	targetWake, err := NewDispatcherTargetWakeSink(dependencies.NodeID, dispatcher)
	if err != nil {
		return nil, fmt.Errorf("construct Target wake sink: %w", err)
	}
	policyWake, err := NewDispatcherPolicyWakeSink(dependencies.NodeID, dispatcher)
	if err != nil {
		return nil, fmt.Errorf("construct Policy wake sink: %w", err)
	}
	finalizer, err := decision.NewDesiredStateFinalizer(dependencies.TargetPolicies)
	if err != nil {
		return nil, fmt.Errorf("construct Desired state finalizer: %w", err)
	}
	lifecycle, err := decision.NewLifecycleServiceWithClock(
		dependencies.NodeID,
		dependencies.Store,
		finalizer,
		targetWake,
		dependencies.Clock,
	)
	if err != nil {
		return nil, fmt.Errorf("construct Decision lifecycle service: %w", err)
	}
	expiration, err := NewExpirationRuntime(lifecycle, dispatcher)
	if err != nil {
		return nil, fmt.Errorf("construct expiration runtime: %w", err)
	}
	decisionPolicyService, err := decision.NewPolicyService(
		dependencies.Store,
		dependencies.Store,
		finalizer,
		policyWake,
		targetWake,
	)
	if err != nil {
		return nil, fmt.Errorf("construct Policy service: %w", err)
	}
	policyService := &NodeBoundPolicyService{
		nodeID:  dependencies.NodeID,
		service: decisionPolicyService,
	}
	var healthSource *BackendHealthSource
	if prober, ok := dependencies.Backend.(BackendHealthProber); ok {
		healthSource, err = newBackendHealthSource(prober, dispatcher, backendHealthSourcePollInterval)
		if err != nil {
			return nil, fmt.Errorf("construct Backend health source: %w", err)
		}
	}
	return &ReconcileRuntime{
		controller:    controller,
		plans:         plans,
		dispatcher:    dispatcher,
		lifecycle:     lifecycle,
		expiration:    expiration,
		targetWake:    targetWake,
		policyWake:    policyWake,
		finalizer:     finalizer,
		policyService: policyService,
		healthSource:  healthSource,
		store:         dependencies.Store,
	}, nil
}

// Controller returns the node-local persistent Controller owned by this runtime.
func (r *ReconcileRuntime) Controller() *Controller {
	if r == nil {
		return nil
	}
	return r.controller
}

// Dispatcher returns the node-local Dispatcher owned by this runtime.
func (r *ReconcileRuntime) Dispatcher() *Dispatcher {
	if r == nil {
		return nil
	}
	return r.dispatcher
}

// DesiredStateFinalizer returns the resolver-backed finalizer for a separately
// composed authoritative Decision service.
func (r *ReconcileRuntime) DesiredStateFinalizer() *decision.DesiredStateFinalizer {
	if r == nil {
		return nil
	}
	return r.finalizer
}

// PolicyService returns the node-bound Policy application service composed
// with this runtime's Store, finalizer, and post-commit wake adapters.
func (r *ReconcileRuntime) PolicyService() *NodeBoundPolicyService {
	if r == nil {
		return nil
	}
	return r.policyService
}

// TargetWakeSink returns the node-bound Target post-commit wake adapter.
func (r *ReconcileRuntime) TargetWakeSink() decision.TargetWakeSink {
	if r == nil {
		return nil
	}
	return r.targetWake
}

// PolicyWakeSink returns the node-bound Policy post-commit wake adapter.
func (r *ReconcileRuntime) PolicyWakeSink() decision.PolicyWakeSink {
	if r == nil {
		return nil
	}
	return r.policyWake
}

// BootstrapInitialManagedPolicy initializes only a missing complete Policy
// before Run transfers Store ownership. It never wakes the not-yet-running
// Dispatcher; Run always loads all current Desired keys for initial reconcile.
func (r *ReconcileRuntime) BootstrapInitialManagedPolicy(
	ctx context.Context,
	updatedAt time.Time,
) (decision.PolicyChange, error) {
	if r == nil || r.policyService == nil {
		return decision.PolicyChange{}, fmt.Errorf("reconcile runtime is not initialized")
	}
	r.runMu.Lock()
	defer r.runMu.Unlock()
	if r.running || r.stopped {
		return decision.PolicyChange{}, fmt.Errorf("reconcile runtime policy bootstrap requires a new runtime")
	}
	return r.policyService.BootstrapInitialManagedPolicy(ctx, updatedAt)
}

// Run transfers Store ownership to the runtime, runs the expiration scheduler
// with Dispatcher, then closes Store after both have fully stopped. It
// preserves both terminal errors.
func (r *ReconcileRuntime) Run(ctx context.Context) error {
	if r == nil || r.expiration == nil || r.store == nil {
		return fmt.Errorf("reconcile runtime is not initialized")
	}
	if ctx == nil {
		return fmt.Errorf("reconcile runtime context is required")
	}
	r.runMu.Lock()
	if r.running {
		r.runMu.Unlock()
		return ErrReconcileRuntimeRunning
	}
	if r.stopped {
		r.runMu.Unlock()
		return ErrReconcileRuntimeStopped
	}
	r.running = true
	r.runMu.Unlock()

	runErr := r.runComponents(ctx)
	closeErr := r.store.Close()
	r.runMu.Lock()
	r.running = false
	r.stopped = true
	r.runMu.Unlock()
	return errors.Join(runErr, closeErr)
}

func (r *ReconcileRuntime) runComponents(ctx context.Context) error {
	if r.healthSource == nil {
		return r.expiration.Run(ctx)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan runtimeComponentResult, 2)
	go func() {
		results <- runtimeComponentResult{component: "expiration runtime", err: r.expiration.Run(runCtx)}
	}()
	go func() {
		results <- runtimeComponentResult{component: "Backend health source", err: r.healthSource.Run(runCtx)}
	}()

	first := <-results
	cancel()
	second := <-results
	if ctx.Err() != nil {
		return reconcileRuntimeShutdownError(first, second)
	}
	return reconcileRuntimeFailure(first, second)
}

type runtimeComponentResult struct {
	component string
	err       error
}

func reconcileRuntimeShutdownError(results ...runtimeComponentResult) error {
	errs := make([]error, 0, len(results))
	for _, result := range results {
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			errs = append(errs, fmt.Errorf("%s failed during shutdown: %w", result.component, result.err))
		}
	}
	return errors.Join(errs...)
}

func reconcileRuntimeFailure(first, second runtimeComponentResult) error {
	errs := make([]error, 0, 2)
	if first.err == nil {
		errs = append(errs, fmt.Errorf("%s stopped before runtime cancellation", first.component))
	} else {
		errs = append(errs, fmt.Errorf("%s failed: %w", first.component, first.err))
	}
	if second.err != nil && !errors.Is(second.err, context.Canceled) {
		errs = append(errs, fmt.Errorf("%s also failed: %w", second.component, second.err))
	}
	return errors.Join(errs...)
}
