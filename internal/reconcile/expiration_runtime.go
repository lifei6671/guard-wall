package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ExpirationRuntimeScheduler separates the no-wakeup startup sweep from the
// recurring scheduler so Dispatcher recovery sees the committed Desired state
// before any Firewall mutation begins.
type ExpirationRuntimeScheduler interface {
	PrepareExpirationStartup(context.Context) (time.Time, error)
	RunExpirationSchedulerAfterStartup(context.Context, time.Time) error
}

// ExpirationRuntime owns the coupled lifecycle of one expiration scheduler and
// one node-local Dispatcher.
type ExpirationRuntime struct {
	scheduler  ExpirationRuntimeScheduler
	dispatcher *Dispatcher
}

// NewExpirationRuntime constructs the expiration-to-enforcement runtime owner.
func NewExpirationRuntime(
	scheduler ExpirationRuntimeScheduler,
	dispatcher *Dispatcher,
) (*ExpirationRuntime, error) {
	if scheduler == nil {
		return nil, fmt.Errorf("expiration runtime scheduler is required")
	}
	if dispatcher == nil {
		return nil, fmt.Errorf("expiration runtime dispatcher is required")
	}
	return &ExpirationRuntime{scheduler: scheduler, dispatcher: dispatcher}, nil
}

// Run prepares startup expiration synchronously, then owns both recurring
// loops until cancellation or the first component failure.
func (r *ExpirationRuntime) Run(ctx context.Context) error {
	if r == nil || r.scheduler == nil || r.dispatcher == nil {
		return fmt.Errorf("expiration runtime is not initialized")
	}
	if ctx == nil {
		return fmt.Errorf("expiration runtime context is required")
	}
	startupSweepStartedAt, err := r.scheduler.PrepareExpirationStartup(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("prepare expiration runtime startup: %w", err)
	}
	if startupSweepStartedAt.IsZero() {
		return fmt.Errorf("prepare expiration runtime startup: sweep time is required")
	}
	if ctx.Err() != nil {
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan expirationRuntimeResult, 2)
	go func() {
		results <- expirationRuntimeResult{
			component: "expiration scheduler",
			err:       r.scheduler.RunExpirationSchedulerAfterStartup(runCtx, startupSweepStartedAt),
		}
	}()
	go func() {
		results <- expirationRuntimeResult{component: "reconcile dispatcher", err: r.dispatcher.Run(runCtx)}
	}()

	select {
	case <-ctx.Done():
		cancel()
		first := <-results
		second := <-results
		return expirationRuntimeShutdownError(first, second)
	case first := <-results:
		cancel()
		second := <-results
		if ctx.Err() != nil {
			return expirationRuntimeShutdownError(first, second)
		}
		return expirationRuntimeFailure(first, second)
	}
}

type expirationRuntimeResult struct {
	component string
	err       error
}

func expirationRuntimeShutdownError(results ...expirationRuntimeResult) error {
	errs := make([]error, 0, len(results))
	for _, result := range results {
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			errs = append(errs, fmt.Errorf("%s failed during shutdown: %w", result.component, result.err))
		}
	}
	return errors.Join(errs...)
}

func expirationRuntimeFailure(first, second expirationRuntimeResult) error {
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
