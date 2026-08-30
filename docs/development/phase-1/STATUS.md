# Guard Phase 1 开发状态

> 本文件是 Phase 1 唯一可变进度源。技术语义见
> [Phase 1 Contract](../../contracts/guard-phase-1-m0-contract-freeze-v0.3.md)，
> 执行规则见 [README.md](README.md)。

## 1. 当前快照

| 项目 | 当前值 |
|---|---|
| 当前阶段 | `M0 Contract Freeze` |
| 当前结论 | `NO-GO` |
| 可启动 | `M0-A`、`M0-B` |
| 被阻塞 | `M0-C`、`M0-D`、`M1–M10` |
| M0 证据状态 | `Specified` |
| Phase 1 发布状态 | `Not Released` |
| 当前 Evidence | `None` |
| 最近更新 | `2026-08-30` |

当前仓库还没有 M0 所需的可编译模型、ADR、migration、Config Schema、
Fake Slice、Contract Tests、真实 nftables Spike 或 Evidence Manifest。

## 2. 状态字段

| 字段 | 合法值 |
|---|---|
| 推进状态 | `BLOCKED`、`READY`、`IN_PROGRESS`、`COMPLETE` |
| 证据状态 | `Specified`、`Implemented`、`Verified`、`Frozen` |
| Gate | `BLOCKED`、`NOT RUN`、`PASS`、`FAIL` |
| Evidence | 仓库相对路径或 `None` |

更新状态时必须同时填写 Evidence；没有证据时不得使用 `Verified`、`Frozen` 或 `PASS`。

## 3. M0 执行状态

详细完成条件与 Evidence 模板见 [M0-EXECUTION.md](M0-EXECUTION.md)。M0-A–M0-D
是静态分组，实时推进状态由下列 A1–D7 子项推导，不再手工维护第二套聚合状态。

| ID | 工作项 | 推进状态 | 证据状态 | 负责人/任务 | Evidence | Blocker / 下一步 |
|---|---|---|---|---|---|---|
| A1 | Core Model 与权威关系 | `READY` | `Specified` | `Unassigned` | `None` | 建立 core-model Contract 与一致性审查 |
| A2 | Source delivery 与事务协调 | `READY` | `Specified` | `Unassigned` | `None` | 建立 source-delivery Contract 与接口表达 |
| A3 | Decision / Enforcement / Reconcile | `READY` | `Specified` | `Unassigned` | `None` | 建立单轨 decision-enforcement Contract |
| A4 | Crash Matrix | `READY` | `Specified` | `Unassigned` | `None` | 为 §12.3/§17.3 建立 case manifest |
| B1 | SQLite 并发、事务与 durability Spike | `READY` | `Specified` | `Unassigned` | `None` | 准备 Spike、migration 草案和故障验证 |
| B2 | Source identity 与 replay Spike | `READY` | `Specified` | `Unassigned` | `None` | 准备 golden vectors 与轮转/restart Spike |
| B3 | nftables Backend Spike | `READY` | `Specified` | `Unassigned` | `None` | 准备隔离 Firewall 环境 |
| B4 | Agent/Enforcer 权限与 IPC Spike | `READY` | `Specified` | `Unassigned` | `None` | 准备权限 ADR、IPC 与 systemd 验证 |
| C1 | Source Fake Slice | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 A1、A2、A4、B1、B2 Verified |
| C2 | Decision/Enforcement Fake Slice | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 A1、A3、A4、B1 Verified |
| C3 | 完整 Crash Matrix | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 C1、C2 Verified |
| D1 | Go 类型与接口 | `BLOCKED` | `Specified` | `Unassigned` | `None` | 只冻结已通过 Spike/Slice 的接口 |
| D2 | SQLite migration | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 B1/C1/C2 相关证据 |
| D3 | Config Schema | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 ownership 与默认值验证 |
| D4 | ADR | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 A/B 结论与用户批准 |
| D5 | Contract Tests | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 C1–C3 可执行用例 |
| D6 | V0.4 同步 | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待新语义 Verified 后清理旧模型 |
| D7 | M0 Evidence Manifest | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 D1–D6 Evidence 汇总 |

## 4. M0 Gate 状态

| Gate | Contract | 当前结果 | Evidence | 未通过原因 | 解锁条件 |
|---|---|---|---|---|---|
| G18.1 Contract 完整性 | §18.1 | `FAIL` | `None` | 选择仅为 Specified；ADR、Spike、编译模型和 V0.4 同步未完成 | M0-A/B 与 D1/D4/D6 对应证据通过 |
| G18.2 可执行验证 | §18.2 | `FAIL` | `None` | Fake Slice、crash/concurrency/replay/firewall/durability 测试不存在 | M0-C、migration 和 Contract Tests 全部通过 |
| G18.3 产物一致性 | §18.3 | `FAIL` | `None` | migration、Config Schema、代码、测试和 Evidence Manifest 未落盘 | M0-D 全部责任产物通过 drift/一致性检查 |

M0 只有在 G18.1、G18.2、G18.3 全部为 `PASS` 后，才可改为
`COMPLETE / Frozen / GO`，并解锁 M1–M10 Entry Gate。

## 5. Phase 1 Work Package 状态

M1–M10 共 43 个 Work Package。所有 WP 都在本节逐项标记，不创建 43 个独立文档。
里程碑状态由其 WP 和 Exit Gate 推导，不另设一份可变状态。

### 5.1 里程碑 Gate

| 里程碑 | Work Package | Entry Gate 摘要 | Entry | Exit Gate | Exit | Evidence | Blocker |
|---|---|---|---|---|---|---|---|
| M1 Runtime | M1-WP1–WP6 | M0 `Frozen/GO`；语言/依赖 ADR；权限、Config、SQLite Verified；Web Security Frozen | `BLOCKED` | §23.4 | `NOT RUN` | `None` | M0 未 Frozen，前置 ADR/Gate 未通过 |
| M2 Sources | M2-WP1–WP5 | M1 Exit；receipt/RawRecord/Position/ID/generation/queue Contract Frozen | `BLOCKED` | §24.4 | `NOT RUN` | `None` | M1 Exit 未通过 |
| M3 Parser | M3-WP1–WP5 | M2 Exit；Parser DSL/依赖/Grok 资源 ADR 与基准 Frozen | `BLOCKED` | §25.4 | `NOT RUN` | `None` | M2 Exit 未通过 |
| M4 Detection | M4-WP1–WP3 | M3 Exit；Event ID/幂等 Verified；CEL/group/window/distinct 基准 Frozen | `BLOCKED` | §26.4 | `NOT RUN` | `None` | M3 Exit 未通过 |
| M5 Decision | M5-WP1–WP4 | M4 Exit；Decision/唯一键/Audit/Projection Verified | `BLOCKED` | §27.4 | `NOT RUN` | `None` | M4 Exit 未通过 |
| M6 Firewall | M6-WP1–WP4 | M1 权限/IPC/timeout；ownership/Snapshot/Plan Frozen；nftables Spike；Apply-confirm Gate | `BLOCKED` | §28.4 | `NOT RUN` | `None` | M0/M1、Spike 与 Apply-confirm Gate 未通过 |
| M7 Reconciliation | M7-WP1–WP4 | M5/M6 Exit；§11 安全顺序/fencing/三 domain/Retry Frozen；Maintenance Frozen | `BLOCKED` | §29.4 | `NOT RUN` | `None` | M5/M6 Exit 未通过 |
| M8 Notification | M8-WP1–WP3 | M1 Store/Config/Auth/secret；M5 Decision ID/DecisionActivated 提交边界稳定；SMTP/Job/幂等/退避/Cooldown Frozen | `BLOCKED` | §30.4 | `NOT RUN` | `None` | M1/M5 与 Notification Gate 未通过 |
| M9 Built-ins | M9-WP1–WP4 | WP1–3 等待 M2–M5；WP4/Exit 等待 M7；manifest/升级语义 Frozen | `BLOCKED` | §31.4 | `NOT RUN` | `None` | M2–M7 未通过 |
| M10 Productization | M10-WP1–WP5 | M1–M9 Exit；OpenAPI/CLI/Retention/upgrade/uninstall/frontend Gate Frozen | `BLOCKED` | §32.4、§36 | `NOT RUN` | `None` | M1–M9 Exit 未通过 |

### 5.2 WP 状态登记

| WP | 名称 | 推进状态 | 证据状态 | Evidence | Blocker / 下一步 |
|---|---|---|---|---|---|
| M1-WP1 | Process | `BLOCKED` | `Specified` | `None` | M0 未 Frozen |
| M1-WP2 | Config/CLI | `BLOCKED` | `Specified` | `None` | M0 未 Frozen |
| M1-WP3 | Store | `BLOCKED` | `Specified` | `None` | M0 未 Frozen |
| M1-WP4 | Privilege | `BLOCKED` | `Specified` | `None` | M0 未 Frozen |
| M1-WP5 | Management | `BLOCKED` | `Specified` | `None` | M0 未 Frozen |
| M1-WP6 | Maintenance | `BLOCKED` | `Specified` | `None` | M0 未 Frozen |
| M2-WP1 | Source Core | `BLOCKED` | `Specified` | `None` | M1 Exit 未通过 |
| M2-WP2 | File | `BLOCKED` | `Specified` | `None` | M1 Exit 未通过 |
| M2-WP3 | Journald | `BLOCKED` | `Specified` | `None` | M1 Exit 未通过 |
| M2-WP4 | Durability | `BLOCKED` | `Specified` | `None` | M1 Exit 未通过 |
| M2-WP5 | Verification | `BLOCKED` | `Specified` | `None` | M1 Exit 未通过 |
| M3-WP1 | Catalog | `BLOCKED` | `Specified` | `None` | M2 Exit 未通过 |
| M3-WP2 | Runtime | `BLOCKED` | `Specified` | `None` | M2 Exit 未通过 |
| M3-WP3 | Ownership | `BLOCKED` | `Specified` | `None` | M2 Exit 未通过 |
| M3-WP4 | Versioning | `BLOCKED` | `Specified` | `None` | M2 Exit 未通过 |
| M3-WP5 | Tooling | `BLOCKED` | `Specified` | `None` | M2 Exit 未通过 |
| M4-WP1 | Rule Catalog | `BLOCKED` | `Specified` | `None` | M3 Exit 未通过 |
| M4-WP2 | Engine | `BLOCKED` | `Specified` | `None` | M3 Exit 未通过 |
| M4-WP3 | Tooling | `BLOCKED` | `Specified` | `None` | M3 Exit 未通过 |
| M5-WP1 | Lifecycle | `BLOCKED` | `Specified` | `None` | M4 Exit 未通过 |
| M5-WP2 | Projection | `BLOCKED` | `Specified` | `None` | M4 Exit 未通过 |
| M5-WP3 | Expiry/Policy | `BLOCKED` | `Specified` | `None` | M4 Exit 未通过 |
| M5-WP4 | Verification | `BLOCKED` | `Specified` | `None` | M4 Exit 未通过 |
| M6-WP1 | Contract | `BLOCKED` | `Specified` | `None` | M0/M1 与 Firewall Entry Gate 未通过 |
| M6-WP2 | Backends | `BLOCKED` | `Specified` | `None` | M0/M1 与 Firewall Entry Gate 未通过 |
| M6-WP3 | Planning | `BLOCKED` | `Specified` | `None` | M0/M1 与 Firewall Entry Gate 未通过 |
| M6-WP4 | Recovery | `BLOCKED` | `Specified` | `None` | M0/M1 与 Apply-confirm Gate 未通过 |
| M7-WP1 | State | `BLOCKED` | `Specified` | `None` | M5/M6 Exit 未通过 |
| M7-WP2 | Planner | `BLOCKED` | `Specified` | `None` | M5/M6 Exit 未通过 |
| M7-WP3 | Operations | `BLOCKED` | `Specified` | `None` | M5/M6 Exit 未通过 |
| M7-WP4 | Verification | `BLOCKED` | `Specified` | `None` | M5/M6 Exit 未通过 |
| M8-WP1 | SMTP | `BLOCKED` | `Specified` | `None` | M1 Store/Config/Auth/secret 尚不可用 |
| M8-WP2 | Delivery | `BLOCKED` | `Specified` | `None` | M1/M5 未通过 |
| M8-WP3 | Operations | `BLOCKED` | `Specified` | `None` | M1/M5 未通过 |
| M9-WP1 | SSH | `BLOCKED` | `Specified` | `None` | M2–M5 未通过 |
| M9-WP2 | Nginx | `BLOCKED` | `Specified` | `None` | M2–M5 未通过 |
| M9-WP3 | Safety | `BLOCKED` | `Specified` | `None` | M2–M5 未通过 |
| M9-WP4 | Packaging/E2E | `BLOCKED` | `Specified` | `None` | M7 Exit 未通过 |
| M10-WP1 | Web | `BLOCKED` | `Specified` | `None` | M1–M9 未通过 |
| M10-WP2 | API/CLI | `BLOCKED` | `Specified` | `None` | M1–M9 未通过 |
| M10-WP3 | Operations | `BLOCKED` | `Specified` | `None` | M1–M9 未通过 |
| M10-WP4 | Docs/Performance | `BLOCKED` | `Specified` | `None` | M1–M9 未通过 |
| M10-WP5 | Release | `BLOCKED` | `Specified` | `None` | M1–M9 Exit 与 Release Gate 未通过 |

## 6. 工作包更新记录

发生状态变化时，在表格中更新当前值，并在此追加一行。不得删除历史记录。

| 时间 | 工作包/Gate | 旧状态 | 新状态 | Evidence | 原因/结果 |
|---|---|---|---|---|---|
| 2026-08-30 | M0-A | `BLOCKED` | `READY` | `None` | Phase 1 Contract 已达到 Specified，可开始行为不变量工作 |
| 2026-08-30 | M0-B | `BLOCKED` | `READY` | `None` | 风险项已定义，可开始隔离 Spike |
| 2026-08-30 | G18.1–G18.3 | `NOT RUN` | `FAIL` | `None` | 必需实现与可执行证据尚未落盘 |

## 7. 下一步队列

1. 为 M0-A 的 A1–A4 确认负责人和任务链接。
2. 为 M0-B 的 B1–B4 准备 Spike 环境与证据目录。
3. 先形成 M0-A/B 的首轮可验证接口，再判断是否解锁 M0-C。
4. 任一状态变化都同时登记 Evidence、Blocker 和本文件更新记录。
