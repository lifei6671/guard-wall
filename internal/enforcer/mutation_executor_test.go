package enforcer_test

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestMutationExecutorFreshApplyDomains(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	basis := snapshot.Digest()
	infrastructure, err := ipc.NewApplyInfrastructureRequest(basis, 1)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ipc.NewApplyPolicyRequest(
		basis,
		2,
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		bridgeProtectedTargets(),
	)
	if err != nil {
		t.Fatal(err)
	}
	target, err := ipc.NewApplyTargetRequest(
		basis,
		3,
		netip.MustParsePrefix("192.0.2.0/24"),
		ipc.MembershipPresent,
		ipc.TimeoutModeNative,
		1_800_000_000_000_000,
		true,
		[]ipc.Scope{ipc.ScopeInput, ipc.ScopeForward},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		request ipc.MutationRequest
		domain  firewall.MutationDomain
		wire    ipc.Domain
	}{
		{name: "infrastructure", request: infrastructure, domain: firewall.MutationDomainInfrastructure, wire: ipc.DomainInfrastructure},
		{name: "policy", request: policy, domain: firewall.MutationDomainPolicy, wire: ipc.DomainPolicy},
		{name: "target", request: target, domain: firewall.MutationDomainTarget, wire: ipc.DomainTarget},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &scriptedMutationBackend{capabilities: capabilities, snapshot: snapshot}
			backend.applyResult = func(plan firewall.OperationPlan) firewall.MutationResult {
				result, err := firewall.NewConfirmedMutationResult(plan)
				if err != nil {
					panic(err)
				}
				return result
			}
			executor, err := enforcer.NewMutationExecutor(backend)
			if err != nil {
				t.Fatal(err)
			}

			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, test.name)
			response := executor.Execute(ctx, test.request)
			assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)
			apply, ok := response.(ipc.ApplyManagedPlanResponse)
			if !ok {
				t.Fatalf("response type = %T, want ApplyManagedPlanResponse", response)
			}
			if apply.Domain() != test.wire {
				t.Fatalf("response domain = %q, want %q", apply.Domain(), test.wire)
			}
			if backend.lastPlan == nil || backend.lastPlan.Domain() != test.domain ||
				backend.lastPlan.BasisSnapshotDigest() != basis ||
				backend.lastPlan.Capabilities().Backend() != capabilities.Backend() {
				t.Fatal("executor did not dispatch the freshly authorized plan")
			}
			wantCalls := []string{"probe", "snapshot", "apply:" + string(test.domain)}
			if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, wantCalls)
			}
			if backend.probeContext != ctx || backend.snapshotContext != ctx || backend.mutationContext != ctx {
				t.Fatal("executor did not propagate the same caller context through the fresh attempt")
			}
		})
	}
}

func TestMutationExecutorRemovalNoopAndResidue(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	request := ipc.NewRemoveManagedInfrastructureRequest()

	t.Run("completely absent", func(t *testing.T) {
		backend := &scriptedMutationBackend{
			capabilities: capabilities,
			snapshot:     bridgeSnapshot(t, capabilities.Backend(), false, false),
		}
		executor, err := enforcer.NewMutationExecutor(backend)
		if err != nil {
			t.Fatal(err)
		}
		response := executor.Execute(context.Background(), request)
		assertExecutorResponse(t, response, ipc.OperationRemoveManagedInfrastructure, ipc.MutationStatusConfirmed, "", false)
		if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe", "snapshot"}) {
			t.Fatalf("calls = %v, want side-effect-free observation only", calls)
		}
	})

	t.Run("partial residue", func(t *testing.T) {
		backend := &scriptedMutationBackend{
			capabilities: capabilities,
			snapshot:     bridgeResidualSnapshot(t, false, true),
		}
		backend.removeResult = func(authorization firewall.RemovalAuthorization) firewall.MutationResult {
			result, err := firewall.NewConfirmedMutationResult(authorization)
			if err != nil {
				panic(err)
			}
			return result
		}
		executor, err := enforcer.NewMutationExecutor(backend)
		if err != nil {
			t.Fatal(err)
		}
		response := executor.Execute(context.Background(), request)
		assertExecutorResponse(t, response, ipc.OperationRemoveManagedInfrastructure, ipc.MutationStatusConfirmed, "", false)
		if backend.lastRemoval == nil || backend.lastRemoval.BasisSnapshotDigest() != backend.snapshot.Digest() {
			t.Fatal("partial residue did not dispatch snapshot-scoped removal")
		}
		if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe", "snapshot", "remove"}) {
			t.Fatalf("calls = %v, want one removal", calls)
		}
	})

	t.Run("partial residue without post-state proof", func(t *testing.T) {
		backend := &scriptedMutationBackend{
			capabilities: capabilities,
			snapshot:     bridgeResidualSnapshot(t, true, false),
		}
		executor, err := enforcer.NewMutationExecutor(backend)
		if err != nil {
			t.Fatal(err)
		}
		response := executor.Execute(context.Background(), request)
		assertExecutorResponse(t, response, ipc.OperationRemoveManagedInfrastructure, ipc.MutationStatusUnknown, ipc.MutationErrorCodeUnknownResult, true)
		if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe", "snapshot", "remove"}) {
			t.Fatalf("calls = %v, want one uncertain removal", calls)
		}
	})
}

func TestMutationExecutorObservationAuthorizationAndCancellationFailures(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	notReadyCapabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend: firewall.BackendKindNftablesNative, ToolVersion: "test-not-ready",
		IPv4: true, IPv6: true, CIDR: true, NativeSet: true,
		NativeTimeout: true, CrashSafeExpiry: true, AtomicBatch: true,
		HostInput: true, Forward: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	request, err := ipc.NewApplyInfrastructureRequest(snapshot.Digest(), 1)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		backend   *scriptedMutationBackend
		request   ipc.MutationRequest
		wantCode  ipc.MutationErrorCode
		wantCalls []string
	}{
		{
			name: "probe unsupported", backend: &scriptedMutationBackend{
				capabilities: capabilities, snapshot: snapshot,
				probeErr: enforcer.ErrMutationBackendUnsupported,
			},
			request: request, wantCode: ipc.MutationErrorCodeUnsupported, wantCalls: []string{"probe"},
		},
		{
			name: "probe raw error is redacted", backend: &scriptedMutationBackend{
				capabilities: capabilities, snapshot: snapshot,
				probeErr: errors.New("nft command exposed a physical object"),
			},
			request: request, wantCode: ipc.MutationErrorCodeNotReady, wantCalls: []string{"probe"},
		},
		{
			name: "probe not ready", backend: &scriptedMutationBackend{
				capabilities: capabilities, snapshot: snapshot,
				probeErr: enforcer.ErrMutationBackendNotReady,
			},
			request: request, wantCode: ipc.MutationErrorCodeNotReady, wantCalls: []string{"probe"},
		},
		{
			name: "snapshot ownership conflict", backend: &scriptedMutationBackend{
				capabilities: capabilities, snapshot: snapshot,
				snapshotErr: firewall.NewOwnershipConflictError(),
			},
			request: request, wantCode: ipc.MutationErrorCodeOwnershipConflict, wantCalls: []string{"probe", "snapshot"},
		},
		{
			name: "invalid capability value", backend: &scriptedMutationBackend{
				snapshot: snapshot,
			},
			request: request, wantCode: ipc.MutationErrorCodeNotReady, wantCalls: []string{"probe"},
		},
		{
			name: "valid but mutation-not-ready capability stops before snapshot", backend: &scriptedMutationBackend{
				capabilities: notReadyCapabilities, snapshot: snapshot,
			},
			request: request, wantCode: ipc.MutationErrorCodeNotReady, wantCalls: []string{"probe"},
		},
		{
			name: "invalid snapshot value", backend: &scriptedMutationBackend{
				capabilities: capabilities,
			},
			request: request, wantCode: ipc.MutationErrorCodeNotReady, wantCalls: []string{"probe", "snapshot"},
		},
		{
			name: "stale basis", backend: &scriptedMutationBackend{
				capabilities: capabilities, snapshot: snapshot,
			},
			request:  mustInfrastructureRequest(t, strings.Repeat("a", 64), 1),
			wantCode: ipc.MutationErrorCodeNotReady, wantCalls: []string{"probe", "snapshot"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, err := enforcer.NewMutationExecutor(test.backend)
			if err != nil {
				t.Fatal(err)
			}
			response := executor.Execute(context.Background(), test.request)
			assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusRejected, test.wantCode, true)
			if calls := test.backend.recordedCalls(); !reflect.DeepEqual(calls, test.wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, test.wantCalls)
			}
		})
	}

	t.Run("pre-canceled request performs no backend work", func(t *testing.T) {
		backend := &scriptedMutationBackend{capabilities: capabilities, snapshot: snapshot}
		executor, err := enforcer.NewMutationExecutor(backend)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		response := executor.Execute(ctx, request)
		assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusRejected, ipc.MutationErrorCodeNotReady, true)
		if calls := backend.recordedCalls(); len(calls) != 0 {
			t.Fatalf("pre-canceled request called backend: %v", calls)
		}
	})

	t.Run("uncorrelatable request performs no backend work", func(t *testing.T) {
		backend := &scriptedMutationBackend{capabilities: capabilities, snapshot: snapshot}
		executor, err := enforcer.NewMutationExecutor(backend)
		if err != nil {
			t.Fatal(err)
		}
		if response := executor.Execute(context.Background(), nil); response != nil {
			t.Fatalf("response = %T, want nil", response)
		}
		if calls := backend.recordedCalls(); len(calls) != 0 {
			t.Fatalf("uncorrelatable request called backend: %v", calls)
		}
		for _, valid := range []ipc.MutationRequest{request, ipc.NewRemoveManagedInfrastructureRequest()} {
			typedNil := reflect.Zero(reflect.TypeOf(valid)).Interface().(ipc.MutationRequest)
			if response := executor.Execute(context.Background(), typedNil); response != nil {
				t.Fatalf("typed-nil %T response = %T, want nil", valid, response)
			}
		}
		if calls := backend.recordedCalls(); len(calls) != 0 {
			t.Fatalf("typed-nil request called backend: %v", calls)
		}
	})

	t.Run("cancellation after probe stops before snapshot", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		backend := &scriptedMutationBackend{capabilities: capabilities, snapshot: snapshot, probeHook: cancel}
		executor, err := enforcer.NewMutationExecutor(backend)
		if err != nil {
			t.Fatal(err)
		}
		response := executor.Execute(ctx, request)
		assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusRejected, ipc.MutationErrorCodeNotReady, true)
		if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe"}) {
			t.Fatalf("calls after cancellation = %v, want probe only", calls)
		}
	})

	t.Run("cancellation after snapshot stops before authorization and mutation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		backend := &scriptedMutationBackend{capabilities: capabilities, snapshot: snapshot, snapshotHook: cancel}
		executor, err := enforcer.NewMutationExecutor(backend)
		if err != nil {
			t.Fatal(err)
		}
		response := executor.Execute(ctx, request)
		assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusRejected, ipc.MutationErrorCodeNotReady, true)
		if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe", "snapshot"}) {
			t.Fatalf("calls after cancellation = %v, want observation only", calls)
		}
	})

	if executor, err := enforcer.NewMutationExecutor(nil); !errors.Is(err, enforcer.ErrMutationBackendRequired) || executor != nil {
		t.Fatalf("nil backend constructor = (%v, %v)", executor, err)
	}
	var typedNilBackend *scriptedMutationBackend
	if executor, err := enforcer.NewMutationExecutor(typedNilBackend); !errors.Is(err, enforcer.ErrMutationBackendRequired) || executor != nil {
		t.Fatalf("typed-nil backend constructor = (%v, %v)", executor, err)
	}
}

func TestMutationExecutorMapsResultCertaintyAndCorrelatesUnknown(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	request := mustInfrastructureRequest(t, snapshot.Digest(), 1)
	otherRequest := mustInfrastructureRequest(t, snapshot.Digest(), 2)
	otherAuthorization, err := enforcer.AuthorizeMutationRequest(otherRequest, capabilities, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	otherMutation, ok := otherAuthorization.Mutation()
	if !ok {
		t.Fatal("missing mismatched authority fixture")
	}
	wrongResult, err := firewall.NewConfirmedMutationResult(otherMutation)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		result     func(firewall.OperationPlan) firewall.MutationResult
		wantStatus ipc.MutationStatus
		wantCode   ipc.MutationErrorCode
		wantCodeOK bool
	}{
		{
			name: "confirmed",
			result: func(plan firewall.OperationPlan) firewall.MutationResult {
				result, err := firewall.NewConfirmedMutationResult(plan)
				if err != nil {
					panic(err)
				}
				return result
			},
			wantStatus: ipc.MutationStatusConfirmed,
		},
		{
			name: "proven rejected",
			result: func(plan firewall.OperationPlan) firewall.MutationResult {
				result, err := firewall.NewRejectedMutationResult(plan, firewall.MutationErrorBackendRejected)
				if err != nil {
					panic(err)
				}
				return result
			},
			wantStatus: ipc.MutationStatusRejected, wantCode: ipc.MutationErrorCodeBackendRejected, wantCodeOK: true,
		},
		{
			name: "unknown",
			result: func(plan firewall.OperationPlan) firewall.MutationResult {
				result, err := firewall.NewUnknownMutationResult(plan)
				if err != nil {
					panic(err)
				}
				return result
			},
			wantStatus: ipc.MutationStatusUnknown, wantCode: ipc.MutationErrorCodeUnknownResult, wantCodeOK: true,
		},
		{
			name: "zero result becomes correlated unknown",
			result: func(firewall.OperationPlan) firewall.MutationResult {
				return firewall.MutationResult{}
			},
			wantStatus: ipc.MutationStatusUnknown, wantCode: ipc.MutationErrorCodeUnknownResult, wantCodeOK: true,
		},
		{
			name: "mismatched result becomes correlated unknown",
			result: func(firewall.OperationPlan) firewall.MutationResult {
				return wrongResult
			},
			wantStatus: ipc.MutationStatusUnknown, wantCode: ipc.MutationErrorCodeUnknownResult, wantCodeOK: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &scriptedMutationBackend{
				capabilities: capabilities,
				snapshot:     snapshot,
				applyResult:  test.result,
			}
			executor, err := enforcer.NewMutationExecutor(backend)
			if err != nil {
				t.Fatal(err)
			}
			response := executor.Execute(context.Background(), request)
			assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, test.wantStatus, test.wantCode, test.wantCodeOK)
			if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe", "snapshot", "apply:infrastructure"}) {
				t.Fatalf("calls = %v, want exactly one mutation", calls)
			}
		})
	}

	t.Run("cancellation after dispatch with no proof becomes unknown", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		backend := &scriptedMutationBackend{
			capabilities: capabilities,
			snapshot:     snapshot,
			applyHook:    cancel,
		}
		executor, err := enforcer.NewMutationExecutor(backend)
		if err != nil {
			t.Fatal(err)
		}
		response := executor.Execute(ctx, request)
		assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusUnknown, ipc.MutationErrorCodeUnknownResult, true)
		if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe", "snapshot", "apply:infrastructure"}) {
			t.Fatalf("calls = %v, want exactly one dispatched mutation", calls)
		}
	})

	t.Run("correlated result wins after dispatch cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		backend := &scriptedMutationBackend{
			capabilities: capabilities,
			snapshot:     snapshot,
			applyHook:    cancel,
			applyResult: func(plan firewall.OperationPlan) firewall.MutationResult {
				result, resultErr := firewall.NewConfirmedMutationResult(plan)
				if resultErr != nil {
					panic(resultErr)
				}
				return result
			},
		}
		executor, err := enforcer.NewMutationExecutor(backend)
		if err != nil {
			t.Fatal(err)
		}
		response := executor.Execute(ctx, request)
		assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)
	})
}

func TestMutationExecutorDoesNotReuseObservationBetweenRequests(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	firstSnapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	secondSnapshot := bridgeSnapshot(t, capabilities.Backend(), true, false)
	backend := &scriptedMutationBackend{
		capabilities: capabilities,
		snapshot:     firstSnapshot,
		applyResult: func(plan firewall.OperationPlan) firewall.MutationResult {
			result, err := firewall.NewConfirmedMutationResult(plan)
			if err != nil {
				panic(err)
			}
			return result
		},
	}
	executor, err := enforcer.NewMutationExecutor(backend)
	if err != nil {
		t.Fatal(err)
	}

	first := executor.Execute(context.Background(), mustInfrastructureRequest(t, firstSnapshot.Digest(), 1))
	assertExecutorResponse(t, first, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)
	backend.setSnapshot(secondSnapshot)
	second := executor.Execute(context.Background(), mustInfrastructureRequest(t, secondSnapshot.Digest(), 2))
	assertExecutorResponse(t, second, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)

	if backend.lastPlan == nil || backend.lastPlan.BasisSnapshotDigest() != secondSnapshot.Digest() {
		t.Fatal("second request did not use its fresh snapshot authority")
	}
	wantCalls := []string{
		"probe", "snapshot", "apply:infrastructure",
		"probe", "snapshot", "apply:infrastructure",
	}
	if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %v, want fresh observation per request %v", calls, wantCalls)
	}
}

func TestMutationExecutorSerializesWholeFreshAttempt(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	probeEntered := make(chan int, 2)
	backend := &scriptedMutationBackend{
		capabilities: capabilities,
		snapshot:     snapshot,
		probeEntered: probeEntered,
		applyEntered: make(chan struct{}),
		applyRelease: make(chan struct{}),
	}
	backend.applyResult = func(plan firewall.OperationPlan) firewall.MutationResult {
		result, err := firewall.NewConfirmedMutationResult(plan)
		if err != nil {
			panic(err)
		}
		return result
	}
	executor, err := enforcer.NewMutationExecutor(backend)
	if err != nil {
		t.Fatal(err)
	}
	request := mustInfrastructureRequest(t, snapshot.Digest(), 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(backend.applyRelease) }) }
	defer release()

	responses := make(chan ipc.MutationResponse, 2)
	go func() { responses <- executor.Execute(context.Background(), request) }()
	select {
	case <-backend.applyEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first mutation did not reach the backend")
	}
	if call := <-probeEntered; call != 1 {
		t.Fatalf("first Probe call number = %d, want 1", call)
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		responses <- executor.Execute(context.Background(), request)
	}()
	<-secondStarted
	select {
	case call := <-probeEntered:
		t.Fatalf("Probe call %d entered while the first full attempt was blocked", call)
	case <-time.After(100 * time.Millisecond):
	}
	release()

	for range 2 {
		select {
		case response := <-responses:
			assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)
		case <-time.After(5 * time.Second):
			t.Fatal("serialized mutation did not complete")
		}
	}
	select {
	case call := <-probeEntered:
		if call != 2 {
			t.Fatalf("second Probe call number = %d, want 2", call)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second serialized Probe did not run after release")
	}
	if got := backend.maxActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent mutations = %d, want 1", got)
	}
	wantCalls := []string{
		"probe", "snapshot", "apply:infrastructure",
		"probe", "snapshot", "apply:infrastructure",
	}
	if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %v, want serialized fresh attempts %v", calls, wantCalls)
	}
}

func TestMutationExecutorWaitingAdmissionIsContextAware(t *testing.T) {
	capabilities := bridgeCapabilities(t)
	snapshot := bridgeSnapshot(t, capabilities.Backend(), true, true)
	backend := &scriptedMutationBackend{
		capabilities: capabilities,
		snapshot:     snapshot,
		applyEntered: make(chan struct{}),
		applyRelease: make(chan struct{}),
	}
	backend.applyResult = func(plan firewall.OperationPlan) firewall.MutationResult {
		result, err := firewall.NewConfirmedMutationResult(plan)
		if err != nil {
			panic(err)
		}
		return result
	}
	executor, err := enforcer.NewMutationExecutor(backend)
	if err != nil {
		t.Fatal(err)
	}
	request := mustInfrastructureRequest(t, snapshot.Digest(), 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(backend.applyRelease) }) }
	defer release()

	firstResponse := make(chan ipc.MutationResponse, 1)
	go func() { firstResponse <- executor.Execute(context.Background(), request) }()
	select {
	case <-backend.applyEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first mutation did not reach the backend")
	}

	waitingCtx, cancelWaiting := context.WithCancel(context.Background())
	waitingResponse := make(chan ipc.MutationResponse, 1)
	waitingStarted := make(chan struct{})
	go func() {
		close(waitingStarted)
		waitingResponse <- executor.Execute(waitingCtx, request)
	}()
	<-waitingStarted
	cancelWaiting()
	select {
	case response := <-waitingResponse:
		assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusRejected, ipc.MutationErrorCodeNotReady, true)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiting request did not return before the active attempt was released")
	}
	if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, []string{"probe", "snapshot", "apply:infrastructure"}) {
		t.Fatalf("waiting cancellation called backend: %v", calls)
	}

	release()
	select {
	case response := <-firstResponse:
		assertExecutorResponse(t, response, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)
	case <-time.After(5 * time.Second):
		t.Fatal("active mutation did not finish after release")
	}

	third := executor.Execute(context.Background(), request)
	assertExecutorResponse(t, third, ipc.OperationApplyManagedPlan, ipc.MutationStatusConfirmed, "", false)
	wantCalls := []string{
		"probe", "snapshot", "apply:infrastructure",
		"probe", "snapshot", "apply:infrastructure",
	}
	if calls := backend.recordedCalls(); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls after gate reuse = %v, want %v", calls, wantCalls)
	}
}

type scriptedMutationBackend struct {
	mu sync.Mutex

	capabilities    firewall.FirewallCapabilities
	snapshot        firewall.ManagedSnapshot
	probeErr        error
	snapshotErr     error
	calls           []string
	lastPlan        firewall.OperationPlan
	lastRemoval     firewall.RemovalAuthorization
	probeContext    context.Context
	snapshotContext context.Context
	mutationContext context.Context

	applyResult  func(firewall.OperationPlan) firewall.MutationResult
	removeResult func(firewall.RemovalAuthorization) firewall.MutationResult
	probeHook    func()
	snapshotHook func()
	applyHook    func()
	probeEntered chan int
	probeCalls   int
	applyEntered chan struct{}
	applyRelease chan struct{}
	enteredOnce  sync.Once
	active       atomic.Int32
	maxActive    atomic.Int32
}

func (b *scriptedMutationBackend) Probe(ctx context.Context) (firewall.FirewallCapabilities, error) {
	b.mu.Lock()
	b.probeContext = ctx
	b.calls = append(b.calls, "probe")
	b.probeCalls++
	call := b.probeCalls
	b.mu.Unlock()
	if b.probeEntered != nil {
		b.probeEntered <- call
	}
	if b.probeHook != nil {
		b.probeHook()
	}
	return b.capabilities, b.probeErr
}

func (b *scriptedMutationBackend) Snapshot(ctx context.Context) (firewall.ManagedSnapshot, error) {
	b.mu.Lock()
	b.snapshotContext = ctx
	b.calls = append(b.calls, "snapshot")
	b.mu.Unlock()
	if b.snapshotHook != nil {
		b.snapshotHook()
	}
	return b.snapshot, b.snapshotErr
}

func (b *scriptedMutationBackend) Apply(ctx context.Context, plan firewall.OperationPlan) firewall.MutationResult {
	b.mu.Lock()
	b.lastPlan = plan
	b.mutationContext = ctx
	b.calls = append(b.calls, "apply:"+string(plan.Domain()))
	b.mu.Unlock()
	b.enterMutation()
	defer b.leaveMutation()
	if b.applyHook != nil {
		b.applyHook()
	}
	if b.applyResult == nil {
		return firewall.MutationResult{}
	}
	return b.applyResult(plan)
}

func (b *scriptedMutationBackend) RemoveManagedInfrastructure(
	ctx context.Context,
	authorization firewall.RemovalAuthorization,
) firewall.MutationResult {
	b.mu.Lock()
	b.lastRemoval = authorization
	b.mutationContext = ctx
	b.calls = append(b.calls, "remove")
	b.mu.Unlock()
	b.enterMutation()
	defer b.leaveMutation()
	if b.removeResult == nil {
		return firewall.MutationResult{}
	}
	return b.removeResult(authorization)
}

func (b *scriptedMutationBackend) recordedCalls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

func (b *scriptedMutationBackend) setSnapshot(snapshot firewall.ManagedSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.snapshot = snapshot
}

func (b *scriptedMutationBackend) enterMutation() {
	active := b.active.Add(1)
	for {
		maximum := b.maxActive.Load()
		if active <= maximum || b.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	if b.applyEntered != nil {
		b.enteredOnce.Do(func() { close(b.applyEntered) })
	}
	if b.applyRelease != nil {
		<-b.applyRelease
	}
}

func (b *scriptedMutationBackend) leaveMutation() { b.active.Add(-1) }

func mustInfrastructureRequest(t *testing.T, basis string, revision int64) ipc.ApplyManagedPlanRequest {
	t.Helper()
	request, err := ipc.NewApplyInfrastructureRequest(basis, revision)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func assertExecutorResponse(
	t *testing.T,
	response ipc.MutationResponse,
	operation ipc.Operation,
	status ipc.MutationStatus,
	code ipc.MutationErrorCode,
	hasCode bool,
) {
	t.Helper()
	if response == nil || response.Operation() != operation || response.Status() != status {
		if response == nil {
			t.Fatalf("response=nil, want operation=%q status=%q", operation, status)
		}
		t.Fatalf("response=%T operation=%q status=%q, want operation=%q status=%q", response, response.Operation(), response.Status(), operation, status)
	}
	gotCode, gotHasCode := response.ErrorCode()
	if gotCode != code || gotHasCode != hasCode {
		t.Fatalf("response error code = (%q, %v), want (%q, %v)", gotCode, gotHasCode, code, hasCode)
	}
}

var _ enforcer.MutationBackend = (*scriptedMutationBackend)(nil)
