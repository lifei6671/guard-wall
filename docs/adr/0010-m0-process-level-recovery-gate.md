# ADR-0010：M0 采用进程级 Crash/Recovery 验收边界

## 状态

```text
Decision: Accepted
Validation maturity: Specified
Date: 2026-09-04
```

## Context

M0 已具备 SQLite 与 Source 的 `SIGKILL → reopen` 证据，以及 clean target Linux 上
Guard-owned socket、SQLite 与 nftables 对象的进程重开证据。完整 OS/power-loss durability、
filesystem barrier/fsync、non-clean Firewall topology 与全量 Crash Matrix 需要更高成本的
环境和故障注入能力，当前不作为 M0 Contract Freeze 的前置条件。

## Decision

M0 的 Crash/Recovery Gate 只要求：

- 已提交与未提交边界的自动化进程级 `SIGKILL → reopen` 证据；
- clean target Linux 上记录范围内的 Guard-owned 进程重开、状态读回与 identity-guarded cleanup；
- 对未覆盖故障域使用 `NOT VERIFIED`，不从现有 Evidence 推导更强耐久性结论。

完整 Crash Matrix、OS reboot/power-loss、filesystem barrier/fsync 与 non-clean Firewall
compatibility 移至 M7/M10 和 Release Evidence。该决定不改变 SQLite `WAL`/`synchronous=FULL`、
最小权限、IPC 身份校验、Firewall 所有权、Probe-first Unknown 处理、背压或事务顺序。

## Consequences

- C3 不再是 M0 Gate 依赖；其完整故障注入继续作为后续验证工作。
- G18.2 仍要求 C1/C2、migration 与 M0 范围 Contract Tests 具备可复核证据；M0 当前仍为 `NO-GO`。
- 未验证的高成本故障域不能标记为 `PASS`、`Verified` 或 Release-ready。

## Verification

- 复核 SQLite、Source 与 C2 现有 process-level `SIGKILL → reopen` Evidence 的注入点、
  committed/uncommitted 边界和状态读回范围。
- 复核 clean target Linux E2E 的 Guard-owned socket、SQLite、nftables 对象和
  identity-guarded cleanup Evidence。
- 在 D5 中自动化 M0 范围 Contract Tests；扩展 Crash Matrix 与非 clean topology Evidence
  仍由后续 M7/M10 验证。

## Rollback

若产品范围恢复对 OS/power-loss durability 或 non-clean Firewall compatibility 的 M0 承诺，
以新的 ADR 取代本决定，并把相应环境、故障注入和 Contract Tests 重新纳入 G18.2。

## Revisit When

- 进入 M7/M10 或准备 Phase 1 Release Evidence；
- 支持的 Linux topology、filesystem 或部署模型发生变化；
- 需要对 OS/power-loss durability 作产品承诺。
