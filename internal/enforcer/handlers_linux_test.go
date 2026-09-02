//go:build linux

package enforcer_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestNewEnforcerHandlersValidatesBackendAndCompleteness(t *testing.T) {
	for _, test := range []struct {
		name    string
		backend enforcer.MutationBackend
	}{
		{name: "nil"},
		{name: "typed nil", backend: (*scriptedMutationBackend)(nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			handlers, err := enforcer.NewEnforcerHandlers(test.backend)
			if !errors.Is(err, enforcer.ErrMutationBackendRequired) {
				t.Fatalf("error = %v, want ErrMutationBackendRequired", err)
			}
			if handlers.ProbeCapabilities != nil || handlers.SnapshotManaged != nil || handlers.Mutation != nil {
				t.Fatal("failed construction returned a partial handler set")
			}
		})
	}

	backend := &scriptedMutationBackend{
		capabilities: bridgeCapabilities(t),
		snapshot:     bridgeSnapshot(t, firewall.BackendKindNftablesNative, true, true),
	}
	handlers, err := enforcer.NewEnforcerHandlers(backend)
	if err != nil {
		t.Fatal(err)
	}
	if handlers.ProbeCapabilities == nil || handlers.SnapshotManaged == nil || handlers.Mutation == nil {
		t.Fatal("successful construction returned an incomplete handler set")
	}
}

func TestEnforcerHandlersProbeResponsesAreClosedAndFresh(t *testing.T) {
	notReady := handlerCapabilities(t, false)
	for _, test := range []struct {
		name     string
		backend  *scriptedMutationBackend
		wantCode ipc.ProbeCapabilitiesFailureCode
		wantOK   bool
	}{
		{
			name:    "success",
			backend: &scriptedMutationBackend{capabilities: bridgeCapabilities(t)},
			wantOK:  true,
		},
		{
			name:    "valid mutation not ready is success",
			backend: &scriptedMutationBackend{capabilities: notReady},
			wantOK:  true,
		},
		{
			name: "unsupported",
			backend: &scriptedMutationBackend{
				probeErr: fmt.Errorf("private probe detail: %w", enforcer.ErrMutationBackendUnsupported),
			},
			wantCode: ipc.ProbeCapabilitiesFailureCodeUnsupported,
		},
		{
			name: "ownership conflict is not ready",
			backend: &scriptedMutationBackend{
				probeErr: fmt.Errorf("private probe detail: %w", enforcer.ErrMutationBackendOwnershipConflict),
			},
			wantCode: ipc.ProbeCapabilitiesFailureCodeNotReady,
		},
		{
			name: "unclassified error",
			backend: &scriptedMutationBackend{
				probeErr: errors.New("private probe detail"),
			},
			wantCode: ipc.ProbeCapabilitiesFailureCodeNotReady,
		},
		{
			name:     "invalid capabilities",
			backend:  &scriptedMutationBackend{},
			wantCode: ipc.ProbeCapabilitiesFailureCodeNotReady,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handlers := mustEnforcerHandlers(t, test.backend)
			response := handlers.ProbeCapabilities(context.Background())
			if test.wantOK {
				success, ok := response.(ipc.ProbeCapabilitiesSuccessResponse)
				if !ok {
					t.Fatalf("response = %T, want success", response)
				}
				if !reflect.DeepEqual(success.Capabilities(), test.backend.capabilities) {
					t.Fatal("success response did not preserve the fresh capability observation")
				}
			} else {
				assertProbeHandlerFailure(t, response, test.wantCode)
			}
			if calls := test.backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe"}) {
				t.Fatalf("calls = %v, want one fresh Probe", calls)
			}
		})
	}
}

func TestEnforcerHandlersSnapshotResponsesAreClosedAndFresh(t *testing.T) {
	valid := bridgeSnapshot(t, firewall.BackendKindNftablesNative, true, true)
	for _, test := range []struct {
		name     string
		backend  *scriptedMutationBackend
		wantCode ipc.SnapshotManagedFailureCode
		wantOK   bool
	}{
		{name: "success", backend: &scriptedMutationBackend{snapshot: valid}, wantOK: true},
		{
			name: "unsupported",
			backend: &scriptedMutationBackend{
				snapshotErr: fmt.Errorf("private snapshot detail: %w", enforcer.ErrMutationBackendUnsupported),
			},
			wantCode: ipc.SnapshotManagedFailureCodeUnsupported,
		},
		{
			name: "enforcer ownership conflict",
			backend: &scriptedMutationBackend{
				snapshotErr: fmt.Errorf("private snapshot detail: %w", enforcer.ErrMutationBackendOwnershipConflict),
			},
			wantCode: ipc.SnapshotManagedFailureCodeOwnershipConflict,
		},
		{
			name: "firewall ownership conflict",
			backend: &scriptedMutationBackend{
				snapshotErr: fmt.Errorf("private snapshot detail: %w", firewall.NewOwnershipConflictError()),
			},
			wantCode: ipc.SnapshotManagedFailureCodeOwnershipConflict,
		},
		{
			name: "unclassified error",
			backend: &scriptedMutationBackend{
				snapshotErr: errors.New("private snapshot detail"),
			},
			wantCode: ipc.SnapshotManagedFailureCodeNotReady,
		},
		{
			name:     "invalid snapshot",
			backend:  &scriptedMutationBackend{},
			wantCode: ipc.SnapshotManagedFailureCodeNotReady,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handlers := mustEnforcerHandlers(t, test.backend)
			response := handlers.SnapshotManaged(context.Background())
			if test.wantOK {
				success, ok := response.(ipc.SnapshotManagedSuccessResponse)
				if !ok {
					t.Fatalf("response = %T, want success", response)
				}
				if success.Snapshot().Digest() != valid.Digest() {
					t.Fatal("success response did not preserve the fresh managed snapshot")
				}
			} else {
				assertSnapshotHandlerFailure(t, response, test.wantCode)
			}
			if calls := test.backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"snapshot"}) {
				t.Fatalf("calls = %v, want one direct fresh Snapshot", calls)
			}
		})
	}
}

func TestEnforcerHandlersCancellationDoesNotReachBackend(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	backend := &scriptedMutationBackend{capabilities: capabilities, snapshot: snapshot}
	handlers := mustEnforcerHandlers(t, backend)
	request := mustInfrastructureRequest(t, snapshot.Digest(), 1)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	assertProbeHandlerFailure(t, handlers.ProbeCapabilities(canceled), ipc.ProbeCapabilitiesFailureCodeNotReady)
	assertSnapshotHandlerFailure(t, handlers.SnapshotManaged(canceled), ipc.SnapshotManagedFailureCodeNotReady)
	assertExecutorResponse(
		t,
		handlers.Mutation(canceled, request),
		ipc.OperationApplyManagedPlan,
		ipc.MutationStatusRejected,
		ipc.MutationErrorCodeNotReady,
		true,
	)
	assertProbeHandlerFailure(t, handlers.ProbeCapabilities(nil), ipc.ProbeCapabilitiesFailureCodeNotReady)
	assertSnapshotHandlerFailure(t, handlers.SnapshotManaged(nil), ipc.SnapshotManagedFailureCodeNotReady)
	assertExecutorResponse(
		t,
		handlers.Mutation(nil, request),
		ipc.OperationApplyManagedPlan,
		ipc.MutationStatusRejected,
		ipc.MutationErrorCodeNotReady,
		true,
	)
	if calls := backend.recordedCalls(); len(calls) != 0 {
		t.Fatalf("canceled or nil context reached backend: %v", calls)
	}
}

func TestEnforcerHandlersDelegateClosedMutations(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)

	t.Run("apply", func(t *testing.T) {
		backend := &scriptedMutationBackend{capabilities: capabilities, snapshot: snapshot}
		backend.applyResult = func(plan firewall.OperationPlan) firewall.MutationResult {
			result, err := firewall.NewConfirmedMutationResult(plan)
			if err != nil {
				panic(err)
			}
			return result
		}
		handlers := mustEnforcerHandlers(t, backend)
		response := handlers.Mutation(context.Background(), mustInfrastructureRequest(t, snapshot.Digest(), 1))
		assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)
		if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe", "snapshot", "apply:infrastructure"}) {
			t.Fatalf("calls = %v, want one fresh Apply attempt", calls)
		}
	})

	t.Run("remove residue", func(t *testing.T) {
		backend := &scriptedMutationBackend{
			capabilities: capabilities,
			snapshot:     bridgeResidualSnapshot(t, true, false),
		}
		backend.removeResult = func(authorization firewall.RemovalAuthorization) firewall.MutationResult {
			result, err := firewall.NewConfirmedMutationResult(authorization)
			if err != nil {
				panic(err)
			}
			return result
		}
		handlers := mustEnforcerHandlers(t, backend)
		response := handlers.Mutation(context.Background(), ipc.NewRemoveManagedInfrastructureRequest())
		assertExecutorResponse(t, response, ipc.OperationRemoveManagedInfrastructure, ipc.MutationStatusConfirmed, "", false)
		if backend.lastRemoval == nil || backend.lastRemoval.BasisSnapshotDigest() != backend.snapshot.Digest() {
			t.Fatal("handler did not dispatch the fresh snapshot-scoped removal authority")
		}
		if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe", "snapshot", "remove"}) {
			t.Fatalf("calls = %v, want one fresh Remove attempt", calls)
		}
	})
}

func TestEnforcerHandlersCopiesShareCrossOperationSerialOwnership(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	backend := newBlockingHandlerBackend(capabilities, snapshot)
	handlers := mustEnforcerHandlers(t, backend)
	copied := handlers
	request := mustInfrastructureRequest(t, snapshot.Digest(), 1)

	probeDone := make(chan ipc.ProbeCapabilitiesResponse, 1)
	go func() { probeDone <- handlers.ProbeCapabilities(context.Background()) }()
	if operation := waitHandlerEntry(t, backend.entered); operation != "probe" {
		t.Fatalf("first backend operation = %q, want probe", operation)
	}

	snapshotDone := make(chan ipc.SnapshotManagedResponse, 1)
	mutationDone := make(chan ipc.MutationResponse, 1)
	snapshotCtx := newDoneObservedContext(context.Background())
	mutationCtx := newDoneObservedContext(context.Background())
	go func() { snapshotDone <- copied.SnapshotManaged(snapshotCtx) }()
	go func() { mutationDone <- copied.Mutation(mutationCtx, request) }()
	waitContextDoneObserved(t, snapshotCtx)
	waitContextDoneObserved(t, mutationCtx)
	assertNoImmediateHandlerEntry(t, backend.entered)

	operations := []string{"probe"}
	backend.release <- struct{}{}
	for len(operations) < 5 {
		operations = append(operations, waitHandlerEntry(t, backend.entered))
		assertNoImmediateHandlerEntry(t, backend.entered)
		backend.release <- struct{}{}
	}

	if _, ok := (<-probeDone).(ipc.ProbeCapabilitiesSuccessResponse); !ok {
		t.Fatal("Probe handler did not return success")
	}
	if _, ok := (<-snapshotDone).(ipc.SnapshotManagedSuccessResponse); !ok {
		t.Fatal("Snapshot handler did not return success")
	}
	assertExecutorResponse(t, <-mutationDone, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)
	if maximum := backend.maxActive.Load(); maximum != 1 {
		t.Fatalf("maximum concurrent backend operations = %d, want 1; operations=%v", maximum, operations)
	}
}

func TestEnforcerHandlersWaitingCancellationDoesNotRunLater(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	backend := newBlockingHandlerBackend(capabilities, snapshot)
	handlers := mustEnforcerHandlers(t, backend)

	probeDone := make(chan ipc.ProbeCapabilitiesResponse, 1)
	go func() { probeDone <- handlers.ProbeCapabilities(context.Background()) }()
	if operation := waitHandlerEntry(t, backend.entered); operation != "probe" {
		t.Fatalf("first backend operation = %q, want probe", operation)
	}

	waitingCtx, cancelWaiting := context.WithCancel(context.Background())
	observedWaitingCtx := newDoneObservedContext(waitingCtx)
	waitingDone := make(chan ipc.SnapshotManagedResponse, 1)
	go func() { waitingDone <- handlers.SnapshotManaged(observedWaitingCtx) }()
	waitContextDoneObserved(t, observedWaitingCtx)
	assertNoImmediateHandlerEntry(t, backend.entered)
	cancelWaiting()
	select {
	case response := <-waitingDone:
		assertSnapshotHandlerFailure(t, response, ipc.SnapshotManagedFailureCodeNotReady)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiting handler did not return")
	}
	backend.release <- struct{}{}
	if _, ok := (<-probeDone).(ipc.ProbeCapabilitiesSuccessResponse); !ok {
		t.Fatal("active Probe handler did not finish after release")
	}
	assertNoImmediateHandlerEntry(t, backend.entered)
}

func TestEnforcerHandlersPanicReleasesSharedGate(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)

	t.Run("direct Probe", func(t *testing.T) {
		backend := &scriptedMutationBackend{capabilities: capabilities, snapshot: snapshot}
		panicValue := errors.New("probe panic")
		backend.probeHook = func() { panic(panicValue) }
		handlers := mustEnforcerHandlers(t, backend)
		assertHandlerPanicIdentity(t, panicValue, func() {
			handlers.ProbeCapabilities(context.Background())
		})

		backend.probeHook = nil
		if _, ok := handlers.SnapshotManaged(context.Background()).(ipc.SnapshotManagedSuccessResponse); !ok {
			t.Fatal("shared gate was not reusable after Probe panic")
		}
	})

	t.Run("Mutation Apply", func(t *testing.T) {
		backend := &scriptedMutationBackend{capabilities: capabilities, snapshot: snapshot}
		panicValue := errors.New("apply panic")
		backend.applyHook = func() { panic(panicValue) }
		handlers := mustEnforcerHandlers(t, backend)
		request := mustInfrastructureRequest(t, snapshot.Digest(), 1)
		assertHandlerPanicIdentity(t, panicValue, func() {
			handlers.Mutation(context.Background(), request)
		})

		backend.applyHook = nil
		backend.applyResult = func(plan firewall.OperationPlan) firewall.MutationResult {
			result, err := firewall.NewConfirmedMutationResult(plan)
			if err != nil {
				panic(err)
			}
			return result
		}
		assertExecutorResponse(
			t,
			handlers.Mutation(context.Background(), request),
			ipc.OperationApplyManagedPlan,
			ipc.MutationStatusConfirmed,
			"",
			false,
		)
	})
}

func handlerCapabilities(t *testing.T, mutationReady bool) firewall.FirewallCapabilities {
	t.Helper()
	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend: firewall.BackendKindNftablesNative, ToolVersion: "handler-test-1",
		IPv4: true, IPv6: true, CIDR: true, NativeSet: true,
		NativeTimeout: true, CrashSafeExpiry: true, AtomicBatch: true,
		HostInput: true, Forward: true, OwnershipProven: mutationReady, MutationReady: mutationReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	return capabilities
}

func mustEnforcerHandlers(t *testing.T, backend enforcer.MutationBackend) ipc.EnforcerHandlers {
	t.Helper()
	handlers, err := enforcer.NewEnforcerHandlers(backend)
	if err != nil {
		t.Fatal(err)
	}
	return handlers
}

func assertProbeHandlerFailure(
	t *testing.T,
	response ipc.ProbeCapabilitiesResponse,
	want ipc.ProbeCapabilitiesFailureCode,
) {
	t.Helper()
	failure, ok := response.(ipc.ProbeCapabilitiesFailureResponse)
	if !ok || failure.FailureCode() != want {
		t.Fatalf("response = %T code=%v, want Probe failure %q", response, probeFailureCode(response), want)
	}
}

func probeFailureCode(response ipc.ProbeCapabilitiesResponse) ipc.ProbeCapabilitiesFailureCode {
	failure, ok := response.(ipc.ProbeCapabilitiesFailureResponse)
	if !ok {
		return ""
	}
	return failure.FailureCode()
}

func assertSnapshotHandlerFailure(
	t *testing.T,
	response ipc.SnapshotManagedResponse,
	want ipc.SnapshotManagedFailureCode,
) {
	t.Helper()
	failure, ok := response.(ipc.SnapshotManagedFailureResponse)
	if !ok || failure.FailureCode() != want {
		t.Fatalf("response = %T code=%v, want Snapshot failure %q", response, snapshotFailureCode(response), want)
	}
}

func snapshotFailureCode(response ipc.SnapshotManagedResponse) ipc.SnapshotManagedFailureCode {
	failure, ok := response.(ipc.SnapshotManagedFailureResponse)
	if !ok {
		return ""
	}
	return failure.FailureCode()
}

func waitHandlerEntry(t *testing.T, entered <-chan string) string {
	t.Helper()
	select {
	case operation := <-entered:
		return operation
	case <-time.After(5 * time.Second):
		t.Fatal("backend operation did not start")
		return ""
	}
}

func assertNoImmediateHandlerEntry(t *testing.T, entered <-chan string) {
	t.Helper()
	select {
	case operation := <-entered:
		t.Fatalf("backend operation %q overlapped the active handler", operation)
	default:
	}
}

type doneObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func newDoneObservedContext(ctx context.Context) *doneObservedContext {
	return &doneObservedContext{Context: ctx, observed: make(chan struct{})}
}

func (c *doneObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func waitContextDoneObserved(t *testing.T, ctx *doneObservedContext) {
	t.Helper()
	select {
	case <-ctx.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not reach context-aware admission")
	}
}

func assertHandlerPanicIdentity(t *testing.T, want any, call func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != want {
			t.Fatalf("recovered panic = %v, want original panic identity", recovered)
		}
	}()
	call()
}

type blockingHandlerBackend struct {
	capabilities firewall.FirewallCapabilities
	snapshot     firewall.ManagedSnapshot
	entered      chan string
	release      chan struct{}
	active       atomic.Int32
	maxActive    atomic.Int32
}

func newBlockingHandlerBackend(
	capabilities firewall.FirewallCapabilities,
	snapshot firewall.ManagedSnapshot,
) *blockingHandlerBackend {
	return &blockingHandlerBackend{
		capabilities: capabilities,
		snapshot:     snapshot,
		entered:      make(chan string, 8),
		release:      make(chan struct{}),
	}
}

func (b *blockingHandlerBackend) Probe(ctx context.Context) (firewall.FirewallCapabilities, error) {
	b.enter(ctx, "probe")
	return b.capabilities, ctx.Err()
}

func (b *blockingHandlerBackend) Snapshot(ctx context.Context) (firewall.ManagedSnapshot, error) {
	b.enter(ctx, "snapshot")
	return b.snapshot, ctx.Err()
}

func (b *blockingHandlerBackend) Apply(
	ctx context.Context,
	plan firewall.OperationPlan,
) firewall.MutationResult {
	b.enter(ctx, "apply")
	result, err := firewall.NewConfirmedMutationResult(plan)
	if err != nil {
		panic(err)
	}
	return result
}

func (b *blockingHandlerBackend) RemoveManagedInfrastructure(
	ctx context.Context,
	authorization firewall.RemovalAuthorization,
) firewall.MutationResult {
	b.enter(ctx, "remove")
	result, err := firewall.NewConfirmedMutationResult(authorization)
	if err != nil {
		panic(err)
	}
	return result
}

func (b *blockingHandlerBackend) enter(ctx context.Context, operation string) {
	active := b.active.Add(1)
	for {
		maximum := b.maxActive.Load()
		if active <= maximum || b.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	defer b.active.Add(-1)
	b.entered <- operation
	select {
	case <-ctx.Done():
	case <-b.release:
	}
}

var _ enforcer.MutationBackend = (*blockingHandlerBackend)(nil)
