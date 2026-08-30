# D1/D2 Code and Migration — Worktree Evidence

- Evidence state: `PASS_WITH_UNVERIFIED_DOMAINS`
- Maturity: `worktree_preliminary`
- Repository: `master @ 3aca38aba0e2aa871b4c9ebced9187128ce0b2a3 (dirty)`
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

## Checksums

```text
99ec7697b345f43b379addd9afd0da5c45201e8b8c016cd99b1ff287fc8b115a  go.mod
9146e7666d70f22e7739e8735d90275a271753977bd21337caf82345fd8f3cd0  go.sum
ada106123ef675a9989805743d95f29c22f361b21cb5aabc5c1f277dc3fe61da  migrations/0001_m0.sql
27d3cb5cfc1922803afe33ff81df1bb48e7898ed53b3604bd8ad83c888f805d3  internal/store/migration.go
5ed2ad25dd0cdd1d8414886162f0dcda0aec227edb2a80b37e030134db46c3c8  internal/store/uow.go
f13d8e5dd1f9b319af5ae11df44d430f2c17f2bc55d526157336aaa2a7cb7eed  internal/store/source_state.go
55f6eb53c8dd0b764fe4666267f0ad08d8c2eba02075dea36d35e51bb843352d  internal/processor/coordinator.go
da4b8e96386c88cc55d34fc5a3de853afd5db8781cc1bd9b09042ea1ac593278  internal/processor/sqlite_store.go
34824c618d661b11a23d3a065cc62bb06c71be9b55aa2ece401c962523825877  internal/processor/plan.go
```

## Not verified or not implemented

- Linux runtime, SIGKILL/reopen, service restart, OS reboot, filesystem/barrier, and power cut;
- Decision duplicate suppression and Manual replace SQLite lifecycle ports;
- real Parser/Detection runner orchestration and Detection Window post-commit memory;
- replay/reprocess reference storage and real Source reader recovery;
- single-instance lock before database open;
- credential file target-Linux deployment verification, full Contract Tests, V0.4 sync,
  and commit-bound Evidence Manifest.

D1 and D2 remain `IN_PROGRESS / Implemented`.
