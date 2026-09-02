//go:build linux

package enforcer

import (
	"context"
	"errors"

	"github.com/lifei6671/guard-wall/internal/firewall"
	"github.com/lifei6671/guard-wall/internal/ipc"
)

// NewEnforcerHandlers composes one complete closed handler set over a single
// privileged Backend. Copies of the returned value retain shared serial
// ownership, so Probe, Snapshot, and Mutation never overlap on that Backend.
func NewEnforcerHandlers(backend MutationBackend) (ipc.EnforcerHandlers, error) {
	executor, err := NewMutationExecutor(backend)
	if err != nil {
		return ipc.EnforcerHandlers{}, err
	}
	state := &enforcerHandlerState{
		backend:  backend,
		executor: executor,
		gate:     make(chan struct{}, 1),
	}
	state.gate <- struct{}{}
	return ipc.EnforcerHandlers{
		ProbeCapabilities: state.probeCapabilities,
		SnapshotManaged:   state.snapshotManaged,
		Mutation:          state.mutation,
	}, nil
}

type enforcerHandlerState struct {
	backend  MutationBackend
	executor *MutationExecutor
	gate     chan struct{}
}

func (s *enforcerHandlerState) probeCapabilities(ctx context.Context) ipc.ProbeCapabilitiesResponse {
	if !s.acquire(ctx) {
		return newProbeFailure(ipc.ProbeCapabilitiesFailureCodeNotReady)
	}
	defer s.release()

	capabilities, err := s.backend.Probe(ctx)
	if ctx.Err() != nil {
		return newProbeFailure(ipc.ProbeCapabilitiesFailureCodeNotReady)
	}
	if err != nil {
		return newProbeFailure(probeFailureCode(err))
	}
	response, err := ipc.NewProbeCapabilitiesSuccessResponse(capabilities)
	if err != nil {
		return newProbeFailure(ipc.ProbeCapabilitiesFailureCodeNotReady)
	}
	return response
}

func (s *enforcerHandlerState) snapshotManaged(ctx context.Context) ipc.SnapshotManagedResponse {
	if !s.acquire(ctx) {
		return newSnapshotFailure(ipc.SnapshotManagedFailureCodeNotReady)
	}
	defer s.release()

	snapshot, err := s.backend.Snapshot(ctx)
	if ctx.Err() != nil {
		return newSnapshotFailure(ipc.SnapshotManagedFailureCodeNotReady)
	}
	if err != nil {
		return newSnapshotFailure(snapshotFailureCode(err))
	}
	response, err := ipc.NewSnapshotManagedSuccessResponse(snapshot)
	if err != nil {
		return newSnapshotFailure(ipc.SnapshotManagedFailureCodeNotReady)
	}
	return response
}

func (s *enforcerHandlerState) mutation(
	ctx context.Context,
	request ipc.MutationRequest,
) ipc.MutationResponse {
	if !s.acquire(ctx) {
		return rejectedMutationResponse(request, ipc.MutationErrorCodeNotReady)
	}
	defer s.release()
	return s.executor.Execute(ctx, request)
}

func (s *enforcerHandlerState) acquire(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-s.gate:
	}
	if ctx.Err() != nil {
		s.release()
		return false
	}
	return true
}

func (s *enforcerHandlerState) release() { s.gate <- struct{}{} }

func probeFailureCode(err error) ipc.ProbeCapabilitiesFailureCode {
	if errors.Is(err, ErrMutationBackendUnsupported) {
		return ipc.ProbeCapabilitiesFailureCodeUnsupported
	}
	return ipc.ProbeCapabilitiesFailureCodeNotReady
}

func snapshotFailureCode(err error) ipc.SnapshotManagedFailureCode {
	var ownershipConflict firewall.OwnershipConflictError
	switch {
	case errors.Is(err, ErrMutationBackendUnsupported):
		return ipc.SnapshotManagedFailureCodeUnsupported
	case errors.Is(err, ErrMutationBackendOwnershipConflict), errors.As(err, &ownershipConflict):
		return ipc.SnapshotManagedFailureCodeOwnershipConflict
	default:
		return ipc.SnapshotManagedFailureCodeNotReady
	}
}

func newProbeFailure(code ipc.ProbeCapabilitiesFailureCode) ipc.ProbeCapabilitiesResponse {
	response, err := ipc.NewProbeCapabilitiesFailureResponse(code)
	if err != nil {
		return nil
	}
	return response
}

func newSnapshotFailure(code ipc.SnapshotManagedFailureCode) ipc.SnapshotManagedResponse {
	response, err := ipc.NewSnapshotManagedFailureResponse(code)
	if err != nil {
		return nil
	}
	return response
}
