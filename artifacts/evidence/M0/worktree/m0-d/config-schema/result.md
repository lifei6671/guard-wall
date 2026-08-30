# D3 Config Schema — Worktree Evidence

- Evidence state: `PASS_WITH_UNVERIFIED_DOMAINS`
- Maturity: `worktree_preliminary`
- Repository: `master @ 3aca38aba0e2aa871b4c9ebced9187128ce0b2a3 (dirty)`
- Schema: JSON Schema Draft 2020-12
- Contract status: `D3 IN_PROGRESS / Implemented`; not `Verified`

## Commands

```powershell
Get-Content schema/config-v1.schema.json -Raw | ConvertFrom-Json | Out-Null
go test -race ./internal/config ./schema
go vet ./internal/config ./schema
go test -count=10 ./internal/config
```

All commands passed on 2026-08-30.

## Implemented and observed

- Schema is embedded from `schema/config-v1.schema.json` and remains the only default/range source;
- strict root and nested unknown-field rejection;
- six M0 resource defaults and min/max boundaries;
- required absolute database path and SMTP credential-file reference;
- non-loopback plaintext Web listener requires explicit authorization;
- ownership, hot-reload, restart, and sensitive metadata exposed through read-only FieldPolicy;
- exact §15.1 ownership matrix assertions;
- Config typed JSON leaf paths and Schema leaf paths must match bidirectionally;
- inline SMTP password and sensitive-value error leakage are rejected;
- JSON-compatible YAML scope is stated without claiming full YAML syntax support.
- Linux credential reads use `lstat`, `O_NOFOLLOW`, final-fd `fstat`, a 64 KiB cap,
  exact root/guard ownership, `0640`-or-stricter permissions, and sanitized errors;
- Windows and non-Linux credential reads fail explicitly as unsupported.

An independent read-only review found no P0/P1 in the implemented Schema scope.

## Not verified or not implemented

- full YAML syntax loader;
- target Linux `root:guard` deployment, parent-directory trust, and SMTP Ready failure integration;
- atomic logging hot reload and restart-bound field enforcement;
- Linux packaging ownership/mode integration;
- Auth/Session fields gated by the separate Web Security milestone;
- M3/M4 Parser/Detection limits that Contract §16 explicitly defers.

## Checksum

```text
e917978707abe65cb4fc85279358b5306582c7681c83b949f6407014efac2448  schema/config-v1.schema.json
28d1e7d35e288e56b6ad23e613bb91d51e7c9f8aa7502ab194cb969dad502b55  internal/config/credential.go
9e4e46259d8a2eb52a627bf4c5e302d83f5dc6b6435356d16a47534563170d27  internal/config/credential_linux.go
```

These omissions keep D3 at `IN_PROGRESS / Implemented`.
