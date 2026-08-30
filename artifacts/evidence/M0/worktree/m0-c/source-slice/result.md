# C1 Source Fake Slice — Worktree Evidence

- Evidence state: `PASS_WITH_UNVERIFIED_DOMAINS`
- Maturity: `worktree_preliminary`
- Repository: `master @ 2dbc986fb38c8c8b4f4904df2241792f516f4228 (dirty)`
- Environment: `go1.27.0 windows/amd64`, race enabled
- Contract status: `C1 IN_PROGRESS / Implemented`; not `Verified`
- User code review: `PASS` on 2026-08-30 for the shutdown primitives batch; complete C1 remains in progress.

## Commands

```powershell
go test -race ./...
go vet ./...
go test -count=10 ./internal/core ./internal/detection ./internal/processor ./internal/store
go test -race -count=10 ./internal/processor -run '^TestShutdownPrimitives_'
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
- typed Parser/Detection outcome/Alert/Automatic request/Critical Audit ports connected through
  the Coordinator SQLite adapter and Decision semantic service;
- real Coordinator SQLite transactions committing outcomes, business facts, Audit and receipt;
- deferred receipt foreign keys and `EventID + RuleID + RuleVersion` durable membership
  idempotency, including stable Delivery identity conflict rejection.
- Guard-owned `SecurityEvent` identity and Parser-owned field boundary, including per-Parser
  RawRecord isolation and per-Rule Event isolation;
- production Pipeline orchestration from frozen Parser/Rule snapshots through staged Window
  preview to typed Parser outcome, Detection contribution, optional Alert and terminal receipt;
- post-commit Window Confirm, rollback Abort, commit-unknown found/absent/readback-error
  resolution, and deferred resolution after Coordinator reconstruction;
- shared Delivery gate across overlapping Coordinator instances, preventing a stale receipt
  pre-check from applying a second Window attempt;
- real SQLite Alert failure rollback followed by a successful retry with one Window contribution;
- real SQLite Parser `RecordPermanent` outcome + sanitized Critical Audit + poison receipt in one
  transaction, while another Parser can continue through Detection.
- real SQLite Rule success/RecordPermanent/success terminal outcomes, deterministic poison receipt,
  sanitized Critical Audit, failed-candidate Window exclusion, receipt-failure rollback and one retry.
- preliminary shutdown primitive composition drains a fixed already-accepted Delivery set before
  checkpointing; a cancelled second checkpoint write leaves a pending candidate that final `Flush`
  advances from sequence 1 to 2 before Audit readback and DB Close.
- shutdown cancellation before transaction open produces no receipt or checkpoint; SQLite Close/Reopen
  then replays the same stable Delivery and committed receipt replay skips a second runner execution.

The latest two independent read-only reviews found no P0/P1 in this preliminary primitive-composition scope.

## Not verified

- real process crash/restart;
- real Parser DSL/runtime and M4 Detection DSL/sliding-window implementation;
- Match-stage Rule poison, suppression overflow and multi-Rule target Projection concurrency remain
  follow-up coverage; Detection still does not construct authoritative Decision or Projection;
- production shutdown owner, real management/Source intake stop, concurrent admission/in-flight drain
  barrier, 30s timeout orchestration, commit-unknown shutdown and real SIGTERM/restart;
- real Source reader rotation recovery, replay/reprocess reference model, and copytruncate data-loss signaling;
- Linux reboot and power-loss durability.

These omissions keep C1 at `IN_PROGRESS / Implemented`.
