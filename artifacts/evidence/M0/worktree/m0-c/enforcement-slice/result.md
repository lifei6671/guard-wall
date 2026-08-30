# C2 Decision/Enforcement Fake Slice — Worktree Evidence

- Evidence state: `PASS_WITH_UNVERIFIED_DOMAINS`
- Maturity: `worktree_preliminary`
- Repository: `master @ 2dbc986fb38c8c8b4f4904df2241792f516f4228 (dirty)`
- Environment: `go1.27.0 windows/amd64`, race enabled
- Contract status: `C2 IN_PROGRESS / Implemented`; not `Verified`
- Previous Manual/expiry batch user code review: `PASS` on 2026-08-30.
- Availability recovery primitive user code review: `PASS` on 2026-08-30; complete C2 remains in progress.
- Repeated Apply unavailability budget user code review: `PASS` on 2026-08-30.
- Exhausted Unknown observation-only recovery user code review: `PASS` on 2026-08-30.
- Retry/pending-Probe SQLite close/reopen recovery user code review: `PASS` on 2026-08-30.
- Health-event/wakeup Dispatcher primitive user code review: `PASS` on 2026-08-30; complete C2 remains in progress.

## Commands

```powershell
go test -race ./internal/reconcile -run 'Test(BackendHealthRecoveryProbesBeforeRetryWithoutResettingBudget|RepeatedApplyUnavailabilityDoesNotPermitSeventhMutationAfterBudgetExhaustion|ExhaustedUnknownCanConvergeByProbeWithoutSeventhMutation)$' -count=20
go test -race ./internal/reconcile -run 'TestDispatcher' -count=20
go test -race ./internal/reconcile -count=5
go test -race -count=5 ./internal/core ./internal/reconcile ./internal/store
go test -race ./...
go vet ./...
go mod verify
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'; go build ./...
$env:GOOS='linux'; $env:GOARCH='arm64'; $env:CGO_ENABLED='0'; go build ./...
gofmt -d internal/reconcile/controller.go internal/reconcile/dispatcher.go internal/reconcile/dispatcher_test.go
git diff --check -- internal/reconcile/controller.go artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md docs/development/phase-1/STATUS.md
```

All delivery-scoped commands passed on 2026-08-30. The scoped diff check reported only line-ending
conversion warnings and no whitespace error. A full dirty-worktree diff check still reports trailing
whitespace in unrelated pre-existing `internal/config` changes, which this batch did not modify.

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

Independent read-only reviews found no P0/P1 in this preliminary implemented scope, including
same-Controller availability recovery, bounded repeated Apply unavailability, and exhausted
observation-only recovery. The retry/pending-Probe Store and Controller integration both passed
independent final reviews with no P0/P1/P2. The new Dispatcher primitive passed independent contract
and concurrency review for its implemented boundary; production event sources and the remaining
lifecycle domains stay unverified.

## Not verified

- complete Observed Firewall state persistence and authoritative startup reconstruction;
- real nftables/iptables/firewalld behavior and privileged IPC;
- complete TargetPrepared safety ordering and real Backend native-timeout expiry/precision tolerance;
- Manual/expiry same-transaction Target generation, Snapshot revision, retry-budget and post-commit wakeup;
- expiration scheduler, 60s/62s virtual-time bound, first-Apply expiry fence and maintenance;
- production health-event source/monitor, post-commit producer wiring, real process/service restart
  startup ordering, crash injection and target-Linux durability; the current proof drives the
  Dispatcher API, closes/reopens real SQLite and constructs a fresh Controller around the same
  in-memory Fake Backend, but does not restart a process;
- authoritative commit-unknown readback for complete Observed-state/runtime writeback;
- a real SQLite driver-level `tx.Commit()` acknowledgement-loss injection; committed/rolled-back
  branches are currently exercised through the Store port around real SQLite transitions;
- C3 crash matrix.

These omissions keep C2 at `IN_PROGRESS / Implemented`.
