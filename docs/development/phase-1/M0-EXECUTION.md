# Guard Phase 1 — M0 执行与 Gate 矩阵

本文件把 Contract 的 M0 要求聚合为四个可执行活动，不复制具体技术语义。
当前状态只在 [STATUS.md](STATUS.md) 更新；规范条款以
[Phase 1 Contract](../../contracts/guard-phase-1-m0-contract-freeze-v0.3.md)
为准。

## 1. 目标与完成定义

M0 的目标是把 Phase 1 核心语义从 `Specified` 推进到 `Verified/Frozen`，形成：

- 唯一且可编译的核心模型与接口；
- 可执行的 Source 和 Decision/Enforcement Fake Slice；
- 可复现的 SQLite、身份、权限和 nftables Spike；
- migration、Config Schema、ADR、Contract Tests；
- 第 18 节三组 Gate 的完整证据。

只有 G18.1、G18.2、G18.3 全部 `PASS`，M0 才能签署 `Frozen/GO`。

## 2. 依赖关系

```text
M0-A 行为不变量 ─┐
                  ├─→ C1/C2 Fake Slice ─→ M0-D ─→ M0 GO
B1/B2 风险 Spike ─┘

B3/B4 风险 Spike ─────────────────────────────↗ G18.1 / M0 GO

扩展 Crash Matrix ────────────────────────────→ M7/M10 Verification
```

- M0-A 与 M0-B 中互不依赖的工作可以并行。
- M0-C 必须等待其依赖的 M0-A 条款和 M0-B 风险项形成可执行结论。
- C1 只依赖 B1/B2，C2 只依赖 B1；B3/B4 不阻塞 C1/C2，但必须在
  G18.1 与 M0 GO 前完成。
- M0-D 只能冻结已经被 Spike 或 Fake Slice 验证过的接口。

## 3. M0-A — 行为不变量

| ID | 工作包 | Contract 锚点 | 责任产物 | 完成条件 | Evidence 模板 |
|---|---|---|---|---|---|
| A1 | Core Model 与权威关系 | §4、§7、§10–11 | `docs/contracts/core-model.md` | 模型职责、唯一权威源、身份和版本边界无实现级 TBD；一致性审查无 P0/P1 | `artifacts/evidence/M0/<commit>/m0-a/core-model-review.*` |
| A2 | Source delivery 与事务协调 | §6、§8–9、§22.2 | `docs/contracts/source-delivery.md` | SourceDurable、ProcessingComplete、checkpoint、重放与唯一 UnitOfWork 可由接口表达 | `artifacts/evidence/M0/<commit>/m0-a/source-contract-review.*` |
| A3 | Decision / Enforcement / Reconcile | §10–12 | `docs/contracts/decision-enforcement.md` | Decision、Projection、三个 failure domain、fencing 和安全顺序只有一套状态模型 | `artifacts/evidence/M0/<commit>/m0-a/enforcement-review.*` |
| A4 | M0 Crash/Recovery Evidence | §12.3、§17.3 | `tests/contracts/m0-process-recovery.yaml` | 每个 M0 用例都有测试 ID、注入点、读回范围和预期最终状态 | `artifacts/evidence/M0/<commit>/m0-a/crash-recovery.*` |

M0-A 判定要求：

- A1–A4 全部具备可复核产物和 Evidence。
- 不存在 receipt/durable inbox 双轨。
- 不存在 Decision 级 Retry 或旧 Decision 状态。
- 不存在阻塞 Fake Slice 的 TBD。

## 4. M0-B — 风险 Spike

| ID | 工作包 | Contract 锚点 | 责任产物 | 完成条件 | Evidence 模板 |
|---|---|---|---|---|---|
| B1 | SQLite 并发、事务与 durability | §10.5–10.6、§13 | Spike、migration 草案、测试 | 唯一约束、事务回滚、PRAGMA read-back 和声明故障域得到验证 | `artifacts/evidence/M0/<commit>/m0-b/sqlite/` |
| B2 | Source identity 与 replay | §6.2–7.4、§8 | golden vectors、轮转/restart Spike | Delivery/Event ID 跨进程与重启稳定，generation/replay 行为可复现 | `artifacts/evidence/M0/<commit>/m0-b/source-identity/` |
| B3 | nftables Backend | §14 | `docs/contracts/firewall-behavior.md`、隔离环境 Spike | hook/priority、atomic batch、Snapshot/Plan、ownership 和 foreign preservation 得到验证 | `artifacts/evidence/M0/<commit>/m0-b/nftables/` |
| B4 | Agent/Enforcer 权限与 IPC | §15.3 | 权限 ADR、IPC Spike | UID 校验、协议白名单、socket mode、systemd hardening 和非法请求拒绝得到验证 | `artifacts/evidence/M0/<commit>/m0-b/permission-ipc/` |

每个 Spike Evidence 必须记录：

- commit 与精确命令；
- OS、内核和相关工具版本；
- 初始状态、预期结果、实际结果；
- 失败项和已知限制；
- Supported、Unverified、Unsupported 边界；
- 产物 checksum。

nftables Spike 只能在隔离 VM、network namespace 或专用测试环境执行。

## 5. M0-C — 可执行 Fake Slice

| ID | 工作包 | 依赖 | Contract 锚点 | 完成条件 | Evidence 模板 |
|---|---|---|---|---|---|
| C1 | Source Slice | A1、A2、A4、B1、B2 | §17.1 | receipt pipeline 全部用例通过；事务失败、重放、乱序、shutdown 与 checkpoint 行为正确 | `artifacts/evidence/M0/<commit>/m0-c/source-slice/` |
| C2 | Decision/Enforcement Slice | A1、A3、A4、B1 | §17.2 | Fake Backend 用例通过；Decision、Projection、Retry domain 与 drift 状态正确 | `artifacts/evidence/M0/<commit>/m0-c/enforcement-slice/` |
| C3 | 扩展 Crash Matrix | C1、C2 | §12.4 | M7/M10 阶段覆盖全部故障点，并断言 DB、Firewall、Observed、History、Retry、Audit 最终状态 | `artifacts/evidence/M7-or-M10/<commit>/crash-matrix/` |

M0-C 判定要求：

- 测试命令可以重复执行，M0 process crash/reopen 用例无跳过。
- 不产生重复持久化副作用。
- checkpoint 不越过未持久化记录。
- Fake Firewall 的最终状态与 Desired/Observed Contract 一致。

## 6. M0-D — 正式冻结

| ID | 工作包 | 责任产物 | 完成条件 | Evidence 模板 |
|---|---|---|---|---|
| D1 | Go 类型与接口 | 编译通过的 Core/Source/Store/Firewall/Reconcile 接口 | 只表达已验证语义；compile、format、lint、race 通过 | `artifacts/evidence/M0/<commit>/m0-d/code/` |
| D2 | SQLite migration | `migrations/` | 空库迁移、重复启动、失败回滚、唯一约束和重启恢复通过 | `artifacts/evidence/M0/<commit>/m0-d/migration/` |
| D3 | Config Schema | `schema/config-v1.schema.*` | ownership、默认值、范围、未知字段拒绝和派生配置一致 | `artifacts/evidence/M0/<commit>/m0-d/config-schema/` |
| D4 | ADR | `docs/adr/` | 工程语言、delivery、M0 process recovery 边界、权限、Firewall 与 Retry 决策得到批准 | `artifacts/evidence/M0/<commit>/m0-d/adr-review.*` |
| D5 | Contract Tests | `tests/contracts/m0-process-recovery.yaml`、`scripts/test-m0-process-recovery.ps1` | §17 中 M0 范围的自动化验证全部通过 | `artifacts/evidence/M0/<commit>/m0-d/contracts/` |
| D6 | V0.4 同步 | 总体技术方案 V0.4 | 旧 Decision/Retry CLI/Metric/ADR 语义已清理，一致性检查通过 | `artifacts/evidence/M0/<commit>/m0-d/v0.4-sync.*` |
| D7 | Evidence Manifest | `artifacts/evidence/M0/<commit>/manifest.*` | 汇总命令、环境、结果、失败、checksum、限制和 reviewer | 同责任产物 |

## 7. Gate 执行矩阵

Gate 当前结果记录在 [STATUS.md](STATUS.md)。本表只定义判定输入和通过规则。

| Gate | Contract | 依赖工作包 | 必需证据 | PASS 规则 |
|---|---|---|---|---|
| G18.1 Contract 完整性 | §18.1 | A1–A4、B1–B4、D1、D4、D6 | Contract/ADR review、Spike、编译模型、一致性报告 | 单轨模型、权限、process recovery 边界、Retry、Backend 接口均 Verified；V0.4 已同步 |
| G18.2 可执行验证 | §18.2 | C1、C2、D2、D5 | 测试日志、进程级故障注入结果、clean target Evidence、环境信息、JUnit 或等价报告 | 两条 Slice、M0 crash/reopen、并发、重放、迁移、drift 与进程级 durability 必测项全部通过 |
| G18.3 产物一致性 | §18.3 | D1–D7 | drift check、checksum、Evidence Manifest | migration、Config Schema、代码、文档和测试无权威冲突 |

任一必测项为 `FAIL` 或 `NOT RUN` 时，Gate 必须保持 `FAIL`。

## 8. 推荐推进顺序

1. 并行启动 A1–A4 与 B1–B4。
2. A/B 形成首轮可验证接口后，分别判断 C1、C2 的依赖是否满足。
3. C1、C2 通过后完成 M0 范围的 process crash/reopen Evidence；扩展 Crash Matrix 留给 M7/M10。
4. 只把已经由 Spike/Slice 验证的接口纳入 D1–D5。
5. 同步完成 D6，生成 D7。
6. 逐项复核 G18.1、G18.2、G18.3。
7. 三项全部 `PASS` 后签署 M0 `Frozen/GO`。

## 9. NO-GO 边界

M0 Gate 未全部通过前：

允许：

- 推进 M0-A、M0-B；
- 创建 M0 必需的可编译骨架、Fake 工具和测试基础设施；
- 在隔离环境执行风险 Spike。

禁止：

- 宣称 M0 Frozen；
- 启动依赖未验证 Contract 的 M1–M10 正式实现；
- 宣称 Phase 1 已具备 Release 条件；
- 用“实现时决定”“后补测试”或人工 waiver 绕过 Gate。

## 10. GO 签署模板

只有三项 M0 Gate 全部 `PASS` 后，才可在 Evidence Manifest 中填写：

```text
Decision: GO | NO-GO
Contract Commit:
Evidence Manifest:
Reviewed By:
Reviewed At:
Open Limitations:
```
