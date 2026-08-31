package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lifei6671/guard-wall/internal/source"
)

// RunSourceRuntime owns one Source processing loop through graceful shutdown.
// The caller transfers database close ownership for the duration of the run.
// If the shutdown deadline expires before the worker exits, the function
// returns context.DeadlineExceeded without closing a database the worker may
// still be using; the process owner must then exit without reusing it.
func RunSourceRuntime(
	ctx context.Context,
	shutdownTimeout time.Duration,
	queue *source.DeliveryQueue,
	coordinator *Coordinator,
	checkpoints *source.CheckpointManager,
	database io.Closer,
) error {
	if ctx == nil {
		return fmt.Errorf("run source runtime: context is required")
	}
	if shutdownTimeout <= 0 {
		return fmt.Errorf("run source runtime: shutdown timeout must be positive")
	}
	if queue == nil || coordinator == nil || checkpoints == nil || database == nil {
		return fmt.Errorf("run source runtime: queue, coordinator, checkpoints, and database are required")
	}

	workerCtx, cancelWorker := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWorker()
	workerResult := make(chan error, 1)
	go func() {
		workerResult <- runSourceWorker(workerCtx, queue, coordinator, checkpoints)
	}()

	select {
	case workerErr := <-workerResult:
		queue.Seal()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancelShutdown()
		return finishSourceRuntime(shutdownCtx, checkpoints, database, workerErr)
	case <-ctx.Done():
		shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancelShutdown()
		stopWorker := context.AfterFunc(shutdownCtx, cancelWorker)
		defer stopWorker()
		queue.Seal()

		select {
		case workerErr := <-workerResult:
			if err := shutdownCtx.Err(); err != nil {
				return errors.Join(
					fmt.Errorf("run source runtime: shutdown timeout: %w", err),
					workerErr,
				)
			}
			return finishSourceRuntime(shutdownCtx, checkpoints, database, workerErr)
		case <-shutdownCtx.Done():
			cancelWorker()
			return fmt.Errorf("run source runtime: shutdown timeout: %w", shutdownCtx.Err())
		}
	}
}

func runSourceWorker(
	ctx context.Context,
	queue *source.DeliveryQueue,
	coordinator *Coordinator,
	checkpoints *source.CheckpointManager,
) error {
	for {
		delivery, err := queue.Dequeue(ctx)
		if errors.Is(err, source.ErrDeliveryQueueSealed) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("dequeue delivery: %w", err)
		}

		completion, err := coordinator.Process(ctx, delivery)
		if err != nil {
			return fmt.Errorf("process delivery %q: %w", delivery.ID, err)
		}
		if err := checkpoints.Complete(ctx, completion); err != nil {
			return fmt.Errorf("complete delivery %q: %w", delivery.ID, err)
		}
	}
}

func finishSourceRuntime(
	ctx context.Context,
	checkpoints *source.CheckpointManager,
	database io.Closer,
	workerErr error,
) error {
	flushErr := checkpoints.Flush(ctx)
	closeErr := database.Close()
	return errors.Join(
		workerErr,
		wrapSourceRuntimeError("flush checkpoint", flushErr),
		wrapSourceRuntimeError("close database", closeErr),
	)
}

func wrapSourceRuntimeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("run source runtime: %s: %w", operation, err)
}
