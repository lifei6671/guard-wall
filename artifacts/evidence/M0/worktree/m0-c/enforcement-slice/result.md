# C2 Decision/Enforcement Fake Slice — Worktree Evidence

- Evidence state: `PASS_WITH_UNVERIFIED_DOMAINS`
- Maturity: `worktree_preliminary`
- Repository: `master @ 3aca38aba0e2aa871b4c9ebced9187128ce0b2a3 (dirty)`
- Environment: `go1.27.0 windows/amd64`, race enabled
- Contract status: `C2 IN_PROGRESS / Implemented`; not `Verified`

## Commands

```powershell
go test -race ./internal/core ./internal/decision ./internal/enforcement ./internal/firewall/fake ./internal/reconcile
go vet ./internal/core ./internal/decision ./internal/enforcement ./internal/firewall/fake ./internal/reconcile
```

Both commands passed on 2026-08-30.

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

An independent read-only review found no P0/P1 in the implemented scope after the Unknown
global Probe barrier, atomic fenced confirmation, and Absent residue checks were added.

## Not verified

- SQLite persistence and restart recovery of retry/observed state;
- real nftables/iptables/firewalld behavior and privileged IPC;
- complete TargetPrepared safety ordering and SafetyGrace expiry;
- SQLite-backed Decision duplicate/manual-replace lifecycle updates and restart recovery;
- expiration scheduler/SafetyGrace and backend health-flap persistence;
- C3 crash matrix.

These omissions keep C2 at `IN_PROGRESS / Implemented`.
