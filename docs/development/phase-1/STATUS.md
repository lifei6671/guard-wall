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
| 最近更新 | `2026-09-01` |

当前仓库已有 M0-A Contract、Crash Matrix manifest、两份 ADR、Go Core、SQLite
migration/Store、Config Schema、安全 credential reader、single-document YAML loader、SMTP credential
readiness gate，以及增强后的 C1/C2 preliminary
Slice；C1 已接通 Parser→SecurityEvent→Rule→Window→SQLite receipt 的成功/Rule poison 编排，
并补充 fixed accepted set 的 drain/checkpoint/Close 与 cancellation/reopen/replay primitive tests；
C2 已接通 Automatic Decision 创建/重复抑制、SQLite Manual create/replace、expiry，且三条
入口都在原事务内统一物化 Target generation、全局 SnapshotRevision、retry reset 与 Audit，
confirmed commit 后仅 Wake 语义变化 Target；同进程 Backend availability recovery 与 retry/pending-Probe SQLite
关闭重开恢复、health-event/wakeup Dispatcher primitives、60s expiration scheduler、启动 due sweep、
首次 Apply expiry fence、production Desired PlanProvider、共享虚拟时钟与 expiration runtime owner 已通过，
且真实 SQLite→Fake Snapshot 的 62s 虚拟时间闭环已验证；完整三域 Observed SQLite authority、
Probe/Unknown fencing 回写与 fresh startup Probe 原语已获用户 Code Review 通过；Dispatcher-owned Backend
health lifecycle、双 context Probe、精确 capped backoff、mutation gate 与 Health/Metric read model 已获用户
Code Review 通过；C1 signal-aware lifecycle owner 也已获用户 Code Review 通过，但仍缺真实
Source reader/management intake、Enforcer/IPC health 事件源、可执行进程 composition/runtime startup、
真实 shutdown/crash、C3、正式 Contract Tests、
目标 Linux durability 与 commit-bound Evidence Manifest。
另按用户确认的 GORM-0b 边界接入 `gorm.io/gorm v1.31.2` core 与 project-owned
modernc Dialector/non-closing ConnPool；GORM-1a 仅将 `PutParserOutcome` 的七列 INSERT
迁移到绑定既有 raw `*sql.Tx` 的不可最终化私有 session。checksummed migration、PRAGMA、
其余 UnitOfWork/关键一致性 SQL、Schema、公共 API 和 Store pool/事务最终化所有权保持不变。

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
| B1 | SQLite 并发、事务与 durability Spike | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-b/sqlite-result.json` | Go driver/PRAGMA/migration 与 Ubuntu WSL2 cross-process SIGKILL→reopen committed/uncommitted matrix 已通过；缺 OS reboot、filesystem barrier 与 power-loss 证据 |
| B2 | Source identity 与 replay Spike | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-b/identity-result.json`、`m0-c/source-slice/result.md` | golden vectors、Ubuntu WSL2 clean restart-replay、两个 committed-boundary generation transition SIGKILL 窗口、真实 opaque Journald cursor reopen 与 processing UnitOfWork transaction-internal SIGKILL rollback/direct replay 已通过；缺真实 File/Journald reader、copytruncate、Source-state internal crash、cursor invalidation/vacuum/resume 与 replay/reprocess refs |
| B3 | nftables Backend Spike | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-b/nftables-result.json` | 缺 production hook/priority、packet path、Snapshot/Plan、ownership 与恢复证据 |
| B4 | Agent/Enforcer 权限与 IPC Spike | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-b/ipc-result.json` | WSL2 framing fail-closed、四操作 allowlist、正式 IPC v1 request Schema、mutation-only response Schema、production payload validator、frame reader、accepted-connection `SO_PEERCRED` gate 与 listener/socket lifecycle library 已通过；缺 Probe/Snapshot 成功响应、production response/writer/client/executor、真实 `/run/guard` root:guard/跨 UID、真实 Snapshot/owner/capability/object-role、systemd hardening、持续 fuzz 与恢复证据 |
| C1 | Source Fake Slice | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-c/source-slice/result.md` | Queue Seal fixed accepted set、单 Source runtime owner、drain/Flush/Audit/Close、timeout 不提前 Close 与 commit-unknown readback 已通过 race；缺真实 Source reader/management intake、signal executable、进程 restart 与 Linux durability |
| C2 | Decision/Enforcement Fake Slice | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md` | Automatic/Manual/expiry generation/SnapshotRevision/Wake、retry/pending-Probe SQLite 恢复、60s scheduler、62s SQLite→Fake 闭环、完整三域 Observed 与 Dispatcher-owned Backend health lifecycle 原语已通过用户 Review；仍缺真实 Enforcer/IPC health 源、可执行进程 composition/runtime startup 与真实进程 restart |
| C3 | 完整 Crash Matrix | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 C1、C2 Verified |
| D1 | Go 类型与接口 | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/code-migration/result.md` | Desired/Reconcile 端口及 Source Queue Seal/RunSourceRuntime 已通过 race/vet；缺真实 executable composition、Source reader/management intake 与 IPC health source wiring |
| D2 | SQLite migration | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/code-migration/result.md` | migration 0001–0005、Target Desired/retry/probe/Observed 原子性、v4 cache 安全失效与 SQLite close/reopen 已通过；缺 replay/reprocess refs、真实进程/runtime restart 与目标 Linux durability |
| D3 | Config Schema | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md` | Schema/default/range/ownership/drift、credential reader、YAML loader/resource cap、SMTP readiness、atomic logging owner 与 Ubuntu WSL2 native file-to-Ready library integration 已实现并通过；缺真实 SMTP worker、production packaging/systemd/parent trust、config watcher/executable wiring 和目标 Linux 安装验证 |
| D4 | ADR | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-d/adr-review.md` | ADR-0001/0002 已接受；delivery/durability/firewall/retry ADR 尚未齐全 |
| D5 | Contract Tests | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 C1–C3 可执行用例 |
| D6 | V0.4 同步 | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待新语义 Verified 后清理旧模型 |
| D7 | M0 Evidence Manifest | `BLOCKED` | `Specified` | `Unassigned` | `None` | 等待 D1–D6 Evidence 汇总 |

## 4. M0 Gate 状态

| Gate | Contract | 当前结果 | Evidence | 未通过原因 | 解锁条件 |
|---|---|---|---|---|---|
| G18.1 Contract 完整性 | §18.1 | `FAIL` | `artifacts/evidence/M0/worktree/` | A 与 D1/D4 部分 Implemented；B 仍 preliminary，D6 未完成 | M0-B 与 D1/D4/D6 对应证据通过 |
| G18.2 可执行验证 | §18.2 | `FAIL` | `artifacts/evidence/M0/worktree/` | C1/C2 preliminary runtime 原语与 B1 Linux process-level SIGKILL→reopen 已运行；真实 executable/IPC health source/service restart、C3、firewall、OS reboot/filesystem barrier/power-loss durability 与完整 Contract Tests 仍未完成 | M0-C、migration、剩余 durability 域和 Contract Tests 全部通过 |
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
| 2026-08-31 | C2 stale completion / receipt replay / Evidence 修复 | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/enforcement-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；旧 generation completion stale 清理、base adapter 无 wake receipt replay 与实际执行日期修复已接受。证据仍为 worktree preliminary，不提升 C2 总项或 M0 Gate |
| 2026-08-31 | C1 signal-aware lifecycle owner | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | 用户批准导出内部 Go 边界；Queue Seal、单 worker drain、final Flush/Audit barrier/Close、timeout 与 commit-unknown readback 不提前 Close 已实现并通过 targeted/full race、vet、module 与双架构构建；final Tier-3 独立终审完整覆盖且 P0/P1/P2/P3 均无，等待用户 Code Review |
| 2026-08-31 | C1 signal-aware lifecycle owner | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；Queue Seal、单 worker fixed-set drain、final Flush/Audit barrier/Close、timeout 与 commit-unknown readback 不提前 Close 已接受；C1 总项仍为 `IN_PROGRESS / Implemented`，不代表真实 Source reader/management intake、signal executable、进程 restart 或 Linux durability |
| 2026-08-31 | D3-a single-document YAML loader | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | 用户批准 `go.yaml.in/yaml/v3 v3.0.5` 与 `config.Load` 行为扩展；普通 block/flow YAML、注释、quoted/folded strings 与 JSON compatibility 已实现，duplicate/non-string key、多文档、alias/merge/tag、隐式高风险类型已拒绝；targeted/full race、vet、module、双架构构建通过，final Tier-3 独立终审完整覆盖且 P0/P1/P2/P3 均无，等待用户 Code Review |
| 2026-08-31 | D3-a single-document YAML loader | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；受限对象图语义的 single-document YAML loader 与依赖边界已接受。D3 总项仍为 `IN_PROGRESS / Implemented`，不代表 SMTP Ready、hot-reload/restart owner、YAML resource cap 或目标 Linux credential 权限验证 |
| 2026-08-31 | D3-b SMTP credential readiness gate | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | 用户批准导出内部 Go 组合契约；secure read 后才可由 active worker 宣布 Ready，失败/取消/重复 Run/late callback 不越过屏障，secret 全路径清零。checkpoint/final review 分别修复 worker error 泄密与 post-Ready sentinel collision 两个 P1；repair round 2 fresh delta review 通过，final P0/P1/P2/P3 均无，等待用户 Code Review。仅为 preliminary runtime gate，不代表真实 SMTP/TLS/队列或目标 Linux file-to-Ready |
| 2026-08-31 | D3-b SMTP credential readiness gate | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；secure credential read 后的 active-worker Ready barrier、secret 清零与两轮 P1 repair 已接受。D3 总项仍为 `IN_PROGRESS / Implemented`，不代表真实 SMTP/TLS/队列或目标 Linux file-to-Ready |
| 2026-08-31 | D3-c atomic logging reload/restart owner primitive | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | 用户批准导出内部 Go Runtime 契约；`slog.LevelVar` 原子发布 level，Reload 在同一临界区拒绝任一 restart-bound delta 且不部分更新。11 个 restart 字段、Schema policy drift、并发与 slog handler 集成测试通过；final CHILD_AGENT full+delta 复审 P0/P1/P2/P3 均无，等待用户 Code Review。不代表 production watcher/executable wiring 或目标 Linux native reload/restart |
| 2026-08-31 | D3-c atomic logging reload/restart owner primitive | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；原子日志级别发布、restart-bound delta 整体拒绝、FieldPolicy drift 与并发/slog 集成测试已接受。D3 总项仍为 `IN_PROGRESS / Implemented`，不代表 production watcher/executable wiring、目标 Linux native reload/restart、YAML resource cap 或目标 Linux credential 权限验证 |
| 2026-08-31 | D3-d YAML resource cap | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | 用户批准 64 KiB / 512 unique Nodes / maximum Node depth 32 的导出 `config.Load` 与 Contract 边界；exact/one-over 测试、targeted/full race、vet、module、Linux 双架构 build 通过，独立 Tier-3 full-scope review P0/P1/P2/P3 均无，等待用户 Code Review。不代表 parser benchmark、CI、commit-bound Evidence 或目标 Linux runtime |
| 2026-08-31 | D3-d YAML resource cap | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；64 KiB 输入、512 unique Nodes、maximum Node depth 32 与 fixed sanitized failure 已接受。D3 总项仍为 `IN_PROGRESS / Implemented`，不代表 parser benchmark、CI、commit-bound Evidence、production wiring 或目标 Linux runtime |
| 2026-08-31 | D3-e Linux native credential file-to-Ready integration | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | Ubuntu 22.04.5 WSL2 以 non-root `guard` 通过 production reader 的 0640/0440 Ready 与七项拒绝矩阵；临时 user/group、fixture、binary/script 均清理。初次 full-scope review 代码/测试 P0-P3 均无，Evidence repair round 1 补齐可复现记录后，fresh delta review 将缺口标为 resolved，最终 Coverage `COMPLETE`、Freshness `FRESH`、Gate `PASSED`；等待用户 Code Review。不代表 production packaging/systemd、persistent identity、parent trust、真实 SMTP 或 executable wiring |
| 2026-08-31 | D3-e Linux native credential file-to-Ready integration | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-d/config-schema/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；Ubuntu WSL2 non-root production-reader integration、九项 fixture matrix、cleanup 与 Evidence repair 已接受。D3 总项仍为 `IN_PROGRESS / Implemented`，不代表 production packaging/systemd、persistent identity、parent trust、真实 SMTP、installed file-to-Ready 或 executable wiring |
| 2026-08-31 | B1-f Linux native SQLite SIGKILL→reopen integration | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-b/sqlite-result.json`、`m0-d/code-migration/result.md` | Ubuntu WSL2 production `Store.Open` committed/uncommitted matrix 初跑与 `count=20` 通过；targeted/full race、vet/module、双架构 build 与 cleanup 通过。Tier-3 checkpoint 无 findings；final review 的 Evidence P1/HANDOFF P2 经 repair round 1 fresh delta 标为 resolved，最终 P0-P3 均无、Coverage `COMPLETE`、Freshness `FRESH`、Gate `PASSED`，等待用户 Code Review。不代表 service/OS reboot、filesystem barrier、power loss、production executable 或 commit-bound Evidence |
| 2026-08-31 | B1-f Linux native SQLite SIGKILL→reopen integration | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-b/sqlite-result.json`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；process SIGKILL committed/uncommitted reopen matrix 与 Evidence repair 已接受。B1 总项仍为 `IN_PROGRESS / Implemented`；不代表 service/OS reboot、filesystem barrier、power loss、production executable 或 commit-bound Evidence |
| 2026-08-31 | B2-f Linux native Source generation restart/replay integration | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-b/identity-result.json`、`m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | 两个 fresh Linux helper 通过 production Store/Source adapter 验证旧 Draining+新 Open、双 receipt、连续 checkpoint、Delivery/Event ID 跨 clean restart 稳定且跨 generation 不碰撞；Ubuntu WSL2 初跑/count=20、全套验证与 Tier-3 code checkpoint/final review 通过。最终 `APPROVED_WITH_FOLLOWUPS`：无 P0/P1/P3，Windows Temp binary cleanup 为非阻断 P2；等待用户 Code Review。不代表真实 reader、copytruncate、Journald、crash、replay/reprocess refs、OS/power loss 或 executable |
| 2026-08-31 | B2-f Linux native Source generation restart/replay integration | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-b/identity-result.json`、`m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；clean cross-process generation/receipt/checkpoint restart-replay 与已声明的非阻断 Temp cleanup P2 已接受。B2 总项仍为 `IN_PROGRESS / Implemented`；真实 reader、copytruncate、Journald、transition crash、replay/reprocess refs、OS/power loss 与 executable 仍未验证 |
| 2026-08-31 | B2-g Source generation transition SIGKILL/recovery integration | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-b/identity-result.json`、`m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | 两个 committed-boundary crash 窗口、session-local sequence reset、跨进程稳定 ID 与连续 checkpoint 已通过 Ubuntu WSL2 初跑/count=20 和全套验证。code checkpoint repair round 1 与 final Evidence repair round 2 最终均 `COMPLETE / FRESH / PASSED`，P0-P3 全无；等待用户 Code Review。不代表 transaction-internal crash、真实 reader/copytruncate、Coordinator replay/Parser 重评、Journald、refs、OS/power loss 或 executable |
| 2026-08-31 | B2-g Source generation transition SIGKILL/recovery integration | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-b/identity-result.json`、`m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；两个 committed-boundary SIGKILL 窗口、session-local sequence reset、跨进程稳定 ID、连续 checkpoint 与 Evidence repair 已接受。B2 总项仍为 `IN_PROGRESS / Implemented`；所有 transaction-internal crash、真实 reader/copytruncate、Coordinator replay/Parser 重评、Journald、refs、OS/power loss 与 executable 边界不变 |
| 2026-08-31 | B2-h Linux native Journald cursor restart/replay integration | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-b/identity-result.json`、`m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | Ubuntu WSL2 两个真实 opaque cursor 经 production position/Delivery identity、双 terminal receipt、连续 checkpoint 与 fresh-process reopen 精确回读；初跑/count=20 无 SKIP，全套 race/vet/module/双架构 integration-test build 与 cleanup 通过。code repair round 1 与 final Evidence repair round 1 最终均 `COMPLETE / FRESH / PASSED`，P0-P3 全无；等待用户 Code Review。不代表 production Journald reader/Coordinator replay、cursor invalidation/vacuum/resume、完整 at-least-once、service/OS/power loss 或 commit-bound Evidence |
| 2026-08-31 | B2-h Linux native Journald cursor restart/replay integration | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-b/identity-result.json`、`m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；真实 opaque cursor 的 production identity/receipt/checkpoint fresh-process reopen、无 SKIP WSL matrix 与两轮 repair 已接受。B2 总项仍为 `IN_PROGRESS / Implemented`；production Journald reader/Coordinator replay、cursor invalidation/vacuum/resume、完整 at-least-once、service/OS/power loss 与 commit-bound Evidence 仍未验证 |
| 2026-08-31 | B2-i Linux native processing transaction-internal SIGKILL/replay integration | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-b/identity-result.json`、`m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | production UnitOfWork 八项未提交写在精确 SIGKILL/fresh reopen 后全部不可见，receipt/checkpoint 不前进；direct UoW replay 后八表各一并直接推进 sequence-1 checkpoint。Ubuntu WSL2 初跑/count=20、全套验证、独立 code checkpoint 与 final full-scope review 均 `COMPLETE / FRESH / PASSED`，P0-P3 全无；等待用户 Code Review。不代表 Coordinator/Parser+Rule 重评、真实 reader、commit-unknown、Source-state internal crash、production checkpoint manager、完整 at-least-once、service/OS/power loss 或 commit-bound Evidence |
| 2026-08-31 | B2-i Linux native processing transaction-internal SIGKILL/replay integration | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-b/identity-result.json`、`m0-c/source-slice/result.md`、`m0-d/code-migration/result.md` | 用户 Code Review 明确通过；production UnitOfWork transaction-internal SIGKILL rollback、direct replay、direct sequence-1 checkpoint 与完整终审已接受。B2 总项仍为 `IN_PROGRESS / Implemented`；Coordinator/Parser+Rule 重评、真实 reader、commit-unknown、Source-state internal crash、production checkpoint manager、完整 at-least-once、service/OS/power loss 与 commit-bound Evidence 仍未验证 |
| 2026-08-31 | B4-f IPC framing fail-closed matrix | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 现有 Linux Spike 的 allocation-before-cap、截断、严格 JSON、未知字段/版本/operation、多 JSON value、四操作 allowlist 与 fuzz seeds 已通过 Ubuntu WSL2 初跑/count=20、原 socket/`SO_PEERCRED` smoke、Linux vet、双架构编译和全仓回归。独立 code checkpoint 无 findings；final Evidence exact-recipe P1 经 repair round 1 fresh delta 标为 resolved，最终 `COMPLETE / FRESH / PASSED`、P0-P3 全无，等待用户 Code Review。不代表 production parser/object/Plan validator、跨 UID、systemd/capability、持续 fuzz、恢复、非 WSL Linux、executable 或 commit-bound Evidence |
| 2026-08-31 | B4-f IPC framing fail-closed matrix | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户 Code Review 明确通过；framing fail-closed、四操作 allowlist、fuzz seeds 与 Evidence repair 已接受。用户同时重申 Enforcer 用于限制已攻陷 Agent 的权限，后续协议和实现不得接受任意命令，必须独立严格校验语义操作。B4 总项仍为 `IN_PROGRESS / Implemented`；production Schema/parser/object/Plan validator、跨 UID、systemd/capability、持续 fuzz、恢复、非 WSL Linux、executable 与 commit-bound Evidence 仍未验证 |
| 2026-08-31 | B4-g formal IPC v1 request Schema 与安全 golden vectors | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `schema/ipc-v1.schema.json`、`schema/testdata/ipc-v1/`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 四个 request operation 已冻结为递归 closed union；`ApplyManagedPlan` 仅能表达一个 domain 和一个 typed operation，协议不存在 command/args/binary/env/cwd 或任意 nftables 物理对象名。6 valid + 23 invalid golden、64 KiB/depth 8/token 4096/prefix 1024 exact/one-over、targeted/full race/vet/module 与 Linux 双架构 test compile 均通过；code checkpoint 两个 P2 已修复，final full-scope review 为 `COMPLETE / FRESH / APPROVED / PASSED`，P0-P3 全无，等待用户 Code Review。不代表 production predecoder/DTO/validator/socket/executor、真实 Snapshot/owner/capability/object-role 校验、跨 UID/systemd/root/capability、持续 fuzz 或 commit-bound Evidence |
| 2026-08-31 | B4-g formal IPC v1 request Schema 与安全 golden vectors | `REVIEW / Implemented` | `DONE / Implemented` | `schema/ipc-v1.schema.json`、`schema/testdata/ipc-v1/`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户 Code Review 明确通过；四操作 closed request Schema、任意命令/物理对象名不可表达边界、29 个 security golden、资源 exact/one-over 与独立终审已接受。B4 总项仍为 `IN_PROGRESS / Implemented`；production predecoder/DTO/validator/socket/executor、真实 Snapshot/owner/capability/object-role enforcement、跨 UID/systemd/root capability、持续 fuzz 与 commit-bound Evidence 仍未验证 |
| 2026-08-31 | B4-h production IPC v1 payload predecoder/typed validator | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `internal/ipc/`、`schema/ipc_v1_schema_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | sealed typed DTO、64 KiB/depth/token/duplicate/unknown、mathematical integer、Prefix/policy/timeout/scope 与 29 golden 已接入 production decoder。code checkpoint repair round 1 及 final Evidence repair round 1 后，终审为 `APPROVED_WITH_FOLLOWUPS / COMPLETE / FRESH / PASSED`，P0/P1/P3 均无；Windows Temp cross-build fixture cleanup 是非阻断 P2，等待用户 Code Review。Linux native race/CI 未运行；不代表 framing/socket/executor、真实 Snapshot/capability/object-role、跨 UID/systemd/root 或持续 fuzz |
| 2026-08-31 | B4-h production IPC v1 payload predecoder/typed validator | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/`、`schema/ipc_v1_schema_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户 Code Review 明确通过；production payload predecoder、sealed typed DTO、semantic validator、29 个 golden、资源边界、两轮 repair 与非阻断 Windows Temp cleanup P2 已接受。B4 总项仍为 `IN_PROGRESS / Implemented`；Linux native race、CI、framing/socket/executor、真实 Snapshot/owner/capability/object-role、跨 UID/systemd/root、恢复、持续 fuzz、非 WSL Linux 与 commit-bound Evidence 仍未验证 |
| 2026-08-31 | B4-i production IPC v1 frame reader | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `internal/ipc/frame.go`、`internal/ipc/frame_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | `uint32-be` header、1 MiB frame/64 KiB request pre-allocation caps、稳定截断分类与 one-frame stream consumption 已接入 production `DecodeRequest`。六个 valid golden、exact/one-over、连续帧、错误脱敏、seed fuzz、targeted/full race、Linux 双架构 build 与 WSL count=20 已通过；code checkpoint 与 final full-scope review 最终均 `COMPLETE / FRESH / APPROVED / PASSED`，ADR repair round 1 后 P0-P3 全无，等待用户 Code Review。不代表 socket/`SO_PEERCRED`/executor、真实权限或持续 fuzz |
| 2026-08-31 | B4-i production IPC v1 frame reader | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/frame.go`、`internal/ipc/frame_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户 Code Review 明确通过；production frame reader、pre-allocation caps、稳定错误分类、one-frame consumption、完整验证与 ADR repair round 1 已接受。B4 总项仍为 `IN_PROGRESS / Implemented`；socket/`SO_PEERCRED`/executor、真实 Snapshot/owner/capability/object-role、跨 UID/systemd/root、恢复、持续 fuzz、Linux native race、非 WSL Linux、CI、production executable 与 commit-bound Evidence 仍未验证 |
| 2026-08-31 | B4-j production IPC accepted-connection peer identity gate | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `internal/ipc/peer_linux.go`、`internal/ipc/peer_linux_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | Linux-only `DecodeUnixFrame` 先读取 `SO_PEERCRED`、匹配启动时注入的 expected guard UID，再调用 production `DecodeFrame`；credential failure/UID mismatch 脱敏 fail-closed。真实 WSL socket same-UID、mismatch-before-read 且 stream 保持完整、closed connection、targeted/full count=1/20、双架构 build 与全仓回归通过；code checkpoint repair 0 与 final Evidence repair round 1 最终均 `COMPLETE / FRESH / APPROVED / PASSED`，P0-P3 全无，等待用户 Code Review。不代表 listener/socket lifecycle、真实 cross-UID、executor、root/systemd/capability 或持续 fuzz |
| 2026-09-01 | B4-j production IPC accepted-connection peer identity gate | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/peer_linux.go`、`internal/ipc/peer_linux_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户 Code Review 明确通过；Linux-only accepted-connection `SO_PEERCRED` UID gate、脱敏失败分类、真实 WSL socket 验证、双架构 build、全仓回归与 Evidence repair round 1 已接受。B4 总项仍为 `IN_PROGRESS / Implemented`；listener/socket lifecycle、真实 guard/cross-UID、response/executor、root/systemd/capability、Linux native race、持续 fuzz、非 WSL Linux、CI、production executable 与 commit-bound Evidence 仍未验证，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO` |
| 2026-09-01 | B4-k production Unix listener/socket lifecycle | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `internal/ipc/listener_linux.go`、`internal/ipc/listener_linux_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 固定 `/run/guard/enforcer.sock`、root:guard 0750/0660 read-back、directory-fd flock、active/stale/replace fail-closed、identity-safe cleanup、context-aware AcceptRequest→B4-j gate 已实现。Ubuntu 22.04.5 WSL2 targeted count=1/20 与 full IPC、Linux 双架构 build/vet、全仓回归和 cleanup 通过；checkpoint repair round 1 修复两个 P1 及测试暴露的分类竞态，final docs/Evidence repair round 1 修复 ADR P2 与精确命令 Evidence P1，fresh delta 最终 `COMPLETE / FRESH / APPROVED / PASSED`、P0-P3 全无，等待用户 Code Review。真实 `/run/guard` root:guard、跨 UID、executable response/executor、systemd/root capability、Linux native race、持续 fuzz、非 WSL Linux、CI 与 commit-bound Evidence 仍未验证；B4/G18/M0 不提升 |
| 2026-09-01 | B4-k production Unix listener/socket lifecycle | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/listener_linux.go`、`internal/ipc/listener_linux_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户 Code Review 明确通过；已接受固定 socket path、root:guard 0750/0660 read-back、lifetime directory-fd flock、stale/active/replacement fail-closed、identity-safe cleanup、context-aware AcceptRequest→B4-j gate，以及 checkpoint/docs/Evidence repair round 1 的最终 `COMPLETE / FRESH / APPROVED / PASSED` 证据。真实 `/run/guard` root:guard、跨 UID、executable response/executor、systemd/root capability、Linux native race、持续 fuzz、非 WSL Linux、CI 与 commit-bound Evidence 仍未验证；B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO` |
| 2026-09-01 | B4-l1 mutation-only IPC v1 response Schema 与 golden vectors | `IN_PROGRESS / Implemented` | `REVIEW / Implemented` | `schema/ipc-v1-mutation-response.schema.json`、`schema/testdata/ipc-v1-mutation-response/`、`schema/ipc_v1_mutation_response_schema_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | Apply 三 domain 与 Remove 的 confirmed/rejected/unknown 六分支 closed union、operation-specific rejected error allowlist、unknown_result、12 valid + 28 invalid golden、4 KiB/depth 2/token 32 exact/one-over、targeted count=20 race、全仓 race/vet/module、Linux 双架构 CGo-free compile 均通过。本批仅冻结 mutation response contract；Probe/Snapshot success payload 与 production DTO/codec/writer/client/accept-loop/executor 未实现。B4 总项保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`；等待用户 Code Review |
| 2026-09-01 | B4-l1 mutation-only IPC v1 response Schema 与 golden vectors | `REVIEW / Implemented` | `DONE / Implemented` | `schema/ipc-v1-mutation-response.schema.json`、`schema/testdata/ipc-v1-mutation-response/`、`schema/ipc_v1_mutation_response_schema_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户 Code Review 明确通过；已接受六分支 mutation response closed union、operation-specific rejected allowlist、unknown_result、40 个 golden、精确资源边界，以及 Evidence repair round 1/2 后最终 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无的证据。本批仍不代表 Probe/Snapshot success 或 production response DTO/codec/writer/client/accept-loop/executor；B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO` |
| 2026-09-01 | GORM-0b core + project-owned modernc adapter | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `go.mod`、`go.sum`、`internal/store/gorm_adapter.go`、`internal/store/gorm_adapter_test.go`、`docs/adr/0003-gorm-core-modernc-adapter.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-adapter/result.md` | 仅新增 `gorm.io/gorm v1.31.2` core；GORM 复用唯一 modernc pool，non-closing wrapper 阻止解包/关闭，初始化零 I/O，migration fail-fast，普通参数化 CRUD/OnConflict/三类 RETURNING/显式事务由临时 raw fixture 验证。selected module graph/go.sum 含 GORM upstream test 的官方/mattn driver，但 compiled closure、CGo-free test binary metadata 与 runtime registry 均不含第二 driver。targeted count=20 race、全仓 race/vet/module、Windows CGo-free runtime、Linux 双架构 CGo-free test compile、checkpoint 与 final FULL_SCOPE 终审均通过；最终 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无，等待用户 Code Review。未迁移生产 SQL，未改变 Schema/公共 Store API/PRAGMA/关键事务语义；真实 Linux runtime、CI、漏洞扫描、SBOM、性能和 commit-bound Evidence 未完成，M0 保持 `NO-GO`。 |
| 2026-09-01 | GORM-0b core + project-owned modernc adapter | `REVIEW / Implemented` | `DONE / Implemented` | `go.mod`、`go.sum`、`internal/store/gorm_adapter.go`、`internal/store/gorm_adapter_test.go`、`docs/adr/0003-gorm-core-modernc-adapter.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-adapter/result.md` | 用户 Code Review 明确通过；已接受 GORM core、project-owned modernc Dialector、non-closing pool ownership、disabled Migrator、临时 fixture CRUD/OnConflict/三类 RETURNING/事务验证，以及最终 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无的证据。GORM-1 生产 SQL 迁移未授权；Schema/公共 Store API/PRAGMA/关键 raw SQL 与未验证域不变，D1/D2/D4、G18.1-G18.3 不提升，M0 保持 `NO-GO`。 |
| 2026-09-01 | GORM-1a `PutParserOutcome` 单 INSERT | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/store/gorm_adapter.go`、`internal/store/uow.go`、`internal/store/decision_lifecycle.go`、`internal/store/gorm_uow_test.go`、`docs/adr/0004-gorm-put-parser-outcome-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-put-parser-outcome/result.md` | 仅将 `PutParserOutcome` 的显式七列 INSERT 改为 GORM `Create`；不可 finalise/unwrap 的 transaction ConnPool 同时绑定 cloned Config/Statement 到既有 raw `*sql.Tx`，UnitOfWork 仍唯一 Commit/Rollback。exact SQL/Create callback、三类 outcome raw readback、NULL/零值、commit/rollback、deferred FK、duplicate/cancel sticky error、focused/full race、vet/module、CGo-free 三目标编译及 Ubuntu WSL2 SIGKILL/replay 初跑/count=20 均通过；Tier-3 checkpoint 与 final FULL_SCOPE 终审均为 `APPROVED / COMPLETE / FRESH / PASSED`，P0-P3 全无。其余 UnitOfWork/关键 SQL、Schema/API/依赖/PRAGMA 不变；用户验收待完成，M0 保持 `NO-GO`。 |

## 7. 下一步队列

1. 当前 stale completion、receipt replay 与 Evidence 修复已获用户通过，状态为 `DONE / Implemented`；
   C2 总项保持 `IN_PROGRESS / Implemented`，worktree Evidence 不冒充 commit-bound、CI 或 Linux runtime Evidence。
2. C1 signal-aware lifecycle owner 已获用户 Code Review 通过，当前批为 `DONE / Implemented`；
   C1 总项仍为 `IN_PROGRESS / Implemented`。该批不代表真实 Source reader/management intake、signal executable、
   真实 30 秒墙钟 SIGTERM、进程 restart 或 Linux durability。
3. D3-a single-document YAML loader 已获用户 Code Review 通过，当前为 `DONE / Implemented`。
4. D3-b SMTP credential readiness gate 已获用户 Code Review 通过，当前为 `DONE / Implemented`；
   D3 总项仍为 `IN_PROGRESS / Implemented`，不代表真实 SMTP/TLS/队列或目标 Linux file-to-Ready。
5. D3-c atomic logging reload/restart owner primitive 已获用户 Code Review 通过，当前为
   `DONE / Implemented`；D3 总项仍为 `IN_PROGRESS / Implemented`，不代表 production
   watcher/executable wiring 或目标 Linux native reload/restart。
6. D3-d YAML resource cap 已获用户 Code Review 通过，当前为 `DONE / Implemented`；D3 总项仍为
   `IN_PROGRESS / Implemented`，不代表 parser benchmark、CI、commit-bound Evidence 或目标 Linux runtime。
   D3-e Linux native credential file-to-Ready 已在 Ubuntu 22.04.5 WSL2 以 non-root `guard`
   通过 9 项 production-reader integration；临时 user/group、fixture 与 binary 均已清理。targeted
   package race、Linux integration vet、full race rerun/vet/module 与双架构 build 已通过。初次独立
   full-scope review 对代码/测试未发现 P0/P1/P2/P3，但要求补全临时 fixture 的可复现 Evidence；
   Evidence-only repair round 1 的 fresh delta review 已将缺口标为 resolved，最终 delivery gate
   `PASSED`；用户 Code Review 已明确通过，当前批为 `DONE / Implemented`。D3 总项仍为
   `IN_PROGRESS / Implemented`。B3/B4 与 production
   packaging/systemd/parent trust 继续保持未验证；G18.1–G18.3 `FAIL`、M0 `NO-GO`。
7. B1-f Linux native SQLite SIGKILL→reopen 已完成 test-only 实现与验证：Ubuntu WSL2
   committed/uncommitted 两项在初跑及 `count=20` 均通过，targeted/full race、vet/module、
   双架构 build、格式与 cleanup 通过；独立 final review repair round 1 后最终 gate `PASSED`，
   用户 Code Review 已明确通过，当前为 `DONE / Implemented`。该批不代表 service/OS reboot、
   filesystem barrier、power-loss durability、production executable 或 commit-bound Evidence。
8. B2-f Linux native Source generation restart/replay 已完成 test-only 实现和独立终审：
   两个 fresh helper 使用 production Store/Source adapter，Ubuntu WSL2 初跑与 `count=20`、全套
   race/vet/module/双架构 build、格式和 diff-check 通过；final review 为
   `APPROVED_WITH_FOLLOWUPS`，无 P0/P1/P3，唯一 P2 是 host policy 阻止删除的 Windows Temp
   test binary。用户 Code Review 已明确通过，当前为 `DONE / Implemented`。
   真实 File reader、copytruncate、Journald、crash、replay/reprocess refs、OS/power loss 与
   production executable 保持未验证；B2 总项不提升为 Verified。
9. B2-g Source generation transition SIGKILL/recovery 已完成 test-only 实现、Ubuntu WSL2
   初跑/count=20 与全套验证。两个 committed-boundary 窗口均由父进程精确 SIGKILL 并 fresh
   reopen；crash 前 checkpoint 不越过旧 generation，重启 session 从 sequence 1 开始。
   Tier-3 checkpoint repair round 1 与 final Evidence repair round 2 已清零 P0-P3，当前为
   用户 Code Review 已明确通过，当前为 `DONE / Implemented`。
   transaction-internal crash、真实 reader/copytruncate、Coordinator replay/Parser 重评、Journald、
   refs、OS/power loss 与 executable 仍未验证；B2 总项保持 `IN_PROGRESS / Implemented`。
10. B2-h Linux native Journald cursor restart/replay 已完成 test-only 实现。Ubuntu WSL2
    初跑与 `count=20` 均使用真实 opaque cursor 且无 SKIP；production identity/receipt/checkpoint
    primitives 的 fresh-process reopen、全套 race/vet/module/双架构 integration-test build 与 cleanup
    已通过。code checkpoint repair round 1 已清零 P0-P3；final full-scope review 的 cross-build
    recipe P1 与 command-context P2 经 fixed-fixture Evidence repair round 1 后，fresh delta review
    为 `COMPLETE / FRESH / APPROVED` 且 P0-P3 全无。用户 Code Review 已明确通过，当前为
    `DONE / Implemented`。
    production Journald reader/Coordinator replay、cursor invalidation/vacuum/resume、完整 at-least-once、
    Parser 重评、Audit/Metric/Health、service/OS/power loss 与 commit-bound Evidence 仍未验证；
    B2 总项保持 `IN_PROGRESS / Implemented`。
11. B2-i Linux native processing transaction-internal SIGKILL/replay 已完成 test-only 实现。
    fresh writer 在八类 production UnitOfWork writes staged、Commit 前由父进程精确 SIGKILL；
    fresh reopen 断言八表/receipt/checkpoint 全空，直接 UoW replay 后八表各一、receipt 精确且
    checkpoint sequence 1。Ubuntu WSL2 初跑/count=20 无 SKIP，全套 race/vet/module/双架构
    integration-test build 与 cleanup 已通过；Tier-3 code checkpoint 与 final full-scope review 均为
    `COMPLETE / FRESH / PASSED`，P0-P3 全无；用户 Code Review 已明确通过，当前为
    `DONE / Implemented`。
    Coordinator/current Parser+Rule 重评、真实 reader、commit-unknown、Source-state
    internal crash、production checkpoint manager、OS/power loss 与 commit-bound Evidence 仍未验证；
    B2/C1 总项均保持 `IN_PROGRESS / Implemented`。
12. B4-f IPC framing fail-closed matrix 已完成现有 Linux Spike 的内部 decoder 重构与 test-only
    增量。12 项 table case 覆盖截断 prefix/payload、非法 JSON、未知字段/版本/operation、多 JSON
    value、四个允许 operation 与超限分配前拒绝；4 个 fuzz seed、Ubuntu WSL2 初跑/count=20、
    原 socket/`SO_PEERCRED` smoke、Linux vet、双架构 test/main compile、全仓回归与 cleanup 已通过。
    独立 Tier-3 code checkpoint 为 `COMPLETE / FRESH / PASSED`，P0-P3 全无；首次 final
    full-scope review 的 exact PowerShell→WSL recipe P1 已用固定新 fixture 完整重跑并修复，
    fresh Evidence-only delta review 将其标为 resolved；最终 `COMPLETE / FRESH / APPROVED / PASSED`，
    用户 Code Review 已明确通过，当前为 `DONE / Implemented`。Windows 全仓 race 因 `linux` build
    tag 不覆盖本 Spike，不记为 targeted race。
    production IPC Schema/parser/object/Plan validator、跨 UID、systemd/capability、持续 fuzz、
    timeout/cancel/restart、非 WSL Linux 与 commit-bound Evidence 仍未验证；B4 总项保持
    `IN_PROGRESS / Implemented`，G18.1–G18.3 保持 `FAIL`，M0 保持 `NO-GO`。
13. B4-g formal IPC v1 request Schema 与安全 golden vectors 已完成：四个 operation 均为递归
    closed typed union；`ApplyManagedPlan` 一次仅允许一个 domain 和一个 typed operation，Agent 无法在
    request 中携带 shell command、binary/env/cwd 或任意 nftables 物理对象名。6 个 valid、23 个
    invalid fixture，以及 request bytes、depth、token、prefix exact/one-over 与 fuzz seed invariants
    已通过 targeted/full race、vet、module 和 Linux amd64/arm64 test compile。code checkpoint 的两个
    P2 已修复；final full-scope review 为 `COMPLETE / FRESH / APPROVED / PASSED`，P0-P3 全无，
    用户 Code Review 已明确通过，当前为 `DONE / Implemented`。
    production predecoder/DTO/validator/socket/executor、真实 Snapshot digest/owner/capability/object-role
    enforcement、跨 UID/systemd/root capability、持续 fuzz 与 commit-bound Evidence 仍未验证；B4 总项、
    G18.1–G18.3 与 M0 结论不变。
14. B4-h production IPC v1 payload predecoder/typed validator 已实现：冻结的 29 个 golden 与资源
    exact/one-over 直接运行于 stdlib-only production decoder；sealed read-only typed DTO、内部错误分类、
    mathematical integer、Prefix/policy/timeout/scope 校验已落盘。code checkpoint repair round 1 与
    final Evidence repair round 1 后，独立终审为 `APPROVED_WITH_FOLLOWUPS / COMPLETE / FRESH /
    PASSED`，P0/P1/P3 均无；用户 Code Review 已明确通过，当前为 `DONE / Implemented`。Windows full race、Linux 双架构 CGo-free build 与
    Ubuntu WSL2 初跑/count=20 已通过；Linux native race 与 CI 未运行，Windows Temp cross-build fixture
    cleanup 为非阻断 P2。framing/socket/executor、真实 Snapshot/
    capability/object-role、跨 UID/systemd/root、持续 fuzz 与 commit-bound Evidence 仍未验证；B4 总项、
    G18.1–G18.3 与 M0 结论不变。
15. B4-i production frame reader 已实现：`uint32-be` header、1 MiB frame cap 与 64 KiB request cap
    均在 payload allocation/read 前执行；截断/超限稳定分类且不回显输入，成功仅消费一帧，错误后
    caller 必须丢弃 stream。六个 valid golden、64 KiB/1 MiB exact/one-over、连续帧、seed fuzz、
    targeted/full race、Linux amd64/arm64 CGo-free build 与 Ubuntu WSL2 count=20 已通过，临时产物
    cleanup/absent readback 通过；code checkpoint 与 final full-scope review 最终均 `COMPLETE / FRESH /
    APPROVED / PASSED`，ADR repair round 1 后 P0-P3 全无；用户 Code Review 已明确通过，当前为
    `DONE / Implemented`。socket/`SO_PEERCRED`/executor、真实 Snapshot/capability/
    object-role、跨 UID/systemd/root、持续 fuzz、非 WSL Linux 与 commit-bound Evidence 仍未验证；
    B4 总项、G18.1–G18.3 与 M0 结论不变。
16. B4-j production accepted-connection peer identity gate 已实现：Linux-only `DecodeUnixFrame` 使用
    `SO_PEERCRED` 获取实际 peer UID，匹配启动时注入的 expected guard UID 后才进入 B4-i；失败分类
    不回显 UID、socket 或 OS error，caller 保持 connection ownership。WSL 真实 Unix socket same-UID、
    mismatch-before-read/stream-preservation、closed connection、targeted/full count=1/20，以及 Windows
    回归、Linux 双架构 CGo-free vet/test/build 与 cleanup readback 均通过。code checkpoint 为
    `COMPLETE / FRESH / APPROVED / PASSED`、repair round 0、P0-P3 全无。final full-scope review 的精确
    Evidence 命令 P1 经 repair round 1 全新 fixture 重跑后 resolved，最终 `COMPLETE / FRESH / APPROVED /
    PASSED`、P0-P3 全无；用户 Code Review 已明确通过，当前为 `DONE / Implemented`。
    production listener/socket lifecycle、真实 guard/cross-UID、response/executor、root/systemd/capability、
    Linux native race、持续 fuzz、非 WSL Linux、CI 与 commit-bound Evidence 仍未验证；B4 总项、
    G18.1–G18.3 与 M0 结论不变。
17. B4-k production Unix listener/socket lifecycle 已实现：固定 path 与 0750/0660 owner/mode read-back、
    directory-fd `flock`、active/stale/replacement fail-closed、identity-safe Close cleanup，以及 context-aware
    `AcceptRequest → DecodeUnixFrame` 均通过真实 WSL socket。独立 checkpoint repair round 1 修复目录失败
    残留与并发 stale takeover 两个 P1，current-hash targeted count=1/20、full IPC、双架构 build/vet、全仓
    回归与 cleanup 均通过，最终 `COMPLETE / FRESH / APPROVED / PASSED`、P0-P3 全无；用户 Code Review
    已明确通过，当前为 `DONE / Implemented`。真实 `/run/guard` root:guard、跨 UID、response/executor、
    systemd/root capability、Linux native race、持续 fuzz、非 WSL Linux、CI 与 commit-bound Evidence 未验证；
    B4 总项、G18.1–G18.3 与 M0 结论不变。
18. B4-l1 mutation-only response contract 已实现：Apply 三 domain 与 Remove 的六个 typed result 分支、
    stable rejected/unknown error codes、closed object 与 4 KiB/depth 2/token 32 资源边界由 12 valid、
    28 invalid golden 及精确边界测试冻结。targeted count=20 race、全仓 race/vet/module、Linux
    amd64/arm64 CGo-free compile 与 diff-check 已通过，当前为 `REVIEW / Implemented`，等待用户 Code
    Review；用户已明确通过，当前为 `DONE / Implemented`。Probe/Snapshot success payload、production
    response DTO/codec/writer/client、accept-loop/executor 与真实 runtime 未实现；B4 总项、G18.1–G18.3
    与 M0 结论不变。
19. GORM-0b 已按用户确认边界实现并获用户 Code Review 通过，当前 `DONE / Implemented`：GORM core 通过项目自有
    modernc Dialector 与 non-closing ConnPool 复用现有 Store pool，AutoMigrate/schema API 被
    fail-fast 禁用。compiled closure、binary metadata 与 driver registry 均无
    官方/mattn SQLite driver；selected graph/go.sum 中的 upstream test 依赖已作为供应链暴露单独记录。
    GORM-1a 现按独立 ADR 只窄化 `PutParserOutcome` 一条 INSERT；后续任何迁移仍须另行选择小批次并确认。
    migration、PRAGMA、CAS/fence、commit-unknown、snapshot 和其余 UnitOfWork 保持 raw SQL。final FULL_SCOPE 终审为
    `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无；用户门已 `PASSED`，
    不提升 D1/D2/D4、G18.1–G18.3 或 M0 结论。
20. GORM-1a 已按用户确认边界实现，当前 `REVIEW / Implemented`：仅 `PutParserOutcome` 使用显式七列
    GORM `Create`，同一 raw `*sql.Tx` 仍由 UnitOfWork 独占 Commit/Rollback；transaction wrapper 不暴露
    Begin/Commit/Rollback/Close/unwrap 能力。高风险 race、全仓回归、CGo-free 三目标编译与 Ubuntu WSL2
    SIGKILL/replay 初跑/count=20 已通过，Tier-3 checkpoint 与 final FULL_SCOPE 终审均为
    `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无。其余 SQL、Schema/API/依赖/PRAGMA 不变；等待用户 Code Review，
    G18.1–G18.3 保持 `FAIL`，M0 保持 `NO-GO`。
