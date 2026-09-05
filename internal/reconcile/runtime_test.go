package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/clock"
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/enforcement"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

func TestNewReconcileRuntimeRejectsIncompleteDependencies(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*RuntimeDependencies)
	}{
		{name: "node id", mutate: func(dependencies *RuntimeDependencies) { dependencies.NodeID = "" }},
		{name: "backend", mutate: func(dependencies *RuntimeDependencies) { dependencies.Backend = nil }},
		{name: "store", mutate: func(dependencies *RuntimeDependencies) { dependencies.Store = nil }},
		{name: "audit", mutate: func(dependencies *RuntimeDependencies) { dependencies.Audit = nil }},
		{name: "clock", mutate: func(dependencies *RuntimeDependencies) { dependencies.Clock = nil }},
		{name: "target policies", mutate: func(dependencies *RuntimeDependencies) { dependencies.TargetPolicies = nil }},
		{name: "queue capacity", mutate: func(dependencies *RuntimeDependencies) { dependencies.QueueCapacity = 0 }},
		{name: "infrastructure revision", mutate: func(dependencies *RuntimeDependencies) { dependencies.Static.InfrastructureRevision = 0 }},
		{name: "infrastructure backend", mutate: func(dependencies *RuntimeDependencies) { dependencies.Static.Infrastructure.Backend = "" }},
		{name: "infrastructure owner", mutate: func(dependencies *RuntimeDependencies) { dependencies.Static.Infrastructure.OwnerVersion = "" }},
		{name: "infrastructure digest", mutate: func(dependencies *RuntimeDependencies) { dependencies.Static.Infrastructure.Digest = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies, store := newRuntimeDependencies()
			test.mutate(&dependencies)
			if _, err := NewReconcileRuntime(context.Background(), dependencies); err == nil {
				t.Fatal("NewReconcileRuntime accepted incomplete dependencies")
			}
			if store.recoveryLoads != 0 || store.observedLoads != 0 || store.closeCalls != 0 {
				t.Fatalf("incomplete dependencies touched Store: recovery=%d observed=%d close=%d", store.recoveryLoads, store.observedLoads, store.closeCalls)
			}
			probes, applies := store.backend.Counts()
			if probes != 0 || applies != 0 {
				t.Fatalf("incomplete dependencies crossed Backend boundary: probes=%d applies=%d", probes, applies)
			}
		})
	}
}

func TestNewReconcileRuntimeComposesOneNodeBoundGraph(t *testing.T) {
	dependencies, store := newRuntimeDependencies()
	runtime, err := NewReconcileRuntime(context.Background(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Controller() == nil || runtime.Dispatcher() == nil || runtime.lifecycle == nil || runtime.expiration == nil || runtime.DesiredStateFinalizer() == nil || runtime.PolicyService() == nil ||
		runtime.TargetWakeSink() == nil || runtime.PolicyWakeSink() == nil {
		t.Fatal("runtime did not construct its complete graph")
	}
	if runtime.PolicyService() != runtime.PolicyService() {
		t.Fatal("runtime did not retain one Policy service")
	}
	if runtime.controller.nodeID != dependencies.NodeID || runtime.plans.nodeID != dependencies.NodeID ||
		runtime.targetWake.nodeID != dependencies.NodeID || runtime.policyWake.nodeID != dependencies.NodeID {
		t.Fatal("runtime graph is not bound to one NodeID")
	}
	if runtime.plans.controller != runtime.controller || runtime.dispatcher.controller != runtime.controller ||
		runtime.dispatcher.plans != runtime.plans || runtime.targetWake.dispatcher != runtime.dispatcher ||
		runtime.policyWake.dispatcher != runtime.dispatcher {
		t.Fatal("runtime graph does not share its Controller, PlanProvider, and Dispatcher")
	}
	if runtime.expiration.scheduler != runtime.lifecycle || runtime.expiration.dispatcher != runtime.dispatcher {
		t.Fatal("runtime graph does not bind the LifecycleService and Dispatcher to one expiration runtime")
	}
	if store.recoveryLoads != 1 || store.observedLoads != 1 {
		t.Fatalf("persistent recovery reads = (%d, %d), want (1, 1)", store.recoveryLoads, store.observedLoads)
	}
	probes, applies := store.backend.Counts()
	if probes != 0 || applies != 0 || store.closeCalls != 0 || store.policyTransactions != 0 || store.lifecycleTransactions != 0 {
		t.Fatalf("construction performed I/O: probes=%d applies=%d close=%d policy_transactions=%d lifecycle_transactions=%d", probes, applies, store.closeCalls, store.policyTransactions, store.lifecycleTransactions)
	}
}

func TestReconcileRuntimeStartupFailureStopsDispatcherBeforeOwnerClosesStore(t *testing.T) {
	dependencies, store := newRuntimeDependencies()
	want := errors.New("load desired failed")
	store.desiredErr = want
	runtime, err := NewReconcileRuntime(context.Background(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	store.close = func() error {
		if err := runtime.TargetWakeSink().WakeTarget(context.Background(), dependencies.NodeID, netip.MustParsePrefix("192.0.2.9/32")); !errors.Is(err, ErrDispatcherStopped) {
			t.Fatalf("Target wake during Store close = %v, want stopped", err)
		}
		if err := runtime.PolicyWakeSink().WakePolicy(context.Background(), dependencies.NodeID); !errors.Is(err, ErrDispatcherStopped) {
			t.Fatalf("Policy wake during Store close = %v, want stopped", err)
		}
		return nil
	}
	if err := runtime.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run() = %v, want startup failure", err)
	}
	if store.closeCalls != 0 {
		t.Fatalf("Run closed borrowed Store %d times, want 0", store.closeCalls)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("owner Close: %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("owner Store Close calls = %d, want 1", store.closeCalls)
	}
	if store.lifecycleTransactions != 1 {
		t.Fatalf("startup expiration transactions = %d, want 1", store.lifecycleTransactions)
	}
	probes, applies := store.backend.Counts()
	if probes != 0 || applies != 0 {
		t.Fatalf("startup failure crossed Backend boundary: probes=%d applies=%d", probes, applies)
	}
}

func TestReconcileRuntimeCancellationStopsDispatcherBeforeOwnerClosesStore(t *testing.T) {
	dependencies, store := newRuntimeDependencies()
	runtime, err := NewReconcileRuntime(context.Background(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	store.close = func() error {
		if err := runtime.PolicyWakeSink().WakePolicy(context.Background(), dependencies.NodeID); !errors.Is(err, ErrDispatcherStopped) {
			t.Fatalf("Policy wake during Store close = %v, want stopped", err)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-runtime.dispatcher.startupReady:
	case <-time.After(time.Second):
		t.Fatal("Dispatcher did not finish startup")
	}
	if err := runtime.Run(context.Background()); !errors.Is(err, ErrReconcileRuntimeRunning) {
		t.Fatalf("concurrent Run() = %v, want running", err)
	}
	if store.closeCalls != 0 {
		t.Fatalf("concurrent Run closed borrowed Store %d times, want 0", store.closeCalls)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() after cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	if err := runtime.Run(context.Background()); !errors.Is(err, ErrReconcileRuntimeStopped) {
		t.Fatalf("Run() after stop with Store still open = %v, want stopped", err)
	}
	if store.closeCalls != 0 {
		t.Fatalf("Run closed borrowed Store %d times, want 0", store.closeCalls)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("owner Close: %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("owner Store Close calls = %d, want 1", store.closeCalls)
	}
	if store.lifecycleTransactions != 1 {
		t.Fatalf("startup expiration transactions = %d, want 1", store.lifecycleTransactions)
	}
	probes, _ := store.backend.Counts()
	if probes == 0 {
		t.Fatal("canceled runtime did not perform its authoritative startup Probe")
	}
}

func TestReconcileRuntimeWiresIPCHealthRecoveryWithoutMutation(t *testing.T) {
	dependencies, store := newRuntimeDependencies()
	backend := &runtimeHealthBackend{Backend: fake.NewBackend()}
	backend.unreachable.Store(true)
	dependencies.Backend = backend
	runtime, err := NewReconcileRuntime(context.Background(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.healthSource == nil {
		t.Fatal("runtime did not compose its Backend health source")
	}
	runtime.healthSource.interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	waitForCounter(t, &backend.healthProbes, 1)
	select {
	case <-runtime.dispatcher.startupReady:
	case <-time.After(time.Second):
		t.Fatal("Dispatcher did not finish startup")
	}
	probesBefore, appliesBefore := backend.Counts()
	healthProbesBefore := backend.healthProbes.Load()
	backend.unreachable.Store(false)
	waitForCounter(t, &backend.healthProbes, healthProbesBefore+3)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		probes, _ := backend.Counts()
		if probes > probesBefore {
			break
		}
		time.Sleep(time.Millisecond)
	}
	probes, applies := backend.Counts()
	if probes != probesBefore+1 || applies != appliesBefore {
		t.Fatalf("recovery crossed Backend boundary as probes=%d/%d applies=%d/%d, want one authoritative Probe and no extra mutation", probes, probesBefore, applies, appliesBefore)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	if store.closeCalls != 0 {
		t.Fatalf("Run closed borrowed Store %d times, want 0", store.closeCalls)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("owner Close: %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("owner Store Close calls = %d, want 1", store.closeCalls)
	}
}

func TestReconcileRuntimeStopsHealthSourceBeforeOwnerClosesStore(t *testing.T) {
	dependencies, store := newRuntimeDependencies()
	backend := &blockingRuntimeHealthBackend{
		Backend: fake.NewBackend(), entered: make(chan struct{}), exited: make(chan struct{}),
	}
	dependencies.Backend = backend
	runtime, err := NewReconcileRuntime(context.Background(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	store.close = func() error {
		select {
		case <-backend.exited:
			return nil
		default:
			t.Fatal("Store closed before Backend health source stopped")
			return nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-backend.entered:
	case <-time.After(time.Second):
		t.Fatal("runtime did not start Backend health source")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	if store.closeCalls != 0 {
		t.Fatalf("Run closed borrowed Store %d times, want 0", store.closeCalls)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("owner Close: %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("owner Store Close calls = %d, want 1", store.closeCalls)
	}
}

func TestReconcileRuntimeFailsOnHealthRecoveryObservedPersistenceError(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "runtime-health-recovery.db")
	database := openRestartStore(t, ctx, databasePath, time.Now().UTC())
	closed := false
	defer func() {
		if !closed {
			_ = database.Close()
		}
	}()
	dependencies, _ := newRuntimeDependencies()
	releaseRecovery := make(chan struct{})
	backend := &scriptedRuntimeHealthBackend{
		Backend:         fake.NewBackend(),
		releaseRecovery: releaseRecovery,
	}
	dependencies.Backend = backend
	persistErr := errors.New("persist recovered Observed state")
	store := &failingRuntimeObservedStore{RuntimeStore: database, failOnObservedWrite: 2, err: persistErr}
	dependencies.Store = store
	runtime, err := NewReconcileRuntime(ctx, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.BootstrapInitialManagedPolicy(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, key := range []ReconcileKey{
		{Domain: DomainInfrastructure},
		{Domain: DomainPolicy},
	} {
		plan, ok, err := runtime.plans.CurrentPlan(ctx, key)
		if err != nil || !ok {
			t.Fatalf("load startup plan %s: ok=%t err=%v", reconcileKeyName(key), ok, err)
		}
		if _, err := backend.Backend.Apply(ctx, plan); err != nil {
			t.Fatalf("pre-align startup physical state for %s: %v", reconcileKeyName(key), err)
		}
	}
	runtime.healthSource.interval = time.Millisecond
	store.close = func() error {
		if err := runtime.TargetWakeSink().WakeTarget(context.Background(), dependencies.NodeID, netip.MustParsePrefix("192.0.2.9/32")); !errors.Is(err, ErrDispatcherStopped) {
			t.Fatalf("Target wake during Store close = %v, want stopped", err)
		}
		closed = true
		return database.Close()
	}
	probesBefore, appliesBefore := backend.Counts()
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	waitForCounter(t, &backend.healthProbes, 1)
	select {
	case <-runtime.dispatcher.startupReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatcher did not finish startup")
	}
	waitForCounter(t, &store.observedWrites, 1)
	probesBefore, appliesBefore = backend.Counts()
	close(releaseRecovery)
	select {
	case err := <-done:
		if !errors.Is(err, persistErr) {
			t.Fatalf("Run() = %v, want recovered Observed persistence failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime swallowed recovered Observed persistence failure")
	}
	if healthProbes := backend.healthProbes.Load(); healthProbes != 2 {
		t.Fatalf("health Probe calls = %d, want initial unavailable and one recovery observation", healthProbes)
	}
	probes, applies := backend.Counts()
	if probes != probesBefore+1 || applies != appliesBefore {
		t.Fatalf("recovery failure Backend calls = probes %d/%d applies %d/%d, want one authoritative Probe and no mutation", probes, probesBefore, applies, appliesBefore)
	}
	if store.closeCalls != 0 {
		t.Fatalf("Run closed borrowed Store %d times, want 0", store.closeCalls)
	}
	observed, err := database.LoadObservedFirewallSnapshot(ctx, dependencies.NodeID)
	if err != nil {
		t.Fatalf("read borrowed Store after Run returned: %v", err)
	}
	if observed.Infrastructure == nil {
		t.Fatal("borrowed Store lost startup Observed infrastructure")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("owner Close: %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("owner Store Close calls = %d, want 1", store.closeCalls)
	}
	reopened := openRestartStore(t, ctx, databasePath, time.Now().UTC())
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened Store: %v", err)
		}
	}()
	recovered, err := reopened.LoadObservedFirewallSnapshot(ctx, dependencies.NodeID)
	if err != nil {
		t.Fatalf("read reopened Store: %v", err)
	}
	if recovered.Infrastructure == nil || *recovered.Infrastructure != *observed.Infrastructure {
		t.Fatalf("reopened infrastructure = %+v, want %+v", recovered.Infrastructure, observed.Infrastructure)
	}
}

type failingRuntimeObservedStore struct {
	RuntimeStore
	observedWrites      atomic.Uint64
	failOnObservedWrite uint64
	err                 error
	close               func() error
	closeCalls          uint64
}

func (s *failingRuntimeObservedStore) ApplyObservedFirewallUpdate(ctx context.Context, update core.ObservedFirewallUpdate) error {
	if s.observedWrites.Add(1) == s.failOnObservedWrite {
		return s.err
	}
	return s.RuntimeStore.ApplyObservedFirewallUpdate(ctx, update)
}

func (s *failingRuntimeObservedStore) Close() error {
	s.closeCalls++
	return s.close()
}

func TestReconcileRuntimeLeavesCloseFailureToOwner(t *testing.T) {
	dependencies, store := newRuntimeDependencies()
	runFailure := errors.New("startup failure")
	closeFailure := errors.New("close failure")
	store.desiredErr = runFailure
	store.close = func() error { return closeFailure }
	runtime, err := NewReconcileRuntime(context.Background(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Run(context.Background())
	if !errors.Is(err, runFailure) || errors.Is(err, closeFailure) {
		t.Fatalf("Run() = %v, want startup failure only", err)
	}
	if store.closeCalls != 0 {
		t.Fatalf("Run closed borrowed Store %d times, want 0", store.closeCalls)
	}
	if err := store.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("owner Close() = %v, want close failure", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("owner Store Close calls = %d, want 1", store.closeCalls)
	}
}

func TestReconcileRuntimeWakeSinksRejectCrossNode(t *testing.T) {
	dependencies, _ := newRuntimeDependencies()
	runtime, err := NewReconcileRuntime(context.Background(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	otherNode := core.NodeID("ffeeddccbbaa99887766554433221100")
	if err := runtime.TargetWakeSink().WakeTarget(context.Background(), otherNode, netip.MustParsePrefix("192.0.2.1/32")); err == nil {
		t.Fatal("Target wake accepted another node")
	}
	if err := runtime.PolicyWakeSink().WakePolicy(context.Background(), otherNode); err == nil {
		t.Fatal("Policy wake accepted another node")
	}
}

func TestReconcileRuntimePolicyServiceRejectsCrossNodeBeforeTransaction(t *testing.T) {
	dependencies, store := newRuntimeDependencies()
	runtime, err := NewReconcileRuntime(context.Background(), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PolicyService().Replace(context.Background(), decision.PolicyWriteRequest{
		NodeID: core.NodeID("ffeeddccbbaa99887766554433221100"),
	}); err == nil {
		t.Fatal("PolicyService accepted another node")
	}
	if store.policyTransactions != 0 {
		t.Fatalf("cross-node Policy write began %d transactions, want 0", store.policyTransactions)
	}
}

func newRuntimeDependencies() (RuntimeDependencies, *runtimeStoreStub) {
	backend := fake.NewBackend()
	store := &runtimeStoreStub{backend: backend, desiredStateReaderStub: desiredStateReaderStub{revision: 1}}
	return RuntimeDependencies{
		NodeID: testNodeID, Backend: backend, Store: store, Audit: &memoryAudit{}, Clock: clock.NewWallClock(),
		Static: StaticDesiredFirewallState{InfrastructureRevision: 1, Infrastructure: core.ManagedInfrastructureIntent{
			Backend: "fake", OwnerVersion: "v1", Digest: "infra-v1",
		}},
		TargetPolicies: decision.TargetPolicyResolverFunc(func(context.Context, decision.DesiredStateTransaction, core.DesiredBanProjection) (enforcement.TargetPolicy, error) {
			return enforcement.TargetPolicy{}, nil
		}),
		QueueCapacity: 1,
	}, store
}

type runtimeStoreStub struct {
	desiredStateReaderStub
	backend *fake.Backend

	mu                    sync.Mutex
	recoveryLoads         int
	observedLoads         int
	lifecycleTransactions int
	policyTransactions    int
	closeCalls            int
	close                 func() error
}

type runtimeHealthBackend struct {
	*fake.Backend
	unreachable  atomic.Bool
	healthProbes atomic.Uint64
}

type scriptedRuntimeHealthBackend struct {
	*fake.Backend
	releaseRecovery <-chan struct{}
	healthProbes    atomic.Uint64
}

func (b *scriptedRuntimeHealthBackend) ProbeHealth(ctx context.Context) error {
	switch b.healthProbes.Add(1) {
	case 1:
		return errBackendUnavailable
	case 2:
		select {
		case <-b.releaseRecovery:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		return errors.New("unexpected Backend health probe")
	}
}

func (b *runtimeHealthBackend) ProbeHealth(context.Context) error {
	b.healthProbes.Add(1)
	if b.unreachable.Load() {
		return errBackendUnavailable
	}
	return nil
}

type blockingRuntimeHealthBackend struct {
	*fake.Backend
	entered, exited chan struct{}
	exitOnce        sync.Once
}

func (b *blockingRuntimeHealthBackend) ProbeHealth(ctx context.Context) error {
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	<-ctx.Done()
	b.exitOnce.Do(func() { close(b.exited) })
	return ctx.Err()
}

func (s *runtimeStoreStub) LoadReconcileRecovery(ctx context.Context, nodeID core.NodeID) (core.ReconcileRecoverySnapshot, error) {
	s.mu.Lock()
	s.recoveryLoads++
	s.mu.Unlock()
	return s.desiredStateReaderStub.LoadReconcileRecovery(ctx, nodeID)
}

func (s *runtimeStoreStub) ApplyReconcileTransition(context.Context, core.ReconcileStateTransition) error {
	return nil
}

func (s *runtimeStoreStub) ApplyReconcileRetryTransition(context.Context, core.ReconcileRetryTransition) error {
	return nil
}

func (s *runtimeStoreStub) ReadReconcileRetryTransition(ctx context.Context, transition core.ReconcileRetryTransition) (core.ReconcileRetryReadback, error) {
	recovery, err := s.LoadReconcileRecovery(ctx, transition.State.NodeID)
	return core.ReconcileRetryReadback{Recovery: recovery}, err
}

func (s *runtimeStoreStub) LoadObservedFirewallSnapshot(context.Context, core.NodeID) (core.ObservedFirewallSnapshot, error) {
	s.mu.Lock()
	s.observedLoads++
	s.mu.Unlock()
	return core.ObservedFirewallSnapshot{}, nil
}

func (s *runtimeStoreStub) ApplyObservedFirewallUpdate(context.Context, core.ObservedFirewallUpdate) error {
	return nil
}

func (s *runtimeStoreStub) RunPolicyTransaction(context.Context, func(decision.PolicyTransaction) error) error {
	s.mu.Lock()
	s.policyTransactions++
	s.mu.Unlock()
	return nil
}

func (s *runtimeStoreStub) RunDecisionTransaction(context.Context, func(decision.LifecycleTransaction) error) error {
	s.mu.Lock()
	s.lifecycleTransactions++
	s.mu.Unlock()
	return nil
}

func (s *runtimeStoreStub) Close() error {
	s.mu.Lock()
	s.closeCalls++
	close := s.close
	s.mu.Unlock()
	if close == nil {
		return nil
	}
	return close()
}
