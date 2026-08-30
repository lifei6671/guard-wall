# Decisions

<!--
Append one ADR-Lite record per engineering or task-contract decision. Never
delete an old decision. Use only these states: Proposed, Accepted, Rejected,
Deprecated, Superseded. When replacing a decision, mark the old record
Superseded and point Superseded By to the new record.
-->

## Decision State Machine

```text
Proposed ──→ Accepted ──→ Deprecated
    │             └────→ Superseded ──→ Superseded By: D-NNN
    └────────────→ Rejected
```

Historical records remain in this file after every transition.

## D-<NNN> <Short decision title>

- Status: `<Proposed | Accepted | Rejected | Deprecated | Superseded>`
- Date: `<YYYY-MM-DD>`
- Superseded By: `<D-NNN or None>`
- Supersedes: `<D-NNN or None>`

### Decision

<State exactly what was chosen. For Proposed or Rejected records, state what is
under consideration or what was declined.>

### Context

<Describe the problem, constraints, and facts that made a decision necessary.>

### Alternatives

- <Alternative considered.>
- <Another alternative considered.>

### Rationale

- <Reason the selected option best fits the current task contract.>

### Trade-offs

- <Cost, limitation, or capability intentionally deferred.>

### Revisit When

- <Observable condition that should trigger reassessment.>

### Source

- Conversation ID: `<conversation-id-if-available>`
- Turn ID: `<turn-id-if-available>`
- Timestamp: `<ISO-8601 timestamp>`
- Anchor: `<Explicit user instruction or decision evidence>`
- Repository: `<branch and commit if applicable>`
- Artifact: `<project-relative path and section if applicable>`

### Related

- HANDOFF: `<section or None>`
- Decisions: `<D-NNN or None>`
- Lessons: `<L-NNN or None>`
- Daily: `<daily/YYYY-MM-DD.md#entry-anchor or None>`
