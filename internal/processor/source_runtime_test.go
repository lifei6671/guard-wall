package processor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/source"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestRunSourceRuntimeDrainsAcceptedSetBeforeCheckpointAndClose(t *testing.T) {
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
	for _, delivery := range deliveries {
		if err := queue.Enqueue(context.Background(), delivery); err != nil {
			t.Fatalf("Enqueue(): %v", err)
		}
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
			if !found || checkpoint.DeliverySequence != 2 {
				return fmt.Errorf("checkpoint before close = %+v, found=%v", checkpoint, found)
			}
			for _, delivery := range deliveries {
				if _, found, err := database.FindProcessingReceipt(ctx, delivery.ID); err != nil || !found {
					return fmt.Errorf("receipt %q before close: found=%v err=%w", delivery.ID, found, err)
				}
			}
			return nil
		},
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunSourceRuntime(runCtx, time.Second, queue, coordinator, checkpoints, closer)
	}()
	waitForSourceRuntimeSignal(t, runner.started, "worker start")
	cancelRun()
	if closer.Calls() != 0 {
		t.Fatal("database closed before accepted delivery was released")
	}
	close(runner.release)
	if err := waitForSourceRuntimeResult(t, result); err != nil {
		t.Fatalf("RunSourceRuntime(): %v", err)
	}
	if closer.Calls() != 1 {
		t.Fatalf("Close() calls = %d, want 1", closer.Calls())
	}

	reopened := openSourceRuntimeStoreAt(t, path)
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened store: %v", err)
		}
	}()
	checkpoint, found, err := reopened.LoadSourceCheckpoint(context.Background(), "source-1")
	if err != nil || !found || checkpoint.DeliverySequence != 2 {
		t.Fatalf("reopened checkpoint = %+v, found=%v err=%v", checkpoint, found, err)
	}
	if auditCount := shutdownAuditCount(t, path); auditCount != len(deliveries) {
		t.Fatalf("critical audit count = %d, want %d", auditCount, len(deliveries))
	}
}

func TestRunSourceRuntimeWorkerFailureSealsQueueAndPreservesCloseError(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	delivery := shutdownDelivery(t, 1, 0, 10)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}

	workerErr := errors.New("runtime worker failed")
	closeErr := errors.New("runtime close failed")
	coordinator := NewCoordinator(
		newEnforcingSQLiteStoreAdapter(t, database),
		&sourceRuntimeErrorRunner{err: workerErr},
	)
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	closer := &sourceRuntimeValidatingCloser{database: database, err: closeErr}

	err = RunSourceRuntime(context.Background(), time.Second, queue, coordinator, checkpoints, closer)
	if !errors.Is(err, workerErr) || !errors.Is(err, closeErr) {
		t.Fatalf("RunSourceRuntime() error = %v, want worker and close errors", err)
	}
	if closer.Calls() != 1 {
		t.Fatalf("Close() calls = %d, want 1", closer.Calls())
	}
	if err := queue.Enqueue(context.Background(), delivery); !errors.Is(err, source.ErrDeliveryQueueSealed) {
		t.Fatalf("post-failure Enqueue() error = %v, want sealed", err)
	}
}

func TestRunSourceRuntimeTimeoutDoesNotCloseStoreUnderActiveWorker(t *testing.T) {
	database, path := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	seedSQLiteProcessingCatalog(t, path)
	delivery := shutdownDelivery(t, 1, 0, 10)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}

	runner := &sourceRuntimeCancelBlockingRunner{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
		returned:  make(chan struct{}),
	}
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), runner)
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	closer := &sourceRuntimeValidatingCloser{database: database}
	runCtx, cancelRun := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunSourceRuntime(runCtx, 20*time.Millisecond, queue, coordinator, checkpoints, closer)
	}()

	waitForSourceRuntimeSignal(t, runner.started, "worker start")
	cancelRun()
	err = waitForSourceRuntimeResult(t, result)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunSourceRuntime() error = %v, want deadline", err)
	}
	if closer.Calls() != 0 {
		t.Fatal("timeout closed database while worker still owned it")
	}

	waitForSourceRuntimeSignal(t, runner.cancelled, "worker cancellation")
	close(runner.release)
	waitForSourceRuntimeSignal(t, runner.returned, "worker return")
	if err := database.Close(); err != nil {
		t.Fatalf("close database after worker exit: %v", err)
	}
}

func TestRunSourceRuntimeTimeoutDoesNotCloseDuringCommitUnknownReadback(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	delivery := testDelivery(t, 1)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}

	baseStore := newFakeStore()
	baseStore.commitState = commitUnknown
	baseStore.persistOnUnknown = true
	readbackStore := &sourceRuntimeBlockingReadbackStore{
		fakeStore: baseStore,
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	coordinator := NewCoordinator(readbackStore, &zeroOutcomeRunner{})
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	closer := &sourceRuntimeCountingCloser{}
	runCtx, cancelRun := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RunSourceRuntime(runCtx, 20*time.Millisecond, queue, coordinator, checkpoints, closer)
	}()

	waitForSourceRuntimeSignal(t, readbackStore.started, "commit-unknown readback")
	cancelRun()
	err = waitForSourceRuntimeResult(t, result)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunSourceRuntime() error = %v, want deadline", err)
	}
	if closer.Calls() != 0 {
		t.Fatal("timeout closed database during commit-unknown readback")
	}

	close(readbackStore.release)
	waitForSourceRuntimeCheckpoint(t, database, checkpoints, delivery.Sequence)
}

type sourceRuntimeBlockingRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func (r *sourceRuntimeBlockingRunner) prepare(_ context.Context, delivery core.Delivery) (preparedAttempt, error) {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == 1 {
		return &fullPreparedAttempt{delivery: delivery}, nil
	}
	return &zeroPreparedAttempt{kind: core.ReceiptSuccess}, nil
}

type sourceRuntimeErrorRunner struct {
	err error
}

func (r *sourceRuntimeErrorRunner) prepare(context.Context, core.Delivery) (preparedAttempt, error) {
	return nil, r.err
}

type sourceRuntimeCancelBlockingRunner struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	returned  chan struct{}
	once      sync.Once
}

type sourceRuntimeBlockingReadbackStore struct {
	*fakeStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *sourceRuntimeBlockingReadbackStore) findReceipt(
	ctx context.Context,
	deliveryID core.DeliveryID,
) (core.ProcessingReceipt, bool, error) {
	if s.beginCount > 0 {
		s.once.Do(func() { close(s.started) })
		<-s.release
	}
	return s.fakeStore.findReceipt(ctx, deliveryID)
}

func (r *sourceRuntimeCancelBlockingRunner) prepare(ctx context.Context, _ core.Delivery) (preparedAttempt, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	close(r.cancelled)
	<-r.release
	close(r.returned)
	return nil, ctx.Err()
}

type sourceRuntimeValidatingCloser struct {
	database *store.Store
	validate func(context.Context) error
	err      error
	mu       sync.Mutex
	calls    int
}

type sourceRuntimeCountingCloser struct {
	mu    sync.Mutex
	calls int
}

func (c *sourceRuntimeCountingCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return nil
}

func (c *sourceRuntimeCountingCloser) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *sourceRuntimeValidatingCloser) Close() error {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	var validationErr error
	if c.validate != nil {
		validationErr = c.validate(context.Background())
	}
	return errors.Join(validationErr, c.database.Close(), c.err)
}

func (c *sourceRuntimeValidatingCloser) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func openSourceRuntimeStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guard.db")
	return openSourceRuntimeStoreAt(t, path), path
}

func openSourceRuntimeStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), path, processorMigrationFS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("cleanup Close(): %v", err)
		}
	})
	return database
}

func newSourceRuntimeCheckpoints(t *testing.T, database *store.Store) *source.CheckpointManager {
	t.Helper()
	tracker, err := source.NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	return source.NewCheckpointManager(tracker, source.NewSQLiteStateStore(database))
}

func waitForSourceRuntimeSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForSourceRuntimeResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("source runtime did not return")
		return nil
	}
}

func waitForSourceRuntimeCheckpoint(
	t *testing.T,
	database *store.Store,
	checkpoints *source.CheckpointManager,
	want core.DeliverySequence,
) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if err := checkpoints.Flush(context.Background()); err != nil {
			t.Fatalf("Flush(): %v", err)
		}
		checkpoint, found, err := database.LoadSourceCheckpoint(context.Background(), "source-1")
		if err != nil {
			t.Fatalf("LoadSourceCheckpoint(): %v", err)
		}
		if found && checkpoint.DeliverySequence == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for checkpoint %d", want)
		case <-time.After(time.Millisecond):
		}
	}
}
