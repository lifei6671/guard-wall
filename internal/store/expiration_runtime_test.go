package store

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appclock "github.com/lifei6671/guard-wall/internal/clock"
	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
	"github.com/lifei6671/guard-wall/internal/reconcile"
)

func TestSQLiteExpirationRuntimeRemovesFakeTargetWithin62Seconds(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	seedNodeAndRule(t, ctx, database)
	start := time.Unix(100_000, 0).UTC()
	clock := newRuntimeManualClock(start)
	target := netip.MustParsePrefix("198.51.100.123/32")
	// SQLite persists decision timestamps at microsecond precision. Keep the
	// expiry one full second after startup so the startup sweep cannot truncate
	// it back to the current instant.
	expiresAt := start.Add(time.Second)
	seedService, err := decision.NewLifecycleServiceWithClock(
		database, newTestDesiredStateFinalizer(t), noOpTargetWakeSink(), clock,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedService.BanManual(ctx, decision.ManualRequest{
		DecisionID: "manual-runtime-expiry", NodeID: testNodeID, Target: target,
		CreatedAt: start, ExpiresAt: &expiresAt,
	}, false); err != nil {
		t.Fatal(err)
	}

	backend := newBlockingTargetConfirmationBackend(target)
	t.Cleanup(backend.ReleaseTargetConfirmation)
	controller, err := reconcile.NewPersistentController(
		ctx, testNodeID, backend, clock, runtimeCriticalAudit{}, database,
	)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := reconcile.NewDesiredPlanProvider(
		testNodeID,
		controller,
		database,
		reconcile.StaticDesiredFirewallState{
			InfrastructureRevision: 1,
			PolicyRevision:         1,
			Infrastructure: core.ManagedInfrastructureIntent{
				Backend: "fake", OwnerVersion: "v1", Digest: "infra-v1",
			},
			Policy: core.ManagedPolicyIntent{RelationDigest: "policy-v1"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := reconcile.NewDispatcherWithClock(controller, provider, 8, clock)
	if err != nil {
		t.Fatal(err)
	}
	wake, err := reconcile.NewDispatcherTargetWakeSink(testNodeID, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := decision.NewLifecycleServiceWithClock(
		database, newTestDesiredStateFinalizer(t), wake, clock,
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := reconcile.NewExpirationRuntime(lifecycle, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- runtime.Run(runCtx) }()

	select {
	case <-backend.targetConfirmationEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("target confirmation Probe did not block after physical Apply")
	}
	waitForRuntimeTimers(t, clock, 1)
	clock.Advance(59 * time.Second)
	assertRuntimeTarget(t, backend.Backend, target, true)
	clock.Advance(time.Second)
	waitForRuntimeTargetGeneration(t, database, target, 2)
	backend.ReleaseTargetConfirmation()
	waitForRuntimeTargetOrFailure(t, backend.Backend, target, false, runDone)
	waitForRuntimeTargetStatus(t, database, target, "converged")
	if elapsed := clock.Now().Sub(expiresAt); elapsed > 62*time.Second {
		t.Fatalf("Fake target removal lag = %s, want <= 62s", elapsed)
	}
	assertDecisionState(t, database, "manual-runtime-expiry", "expired", "expired")
	assertDesiredTargetState(t, database, target, "absent", 2, 2, "converged", 0, 1)
	_, applies := backend.Counts()
	if applies != 4 {
		t.Fatalf("Fake Backend applies = %d, want startup infrastructure + policy + target present + absent", applies)
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("expiration runtime did not stop")
	}
}

func waitForRuntimeTargetGeneration(
	t *testing.T,
	database *Store,
	target netip.Prefix,
	want core.TargetEnforcementGeneration,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var generation int64
		err := database.db.QueryRowContext(context.Background(), `
			SELECT target_enforcement_generation
			FROM target_reconcile_state
			WHERE node_id = ? AND canonical_target = ?`,
			string(testNodeID), target.String()).Scan(&generation)
		if err != nil {
			t.Fatal(err)
		}
		if core.TargetEnforcementGeneration(generation) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime target generation did not become %d", want)
}

func waitForRuntimeTargetStatus(t *testing.T, database *Store, target netip.Prefix, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		err := database.db.QueryRowContext(context.Background(), `
			SELECT status
			FROM target_reconcile_state
			WHERE node_id = ? AND canonical_target = ?`,
			string(testNodeID), target.String()).Scan(&status)
		if err != nil {
			t.Fatal(err)
		}
		if status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime target status did not become %q", want)
}

type runtimeCriticalAudit struct{}

func (runtimeCriticalAudit) AppendCriticalAudit(context.Context, reconcile.CriticalAuditEvent) error {
	return nil
}

type blockingTargetConfirmationBackend struct {
	*fake.Backend
	target                    netip.Prefix
	targetConfirmationEntered chan struct{}
	releaseTargetConfirmation chan struct{}
	blocked                   atomic.Bool
	releaseOnce               sync.Once
}

func newBlockingTargetConfirmationBackend(target netip.Prefix) *blockingTargetConfirmationBackend {
	return &blockingTargetConfirmationBackend{
		Backend:                   fake.NewBackend(),
		target:                    target,
		targetConfirmationEntered: make(chan struct{}),
		releaseTargetConfirmation: make(chan struct{}),
	}
}

func (b *blockingTargetConfirmationBackend) Probe(ctx context.Context) (fake.Snapshot, error) {
	snapshot, err := b.Backend.Probe(ctx)
	if err != nil {
		return fake.Snapshot{}, err
	}
	if _, present := snapshot.Targets[b.target]; !present || !b.blocked.CompareAndSwap(false, true) {
		return snapshot, nil
	}
	close(b.targetConfirmationEntered)
	select {
	case <-b.releaseTargetConfirmation:
		return snapshot, nil
	case <-ctx.Done():
		return fake.Snapshot{}, ctx.Err()
	}
}

func (b *blockingTargetConfirmationBackend) ReleaseTargetConfirmation() {
	b.releaseOnce.Do(func() { close(b.releaseTargetConfirmation) })
}

func waitForRuntimeTarget(t *testing.T, backend *fake.Backend, target netip.Prefix, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := backend.Probe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, exists := snapshot.Targets[target]
		if exists == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	assertRuntimeTarget(t, backend, target, want)
}

func waitForRuntimeTargetOrFailure(
	t *testing.T,
	backend *fake.Backend,
	target netip.Prefix,
	want bool,
	runDone <-chan error,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-runDone:
			t.Fatalf("expiration runtime stopped during target reconciliation: %v", err)
		default:
		}
		snapshot, err := backend.Probe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, exists := snapshot.Targets[target]
		if exists == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	assertRuntimeTarget(t, backend, target, want)
}

func assertRuntimeTarget(t *testing.T, backend *fake.Backend, target netip.Prefix, want bool) {
	t.Helper()
	snapshot, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, exists := snapshot.Targets[target]
	if exists != want {
		t.Fatalf("Fake target present = %t, want %t", exists, want)
	}
}

func waitForRuntimeTimers(t *testing.T, clock *runtimeManualClock, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if clock.TimerCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime active timers = %d, want %d", clock.TimerCount(), want)
}

type runtimeManualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*runtimeManualTimer]time.Time
}

func newRuntimeManualClock(now time.Time) *runtimeManualClock {
	return &runtimeManualClock{now: now, timers: make(map[*runtimeManualTimer]time.Time)}
}

func (c *runtimeManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *runtimeManualClock) NewTimer(delay time.Duration) appclock.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &runtimeManualTimer{clock: c, ch: make(chan time.Time, 1), active: true}
	c.timers[timer] = c.now.Add(delay)
	return timer
}

func (c *runtimeManualClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	now := c.now
	ready := make([]*runtimeManualTimer, 0)
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

func (c *runtimeManualClock) TimerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

type runtimeManualTimer struct {
	clock  *runtimeManualClock
	ch     chan time.Time
	active bool
}

func (t *runtimeManualTimer) C() <-chan time.Time { return t.ch }

func (t *runtimeManualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	delete(t.clock.timers, t)
	return wasActive
}
