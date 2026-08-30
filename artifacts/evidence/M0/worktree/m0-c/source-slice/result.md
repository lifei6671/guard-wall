# C1 Source Fake Slice — Worktree Evidence

- Evidence state: `PASS_WITH_UNVERIFIED_DOMAINS`
- Maturity: `worktree_preliminary`
- Repository: `master @ 3aca38aba0e2aa871b4c9ebced9187128ce0b2a3 (dirty)`
- Environment: `go1.27.0 windows/amd64`, race enabled
- Contract status: `C1 IN_PROGRESS / Implemented`; not `Verified`

## Commands

```powershell
go test -race ./internal/core ./internal/store ./internal/processor ./internal/source
go vet ./internal/core ./internal/store ./internal/processor ./internal/source
go test -count=10 ./internal/store ./internal/processor ./internal/source
```

All commands passed on 2026-08-30.

## Implemented and observed

- stable File/Journald Delivery ID golden vectors and canonical decoding;
- Delivery/receipt identity binding to SourceID and SourcePosition;
- rollback at each staged child-write failure;
- committed receipt replay skip and zero-outcome terminal receipt;
- commit-unknown receipt read-back using an independent bounded context;
- same-Delivery in-process single flight;
- contiguous checkpoint advancement across out-of-order completion;
- failed checkpoint persistence retained for a later flush;
- bounded queue cancellation behavior;
- SQLite terminal receipt replay and conservative commit-unknown read-back;
- monotonic SQLite checkpoint CAS with Source/generation/device/inode validation;
- atomic File rotation with one Open generation per Source;
- Seal-persisted final EOF and maximum DeliverySequence high-water;
- Retire barrier based on checkpoint sequence plus receipt/reference checks;
- SQLite triggers rejecting late receipt/checkpoint writes to Retired generations;
- pre-commit cancellation rollback with a closed transaction.
- immutable all-match Parser Set ordered by `priority + parser_id`, with lazy immutable
  Rule Catalog snapshots and version-switch isolation;
- Parser-local `RecordPermanent` continuation and attempt-aborting
  `Transient/PlanBlocked/Cancelled` classification;
- typed Parser/Detection/Alert/Decision/Projection/Critical Audit writers connected through
  the Coordinator SQLite adapter;
- one real Coordinator SQLite transaction committing all seven outcome/receipt tables;
- deferred receipt foreign keys and `EventID + RuleID + RuleVersion` durable membership
  idempotency, including stable Delivery identity conflict rejection.

An independent read-only review found no P0/P1 in the implemented scope.

## Not verified

- real process crash/restart;
- real Parser DSL/SecurityEvent/Detection engine and Processing Plan-to-Coordinator orchestration;
- poison-record four-class behavior;
- shutdown drain/timeout/restart;
- real Source reader rotation recovery, replay/reprocess reference model, and copytruncate data-loss signaling;
- Detection Window post-commit in-memory application and rollback/retry `count/distinct_count` behavior;
- Linux reboot and power-loss durability.

These omissions keep C1 at `IN_PROGRESS / Implemented`.
