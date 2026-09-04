package processor

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/source"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestShutdownPrimitives_DrainFixedAcceptedSetBeforeCheckpointAndClose(t *testing.T) {
	database, path := openSQLiteProcessingStore(t)
	seedSQLiteProcessingCatalog(t, path)
	state := newProcessorCoverageState(t, database)
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
	events := []string{"accepted_set_frozen"}
	runner := &shutdownSequenceRunner{}
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), runner)
	completions := make([]core.DurableCompletion, 0, len(deliveries))
	drainContext, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	drainResult := make(chan error, 1)
	go func() {
		for range deliveries {
			delivery, err := queue.Dequeue(drainContext)
			if err != nil {
				drainResult <- err
				return
			}
			completion, err := coordinator.Process(drainContext, delivery)
			if err != nil {
				drainResult <- err
				return
			}
			completions = append(completions, completion)
		}
		drainResult <- nil
	}()
	select {
	case err := <-drainResult:
		if err != nil {
			t.Fatalf("drain accepted deliveries: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain accepted deliveries timed out")
	}
	if runner.calls != len(deliveries) {
		t.Fatalf("runner calls = %d, want %d", runner.calls, len(deliveries))
	}
	events = append(events, "pipeline_drained")
	if checkpoint, found, err := database.LoadSourceCheckpoint(context.Background(), "source-1"); err != nil || found {
		t.Fatalf("checkpoint before drain flush = %+v,%v,%v", checkpoint, found, err)
	}
	tracker, err := source.NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := source.NewCheckpointManager(tracker, state)
	if err := checkpoints.Complete(context.Background(), completions[0]); err != nil {
		t.Fatalf("Complete(first) after drain: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkpoints.Complete(cancelled, completions[1]); !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete(second) error = %v, want context cancellation", err)
	}
	checkpoint, found, err := database.LoadSourceCheckpoint(context.Background(), "source-1")
	if err != nil || !found || checkpoint.DeliverySequence != 1 {
		t.Fatalf("checkpoint before final Flush = %+v,%v,%v", checkpoint, found, err)
	}
	if err := checkpoints.Flush(context.Background()); err != nil {
		t.Fatalf("Flush(): %v", err)
	}
	events = append(events, "checkpoint_flushed")

	checkpoint, found, err = database.LoadSourceCheckpoint(context.Background(), "source-1")
	if err != nil || !found {
		t.Fatalf("LoadSourceCheckpoint() = %+v,%v,%v", checkpoint, found, err)
	}
	if checkpoint.DeliverySequence != 2 || checkpoint.Position != deliveries[1].Record.Position {
		t.Fatalf("checkpoint = %+v, want sequence 2", checkpoint)
	}
	for _, delivery := range deliveries {
		if _, found, err := database.FindProcessingReceipt(context.Background(), delivery.ID); err != nil || !found {
			t.Fatalf("FindProcessingReceipt(%q) found=%v err=%v", delivery.ID, found, err)
		}
	}
	auditCount := shutdownAuditCount(t, path)
	if auditCount != 2 {
		t.Fatalf("critical audit count = %d, want 2", auditCount)
	}
	events = append(events, "audit_barrier")
	if err := database.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, _, err := database.LoadSourceCheckpoint(context.Background(), "source-1"); err == nil {
		t.Fatal("closed database accepted checkpoint read")
	}
	events = append(events, "db_closed")
	if want := []string{
		"accepted_set_frozen", "pipeline_drained", "checkpoint_flushed", "audit_barrier", "db_closed",
	}; !slices.Equal(events, want) {
		t.Fatalf("shutdown events = %v, want %v", events, want)
	}
}

func TestShutdownPrimitives_CancellationLeavesAttemptForReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.db")
	database := openShutdownStore(t, path)
	seedShutdownSource(t, database)
	delivery := shutdownDelivery(t, 1, 0, 10)
	state := newProcessorCoverageState(t, database)
	tracker, err := source.NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := source.NewCheckpointManager(tracker, state)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(context.Background(), delivery); err != nil {
		t.Fatalf("Enqueue(): %v", err)
	}

	runner := &shutdownBlockingRunner{started: make(chan struct{})}
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), runner)
	workerContext, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	workerResult := make(chan error, 1)
	go func() {
		queued, err := queue.Dequeue(workerContext)
		if err != nil {
			workerResult <- err
			return
		}
		completion, err := coordinator.Process(workerContext, queued)
		if err == nil {
			err = checkpoints.Complete(workerContext, completion)
		}
		workerResult <- err
	}()
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach the cancellable attempt")
	}
	cancelWorker()
	select {
	case err := <-workerResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled worker error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled worker did not exit")
	}
	if err := checkpoints.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() after cancelled attempt: %v", err)
	}
	if _, found, err := database.FindProcessingReceipt(context.Background(), delivery.ID); err != nil || found {
		t.Fatalf("cancelled receipt found=%v err=%v", found, err)
	}
	if checkpoint, found, err := database.LoadSourceCheckpoint(context.Background(), "source-1"); err != nil || found {
		t.Fatalf("cancelled checkpoint = %+v,%v,%v", checkpoint, found, err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close before replay: %v", err)
	}

	reopened := openShutdownStore(t, path)
	replayState := newProcessorCoverageState(t, reopened)
	replayTracker, err := source.NewCompletionTracker("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	replayCheckpoints := source.NewCheckpointManager(replayTracker, replayState)
	replayRunner := &zeroOutcomeRunner{}
	replayCoordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, reopened), replayRunner)
	completion, err := replayCoordinator.Process(context.Background(), delivery)
	if err != nil {
		t.Fatalf("replay Process(): %v", err)
	}
	if err := replayCheckpoints.Complete(context.Background(), completion); err != nil {
		t.Fatalf("replay Complete(): %v", err)
	}
	if _, err := replayCoordinator.Process(context.Background(), delivery); err != nil {
		t.Fatalf("receipt replay Process(): %v", err)
	}
	if replayRunner.calls != 1 {
		t.Fatalf("replay runner calls = %d, want 1", replayRunner.calls)
	}
	checkpoint, found, err := reopened.LoadSourceCheckpoint(context.Background(), "source-1")
	if err != nil || !found || checkpoint.DeliverySequence != 1 {
		t.Fatalf("replay checkpoint = %+v,%v,%v", checkpoint, found, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close after replay: %v", err)
	}
}

type shutdownBlockingRunner struct {
	started chan struct{}
	once    sync.Once
}

type shutdownSequenceRunner struct {
	calls int
}

func (r *shutdownSequenceRunner) prepare(_ context.Context, delivery core.Delivery) (preparedAttempt, error) {
	r.calls++
	if r.calls == 1 {
		return &fullPreparedAttempt{delivery: delivery}, nil
	}
	return &zeroPreparedAttempt{kind: core.ReceiptSuccess}, nil
}

func (r *shutdownBlockingRunner) prepare(ctx context.Context, _ core.Delivery) (preparedAttempt, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func openShutdownStore(t *testing.T, path string) *store.Store {
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

func seedShutdownSource(t *testing.T, database *store.Store) {
	t.Helper()
	base := time.Unix(1_700_000_000, 0).UTC()
	nodeID := core.NodeID("00112233445566778899aabbccddeeff")
	if err := database.EnsureNodeIdentity(context.Background(), nodeID, base); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureSource(context.Background(), "source-1", nodeID, store.SourceKindFile, base); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterFileGeneration(context.Background(), store.FileGeneration{
		SourceID: "source-1", Generation: "00112233445566778899aabbccddeeff",
		DeviceID: 1, Inode: 2, Path: "/var/log/guard.log", ObservedSize: 30, OpenedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
}

func shutdownAuditCount(t *testing.T, path string) int {
	t.Helper()
	connection := openSQLiteTestConnection(t, path)
	defer func() {
		if err := connection.Close(); err != nil {
			t.Errorf("close audit readback: %v", err)
		}
	}()
	var count int
	if err := connection.QueryRowContext(context.Background(), "SELECT count(*) FROM audit_logs").Scan(&count); err != nil {
		t.Fatalf("count critical audits: %v", err)
	}
	return count
}

func shutdownDelivery(t *testing.T, sequence core.DeliverySequence, start, end uint64) core.Delivery {
	t.Helper()
	filePosition := core.FilePosition{
		Generation: "00112233445566778899aabbccddeeff",
		DeviceID:   1, Inode: 2, StartOffset: start, EndOffset: end,
	}
	position, err := core.NewFilePosition(filePosition)
	if err != nil {
		t.Fatalf("NewFilePosition(): %v", err)
	}
	deliveryID, err := core.FileDeliveryID("source-1", core.FilePosition{
		Generation:  filePosition.Generation,
		StartOffset: start,
		EndOffset:   end,
	})
	if err != nil {
		t.Fatalf("FileDeliveryID(): %v", err)
	}
	return core.Delivery{
		ID: deliveryID, Sequence: sequence,
		Record: core.RawRecord{
			SourceID: "source-1", ObservedAt: time.Unix(1_700_000_000, 0).UTC(),
			Position: position, Content: []byte("record"),
		},
	}
}
