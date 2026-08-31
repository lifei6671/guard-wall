# D1/D2 Code and Migration — Worktree Evidence

- Evidence state: `PASS_WITH_UNVERIFIED_DOMAINS`
- Maturity: `worktree_preliminary`
- Repository baseline: `master @ 0a27e01394dc290588d5a66447fc5763f7f6c8e4 (dirty)`
- Go: `go1.27.0 windows/amd64`
- Module: `github.com/lifei6671/guard-wall`
- SQLite driver: `modernc.org/sqlite v1.57.0`

## Commands and results

```powershell
go mod tidy
go mod verify
go test -race ./...
go vet ./...
go test -count=10 ./internal/config ./internal/decision ./internal/store ./internal/processor ./internal/source
go test -race -count=5 ./internal/core ./internal/store ./internal/reconcile
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
go test -race -count=1 ./...
$env:CGO_ENABLED='0'; $env:GOOS='linux'; $env:GOARCH='amd64'; go build ./...
$env:CGO_ENABLED='0'; $env:GOOS='linux'; $env:GOARCH='arm64'; go build ./...
git diff --check
```

The commands in this accumulated worktree Evidence were executed across 2026-08-30 and
2026-08-31. The stale-completion and receipt-replay repair commands, uncached full race suite,
vet, module verification and Linux cross-builds passed on 2026-08-31. The cross-builds exited 0
with sandbox-denied Go module stat-cache warnings. They are cross-build evidence only; WSL2 had
no Go runtime, so they are not Linux execution evidence.

`golangci-lint 2.12.2` was not runnable because its binary was built with Go 1.26.2,
which is lower than this module's Go 1.27.0 target. `go vet ./...` passed; this limitation
must not be reported as a golangci-lint pass.

Store tests observed:

- every checked-out physical connection read back WAL/FULL, foreign keys, busy timeout, and WAL autocheckpoint;
- empty and repeated migration startup;
- failed migration transaction rollback;
- migration checksum, missing history, and future-version/downgrade rejection;
- concurrent Automatic/Manual partial unique indexes;
- Decision + Projection + Critical Audit + receipt required-write rollback;
- equal revision Projection idempotency and different/lower revision rejection;
- higher revision Projection advancement.
- terminal receipt read-back and SQLite Coordinator replay;
- monotonic Source checkpoint with durable identity checks;
- atomic File generation rotation, lifecycle high-water, safe Retire, and late-write triggers.
- typed Parser terminal, Detection membership and Alert rows with closed validation;
- deferred Parser/Detection-to-receipt foreign keys, Alert-to-membership binding, and
  `EventID + RuleID + RuleVersion` membership uniqueness;
- Coordinator SQLite adapter delegation for Parser/Detection/Alert/Decision/Projection/Audit,
  with a seven-table same-transaction integration test.
- Guard-owned SecurityEvent construction, frozen Parser/Rule execution, staged Window Ledger,
  shared Delivery gate, deferred commit-unknown resolution, and a production C1 Pipeline success path.
- real SQLite rollback/retry and Parser poison transaction tests, with Parser causes excluded from
  durable receipt and Critical Audit fields.
- additive `0002` Detection terminal outcome migration, Rule success/RecordPermanent outcomes,
  poison Audit/receipt rollback-retry, and post-commit Window exclusion.
- transaction-aware Automatic Decision create/suppress semantics: partial-unique conflict handling,
  v1→v2 no-renewal, Projection rebuild/revision, create/suppress Critical Audit, candidate-ID conflict,
  different-Window concurrent suppression, receipt rollback/retry, commit-unknown and replay.
- preliminary Manual/expiry Decision application service and SQLite ports: writer-serialized
  create/duplicate/replace, global ID conflict, concurrent replacement history, first-write expiry
  batch, once-per-Target Projection rebuild, Critical Audit rollback, idempotent replay, and typed
  commit-unknown result preservation.
- Fake SafetyGrace derivation and convergence comparison use the same `EffectiveUntil + 5m` value;
  finite timeout refresh and permanent timeout removal are covered.
- additive migration `0003` stores exact retry/pending-Probe recovery requirements with STRICT
  domain/fence/attempt constraints and one unresolved row per physical operation key.
- typed Store transitions atomically delete an older exact requirement, write the monotonic retry
  ledger, and optionally install its replacement; read hydration uses one SQLite snapshot transaction.
- stale superseded requirements can use a validated `DeleteOnly` transition so an old RetryEpoch is
  removed without rewriting a newer singleton/current ledger row; Store commit errors have a typed
  commit-unknown classification for Controller readback recovery.
- SQLite close/reopen tests cover all three Reconcile domains, exact rollback/delete/replacement,
  pre-Apply Applying+Probe persistence, RetryEpoch/attempt monotonicity and invalid durable input.
- additive `0004_desired_firewall_authority.sql` persists one singleton SnapshotRevision, preserves
  the maximum legacy revision lower bound, adopts legacy retry/probe generation as a monotonic first-
  materialization floor without deleting its physical ambiguity, and fixes the Absent policy-digest constraint without changing migration
  0001–0003 checksums.
- Automatic, Manual and expiry transaction paths share the same normalized Target Intent finalizer;
  semantic no-ops do not write Intent/retry state or advance SnapshotRevision, while a multi-Target
  transaction advances the singleton once. Intent/audit/finalizer failures roll back Decision,
  Projection, Intent, retry state, SnapshotRevision and receipt together.
- confirmed/readback-proven commits emit node-bound Target wakes after durability; rollback and
  unproven commit-unknown do not. A typed post-commit wake error preserves committed business output;
  durable receipt replay re-emits only materialized generation-matched Pending/attempt-zero Target work.
- the exported expiration scheduler runs one due sweep at startup, then uses absolute 60-second
  deadlines, catches up after a slow sweep, stops on cancellation, fails fast, and redelivers durable
  Pending work without duplicating the exact generation already Woken by the startup expiry commit.
- the Controller rejects current finite Present intent at or past EffectiveUntil before Probe/attempt/
  Apply, while stale expired Plans still return ErrStalePlan for one authoritative reread. If expiry is
  crossed only after Applying is durable, no Backend Apply occurs and the consumed attempt is recorded
  as Degraded/expired_before_apply without installing a false Probe barrier.
- `internal/clock` provides one shared injectable Clock/Timer contract while existing constructors retain
  wall-clock behavior. Store exposes a transaction-consistent Desired Target read, and the production
  Desired PlanProvider publishes that exact snapshot before startup Probe or queued state filtering.
- ExpirationRuntime orders a no-wake startup expiration transaction before Dispatcher recovery, preserves
  the startup sweep's absolute 60-second deadline, couples cancellation/failure of both loops, and preserves
  concurrent component error identities. The real SQLite→Fake virtual-time integration proves Absent within 62s.
- `0005_observed_firewall_state.sql` adds complete Infrastructure/Policy/Target Observed persistence, safely
  invalidates the incomplete v4 Target cache, and enforces explicit observation time for every claimed physical
  state. Store exposes atomic per-domain/per-Target updates with monotonic time and Target Desired-generation fences.
- Controller persists successful Probe snapshots, scoped/all-domain Unknown failures, and retry/probe confirmation
  atomically where required. A durable Observed cache seeds only timestamp monotonicity; fresh startup Probe remains
  mandatory before runtime confirmation.
- Independent Controller/Store Tier-3 reviews repaired one time-fence P1 and one NULL-time schema P2, then reported
  no remaining P0/P1/P2/P3. The user Code Review explicitly accepted the complete Observed batch.
- Dispatcher exports a process-local Backend Health/Metric read model and owns startup classification, bounded
  Probe timeout, exact capped recovery backoff, mutation gating and cancellation. Private typed error classification
  separates retryable Backend failure from fatal Observed persistence/fencing/invariant failure without a new schema,
  migration, dependency, Config field, IPC contract, permission rule or executable entry point.
- Fresh final code, test/evidence and integration reviews report no remaining P0/P1/P2/P3 for this frozen batch;
  the user Code Review explicitly accepted the Backend health monitor/runtime ownership batch.
- The 2026-08-31 repair preserves the Store-facing regression identity through the Core persistence
  contract. Controller converts it to stale completion only when authoritative readback proves the
  same Target generation advanced, clears only the superseded Probe physical key, and keeps other
  persistence/readback errors fatal. A deterministic SQLite→Fake interleaving test blocks generation 1
  confirmation until expiry commits generation 2 and then proves convergence to Absent.
- Base processing adapters without a Target wake sink skip unrelated global pending-Target redelivery
  during receipt replay. Enforcing adapters retain redelivery, and the regression test proves a durable
  receipt replays without a second processing attempt while unrelated Target work remains pending.

## Checksums

```text
507c5fdc4a2e1b7844b01d07000ea5244d7e39b481fe3137294a9de9b3abb00b  go.mod
cf25679f0df1b3fd4340744127d50b0b38368dff55045ad99ce8583ce02758e3  go.sum
ada106123ef675a9989805743d95f29c22f361b21cb5aabc5c1f277dc3fe61da  migrations/0001_m0.sql
fd6817796fa3d0c30f6bf3e2537ed369beb9be5fa751a560b13e8ac5b0b0c9f4  migrations/0002_detection_terminal_outcomes.sql
e3e609fa2d8e046c3c196c49644c31486083678210322491bdd7a7a6745e3bbc  migrations/0003_reconcile_restart_recovery.sql
67d55c850ca35a8e5528db2109b4291ae3e758842c1b53560e3ea33497339412  migrations/0004_desired_firewall_authority.sql
42886a2c5ae83b5052ff138f96d42f6ae14001a2302845ab48f0dce09b4e0e4c  migrations/0005_observed_firewall_state.sql
27d3cb5cfc1922803afe33ff81df1bb48e7898ed53b3604bd8ad83c888f805d3  internal/store/migration.go
1b9db0d4c26aeb649a55f090194d16ddf54792b26eae4c6a11ee208d588f70b8  internal/store/uow.go
28d8be7da01673b28c18fa3e37134bb23f2a43a19aa3f8d7744a4fc899171993  internal/store/uow_decision.go
f13d8e5dd1f9b319af5ae11df44d430f2c17f2bc55d526157336aaa2a7cb7eed  internal/store/source_state.go
8983365ef382cce706aaf2fc8b83913a3d15efd06bdbe0733be01899dffc4d60  internal/processor/coordinator.go
cc95b8f83a4c9b2ab77d4b6c779bc9158a76eda69a397249eb7b64bf71aa5694  internal/processor/sqlite_store.go
fc5ce74e696907614a25a9bc3d79f50dae3d502447ea01510143580056978091  internal/processor/plan.go
3c5fbe5725635c495703ee6b2fe0f1511f1dfadbf3566c8c2181551d2f99315b  internal/processor/pipeline.go
0f75a10b642f2564de85c8bcc264e35ae9e207170df85f96152b5d605bc30162  internal/detection/window.go
2c0aac61be54f03e92dd2f843cb1de3932a4d4dcdb01abfd05271a431fad8bdf  internal/core/security_event.go
9ee579f240f46a3981340ba3ed865b4c617e1d919922476b3079ecce524af83b  internal/core/model.go
3764f2f9863ea549c6430d05e7bae9baf53a33a8dcd3fb4463cd36e0eaad60cc  internal/decision/transaction.go
7a6bed21f0c080b89fdfc5d217ca0430f002d2ae7fa508dd53b767d04cf6946b  internal/clock/clock.go
a465f38a09ef375ef8ea56ea6dc49a09b4adcf6f104120fe2602be20f5bc88af  internal/decision/lifecycle.go
b2a2b04d46aa06338bd2da0d4d634710dad5e5685d6f1c9fc4dbb223c66fe2a8  internal/decision/expiration_scheduler.go
a0b3d0f08cf190d801f95813cb288e03fd99b318d796031e2c5b687d0fc2c11c  internal/decision/desired_state.go
66227d51864afd2d6090b510f387a2b50797b7076d7dde4fd6c48e9568cbe170  internal/store/decision_lifecycle.go
a81b9835ed1a7958d2d16abc28d2004fd955df76f894dce6358b53223473cd44  internal/store/desired_state.go
b3118b80b645ef655f5f2b2e767c717dc02594734e5f5a00a4b5c4c97bacdee2  internal/enforcement/intent.go
5ded3dd9c6e1bf0f59ca0f148f006248469b989b107d756002acc09ba65b158b  internal/firewall/fake/backend.go
b6e6b188c66341b362dd802483342c1a3efd469014653c1712186eac60b270f2  internal/core/reconcile_persistence.go
74061b5eb6d80e55a203d22c74b2939fb4731cb096f7a27e4351cd2e801750d0  internal/core/observed_state.go
82cbfddafab68353447f023936dacf16cd1e55b76c811f9e835e071c82fd6d1d  internal/core/observed_state_test.go
13160934bfd59a36374327f21f8b2d39f3bdabb2b1813cccf3d13612a8dfb710  internal/core/reconcile_persistence_test.go
4745adb54b33d052f8a40c02ada061ffc7614664901fe9066c544a21bf003c21  internal/store/reconcile_state.go
4e0c85e69d9d936b7212fb3bca25f7a738137c510a0272d59eaa02732ba0712a  internal/store/observed_state.go
05c098704a868e7197c5d998e9a9a6744ec899c267b6a0563d3bc175b43099ef  internal/store/observed_state_test.go
6ce261aa4336e33ed711b3d20f9bba778d4288a4047854cef52c6a180678276f  internal/store/observed_migration_test.go
31407c24fadbb0206f61252e7e9a676746b5437221c9cbd167c33ac6850c5341  internal/store/reconcile_state_test.go
819e994e0c653e2b008a8edb471e5dfcd0a0ec27b4ac84408f02747ecf99a5c8  internal/reconcile/controller.go
2b4b07d8fbb0b5107347d7f60b7ec3b0752b6e4ede0119d91835ec73f0e9562b  internal/reconcile/persistence.go
44c0b462883309f4738ef3a74749b64aaeb81678f4ba020ced664da9fb5ad366  internal/reconcile/observed_state.go
344c4f3323b064566f1877b77b9b08d9e391882e68a4967cdd081b0d29579742  internal/reconcile/target_wake.go
c4f83f6a0de4905cd0deebad3ef85134dc5d6cfb2d2dc82bf11cbfa1f16773f0  internal/reconcile/restart_sqlite_test.go
2c05d1b0d793ba9cf6b11206523f108a92f42a70aa7c0251367cd87cbc813e8e  internal/processor/shutdown_checkpoint_test.go
ccf5d78120f587a325a644c464b5d7b372d654443a4bec1c664e2f89baa1b5e5  internal/reconcile/health_recovery_test.go
5e650f5abe6c719bec3a3227c263bad33aae982c29436bcdf2e1e93d61ecd2fb  internal/reconcile/dispatcher.go
a7d0121ff87817981136a175303fd1de58f670f7d4a1a562496ae3b13d2d1fbf  internal/reconcile/dispatcher_test.go
4def2da406435717f0b42a0752a1a62f36dae5fe5eac29a32995f8a85d888d45  internal/reconcile/backend_health_monitor_test.go
b43862037475ed367ed2fe1a70211cb67de083c0bd0f83f03846c7df3089e9cc  internal/reconcile/desired_plan_provider.go
d0e0986a90082df1a00722e0a00040c4b7f555a59c79ed93eb202cedb2b9af49  internal/reconcile/expiration_runtime.go
d4cb69a4140f5516698cbdd6ef0d75af56d66952d1b4808b5c45ea10dec8e3b4  internal/store/expiration_runtime_test.go
```

## Not verified or not implemented

- Linux runtime, SIGKILL/reopen, service restart, OS reboot, filesystem/barrier, and power cut; the
  current restart primitive is a Windows test that closes/reopens SQLite and creates a fresh Controller;
- production executable wiring, real Enforcer/IPC health source, real Backend and real process/service restart recovery;
  the current 62-second proof is an in-process real SQLite→Fake integration under one virtual Clock;
- Match-stage Rule permanent, suppression overflow, Projection repair and multi-Rule target concurrency
  remain non-blocking follow-up coverage for the implemented semantic port;
- replay/reprocess reference storage and real Source reader recovery;
- single-instance lock before database open and production executable/runtime startup composition;
- credential file target-Linux deployment verification, full Contract Tests, V0.4 sync,
  and commit-bound Evidence Manifest.

D1 and D2 remain `IN_PROGRESS / Implemented`.
