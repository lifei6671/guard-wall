# Guard Phase 1 开发状态

> 本文件是 Phase 1 唯一可变进度源。技术语义见
> [Phase 1 Contract](../../contracts/guard-phase-1-m0-contract-freeze-v0.3.md)，
> 执行规则见 [README.md](README.md)。

## 1. 当前快照

| 项目 | 当前值 |
|---|---|
| 当前阶段 | `M0 Contract Freeze` |
| 当前结论 | `NO-GO` |
| 已完成 | `A1–A4`（`Implemented`，尚未 `Verified`） |
| 进行中 | `B1–B4`、`C1–C2`、`D1–D4` |
| 可启动 | `None`；当前 READY 项已进入执行 |
| 被阻塞 | `C3`、`D5–D7`、`M1–M10` |
| M0 证据状态 | `Implemented`（worktree preliminary；尚未 `Verified`） |
| Phase 1 发布状态 | `Not Released` |
| 当前 Evidence | `artifacts/evidence/M0/worktree/m0-a/`、`m0-b/`、`m0-c/`、`m0-d/` |
| 最近更新 | `2026-08-30` |

当前仓库已有 M0-A Contract、Crash Matrix manifest、两份 ADR、Go Core、SQLite
migration/Store、Config Schema、安全 credential reader，以及增强后的 C1/C2 preliminary
Slice；typed Processing outcomes 已同事务接入 Coordinator。仍缺真实 Parser/Detection
编排、Window post-commit 语义、C3 Crash Matrix、正式 Contract Tests、目标 Linux
durability 与 commit-bound Evidence Manifest。

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
| A1 | Core Model 与权威关系 | `COMPLETE` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-a/contract-review.md` | 运行级验证留给 C1/C2/D1/D2/D5 |
| A2 | Source delivery 与事务协调 | `COMPLETE` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-a/contract-review.md` | 运行级验证留给 C1/D1/D2/D5 |
| A3 | Decision / Enforcement / Reconcile | `COMPLETE` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-a/contract-review.md` | 运行级验证留给 C2/D1/D2/D5/D6 |
| A4 | Crash Matrix | `COMPLETE` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-a/contract-review.md` | manifest 已审查；runner 执行留给 C3/D5 |
| B1 | SQLite 并发、事务与 durability Spike | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/code-migration/result.md` | Go driver/PRAGMA/migration 已通过；缺目标 Linux SIGKILL/reboot/filesystem 与 power-loss 证据 |
| B2 | Source identity 与 replay Spike | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-c/source-slice/result.md` | 独立 Go golden tests 已通过；缺 restart、generation/replay 证据 |
| B3 | nftables Backend Spike | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-b/nftables-result.json` | 缺 production hook/priority、packet path、Snapshot/Plan、ownership 与恢复证据 |
| B4 | Agent/Enforcer 权限与 IPC Spike | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-b/ipc-result.json` | 缺跨 UID、systemd hardening、capability、对象校验与恢复证据 |
| C1 | Source Fake Slice | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-c/source-slice/result.md` | 多 Parser plan 与 typed outcome 同事务端口已通过；缺真实 Parser/Detection 编排、shutdown、Window post-commit、crash/durability |
| C2 | Decision/Enforcement Fake Slice | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | Decision/Policy/Fake/Reconcile 与 fabricated Automatic Decision SQLite 原子链通过；缺 SQLite lifecycle、expiry scheduler、health/restart |
| C3 | 完整 Crash Matrix | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 C1、C2 Verified |
| D1 | Go 类型与接口 | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/code-migration/result.md` | Core/Config/Decision/Fake/Source 与 typed Processing UoW 端口通过 race/vet；缺真实 Parser/Detection engine ports |
| D2 | SQLite migration | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/code-migration/result.md` | migration/checkpoint/generation/outcome 原子性已通过；缺 replay/reprocess refs、restart 与目标 Linux durability |
| D3 | Config Schema | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md` | Schema/default/range/ownership/drift 与 credential reader 已通过；缺完整 YAML、SMTP Ready、hot-reload/restart 和目标 Linux 权限验证 |
| D4 | ADR | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/adr-review.md` | ADR-0001/0002 已接受；delivery/durability/firewall/retry ADR 尚未齐全 |
| D5 | Contract Tests | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 C1–C3 可执行用例 |
| D6 | V0.4 同步 | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待新语义 Verified 后清理旧模型 |
| D7 | M0 Evidence Manifest | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 D1–D6 Evidence 汇总 |

## 4. M0 Gate 状态

| Gate | Contract | 当前结果 | Evidence | 未通过原因 | 解锁条件 |
|---|---|---|---|---|---|
| G18.1 Contract 完整性 | §18.1 | `FAIL` | `artifacts/evidence/M0/worktree/` | A 与 D1/D4 部分 Implemented；B 仍 preliminary，D6 未完成 | M0-B 与 D1/D4/D6 对应证据通过 |
| G18.2 可执行验证 | §18.2 | `FAIL` | `artifacts/evidence/M0/worktree/m0-c/` | C1/C2 仅最小纵切；C3、完整 replay/firewall/durability 未运行 | M0-C、migration 和 Contract Tests 全部通过 |
| G18.3 产物一致性 | §18.3 | `FAIL` | `artifacts/evidence/M0/worktree/m0-d/` | migration/代码/Config Schema 已落盘；D5–D7 与 commit-bound Manifest 缺失 | M0-D 全部责任产物通过 drift/一致性检查 |

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
| 2026-08-30 | A1–A4、B1–B4 | `READY` | `IN_PROGRESS` | `None` | 用户接受 D-002 并授权继续 M0-A/M0-B |
| 2026-08-30 | A1–A4 | `IN_PROGRESS / Specified` | `COMPLETE / Implemented` | `artifacts/evidence/M0/worktree/m0-a/contract-review.md` | 责任产物齐全；两轮交叉审查已清零 P0/P1 |
| 2026-08-30 | B1–B4 | `IN_PROGRESS / Specified` | `IN_PROGRESS / Implemented` | `artifacts/evidence/M0/worktree/m0-b/` | preliminary Spike 通过；仍有明确 unverified domains |
| 2026-08-30 | C1–C2 | `BLOCKED / Specified` | `IN_PROGRESS / Implemented` | `artifacts/evidence/M0/worktree/m0-c/` | 两条最小 Fake Slice 通过 race/vet 与 P0/P1 复审；完整 §17 用例未完成 |
| 2026-08-30 | D1、D2、D4 | `BLOCKED / Specified` | `IN_PROGRESS / Implemented` | `artifacts/evidence/M0/worktree/m0-d/` | Go module/driver、Core、migration/Store 与 ADR-0002 落盘；目标 Linux 与完整端口未验证 |
| 2026-08-30 | C1、C2、D1、D2 | `IN_PROGRESS / Implemented` | `IN_PROGRESS / Implemented` | `artifacts/evidence/M0/worktree/m0-c/`、`m0-d/` | Source safety、Decision/Policy 与 Store 语义扩展完成；交叉复审清零已实现范围 P0/P1 |
| 2026-08-30 | D3 | `BLOCKED / Specified` | `IN_PROGRESS / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md` | 用户确认配置契约；Schema ownership/default/range/unknown/drift 测试通过 |
| 2026-08-30 | C1、C2、D1–D3 | `IN_PROGRESS / Implemented` | `IN_PROGRESS / Implemented` | `artifacts/evidence/M0/worktree/m0-c/`、`m0-d/` | 多 Parser plan、typed outcome 七表事务接线与 credential reader 完成；交叉复审无 P0/P1，全仓验证通过 |

## 7. 下一步队列

1. 补齐 C1：真实 Parser/SecurityEvent/Detection 编排接入 plan/UoW、Window post-commit
   幂等、poison、shutdown 与 crash/restart。
2. 补齐 C2：SQLite-backed Decision duplicate/manual replace 生命周期、expiry/SafetyGrace
   scheduler、health 与 restart recovery。
3. 补齐 D3 runtime：完整 YAML、SMTP Ready、hot-reload/restart 与目标 Linux
   credential 权限；随后执行 C3、D5–D7 和 Linux SIGKILL/restart/durability。
4. B3/B4 继续补齐生产等价隔离证据；保持 G18.1–G18.3 `FAIL`、M0 `NO-GO`。
