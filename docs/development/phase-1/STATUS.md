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
| 最近更新 | `2026-08-31` |

当前仓库已有 M0-A Contract、Crash Matrix manifest、两份 ADR、Go Core、SQLite
migration/Store、Config Schema、安全 credential reader，以及增强后的 C1/C2 preliminary
Slice；C1 已接通 Parser→SecurityEvent→Rule→Window→SQLite receipt 的成功/Rule poison 编排，
并补充 fixed accepted set 的 drain/checkpoint/Close 与 cancellation/reopen/replay primitive tests；
C2 已接通 Automatic Decision 创建/重复抑制、SQLite Manual create/replace、expiry，且三条
入口都在原事务内统一物化 Target generation、全局 SnapshotRevision、retry reset 与 Audit，
confirmed commit 后仅 Wake 语义变化 Target；同进程 Backend availability recovery 与 retry/pending-Probe SQLite
关闭重开恢复、health-event/wakeup Dispatcher primitives、60s expiration scheduler、启动 due sweep、
首次 Apply expiry fence、production Desired PlanProvider、共享虚拟时钟与 expiration runtime owner 已通过，
且真实 SQLite→Fake Snapshot 的 62s 虚拟时间闭环已验证；完整三域 Observed SQLite authority、
Probe/Unknown fencing 回写与 fresh startup Probe 原语已获用户 Code Review 通过；Dispatcher-owned Backend
health lifecycle、双 context Probe、精确 capped backoff、mutation gate 与 Health/Metric read model 已进入 Review；但仍缺真实 Enforcer/IPC health 事件源、
可执行进程 composition/runtime startup、shutdown/crash、C3、正式 Contract Tests、
目标 Linux durability 与 commit-bound Evidence Manifest。

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
| C1 | Source Fake Slice | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-c/source-slice/result.md` | shutdown primitives 与 cancellation/reopen/replay 已通过；缺真实 intake/drain owner、30s timeout、SIGTERM/crash/durability |
| C2 | Decision/Enforcement Fake Slice | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | Automatic/Manual/expiry generation/SnapshotRevision/Wake、retry/pending-Probe SQLite 恢复、60s scheduler、62s SQLite→Fake 闭环、完整三域 Observed 与 Dispatcher-owned Backend health lifecycle 原语已通过用户 Review；仍缺真实 Enforcer/IPC health 源、可执行进程 composition/runtime startup 与真实进程 restart |
| C3 | 完整 Crash Matrix | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 C1、C2 Verified |
| D1 | Go 类型与接口 | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/code-migration/result.md` | Desired finalizer、typed commit-unknown/post-commit Wake、共享 Clock、Desired PlanProvider/runtime owner、完整 Observed 端口与 process-local Backend health read model 通过 race/vet；缺可执行进程 composition/真实 IPC health source wiring |
| D2 | SQLite migration | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/code-migration/result.md` | migration 0001–0005、Target Desired/retry/probe/Observed 原子性、v4 cache 安全失效与 SQLite close/reopen 已通过；缺 replay/reprocess refs、真实进程/runtime restart 与目标 Linux durability |
| D3 | Config Schema | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md` | Schema/default/range/ownership/drift 与 credential reader 已通过；缺完整 YAML、SMTP Ready、hot-reload/restart 和目标 Linux 权限验证 |
| D4 | ADR | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/adr-review.md` | ADR-0001/0002 已接受；delivery/durability/firewall/retry ADR 尚未齐全 |
| D5 | Contract Tests | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 C1–C3 可执行用例 |
| D6 | V0.4 同步 | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待新语义 Verified 后清理旧模型 |
| D7 | M0 Evidence Manifest | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 D1–D6 Evidence 汇总 |

## 4. M0 Gate 状态

| Gate | Contract | 当前结果 | Evidence | 未通过原因 | 解锁条件 |
|---|---|---|---|---|---|
| G18.1 Contract 完整性 | §18.1 | `FAIL` | `artifacts/evidence/M0/worktree/` | A 与 D1/D4 部分 Implemented；B 仍 preliminary，D6 未完成 | M0-B 与 D1/D4/D6 对应证据通过 |
| G18.2 可执行验证 | §18.2 | `FAIL` | `artifacts/evidence/M0/worktree/m0-c/` | C1 shutdown primitives 与 C2 generation/snapshot/post-commit wake/60s scheduler/first-Apply fence/62s Fake/Backend health lifecycle 已增强；真实可执行 runtime/IPC health source/restart、C3、firewall/durability 仍未运行 | M0-C、migration 和 Contract Tests 全部通过 |
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
| 2026-08-30 | C1、D1 | `IN_PROGRESS / Implemented` | `IN_PROGRESS / Implemented` | `artifacts/evidence/M0/worktree/m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | Parser/Event/Rule success、Parser poison、Window post-commit/rollback/unknown 与跨 Coordinator replay 完成；已实现范围复审无 P0/P1，Rule permanent/C2 semantic port 仍未实现 |
| 2026-08-30 | C1、C2、D1、D2 | `IN_PROGRESS / Implemented` | `IN_PROGRESS / Implemented` | `artifacts/evidence/M0/worktree/m0-c/`、`m0-d/code-migration/result.md` | Rule terminal outcome/poison 与 Automatic create/suppress semantic port 完成；真实 SQLite 并发、rollback/retry、commit-unknown/replay 通过，已实现范围复审无 P0/P1 |
| 2026-08-30 | C2、D1 | `IN_PROGRESS / Implemented` | `IN_PROGRESS / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | preliminary SQLite Manual replace/expiry、typed commit-unknown 与 Fake SafetyGrace 完成；并发/rollback/幂等复审无 P0/P1，完整 generation/scheduler/restart 未验证 |
| 2026-08-30 | C2 Manual/expiry batch | `REVIEW` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | 用户 Code Review 明确通过；C2 总项仍为 `IN_PROGRESS`，进入 C1 shutdown/checkpoint 下一依赖项 |
| 2026-08-30 | C1 shutdown primitives | `DONE` | `Implemented` | `artifacts/evidence/M0/worktree/m0-c/source-slice/result.md` | 用户 Code Review 明确通过；fixed accepted set drain→pending checkpoint final Flush→Audit readback→Close 与 cancellation/reopen/replay 已接受，不代表生产 SIGTERM owner |
| 2026-08-30 | C2 availability recovery primitive | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | 同一 Controller 内 Apply/Probe 失联不重置 attempt，恢复后 Probe-first 并使用剩余预算；复审无 P0/P1，不代表 health-event integration 或 restart persistence |
| 2026-08-30 | C2 availability recovery primitive | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | 用户 Code Review 明确通过；同进程恢复原语已接受，C2 总项仍为 `IN_PROGRESS`，完整 health-event/restart persistence 未验证 |
| 2026-08-30 | C2 repeated Apply unavailability budget | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | 同一 Target key 六次 Apply 不可用保持精确退避并进入 Degraded；最终恢复 Probe 不产生第七次 mutation 或 Retry Audit；复审无 P0/P1 |
| 2026-08-30 | C2 repeated Apply unavailability budget | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | 用户 Code Review 明确通过；六次 mutation 上限与精确退避原语已接受，不代表 health-event integration 或 restart persistence |
| 2026-08-30 | C2 exhausted Unknown observation recovery | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | 同一 Target key 六次 Unknown 后，完整匹配的权威 Probe 可 observation-only Converged；attempt/Apply 保持 6，无第七次 mutation 或 Retry Audit；复审无 P0/P1 |
| 2026-08-30 | C2 exhausted Unknown observation recovery | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | 用户 Code Review 明确通过；observation-only 收敛原语已接受，不代表持久化预算或 restart recovery |
| 2026-08-30 | C2 retry/pending-Probe SQLite recovery | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | 三 domain retry ledger、绝对退避、startup Probe、精确/superseded pending Probe、旧 RetryEpoch、attempt 6/Degraded 与 typed commit-unknown readback 可跨 SQLite 关闭重开恢复；pre-Apply 持久化失败不触达 Backend，复审无 P0/P1/P2；不代表真实进程 restart、完整 Observed/runtime startup 或 Linux durability |
| 2026-08-30 | C2 retry/pending-Probe SQLite recovery | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；SQLite close/reopen retry/pending-Probe recovery 原语已接受，C2 总项仍为 `IN_PROGRESS`；不代表 health-event/wakeup、真实进程 restart、完整 Observed/runtime startup 或 Linux durability |
| 2026-08-30 | C2 health-event/wakeup Dispatcher primitive | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | bounded key-coalescing queue、latest-Plan reread、Backend healthy observation Probe、persisted absolute deadline 与多键 startup snapshot 已实现；race/vet/交叉构建和独立复审通过，等待用户 Code Review；不代表生产 monitor、post-commit producer、expiry scheduler 或真实进程 restart |
| 2026-08-30 | C2 health-event/wakeup Dispatcher primitive | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | 用户 Code Review 明确通过；Dispatcher 调度与恢复原语已接受，C2 总项仍为 `IN_PROGRESS`；不代表生产 monitor、post-commit producer、expiry scheduler、完整 runtime startup 或真实进程 restart |
| 2026-08-30 | C2 Desired generation/snapshot + post-commit Wake | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | migration 0004、Automatic/Manual/expiry 事务末尾统一 Intent finalization、一次 SnapshotRevision、仅变更 Target retry reset 与 confirmed/readback-proven commit Wake 已实现；race/vet/交叉构建通过，等待用户 Code Review；不代表 expiry scheduler、生产 health monitor/runtime owner 或真实进程 restart |
| 2026-08-30 | C2 Desired generation/snapshot + post-commit Wake | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；事务末尾 Desired authority、generation/SnapshotRevision 与 confirmed/readback-proven post-commit Wake 原语已接受，C2 总项仍为 `IN_PROGRESS`；不代表 expiry scheduler、生产 health monitor/runtime owner 或真实进程 restart |
| 2026-08-30 | C2 expiration scheduler + first-Apply fence | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | 启动立即 Expire、60s 绝对周期、慢 sweep 追赶、durable pending 恢复、首次 Apply/Probe/持久化前 expiry fence 与 stale fresh reread 已实现；race/vet/交叉构建及三路独立复审通过，等待用户 Code Review；62s 端到端、production PlanProvider/runtime owner 与真实 restart 仍为 `NOT_VERIFIED` |
| 2026-08-30 | C2 expiration scheduler + first-Apply fence | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；60s scheduler、startup due sweep/pending recovery 与 first-Apply expiry fence 原语已接受，C2 总项仍为 `IN_PROGRESS`；62s 端到端、production PlanProvider/runtime owner 与真实 restart 仍为 `NOT_VERIFIED` |
| 2026-08-30 | C2 expiry 62s runtime composition | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | transaction-consistent Desired read、共享 Clock、production Desired PlanProvider、ordered expiration runtime owner 与真实 SQLite→Fake 62s 虚拟时间测试已实现；全仓 race/vet/双架构构建和独立复审通过，等待用户 Code Review；不代表生产 executable wiring、health monitor、真实 Backend/restart |
| 2026-08-30 | C2 expiry 62s runtime composition | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；transaction-consistent Desired read、共享 Clock、production Desired PlanProvider、ordered expiration runtime owner 与 SQLite→Fake 62s 虚拟时间闭环已接受；C2 总项仍为 `IN_PROGRESS`，不代表生产 executable wiring、health monitor、真实 Backend/restart |
| 2026-08-31 | C2 complete Observed persistence/startup primitive | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | migration 0005、Infrastructure/Policy/Target 完整 Observed、逐 key/时间/Desired fences、Probe 成功/失败/歧义写回、fresh startup Probe 与 commit-unknown 双 readback 已实现；两路 Tier-3 审查修复 1 个 schema P2 后 P0/P1/P2/P3 均无，等待用户 Code Review；不代表生产 health monitor/executable、真实 Backend/process restart 或 Linux durability |
| 2026-08-31 | C2 complete Observed persistence/startup primitive | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；migration 0005、完整三域 Observed authority、Probe/Unknown fencing、fresh startup Probe 与 commit-unknown readback 原语已接受；C2 总项仍为 `IN_PROGRESS`，不代表生产 health monitor/executable、真实 Backend/process restart 或 Linux durability |
| 2026-08-31 | C2 Backend health monitor + runtime ownership primitive | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | Dispatcher-owned startup Degraded/backoff/recovery、双 context Probe、fatal persistence split、Health/Metric read model 与并发交错测试已实现；独立审查修复 startup/timer/event ordering 3 个 P1，fresh code/test/evidence/integration 终审 P0/P1/P2/P3 均无，等待用户 Code Review；不代表真实 IPC health source、executable 或 process restart |
| 2026-08-31 | C2 Backend health monitor + runtime ownership primitive | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；process-local Backend health lifecycle、双 context Probe、fatal persistence split 与 Health/Metric read model 原语已接受；C2 总项仍为 `IN_PROGRESS / Implemented`，不代表真实 IPC health source、executable、process restart 或 Linux durability |

## 7. 下一步队列

1. C2 Backend health monitor + runtime ownership primitive 已获用户通过，状态为 `DONE / Implemented`；
   该批只提供 process-local health lifecycle/read model，不代表真实 Enforcer/IPC source 或 executable。
2. 回到 M0 下一条已满足依赖的 preliminary 工作；生产 intake/drain owner、30s timeout、
   SIGTERM/crash/durability、真实 health source/可执行进程 composition/runtime startup 与真实进程 restart 仍需分别评估授权；
   已完成的 62s SQLite→Fake 虚拟时间证据不替代这些生产验证。
3. 补齐 D3 runtime：完整 YAML、SMTP Ready、hot-reload/restart 与目标 Linux
   credential 权限；随后执行 C3、D5–D7 和 Linux SIGKILL/restart/durability。
4. B3/B4 继续补齐生产等价隔离证据；保持 G18.1–G18.3 `FAIL`、M0 `NO-GO`。
