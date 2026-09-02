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
| 最近更新 | `2026-09-02` |

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
modernc Dialector/non-closing ConnPool；GORM-1a 至 GORM-1e 已迁移五个普通 INSERT。用户随后
明确要求一次性完成当前 GORM-1 剩余代码，final batch 已将 `PutDetectionContribution`、
`PutProjection`、`PutReceipt` 整体迁移到绑定既有 raw `*sql.Tx` 的不可最终化私有 session。
至此 UnitOfWork 八个 processing writer 全部使用 GORM，`uow.go` 无 direct raw SQL；checksummed
migration、PRAGMA、snapshot、commit-unknown、checkpoint/source/Decision/reconcile lifecycle、
Schema、公共 API、Store pool 与事务最终化所有权保持不变。
另按用户确认的 B4-l2 至 B4-l11 边界，mutation request/response、`ProbeCapabilities` 与
`SnapshotManaged` 三条闭集 typed IPC 链路均已实现；raw frame helper 保持 private。B4-l10 新增 immutable
managed snapshot domain、success/failure Schema 与 53 个 fixture、1 MiB/depth 4/token 32768/target 1024
边界、Linux fixed-socket/root-peer/context-only/single-connection/zero-retry transport。targeted/full
normal/Race/Vet/module、三项 5s fuzz、Windows/Linux amd64/Linux arm64 CGo-free compile、WSL2 真实 Unix
socket `count=20` 与 Docker Linux targeted Race `count=20` 已通过。真实 Firewall snapshot provider、
Probe-first 编排、通用 Enforcer executor/serve loop、production root:guard 跨 UID 与 systemd 仍缺失。
B4-l10 用户 Code Review 已明确通过，当前为 `DONE / Implemented`；B4-l9 用户 Code Review 也已明确通过，
当前为 `DONE / Implemented`。B4-l11 已补齐 authenticated mutation single-request server adapter，并通过专项/
全仓/WSL2/Docker/交叉编译与独立 Tier 3 FULL_SCOPE 终审；用户 Code Review 已明确通过，当前为
`DONE / Implemented`。B4-l10 与 B4-l11 代码已分别本地提交为 `9e2925a` 与 `0edeaad`，未推送。
B4-l12 已补齐 production-neutral closed mutation plan/result authority，以及由当前 capabilities + managed
snapshot 驱动的纯二次授权和 IPC 双向 mapper；专项/全仓/交叉编译与独立 Tier 3 修复复核已通过，当前为
`DONE / Implemented`，用户 Code Review 门为 `PASSED`。B4-l13 已补齐同 attempt fresh acquisition 与
context-aware 串行单请求 executor primitive，用户 Code Review 门为 `PASSED`，当前为 `DONE / Implemented`。
B4-l14 已实现 Linux-only closed `EnforcerHandlers` 与 unified authenticated single-connection
`ServeEnforcerOnce`；专项 WSL2/Docker Race、全仓 normal/Race/Vet/module、Docker full IPC Race、三目标
CGo-free test-compile 与三路独立 Tier 3 checkpoint 均通过；最终集成初审的 Evidence replay identity P2
已在 repair round 1 闭合；用户 Code Review 已明确通过，当前为 `DONE / Implemented`、用户门 `PASSED`。真实
Backend/provider 与真实 Firewall 仍未实施。B4-l15 已实现 injected handlers 上的 Linux-only serial persistent
`ServeEnforcer`：post-Accept per-connection timeout、closed continue/fatal policy、close-before-observer、parent
cancellation 与统一 listener serve ownership 已由 WSL2/Docker Race/全仓/跨目标验证覆盖；首轮 safety P1 在
repair round 2 闭合，handler-panic cleanup P1 又在 inner-defer repair round 3 闭合；三路 fresh 独立终审与
records P2 fresh-delta 均 PASSED。用户 Code Review 已明确通过，当前为 `DONE / Implemented`、用户门
`PASSED`。真实 handler
composition、executable/systemd 与生产 `/run/guard` 仍未实施或验证。
B4-l16 已按用户确认实现 Linux-only、production-neutral `NewEnforcerHandlers(MutationBackend)`：三个 closed
handlers 捕获同一私有 backend/executor/context-aware gate，复制后仍跨 Probe/Snapshot/Mutation 串行；
Probe/Snapshot 各自读取 fresh backend 事实，Mutation 委托既有 executor 完成同 attempt
`Probe → Snapshot → Authorize → mutation`。closed error mapping、非法 observation 的 `not_ready`、排队取消
零 backend 调用、真实 residue Remove 委托与 panic 后双 gate 复用均已由 deterministic Linux tests 冻结。
WSL2 `count=20`、Docker targeted Race `count=20` 与 Enforcer+IPC full Race、Windows 全仓 normal/Race/Vet/
module、Linux amd64/arm64 CGo-free build/test-compile 与 Linux Vet 均通过；repair round 1 关闭两项 P1、三项
P2 后，三路独立 Tier 3 fresh-delta 均 `COMPLETE / FRESH / PASSED`、P0-P3 全无。当前
`DONE / Implemented`，用户 Code Review 门为 `PASSED`。真实 Backend/provider、Firewall、systemd/executable、生产
`/run/guard`、配置、依赖、数据库与上级 Gate 均不在本批。
B4-l17 已按用户确认新增 Linux-only `EnforcerRuntime`：构造期仅创建一套 closed handlers 并接管 injected
listener；`Run` 委托既有 `ServeEnforcer`，terminal return/panic 后关闭 listener，合并 serve/Close 错误身份。
atomic state 拒绝重复/并发 Run；仅 typed `already_serving` 外部占用不关闭且允许重试，也不重复构造 handlers。
Windows targeted/full、Linux amd64/arm64 CGo-free compile/Vet、WSL2 `count=20`、Docker targeted/changed-package
Race 与两路独立终审均通过；初审两项 P1 与一项 P2 已修复，当前 `DONE / Implemented`，用户 Code Review 门为
`PASSED`。
真实 Backend/provider、`/run/guard`、UID/GID、systemd/executable、配置、依赖、数据库、部署与上级 Gate 均不在本批。

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
| B4 | Agent/Enforcer 权限与 IPC Spike | `IN_PROGRESS` | `Implemented` | `Codex/current task` | `artifacts/evidence/M0/worktree/m0-b/ipc-result.json`、`artifacts/evidence/M0/worktree/m0-b/ipc-response-codec/result.md`、`artifacts/evidence/M0/worktree/m0-b/ipc-response-frame/result.md`、`artifacts/evidence/M0/worktree/m0-b/ipc-request-codec/result.md`、`artifacts/evidence/M0/worktree/m0-b/ipc-request-frame/result.md`、`artifacts/evidence/M0/worktree/m0-b/ipc-mutation-client/result.md`、`artifacts/evidence/M0/worktree/m0-b/ipc-mutation-server/result.md`、`artifacts/evidence/M0/worktree/m0-b/ipc-snapshot-managed-transport/result.md`、`artifacts/evidence/M0/worktree/m0-b/ipc-enforcer-loop/result.md`、`artifacts/evidence/M0/worktree/m0-b/ipc-enforcer-handlers/result.md`、`artifacts/evidence/M0/worktree/m0-b/ipc-enforcer-cross-uid-runtime/result.md` | WSL2 request framing fail-closed、四操作 allowlist、mutation/Probe/Snapshot typed codec/frame、Linux fixed-socket root-peer client、authenticated single-request server、serial persistent loop、production-neutral closed handler composition 的 Docker Linux Race，以及受控 WSL fixture 的 `/run/guard` root:guard/跨 UID runtime 集成均已通过；仍缺真实 Firewall provider/owner/object-role、production executable/systemd hardening、持续 fuzz、恢复与非 WSL target Linux 证据 |
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
| 2026-09-01 | GORM-1a `PutParserOutcome` 单 INSERT | `REVIEW / Implemented` | `DONE / Implemented` | `internal/store/gorm_adapter.go`、`internal/store/uow.go`、`internal/store/decision_lifecycle.go`、`internal/store/gorm_uow_test.go`、`docs/adr/0004-gorm-put-parser-outcome-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-put-parser-outcome/result.md` | 用户 Code Review 明确通过；已接受单条七列 GORM Create、existing raw transaction private session、NULL/零值、deferred FK、sticky error、CGo-free 三目标编译、Ubuntu WSL2 SIGKILL/replay 及最终 `APPROVED / COMPLETE / FRESH / PASSED` 证据。其余 SQL、Schema/API/依赖/PRAGMA 不变；D1/D2/D4、G18.1-G18.3 与 M0 结论不提升。 |
| 2026-09-01 | GORM-1b `PutDetectionOutcome` 单 INSERT | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/store/uow.go`、`internal/store/gorm_detection_outcome_test.go`、`docs/adr/0005-gorm-put-detection-outcome-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-put-detection-outcome/result.md` | 仅将 `PutDetectionOutcome` 的显式七列 INSERT 改为 existing raw transaction private session 上的 GORM `Create`；success 保持 SQL NULL，record_permanent 保持非空 failure_code，复合主键和 deferred receipt FK 不变。exact SQL/Vars/Create callback、raw readback、commit/rollback、deferred FK、duplicate/cancel sticky error、focused race count=20、全仓 race/vet/module、依赖闭包、三目标 CGo-free 编译及 Ubuntu WSL2 SIGKILL/replay 初跑/count=20 均通过。Tier-3 checkpoint 与 final FULL_SCOPE review 通过；Evidence repair round 1 解决唯一可复现性 P1，fresh delta 后最终 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无，等待用户 Code Review。PutDetectionContribution 与其他关键 SQL、Schema/API/依赖/PRAGMA 不变；M0 保持 `NO-GO`。 |
| 2026-09-01 | GORM-1b `PutDetectionOutcome` 单 INSERT | `REVIEW / Implemented` | `DONE / Implemented` | `internal/store/uow.go`、`internal/store/gorm_detection_outcome_test.go`、`docs/adr/0005-gorm-put-detection-outcome-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-put-detection-outcome/result.md` | 用户 Code Review 明确通过；已接受 existing raw transaction private session 上的七列 GORM Create、success/permanent SQL NULL/非空值、复合主键、deferred receipt FK、sticky first-error、完整验证，以及 Evidence repair round 1 后最终 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无的证据。PutDetectionContribution 与其他关键 SQL、Schema/API/依赖/PRAGMA 不变；D1/D2/D4、G18.1-G18.3 与 M0 结论不提升。 |
| 2026-09-01 | GORM-1c `PutAlert` 单 INSERT | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/store/uow.go`、`internal/store/gorm_alert_test.go`、`docs/adr/0006-gorm-put-alert-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-put-alert/result.md` | 仅将 `PutAlert` 的显式八列 INSERT 改为 existing raw transaction private session 上的 GORM `Create`；private `alertRow` 八个映射字段均有简体中文用途注释。exact SQL/Vars、raw readback、同事务 commit/rollback、三类 immediate FK、正交 PK/UNIQUE、sticky/cancel、focused race count=20、全仓 race/vet/module、依赖闭包、三目标 CGo-free 编译及 Ubuntu WSL2 SIGKILL/replay count=1/20 均通过。独立 checkpoint 通过；final FULL_SCOPE 的 Evidence/STATUS repair round 1 后最终 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无。PutDetectionContribution 与其他关键 SQL、Schema/API/依赖/PRAGMA 不变；等待用户 Code Review，M0 保持 `NO-GO`。 |
| 2026-09-01 | GORM-1c `PutAlert` 单 INSERT | `REVIEW / Implemented` | `DONE / Implemented` | `internal/store/uow.go`、`internal/store/gorm_alert_test.go`、`docs/adr/0006-gorm-put-alert-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-put-alert/result.md` | 用户 Code Review 明确通过；已接受显式八列 GORM Create、字段级简体中文注释、三类 immediate FK、正交 primary key/detection membership unique、sticky first-error、事务原子性、完整验证及 final FULL_SCOPE repair round 1 后最终 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无的证据。`PutDetectionContribution` 与其他关键 SQL、Schema/API/依赖/PRAGMA 不变；D1/D2/D4、G18.1-G18.3 与 M0 结论不提升。 |
| 2026-09-01 | GORM-1d `AppendCriticalAudit` 单 INSERT | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/store/uow.go`、`internal/store/gorm_critical_audit_test.go`、`docs/adr/0007-gorm-append-critical-audit-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-critical-audit/result.md` | 仅将 `AppendCriticalAudit` 的显式 15 列 INSERT 改为 existing raw transaction private session 上的 GORM `Create`；private `criticalAuditRow` 15 个映射字段均有简体中文用途注释。exact SQL/Vars、四个 SQL NULL、`critical=1`、JSON 原文/空值归一化、UTC microseconds、同事务 commit/rollback、三类 immediate FK、正交 PK/UNIQUE、sticky/cancel、focused race count=20、全仓 race/vet/module、依赖闭包、三目标 CGo-free 编译及 Ubuntu WSL2 SIGKILL/replay count=1/20 均通过。Tier-3 implementation checkpoint 与 final FULL_SCOPE 终审均为 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无。`PutDetectionContribution`、`PutDecision`、`PutProjection`、`PutReceipt` 与其他关键 SQL、Schema/API/依赖/PRAGMA 不变；等待用户 Code Review，M0 保持 `NO-GO`。 |
| 2026-09-01 | GORM-1d `AppendCriticalAudit` 单 INSERT | `REVIEW / Implemented` | `DONE / Implemented` | `internal/store/uow.go`、`internal/store/gorm_critical_audit_test.go`、`docs/adr/0007-gorm-append-critical-audit-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-critical-audit/result.md` | 用户 Code Review 明确通过；已接受窄化的显式 15 列 GORM `Create`、字段级简体中文注释、四个 SQL NULL、`critical=1`、JSON 原文/空值归一化、UTC microseconds、三类 immediate FK、正交 primary key/idempotency unique、sticky first-error、事务原子性、context 取消及现有 review/verification Evidence。`PutDetectionContribution`、`PutDecision`、`PutProjection`、`PutReceipt` 与其他关键 SQL、Schema/API/依赖/PRAGMA 不变；不提升 G18.1-G18.3 或 M0 Gate。 |
| 2026-09-01 | GORM-1e `UnitOfWork.PutDecision` 单 INSERT | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/store/uow.go`、`internal/store/gorm_decision_test.go`、`docs/adr/0008-gorm-put-decision-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-put-decision/result.md` | 用户明确确认只迁移 `UnitOfWork.PutDecision` 的显式 15 列 INSERT；private `decisionRow` 15 个映射字段均有紧邻简体中文用途注释，并使用 existing raw transaction private session 上的显式 `Select/Create`。exact SQL/Vars、automatic/manual、active/expired/revoked、全部 SQL NULL/非空值、UTC microseconds、suppressed count 有符号上限、node/rule-version/alert immediate FK、主键、两条 active partial-unique、terminal predicate、sticky/cancel 与同事务 commit/rollback 均由专项测试冻结。focused Store/Processor Race count=20、全仓 Race/Vet/module、247 包依赖闭包、58 字段注释、三目标 CGo-free 编译及 Ubuntu WSL2 current-hash SIGKILL/replay count=1/20 均通过。Tier-3 checkpoint repair round 1 补齐 Create 前非法引用/生命周期失败路径，FINAL FULL_SCOPE repair round 2 修正 Evidence 时态，closure repair round 3 移除已完成 review 的 stale 未验证项；fresh delta 后最终 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。等待用户 Code Review；其他 Decision SQL、`PutDetectionContribution`、`PutProjection`、`PutReceipt`、Schema/API/依赖/PRAGMA/pool/事务最终化所有权不变，M0 保持 `NO-GO`。 |
| 2026-09-01 | GORM-1e `UnitOfWork.PutDecision` 单 INSERT | `REVIEW / Implemented` | `DONE / Implemented` | `internal/store/uow.go`、`internal/store/gorm_decision_test.go`、`docs/adr/0008-gorm-put-decision-uow-exception.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-put-decision/result.md` | 用户 Code Review 明确通过；已接受显式 15 列 GORM `Create`、字段级简体中文注释、automatic/manual 与三种生命周期、六个 SQL NULL/非空值、UTC microseconds、suppressed count 有符号上限、三个 immediate FK、主键、两条 active partial-unique、terminal predicate、sticky/cancel、同事务原子性、完整验证以及累计 repair round 3 后最终 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无的证据。其他 Decision SQL、`PutDetectionContribution`、`PutProjection`、`PutReceipt`、Schema/API/依赖/PRAGMA/pool/事务最终化所有权不变；不提升 G18.1-G18.3 或 M0 Gate。 |
| 2026-09-01 | GORM-1f `UnitOfWork.PutReceipt` 单 INSERT 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | `internal/store/uow.go`、`internal/core/model.go`、`migrations/0001_m0.sql`、现有 processing/receipt/SIGKILL-replay 测试 | 下一最小候选仅为 `PutReceipt` 的显式 16 列 INSERT：无 ON CONFLICT、RowsAffected、RETURNING、read-back、CAS 或 revision fence；拟使用 existing raw transaction private session 上的显式 `Select/Create` 与 private `processingReceiptRow`，16 个映射字段均需紧邻简体中文用途注释。实现前必须冻结 DeliveryID 与 source/position 绑定、file/journald 和 success/record_permanent 两组 closed union、11 个 SQL NULL/非空值、file uint64→SQLite int64 边界、UTC microseconds、delivery 主键、source 与 source-generation immediate FK、parser/detection outcome deferred FK 在 receipt 写入后提交、sticky/cancel、同事务 commit/rollback 与 crash/replay。`PutDetectionContribution`、`PutProjection`、其他 receipt/read-back/checkpoint/commit-unknown SQL、Schema/migration/API/依赖/PRAGMA/pool/事务最终化全部排除；生产代码、测试、ADR 与 Evidence 尚未修改，等待数据库行为变更 Ask First 明确确认。G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |
| 2026-09-01 | GORM-1 final batch：剩余 UnitOfWork writers 整体迁移 | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/store/uow.go`、`internal/store/gorm_conflict_writes_test.go`、`internal/store/gorm_receipt_test.go`、`docs/adr/0009-gorm-complete-unit-of-work-writes.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-complete-uow-writes/result.md` | 用户明确要求一次性完成、不再逐项等待；`PutDetectionContribution`、`PutProjection`、`PutReceipt` 已整体迁移到 existing raw transaction private GORM session。新增 5/7/16 列 private row model；Contribution 定向 DO NOTHING、底层 RowsAffected error、stable DeliveryID read-back 与 `sql.ErrNoRows` 保持；Projection 单语句 revision-fenced upsert、底层 RowsAffected error、NULL-safe idempotent read-back 保持；Receipt 两组 closed union、11 nullable、整数/时间、PK/immediate/deferred FK、retired trigger 与 crash/replay 保持。fresh focused Race count=20 分别 PASS（172.617s/162.564s），最终全仓 normal/Race、Vet/module、247 依赖闭包、86 字段注释、三目标 CGo-free build、Linux integration vet 与 WSL current-hash replay count=1/20 均 PASS。独立 Tier-3 implementation checkpoint 与 final FULL_SCOPE/INTEGRATION 均为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、repair round 0、P0-P3 全无；等待用户 Code Review。migration/PRAGMA/snapshot/commit-unknown/其他 lifecycle SQL、Schema/API/依赖/pool/事务最终化不变；G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |
| 2026-09-01 | GORM-1 final batch Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `internal/store/uow.go`、`internal/store/gorm_conflict_writes_test.go`、`internal/store/gorm_receipt_test.go`、`docs/adr/0009-gorm-complete-unit-of-work-writes.md`、`artifacts/evidence/M0/worktree/m0-d/gorm-complete-uow-writes/result.md` | 用户明确回复 `GORM-1 final batch Code Review 通过`，用户门更新为 `PASSED`；接受三 writer 整体迁移、完整验证、独立 implementation checkpoint 与 final FULL_SCOPE/INTEGRATION 结果。仅关闭本 Delivery Unit，不描述为 Verified；migration/PRAGMA/snapshot/commit-unknown/其他 lifecycle SQL、Schema/API/依赖/pool/事务最终化不变，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。未提交、未推送。 |
| 2026-09-01 | B4-l2 mutation response typed DTO + payload codec 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | `internal/ipc/request.go`、拟新增 `internal/ipc/response.go`、`internal/ipc/response_test.go`、`schema/ipc-v1-mutation-response.schema.json`、`schema/testdata/ipc-v1-mutation-response/`、`docs/adr/0001-phase1-process-privilege-boundary.md` | GORM-1 已收口，最短未完成依赖回到 B4。建议仅把 B4-l1 已冻结的 Apply/Remove 六分支 mutation response 落为 sealed read-only typed DTO、类型安全构造函数及 `EncodeMutationResponse`/`DecodeMutationResponse` payload codec；严格保持 4 KiB、depth 2、token 32、UTF-8、duplicate-key、single JSON value、closed object 和 operation-specific error allowlist。该批新增导出接口，须 Ask First；不含 uint32-be response frame、raw payload writer、Unix client、accept-loop/executor、fake/backend 映射、连接关闭/partial-write/重试、Probe/Snapshot success、Schema/依赖/配置/数据库/systemd 变更。生产代码、测试、ADR 与 Evidence 尚未修改，未运行验证；G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |
| 2026-09-01 | B4-l2 mutation response typed DTO + payload codec | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/ipc/response.go`、`internal/ipc/response_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-response-codec/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确确认仅实现 mutation response typed DTO 与 payload codec。sealed Apply/Remove interfaces、六个类型安全构造入口、确定性 compact JSON encode、fail-closed decode、独立稳定 codec error classification 已落地；复用 12 valid + 28 invalid golden，覆盖 constructor/allowlist、nil/typed-nil、4 KiB/depth 2/token 32 exact/one-over、数学整数 version、分类优先级、错误脱敏与 seed fuzz invariants。targeted count=20 Race、全仓 normal/Race/Vet/module、139 包 IPC test dependency closure、三目标 CGo-free compile、gofmt 与 diff-check 均通过；独立 implementation checkpoint 与 final FULL_SCOPE/INTEGRATION delta closure 均通过，repair round 1 后最终 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。等待用户 Code Review；frame/writer/client/executor/backend mapping/connection semantics/Probe-Snapshot success 仍排除，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |
| 2026-09-01 | B4-l2 mutation response typed DTO + payload codec Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/response.go`、`internal/ipc/response_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-response-codec/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `B4-l2 Code Review 通过`，用户门更新为 `PASSED`；已接受 sealed typed DTO、六分支构造、确定性 encode、fail-closed decode、40 golden、完整验证及 final repair round 1 后 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED` 的证据。仅关闭本 Delivery Unit，不描述为 Verified；frame/writer/client/executor/backend mapping/connection semantics/Probe-Snapshot success 仍排除，B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。未提交、未推送。 |
| 2026-09-01 | B4-l3 typed mutation response frame 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | 拟新增 `internal/ipc/response_frame.go`、`internal/ipc/response_frame_test.go`，复用 `internal/ipc/frame.go`、`internal/ipc/response.go`、mutation response Schema/goldens 与 ADR-0001 | B4-l2 通过后，契约/代码/测试并行预检推荐下一批只新增 platform-neutral `DecodeMutationResponseFrame(io.Reader)` 与 `WriteMutationResponseFrame(io.Writer, MutationResponse)`；raw payload helper 保持 package-private，禁止导出任意 `[]byte` writer。冻结 uint32-be、1 MiB frame 与 4 KiB mutation payload，encode 失败零写入；positive short-write 只续写同一 frame，带 error 或 `0,nil` 立即返回稳定脱敏 write failure；函数不 Dial/Accept/SetDeadline/Close、不发送第二帧、不构造 wire Unknown。新增导出 API/错误码须 Ask First；Unix client、request encoder/constructors、accept-loop/executor、Backend mapping、connection ownership/deadline/close/semantic retry、Probe/Snapshot success、Schema/依赖/配置/数据库/systemd 均排除。未修改生产代码/测试，未运行验证；B4/G18/M0 不提升。 |
| 2026-09-01 | B4-l3 typed mutation response frame reader/writer | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/ipc/response_frame.go`、`internal/ipc/response_frame_test.go`、`internal/ipc/response_frame_internal_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-response-frame/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确确认仅实现 typed mutation response frame reader/writer，raw payload writer 保持 private。reader 冻结 header→1 MiB→4 KiB→payload→codec precedence；writer encode-first、零写入失败、positive short-write、`0,nil`/error/非法 count 脱敏失败、nil writer、不 Close/不二次 frame 均已实现。12 valid + 28 invalid golden、private helper 0/1 MiB/one-over、targeted Race count=20、全仓 normal/Race/Vet/module、140 包 IPC test dependency closure、三目标 CGo-free compile、gofmt/diff-check 均通过；独立 implementation checkpoint 与 final FULL_SCOPE/INTEGRATION 均为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`，repair round 0、P0-P3 全无。等待用户 Code Review；client/request writer/executor/backend/connection lifecycle/Probe-Snapshot success 仍排除，B4/G18/M0 不提升。 |
| 2026-09-01 | B4-l3 typed mutation response frame Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/response_frame.go`、`internal/ipc/response_frame_test.go`、`internal/ipc/response_frame_internal_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-response-frame/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `B4-l3 Code Review 通过`，用户门更新为 `PASSED`；已接受 typed frame reader/writer、private raw helper、40 golden、完整验证以及 final repair round 0 后 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED` 的证据。仅关闭本 Delivery Unit，不描述为 Verified；client/request writer/executor/backend/connection lifecycle/Probe-Snapshot success 仍排除，B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。未提交、未推送。 |
| 2026-09-01 | B4-l4 mutation request typed constructors + payload encoder 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | `internal/ipc/request.go`、拟新增 `internal/ipc/request_encode_test.go`、复用 `schema/ipc-v1.schema.json` 与 29 个 request golden、`docs/adr/0001-phase1-process-privilege-boundary.md` | B4-l3 通过后，契约/实现/测试并行预检选择下一最短依赖：仅为 Apply 三 domain 与 Remove 新增 sealed `MutationRequest`、四个 domain-specific typed constructors 和 deterministic `EncodeMutationRequest`。version/owner/operation/kind 固定，不接收 raw JSON/map/command/binary/env/cwd/物理对象名；非法 Prefix/order/timeout/scope 不自动修正。复用 production decoder、6 valid + 23 invalid golden，新增 constructor、slice aliasing、nil/typed-nil、64 KiB/prefix exact-one-over、deterministic encode、round-trip、脱敏与 seed fuzz 测试。该批新增导出 API，须 Ask First；request frame writer、Unix client、deadline/close/retry、response routing、Probe/Snapshot success、accept-loop/executor、Backend/Firewall、Schema/依赖/配置/数据库/systemd 均排除。未修改生产代码，验证 `NOT RUN`；B4/G18/M0 不提升。 |
| 2026-09-01 | B4-l4 mutation request typed constructors + payload encoder | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/ipc/request.go`、`internal/ipc/request_encode_test.go`、`internal/ipc/request_encode_internal_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-request-codec/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确确认新增 sealed `MutationRequest`、Apply Infrastructure/Policy/Target 与 Remove 四个安全构造入口及 deterministic `EncodeMutationRequest`。固定 version/owner/operation/kind/schema version，不接受 raw JSON/map/命令或物理对象名；Policy slices copy、非法 Prefix/order/timeout/expiry/scope fail-closed，空 allowlist 固定 `[]`、Target 无 expiry 固定 `null`。6 valid + 23 invalid golden、非-golden caller-value round-trip、aliasing、nil/typed-nil、64 KiB/prefix exact-one-over、确定性编码、错误脱敏与 seed fuzz 已冻结。targeted Race count=20、5s fuzz、全仓 normal/Race/Vet/module、140 包 IPC test dependency closure、三目标 CGo-free compile、gofmt/diff-check 均通过；independent contract guard、implementation/test-quality checkpoint、final FULL_SCOPE/INTEGRATION 及记录 delta closure 均为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`，repair round 1 后 P0-P3 全无，仅等待用户 Code Review。request frame writer、Unix client、连接/重试/response routing、Probe/Snapshot success、accept-loop/executor、Backend/Firewall 仍排除；B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-01 | B4-l4 mutation request typed constructors + payload encoder Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/request.go`、`internal/ipc/request_encode_test.go`、`internal/ipc/request_encode_internal_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-request-codec/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `B4-l4 Code Review 通过`，用户门更新为 `PASSED`；已接受 sealed mutation request union、四个 domain-specific constructors、deterministic encoder、29 个 request golden 复用、专项/fuzz/完整验证及 repair round 1 后最终 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无的证据。仅关闭本 Delivery Unit，不描述为 Verified；request frame writer、Unix client、连接/重试/response routing、Probe/Snapshot success、accept-loop/executor、Backend/Firewall 仍排除，B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。未提交、未推送。 |
| 2026-09-01 | B4-l5 typed mutation request frame writer 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | 拟新增 `internal/ipc/request_frame.go`、`internal/ipc/request_frame_test.go`，复用 `internal/ipc/request.go`、`internal/ipc/frame.go`、`internal/ipc/response_frame.go` 与 ADR-0001 | B4-l4 通过后，并行比较 request frame writer、直接 Unix client、Probe/Snapshot success contract 与 accept-loop/executor。跨全部候选的最短闭合依赖是仅新增 `WriteMutationRequestFrame(io.Writer, MutationRequest) error`：先完成 typed encode，再复用 private `writeFramePayload` 写单一 uint32-be frame；不导出 raw payload writer，不新增错误码/cap，不移动共享 helper。冻结 encode failure 零写入、positive short-write、error/`0,nil`/非法 count 终止且脱敏、nil writer、不 Close/Flush/SetDeadline、不二次 frame/重试。复用四个 mutation golden、DecodeFrame round-trip、forged/nil/typed-nil、64 KiB 与共享 helper 0/1 MiB/one-over 回归。直接 Unix client 还缺 server UID、deadline/Close、response correlation 与 Unknown 语义；accept-loop/executor 还被 Probe/Snapshot response contract、Backend mapping 和二次权限校验阻塞。该批新增导出 API，须 Ask First；Unix client、Dial/deadline/Close、response routing、semantic retry、Probe/Snapshot success、accept-loop/executor、Backend/Firewall、Schema/依赖/配置/数据库/systemd 均排除。production/test/ADR/Evidence 未修改，验证 `NOT RUN`；B4/G18/M0 不提升。 |
| 2026-09-01 | B4-l5 typed mutation request frame writer | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/ipc/request_frame.go`、`internal/ipc/request_frame_test.go`、`internal/ipc/request_frame_internal_test.go`、`internal/ipc/response_frame.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-request-frame/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户按冻结范围确认仅新增 `WriteMutationRequestFrame(io.Writer, MutationRequest) error`。实现先完整 typed encode，再复用 private `writeFramePayload`；validation failure 零写入，positive short-write 续写，error/`0,nil`/非法 count 稳定脱敏终止，不 Close/Flush/SetDeadline、不二次 frame/重试。四 mutation exact golden、DecodeFrame 逐字段 round-trip、32 次 determinism、nil/typed-nil/forged state、validation-vs-nil-writer precedence、header/payload short-write/terminal failure、错误脱敏与 lifecycle 均由专项测试冻结。targeted request+response Race count=20、全仓 normal/Race/Vet/module、140 包 IPC test dependency closure、三目标 CGo-free compile、gofmt/diff-check 均通过；contract guard、test-quality、implementation checkpoint 与 final FULL_SCOPE/INTEGRATION 均为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`，repair round 0、P0-P3 全无，仅等待用户 Code Review。Unix client、连接/response/Unknown、Probe/Snapshot success、executor/Backend 仍排除，B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-01 | B4-l5 typed mutation request frame writer Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/request_frame.go`、`internal/ipc/request_frame_test.go`、`internal/ipc/request_frame_internal_test.go`、`internal/ipc/response_frame.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-request-frame/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `B4-l5 Code Review 通过`，用户门更新为 `PASSED`；已接受 typed mutation request frame writer、encode-before-write、shared helper、完整 failure/lifecycle 矩阵、全仓验证、三目标构建及 repair round 0 后最终 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无的证据。仅关闭本 Delivery Unit，不描述为 Verified；Unix client、连接生命周期、response correlation、Unknown/Probe-first、Probe/Snapshot success、executor、Backend/Firewall 仍排除，B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。未提交、未推送。 |
| 2026-09-01 | B4-l6 Linux mutation Unix round-trip client 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | 拟新增 `internal/ipc/client_linux.go`、`internal/ipc/client_linux_test.go`，复用 `internal/ipc/request_frame.go`、`internal/ipc/response_frame.go`、`internal/ipc/peer_linux.go`、`internal/ipc/listener_linux.go` 与 ADR-0001 | 三路只读评审一致推荐下一最小闭环为 mutation-only Linux client：拟新增 `RoundTripMutation(context.Context, MutationRequest) (MutationResponse, error)`，生产入口固定 `/run/guard/enforcer.sock` 与 root server UID 0；连接后首次写入前用 `SO_PEERCRED` 认证，单连接仅写一个 typed request frame、读一个 typed response frame并关闭。context 是唯一时间预算并覆盖 Dial/读写，不新增内部固定 timeout；Apply 校验 operation+domain，Remove 校验 operation/type；完整关联 response 优先于随后 cancellation，typed wire `unknown` 正常返回。写入开始后如未取得完整且关联正确 response，mutation 结果按不确定处理、上层必须 Probe/Snapshot 后再决策，client 不伪造 Unknown、不自动重连或重试。新增 Linux-only 导出 API、稳定脱敏 client error codes、root peer 与生命周期/Unknown 语义命中 Ask First，立即实现保持 `NO-GO`。Probe/Snapshot success contract 因缺 production capabilities/ManagedState/ForeignContext 编译权威与资源/错误闭集而另批；executor/serve loop 继续被 observation contract、Backend mapping 与二次语义校验阻塞。production/test/ADR/Evidence 未修改，验证 `NOT RUN`；B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-01 | B4-l6 Linux mutation Unix round-trip client | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/ipc/client_linux.go`、`internal/ipc/client_linux_test.go`、`internal/ipc/peer_linux.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-mutation-client/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确确认 B4-l6 边界。Linux-only `RoundTripMutation` 固定 production socket/root peer UID，invalid request validation-before-transport、peer-before-write、context-only Dial/I/O budget、arm 后同步 pre-write cancel guard、单连接单帧、operation/type/domain correlation、完整关联 response 胜随后 cancellation、typed wire Unknown 正常返回、best-effort close 与零自动重试均已实现。真实临时 Unix socket 覆盖四 mutation、confirmed/rejected/typed unknown、UID mismatch 零写、post-Dial cancel 零写、read cancel/deadline、truncated/invalid response、EOF close 与 second-accept timeout。Linux IPC full Race count=20、Windows/Linux full normal/Vet/module、Windows full Race、140 依赖闭包及三目标 CGo-free compile 均通过；contract guard 两个 P1 已修复，closure 为 `COMPLETE / FRESH / PASSED`。final FULL_SCOPE/INTEGRATION 为 `APPROVED_WITH_FOLLOWUPS / CHILD_AGENT / COMPLETE / FRESH / PASSED`，记录 P2-2/P2-3 已修复，仅保留调用栈白盒测试稳健性 P2，当前等待用户 Code Review。WSL native Go `UNAVAILABLE`，真实 `/run/guard` root/cross-UID、executor/Backend/Probe-first/CI/commit-bound Evidence 未验证。B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-01 | B4-l6 Linux mutation Unix round-trip client Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/client_linux.go`、`internal/ipc/client_linux_test.go`、`internal/ipc/peer_linux.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-mutation-client/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `B4-l6 Code Review 通过`，用户门更新为 `PASSED`；已接受固定 production socket/root peer、validation-before-transport、peer-before-write、context-only 单连接 typed round-trip、response correlation、typed Unknown、零自动重试、完整验证、两个 P1 修复以及最终 `APPROVED_WITH_FOLLOWUPS / CHILD_AGENT / COMPLETE / FRESH / PASSED` 的证据。唯一调用栈白盒测试 P2 作为非阻塞 follow-up 保留。仅关闭本 Delivery Unit，不描述为 Verified；真实 `/run/guard` root/cross-UID、Probe/Snapshot success/Probe-first、accept-loop/executor、Backend/Firewall、systemd、CI 与 commit-bound Evidence 仍未验证。B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。未提交、未推送。 |
| 2026-09-01 | B4-l7 platform-neutral FirewallCapabilities domain authority 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | 拟新增 `internal/firewall/capabilities.go`、`internal/firewall/capabilities_test.go`，复用 `docs/contracts/firewall-behavior.md`、M0 Contract §14 与技术设计 §56 | B4-l6 通过后，三路只读评审比较 Probe/Snapshot success contract 与 Enforcer executor/serve loop。executor/serve loop 因缺 production Firewall 编译契约、IPC→Backend mapper、二次 capability/ownership/object-role 校验、Backend result allowlist、Probe-first 与 lifecycle 而 `NO-GO`；现有 reconcile/fake 不能下沉 root Enforcer。直接合并 Probe+Snapshot response 也会让 IPC Schema 反向发明尚未冻结的领域权威。建议下一最小批仅在 platform-neutral `internal/firewall` 冻结 closed `BackendKind` 与不可变 `FirewallCapabilities`：覆盖 backend/tool version、IPv4/IPv6/CIDR、set/native timeout/crash-safe expiry、atomic batch、INPUT/FORWARD、UFW/Docker integration、ownership/mutation readiness；非法组合 fail-closed，字符串/枚举有界，不携带 command、binary、任意物理对象名或 raw backend error。该批不新增/修改 Backend 方法集，不含 `ManagedState`/`ForeignContext`、IPC Schema/DTO/codec/frame/client、executor/serve loop、真实 nftables、配置/依赖/数据库/systemd。新增跨模块导出类型与 capability 语义命中 Ask First，立即实现保持 `NO-GO`；验证 `NOT RUN`，B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-01 | B4-l7 platform-neutral FirewallCapabilities domain authority | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/firewall/capabilities.go`、`internal/firewall/capabilities_test.go`、`artifacts/evidence/M0/worktree/m0-b/firewall-capabilities/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `确认 B4-l7`。已冻结三个 closed backend kind、immutable capability value、1..128 bytes printable ASCII tool version，以及 IP family/scope、native timeout/set、crash-safe expiry、Docker/FORWARD、ownership/CIDR、native nftables mutation readiness 的 fail-closed 不变量；iptables atomic batch/native set 双向独立，UFW integration proof 不误绑 INPUT。专项 Race count=20、全仓 normal/Race/Vet/module、135 包依赖闭包、三目标 CGo-free compile、gofmt/diff-check 均 PASS；独立 contract/test checkpoint 与 test delta closure 均为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。final FULL_SCOPE/INTEGRATION 初审为 `APPROVED_WITH_FOLLOWUPS`、无 P0/P1，仅一个反向独立性测试 P2；补测并重跑受影响验证/三目标构建后，final delta closure 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。仅等待用户 Code Review。不修改 Backend interface，不含 ManagedState/ForeignContext、IPC、executor、真实 Firewall/runtime，B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-01 | B4-l7 platform-neutral FirewallCapabilities domain authority Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `internal/firewall/capabilities.go`、`internal/firewall/capabilities_test.go`、`artifacts/evidence/M0/worktree/m0-b/firewall-capabilities/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `B4-l7 Code Review 通过`，用户门更新为 `PASSED`；接受 closed backend kind、immutable capability value、fail-closed 组合不变量、完整验证、三目标构建，以及 final repair 后 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无的证据。仅关闭本 Delivery Unit，不描述为 Verified；Backend interface、ManagedState/ForeignContext、IPC、executor、真实 Firewall/runtime 边界不变，B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。未提交、未推送。 |
| 2026-09-01 | B4-l8 ProbeCapabilities success Schema + security golden 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | 拟新增 `schema/ipc-v1-probe-capabilities-success.schema.json`、`schema/ipc_v1_probe_capabilities_success_schema_test.go`、`schema/testdata/ipc-v1-probe-capabilities-success/`，复用 `internal/firewall/capabilities.go` 与 Firewall/IPC Contract | B4-l7 通过后，三路只读评审比较 Probe response、production Backend、executor/serve loop 与真实 Firewall；后二者仍缺 ManagedState/ForeignContext、OperationPlan/ApplyResult、IPC mapper、二次 capability/ownership/object-role 校验、failure envelope 与 lifecycle，因此 `NO-GO`。下一最小批建议仅冻结 success-only Probe wire：root exact `{version,operation,payload}`；payload exact 15 required fields，一对一映射 B4-l7 capabilities；三个 backend closed enum，tool version 1..128 trimmed printable ASCII，其余字段 strict boolean，semantic invariants 必须与 production constructor 一致。资源上限拟为 1 MiB frame、4 KiB response、depth 2、token 64；覆盖 duplicate/unknown/missing/type confusion、UTF-8/single-value、exact/one-over、command/binary/env/cwd/object/error 注入、正反 capability independence 与 seed fuzz。该公共 wire contract 命中 Ask First；当前 production/schema/golden 未修改，验证 `NOT RUN`。不含 Go DTO/codec/frame/client、Probe failure union、Snapshot、Backend interface、executor、真实 Firewall、配置/依赖/DB/systemd/runtime；B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-01 | B4-l8 ProbeCapabilities success Schema + security golden | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `schema/ipc-v1-probe-capabilities-success.schema.json`、`schema/ipc_v1_probe_capabilities_success_schema_test.go`、`schema/testdata/ipc-v1-probe-capabilities-success/`、`artifacts/evidence/M0/worktree/m0-b/ipc-probe-capabilities-success/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `确认 B4-l8`。success-only root/payload closed Schema、15 字段 B4-l7 constructor mapping、三个 backend enum、tool version 1..128 printable ASCII、13 个 strict boolean、4 KiB/depth 2/token 64 已冻结；4 valid + 21 invalid golden、结构/语义分类、能力独立性、注入面、exact/one-over、脱敏与 seed fuzz 通过。targeted Race count=20、final 5s fuzz、全仓 normal/Race/Vet/module、141 包依赖闭包、三目标 CGo-free compile 均 PASS；独立 checkpoint 的 string-type P2 已修复，fresh delta closure 为 `COMPLETE / FRESH / PASSED / APPROVED`、P0-P3 全无。final FULL_SCOPE/INTEGRATION 为 `APPROVED_WITH_FOLLOWUPS / CHILD_AGENT / COMPLETE / FRESH / PASSED`，无 P0/P1/P3；fixture tree identity 与 untracked diff-check 两个 Evidence-only P2 已补完整 tree hash/算法和 28 文件真实格式扫描。record-only freshness closure 最终为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无；当前等待用户 Code Review，不描述为 Verified。Probe failure、DTO/codec/frame/client、Snapshot、Backend/executor/真实 Firewall、Linux runtime/CI/commit-bound Evidence 均未验证；B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。未提交、未推送。 |
| 2026-09-01 | B4-l8 ProbeCapabilities success Schema + security golden Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `schema/ipc-v1-probe-capabilities-success.schema.json`、`schema/ipc_v1_probe_capabilities_success_schema_test.go`、`schema/testdata/ipc-v1-probe-capabilities-success/`、`artifacts/evidence/M0/worktree/m0-b/ipc-probe-capabilities-success/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `B4-l8 Code Review 通过`，用户门更新为 `PASSED`；已接受 success-only closed Schema、15 字段 production constructor mapping、4 valid + 21 invalid golden、安全/资源/能力独立性、全仓验证、三目标编译，以及最终 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无的证据。仅关闭 B4-l8 Delivery Unit，不描述为 Verified；Probe failure、DTO/codec/frame/client、Snapshot、Backend/executor/真实 Firewall、Linux runtime、CI 与 commit-bound Evidence 仍未验证。B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。本次仅同步验收状态，未重跑代码验证；未提交、未推送。 |
| 2026-09-01 | B4-l9 ProbeCapabilities IPC transport 完整批次 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | 拟新增 Probe failure Schema/golden、typed response codec/frame、fixed Probe request frame、Linux client、认证后单请求 server adapter、真实临时 Unix socket E2E、`artifacts/evidence/M0/worktree/m0-b/ipc-probe-capabilities-transport/result.md`，并更新 ADR-0001 | 用户明确回复 `确认 B4-l9`，一次授权公共 failure wire 与 exported Go API。failure exact root `{version,operation,error_code}`，closed code 仅 `unsupported|not_ready`，无 status/payload/message/details/cause/raw error；B4-l8 success 保持不变，可信完整事实但不可 mutation 仍返回 success + `mutation_ready=false`。transport 固定 production socket/root UID、peer-before-write、context-only 单连接单 request/response、严格 operation correlation、零自动重试；server 只能在 `AcceptRequest` 认证/解码后调用 typed handler并取得连接所有权。该批不实现/注册真实 nftables/iptables Backend，不证明 production ownership、UFW/Docker、packet path 或 `mutation_ready=true`，不含 Snapshot、通用 accept loop/executable、配置/依赖/DB/systemd。当前进入整体实现与验证；B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-01 | B4-l9 ProbeCapabilities IPC transport 实现、验证与独立终审 | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/ipc/probe_request.go`、`internal/ipc/probe_request_frame.go`、`internal/ipc/probe_response.go`、`internal/ipc/probe_response_frame.go`、`internal/ipc/probe_client_linux.go`、`internal/ipc/probe_server_linux.go`、对应测试、failure Schema/2 valid + 20 invalid golden、`artifacts/evidence/M0/worktree/m0-b/ipc-probe-capabilities-transport/result.md`、ADR-0001 | fixed request、sealed success/failure DTO、deterministic codec、4 KiB/depth2/token64 decoder、1 MiB frame、Linux fixed socket/root peer client、认证后单请求 server adapter与 typed remote failure 已整体闭合。Windows targeted Race count=20、Docker Linux targeted Race count=20、WSL2 真实临时 Unix Socket count=100、三项 5s fuzz、全仓 normal/Race/Vet/module、145 包依赖闭包、Windows/Linux amd64/Linux arm64 六个 CGo-free IPC/Schema test builds 均 PASS。首次并行 full suite 的既有 reconcile 时序波动已以 exact count=20、独立 full normal/Race 闭合并如实记录。CONTRACT/SECURITY checkpoint P0-P3 全无；TEST-QUALITY 初审发现 server partial-write cancellation 丢失 context identity 一个 P1，已修复并补 incomplete/complete linearization tests，fresh delta closure 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。FULL_SCOPE/INTEGRATION 为 `APPROVED_WITH_FOLLOWUPS / CHILD_AGENT / COMPLETE / FRESH / PASSED`；两轮 Evidence-only P2（实时状态未推进、diff-check 覆盖被夸大）均已修复，最终 record-only closure 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。当前仅等待用户 Code Review，不描述为 Verified。真实 Firewall prober/Backend、Snapshot、通用 Enforcer runtime/systemd、目标主机 production facts、CI/commit-bound Evidence 未验证；B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-01 | GORM UnitOfWork 与 B4 typed Firewall IPC 本地提交 | `REVIEW / Implemented` | `REVIEW / Implemented` | `4642cfb feat(store): complete GORM unit-of-work writers`；`04546f3 feat(ipc): add typed firewall transport stack` | 用户明确要求“把代码提交”。提交前完整审计 staged/unstaged/untracked/deleted，按两个独立逻辑范围提交：GORM ADR/UnitOfWork/tests 为 15 files、5137 insertions/195 deletions；Firewall domain/typed mutation+Probe IPC/Schema/golden/STATUS 为 82 files、9596 insertions/16 deletions。两次 staged diff-check 与敏感信息模式扫描均无阻断；提交后标准 `git status --porcelain` 为空。`master` 相对 `origin/master` ahead 3（含既有一个本地提交）；未推送。该提交操作不替代 B4-l9 用户 Code Review，不把其提升为 DONE/Verified，不提升 B4/G18/M0。 |
| 2026-09-02 | B4-l10 SnapshotManaged 完整功能单元 | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/firewall/snapshot.go`、`internal/ipc/snapshot_*.go`、success/failure Schema 与 53 个 fixture、`artifacts/evidence/M0/worktree/m0-b/ipc-snapshot-managed-transport/result.md`、ADR-0001 | 用户明确回复 `确认 B4-l10`。immutable managed snapshot domain、versioned canonical digest、sealed success/failure codec/frame、Linux fixed-socket/root-peer client 与认证后单请求 server 已闭合；targeted/full normal/Race/Vet/module、三项 fuzz、六项跨目标编译、WSL2 与 Docker Linux Race 均通过。三个分区 checkpoint 和 final integration fresh-delta closure 最终 `CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无；等待用户 Code Review。真实 Firewall provider、Probe-first、通用 executor/runtime、root:guard/systemd、CI/commit-bound Evidence 与上级 Gate 不在本批。 |
| 2026-09-02 | B4-l10 SnapshotManaged Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-b/ipc-snapshot-managed-transport/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `B4-l10 Code Review 通过`，用户门更新为 `PASSED`；仅关闭 B4-l10 Delivery Unit，不描述为 `Verified`。冻结实现与 53 个 fixture 身份未漂移，本次仅同步验收记录，未重跑代码验证；未提交、未推送。B4-l9 保持 `REVIEW / Implemented`，B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |
| 2026-09-02 | B4-l11 authenticated mutation server adapter 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | 拟新增 Linux-only `MutationHandler`、`(*UnixListener).ServeMutationOnce`、stable local server error 与专项真实 Unix socket 测试 | B4-l2-l6 已冻结 mutation request/response codec/frame/client，B4-k 已冻结 listener/peer gate；当前最短依赖满足项是补齐认证后 mutation 单请求 server adapter。拟要求 authentication/decode-before-handler、closed typed response、operation/domain correlation-before-write、context-only deadline、单连接/单请求/最多一帧、完整 frame delivery point 与错误脱敏。该导出 API 命中 Ask First，当前实现 `NO-GO`、验证 `NOT RUN`。不含 accept loop、并发/优雅停机、Backend executor/provider、Plan/result mapping、Probe-first、真实 Firewall、配置/依赖/DB/systemd；B4/G18/M0 不提升。 |
| 2026-09-02 | B4-l11 authenticated mutation server adapter | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/ipc/client_linux.go`、`internal/ipc/mutation_server_linux.go`、`internal/ipc/mutation_server_linux_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-mutation-server/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `确认 B4-l11`。Linux-only `MutationHandler`、`(*UnixListener).ServeMutationOnce` 与 stable server errors 已实现；`AcceptRequest` 保证认证/解码先于 handler，closed typed response 在首字节前完成 operation/domain correlation 与 encode，context-only deadline、单连接/单请求/最多一帧、完整 frame delivery point、accepted connection ownership 与 typed-nil fail-closed 均由专项测试冻结。targeted Docker Race `count=20`、全仓 normal/Race/Vet/module、WSL2 两组 `count=20`、三目标 CGo-free compile、格式/凭据扫描均通过；独立 Tier 3 FULL_SCOPE 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`，P0-P3 全无。当前等待用户 Code Review；未提交、未推送。通用 accept loop、Backend executor/provider、Plan/result mapping、Probe-first、真实 Firewall、配置/依赖/DB/systemd/runtime 与 B4/G18/M0 均不提升。 |
| 2026-09-02 | B4-l11 authenticated mutation server adapter Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-b/ipc-mutation-server/result.md`、`docs/adr/0001-phase1-process-privilege-boundary.md` | 用户明确回复 `B4-l11 Code Review 通过`，用户门更新为 `PASSED`；仅关闭 B4-l11 Delivery Unit，不描述为 `Verified`。三个冻结代码文件 SHA256 未漂移，既有 targeted/full、WSL2、Docker、三目标 CGo-free compile 与独立终审结论继续有效；本次仅同步验收记录，未重跑代码验证。未提交、未推送。B4-l9 保持 `REVIEW / Implemented`，B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |
| 2026-09-02 | B4-l10/B4-l11 本地代码提交 | `DONE / Implemented` | `DONE / Implemented` | `9e2925a`、`0edeaad` | 用户明确要求先提交代码。B4-l10 的 69 个 managed snapshot domain/typed transport/Schema/fixture 文件提交为 `9e2925a feat(ipc): add managed snapshot transport`；B4-l11 的 3 个 mutation server adapter 文件提交为 `0edeaad feat(ipc): add authenticated mutation server`。提交前全仓 normal tests、Vet、module verify、gofmt/diff-check 与分组 credential-value scan 均通过；两个提交后 `origin/master...HEAD = 0 6`，未推送。ADR/STATUS 由独立记录提交同步。 |
| 2026-09-02 | B4-l12 production-neutral mutation semantic bridge 只读预检 | `NOT_STARTED / Specified` | `IN_PROGRESS / Specified` | 拟新增 platform-neutral Firewall mutation plan/result authority、特权侧 pure authorization 与 IPC 双向 mapper 及专项测试 | CodeGraph 与三路独立审计确认：production `internal/firewall` 已有 `FirewallCapabilities`/`ManagedSnapshot`，但 `OperationPlan`/`ApplyResult` 仅存在于 `internal/firewall/fake`，`reconcile.Backend` 仍直接绑定 fake 类型；`MutationHandler` 尚无 production mapper/实现。下一最短完整单元拟一次冻结 closed immutable 三 domain plan、ownership-scoped removal authorization、closed confirmed/rejected/unknown result、deterministic plan digest，以及 validated IPC request + fresh capabilities/snapshot 的 fail-closed 二次授权和 result→IPC mapper。不得复用 fake plan/result，不含 Backend interface/provider、真实 nftables/iptables、物理对象名/命令、handler 接线、accept loop、Probe-first/retry/Reconcile、配置/依赖/DB/systemd/executable。该批新增跨包导出 Go contract 并冻结权限语义，命中 Ask First；当前实施 `NO-GO`、验证 `NOT RUN`，未提交、未推送。B4-l9、B4/G18/M0 不提升。 |
| 2026-09-02 | B4-l12 production-neutral mutation semantic bridge | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/firewall/mutation.go`、`internal/firewall/mutation_test.go`、`internal/enforcer/mutation.go`、`internal/enforcer/mutation_test.go`、`artifacts/evidence/M0/worktree/m0-b/firewall-mutation-semantic-bridge/result.md`、ADR-0001 | 用户明确回复 `确认 B4-l12`。closed immutable infrastructure/policy/target Apply authority、ownership-scoped Remove、完整 capabilities/snapshot-bound deterministic digest、closed confirmed/rejected/unknown result与 explicit IPC mapper 已实现。授权 fail-closed 覆盖 owner、authority validity/readiness、backend、basis、family/CIDR/scope/native timeout；iptables 不被错误要求全局 atomic。完全空 managed state 的 Remove 为 immediate confirmed no-op，policy-only/target-only partial residue 仍生成 cleanup authority。targeted Race `count=20`、全仓 normal/Race/Vet/module、Windows amd64/Linux amd64/Linux arm64 六项 CGo-free test-compile 均通过。独立 Tier 3 FULL_SCOPE 初审发现 partial-state Remove 一个 P1 与 domain-specific mapper assertions 一个 P2，repair round 1 后 fresh-delta 为 `APPROVED / FRESH / PASSED`、P0-P3 全无。当前等待用户 Code Review；未提交、未推送。fresh acquisition/same-attempt authority、Backend/provider、handler/executor/serve loop、真实 Firewall、Probe-first/retry/Reconcile、配置/依赖/DB/systemd/executable 均不在本批；B4-l9、B4/G18/M0 不提升。 |
| 2026-09-02 | B4-l12 mutation semantic bridge Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `artifacts/evidence/M0/worktree/m0-b/firewall-mutation-semantic-bridge/result.md`、ADR-0001 | 用户明确回复 `通过，继续下一步`，B4-l12 用户 Code Review 门更新为 `PASSED`。四个冻结 Go 文件 SHA256 未漂移；既有 targeted/full、三目标 test-compile 与独立 Tier 3 repair closure 继续有效，本次仅同步验收记录，未重跑代码验证。未提交、未推送。B4-l9 保持 `REVIEW / Implemented`，B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |
| 2026-09-02 | B4-l9 ProbeCapabilities IPC transport Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `04546f3`、`artifacts/evidence/M0/worktree/m0-b/ipc-probe-capabilities-transport/result.md`、ADR-0001 | 用户明确回复 `B4-l9 Code Review 通过`，用户门更新为 `PASSED`；仅关闭 B4-l9 Delivery Unit，不描述为 `Verified`。实现身份继续由本地提交 `04546f3` 绑定，既有验证与最终 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED` 结论继续有效；本次仅同步验收记录，未重跑代码验证、未新增提交、未推送。B4-l14 的依赖门随之解除并转为 `IN_PROGRESS / Specified`，但实施仍为 `NO-GO`、验证 `NOT RUN`，等待独立 `确认 B4-l14`。B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |
| 2026-09-02 | B4-l14 unified authenticated single-connection Enforcer router | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/ipc/enforcer_server_linux.go`、`internal/ipc/enforcer_server_linux_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-enforcer-router/result.md`、ADR-0001 | 用户明确回复 `确认 B4-l14`。Linux-only closed `EnforcerHandlers` 与 `(*UnixListener).ServeEnforcerOnce` 已实现：全部 handler 在 Accept 前完整校验；仅一次 `AcceptRequest`；按 closed concrete request 精确路由 Probe、Snapshot、Apply/Remove；每次最多调用一个 typed handler；mutation operation/domain correlation 与全部响应 encode 均在首字节前完成；单帧交付后关闭 accepted connection，listener 保持 caller-owned。WSL2 targeted `count=20`、Docker targeted Race `count=20`、Windows targeted/full normal/Race/Vet/module、Docker full IPC Race、三目标 CGo-free test-compile、141 包依赖闭包与格式/凭据/readback 均通过。三路独立 Tier 3 checkpoint 均 `CHILD_AGENT / COMPLETE / FRESH / PASSED`。最终 PARTITIONED_PLUS_INTEGRATION 初审 P0/P1 全无，仅一个 Evidence replay identity P2；补齐 WSL binary hash/exact host recipe、Docker image/mount/network recipe 与三目标 exact cross-build 后，fresh-delta 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无，repair round 1。当前等待用户 Code Review。持续 loop、continue/fatal policy、per-request timeout、真实 Backend/Firewall、systemd/executable/config/deps/DB 不在本批；B4/G18/M0 不提升，未提交、未推送。 |
| 2026-09-02 | B4-l14 unified Enforcer router Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/enforcer_server_linux.go`、`internal/ipc/enforcer_server_linux_test.go`、`artifacts/evidence/M0/worktree/m0-b/ipc-enforcer-router/result.md`、ADR-0001 | 用户明确回复 `B4-l14 Code Review 通过`，用户门更新为 `PASSED`；仅关闭 B4-l14 Delivery Unit，不描述为 `Verified`。两个冻结 Go 文件 SHA256 未漂移，既有 WSL2/Docker/全仓/跨目标验证、三路独立 checkpoint、final integration repair round 1 与 record-only closure 结论继续有效。本次仅同步验收记录，未重跑代码验证；未提交、未推送。持续 loop、真实 Backend/Firewall、systemd/executable 与生产 `/run/guard` 仍未验证；B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |
| 2026-09-02 | B4-l15 persistent unified Enforcer serve loop | `IN_PROGRESS / Specified` | `REVIEW / Implemented` | `internal/ipc/enforcer_loop_linux.go`、`internal/ipc/enforcer_loop_linux_test.go`、shared listener/one-shot adapters、`artifacts/evidence/M0/worktree/m0-b/ipc-enforcer-loop/result.md`、ADR-0001 | 用户明确回复 `确认 B4-l15`。Linux-only serial `ServeEnforcer` 已实现：idle Accept 只受 parent context；raw Accept 后启动有限 per-connection timeout，覆盖认证/decode/handler/encode/write；request-local failure 先关闭连接，再同步观察一次并继续；listener/credential/handler contract/invariant/unknown failure fail-closed 返回；parent cancel/deadline 保留 `errors.Is`。race-free listener serve owner 覆盖 persistent loop、四个 one-shot adapter 与 `AcceptRequest` admission，竞争入口在 Accept 前返回稳定 `already_serving`。WSL2 targeted `count=20`、Docker targeted/full IPC Race、全仓 normal/Race/Vet/module 与 Linux amd64/arm64 CGo-free compile 均通过。首轮安全审查的 loop-only gate 绕过 P1 在 repair round 2 以统一 ownership/六入口 oracle 闭合；随后 handler panic 清理 P1 在 repair round 3 以 inner-defer attempt 与 recover oracle 闭合。三路 fresh 终审均 PASSED；TEST-QUALITY 保留一个非阻断 100 ms serial oracle P2。Evidence replay/STATUS records P2 修复后的 fresh-delta 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。当前等待用户 Code Review。真实 Backend/Firewall、handler composition、并发/rate-limit/backoff、systemd/executable/config/deps/DB、生产 `/run/guard` 不在本批；未提交、未推送，B4/G18/M0 不提升。 |
| 2026-09-02 | B4-l15 persistent Enforcer loop Code Review 通过 | `REVIEW / Implemented` | `DONE / Implemented` | `internal/ipc/enforcer_loop_linux.go`、`internal/ipc/enforcer_loop_linux_test.go`、shared listener/one-shot adapters、`artifacts/evidence/M0/worktree/m0-b/ipc-enforcer-loop/result.md`、ADR-0001 | 用户明确回复 `B4-l15 Code Review 通过`，用户门更新为 `PASSED`；仅关闭 B4-l15 Delivery Unit，不描述为 `Verified`。九个冻结 Go 文件 SHA256 未漂移，既有 WSL2/Docker/全仓/跨目标验证、两轮 P1 repair、三路 fresh 终审与 records fresh-delta 结论继续有效。本次仅同步验收记录，代码验证未重跑；未提交、未推送。真实 Backend/Firewall、production handler composition、systemd/executable 与生产 `/run/guard` 仍未验证；B4 保持 `IN_PROGRESS / Implemented`，G18.1-G18.3 保持 `FAIL`，M0 保持 `NO-GO`。 |

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
20. GORM-1a 已按用户确认边界实现并获用户 Code Review 通过，当前 `DONE / Implemented`：仅 `PutParserOutcome` 使用显式七列
    GORM `Create`，同一 raw `*sql.Tx` 仍由 UnitOfWork 独占 Commit/Rollback；transaction wrapper 不暴露
    Begin/Commit/Rollback/Close/unwrap 能力。高风险 race、全仓回归、CGo-free 三目标编译与 Ubuntu WSL2
    SIGKILL/replay 初跑/count=20 已通过，Tier-3 checkpoint 与 final FULL_SCOPE 终审均为
    `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无。其余 SQL、Schema/API/依赖/PRAGMA 不变；
    G18.1–G18.3 保持 `FAIL`，M0 保持 `NO-GO`。
21. GORM-1b 已按用户确认边界实现并获用户 Code Review 通过，当前 `DONE / Implemented`：仅 `PutDetectionOutcome` 使用
    existing raw transaction private session 上的显式七列 GORM `Create`；success/permanent 的 SQL NULL/
    非空值、复合主键、deferred receipt FK、Commit/Rollback 与 sticky first-error 语义保持不变。
    focused race count=20、全仓 race/vet/module、依赖闭包、CGo-free 三目标编译和 Ubuntu WSL2
    SIGKILL/replay 初跑/count=20 已通过，Tier-3 checkpoint 为 `PASSED / COMPLETE / FRESH`、P0-P3
    全无。final FULL_SCOPE review 的唯一 Evidence 可复现性 P1 已在 repair round 1 解决，fresh delta
    后最终为 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无。
    `PutDetectionContribution` 与其他关键 SQL 保持 raw，G18.1–G18.3 保持 `FAIL`，M0 保持 `NO-GO`。
22. GORM-1c 已按用户确认边界实现并获用户 Code Review 通过，当前 `DONE / Implemented`：仅 `PutAlert` 使用 existing raw transaction
    private session 上的显式八列 GORM `Create`；private `alertRow` 的八个 GORM 映射字段均有简体中文字段级用途注释。
    三类 immediate FK、正交 primary key/detection membership unique、sticky first-error、事务原子性与 context 取消
    均由专用测试冻结；focused race count=20、全仓 race/vet/module、依赖闭包、三目标 CGo-free 编译和 Ubuntu WSL2
    SIGKILL/replay count=1/20 已通过。独立 checkpoint 通过；final FULL_SCOPE 的 Evidence/STATUS repair
    round 1 后最终 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无；
    `PutDetectionContribution` 与其他关键 SQL保持 raw，G18.1–G18.3 保持 `FAIL`，M0 保持 `NO-GO`。
23. GORM-1d 已获用户 Code Review 通过，当前 `DONE / Implemented`：仅 `AppendCriticalAudit` 使用 existing raw transaction
    private session 上的显式 15 列 GORM `Create`；private `criticalAuditRow` 的 15 个 GORM 映射字段均有简体中文
    字段级用途注释。四个 SQL NULL、`critical=1`、JSON 原文/空值归一化、UTC microseconds、三类 immediate FK、
    正交 primary key/idempotency unique、sticky first-error、事务原子性和 context 取消均由专用测试冻结；focused
    race count=20、全仓 race/vet/module、依赖闭包、三目标 CGo-free 编译和 Ubuntu WSL2 SIGKILL/replay count=1/20
    已通过。Tier-3 implementation checkpoint 与 final FULL_SCOPE 终审均为
    `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无。其他关键 SQL
    保持 raw，G18.1–G18.3 保持 `FAIL`，M0 保持 `NO-GO`。
24. GORM-1e 已获用户 Code Review 通过，当前 `DONE / Implemented`：仅 `UnitOfWork.PutDecision` 的单条显式
    15 列 INSERT 在 existing raw transaction private session 上改为显式 `Select/Create`，并新增 private
    `decisionRow` 与 15 个逐字段简体中文用途注释。该写入无 ON CONFLICT、RowsAffected、RETURNING、
    read-back、CAS 或 revision fence。必须冻结
    automatic/manual 引用规则、active/expired/revoked 生命周期组合、SQL NULL、UTC microseconds、
    node/rule-version/alert immediate FK、主键和两条 active partial-unique、sticky first-error、context 取消及
    同事务 commit/rollback。`InsertAutomaticDecision`、`InsertManualDecision`、其他 Decision 生命周期 SQL、
    `PutDetectionContribution`、`PutProjection`、`PutReceipt`、Schema/migration/API/依赖/PRAGMA、Store pool 与
    transaction finalization ownership 全部排除。focused Store/Processor Race count=20、全仓 Race/Vet/module、
    247 包依赖闭包、58 字段注释、三目标 CGo-free 编译和 Ubuntu WSL2 current-hash SIGKILL/replay
    count=1/20 均通过；Tier-3 checkpoint repair round 1 关闭一个 P1 测试缺口，FINAL FULL_SCOPE
    repair round 2 修正一个 Evidence 时态 P2，closure repair round 3 移除已完成 review 的 stale
    未验证项，fresh delta 后最终
     `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无；
     G18.1–G18.3 保持 `FAIL`，M0 保持 `NO-GO`。
25. 用户已明确授权一次性完成剩余 GORM-1 代码并通过 Code Review，当前 final batch 为 `DONE / Implemented`：
    `PutDetectionContribution`、`PutProjection`、`PutReceipt` 已整体迁移；UnitOfWork 八个 processing writer
    全部使用绑定同一 raw `*sql.Tx` 的 private GORM session，`uow.go` direct raw SQL 为零。三模型显式
    5/7/16 列与 28 个字段注释、Contribution conflict/stable identity、Projection revision fence/read-back、
    Receipt closed union/FK/crash-replay 均由专项测试冻结。focused Race count=20、最终全仓 normal/Race、
    Vet/module、依赖闭包、字段注释、三目标 CGo-free build、Linux integration vet、WSL current-hash replay
    与独立 Tier-3 implementation checkpoint、final FULL_SCOPE/INTEGRATION 全部通过，P0-P3 全无；用户门为
    `PASSED`。migration/PRAGMA/snapshot/commit-unknown/其他 lifecycle SQL、Schema/API/依赖/pool/
    transaction finalization 保持不变；G18.1–G18.3 保持 `FAIL`，M0 保持 `NO-GO`。
26. 用户已明确通过 B4-l2 Code Review，当前为 `DONE / Implemented`：sealed Apply/Remove response
    interfaces、六个类型安全构造分支、确定性 JSON
    encode、fail-closed decode 和稳定本地 codec error classification 已实现；既有 40 golden、精确资源
    边界、targeted count=20 Race、全仓 normal/Race/Vet/module 与三目标 CGo-free compile 均通过，独立
    implementation checkpoint 与 final FULL_SCOPE/INTEGRATION delta closure 均通过，repair round 1 后最终
    `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无，用户门为 `PASSED`；
    response frame、Unix client、executor/accept-loop、Probe/Snapshot success、
    Schema/依赖/配置/数据库与 systemd 均排除，B4/G18/M0 不提升。
27. 用户已明确通过 B4-l3 Code Review，当前为 `DONE / Implemented`。platform-neutral reader/writer、
    private raw helper 与 stable `write_failed` 已落地；
    40 golden、1 MiB/4 KiB pre-read cap、truncated frame、deterministic payload、encode-before-write、
    partial-write/zero-progress/error 脱敏、nil/invalid writer 和 caller-owned stream 均由测试冻结。targeted
    Race count=20、全仓 normal/Race/Vet/module、140 依赖闭包、三目标 CGo-free compile 与独立
    implementation checkpoint 与 final FULL_SCOPE/INTEGRATION 均通过，repair round 0、P0-P3 全无，
    用户门为 `PASSED`。Unix
    client、request writer、executor/backend、deadline/close、Unknown/Probe-first 和 Probe/Snapshot success
    仍排除，B4/G18/M0 不提升。
28. 用户已明确通过 B4-l4 Code Review，当前为 `DONE / Implemented`。mutation request sealed typed
    constructors 与 deterministic payload encoder、三文件实现/测试、6 valid + 23 invalid golden、非-golden
    round-trip、aliasing、nil/typed-nil、资源边界、错误脱敏和 seed fuzz 已通过 targeted/full Race、Vet/module、
    三目标 CGo-free compile；independent checkpoint 与 final FULL_SCOPE/INTEGRATION 在 repair round 1 后均
    `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无，记录 delta freshness closure 同样
    通过，用户门为 `PASSED`。request frame writer、Unix client、accept-loop/executor、Backend/result
    mapping、connection lifecycle、Probe/Snapshot success 与其他系统边界全部排除，B4/G18/M0 不提升。
29. 用户已明确通过 B4-l5 Code Review，当前为 `DONE / Implemented`。typed mutation request frame writer、
    encode-before-write、validation failure 零写入、short-write、zero-progress、terminal error、nil writer、
    caller-owned stream 与零重试均由四 golden、round-trip 和专项 failure-path 测试冻结。targeted request+
    response Race count=20、全仓 normal/Race/Vet/module、140 依赖闭包、三目标 CGo-free compile 与独立
    contract/test/implementation checkpoint 与 final FULL_SCOPE/INTEGRATION 均通过，repair round 0、P0-P3
    全无，用户门为 `PASSED`。Unix client、连接生命周期、response correlation、Unknown/Probe-first、Probe/Snapshot
    success、accept-loop/executor、Backend/Firewall 与系统边界全部排除，B4/G18/M0 不提升。
30. B4-l6 Linux mutation Unix round-trip client 只读预检已完成，当前为 `IN_PROGRESS / Specified`；三路评审
    一致将其排在 Probe/Snapshot response contract 与 Enforcer executor/serve loop 之前。拟议 Linux-only
    `RoundTripMutation` 固定 production socket 与 root peer UID，单连接完成认证、typed request/response、
    operation/domain correlation 和关闭；context 覆盖 Dial/读写，无内部 timeout、连接池、重连或自动重试。
    写入开始后未取得完整关联 response 时由上层按不确定结果执行 Probe-first；client 不伪造 wire Unknown。
    新导出 API、client error codes 与运行时/安全语义须 Ask First；当前生产代码/测试未修改，验证 `NOT RUN`。
31. 用户已确认 B4-l6，当前为 `REVIEW / Implemented`。Linux-only `RoundTripMutation`、private root peer
    verification、context Dial/I/O lifecycle、单连接 typed round-trip 与 operation/type/domain correlation 已实现；
    contract guard 的 pre-write cancel 与 Linux evidence 两个 P1 均修复。Linux IPC full Race count=20、
    Windows/Linux full normal/Vet/module、Windows full Race、140 依赖闭包和三目标 CGo-free compile 通过。
    保留一个 post-Dial cancel 白盒测试 P2；WSL Go、真实 production path/root/cross-UID、executor/Backend/
    Probe-first/CI/commit-bound Evidence 未验证。final FULL_SCOPE/INTEGRATION 已
    `APPROVED_WITH_FOLLOWUPS / CHILD_AGENT / COMPLETE / FRESH / PASSED`，下一门仅为用户 Code Review；
    B4/G18/M0 不提升。
32. 用户已明确通过 B4-l6 Code Review，本 Delivery Unit 当前为 `DONE / Implemented`，用户门为
    `PASSED`。保留一个 test-only 调用栈白盒稳健性 P2；未验证边界不变，B4/G18/M0 不提升。下一任务须先
    只读预检并重新确认范围与 Ask First 边界，当前不直接实施 Probe/Snapshot 或 executor/serve loop。
33. B4-l7 只读预检完成：下一最小依赖不是直接写 observation response 或 executor，而是先冻结
    platform-neutral `BackendKind` + `FirewallCapabilities` 编译权威。拟议范围仅含 immutable domain value、
    closed enum、fail-closed validation、资源上限与专项测试；不修改 Backend interface，不含 ManagedState/
    ForeignContext、IPC、真实 Firewall 或 runtime composition。该跨模块导出契约等待用户 Ask First 明确确认；
    当前 `IN_PROGRESS / Specified`，实现 `NO-GO`，B4/G18/M0 不提升。
34. 用户已明确确认并完成 B4-l7 production/test/checkpoint，当前 `REVIEW / Implemented`。三个 backend
    kind、immutable capability value、bounded tool version 与组合不变量已冻结；targeted Race count=20、全仓
    normal/Race/Vet/module、135 包依赖闭包及三目标 CGo-free compile 均通过。独立 checkpoint/test delta
    closure 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。下一门为 final
    FULL_SCOPE/INTEGRATION 初审无 P0/P1；唯一 iptables native-set/atomic-batch 反向独立性测试 P2 已修复并
    重跑受影响验证/三目标构建，final delta closure 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH /
    PASSED`、P0-P3 全无。下一门仅为用户 Code Review；B4/G18/M0 不提升。
35. 用户已明确回复 `B4-l7 Code Review 通过`，本 Delivery Unit 当前为 `DONE / Implemented`，用户门为
    `PASSED`。仅关闭 B4-l7，不描述为 Verified；Backend interface、observation wire、真实 Firewall/runtime
    与上级 B4/G18/M0 边界不变。下一任务须重新做只读候选预检与 Ask First 判断。
36. B4-l8 只读预检完成，当前 `IN_PROGRESS / Specified`。下一最小批仅为 ProbeCapabilities success
    Schema + security golden/semantic tests；不新增 exported Go API。拟冻结 nested payload 的 15 字段、
    4 KiB/depth 2/token 64 与 B4-l7 同构不变量。该 wire contract 等待用户 Ask First；立即实现 `NO-GO`。
    Probe failure、DTO/codec/frame/client、Snapshot、Backend/executor/真实 Firewall 均另批，B4/G18/M0 不提升。
37. 用户已明确确认并完成 B4-l8 Schema/golden/checkpoint，当前 `REVIEW / Implemented`。success-only
    nested payload、15 字段 production constructor mapping、closed backend/tool version/boolean contract、
    4 valid + 21 invalid、安全/资源/能力独立性与 fuzz 已冻结。targeted Race count=20、全仓 normal/Race/
    Vet/module、141 包依赖闭包和三目标 CGo-free compile 均通过；checkpoint 的 string-type P2 已修复，
    fresh delta closure 为 `COMPLETE / FRESH / PASSED / APPROVED`、P0-P3 全无。下一门仅为用户 Code Review；
    Probe failure、DTO/codec/frame/client、Snapshot、Backend/executor/真实 Firewall 与 B4/G18/M0 均不提升。
38. B4-l8 final FULL_SCOPE/INTEGRATION 为 `APPROVED_WITH_FOLLOWUPS / CHILD_AGENT / COMPLETE / FRESH /
    PASSED`，无 P0/P1/P3。fixture tree identity 与 untracked `git diff --check` 两个 Evidence-only P2 已修复：
    Evidence 记录完整 26-file tree hash/规范化算法，并以 `gofmt -d` 与 28 个 UTF-8 文本 final-LF/
    trailing-whitespace scan 替代无效覆盖声明。当前等待 record-only freshness closure；实现范围与上级 Gate 不变。
39. B4-l8 record-only freshness closure 已通过：最终 verdict 为 `APPROVED / CHILD_AGENT / COMPLETE /
    FRESH / PASSED`，P0-P3 全无；Schema/test/golden/ADR/build 未漂移。当前下一门仅为用户 Code Review，
    不得提前标为 DONE/Verified；B4/G18/M0 与未验证边界不变。
40. 用户已明确回复 `B4-l8 Code Review 通过`，本 Delivery Unit 当前为 `DONE / Implemented`，用户门为
    `PASSED`。仅关闭 B4-l8，不描述为 Verified；Probe failure、production observation codec/runtime、
    Backend/executor/真实 Firewall 与上级 B4/G18/M0 边界不变。下一任务须重新做只读候选预检。
41. 用户已明确回复 `确认 B4-l9`。当前一次整体实施 ProbeCapabilities failure Schema、typed response
    codec/frame、固定 request frame、Linux client、认证后 server adapter、真实临时 Unix socket E2E 与完整
    Evidence；不再按 DTO/frame/client 拆成多个用户交付批次。真实 Firewall Backend、Snapshot、通用 Enforcer
    runtime、配置/依赖/systemd 仍排除。当前 `IN_PROGRESS / Specified`，完成前不提升 B4/G18/M0。
42. B4-l9 整体实现与验证完成，当前 `REVIEW / Implemented`。专项 Windows/Linux Race、WSL2 real socket
    count=100、三项 fuzz、全仓 normal/Race/Vet/module、145 包闭包与六个三目标 CGo-free test builds 均通过。
    独立 TEST-QUALITY 的 partial-write cancellation P1 已修复并 fresh closure P0-P3 全无；final
    FULL_SCOPE/INTEGRATION 的两轮 Evidence-only P2 均已修复，最终 record-only closure 为
    `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。当前下一门仅为用户 Code Review；
    真实 Firewall/runtime/CI/commit-bound 与 B4/G18/M0 边界不变。
43. 用户要求提交代码；已按逻辑范围创建本地提交 `4642cfb`（GORM UnitOfWork）与 `04546f3`
    （typed Firewall IPC/Probe transport）。提交后标准 worktree clean，`master` 相对 `origin/master` ahead 3；
    未推送。B4-l9 仍为 `REVIEW / Implemented`，下一门仅为用户 Code Review。
44. B4-l10 SnapshotManaged 完整功能单元已实现并通过完整验证与独立终审，用户已明确回复
    `B4-l10 Code Review 通过`，当前为 `DONE / Implemented`，用户门为 `PASSED`。仅关闭 B4-l10，
    不描述为 `Verified`；B4-l9、B4/G18/M0 与真实 Firewall/runtime 边界不变。
45. B4-l11 只读预检完成，当前为 `IN_PROGRESS / Specified`。下一最小批拟仅新增 Linux-only
    authenticated mutation single-request server adapter：认证/解码后才调用 closed typed handler，响应在写入前
    强制 operation/domain correlation，沿用 context-only deadline、单连接、最多一帧与完整 frame delivery point。
    新导出 API 等待用户 Ask First 明确确认；当前实现 `NO-GO`、验证 `NOT RUN`。
    通用 accept loop、并发/优雅停机、Backend executor/provider、Plan/result mapping、Probe-first、真实 Firewall、
    配置/依赖/数据库/systemd/runtime composition 均另批，B4/G18/M0 不提升。
46. 用户已明确回复 `确认 B4-l11`。authenticated mutation single-request server adapter 已实现并通过 targeted
    Docker Race `count=20`、全仓 normal/Race/Vet/module、WSL2 两组 `count=20`、三目标 CGo-free compile 与
    独立 Tier 3 FULL_SCOPE 终审，最终 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。
    当前为 `REVIEW / Implemented`，下一门仅为用户 Code Review；B4-l9 仍独立保持 `REVIEW / Implemented`，
    B4/G18/M0 与真实 Firewall/runtime 边界不变。
47. 用户已明确回复 `B4-l11 Code Review 通过`，本 Delivery Unit 当前为 `DONE / Implemented`，用户门为
    `PASSED`。冻结代码身份未漂移，本次仅同步验收记录，未重跑代码验证；B4-l9 仍独立保持
    `REVIEW / Implemented`，B4/G18/M0 与真实 Firewall/runtime 边界不变。下一任务须重新做只读候选预检。
48. 用户明确要求先提交代码；B4-l10 与 B4-l11 已分别创建本地提交 `9e2925a`、`0edeaad`。提交前全仓
    normal tests、Vet、module verify、gofmt/diff-check 与 credential-value scan 均通过；未推送。B4-l9、
    B4/G18/M0 与生产排除边界不变。
49. B4-l12 只读预检完成。下一最短完整单元拟先冻结 production-neutral closed mutation plan/result authority，
    并增加 privileged pure authorization 与 IPC 双向 mapper；直接进入 Backend/provider、executor/serve loop 或
    真实 Firewall 均 `NO-GO`。该批新增跨包导出契约并冻结权限语义，当前等待 Ask First 明确确认；验证
    `NOT RUN`，未提交、未推送，B4-l9、B4/G18/M0 不提升。
50. 用户明确回复 `确认 B4-l12`。本批 closed mutation authority、pure authorization 与 IPC mapper 已实现并完成
    targeted/full normal/Race/Vet/module、三目标六项 CGo-free test-compile 和独立 Tier 3 repair round 1 closure；
    当前为 `REVIEW / Implemented`，下一门仅为用户 Code Review。fresh acquisition、Backend/provider、handler/
    executor/serve loop、真实 Firewall 与 B4/G18/M0 Gate 不提升；未提交、未推送。
51. 用户明确回复 `通过，继续下一步`，B4-l12 用户 Code Review 门更新为 `PASSED`，Delivery Unit 更新为
    `DONE / Implemented`。四个冻结 Go 文件身份未漂移，本次仅同步验收记录，未重跑代码验证；下一步只做
    dependency-satisfied candidate 的只读预检，不将“继续”视为新边界实施授权。B4-l9、B4/G18/M0 不提升。
52. B4-l13 authenticated single-request mutation executor 只读预检完成，当前为
    `IN_PROGRESS / Specified`。下一最小完整单元拟在 `internal/enforcer` 新增消费侧最窄
    `MutationBackend` port 与串行单请求 executor：每次 attempt 在同一 backend/context/临界区内严格执行
    `Probe → Snapshot → Authorize → Apply/Immediate`，不缓存 observation、不重试；Apply 一旦进入而结果
    缺失、非法或关联失败，必须按原 authority 返回 correlated `Unknown`。本批新增内部导出 runtime/security
    contract，等待 Ask First 明确确认；实施 `NO-GO`、验证 `NOT RUN`。完整 Firewall Backend、真实
    nftables/iptables、accept loop、systemd/executable、配置/依赖/数据库及上级 Gate 均不在本批。
53. 用户明确回复 `确认 B4-l13`。消费侧最窄 `MutationBackend`、串行 `MutationExecutor` 与 scripted
    backend oracle 已实现：无法关联的 request/pre-cancel 在 Probe 前停止，valid-but-not-ready capability 在
    Snapshot 前停止，排队请求可按自身 context 及时取消；每个 request 在同一 backend/context/context-aware
    单槽 gate 内 fresh 执行
    `Probe → Snapshot → Authorize → Apply/Remove/Immediate`，mutation 最多一次，非法或不关联结果转为
    correlated `Unknown`。targeted Windows/Docker Linux Race `count=20`、全仓 normal/Race/Vet/module、
    三目标六项 CGo-free test-compile 均通过。独立 Tier 3 检查点的 full-attempt 串行 oracle P1 与 typed-nil
    request P2 已在 repair round 1 修复；final FULL_SCOPE 的 context-unaware admission P1 与摘要 P2 也在
    repair round 1 闭合，最终 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。当前为
    `REVIEW / Implemented`，下一门仅为用户 Code Review。真实 Backend/Firewall、handler 接线、serve loop、
    systemd/runtime 与 B4/G18/M0 均不提升。
54. 用户明确回复 `通过，继续`，B4-l13 用户 Code Review 门更新为 `PASSED`，Delivery Unit 更新为
    `DONE / Implemented`。两个冻结 Go 文件身份与 final record-only closure 一致，本次仅同步验收记录，
    未重跑代码验证；下一步只做 dependency-satisfied candidate 的只读预检，不将“继续”视为新 runtime、
    Firewall 或系统边界实施授权。B4-l9、B4/G18/M0 不提升。
55. B4-l14 unified authenticated single-connection Enforcer router 只读预检完成，当前为
    `IN_PROGRESS / Specified`。Probe、Snapshot、Mutation 共用 `/run/guard/enforcer.sock`，因此 mutation-only
    loop 会误拒绝合法 Probe/Snapshot；下一最小完整单元应先新增 closed `EnforcerHandlers` 与
    `(*UnixListener).ServeEnforcerOnce`，一次 Accept/认证/解码后精确路由四个 operation，最多调用一个 typed
    handler、写一帧并关闭 accepted connection。本批不含持续 loop、错误恢复策略、listener Close ownership、
    timeout policy 或真实 Backend。Router 会直接消费 B4-l9 Probe transport；其用户 Code Review 已明确通过，
    依赖门已解除，但新增导出 IPC router/security contract 仍需单独 `确认 B4-l14`。当前实施 `NO-GO`、验证
    `NOT RUN`，B4/G18/M0 不提升。
56. 用户明确回复 `B4-l9 Code Review 通过`。B4-l9 用户门更新为 `PASSED`，Delivery Unit 更新为
    `DONE / Implemented`；实现身份继续由本地提交 `04546f3` 绑定。本次仅同步验收记录，未重跑代码验证、
    未新增提交、未推送。B4-l14 由 `BLOCKED / Specified` 转为 `IN_PROGRESS / Specified`，实施仍为
    `NO-GO`、验证 `NOT RUN`，等待独立 `确认 B4-l14`；B4/G18/M0 不提升。
57. 用户明确回复 `确认 B4-l14`。closed `EnforcerHandlers` 与 Linux-only `ServeEnforcerOnce` 已实现：
    handler bundle 在 Accept 前完整校验，一次认证/解码后按 concrete closed request 精确路由 Probe、Snapshot、
    Apply/Remove，mutation correlation 与全部 encode 在首字节前完成，最多一帧并关闭 accepted connection，
    listener 由 caller 持有。WSL2 targeted `count=20`、Docker targeted Race `count=20`、全仓 normal/Race/Vet/
    module、Docker full IPC Race、三目标 CGo-free test-compile 与三路独立 Tier 3 checkpoint 均通过，P0-P3
    全无。最终 PARTITIONED_PLUS_INTEGRATION 初审仅有一个 Evidence replay identity P2；补齐 WSL binary
    SHA256/exact host recipe、Docker image/mount/network recipe 与三目标 exact cross-build 后，repair round 1
    fresh-delta 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无。当前
    `REVIEW / Implemented`，等待用户 Code Review；持续 runtime、真实 Backend/Firewall、systemd/executable
    与 B4/G18/M0 不提升，未提交、未推送。
58. 用户明确回复 `B4-l14 Code Review 通过`。B4-l14 用户门更新为 `PASSED`，Delivery Unit 更新为
    `DONE / Implemented`；两个冻结 Go 文件身份未漂移，既有验证与独立审查结论继续有效。本次仅同步验收记录，
    未重跑代码验证、未提交、未推送。下一任务须重新做 dependency-satisfied candidate 只读预检；不自动授权
    新的 runtime、Firewall 或系统边界实施。B4/G18/M0 不提升。
59. B4-l15 persistent unified Enforcer serve loop 只读预检完成，当前为 `IN_PROGRESS / Specified`。
    B4-k listener/peer gate 与 B4-l14 unified router 已满足依赖；handler composition 仍缺真实 Probe/Snapshot
    provider 与 Firewall Backend，systemd/executable 又依赖完整 composition，因此下一最短完整单元是 injected
    handlers 上的串行持续 loop。拟新增 `EnforcerServeOptions` 与 Linux-only `ServeEnforcer`：idle Accept 只受
    parent context；每个连接在 raw Accept 成功后、SO_PEERCRED 前启动有限正值 request timeout，覆盖认证、decode、
    handler、encode 与 write。request-local failure 必须在关闭连接后同步、脱敏观察一次再继续；listener/credential/
    handler contract/invariant/未知错误均 fail-closed 返回，parent cancel/deadline 终止并保留 `errors.Is` 身份。
    listener 始终 caller-owned，既有 `ServeEnforcerOnce` 行为保持不变。本批不含真实 handler wiring、Backend/
    Firewall、并发连接、rate-limit/backoff、systemd/executable/config/deps/DB。新增导出 runtime/security contract
    命中 Ask First；当前实施 `NO-GO`、验证 `NOT RUN`，等待用户 `确认 B4-l15`，B4/G18/M0 不提升。
60. 用户明确回复 `确认 B4-l15`。新增 Linux-only `EnforcerServeOptions` 与 serial `ServeEnforcer`；idle Accept
    只受 parent context，per-connection timeout 从 raw Accept 后、SO_PEERCRED 前开始，覆盖完整 request lifecycle。
    closed classifier 将 peer mismatch、受支持 malformed/truncated input、validation、write failure 与 request
    deadline 作为 close-before-observer 的 request-local failure；listener/credential、handler contract/correlation、
    invariant 与 unknown failure 均终止循环。parent cancel/deadline 保留 `errors.Is`；listener caller-owned。
    首轮独立安全审查发现 loop-only owner 可被 one-shot Serve/`AcceptRequest` 绕过的 P1；repair round 2 使用共享
    atomic serve owner 覆盖五个高层 Serve 入口和低层 Accept admission，并用六入口 bounded table oracle 冻结
    fail-fast `ListenerErrorCodeAlreadyServing`、零 handler/observer 与取消后复用。repair-round 安全终审又发现
    handler panic 跳过普通 Close/cancel、先释放 owner 的 P1；repair round 3 使用 per-connection inner defer，
    并新增通道握手 recover oracle 证明 peer bounded close、child canceled、observer=0 与 owner reuse。WSL2 targeted `count=20`、
    Docker targeted Race `count=20`/full IPC Race、全仓 normal/Race/Vet/module、Linux amd64/arm64 CGo-free
    test-compile 与格式检查均通过。三路 repair round 3 fresh 终审均 PASSED；Evidence replay 与 B4 summary
    records P2 修复后的 fresh-delta 为 `APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`。TEST-QUALITY
    保留一个非阻断 100 ms serial oracle P2。当前 `REVIEW / Implemented`，等待用户 Code Review；
    未提交、未推送。真实 Backend/Firewall、handler composition、并发/rate-limit/backoff、systemd/executable/
    config/deps/DB、生产 `/run/guard` 与 B4/G18/M0 不提升。
61. 用户明确回复 `B4-l15 Code Review 通过`。B4-l15 用户门更新为 `PASSED`，Delivery Unit 更新为
    `DONE / Implemented`；九个冻结 Go 文件 SHA256 未漂移，既有验证、repair round 2/3、三路 fresh 终审与
    records fresh-delta 结论继续有效。本次仅同步验收记录，代码验证未重跑、未提交、未推送。下一动作等待
    用户明确继续后再做 dependency-satisfied candidate 只读预检；B4/G18/M0 不提升。
62. 用户明确回复 `B4-l16 Code Review 通过`。B4-l16 用户门更新为 `PASSED`，Delivery Unit 更新为
    `DONE / Implemented`；两个冻结 Linux Go 文件 SHA256 未漂移，既有 WSL2/Docker Race、Windows 全仓、Linux
    双架构编译/Vet 与三路 fresh-delta 终审结论继续有效。本次仅同步验收记录，代码验证未重跑、未提交、未推送。
    随后按用户“继续”的范围完成下一候选只读预检：B4-l17 拟只新增注入式 Enforcer 生命周期/组合 owner，确保
    单一 backend 只构造一套共享 gate 的 handlers，并在既有 `ServeEnforcer` 返回后有序关闭 caller-injected
    listener；真实 Firewall/provider、生产 `/run/guard`、UID/GID、systemd/executable、配置、依赖、数据库与
    部署均排除。该新增导出 runtime/lifecycle contract 命中 Ask First，实施 `NO-GO`、验证 `NOT RUN`，等待
    用户 `确认 B4-l17`；B4/G18/M0 不提升。
63. 用户明确回复 `确认 B4-l17`。新增 Linux-only `EnforcerRuntime`/`NewEnforcerRuntime`，构造期恰好一次
    生成 closed handler set 并接管 injected listener；`Run` 委托既有 `ServeEnforcer`，终止返回或 panic 后关闭
    listener，保留 serve/Close 的错误身份。runtime atomic state 阻断重复/并发 Run；typed `already_serving`
    外部占用不关闭、可重试且不重构造 handlers。初审发现一次构造与 Close-only error 两项 P1、observer forwarding
    一项 P2；factory seam 和 deterministic oracles 已闭合，repair round 1 后两路独立 fresh-delta 均 P0-P3 全无。
    Windows targeted/full、Linux amd64/arm64 CGo-free compile/Vet、WSL2 `count=20`、Docker targeted/changed-package
    Race、全仓 normal/Race/Vet/module 均通过。当前 `REVIEW / Implemented`，等待用户 Code Review；真实 provider/
    Firewall、生产 `/run/guard`、UID/GID、systemd/executable、配置、依赖、数据库、部署与 B4/G18/M0 Gate 不提升。
64. 用户明确回复 `B4-l17 Code Review 通过`。B4-l17 用户门更新为 `PASSED`，Delivery Unit 更新为
    `DONE / Implemented`；两个冻结 Linux Go 文件 SHA256 未漂移，既有 Windows/WSL2/Docker/全仓、repair round 1
    与两路 fresh-delta 终审结论继续有效。本次仅同步验收记录，代码验证未重跑、未提交、未推送；B4/G18/M0 不提升。
65. 用户回复“继续”后，B4-l18 只读预检已完成，当前 `IN_PROGRESS / Specified`。下一最小单元仅为 Linux native
    `/run/guard` root:guard 跨 UID Enforcer runtime 集成证据：复用 `ListenUnix`、`NewEnforcerRuntime` 与
    test-only closed `MutationBackend`，在受控 WSL fixture 中验证 socket owner/mode、`guard` 的请求准入、非预期
    UID 在 `SO_PEERCRED` 前拒绝、backend 零调用与 cancel 后精确 cleanup。该批不新增 Go API/IPC wire/schema，
    不接真实 Firewall/provider、systemd/executable、配置/依赖/数据库或部署。因需短暂创建并清理 root 级
    `/run/guard` fixture 与临时 `guard` 用户/组，实施 `NO-GO`、验证 `NOT RUN`，等待用户 `确认 B4-l18`；B4/G18/M0
    不提升。
66. 用户明确回复 `确认 B4-l18`。新增 `linux && integration` 的 Enforcer runtime 跨 UID 测试：在受控 Ubuntu
    WSL root fixture 中使用 `ListenUnix → NewEnforcerRuntime` 与 test-only closed backend，验证 `/run/guard`
    与 socket 为 root:guard `0750/0660`，root 请求被 `SO_PEERCRED` 拒绝且四类 backend 调用均为零，`guard`
    子进程仅完成一次 Probe，cancel 后 runtime 保留 `context.Canceled` 身份并按 socket identity 清理。修复
    listener 创建后、属性断言前的 cleanup 空窗，并接受 peer-close 产生的受控 `truncated_length/write_failed`
    客户端差异。Windows targeted/full Race、全仓 normal/Race/Vet/module、Linux integration amd64/arm64
    CGo-free compile/Vet 和 WSL2 `count=20` 均通过；WSL native Go 工具链未安装，Linux `-race` 集成验证为
    `UNAVAILABLE`。代码、记录与交叉集成三路独立终审均 `COMPLETE / FRESH / PASSED`、P0-P3 全无。当前
    `REVIEW / Implemented`，等待用户 Code Review；真实 Firewall/provider、systemd/
    executable、配置、依赖、数据库、部署、CI 与 B4/G18/M0 Gate 不提升。
67. 用户确认 B4-l19 并明确允许联网后，既有 B4-l18 cross-UID integration test 已在官方 Go 1.27 Linux/amd64
    构建阶段完成 module verify 与 Race test-binary 编译；实际执行在独立 `--network none`、`--rm`、只挂载
    fixture script 的 Docker 容器内以 `-race` / `count=20` 通过。root:guard `0750/0660`、root peer 拒绝且
    backend 零调用、guard 单次 Probe、`context.Canceled` 与 socket cleanup 均由冻结测试覆盖。当前 B4-l19
    为 `REVIEW / Implemented`、用户 Code Review `PENDING`；B4-l18 的用户门也仍独立 `PENDING`。此证据不是
    WSL native Linux、目标发行版、CI 或 commit-bound 结论。records 与交叉集成独立终审均
    `COMPLETE / FRESH / PASSED`、P0-P3 全无；B4/G18/M0 不提升。
