package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"sync"
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

func TestReconcileRuntimeStartupFailureStopsDispatcherBeforeClosingStore(t *testing.T) {
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
	if store.closeCalls != 1 {
		t.Fatalf("Store Close calls = %d, want 1", store.closeCalls)
	}
	if store.lifecycleTransactions != 1 {
		t.Fatalf("startup expiration transactions = %d, want 1", store.lifecycleTransactions)
	}
	probes, applies := store.backend.Counts()
	if probes != 0 || applies != 0 {
		t.Fatalf("startup failure crossed Backend boundary: probes=%d applies=%d", probes, applies)
	}
}

func TestReconcileRuntimeCancellationStopsDispatcherBeforeClosingStore(t *testing.T) {
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
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() after cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	if store.closeCalls != 1 {
		t.Fatalf("Store Close calls = %d, want 1", store.closeCalls)
	}
	if store.lifecycleTransactions != 1 {
		t.Fatalf("startup expiration transactions = %d, want 1", store.lifecycleTransactions)
	}
	probes, _ := store.backend.Counts()
	if probes == 0 {
		t.Fatal("canceled runtime did not perform its authoritative startup Probe")
	}
	if err := runtime.Run(context.Background()); !errors.Is(err, ErrReconcileRuntimeStopped) {
		t.Fatalf("second Run() = %v, want stopped", err)
	}
}

func TestReconcileRuntimeJoinsRunAndCloseFailures(t *testing.T) {
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
	if !errors.Is(err, runFailure) || !errors.Is(err, closeFailure) {
		t.Fatalf("Run() = %v, want joined failures", err)
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
