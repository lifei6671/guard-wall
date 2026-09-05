package processor

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/source"
	"github.com/lifei6671/guard-wall/internal/store"
)

func TestSourceMaintenanceRunsAfterDurableDrainBeforeClose(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%v", stop), func(t *testing.T) {
			database, path := openSourceRuntimeStore(t)
			seedShutdownSource(t, database)
			seedSQLiteProcessingCatalog(t, path)
			delivery := shutdownDelivery(t, 1, 0, 10)
			queue, err := source.NewDeliveryQueue(1)
			if err != nil {
				t.Fatal(err)
			}
			readerReturned := make(chan struct{})
			reader := sourceIntakeReaderFunc(func(ctx context.Context, sink source.DeliverySink) error {
				defer close(readerReturned)
				if err := sink.Deliver(ctx, delivery); err != nil {
					return err
				}
				if stop {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			})
			runner := &sourceRuntimeBlockingRunner{started: make(chan struct{}), release: make(chan struct{})}
			closer := &sourceRuntimeValidatingCloser{database: database}
			release, callbackRelease := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			maintenance := func(ctx context.Context) error {
				calls.Add(1)
				select {
				case <-readerReturned:
				default:
					return errors.New("maintenance before reader returned")
				}
				if queue.Len() != 0 {
					return errors.New("maintenance before queue drained")
				}
				if err := queue.Enqueue(ctx, delivery); !errors.Is(err, source.ErrDeliveryQueueSealed) {
					return fmt.Errorf("queue not sealed: %v", err)
				}
				checkpoint, found, err := database.LoadSourceCheckpoint(ctx, "source-1")
				if err != nil || !found || checkpoint.DeliverySequence != delivery.Sequence {
					return fmt.Errorf("checkpoint before maintenance: %+v found=%v err=%v", checkpoint, found, err)
				}
				if _, found, err := database.FindProcessingReceipt(ctx, delivery.ID); err != nil || !found {
					return fmt.Errorf("receipt before maintenance: found=%v err=%v", found, err)
				}
				// 通知测试观察同步维护期间仍在借用数据库。
				close(release)
				<-callbackRelease
				return nil
			}
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- RunSourceIntakeRuntime(runCtx, 5*time.Second, reader, queue, NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), runner), newSourceRuntimeCheckpoints(t, database), maintenance)
			}()
			waitForSourceRuntimeSignal(t, runner.started, "worker start")
			if stop {
				cancel()
			}
			waitForSourceRuntimeSignal(t, readerReturned, "reader return")
			if calls.Load() != 0 || closer.Calls() != 0 {
				t.Fatal("maintenance or close before worker drained")
			}
			close(runner.release)
			select {
			case <-release:
			case err := <-result:
				t.Fatalf("runtime returned before maintenance barrier: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("maintenance did not start")
			}
			if closer.Calls() != 0 {
				t.Fatal("close while maintenance is active")
			}
			close(callbackRelease)
			if err := waitForSourceRuntimeResult(t, result); err != nil {
				t.Fatal(err)
			}
			checkpoint, found, err := database.LoadSourceCheckpoint(context.Background(), "source-1")
			if err != nil || !found || checkpoint.DeliverySequence != delivery.Sequence || checkpoint.Position != delivery.Record.Position {
				t.Fatalf("borrowed checkpoint=%+v found=%v err=%v", checkpoint, found, err)
			}
			if receipt, found, err := database.FindProcessingReceipt(context.Background(), delivery.ID); err != nil || !found || receipt.DeliveryID != delivery.ID {
				t.Fatalf("borrowed receipt=%+v found=%v err=%v", receipt, found, err)
			}
			if err := finishSourceIntakeTestOwner(t, nil, closer); err != nil {
				t.Fatal(err)
			}
			reopened := openSourceRuntimeStoreAt(t, path)
			recovered, found, err := reopened.LoadSourceCheckpoint(context.Background(), "source-1")
			if err != nil || !found || recovered != checkpoint {
				t.Fatalf("reopened checkpoint=%+v want=%+v found=%v err=%v", recovered, checkpoint, found, err)
			}
			if receipt, found, err := reopened.FindProcessingReceipt(context.Background(), delivery.ID); err != nil || !found || receipt.DeliveryID != delivery.ID {
				t.Fatalf("reopened receipt=%+v found=%v err=%v", receipt, found, err)
			}
			if calls.Load() != 1 || closer.Calls() != 1 {
				t.Fatalf("maintenance=%d close=%d", calls.Load(), closer.Calls())
			}
		})
	}
}

func TestSourceMaintenanceSkipsReaderAndWorkerFailures(t *testing.T) {
	for _, mode := range []string{"reader", "worker", "cancel-with-failure"} {
		t.Run(mode, func(t *testing.T) {
			database, _ := openSourceRuntimeStore(t)
			seedShutdownSource(t, database)
			queue, err := source.NewDeliveryQueue(2)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New(mode)
			accepted := make(chan struct{})
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := sourceIntakeReaderFunc(func(ctx context.Context, sink source.DeliverySink) error {
				if mode == "cancel-with-failure" {
					cancel()
					<-ctx.Done()
					return errors.Join(ctx.Err(), failure)
				}
				if mode == "reader" {
					return failure
				}
				for i := 1; i <= 2; i++ {
					if err := sink.Deliver(ctx, shutdownDelivery(t, core.DeliverySequence(i), uint64((i-1)*10), uint64(i*10))); err != nil {
						return err
					}
				}
				close(accepted)
				return nil
			})
			closer := &sourceRuntimeCountingCloser{}
			calls := 0
			err = RunSourceIntakeRuntime(runCtx, time.Second, reader, queue, NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), &sourceMaintenanceErrorRunner{accepted: accepted, err: failure}), newSourceRuntimeCheckpoints(t, database), func(context.Context) error { calls++; return nil })
			err = finishSourceIntakeTestOwner(t, err, closer)
			if !errors.Is(err, failure) || calls != 0 || closer.Calls() != 1 {
				t.Fatalf("error=%v maintenance=%d close=%d", err, calls, closer.Calls())
			}
			if mode == "worker" && queue.Len() != 1 {
				t.Fatalf("pending queue=%d, want 1", queue.Len())
			}
		})
	}
}

type sourceMaintenanceErrorRunner struct {
	accepted <-chan struct{}
	err      error
}

func (r *sourceMaintenanceErrorRunner) prepare(ctx context.Context, _ core.Delivery) (preparedAttempt, error) {
	select {
	case <-r.accepted:
		return nil, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSourceMaintenanceFinishFailureAndDeadline(t *testing.T) {
	for _, mode := range []string{"flush-error", "maintenance-error", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			database, _ := openSourceRuntimeStore(t)
			seedShutdownSource(t, database)
			checkpoints := newSourceRuntimeCheckpoints(t, database)
			maintenanceErr, closeErr := errors.New("maintenance failed"), errors.New("close failed")
			closer := &sourceRuntimeValidatingCloser{database: database, err: closeErr}
			var flushErr error
			if mode == "flush-error" {
				flushErr = checkpoints.Complete(context.Background(), core.DurableCompletion{})
				if flushErr == nil {
					t.Fatal("expected invalid completion")
				}
			}
			deadline := time.Now().Add(time.Second)
			if mode == "deadline" {
				deadline = time.Now().Add(30 * time.Millisecond)
			}
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			calls := 0
			err := finishSourceIntakeRuntime(ctx, checkpoints, nil, func(callbackCtx context.Context) error {
				calls++
				got, ok := callbackCtx.Deadline()
				if !ok || !got.Equal(deadline) {
					t.Errorf("maintenance deadline=%v, want %v", got, deadline)
				}
				if closer.Calls() != 0 {
					t.Error("closed before maintenance")
				}
				if mode == "deadline" {
					<-callbackCtx.Done()
				}
				return maintenanceErr
			})
			err = finishSourceIntakeTestOwner(t, err, closer)
			if mode == "flush-error" {
				if !errors.Is(err, flushErr) || !errors.Is(err, closeErr) || calls != 0 || closer.Calls() != 1 {
					t.Fatalf("error=%v maintenance=%d close=%d", err, calls, closer.Calls())
				}
			} else if mode == "deadline" {
				if !errors.Is(err, maintenanceErr) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSourceIntakeShutdownTimeout) || calls != 1 || closer.Calls() != 0 {
					t.Fatalf("error=%v maintenance=%d close=%d", err, calls, closer.Calls())
				}
			} else if !errors.Is(err, maintenanceErr) || !errors.Is(err, closeErr) || calls != 1 || closer.Calls() != 1 {
				t.Fatalf("error=%v maintenance=%d close=%d", err, calls, closer.Calls())
			}
		})
	}
}

func TestSourceMaintenanceSkipsReaderTimeout(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	reader := &sourceIntakeIgnoringCancelReader{started: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	closer := &sourceRuntimeCountingCloser{}
	var calls atomic.Int32
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- RunSourceIntakeRuntime(runCtx, 20*time.Millisecond, reader, queue, NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), &sourceRuntimeErrorRunner{}), newSourceRuntimeCheckpoints(t, database), func(context.Context) error { calls.Add(1); return nil })
	}()
	waitForSourceRuntimeSignal(t, reader.started, "reader start")
	cancel()
	err = waitForSourceRuntimeResult(t, result)
	err = finishSourceIntakeTestOwner(t, err, closer)
	close(reader.release)
	waitForSourceRuntimeSignal(t, reader.stopped, "reader return")
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSourceIntakeShutdownTimeout) || calls.Load() != 0 || closer.Calls() != 0 {
		t.Fatalf("error=%v maintenance=%d close=%d", err, calls.Load(), closer.Calls())
	}
}

func TestSourceMaintenanceSkipsWorkerTimeout(t *testing.T) {
	database, path := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	seedSQLiteProcessingCatalog(t, path)
	queue, err := source.NewDeliveryQueue(1)
	if err != nil {
		t.Fatal(err)
	}
	delivery := shutdownDelivery(t, 1, 0, 10)
	runner := &sourceRuntimeCancelBlockingRunner{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	reader := sourceIntakeReaderFunc(func(ctx context.Context, sink source.DeliverySink) error { return sink.Deliver(ctx, delivery) })
	closer := &sourceRuntimeCountingCloser{}
	var calls atomic.Int32
	result := make(chan error, 1)
	go func() {
		result <- RunSourceIntakeRuntime(context.Background(), 20*time.Millisecond, reader, queue, NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), runner), newSourceRuntimeCheckpoints(t, database), func(context.Context) error { calls.Add(1); return nil })
	}()
	waitForSourceRuntimeSignal(t, runner.started, "worker start")
	err = waitForSourceRuntimeResult(t, result)
	err = finishSourceIntakeTestOwner(t, err, closer)
	waitForSourceRuntimeSignal(t, runner.cancelled, "worker cancellation")
	close(runner.release)
	waitForSourceRuntimeSignal(t, runner.returned, "worker return")
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSourceIntakeShutdownTimeout) || calls.Load() != 0 || closer.Calls() != 0 {
		t.Fatalf("error=%v maintenance=%d close=%d", err, calls.Load(), closer.Calls())
	}
}

func TestSourceMaintenanceSkipsFlushIOTimeout(t *testing.T) {
	database, _ := openSourceRuntimeStore(t)
	seedShutdownSource(t, database)
	checkpoints := newSourceRuntimeCheckpoints(t, database)
	delivery := shutdownDelivery(t, 1, 0, 10)
	coordinator := NewCoordinator(newEnforcingSQLiteStoreAdapter(t, database), &zeroOutcomeRunner{})
	completion, err := coordinator.Process(context.Background(), delivery)
	if err != nil {
		t.Fatal(err)
	}
	// 保留真实已提交 receipt 对应的待 Flush 候选，首次保存通过已取消 context 失败。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkpoints.Complete(cancelled, completion); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare pending checkpoint: %v", err)
	}
	lock, err := database.BeginProcessing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			if err := lock.Rollback(); err != nil {
				t.Errorf("rollback writer: %v", err)
			}
		}
	}()
	// 不提交测试写入；持有 SQLite writer lock，迫使最终 Flush 等待同一数据库。
	if err := lock.AppendCriticalAudit(context.Background(), store.CriticalAudit{
		ID: "maintenance-flush-lock", IdempotencyKey: "maintenance-flush-lock",
		NodeID: "00112233445566778899aabbccddeeff", Category: "source", Action: "flush_lock",
		Result: "success", Severity: "info", ActorType: "system", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancelDeadline := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelDeadline()
	closer := &sourceRuntimeCountingCloser{}
	calls := 0
	err = finishSourceIntakeRuntime(ctx, checkpoints, nil, func(context.Context) error { calls++; return nil })
	err = finishSourceIntakeTestOwner(t, err, closer)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrSourceIntakeShutdownTimeout) || !containsErrorText(err, "flush checkpoint") || !containsErrorText(err, "save source checkpoint") {
		t.Fatalf("error=%v, want shutdown deadline and retained Flush IO error", err)
	}
	if calls != 0 || closer.Calls() != 0 {
		t.Fatalf("maintenance=%d close=%d", calls, closer.Calls())
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	locked = false
	if _, found, err := database.LoadSourceCheckpoint(context.Background(), "source-1"); err != nil || found {
		t.Fatalf("failed Flush checkpoint found=%v err=%v", found, err)
	}
	if _, found, err := database.FindProcessingReceipt(context.Background(), delivery.ID); err != nil || !found {
		t.Fatalf("durable receipt found=%v err=%v", found, err)
	}
	// 仅测试清理阶段显式恢复 Flush，证明候选未被超时丢弃。
	if err := checkpoints.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := database.LoadSourceCheckpoint(context.Background(), "source-1")
	if err != nil || !found || checkpoint.DeliverySequence != delivery.Sequence || checkpoint.Position != delivery.Record.Position {
		t.Fatalf("recovered checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
}
