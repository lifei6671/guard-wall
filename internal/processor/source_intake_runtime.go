package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/source"
)

// RunSourceIntakeRuntime composes one Source reader with the processing
// runtime. Shutdown always stops the reader before sealing the accepted queue,
// then drains the worker, flushes checkpoints, and closes the database.
func RunSourceIntakeRuntime(
	ctx context.Context,
	shutdownTimeout time.Duration,
	reader source.Reader,
	queue *source.DeliveryQueue,
	coordinator *Coordinator,
	checkpoints *source.CheckpointManager,
	database io.Closer,
) error {
	if ctx == nil {
		return fmt.Errorf("run source intake runtime: context is required")
	}
	if shutdownTimeout <= 0 {
		return fmt.Errorf("run source intake runtime: shutdown timeout must be positive")
	}
	if sourceReaderIsNil(reader) {
		return fmt.Errorf("run source intake runtime: reader is required")
	}
	if queue == nil || coordinator == nil || checkpoints == nil || database == nil {
		return fmt.Errorf("run source intake runtime: queue, coordinator, checkpoints, and database are required")
	}

	readerCtx, cancelReader := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelReader()
	workerCtx, cancelWorker := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWorker()

	readerResult := make(chan error, 1)
	go func() {
		readerResult <- reader.Read(readerCtx, sourceIntakeDeliverySink{queue: queue})
	}()
	workerResult := make(chan error, 1)
	go func() {
		workerResult <- runSourceWorker(workerCtx, queue, coordinator, checkpoints)
	}()

	select {
	case readerErr := <-readerResult:
		shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancelShutdown()
		return drainSourceIntakeRuntime(
			shutdownCtx,
			queue,
			checkpoints,
			database,
			workerResult,
			readerErr,
		)
	case workerErr := <-workerResult:
		return stopReaderAndFinishSourceIntakeRuntime(
			ctx,
			shutdownTimeout,
			cancelReader,
			readerResult,
			queue,
			checkpoints,
			database,
			nil,
			workerErr,
		)
	case <-ctx.Done():
		return stopReaderAndFinishSourceIntakeRuntime(
			ctx,
			shutdownTimeout,
			cancelReader,
			readerResult,
			queue,
			checkpoints,
			database,
			workerResult,
			nil,
		)
	}
}

func stopReaderAndFinishSourceIntakeRuntime(
	ctx context.Context,
	shutdownTimeout time.Duration,
	cancelReader context.CancelFunc,
	readerResult <-chan error,
	queue *source.DeliveryQueue,
	checkpoints *source.CheckpointManager,
	database io.Closer,
	workerResult <-chan error,
	workerErr error,
) error {
	shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancelShutdown()
	cancelReader()

	select {
	case readerErr := <-readerResult:
		if sourceIntakeExpectedCancellation(readerErr) {
			readerErr = nil
		}
		return drainSourceIntakeRuntime(
			shutdownCtx,
			queue,
			checkpoints,
			database,
			workerResult,
			errors.Join(workerErr, readerErr),
		)
	case <-shutdownCtx.Done():
		return sourceIntakeShutdownTimeout(workerErr)
	}
}

func drainSourceIntakeRuntime(
	ctx context.Context,
	queue *source.DeliveryQueue,
	checkpoints *source.CheckpointManager,
	database io.Closer,
	workerResult <-chan error,
	initialErr error,
) error {
	queue.Seal()
	if workerResult != nil {
		select {
		case workerErr := <-workerResult:
			initialErr = errors.Join(initialErr, workerErr)
		case <-ctx.Done():
			return sourceIntakeShutdownTimeout(initialErr)
		}
	}
	return finishSourceIntakeRuntime(ctx, checkpoints, database, initialErr)
}

func finishSourceIntakeRuntime(
	ctx context.Context,
	checkpoints *source.CheckpointManager,
	database io.Closer,
	initialErr error,
) error {
	if err := ctx.Err(); err != nil {
		return sourceIntakeShutdownTimeout(initialErr)
	}
	flushErr := checkpoints.Flush(ctx)
	if ctx.Err() != nil {
		return sourceIntakeShutdownTimeout(errors.Join(initialErr, wrapSourceRuntimeError("flush checkpoint", flushErr)))
	}
	closeErr := database.Close()
	return errors.Join(
		initialErr,
		wrapSourceRuntimeError("flush checkpoint", flushErr),
		wrapSourceRuntimeError("close database", closeErr),
	)
}

func sourceIntakeShutdownTimeout(err error) error {
	return errors.Join(
		err,
		fmt.Errorf("run source intake runtime: shutdown timeout: %w", context.DeadlineExceeded),
	)
}

func sourceReaderIsNil(reader source.Reader) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func sourceIntakeExpectedCancellation(err error) bool {
	if err == context.Canceled {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errors := joined.Unwrap()
		if len(errors) == 0 {
			return false
		}
		for _, joinedErr := range errors {
			if !sourceIntakeExpectedCancellation(joinedErr) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return sourceIntakeExpectedCancellation(wrapped.Unwrap())
	}
	return false
}

type sourceIntakeDeliverySink struct {
	queue *source.DeliveryQueue
}

func (s sourceIntakeDeliverySink) Deliver(ctx context.Context, delivery core.Delivery) error {
	if err := delivery.Validate(); err != nil {
		return fmt.Errorf("deliver source input: %w", err)
	}
	if err := s.queue.Enqueue(ctx, delivery); err != nil {
		return fmt.Errorf("enqueue source delivery: %w", err)
	}
	return nil
}
