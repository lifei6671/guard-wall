# C2 Decision/Enforcement Fake Slice — Worktree Evidence

- Evidence state: `PASS_WITH_UNVERIFIED_DOMAINS`
- Maturity: `worktree_preliminary`
- Repository baseline: `master @ 0a27e01394dc290588d5a66447fc5763f7f6c8e4 (dirty)`
- Environment: `go1.27.0 windows/amd64`, race enabled
- Contract status: `C2 IN_PROGRESS / Implemented`; not `Verified`
- Previous Manual/expiry batch user code review: `PASS` on 2026-08-30.
- Availability recovery primitive user code review: `PASS` on 2026-08-30; complete C2 remains in progress.
- Repeated Apply unavailability budget user code review: `PASS` on 2026-08-30.
- Exhausted Unknown observation-only recovery user code review: `PASS` on 2026-08-30.
- Retry/pending-Probe SQLite close/reopen recovery user code review: `PASS` on 2026-08-30.
- Health-event/wakeup Dispatcher primitive user code review: `PASS` on 2026-08-30; complete C2 remains in progress.
- Desired generation/snapshot + post-commit Wake batch: `DONE / Implemented`; user Code Review passed.
- Expiration scheduler + first-Apply fence batch: `DONE / Implemented`; user Code Review passed.
- Expiry 62s runtime composition batch: `DONE / Implemented`; user Code Review passed.
- Backend health monitor + runtime ownership primitive: `DONE / Implemented`; user Code Review passed.

## Commands

```powershell
go test -race ./internal/reconcile -run 'Test(BackendHealthRecoveryProbesBeforeRetryWithoutResettingBudget|RepeatedApplyUnavailabilityDoesNotPermitSeventhMutationAfterBudgetExhaustion|ExhaustedUnknownCanConvergeByProbeWithoutSeventhMutation)$' -count=20
go test -race ./internal/reconcile -run 'TestDispatcher' -count=20
go test -race ./internal/store -run 'Test(SQLiteExpiryBatchRebuildsEachTargetOnceAndIsIdempotent|LifecycleServiceWakesOnlyChangedTargetsAfterConfirmedCommit|SQLiteExpiryMultipleTargetsAdvancesSnapshotOnce|SQLiteIntentWriteFailureRollsBackDecisionProjectionAndAudit|SQLiteSnapshotRevisionExhaustionRollsBackEntireDecisionTransaction|MigrationV4UpgradesLegacyDesiredStateWithoutRevisionOrRetryRegression|PutTargetEnforcementIntentRejectsGenerationBeyondSQLiteInteger)$' -count=20
go test -race ./internal/processor -run 'TestSQLite(AutomaticCommitUnknownReadbackWakesProvenCommit|AutomaticPostCommitWakeFailurePreservesCompletion|PipelineAutomaticDecisionCreateAndDuplicateSuppression|BaseAdapterRejectsAutomaticDecisionWithoutDesiredStateDependencies)$' -count=20
go test -race ./internal/decision -run TestWakeCommittedTargetsReportsFailedAndPendingSuffix -count=50
go test -race ./internal/decision ./internal/reconcile
go test -race ./internal/decision -run 'TestRunExpirationScheduler' -count=20
go test -race ./internal/reconcile -run 'Test(ExpiredPresentTargetIsFencedBeforeProbeOrAttempt|ExpiredStaleTargetReturnsStalePlan|TargetExpiringAfterAttemptPersistenceDoesNotApply|TargetExpiringDuringRequiredProbeDoesNotBeginAnotherAttempt|DispatcherTreatsExpiredTargetAsNoOpAndContinues|DispatcherRefreshesExpiredStalePlanBeforeNoOp)$' -count=20
go test -race ./internal/store -run 'TestSQLiteExpirationSchedulerStartsWithDueSweepBeforePendingRecovery' -count=20
go test -race ./internal/decision -run 'Test(PrepareExpirationStartup|RunExpirationSchedulerAfterStartup)' -count=20
go test -race ./internal/reconcile -run 'Test(DesiredPlanProvider|ExpirationRuntime|DispatcherWakeRefreshesPastConvergedGeneration)' -count=20
go test -race ./internal/store -run TestSQLiteExpirationRuntimeRemovesFakeTargetWithin62Seconds -count=20
go test -race ./internal/store -run '^TestSQLiteExpirationRuntimeRemovesFakeTargetWithin62Seconds$' -count=20 '-cpu=1,16'
go test -race ./internal/processor -run 'TestSQLite(BaseAdapterReceiptReplayIgnoresUnrelatedPendingTarget|AutomaticPostCommitWakeFailurePreservesCompletion)$' -count=20
go test -race ./internal/reconcile -count=5
go test -race ./internal/reconcile -run 'TestBackendHealth' -count=20
go test -race ./internal/reconcile -run 'TestBackendHealthTimerRechecksDeadlineInsideOperationGate|TestBackendHealthyFailureDuringStartupBackoffDoesNotBypassNewDeadline|TestBackendHealthyFailureCannotBeOverwrittenByEmptyStartupClassification|TestBackendHealthFailureGatesQueuedMutationUntilRecoveryProbe' -count=50
go test -race -count=5 ./internal/core ./internal/reconcile ./internal/store
go test -race -count=1 ./...
go vet ./...
go mod verify
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'; go build ./...
$env:GOOS='linux'; $env:GOARCH='arm64'; $env:CGO_ENABLED='0'; go build ./...
gofmt -d internal/decision/lifecycle.go internal/decision/expiration_scheduler.go internal/decision/expiration_scheduler_test.go internal/reconcile/controller.go internal/reconcile/controller_test.go internal/reconcile/dispatcher.go internal/reconcile/dispatcher_test.go internal/store/decision_lifecycle_test.go
git diff --check
```

The commands in this accumulated worktree Evidence were executed across 2026-08-30 and
2026-08-31. The stale-completion and receipt-replay repair commands, uncached full race suite,
vet, module verification and Linux cross-builds passed on 2026-08-31. The cross-builds exited 0
with sandbox-denied Go module stat-cache warnings; they remain cross-build evidence only. The
dirty-worktree diff check reported only line-ending conversion warnings and no whitespace error.

## Implemented and observed

- Decision/Projection aggregation and finite/permanent expiry behavior;
- Automatic identity/suppression with required frozen RuleVersion and out-of-order triggers;
- global DecisionID uniqueness across history and sources;
- Manual typed AlreadyBanned, replace, revoke, and terminal conflict behavior;
- Allowlist/Protected Policy kept orthogonal to Decision lifecycle;
- IPv4/IPv6 CIDR union coverage and stable Policy relation digest;
- target generation changes only with Firewall-significant intent;
- Plan digest binds payload, fences, and optional physical snapshot basis;
- Controller rejects a validly re-digested but non-authoritative desired payload;
- Unknown result requires an authoritative Probe before another mutation;
- Probe-confirmed Unknown converges without a second Apply or attempt;
- Confirmed Apply converges only after physical postcondition comparison;
- Infrastructure, Policy, and Target retry ledgers are isolated;
- target completion fencing rejects stale generations without coupling unrelated snapshots;
- administrator Retry publishes a new epoch only after Critical Audit succeeds;
- invalid plans degrade without consuming a backend mutation attempt;
- Absent intent clears timeout and policy relation attributes.
- typed Automatic Decision, Desired Projection, Critical Audit and processing receipt can commit
  atomically through the Coordinator SQLite UnitOfWork; any child-write failure rolls back all rows.
- transaction-aware Automatic create/suppress is reached only through the semantic port; v1→v2
  duplicate preserves first ID/RuleVersion/Alert/expiry/UpdatedAt and leaves unchanged Projection
  revision while updating only last-triggered and suppression count.
- real SQLite tests cover candidate DecisionID conflict, different-Window concurrent suppression,
  receipt-failure rollback/retry, commit-unknown persisted/rolled-back branches and receipt replay.
- preliminary SQLite Manual lifecycle tests cover first create, same/new-ID duplicate
  `AlreadyBanned`, global DecisionID conflict, explicit `Revoked/manual_replace`, concurrent create
  uniqueness, concurrent replace history/audit chaining, Projection revision, and Critical Audit in
  one short transaction; injected Audit failure rolls the entire replace back.
- expiry uses a first-write `UPDATE ... RETURNING` batch, expires only `ExpiresAt <= now`, rebuilds
  each affected Target Projection once, and is idempotent on replay; injected Audit failure rolls
  the batch back. Commit acknowledgement loss has a typed `ErrCommitUnknown` and preserves the
  expected stable-ID result for authoritative readback.
- the M0 Fake path derives `NativeExpiry = EffectiveUntil + 5m`, refreshes it when EffectiveUntil
  moves, clears it for permanent Targets, and rejects the unadjusted expiry as converged drift.
- a same-Controller Backend availability recovery primitive preserves the consumed attempt when
  Apply and the required Probe fail, blocks another mutation while unavailable, then performs
  Probe-before-retry and converges at attempt 2 after the caller drives Execute again.
- for one unchanged Target generation/RetryEpoch, six availability-related Apply failures retain
  the exact `1s / 5s / 30s / 5m / 15m` schedule and end Degraded; a later successful Probe against
  still-unmatched physical state returns budget exhausted without a seventh mutation or Retry Audit.
- after six non-mutating Unknown results exhaust the same Target key, a later authoritative Probe
  of a complete matching physical state may change Degraded to Converged observation-only; attempt
  and Apply counts remain 6, the pending Probe clears, and no administrator Retry Audit is written.
- additive migration `0003_reconcile_restart_recovery.sql` persists one exact pending-Probe
  requirement, including its domain key, optional infrastructure snapshot fence, originating
  RetryEpoch and attempt ordinal; physical-key uniqueness prevents duplicate unresolved barriers.
- every external Apply is preceded by one SQLite transaction that writes Applying, increments the
  durable attempt and installs/replaces the exact Probe requirement; a persistence failure prevents
  the Backend call.
- a fresh persistent Controller can close and reopen the same SQLite database, hydrate all three
  retry domains, restore absolute retry time and Probe-first ordering, then either converge from a
  matching Probe without a second Apply or consume only the remaining budget after a mismatch.
- an administrator RetryEpoch does not delete an older ambiguous physical outcome; after reopen the
  older exact requirement is probed and atomically replaced by the new epoch attempt when needed.
- attempt 6/Degraded plus its pending Probe survives close/reopen; a mismatching recovery Probe does
  not permit a seventh mutation or silently create a new RetryEpoch.
- a hydrated Controller never derives `ConfirmedTarget` from durable Converged alone; before its
  first post-hydration mutation it performs an authoritative startup Probe, and clears that runtime
  barrier only after a matching Converged write or a mismatching pre-Apply transition is durable.
- a crash after durable Applying restores the original absolute backoff from LastAttemptAt; attempt
  6 restores as Degraded. A later Retry that supersedes an unresolved old ref can clear the old exact
  marker without regressing or overwriting the newer singleton/current ledger, with or without reopen.
- typed reconcile commit-unknown handling reads back the SQLite snapshot, distinguishes committed
  and rolled-back transitions, rebuilds in-memory ledgers/Probe barriers, and forces a deferred reload
  at the next mutation entry when immediate readback also fails.
- the exported runtime Dispatcher owns one bounded, cancelable queue and one absolute-deadline timer;
  wakeups coalesce by domain/Target key, and the worker reloads the current Plan after dequeue.
- Backend healthy events first perform an observation-only authoritative Probe. Matching pending
  outcomes converge without Apply; unresolved current keys are requeued without resetting RetryEpoch,
  attempt count, or a persisted future `NextAttemptAt`.
- startup recovery reads all provider keys, performs one authoritative snapshot comparison, confirms
  multiple matching keys without loading mutation Plans or calling Apply, and schedules only drifted
  keys. A drifted Converged key is explicitly forced into reconcile; ordinary RetryWaiting keys still
  obey their persisted absolute deadline.
- a startup mismatch does not clear an Unknown-result Probe barrier. If physical state converges while
  waiting for the future deadline, the deadline path performs Probe-only recovery and preserves both
  Apply count and attempt count.
- stale Plans receive at most one fresh reread, expired Probe failures do not hot-loop, and cancellation
  stops the startup feeder/worker while later wakeups fail explicitly.
- additive migration `0004_desired_firewall_authority.sql` adds the singleton authoritative
  SnapshotRevision, retains legacy retry/probe generation as a monotonic materialization floor, and rebuilds
  `enforcement_states` so Absent Intent requires an empty policy relation digest while partial/full
  coverage still requires a 64-character digest.
- Automatic, Manual and expiry now share one transaction-final Target finalizer. It re-reads each
  transaction-final Projection, resolves the normalized Intent from transaction-consistent or immutable
  policy input, increments only changed Target generations, resets only those Target retry ledgers, and
  increments the global SnapshotRevision exactly once when any Target changed.
- explanatory Projection-only changes and Automatic suppression preserve generation, SnapshotRevision,
  retry epoch/attempt budget and Wake count; multi-Target expiry assigns one shared SnapshotRevision.
- confirmed Decision commits Wake only changed Target keys through a node-bound Dispatcher adapter.
  Automatic commit-unknown wakes only after receipt readback proves the same transaction committed;
  rollback/unknown-without-proof emits no Wake. Typed post-commit Wake failures preserve the committed
  Manual result or processing completion. A later replay of the same durable processing receipt scans
  only materialized, generation-matched `Pending/attempt=0` Target state and re-emits the missed Wake.
- Store now reads one node's complete normalized Target Desired set and global SnapshotRevision from
  one read-only SQLite transaction. The production Desired PlanProvider publishes that same complete
  snapshot before startup Probe or queued retry-state filtering and builds the corresponding current Plan.
- scheduler, Dispatcher and Controller can share one injected Clock without changing the existing wall-clock
  constructors. The expiration runtime performs a no-wake startup sweep, starts Dispatcher recovery, and
  anchors the first recurring deadline to the startup sweep's original start time rather than its completion.
- a real SQLite integration test composes LifecycleService, PersistentController, production Desired
  PlanProvider, Dispatcher, expiration runtime, shared virtual Clock and Fake Backend. It observes the Target
  Present after startup, advances to the 60-second sweep, then observes Fake Snapshot Absent, expired Decision,
  generation/SnapshotRevision advancement and durable Converged state within the frozen 62-second bound.
- additive migration `0005_observed_firewall_state.sql` persists complete Infrastructure, Policy and Target
  physical observations. The v4 Target cache is deliberately invalidated during upgrade because it cannot
  reconstruct the complete physical state; Desired generations and retry/Probe recovery remain intact.
- every successful authoritative Probe writes a complete Observed snapshot with current Desired revision/
  generation confirmation only after exact physical comparison. Probe failure writes all domains Unknown;
  ambiguous Apply outcomes write only the affected domain Unknown and keep the exact Probe barrier.
- retry/probe convergence and Observed confirmation share one SQLite transition. Commit-unknown recovery reads
  back both durable recovery and Observed state; a reopened Controller seeds only the monotonic observation clock
  from cache and still requires a fresh startup Probe rather than trusting cached confirmation.
- production Desired startup keys now include Infrastructure, Policy and every current Target, so a clean runtime
  startup refreshes all Observed domains before applying only the drifted keys.
- the Dispatcher now owns the Backend health lifecycle. Startup Probe failure durably records complete Unknown
  Observed state, keeps the owner running in Degraded, retains all Infrastructure/Policy/Target keys, and retries
  at exact internal `1s / 5s / 30s / 5m / 15m` absolute deadlines without a hot loop or mutation while unhealthy.
- health Probes use a bounded child context while Unknown persistence uses the parent runtime context. A Backend
  failure remains retryable only when that persistence succeeds; persistence/fencing/invariant failures preserve
  both error roots and stop the Dispatcher instead of being hidden as Backend unavailability.
- `BackendHealthStatus` exposes the process-local `NotReady / Healthy / Degraded` state plus consecutive/total
  failure counters for future Health/Metric adapters without adding Config Schema, migration, dependency, IPC,
  permission, or executable wiring.
- startup classification is published through one barrier. Health operations share a single operation gate, and
  timer callbacks re-check the authoritative deadline while holding that gate, preventing external failures from
  being overwritten or bypassed by a stale timer. Cancellation tears down the same owner without a Probe/timer leak.

Independent read-only reviews found no P0/P1 in the earlier preliminary implemented scope, including
same-Controller availability recovery, bounded repeated Apply unavailability, and exhausted
observation-only recovery. The retry/pending-Probe Store and Controller integration both passed
independent final reviews with no P0/P1/P2. The new Dispatcher primitive passed independent contract
and concurrency review for its implemented boundary. The Desired generation/snapshot + post-commit Wake
batch passed three independent final reviews with no remaining P0/P1/P2/P3 and the user Code Review.
The follow-up expiration batch adds a startup-immediate, absolute-60-second scheduler, durable pending-work
redelivery, and a first-Apply expiry fence. An independent review found one stale-expired Plan P1; Repair
Round 1 now returns `ErrStalePlan` before the expired no-op, forces one fresh Plan reread, fences again after
Probe/before attempt and immediately before Apply, and records the post-Applying narrow-window case as
`Degraded/expired_before_apply` without a false Probe barrier. Three fresh independent re-reviews report
no remaining P0/P1/P2/P3 for the implemented boundary.
The 62s runtime batch passed independent Store, Plan/Dispatcher/E2E and clock/scheduler/runtime reviews.
One slow-start deadline P1 and one spontaneous-cancellation P1 were repaired; dual-component errors now
preserve both identities. Fresh final reviews report no remaining P0/P1/P2/P3.
The complete Observed persistence/startup batch passed independent Controller and Store Tier-3 reviews.
One Store time-fence P1 and one schema NULL-time P2 were repaired; fresh reviews report no remaining
P0/P1/P2/P3 for the implemented boundary. The user Code Review explicitly accepted this batch.
The Backend health monitor checkpoint and final code reviews found and repaired three startup/timer/event ordering P1 findings.
Fresh code, test/evidence and integration reviews report no remaining P0/P1/P2/P3 for the frozen delivery;
the user Code Review explicitly accepted this batch.
The 2026-08-31 repair classifies an old Target completion as stale only after SQLite reports a
regression and authoritative recovery readback proves that the same Target generation advanced.
It then clears the superseded Probe by its exact old physical key and rebuilds the current Plan,
while unrelated persistence/readback failures remain fatal. The deterministic SQLite→Fake test
blocks the old confirmation Probe until expiry commits generation 2 and proves the Dispatcher
continues to remove the Target. Base processing adapters without a Target wake sink now skip
global pending-Target redelivery during receipt replay; enforcing adapters retain redelivery.

## Not verified

- real nftables/iptables/firewalld behavior and privileged IPC;
- complete TargetPrepared safety ordering and real Backend native-timeout expiry/precision tolerance;
- real Enforcer/IPC health-event source, executable process composition/startup ownership, real process/service restart
  startup ordering, crash injection and target-Linux durability; the current proof drives the
  complete SQLite→Fake expiration runtime and process-local health owner in-process, but does not use a real
  Backend health source or restart a process;
- a real SQLite driver-level `tx.Commit()` acknowledgement-loss injection; committed/rolled-back
  branches are currently exercised through the Store port around real SQLite transitions;
- C3 crash matrix.

These omissions keep C2 at `IN_PROGRESS / Implemented`.
