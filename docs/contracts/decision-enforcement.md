# Guard Phase 1 — Decision / Enforcement / Reconcile Contract

> M0-A3 责任产物。本文用于把主 Contract 中的 Decision、Projection、
> Enforcement 与 Reconcile 语义整理为可实现、可评审的单轨模型。

## 1. 状态与权威边界

| 项目 | 当前值 |
|---|---|
| 适用范围 | Phase 1 Standalone Agent，M0-A3 |
| 文档成熟度 | `Specified` 目标；实时推进与证据状态见 Phase 1 STATUS |
| 规范权威 | [Phase 1 Contract](guard-phase-1-m0-contract-freeze-v0.3.md) §4、§10–12、§17.2–17.3 |
| 存储权威 | M0-D migration SQL，当前尚未落盘 |
| 行为权威 | M0-D Contract Tests，当前尚未落盘 |
| 预期证据 | `artifacts/evidence/M0/<commit>/m0-a/enforcement-review.*` |

本文不复制完整 DDL、Go struct、Backend 命令或全部测试步骤。若本文与主 Contract
冲突，以主 Contract 为准；实现代码、migration 和测试落盘后，还必须通过一致性检查，
不得仅凭本文把 A3 标记为 `Verified`。

## 2. 单轨职责模型

```text
Active Decisions                         Policy / Config Facts
  唯一安全意图事实源                      Allowlist / Protected Targets /
        │                                 scope / infrastructure schema
        ↓                                           │
Desired Ban Projection                              │
  可由 Active Decisions 重建                        │
        └──────────────────┬────────────────────────┘
                           ↓
             Normalized Target Enforcement Intent
                           +
             Managed Infrastructure / Policy Intent
                           ↓
               Desired Firewall Snapshot
                           ↓
               Reconcile Plan + Fencing
                           ↓
                    Firewall Backend
                           ↓
              Probe / Observed Firewall Snapshot
```

必须保持以下职责隔离：

1. Decision 只表示“为什么、应该发生什么”，不表示 Firewall 是否执行成功。
2. Desired Ban Projection 是安全意图的物化投影，不是最终 Firewall 规则。
3. Policy Resolver 把 Projection 与 Policy/Config 合成为最终 Target Intent。
4. Reconciler 只根据完整 Desired Snapshot 和权威 Probe/Snapshot 收敛外部状态。
5. Firewall 错误只能改变对应 Reconcile failure domain，禁止改变 Decision state。
6. DB Observed State 只是最近一次已确认观察的缓存，不能替代 Firewall Probe。

## 3. Decision Contract

### 3.1 状态与终止原因

Phase 1 只允许：

| State | EndReason | 约束 |
|---|---|---|
| `Active` | `NULL` | 唯一可参与 Desired Ban Projection 的状态 |
| `Expired` | `expired` | 只能由到期路径产生 |
| `Revoked` | `manual` | 显式人工撤销 |
| `Revoked` | `manual_replace` | 只适用于 Manual Decision |
| `Revoked` | `rule_disabled` | 只适用于 Automatic Decision |
| `Revoked` | `system_cleanup` | 只适用于明确的系统清理流程 |

只允许 `Active → Expired` 或 `Active → Revoked`。终态不可复活；重复终止必须
幂等无变更，或返回稳定冲突错误，禁止覆盖原 EndReason。

以下状态不属于 Decision：

```text
Pending / Applying / RetryWaiting / Converged / Degraded
```

它们只属于 Reconcile failure domain。`Failed`、`Revoking`、Decision 级 Retry 和
`guard-agent decision retry` 均不进入 Phase 1 实现。

### 3.2 身份与唯一性

业务身份冻结为：

| 来源 | Active 唯一键 | 重复行为 |
|---|---|---|
| Automatic | `node_id + rule_id + canonical_target` | 原子更新 suppression，不延长到期时间 |
| Manual | `node_id + canonical_target` | 默认返回 `AlreadyBanned` |

唯一性必须由 SQLite partial unique index 保证，不能只做应用层“先查后插”。

- Automatic Action 在 Phase 1 固定为 `ban`，不进入唯一键。
- RuleVersion 不进入 Automatic 唯一键，Rule 升级不能绕过“不自动续期”。
- Scope 是派生执行属性，不是 Decision 身份。
- Automatic 与 Manual 可以对同一 Target 同时 Active。
- Automatic 重复命中只更新 `last_triggered_at` 和 `suppressed_alert_count`。
- Manual 只有显式 replace 才能在同一事务中终止旧 Decision、写 Critical Audit、
  创建新 Decision。

### 3.3 创建、终止与到期

- Automatic Decision 必须在 Processing Coordinator 管理的 outcome UnitOfWork 中创建；
  Decision、Projection、Critical Audit 和 processing receipt 共同成功或回滚。
- Manual 创建、replace、撤销必须通过应用服务开启的短事务完成，CLI/Web 禁止直写 DB。
- `ExpiresAt` 在 Decision 创建时计算，不以首次 Firewall Apply 成功时间为基准。
- 首次 Apply 前已经到期的 Decision 不得再产生 Ban tightening。
- 正常运行期到期必须在同一事务中终止 Decision、写 Critical Audit、重算 Projection，
  再按规范化 Intent 是否变化决定 generation/revision。
- Maintenance 只暂停 Firewall mutation，不暂停 Decision 到期和 Desired 更新。

M0 Fake Slice 的到期验收使用可注入时钟：健康 Fake Backend 从 `ExpiresAt` 到实际
Firewall Snapshot 为 `Absent` 的时间不得超过 62 秒虚拟时间。真实 Backend 的上限在
M6 以“expiration detection lag + dispatch lag + 一次 operation timeout”分别验证。

### 3.4 Allowlist 是正交 Policy

Allowlist 默认只抑制实际 Firewall 副作用，不终止 Active Decision，也不抹除 Detection、
Alert 或攻击事实。只有 Rule 显式配置 `exclude.source.allowlisted=true` 时，才在检测阶段
跳过该来源。

Policy Resolver 必须保留 CIDR 范围关系：

```text
None / Partial / Full
```

Partial overlap 不能删除整个 Ban Target。例如 `/32` Allowlist 不得撤销重叠的 `/24`
Manual Decision；Firewall Snapshot 必须同时表达 blacklist range 和更高优先级 Policy。

## 4. Projection 与版本分层

### 4.1 Desired Ban Projection

同一 canonical Target 的 Active Decisions 聚合为一个 Projection：

- Active count 大于零时为 `Present`，否则为 `Absent`。
- 全部 Decision 都有限期时，`EffectiveUntil` 取最大值。
- 任一 Decision 为永久时，`EffectiveUntil=nil`。
- Projection 必须能从 Active Decisions 全量重建。

Projection 的 Decision 数量、ID 列表等解释性字段变化，不必然表示 Firewall Intent 变化。

### 4.2 Normalized Target Enforcement Intent

Policy Resolver 至少以这些 Firewall 有意义的属性判定 Target Intent 是否变化：

```text
CanonicalTarget
BanMembership
EffectiveUntil
TimeoutMode
Scopes
AddressFamily
PolicyCoverage
PolicyRelationDigest
Backend-relevant target attributes
```

支持 native timeout 的 Backend 对有限期 Present Target 使用
`EffectiveUntil + SafetyGrace`。它只是 Agent crash 时的 failsafe；Decision/Projection
仍是到期权威。永久 Target 必须确认 `TimeoutMode=none`。

### 4.3 revision / generation

| 名称 | 所属事实 | 递增条件 | 主要用途 |
|---|---|---|---|
| `TargetProjectionRevision` | Target 的 Decision/Projection 构成 | Projection 解释性或实质内容变化 | 证明 Projection 来自当前 Active Decisions |
| `TargetEnforcementGeneration` | Target 最终规范化 Firewall Intent | 实际执行语义变化 | Target fencing 与 Retry budget key |
| `PolicyRevision` | Allowlist、Protected Targets 等 Policy | Policy 事实变化 | Policy fencing 与 Retry key |
| `InfrastructureRevision` | Backend schema、hook、scope 等基础设施 | Infrastructure Intent 变化 | Infrastructure fencing 与 Retry key |
| `SnapshotRevision` | 完整 Desired Firewall Snapshot | 外部期望内容变化 | full snapshot/infrastructure/policy fencing |

强制规则：

- 仅 DecisionCount 改变但最终 Target Intent 不变时，只递增 Projection revision。
- Policy 变化与某 Target 不相交时，不递增该 Target generation。
- Policy Full/Partial overlap 改变 Target 的 coverage/digest 时，递增该 Target generation。
- Retry budget 绑定执行 generation/revision，禁止绑定解释性 Projection revision。

## 5. Desired、Observed 与确认

Desired Firewall Snapshot 必须完整包含：

```text
Managed Infrastructure Intent
Managed Policy Intent
Normalized Target Enforcement Intent[]
```

Observed Snapshot 必须分别表达 Infrastructure、Policy 和 Target 物理状态。Backend
只返回物理状态与 Guard owner marker，不能声称观察到了应用层 generation。

`ConfirmedTargetEnforcementGeneration` 只能由 Reconciler 写入：它必须把最新权威
Probe/Snapshot 与当前 Desired Intent 的全部相关属性比较，并在回写时再次通过 fencing。
该字段表示“已证实物理状态匹配此 generation”，不表示 generation 存在于 Firewall。

以下规则不可弱化：

- operation timeout、连接中断或结果不确定时，Observed 必须为 `Unknown`。
- Unknown 后再次 mutation 前必须先 Probe。
- Converged 必须比较 membership、coverage/digest、timeout、expiry、scope、family 和 owner，
  不能只比较 Present/Absent。
- NativeExpiry 必须按 Backend 时间精度设置明确容差。
- foreign 对象永远不是 Guard Desired State 的可修改部分。

## 6. Reconcile Plan 与 TargetPrepared

每次 Reconcile 都从完整 Desired Snapshot 与新 Probe/Snapshot 构建 Plan，安全顺序为：

```text
Probe / Snapshot
  → Ensure Guard-owned Infrastructure
  → 增加或扩大 Allowlist / Protected Policy
  → Target tightening
  → 在替代保护已确认后执行 Target relaxation
  → 为待删除/缩小 Policy 的 Target 建立 TargetPrepared
  → 删除或缩小 Policy
  → Confirm Snapshot
```

其中：

- tightening 包括增加 Ban、延长 timeout、扩大 scope。
- relaxation 包括删除 Ban、缩短 timeout、缩小 scope。
- `/24 → /32` 必须先确认 `/32` 存在，再删除 `/24`；只有同一 Target 的原子替换
  才能合为一个 batch。
- `TargetPrepared` 是 Plan 内前置条件，不是新的持久化状态。它表示旧 Policy 尚在时，
  Target 的 membership、timeout、scope 和其他非 Policy 属性已经匹配新 Intent。
- Policy 删除/缩小必须等待全部受影响 TargetPrepared；删除后重新 Snapshot，只有
  PolicyRevision 与相关 Target generations 同时匹配才能 Converged。
- Infrastructure 未 Converged 时，禁止 Policy/Target 外部写。
- 保护性 Policy 未收敛时，禁止受影响 Target tightening。
- dependency blocked 不消耗 mutation attempt。

一个外部 batch 只能属于一个 failure domain；Phase 1 Target batch 只能包含一个
CanonicalTarget。Backend 无法可靠归属跨域错误时，禁止跨 domain 合批。

## 7. Failure Domain、Retry 与 Fencing

### 7.1 三个 failure domain

| Domain | 负责内容 | Retry key |
|---|---|---|
| Infrastructure | table、chain、hook/jump、owner/version、backend schema、INPUT/FORWARD infrastructure | `InfrastructureRevision + RetryEpoch` |
| Policy | allowlist、protected targets、managed policy objects | `PolicyRevision + RetryEpoch` |
| Target | ban membership、native timeout、per-target scope/attributes | `CanonicalTarget + TargetEnforcementGeneration + RetryEpoch` |

Infrastructure、Policy、Target 的 attempt、last error、next attempt 和 Degraded 状态必须
分别持久化。一个 domain 失败不得消耗另一个 domain 的预算。

### 7.2 Retry 状态机

```text
Pending
  → 在外部写前持久化 attempt 与 Applying
  → mutation
  → authoritative confirm
      ├─ match                         → Converged
      ├─ retryable 且预算剩余         → RetryWaiting
      └─ non-retryable 或预算耗尽     → Degraded
```

每个 domain key 有一次首次调用和五次自动重试，退避为
`1s / 5s / 30s / 5m / 15m`。

- crash、timeout 或 unknown result 都消耗已经持久化的 mutation attempt。
- Probe/Snapshot 不消耗 mutation attempt，但必须独立限时、退避和观测。
- 同一 key 的 restart、health flap、普通 Reconcile 不重置预算。
- revision/generation 变化形成新业务 key，才能从 attempt 0 开始。
- OwnershipConflict、不支持能力和非法 Plan 是 non-retryable。
- 管理员 Retry 只为指定 domain 创建新 RetryEpoch、归零 attempt，并写 Critical Audit；
  禁止直接修改 Decision。

管理员接口只能表达：

```text
reconcile retry infrastructure
reconcile retry policy
reconcile retry target <canonical-target>
```

### 7.3 Fencing 与并发

- 同一 Agent 只能有一个 Firewall mutation executor；planner 可以并发计算。
- Infrastructure 操作校验 InfrastructureRevision，必要时同时校验 SnapshotRevision。
- Policy 操作校验 PolicyRevision。
- Target 操作只以对应 TargetEnforcementGeneration 作为业务 fencing。
- 无关 Target 导致 SnapshotRevision 前进时，不得丢弃 generation 未变化且已确认的结果。
- stale completion 只能结束旧 attempt，禁止覆盖当前 Desired/Observed；随后必须 Probe，
  为当前 revision/generation 重建 Plan。

Reconcile Metrics 只能使用低基数 `domain/result/backend` 枚举；Target、DecisionID、
Retry key 和错误文本禁止成为 label。

## 8. 持久化顺序与崩溃恢复

### 8.1 Intent-first 原则

所有安全意图必须先在一个短 SQLite 事务中持久化，再触发 Firewall：

```text
Decision / Policy / Infrastructure change
  → 更新权威事实
  → 重算 Projection 与受影响 Target Intent
  → 递增必要 revision/generation
  → 写 Critical Audit
  → COMMIT
  → 构造 Desired Snapshot
  → Probe / Plan
  → 持久化 domain attempt
  → Firewall mutation
  → authoritative confirm + fenced observed write-back
```

禁止先改 Firewall 再写 Intent。系统只承诺 DB 内事务原子性、Backend 支持范围内的
Firewall operation 原子性，以及两者之间的最终一致性，不宣称跨系统 ACID。

### 8.2 启动恢复

启动恢复的依赖顺序固定为：

1. 验证 Runtime Config、单实例锁、DB、migration 和 durability 设置。
2. 加载并验证 SQLite domain config。
3. 在事务中终止已经到期的 Active Decisions，写 Audit 并重算 Projection/generation。
4. 从剩余 Active Decisions 校验或重建 Projection，组合完整 Desired Snapshot。
5. 读取持久化 Maintenance 状态。
6. Probe Backend。
7. Maintenance 时跳过 repair；Probe 成功时 Initial Reconcile；Probe 失败时把
   Enforcement 标为 NotReady/Degraded，但仍启动 Source、Detection 和管理面。

Backend 恢复只触发 Probe，不能因“重新健康”重置任何 retry budget。

### 8.3 Crash Point 结果不变量

| 故障窗口 | 恢复后必须成立 |
|---|---|
| Decision commit 前 | 没有新 Decision，也没有 Firewall 副作用 |
| Decision commit 后、Firewall 前 | 从 Desired/Policy/Config 恢复并继续收敛 |
| Firewall 成功后、Observed 回写前 | Probe 识别物理结果，以当前 generation fencing 确认或重算 |
| Decision 终止后、Revoke 前 | Desired 已变化，按新 generation 继续收敛 |
| Revoke 后、Observed 回写前 | Probe 识别 Absent 并确认 |
| attempt 持久化后、外部调用前 | attempt 不回退；先 Probe，再使用剩余预算 |
| timeout / unknown | Observed=Unknown；任何新 mutation 前先 Probe |
| 无关 Target 推进 SnapshotRevision | 不阻止 generation 未变化 Target 最终 Converged |

每个窗口都必须由自动故障注入覆盖，并断言最终 DB Desired、Firewall Snapshot、
Observed、Decision History、Retry Domain State 和 Audit。

## 9. 验证映射

| 不变量组 | 主 Contract 测试映射 | A3/M0 证据 |
|---|---|---|
| Decision identity、duplicate、manual replace | §17.2 #1–3、#23 | SQLite 并发测试、Decision Slice |
| 多 Decision Projection、永久/有限期合并 | §17.2 #4–5、#27 | Projection table tests、Fake Slice |
| Apply/Revoke 与 Decision 解耦 | §17.2 #6–8 | failure-domain tests |
| drift、ownership、unknown result | §17.2 #9、#14–15、#22、#24 | Fake/真实 Backend Snapshot tests |
| fencing 与无关 Target | §17.2 #10–11、#27 | 并发 completion tests |
| expiration 与 native timeout | §17.2 #7、#12–13、#25 | virtual clock tests |
| retry budget、health flap、管理员 Retry | §17.2 #16–18、#29–30 | retry ledger tests |
| Allowlist 正交与 CIDR overlap | §17.2 #19–21、#28 | Policy Resolver tests |
| 崩溃恢复最终状态 | §12.3、§17.3 | automated crash matrix |

所有 Evidence 必须记录 commit、精确命令、环境、passed/failed/skipped/not-run、
失败摘要和 reviewer。任何必测项为 `FAIL` 或 `NOT RUN` 时，A3 不得标记为 Verified。

## 10. 仍需 Spike / Slice 证明

以下项目已有唯一规范，但当前没有可执行证据：

1. SQLite partial unique index 在并发 Automatic 创建和 Manual replace 下的真实行为。
2. Decision、Projection、Critical Audit、receipt 任一子写失败时 UnitOfWork 整体回滚。
3. 可注入时钟下的 62 秒 Fake expiration 上限及到期 generation 判定。
4. Fake Backend 对三个 failure domain、unknown result、stale completion 和预算持久化的证明。
5. nftables Snapshot/Plan/atomic batch 是否能支持 domain 归属、TargetPrepared、
   `/24 → /32` 安全替换和权威 confirm。
6. foreign ownership conflict、Policy 删除失败、tightening 成功后 relaxation 失败时，
   系统不会产生保护空窗、循环等待或跨域扣减。
7. Agent/Enforcer restart 和 Backend health flap 不重置相同 revision/generation 的预算。
8. SQLite process-crash、reboot 和声明的 power-loss durability 边界。

Apply-confirm 的完整 Schema、跨重启回滚与真实高风险操作属于 M6 前置 Gate，不是
M0-A3 已验证能力；A3 只要求当前 Plan/Recovery 接口不阻止其后续实现。

## 11. A3 文档完成与后续验证义务

A3 文档满足以下条件后，可以从 `Specified` 更新为 `COMPLETE / Implemented`：

- Decision 状态、EndReason、Automatic/Manual identity 只有一套可编译模型。
- Projection、Normalized Intent 和五类 revision/generation 可由接口无歧义表达。
- TargetPrepared、安全 Plan、三 failure domain、Retry 和 fencing 没有实现级 TBD。
- A3 review 没有未解决 P0/P1，并形成可定位 Evidence。

下列项目是 A3 模型从 `Implemented` 升级为运行级 `Verified` 的后续义务，不能作为
C2 启动前要求 A3 已 Verified 的循环前置：

- M0-C Decision/Enforcement Slice 与 §17.2/§17.3 对应测试全部通过；
- D1 类型接口、D2 migration、D4 ADR、D5 Contract Tests 与本文一致；
- D6 已清除 V0.4 的旧 Decision 状态、Decision Retry CLI/Metric/ADR 语义。

在文档审查 Evidence 形成前保持 `Specified`；审查通过但运行证据未齐时只能是
`Implemented`，不得标记 `Verified`。
