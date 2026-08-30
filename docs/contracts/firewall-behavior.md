# Firewall Backend Behavior Contract

> 文档类型：M0-B3 Firewall Backend 子 Contract
>
> 规范成熟度：`Specified`；当前工作包与 Gate 状态只在
> [Phase 1 STATUS](../development/phase-1/STATUS.md) 维护
>
> 上位规范：[Phase 1 Contract](guard-phase-1-m0-contract-freeze-v0.3.md) §14、§28
>
> 初步证据：[nftables-result.json](../../artifacts/evidence/M0/worktree/m0-b/nftables-result.json)

## 1. 目的与权威边界

本文把上位 Contract 的 Firewall Backend 行为收敛为可实现、可审查、可验证的边界，
并记录当前 namespace Spike 实际证明了什么、尚未证明什么。

本文不重新定义：

- Desired/Observed、revision/generation、retry domain；以上位 Contract 第 4、11 节为准。
- Go struct、error type 和字段名；以 M0-D 编译通过的代码为权威。
- 物理 nftables/iptables 命令、production hook priority 或 jump position；必须由目标环境
  Spike 和 Golden State 冻结。
- Apply-confirm 的 Schema、状态机和 rollback journal；其为独立 M6/M10 前置 Gate。
- 当前负责人、进度和 Go/No-Go 结论；只在 `STATUS.md` 更新。

若本文与上位 Contract 冲突，以上位 Contract 为准，冲突阻塞 B3 验收。

## 2. Backend 职责与非职责

Backend 只负责把外部 Firewall 事实转换为能力、快照和受 ownership 约束的变更结果：

```text
Probe → Snapshot → Plan → Apply → Snapshot
                         └─ RemoveManagedInfrastructure（显式清理）
```

Backend 必须：

- 探测当前主机实际能力，不从配置或数据库猜测能力。
- 读取完整 Guard-owned 状态，并把相关 foreign 状态作为只读上下文返回。
- 从 canonical current/desired snapshot 产生单一 failure domain 的 OperationPlan。
- 只修改计划明确列出的 Guard-owned 对象。
- 返回可区分的 `Confirmed`、`Rejected`、`Unknown` 结果。
- 在 cancellation、timeout 和连接中断边界上保守报告真实不确定性。

Backend 不负责：

- 创建、到期或撤销 Decision；
- 构造业务 Desired Intent；
- 管理 Infrastructure/Policy/Target retry budget；
- 用 DB Observed 状态代替真实 Probe/Snapshot；
- 修改或“收养”foreign 对象；
- 通过一次性 `Add/Delete` 绕开声明式 Plan/Apply；
- 决定 Apply-confirm 是否通过或自行维护确认 deadline。

## 3. 冻结方法集

语义方法集固定为：

```text
Probe(ctx) -> FirewallCapabilities
Snapshot(ctx) -> ManagedState + read-only ForeignContext
Plan(current snapshot, desired snapshot) -> domain-scoped OperationPlan
Apply(ctx, one domain-scoped OperationPlan) -> ApplyResult
RemoveManagedInfrastructure(ctx, expected OwnerVersion) -> ApplyResult
```

具体 Go 类型由 M0-D 冻结，但不得改变以下边界：

- 所有可能阻塞的操作接收并传播 context cancellation。
- `Snapshot` 返回外部事实，不接受 DB 值作为“已存在”的替代证据。
- `Plan` 不执行 mutation；同一 canonical 输入必须产生等价计划。
- 一个 `OperationPlan` 只能属于 `Infrastructure`、`Policy` 或 `Target` 之一。
- `Apply` 一次只执行一个 domain-scoped plan，并把该 domain 和计划 digest 带入结果/evidence。
- 清理只能调用 `RemoveManagedInfrastructure`，不能用空 Desired Snapshot 暗中替代 purge 语义。

## 4. Probe

`Probe` 至少必须识别：

- Backend 类型、工具可用性和版本；
- IPv4/IPv6、INPUT/FORWARD、set、native timeout、atomic batch 等能力；
- 已知 UFW、Docker 或其他会影响真实 packet path 的集成条件；
- 是否能证明 Guard ownership 和安全 mutation 前提。

Probe 失败或拓扑未知时不得 mutation。无法证明 ownership 的自定义拓扑必须返回稳定的
unsupported/not-ready 原因；禁止降级为“尝试写入看看”。

## 5. Snapshot 与 drift

`Snapshot` 必须同时返回：

1. `ManagedState`：稳定命名且 owner/version 匹配的 Guard-owned table、chain、set、rule、hook/jump。
2. `ForeignContext`：规划或验证 packet path 所需、但 Backend 无权修改的外部对象。

Snapshot 必须：

- 保留足以判断 owner/version、hook、priority、jump position、set flags、timeout、IP family、
  INPUT/FORWARD scope 和 drift 的事实。
- 把同名但标记缺失或不匹配的对象报告为 `OwnershipConflict`，不得放入可修改 ManagedState。
- 对动态 handle、counter 和非稳定输出排序做 canonicalization，再用于 Plan、digest 和 fixture 比较。
- 在 Probe/Snapshot 失败时返回失败，不根据上一次 DB Observed 结果伪造当前状态。

Drift 是真实 Snapshot 与 Desired Snapshot 的差异。Backend 只报告事实并生成计划；Observed
状态写回、fencing 和 retry 由 Reconcile 层负责。

## 6. Plan

`Plan(current, desired)` 必须产生可审计、可重复计算的 operation list，且每个 mutation 都包含：

- failure domain：`Infrastructure`、`Policy` 或 `Target`；
- 目标 Guard-owned object identity 与 expected OwnerVersion；
- 前置条件和预期后置状态；
- 可用于日志、evidence 和 Apply-confirm 的稳定 plan digest 输入；
- 不应变化的 foreign context 摘要。

Plan 必须覆盖 Ensure、drift repair 和显式 cleanup，但不得：

- 混合多个 failure domain 后只返回一个不可归因结果；
- 计划删除 foreign 对象；
- 在 owner/version 冲突时生成接管或覆盖操作；
- 把 Allowlist 生成为无条件 `ACCEPT`；上位 Contract 要求其只表达 `RETURN` 语义；
- 因输出顺序、动态 handle 或 counter 变化制造虚假 drift。

## 7. Apply、atomicity 与结果语义

### 7.1 ApplyResult

| 结果 | 允许条件 | 后续动作 |
|---|---|---|
| `Confirmed` | Backend 已证实计划后置状态成立 | Reconcile 仍按 matching revision/generation fencing 写 Observed |
| `Rejected` | 明确未应用，或原子设施已证明整个 batch 回滚 | 记录对应 failure domain 失败；不得伪装部分成功 |
| `Unknown` | timeout、连接中断、进程中止或任何无法证明最终状态的情况 | 必须先重新 Probe/Snapshot，禁止盲目重复 Apply |

返回 `Confirmed` 不等于 DB Observed 已更新，也不绕过 Reconcile fencing。返回 `Unknown`
不等于失败；它表示外部状态只能重新读取确认。

### 7.2 Atomicity

- nftables-native 对一个 domain-scoped plan 必须使用内核支持的 atomic batch；任一 batch
  命令失败时，不得留下该 batch 的部分 mutation。
- 只有已证明“未发生 mutation”或“整个 atomic batch 已回滚”时才可返回 `Rejected`。
- 若 Backend 或命令序列不具备原子保证，任何中途失败都必须返回 `Unknown`，并通过
  Snapshot + reconcile 收敛；不得根据已执行命令数量猜测最终状态。
- M6 的 iptables-nft/legacy、ipset 和无 ipset fallback 必须分别冻结其能力降级、恢复与
  Golden State，不能从 nftables namespace Spike 外推 atomicity。

## 8. Ownership 与 foreign preservation

Backend 只能修改稳定命名且带正确 owner/version 标记的 Guard-owned 对象。

遇到同名对象但 owner/version 缺失或不匹配时，所有 Ensure、Sync、Apply、uninstall 和 purge
路径都必须：

1. 返回 `OwnershipConflict`；
2. 停止本次 mutation；
3. 保持冲突对象及其他 foreign 对象不变；
4. 由上层把 Infrastructure Domain 置为 Degraded。

`RemoveManagedInfrastructure(expected OwnerVersion)` 只允许删除匹配 owner/version 的
Guard-owned infrastructure。expected version 不匹配、ownership 无法证明或 Snapshot 失败时，
必须拒绝清理。普通 drift repair 禁止隐式扩大为 uninstall/purge。

Foreign preservation 必须通过 Apply 前后 canonical Snapshot 对比证明，而不是仅检查命令返回码。

## 9. production hook 与 priority 冻结规则

本文不指定任何数字 priority、chain jump position 或 UFW/Docker 相对位置。

production 值只有在以下证据齐全后才能进入对应 Backend 的 Golden State：

- 精确 OS image、kernel、Firewall 工具、UFW、Docker 版本与启用状态；
- 真实 packet path 的 INPUT/FORWARD、IPv4/IPv6 包测试；
- Allowlist-before-Ban 与 Guard-owned jump 的实际顺序；
- 与 foreign/UFW/Docker reload、restart、install/uninstall 的交互；
- 重复 Apply、drift repair 与 cleanup 后的 canonical Snapshot。

单一 WSL2 network namespace 结果不得外推为 Ubuntu LTS production priority，也不得替代
Docker published-port FORWARD 或 UFW reload 证据。

## 10. 当前 namespace Spike 的证据边界

证据文件声明：

```text
status: PASS_WITH_UNVERIFIED_DOMAINS
evidence_maturity: worktree_preliminary
environment: Ubuntu 22.04 on WSL2 / temporary network namespace
base_commit: 3aca38a
working_tree_dirty: true
```

### 10.1 已初步证明

仅对该 evidence 记录的环境和命令，以下检查为 `true`：

- Guard managed table 可以应用；
- 测试规则中 allow-before-ban 顺序成立；
- native timeout 语法可用；
- 失败 nftables batch 未留下部分变更；
- foreign table 保持不变；
- managed table cleanup 成功；
- 临时 namespace 已删除，host/default WSL namespace 未被修改。

这些结果是 capability/preliminary evidence，不冻结 production priority，不证明完整 Backend
接口、真实 packet path、跨重启恢复或全部 M6 Required 支持矩阵。

### 10.2 明确未验证

Evidence 明确列出：

- production hook and priority；
- INPUT/FORWARD 真实 packet path；
- process crash and restart recovery；
- same-name ownership conflict；
- Apply-confirm rollback。

此外，该 Spike 没有提供完整 `Probe → Snapshot → Plan → Apply → Snapshot` fixture、IPv6、
UFW/Docker、drift、重复 reinstall 或 timeout=`Unknown` 的可复核结果。

## 11. Required 验证矩阵

| 验证项 | M0-B3 要求 | 当前 evidence | 后续 Gate |
|---|---|---|---|
| Probe/capability report | 必须 | 部分：工具与环境已记录 | B3 / M6-WP1 |
| production hook/priority | 必须 | `NOT VERIFIED` | B3 |
| INPUT/FORWARD 真实 packet path | 必须 | `NOT VERIFIED` | B3 / M6-WP2 |
| IPv4/IPv6 scope | 必须 | `NOT VERIFIED` | M6-WP2 |
| Snapshot / canonical drift | 必须 | `NOT VERIFIED` | B3 / M6-WP3 |
| domain-scoped Plan 分类 | 必须 | `NOT VERIFIED` | B3 / M6-WP3 |
| nftables atomic batch | 必须 | namespace 初步通过 | B3；目标基线重验 |
| `Confirmed/Rejected/Unknown` | 必须 | `NOT VERIFIED` | B3 / M6-WP1 |
| repeated Apply/Ensure 幂等 | 必须 | `NOT VERIFIED` | B3 / M6-WP3 |
| foreign preservation | 必须 | 单个 foreign table 初步通过 | B3；Golden State 重验 |
| same-name OwnershipConflict | 必须 | `NOT VERIFIED` | B3 / M6-WP3 |
| owner-version cleanup | 必须 | 仅 managed cleanup 初步通过 | B3 / M6-WP3 |
| timeout/连接中断后先 Probe | 必须 | `NOT VERIFIED` | M6-WP3 |
| process crash/restart recovery | 必须 | `NOT VERIFIED` | M6-WP3 / C3 |
| UFW/Docker/iptables 支持矩阵 | M6 Release 必须 | `NOT VERIFIED` | M6-WP2 / Release Gate |
| Apply-confirm rollback | 高风险 mutation 前必须 | `NOT VERIFIED`，但不属于 B3 核心 Slice | 独立 M6/M10 前置 Gate |

所有写测试只能在 disposable VM、临时 network namespace 或专用测试环境执行，禁止在开发机
默认 namespace 或 production Firewall 上试验。Evidence 必须记录精确环境、命令、初始/预期/
实际状态、失败项、支持边界和 checksum。

## 12. Golden State 最低要求

每个 production Backend 进入 M6 实现前必须提供可执行 fixture，至少记录：

- 精确环境和 capabilities；
- object names、owner/version markers、hook/priority、jump position、set flags 和 timeout behavior；
- IPv4/IPv6、INPUT/FORWARD scope；
- initial managed snapshot 与只读 foreign context；
- desired snapshot 及 infrastructure/policy/per-target revision/generation；
- expected domain-scoped plan、ApplyResult、managed snapshot 和 unchanged foreign context；
- repeated-apply、drift repair、uninstall/cleanup 结果。

Fixture 比较前必须 canonicalize 动态 handle、counter 和输出排序。

## 13. B3 完成条件与剩余阻塞

B3 只有同时满足以下条件，其 evidence 才可标记 `Verified`：

1. 目标 nftables 基线上的 production hook/priority 与真实 INPUT/FORWARD packet path 已验证。
2. `Probe → Snapshot → Plan → Apply → Snapshot` fixture 完整，Plan 正确归类 failure domain。
3. atomic batch、重复 Apply、drift 和 `Confirmed/Rejected/Unknown` 行为通过故障注入。
4. same-name ownership conflict、foreign preservation 和 owner-version cleanup 均通过前后快照证明。
5. Evidence 记录精确环境、命令、commit、结果、限制与 checksum，并通过独立复核。
6. 具体 Go 接口未表达未经验证的 production priority 或超出证据的支持声明。

当前 B3 仍不能标记 `Verified`：现有 JSON 自身声明 `worktree_preliminary` 和
`PASS_WITH_UNVERIFIED_DOMAINS`，且缺少上述 hook/priority、真实 packet path、完整
Snapshot/Plan、ownership conflict、Unknown/recovery 与 Golden State 证据。Apply-confirm 仍未验证，
但它是独立的 M6/M10 高风险 mutation Gate，不应被伪装成 B3 已完成，也不应与 B3 核心 Slice 混为一项。
