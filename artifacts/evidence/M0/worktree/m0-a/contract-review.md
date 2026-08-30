# M0-A Contract Cross-review Evidence

> Evidence maturity: `worktree_preliminary`. This review supports
> `COMPLETE / Implemented` for A1–A4. It does not prove runtime behavior and
> must not be used to mark any item `Verified` or `Frozen`.

## Review identity

- Milestone: `M0`
- Work packages: `A1`, `A2`, `A3`, `A4`
- Base commit: `3aca38aba0e2aa871b4c9ebced9187128ce0b2a3`
- Working tree: dirty; reviewed artifacts are not commit-bound
- Reviewed at: `2026-08-30T14:26:25+08:00`
- Reviewers: `core_contract_designer`, `execution_gate_reviewer`, root integrator
- Result: `PASS_WITH_RUNTIME_VALIDATION_PENDING`

## Reviewed artifacts

| Artifact | SHA-256 at review time |
|---|---|
| `docs/contracts/core-model.md` | `090fd773f4b31d68942bd5ebddbb6e3cb6533d821b4407303834465b948c5510` |
| `docs/contracts/source-delivery.md` | `9c0f2b532e1e55313a449f082593053e907752343adaab5edbeaa5b09fadd014` |
| `docs/contracts/decision-enforcement.md` | `2d3c0881758fb02f93b59427da906aa0efe24e2a330ffdf0793b0d3233ae3162` |
| `tests/contracts/m0-crash-matrix.yaml` | `c244ec74e9c4a3627963123ca510910dfdda289fbb30e37092818561c1d9f72a` |
| `docs/contracts/guard-phase-1-m0-contract-freeze-v0.3.md` | `565ea42e1a52e8b386879bdc9740419a15c5d644d0c81dc2122bde51a9250b99` |
| `docs/development/phase-1/STATUS.md` | `1cf9a4ae3ec93d18edb50c4ecf3bdf281d2f6e06918e4b3ede7a7a86af82eca6` |

`STATUS.md` is expected to change immediately after this review because it is
the live progress source. Its hash records the dependency graph that was
reviewed, not the later status-only registration edit.

## Review results

- A1: one authoritative Core Model; `EmittedIndex` is consistently `uint32`;
  `NativeExpiry` is not part of Desired Intent; no unresolved P0/P1.
- A2: receipt pipeline, delivery boundaries, replay and the single Processing
  Coordinator UnitOfWork are expressible without a durable-inbox alternative;
  no unresolved P0/P1.
- A3: Decision, Projection, three Retry domains, fencing and safe ordering use
  one state model; document completion no longer depends on downstream C/D/G18
  runtime verification; no unresolved P0/P1.
- A4: all eight Contract crash points have unique IDs, injection points and all
  nine required final-state categories; no unresolved P0/P1.
- C1/C2 entry depends on relevant A artifacts being `Implemented` plus this
  review Evidence. It does not require A1/A3 to be runtime `Verified`, so the
  previous dependency cycle is closed.

## Repeatable checks

```text
python -B -c "import yaml,pathlib; ..."
```

Result: schema `guard.m0-crash-matrix.v1`; 8 cases; 9 required final-state
fields per case; no missing or extra fields.

```text
rg -n "EmittedIndex|NativeExpiry" docs/contracts/core-model.md \
  docs/contracts/decision-enforcement.md \
  docs/contracts/guard-phase-1-m0-contract-freeze-v0.3.md
```

Result: `EmittedIndex` is `uint32`; `NativeExpiry` remains only in the
Backend/Observed comparison domain.

Markdown local-link resolution, code-fence parity and `git diff --check` were
also run against the reviewed document set and passed.

## Prior findings and disposition

- P0 A3/C2/D dependency loop: resolved.
- P0 A1/G18 dependency loop: resolved.
- P1 `EmittedIndex` type mismatch: resolved.
- P1 `NativeExpiry` in Desired Intent: resolved.
- P1 incomplete per-case crash final state: resolved.
- Final cross-review: no open P0/P1.

## Not proven by this review

- Go types and interfaces compile against a frozen module and dependency set.
- Source and Decision/Enforcement Fake Slices pass.
- Crash injection produces the declared final states; A4 currently validates
  the manifest, not a runner execution.
- SQLite migration, Config Schema, production Firewall behavior, Linux
  privilege boundary, restart recovery or power-loss durability pass.
- G18.1, G18.2, G18.3 or M0 `Frozen/GO` pass.
