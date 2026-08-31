package decision

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	appclock "github.com/lifei6671/guard-wall/internal/clock"
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/enforcement"
)

func TestRunExpirationSchedulerStartsImmediatelyAndUsesAbsoluteDeadline(t *testing.T) {
	clock := newExpirationManualClock(time.Unix(10_000, 0).UTC())
	nodeID := core.NodeID("0123456789abcdef0123456789abcdef")
	pending := []TargetEnforcementChange{
		{NodeID: nodeID, Target: netip.MustParsePrefix("192.0.2.10/32")},
		{NodeID: nodeID, Target: netip.MustParsePrefix("192.0.2.11/32")},
	}
	runner := newExpirationSchedulerRunner(clock)
	runner.pending = pending
	wakes := &expirationWakeRecorder{}
	service := newExpirationSchedulerService(t, runner, wakes, clock)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.RunExpirationScheduler(ctx) }()

	waitForExpirationRuns(t, runner, 1)
	waitForExpirationTimers(t, clock, 1)
	if reads := runner.PendingReads(); reads != 1 {
		t.Fatalf("pending reads = %d, want 1", reads)
	}
	if got := wakes.Changes(); len(got) != len(pending) || got[0] != pending[0] || got[1] != pending[1] {
		t.Fatalf("startup pending wakes = %+v, want %+v", got, pending)
	}

	clock.Advance(expirationDetectionInterval - time.Millisecond)
	if runs := runner.RunCount(); runs != 1 {
		t.Fatalf("runs before deadline = %d, want 1", runs)
	}
	clock.Advance(time.Millisecond)
	waitForExpirationRuns(t, runner, 2)
	if got := runner.RunTimes(); len(got) != 2 || !got[0].Equal(time.Unix(10_000, 0).UTC()) ||
		!got[1].Equal(time.Unix(10_060, 0).UTC()) {
		t.Fatalf("sweep starts = %v", got)
	}
	if reads := runner.PendingReads(); reads != 1 {
		t.Fatalf("pending reads after recurring sweep = %d, want startup-only read", reads)
	}

	cancel()
	if err := waitForExpirationResult(t, result); err != nil {
		t.Fatalf("RunExpirationScheduler() cancellation error = %v", err)
	}
	waitForExpirationTimers(t, clock, 0)
}

func TestRunExpirationSchedulerImmediatelyCatchesUpAfterSlowSweep(t *testing.T) {
	clock := newExpirationManualClock(time.Unix(20_000, 0).UTC())
	runner := newExpirationSchedulerRunner(clock)
	runner.advanceOnRun[1] = expirationDetectionInterval + time.Second
	service := newExpirationSchedulerService(t, runner, &expirationWakeRecorder{}, clock)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.RunExpirationScheduler(ctx) }()

	waitForExpirationRuns(t, runner, 2)
	if got := runner.RunTimes(); len(got) < 2 || !got[1].Equal(time.Unix(20_061, 0).UTC()) {
		t.Fatalf("catch-up sweep starts = %v", got)
	}
	waitForExpirationTimers(t, clock, 1)
	cancel()
	if err := waitForExpirationResult(t, result); err != nil {
		t.Fatalf("RunExpirationScheduler() cancellation error = %v", err)
	}
	waitForExpirationTimers(t, clock, 0)
}

func TestRunExpirationSchedulerFailsFast(t *testing.T) {
	expireFailure := errors.New("injected expiration failure")
	pendingFailure := errors.New("injected pending read failure")
	wakeFailure := errors.New("injected pending wake failure")
	nodeID := core.NodeID("0123456789abcdef0123456789abcdef")
	tests := []struct {
		name       string
		configure  func(*expirationSchedulerRunner, *expirationWakeRecorder)
		want       error
		wantReads  int
		wantRuns   int
		wantTimers int
	}{
		{
			name: "expiration transaction",
			configure: func(runner *expirationSchedulerRunner, _ *expirationWakeRecorder) {
				runner.runErrors[1] = expireFailure
			},
			want: expireFailure, wantRuns: 1,
		},
		{
			name: "pending read",
			configure: func(runner *expirationSchedulerRunner, _ *expirationWakeRecorder) {
				runner.pendingError = pendingFailure
			},
			want: pendingFailure, wantReads: 1, wantRuns: 1,
		},
		{
			name: "pending wake",
			configure: func(runner *expirationSchedulerRunner, wakes *expirationWakeRecorder) {
				runner.pending = []TargetEnforcementChange{{
					NodeID: nodeID, Target: netip.MustParsePrefix("198.51.100.8/32"),
				}}
				wakes.err = wakeFailure
			},
			want: wakeFailure, wantReads: 1, wantRuns: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := newExpirationManualClock(time.Unix(30_000, 0).UTC())
			runner := newExpirationSchedulerRunner(clock)
			wakes := &expirationWakeRecorder{}
			test.configure(runner, wakes)
			service := newExpirationSchedulerService(t, runner, wakes, clock)
			err := service.RunExpirationScheduler(context.Background())
			if !errors.Is(err, test.want) {
				t.Fatalf("RunExpirationScheduler() error = %v, want %v", err, test.want)
			}
			if runs := runner.RunCount(); runs != test.wantRuns {
				t.Fatalf("run count = %d, want %d", runs, test.wantRuns)
			}
			if reads := runner.PendingReads(); reads != test.wantReads {
				t.Fatalf("pending reads = %d, want %d", reads, test.wantReads)
			}
			if timers := clock.TimerCount(); timers != test.wantTimers {
				t.Fatalf("active timers = %d, want %d", timers, test.wantTimers)
			}
		})
	}
}

func TestRunExpirationSchedulerRequiresPendingRecoveryCapability(t *testing.T) {
	clock := newExpirationManualClock(time.Unix(40_000, 0).UTC())
	runner := expirationTransactionRunnerFunc(func(context.Context, func(LifecycleTransaction) error) error {
		return nil
	})
	service := newExpirationSchedulerService(t, runner, &expirationWakeRecorder{}, clock)
	err := service.RunExpirationScheduler(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot read pending target enforcement changes") {
		t.Fatalf("RunExpirationScheduler() error = %v", err)
	}
	if timers := clock.TimerCount(); timers != 0 {
		t.Fatalf("active timers = %d, want 0", timers)
	}
}

func TestStartupPendingRedeliveryExcludesCurrentExpirationChanges(t *testing.T) {
	nodeID := core.NodeID("0123456789abcdef0123456789abcdef")
	justWoken := TargetEnforcementChange{
		NodeID: nodeID, Target: netip.MustParsePrefix("198.51.100.40/32"), Generation: 3,
	}
	previouslyPending := TargetEnforcementChange{
		NodeID: nodeID, Target: netip.MustParsePrefix("198.51.100.41/32"), Generation: 7,
	}
	differentGeneration := TargetEnforcementChange{
		NodeID: nodeID, Target: justWoken.Target, Generation: 2,
	}
	filtered := excludeCommittedTargetChanges(
		[]TargetEnforcementChange{justWoken, previouslyPending, differentGeneration},
		[]TargetEnforcementChange{justWoken},
	)
	if len(filtered) != 2 || filtered[0] != previouslyPending || filtered[1] != differentGeneration {
		t.Fatalf("startup pending redelivery = %+v", filtered)
	}
	wakes := &expirationWakeRecorder{}
	if err := WakeCommittedTargets(context.Background(), wakes, filtered); err != nil {
		t.Fatal(err)
	}
	if got := wakes.Changes(); len(got) != 2 || got[0].Target != previouslyPending.Target ||
		got[1].Target != differentGeneration.Target {
		t.Fatalf("startup pending wakes = %+v", got)
	}
}

func TestRunExpirationSchedulerTreatsCancellationAsNormalExit(t *testing.T) {
	clock := newExpirationManualClock(time.Unix(50_000, 0).UTC())
	runner := newExpirationSchedulerRunner(clock)
	service := newExpirationSchedulerService(t, runner, &expirationWakeRecorder{}, clock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.RunExpirationScheduler(ctx); err != nil {
		t.Fatalf("RunExpirationScheduler() error = %v", err)
	}
	if timers := clock.TimerCount(); timers != 0 {
		t.Fatalf("active timers = %d, want 0", timers)
	}
}

func TestPrepareExpirationStartupRunsOnceWithoutWakeOrPendingRead(t *testing.T) {
	clock := newExpirationManualClock(time.Unix(60_000, 0).UTC())
	runner := newExpirationSchedulerRunner(clock)
	runner.pending = []TargetEnforcementChange{{
		NodeID: core.NodeID("0123456789abcdef0123456789abcdef"),
		Target: netip.MustParsePrefix("192.0.2.50/32"),
	}}
	wakes := &expirationWakeRecorder{}
	service := newExpirationSchedulerService(t, runner, wakes, clock)

	startedAt, err := service.PrepareExpirationStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(60_000, 0).UTC(); !startedAt.Equal(want) {
		t.Fatalf("startup sweep started at %v, want %v", startedAt, want)
	}
	if runs := runner.RunCount(); runs != 1 {
		t.Fatalf("startup expiration runs = %d, want 1", runs)
	}
	if reads := runner.PendingReads(); reads != 0 {
		t.Fatalf("startup preparation pending reads = %d, want 0", reads)
	}
	if got := wakes.Changes(); len(got) != 0 {
		t.Fatalf("startup preparation emitted wakes: %+v", got)
	}
}

func TestRunExpirationSchedulerAfterStartupWaitsBeforeFirstSweep(t *testing.T) {
	clock := newExpirationManualClock(time.Unix(70_000, 0).UTC())
	runner := newExpirationSchedulerRunner(clock)
	service := newExpirationSchedulerService(t, runner, &expirationWakeRecorder{}, clock)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- service.RunExpirationSchedulerAfterStartup(ctx, time.Unix(70_000, 0).UTC())
	}()

	waitForExpirationTimers(t, clock, 1)
	clock.Advance(expirationDetectionInterval - time.Millisecond)
	if runs := runner.RunCount(); runs != 0 {
		t.Fatalf("recurring scheduler ran before first deadline: %d", runs)
	}
	clock.Advance(time.Millisecond)
	waitForExpirationRuns(t, runner, 1)
	if got := runner.RunTimes(); len(got) != 1 || !got[0].Equal(time.Unix(70_060, 0).UTC()) {
		t.Fatalf("first recurring sweep = %v", got)
	}

	cancel()
	if err := waitForExpirationResult(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestRunExpirationSchedulerAfterSlowStartupKeepsOriginalDeadline(t *testing.T) {
	clock := newExpirationManualClock(time.Unix(80_000, 0).UTC())
	runner := newExpirationSchedulerRunner(clock)
	runner.advanceOnRun[1] = 30 * time.Second
	service := newExpirationSchedulerService(t, runner, &expirationWakeRecorder{}, clock)
	startupSweepStartedAt, err := service.PrepareExpirationStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- service.RunExpirationSchedulerAfterStartup(ctx, startupSweepStartedAt)
	}()
	waitForExpirationTimers(t, clock, 1)
	clock.Advance(30*time.Second - time.Millisecond)
	if runs := runner.RunCount(); runs != 1 {
		t.Fatalf("recurring scheduler ran before original deadline: %d", runs)
	}
	clock.Advance(time.Millisecond)
	waitForExpirationRuns(t, runner, 2)
	if got := runner.RunTimes(); len(got) != 2 || !got[1].Equal(time.Unix(80_060, 0).UTC()) {
		t.Fatalf("recurring sweep starts = %v", got)
	}

	cancel()
	if err := waitForExpirationResult(t, result); err != nil {
		t.Fatal(err)
	}
}

type expirationTransactionRunnerFunc func(context.Context, func(LifecycleTransaction) error) error

func (f expirationTransactionRunnerFunc) RunDecisionTransaction(
	ctx context.Context,
	operation func(LifecycleTransaction) error,
) error {
	return f(ctx, operation)
}

type expirationSchedulerRunner struct {
	mu           sync.Mutex
	clock        *expirationManualClock
	runTimes     []time.Time
	runErrors    map[int]error
	advanceOnRun map[int]time.Duration
	pending      []TargetEnforcementChange
	pendingError error
	pendingReads int
}

func newExpirationSchedulerRunner(clock *expirationManualClock) *expirationSchedulerRunner {
	return &expirationSchedulerRunner{
		clock: clock, runErrors: make(map[int]error), advanceOnRun: make(map[int]time.Duration),
	}
}

func (r *expirationSchedulerRunner) RunDecisionTransaction(
	ctx context.Context,
	_ func(LifecycleTransaction) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	r.runTimes = append(r.runTimes, r.clock.Now())
	run := len(r.runTimes)
	err := r.runErrors[run]
	advance := r.advanceOnRun[run]
	r.mu.Unlock()
	if advance > 0 {
		r.clock.Advance(advance)
	}
	return err
}

func (r *expirationSchedulerRunner) PendingTargetEnforcementChanges(
	ctx context.Context,
) ([]TargetEnforcementChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingReads++
	return append([]TargetEnforcementChange(nil), r.pending...), r.pendingError
}

func (r *expirationSchedulerRunner) RunCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runTimes)
}

func (r *expirationSchedulerRunner) RunTimes() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.runTimes...)
}

func (r *expirationSchedulerRunner) PendingReads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingReads
}

type expirationWakeRecorder struct {
	mu      sync.Mutex
	changes []TargetEnforcementChange
	err     error
}

func (r *expirationWakeRecorder) WakeTarget(
	_ context.Context,
	nodeID core.NodeID,
	target netip.Prefix,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, TargetEnforcementChange{NodeID: nodeID, Target: target})
	return r.err
}

func (r *expirationWakeRecorder) Changes() []TargetEnforcementChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]TargetEnforcementChange(nil), r.changes...)
}

func newExpirationSchedulerService(
	t *testing.T,
	runner TransactionRunner,
	wake TargetWakeSink,
	schedulerClock appclock.Clock,
) *LifecycleService {
	t.Helper()
	finalizer, err := NewDesiredStateFinalizer(TargetPolicyResolverFunc(
		func(context.Context, DesiredStateTransaction, core.DesiredBanProjection) (enforcement.TargetPolicy, error) {
			return enforcement.TargetPolicy{}, nil
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewLifecycleServiceWithClock(runner, finalizer, wake, schedulerClock)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func waitForExpirationRuns(t *testing.T, runner *expirationSchedulerRunner, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runner.RunCount() >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expiration run count = %d, want at least %d", runner.RunCount(), count)
}

func waitForExpirationTimers(t *testing.T, clock *expirationManualClock, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if clock.TimerCount() == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active timer count = %d, want %d", clock.TimerCount(), count)
}

func waitForExpirationResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("expiration scheduler did not stop")
		return nil
	}
}

type expirationManualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*expirationManualTimer]time.Time
}

func newExpirationManualClock(now time.Time) *expirationManualClock {
	return &expirationManualClock{now: now, timers: make(map[*expirationManualTimer]time.Time)}
}

func (c *expirationManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *expirationManualClock) NewTimer(delay time.Duration) appclock.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &expirationManualTimer{clock: c, ch: make(chan time.Time, 1), active: true}
	c.timers[timer] = c.now.Add(delay)
	return timer
}

func (c *expirationManualClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	now := c.now
	ready := make([]*expirationManualTimer, 0)
	for timer, deadline := range c.timers {
		if timer.active && !deadline.After(now) {
			timer.active = false
			delete(c.timers, timer)
			ready = append(ready, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range ready {
		timer.ch <- now
	}
}

func (c *expirationManualClock) TimerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

type expirationManualTimer struct {
	clock  *expirationManualClock
	ch     chan time.Time
	active bool
}

func (t *expirationManualTimer) C() <-chan time.Time { return t.ch }

func (t *expirationManualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	delete(t.clock.timers, t)
	return wasActive
}
