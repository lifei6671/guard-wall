package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/source"
)

func TestRunSourceIntakeRuntimeStopsReaderBeforeDrainAndClose(t *testing.T) {
	database, path := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	seedSQLiteProcessingCatalog(t, path)
	delivery := shutdownDelivery(t, 1, 0, 10)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}

	reader := &sourceIntakeBlockingReader{
		delivery: delivery,
		started:  make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	runner := &sourceRuntimeBlockingRunner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), runner)
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	closer := &sourceRuntimeValidatingCloser{
		database: database,
		validate: func(ctx context.Context) error {
			checkpoint, found, err := database.LoadSourceCheckpoint(ctx, "source-1")
			if err != nil {
				return err
			}
			if !found || checkpoint.DeliverySequence != delivery.Sequence {
				return fmt.Errorf("checkpoint before close = %+v, found=%v", checkpoint, found)
			}
			if _, found, err := database.FindProcessingReceipt(ctx, delivery.ID); err != nil || !found {
				return fmt.Errorf("receipt %q before close: found=%v err=%w", delivery.ID, found, err)
			}
			return nil
		},
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunSourceIntakeRuntime(runCtx, time.Second, reader, queue, coordinator, checkpoints, nil)
	}()

	waitForSourceRuntimeSignal(t, reader.started, "reader start")
	waitForSourceRuntimeSignal(t, runner.started, "worker start")
	cancelRun()
	waitForSourceRuntimeSignal(t, reader.stopped, "reader stop")
	if closer.Calls() != 0 {
		t.Fatal("database closed before reader stopped and accepted delivery drained")
	}
	close(runner.release)
	if err := waitForSourceRuntimeResult(t, result); err != nil {
		t.Fatalf("RunSourceIntakeRuntime(): %v", err)
	}
	if closer.Calls() != 0 {
		t.Fatal("runtime closed borrowed Store")
	}
	if err := closer.validate(context.Background()); err != nil {
		t.Fatalf("read borrowed Store after runtime returned: %v", err)
	}
	if err := finishSourceIntakeTestOwner(t, nil, closer); err != nil {
		t.Fatal(err)
	}
	reopened := openSourceRuntimeStoreAt(t, path)
	checkpoint, found, err := reopened.LoadSourceCheckpoint(context.Background(), "source-1")
	if err != nil || !found || checkpoint.DeliverySequence != delivery.Sequence || checkpoint.Position != delivery.Record.Position {
		t.Fatalf("reopened checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
	if receipt, found, err := reopened.FindProcessingReceipt(context.Background(), delivery.ID); err != nil || !found || receipt.DeliveryID != delivery.ID {
		t.Fatalf("reopened receipt=%+v found=%v err=%v", receipt, found, err)
	}
	if reader.Calls() != 1 {
		t.Fatalf("Reader.Read() calls = %d, want 1", reader.Calls())
	}
	if closer.Calls() != 1 {
		t.Fatalf("Close() calls = %d, want 1", closer.Calls())
	}
}

func TestRunSourceIntakeRuntimeReaderErrorDrainsAcceptedSet(t *testing.T) {
	database, path := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	seedSQLiteProcessingCatalog(t, path)
	deliveries := []core.Delivery{
		shutdownDelivery(t, 1, 0, 10),
		shutdownDelivery(t, 2, 10, 20),
	}
	queue, err := source.NewDeliveryQueue(len(deliveries))
	if err != nil {
		t.Fatal(err)
	}
	readerErr := errors.New("reader stopped unexpectedly")
	closeErr := errors.New("close failed")
	reader := sourceIntakeReaderFunc(func(ctx context.Context, sink source.DeliverySink) error {
		for _, delivery := range deliveries {
			if err := sink.Deliver(ctx, delivery); err != nil {
				return err
			}
		}
		return readerErr
	})
	runner := &sourceRuntimeBlockingRunner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), runner)
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	closer := &sourceRuntimeValidatingCloser{
		database: database,
		err:      closeErr,
		validate: func(ctx context.Context) error {
			checkpoint, found, err := database.LoadSourceCheckpoint(ctx, "source-1")
			if err != nil {
				return err
			}
			if !found || checkpoint.DeliverySequence != 2 {
				return fmt.Errorf("checkpoint before close = %+v, found=%v", checkpoint, found)
			}
			return nil
		},
	}

	result := make(chan error, 1)
	go func() {
		result <- RunSourceIntakeRuntime(context.Background(), time.Second, reader, queue, coordinator, checkpoints, nil)
	}()
	waitForSourceRuntimeSignal(t, runner.started, "worker start")
	if closer.Calls() != 0 {
		t.Fatal("database closed before reader error accepted set drained")
	}
	close(runner.release)
	err = waitForSourceRuntimeResult(t, result)
	if errors.Is(err, closeErr) {
		t.Fatalf("runtime returned owner close error: %v", err)
	}
	err = finishSourceIntakeTestOwner(t, err, closer)
	if !errors.Is(err, readerErr) || !errors.Is(err, closeErr) {
		t.Fatalf("RunSourceIntakeRuntime() error = %v, want reader and close errors", err)
	}
	if closer.Calls() != 1 {
		t.Fatalf("Close() calls = %d, want 1", closer.Calls())
	}
}

func TestRunSourceIntakeRuntimePreservesReaderFailureJoinedWithCancellation(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	readerFailure := errors.New("reader cleanup failed")
	readerStopped := make(chan struct{})
	reader := sourceIntakeReaderFunc(func(ctx context.Context, _ source.DeliverySink) error {
		<-ctx.Done()
		close(readerStopped)
		return errors.Join(readerFailure, ctx.Err())
	})
	closer := &sourceRuntimeCountingCloser{}
	runCtx, cancelRun := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunSourceIntakeRuntime(
			runCtx,
			time.Second,
			reader,
			queue,
			NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), &sourceRuntimeErrorRunner{}),
			checkpoints,
			nil,
		)
	}()

	cancelRun()
	waitForSourceRuntimeSignal(t, readerStopped, "reader stop")
	err = waitForSourceRuntimeResult(t, result)
	err = finishSourceIntakeTestOwner(t, err, closer)
	if !errors.Is(err, readerFailure) {
		t.Fatalf("RunSourceIntakeRuntime() error = %v, want reader failure", err)
	}
	if closer.Calls() != 1 {
		t.Fatalf("Close() calls = %d, want 1", closer.Calls())
	}
}

func TestRunSourceIntakeRuntimeRejectsInvalidDeliveryAtSink(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	reader := sourceIntakeReaderFunc(func(ctx context.Context, sink source.DeliverySink) error {
		return sink.Deliver(ctx, core.Delivery{})
	})
	closer := &sourceRuntimeCountingCloser{}

	err = RunSourceIntakeRuntime(
		context.Background(),
		time.Second,
		reader,
		queue,
		NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), &sourceRuntimeErrorRunner{}),
		newSourceRuntimeCheckpoints(t, database),
		nil,
	)
	if err == nil || !containsErrorText(err, "delivery id is not canonical") {
		t.Fatalf("RunSourceIntakeRuntime() error = %v, want invalid delivery", err)
	}
	if queue.Len() != 0 {
		t.Fatalf("queue length = %d, want 0", queue.Len())
	}
	err = finishSourceIntakeTestOwner(t, err, closer)
	if closer.Calls() != 1 {
		t.Fatalf("Close() calls = %d, want 1", closer.Calls())
	}
}

func TestRunSourceIntakeRuntimeTimeoutDoesNotCloseWhileReaderIsActive(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	reader := &sourceIntakeIgnoringCancelReader{
		started: make(chan struct{}),
		release: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	closer := &sourceRuntimeCountingCloser{}
	runCtx, cancelRun := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunSourceIntakeRuntime(
			runCtx,
			20*time.Millisecond,
			reader,
			queue,
			NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), &sourceRuntimeErrorRunner{}),
			checkpoints,
			nil,
		)
	}()

	waitForSourceRuntimeSignal(t, reader.started, "reader start")
	cancelRun()
	err = waitForSourceRuntimeResult(t, result)
	err = finishSourceIntakeTestOwner(t, err, closer)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSourceIntakeShutdownTimeout) {
		t.Fatalf("RunSourceIntakeRuntime() error = %v, want deadline", err)
	}
	if closer.Calls() != 0 {
		t.Fatal("timeout closed database while reader still owned the intake")
	}
	close(reader.release)
	waitForSourceRuntimeSignal(t, reader.stopped, "reader return")
}

func TestRunSourceIntakeRuntimePreservesWorkerFailureWhenReaderStopTimesOut(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	delivery := shutdownDelivery(t, 1, 0, 10)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	readerStarted := make(chan struct{})
	readerRelease := make(chan struct{})
	readerStopped := make(chan struct{})
	reader := sourceIntakeReaderFunc(func(ctx context.Context, sink source.DeliverySink) error {
		if err := sink.Deliver(ctx, delivery); err != nil {
			return err
		}
		close(readerStarted)
		<-readerRelease
		close(readerStopped)
		return nil
	})
	workerErr := errors.New("worker failed")
	closer := &sourceRuntimeCountingCloser{}
	result := make(chan error, 1)
	go func() {
		result <- RunSourceIntakeRuntime(
			context.Background(),
			20*time.Millisecond,
			reader,
			queue,
			NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), &sourceRuntimeErrorRunner{err: workerErr}),
			newSourceRuntimeCheckpoints(t, database),
			nil,
		)
	}()

	waitForSourceRuntimeSignal(t, readerStarted, "reader start")
	err = waitForSourceRuntimeResult(t, result)
	err = finishSourceIntakeTestOwner(t, err, closer)
	if !errors.Is(err, workerErr) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSourceIntakeShutdownTimeout) {
		t.Fatalf("RunSourceIntakeRuntime() error = %v, want worker error and deadline", err)
	}
	if closer.Calls() != 0 {
		t.Fatal("timeout closed database after worker failure while reader remained active")
	}
	close(readerRelease)
	waitForSourceRuntimeSignal(t, readerStopped, "reader return")
}

func TestRunSourceIntakeRuntimeTimeoutDoesNotCloseDuringCommitUnknownReadback(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	delivery := sqliteDeliveryAt(t, 0, 10, time.Unix(1_700_000_000, 0).UTC())
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	reader := sourceIntakeReaderFunc(func(ctx context.Context, sink source.DeliverySink) error {
		return sink.Deliver(ctx, delivery)
	})
	baseStore := newFakeStore()
	baseStore.commitState = commitUnknown
	baseStore.persistOnUnknown = true
	readbackStore := &sourceRuntimeBlockingReadbackStore{
		fakeStore: baseStore,
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	closer := &sourceRuntimeCountingCloser{}
	result := make(chan error, 1)
	go func() {
		result <- RunSourceIntakeRuntime(
			context.Background(),
			20*time.Millisecond,
			reader,
			queue,
			NewCoordinator(readbackStore, &zeroOutcomeRunner{}),
			checkpoints,
			nil,
		)
	}()

	waitForSourceRuntimeSignal(t, readbackStore.started, "commit-unknown readback")
	err = waitForSourceRuntimeResult(t, result)
	err = finishSourceIntakeTestOwner(t, err, closer)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSourceIntakeShutdownTimeout) {
		t.Fatalf("RunSourceIntakeRuntime() error = %v, want deadline", err)
	}
	if closer.Calls() != 0 {
		t.Fatal("timeout closed database during commit-unknown readback")
	}
	close(readbackStore.release)
	waitForSourceRuntimeCheckpoint(t, database, checkpoints, delivery.Sequence)
}

func TestRunSourceIntakeRuntimeRejectsNilReaderBeforeStartingRuntime(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	closer := &sourceRuntimeValidatingCloser{database: database}
	var reader *sourceIntakeBlockingReader
	err := RunSourceIntakeRuntime(
		context.Background(),
		time.Second,
		reader,
		nil,
		nil,
		nil,
		nil,
	)
	if err == nil || !containsErrorText(err, "reader is required") {
		t.Fatalf("RunSourceIntakeRuntime() error = %v, want reader validation", err)
	}
	if _, _, readErr := database.LoadSourceCheckpoint(context.Background(), "source-1"); readErr != nil {
		t.Fatalf("read Store after validation failure: %v", readErr)
	}
	err = finishSourceIntakeTestOwner(t, err, closer)
	if !containsErrorText(err, "reader is required") || closer.Calls() != 1 {
		t.Fatalf("owner error=%v close=%d", err, closer.Calls())
	}
}

func TestRunSourceIntakeRuntimeComponentDeadlineReturnsCloseOwnership(t *testing.T) {
	for _, component := range []string{"reader", "worker", "maintenance"} {
		t.Run(component, func(t *testing.T) {
			database, _ := openSourceRuntimeStore(t)
			seedShutdownSource(t, database)
			queue, err := source.NewDeliveryQueue(1)
			if err != nil {
				t.Fatal(err)
			}
			reader := sourceIntakeReaderFunc(func(ctx context.Context, sink source.DeliverySink) error {
				if component == "reader" {
					return context.DeadlineExceeded
				}
				if component == "worker" {
					return sink.Deliver(ctx, shutdownDelivery(t, 1, 0, 10))
				}
				return nil
			})
			maintenanceCalls := 0
			closer := &sourceRuntimeValidatingCloser{database: database}
			err = RunSourceIntakeRuntime(context.Background(), time.Second, reader, queue,
				NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), &sourceRuntimeErrorRunner{err: context.DeadlineExceeded}),
				newSourceRuntimeCheckpoints(t, database), func(context.Context) error {
					maintenanceCalls++
					return context.DeadlineExceeded
				})
			if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSourceIntakeShutdownTimeout) {
				t.Fatalf("component error=%v, want ordinary deadline", err)
			}
			if closer.Calls() != 0 {
				t.Fatal("runtime closed borrowed Store")
			}
			if _, _, readErr := database.LoadSourceCheckpoint(context.Background(), "source-1"); readErr != nil {
				t.Fatalf("read borrowed Store: %v", readErr)
			}
			wantMaintenance := 0
			if component == "maintenance" {
				wantMaintenance = 1
			}
			if maintenanceCalls != wantMaintenance {
				t.Fatalf("maintenance calls=%d, want %d", maintenanceCalls, wantMaintenance)
			}
			err = finishSourceIntakeTestOwner(t, err, closer)
			if !errors.Is(err, context.DeadlineExceeded) || closer.Calls() != 1 {
				t.Fatalf("owner error=%v close=%d", err, closer.Calls())
			}
		})
	}
}

type sourceIntakeReaderFunc func(context.Context, source.DeliverySink) error

func (f sourceIntakeReaderFunc) Read(ctx context.Context, sink source.DeliverySink) error {
	return f(ctx, sink)
}

type sourceIntakeBlockingReader struct {
	delivery core.Delivery
	started  chan struct{}
	stopped  chan struct{}
	mu       sync.Mutex
	calls    int
}

func (r *sourceIntakeBlockingReader) Read(ctx context.Context, sink source.DeliverySink) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if err := sink.Deliver(ctx, r.delivery); err != nil {
		return err
	}
	close(r.started)
	defer close(r.stopped)
	<-ctx.Done()
	return ctx.Err()
}

func (r *sourceIntakeBlockingReader) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type sourceIntakeIgnoringCancelReader struct {
	started chan struct{}
	release chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (r *sourceIntakeIgnoringCancelReader) Read(context.Context, source.DeliverySink) error {
	r.once.Do(func() { close(r.started) })
	<-r.release
	close(r.stopped)
	return nil
}

func containsErrorText(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}

// 测试调用者只在 runtime 已交回安全关闭责任后关闭 Store。
func finishSourceIntakeTestOwner(t *testing.T, runErr error, closer interface {
	Close() error
	Calls() int
}) error {
	t.Helper()
	if closer.Calls() != 0 {
		t.Fatalf("runtime closed borrowed Store %d times", closer.Calls())
	}
	if errors.Is(runErr, ErrSourceIntakeShutdownTimeout) {
		return runErr
	}
	return errors.Join(runErr, closer.Close())
}
