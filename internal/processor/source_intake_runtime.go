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
// maintenance 在成功排空并保存 checkpoint 后、关闭数据库前同步执行；nil 跳过。
// 它共享剩余 shutdown deadline，须配合取消并在返回前结束自身数据库使用。
// 超时返回时不关闭数据库，进程所有者必须退出且不得复用；本回调不证明跨运行排他。
func RunSourceIntakeRuntime(
	ctx context.Context,
	shutdownTimeout time.Duration,
	reader source.Reader,
	queue *source.DeliveryQueue,
	coordinator *Coordinator,
	checkpoints *source.CheckpointManager,
	database io.Closer,
	maintenance func(context.Context) error,
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
			maintenance,
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
			maintenance,
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
			maintenance,
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
	maintenance func(context.Context) error,
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
			maintenance,
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
	maintenance func(context.Context) error,
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
	return finishSourceIntakeRuntime(ctx, checkpoints, database, initialErr, maintenance)
}

func finishSourceIntakeRuntime(
	ctx context.Context,
	checkpoints *source.CheckpointManager,
	database io.Closer,
	initialErr error,
	maintenance func(context.Context) error,
) error {
	if err := ctx.Err(); err != nil {
		return sourceIntakeShutdownTimeout(initialErr)
	}
	flushErr := checkpoints.Flush(ctx)
	if ctx.Err() != nil {
		return sourceIntakeShutdownTimeout(errors.Join(initialErr, wrapSourceRuntimeError("flush checkpoint", flushErr)))
	}
	var maintenanceErr error
	if initialErr == nil && flushErr == nil && maintenance != nil {
		maintenanceErr = maintenance(ctx)
		if ctx.Err() != nil {
			return sourceIntakeShutdownTimeout(wrapSourceRuntimeError("maintenance", maintenanceErr))
		}
	}
	closeErr := database.Close()
	return errors.Join(
		initialErr,
		wrapSourceRuntimeError("flush checkpoint", flushErr),
		wrapSourceRuntimeError("maintenance", maintenanceErr),
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
