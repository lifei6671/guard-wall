package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const backendHealthSourcePollInterval = time.Second

// BackendHealthProber is the narrow availability boundary used by the runtime
// health source. It must not perform a managed-state read or a mutation.
type BackendHealthProber interface {
	ProbeHealth(context.Context) error
}

type backendHealthRecoveryObserver interface {
	BackendHealthy(context.Context) (int, error)
}

// BackendHealthSource polls a narrow Backend availability probe. It reports
// only an unavailable-to-available transition; Dispatcher then performs the
// authoritative recovery Probe and decides whether any key may be woken.
type BackendHealthSource struct {
	prober   BackendHealthProber
	observer backendHealthRecoveryObserver
	interval time.Duration
	timeout  time.Duration
}

func newBackendHealthSource(
	prober BackendHealthProber,
	observer backendHealthRecoveryObserver,
	interval time.Duration,
) (*BackendHealthSource, error) {
	if prober == nil {
		return nil, fmt.Errorf("Backend health prober is required")
	}
	if observer == nil {
		return nil, fmt.Errorf("Backend health recovery observer is required")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("Backend health source poll interval must be positive")
	}
	return &BackendHealthSource{
		prober: prober, observer: observer, interval: interval, timeout: backendHealthProbeTimeout,
	}, nil
}

// Run owns one polling loop until cancellation. Probe failures are expected
// availability observations, not component failures. The source has no
// authority to wake, mutate, or alter retry state directly.
func (s *BackendHealthSource) Run(ctx context.Context) error {
	if s == nil || s.prober == nil || s.observer == nil || s.interval <= 0 || s.timeout <= 0 {
		return fmt.Errorf("Backend health source is not initialized")
	}
	if ctx == nil {
		return fmt.Errorf("Backend health source context is required")
	}

	known := false
	reachable := false
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		if err := s.observe(ctx, &known, &reachable); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *BackendHealthSource) observe(ctx context.Context, known, reachable *bool) error {
	probeCtx, cancel := context.WithTimeout(ctx, s.timeout)
	err := s.prober.ProbeHealth(probeCtx)
	cancel()
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		*known = true
		*reachable = false
		return nil
	}
	if *known && !*reachable {
		_, notifyErr := s.observer.BackendHealthy(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var unavailable *backendHealthUnavailableError
		if errors.Is(notifyErr, ErrDispatcherStopped) {
			return notifyErr
		}
		if notifyErr != nil && !errors.As(notifyErr, &unavailable) {
			return notifyErr
		}
	}
	*known = true
	*reachable = true
	return nil
}
