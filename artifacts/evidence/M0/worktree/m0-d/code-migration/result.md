# D1/D2 Code and Migration — Worktree Evidence

- Evidence state: `PASS_WITH_UNVERIFIED_DOMAINS`
- Maturity: `worktree_preliminary`
- Repository: `master @ 2dbc986fb38c8c8b4f4904df2241792f516f4228 (dirty)`
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
$env:CGO_ENABLED='0'; $env:GOOS='linux'; $env:GOARCH='amd64'; go build ./...
$env:CGO_ENABLED='0'; $env:GOOS='linux'; $env:GOARCH='arm64'; go build ./...
git diff --check
```

All commands passed on 2026-08-30. The two Linux commands are cross-build evidence only;
WSL2 had no Go runtime, so they are not Linux execution evidence.

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

## Checksums

```text
507c5fdc4a2e1b7844b01d07000ea5244d7e39b481fe3137294a9de9b3abb00b  go.mod
cf25679f0df1b3fd4340744127d50b0b38368dff55045ad99ce8583ce02758e3  go.sum
ada106123ef675a9989805743d95f29c22f361b21cb5aabc5c1f277dc3fe61da  migrations/0001_m0.sql
fd6817796fa3d0c30f6bf3e2537ed369beb9be5fa751a560b13e8ac5b0b0c9f4  migrations/0002_detection_terminal_outcomes.sql
e3e609fa2d8e046c3c196c49644c31486083678210322491bdd7a7a6745e3bbc  migrations/0003_reconcile_restart_recovery.sql
27d3cb5cfc1922803afe33ff81df1bb48e7898ed53b3604bd8ad83c888f805d3  internal/store/migration.go
1b9db0d4c26aeb649a55f090194d16ddf54792b26eae4c6a11ee208d588f70b8  internal/store/uow.go
28d8be7da01673b28c18fa3e37134bb23f2a43a19aa3f8d7744a4fc899171993  internal/store/uow_decision.go
f13d8e5dd1f9b319af5ae11df44d430f2c17f2bc55d526157336aaa2a7cb7eed  internal/store/source_state.go
c2062f78c81a48e67efca8c97b707b38258e0a060f38e06806065f0495a5e927  internal/processor/coordinator.go
34946f85c565b73a4c9c3dd2e92c64f24e1a2be31936c5a53982864436418f1a  internal/processor/sqlite_store.go
fc5ce74e696907614a25a9bc3d79f50dae3d502447ea01510143580056978091  internal/processor/plan.go
3c5fbe5725635c495703ee6b2fe0f1511f1dfadbf3566c8c2181551d2f99315b  internal/processor/pipeline.go
0f75a10b642f2564de85c8bcc264e35ae9e207170df85f96152b5d605bc30162  internal/detection/window.go
2c0aac61be54f03e92dd2f843cb1de3932a4d4dcdb01abfd05271a431fad8bdf  internal/core/security_event.go
9ee579f240f46a3981340ba3ed865b4c617e1d919922476b3079ecce524af83b  internal/core/model.go
3764f2f9863ea549c6430d05e7bae9baf53a33a8dcd3fb4463cd36e0eaad60cc  internal/decision/transaction.go
dfd2236c0f782f96e8102e23253b4fa3e547663c12be6e15cde277f258377a24  internal/decision/lifecycle.go
66227d51864afd2d6090b510f387a2b50797b7076d7dde4fd6c48e9568cbe170  internal/store/decision_lifecycle.go
b3118b80b645ef655f5f2b2e767c717dc02594734e5f5a00a4b5c4c97bacdee2  internal/enforcement/intent.go
5ded3dd9c6e1bf0f59ca0f148f006248469b989b107d756002acc09ba65b158b  internal/firewall/fake/backend.go
8c218050019442141070b67872fad49169810aee3bb4acd1aacc7e4ea8c7feb9  internal/core/reconcile_persistence.go
13160934bfd59a36374327f21f8b2d39f3bdabb2b1813cccf3d13612a8dfb710  internal/core/reconcile_persistence_test.go
9d8a1b0fe4a4d0682c4a8ebc2c422d749f0c4d8e640d94dce68582fa821e7108  internal/store/reconcile_state.go
31407c24fadbb0206f61252e7e9a676746b5437221c9cbd167c33ac6850c5341  internal/store/reconcile_state_test.go
4e0206421f522cc7686d302d79d17d033c14996a0e7e2be8f220554103a61bf4  internal/reconcile/controller.go
95a03b2027ce441931a3603dde3d4694a3ec55a8ffea55f4e83136b2d697b9ab  internal/reconcile/persistence.go
df61f2ed434f47cebd0068943a78ff3de5ea621abd02eb2f4e7e1511d4ed2332  internal/reconcile/restart_sqlite_test.go
2d18de0b2bfc0e46fe4e3f1da568d052b84d87a7396bc3f170d9cfd2aff98567  internal/processor/shutdown_checkpoint_test.go
ccf5d78120f587a325a644c464b5d7b372d654443a4bec1c664e2f89baa1b5e5  internal/reconcile/health_recovery_test.go
```

## Not verified or not implemented

- Linux runtime, SIGKILL/reopen, service restart, OS reboot, filesystem/barrier, and power cut; the
  current restart primitive is a Windows test that closes/reopens SQLite and creates a fresh Controller;
- complete Manual/expiry generation/snapshot/retry-budget transaction, scheduler/wakeup and restart recovery;
- Match-stage Rule permanent, suppression overflow, Projection repair and multi-Rule target concurrency
  remain non-blocking follow-up coverage for the implemented semantic port;
- replay/reprocess reference storage and real Source reader recovery;
- single-instance lock before database open and complete Observed Firewall/runtime startup persistence;
- credential file target-Linux deployment verification, full Contract Tests, V0.4 sync,
  and commit-bound Evidence Manifest.

D1 and D2 remain `IN_PROGRESS / Implemented`.
