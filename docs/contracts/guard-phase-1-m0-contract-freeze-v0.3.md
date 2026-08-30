# Guard Phase 1 M0 — Contract Freeze 技术规格 V0.3

---

## 1. 文档状态

| 项目 | 内容 |
|---|---|
| 状态 | M0 执行基线，尚未 Frozen |
| 适用范围 | Guard Phase 1 |
| 架构基线 | [Guard 分布式日志驱动主机防护系统技术方案 V0.4](../guard-distributed-log-driven-host-protection-system-technical-design-v0.4.md) |
| 目标 | 把架构语义下沉为可编译、可迁移、可故障注入验证的开发 Contract |

本文中的“必须”“禁止”“只能”是规范性要求。

只有第 18 节全部通过后，本文件状态才能从“M0 执行基线”改为
“Frozen”，Phase 1 才允许按里程碑拆分为并行开发任务。

### 1.1 与 V0.4 的权威关系

- M0 未 Frozen 时，V0.4 继续承担已发布的产品范围与总体架构基线；本文中的冲突项
  是待 Spike、Slice 和 ADR 验证的候选修正，只允许用于 M0 实验，不得当作已发布
  代码 Contract。
- M0 Frozen 后，对本文覆盖的 Source、SecurityEvent、Decision、Enforcement、
  Firewall、Resource Limits 和故障恢复 Contract，以 Frozen M0、对应 ADR 及可执行
  产物为实现权威。
- M0 Frozen 的同一变更必须同步修订 V0.4 中已过期的 Decision 状态、Retry CLI、
  Metrics 和 ADR。禁止长期保留两套权威语义。

至少需要同步处理：V0.4 的 `Pending/Revoking/Failed` Decision、Decision 级 Retry
字段、`guard-agent decision retry`、`guard_decision_failed_total` 和 ADR-017；相应能力
迁移到 Enforcement/Reconcile Contract。

---

## 2. M0 的定位

M0 不是新的产品功能里程碑，也不是继续扩写 V0.4。

M0 只解决以下问题：

1. 同一个业务语义只能有一个权威模型。
2. 数据库、内存状态和 Firewall 之间的故障恢复行为必须确定。
3. Source checkpoint 必须真正满足已声明的投递语义。
4. 并发、重放和重启不能制造重复持久化副作用。
5. Firewall Backend 必须只管理 Guard-owned 对象。
6. 关键行为必须能用自动化测试验证，而不是依赖开发者理解文字。

M0 完成后，系统应能够稳定运行两条最小纵向链路：

```text
RawRecord
  → Parser
  → SecurityEvent
  → Detection
  → durable outcome
  → SourceDurable/checkpoint
```

```text
Decision
  → Desired Ban Projection
  → Desired Firewall Snapshot
  → Reconciler
  → Fake Firewall
  → Observed Enforcement State
```

---

## 3. 范围边界

### 3.1 M0 必须完成

- SecurityEvent、SourcePosition 和稳定事件身份。
- Decision、Desired Projection、Normalized Enforcement Intent 与 Reconcile State 的职责、状态和不变量。
- SourceDurable、ProcessingComplete、checkpoint、重放、背压和优雅停机。
- SQLite 最小 v1 migration、唯一约束和事务边界。
- Firewall 声明式行为、所有权和一个真实 nftables Spike。
- Config 权威关系与最小配置 Schema。
- 进程权限边界 ADR。
- 两条 Fake Vertical Slice。
- 核心 Given/When/Then Contract Tests。
- M0 Go/No-Go Gate。

### 3.2 可以与 M0 并行，但不得反向冻结 Contract

- 仓库目录和构建骨架。
- 日志、测试、lint、CI 基础设施。
- SQLite migration runner 骨架。
- HTTP router 空壳。
- Fake Backend 测试工具。

如果工程语言、前端技术栈或第三方依赖尚未通过 ADR 确认，禁止以“搭骨架”
为由新增依赖或锁文件。

### 3.3 不进入 M0

- Phase 2 Cluster 能力。
- 完整 Web 页面和全部 REST/OpenAPI 资源。
- React、Vite、shadcn/ui 或其他具体前端栈选型。
- 全部 iptables、UFW、Docker Backend 的生产实现。
- GeoIP、Threat Intelligence、机器学习、自动 CIDR 聚合。
- Progressive Ban 和复杂关联检测。

这些内容只能在对应里程碑开始前另行冻结，不能阻塞 M0 核心链路。

---

## 4. 权威源与派生状态

系统必须遵循以下权威关系：

```text
Active Decisions
    = 唯一安全意图事实

Desired Ban Projection
    = 可从 Active Decisions 重建的物化投影

Allowlist
Protected Targets
Firewall Config
Backend Infrastructure Schema
    = 独立 Policy / Config 事实

Desired Ban Projection
    + Policy / Config 事实
    ↓
Normalized Target Enforcement Intent
    + Managed Infrastructure Intent
    + Managed Policy Intent
    ↓
Desired Firewall Snapshot
    = Reconciler 的完整期望输入

Firewall Snapshot
    = 外部执行状态的当前观察结果

DB Observed State
    = 最近一次 Firewall 观察缓存，不是外部事实源
```

必须满足：

1. Decision 写入、终止和对应 Desired Ban Projection 更新必须在同一个 SQLite
   写事务内完成。
2. Desired Ban Projection 损坏或缺失时，必须能够从 Active Decisions 全量重建。
3. Reconciler 禁止根据 DB 中旧的 Observed State 猜测 Firewall 当前状态。
4. Probe 或 Snapshot 失败时，相关 Observed State 必须为 `Unknown`。
5. Reconcile 必须使用分层 revision/generation 做 fencing；禁止用一个全局 generation
   同时承担安全意图版本、Policy 版本、Infrastructure 版本和 Target Retry budget。
6. Reconciler 必须收敛完整 Desired Firewall Snapshot，包括 Guard-owned infrastructure、
   Allowlist、Protected Targets、INPUT/FORWARD scope 和 Ban Projection，不能只同步 blacklist。
7. Target Retry budget 只能在“该 Target 最终可执行 Firewall Intent”发生语义变化时获得
   新 generation；仅 Active Decision 内部构成变化但最终执行属性未变化时不得重置预算。
8. Allowlist、Protected Targets 或相关 scope 变化如果改变某 Target 的最终执行属性，必须
   同时产生新的 Target Enforcement Generation；仅递增 PolicyRevision 不足以表达该变化。

版本分层冻结为：

```text
TargetProjectionRevision
  = 单个 Target 的 Active Decisions / Desired Ban Projection 版本
  = 用于证明投影是否由当前安全意图重建
  = 不直接作为 Retry budget key

TargetEnforcementGeneration
  = 单个 Target 的规范化最终 Firewall Intent 版本
  = 只有影响该 Target 实际执行语义的字段变化时才递增
  = Target Retry budget 的业务 generation

PolicyRevision
  = Allowlist / Protected Targets 等全局 Policy 事实版本

InfrastructureRevision
  = Backend schema / hook / INPUT-FORWARD scope / managed infrastructure 版本

SnapshotRevision
  = 完整 Desired Firewall Snapshot 的规范化外部期望内容变化时递增的全局观察版本
  = 用于 full snapshot / infrastructure / policy fencing
  = 不得因为无关 Target 变化而单独使另一个 Target 的已确认结果失效
```

`Normalized Target Enforcement Intent` 至少由下列对 Firewall 有意义的规范化属性组成：

```text
CanonicalTarget
BanMembership: Present/Absent
EffectiveUntil
TimeoutMode
Scopes
AddressFamily
PolicyCoverage: None/Partial/Full
PolicyRelationDigest
Backend-relevant target attributes
```

`ActiveDecisionCount`、Decision ID 列表等解释性字段可以属于 Desired Ban Projection，但若
它们变化而上述规范化执行属性不变，只递增 `TargetProjectionRevision`，不得递增
`TargetEnforcementGeneration`。

Reconcile failure domain 必须分层，至少区分：

```text
Infrastructure Domain
  key = InfrastructureRevision + RetryEpoch

Policy Domain
  key = PolicyRevision + RetryEpoch

Target Domain
  key = CanonicalTarget + TargetEnforcementGeneration + RetryEpoch
```

禁止把一次 table/hook/owner 冲突按所有 Target 分摊 retry，也禁止让单个 Target Apply
失败消耗 Infrastructure 或 Policy budget。

各类产物的唯一权威源如下：

| 内容 | 唯一权威源 |
|---|---|
| 架构取舍和不变量 | 本文与 ADR |
| Go 类型和接口 | M0-D 冻结后的代码 |
| SQLite Schema | migration SQL |
| 配置字段 | Config Schema |
| 行为验收 | 自动化 Contract Tests |

Markdown 不得复制完整 DDL、完整生成代码或测试实现，避免多份内容漂移。

---

## 5. M0 执行顺序

```text
M0-A 行为不变量草案
  ├─ Core Model
  ├─ Decision / Enforcement
  ├─ SourceDurable / ProcessingComplete 屏障
  └─ Crash Matrix
          ↓
M0-B 风险 Spike
  ├─ SQLite 并发唯一约束
  ├─ Source replay identity
  ├─ nftables hook/priority/atomic batch
  └─ Backend Snapshot/Ensure 可行性
          ↓
M0-C 可执行 Slice
  ├─ Source → SourceDurable → checkpoint / ProcessingComplete
  └─ Decision → Fake Firewall → Reconcile
          ↓
M0-D 正式冻结
  ├─ 类型和接口
  ├─ migration
  ├─ Config Schema
  ├─ ADR
  └─ Contract Tests
```

M0-A、M0-B 可以并行处理互不依赖的研究项。

M0-C 必须基于 M0-A 的第一版行为不变量；Fake Slice 不是和 Contract 完全无依赖
的独立开发任务。

M0-D 只能冻结已经被 Spike 或 Slice 验证过的接口。

---

## 6. SourcePosition 与稳定身份 Contract

### 6.1 RawRecord 必备字段

RawRecord 必须包含：

```text
SourceID
ObservedAt
DeliverySequence
Position
Content
Metadata
```

`Position` 必须是类型化字段，禁止只藏在无约束的 `Metadata` 中。

`DeliverySequence` 是 Guard 为每个 Source processing session 分配的单调递增序号，
只用于本轮处理顺序和连续 SourceDurable 判断。外部 Position 负责恢复日志源，稳定 Delivery ID
负责跨重启幂等，Sequence 不承担稳定身份。

M0-B 根据投递方案冻结 Sequence 的持久化方式：

- durable inbox：Sequence 随 inbox item 一起持久化；
- receipt pipeline：Sequence 可以是 session-local，crash 后从 checkpoint 重读并重新
  分配，幂等依靠稳定 Delivery ID 和 receipt。

禁止把“持久化 Sequence”实现成未经基准验证的逐条独立 SQLite 更新。

### 6.2 File Position

File Source 的位置身份至少包含：

```text
source_id
file_generation
device_id
inode
record_start_offset
record_end_offset
```

要求：

- `file_generation` 在 Guard 识别出一个新的文件代际时生成，并且必须在发出该
  generation 的第一条 RawRecord 前 durable persist。
- 同一路径 rename/create 后，新旧文件必须属于不同 generation。
- 记录字节范围统一为半开区间
  `[record_start_offset, record_end_offset)`；`record_end_offset` 是下一条未读记录的
  起点，checkpoint 恢复时从该位置开始。
- generation 切换必须有明确屏障：旧 generation 已读取记录与新 generation 记录都
  分配同一 Source 下连续的 DeliverySequence；checkpoint 只能提交最高连续 Sequence
  对应的 generation/offset。
- canonical identity 禁止只使用日志内容 hash。

File generation registry 或等价 durable source state 至少保存：

```text
source_id
file_generation
device_id
inode
resolved_path
created_at
lifecycle_state
```

旧 generation 尚未 checkpoint、但新 generation 已产生 receipt 时发生 crash，重启后
新 generation 的 Delivery ID 必须保持不变。

Generation registry row 只有在该 generation 已被持久化 checkpoint 安全越过、没有
未完成 inbox/receipt 引用、且确认不存在需要重放的记录后才能进入可清理终态。清理
前必须保留 Delivery ID 重建所需字段。

### 6.3 Journald Position

Journald Source 的位置身份至少包含：

```text
source_id
cursor
```

cursor 无效时，按照 V0.4 的 `resume_policy` 执行，并产生可观察的 Audit、Metric
和 Health 状态变化。

Journald cursor 是 opaque value，禁止直接比较大小；checkpoint 保存最高连续
DeliverySequence 对应的 cursor。

### 6.4 Delivery ID

每条 RawRecord 必须拥有稳定的 Delivery ID：

```text
File:
  source_id + file_generation + start_offset + end_offset

Journald:
  source_id + cursor
```

同一条记录因崩溃重放时，Delivery ID 必须保持不变。

---

## 7. SecurityEvent Contract

SecurityEvent 的逻辑模型冻结为：

```go
type SecurityEvent struct {
    ID string

    ObservedAt time.Time
    Timestamp  *time.Time

    SourceID       string
    SourcePosition SourcePosition
    ParserID       string
    ParserVersion  string
    OutputIndex    int
    NodeID         string

    EventType string

    Source Endpoint
    Target Endpoint

    User *UserInfo
    HTTP *HTTPInfo

    Service string

    Labels map[string]string
    Fields map[string]any
}
```

### 7.1 System-owned 字段

以下字段只能由 Guard 生成，Parser 禁止覆盖：

```text
ID
ObservedAt
SourceID
SourcePosition
ParserID
ParserVersion
OutputIndex
NodeID
```

### 7.2 Parser-owned 字段

Parser 可以映射：

```text
Timestamp
EventType
Source
Target
User
HTTP
Service
Labels
Fields
```

### 7.3 字段语义

- `ObservedAt` 必填，用于 Detection Window。
- `Timestamp` 可空，仅表示日志声称的发生时间。
- `Timestamp` 解析失败时按 Parser 配置产生明确错误或空值，禁止回退成
  `ObservedAt` 并伪装解析成功。
- `source.ip`、`target.ip` 和 Prefix 必须使用 `netip` 严格解析。
- Prefix 入库和参与唯一键前必须 canonicalize/mask。

### 7.4 Event ID

同一 RawRecord 可由多个 Parser 产生零个、一个或多个 SecurityEvent。

SecurityEvent ID 必须由下列稳定信息派生：

```text
delivery_id
parser_id
parser_version
emitted_index
```

M0-D 必须冻结具体编码和 hash 算法，并提供跨重启稳定性测试。

### 7.5 Processing Plan 与版本一致性

单次 processing attempt 必须使用不可变的 Parser/Rule version snapshot；运行中的原子
版本切换不得改变已经开始处理的记录。

Source 阶段只能可靠知道候选 Parser 集，不能在 Parser 输出产生前假定已经知道真正
“适用”的 Detection Rule。因此 Processing Plan 必须按阶段冻结：

```text
RawRecord ingress
  → Parser Set Snapshot

Parser produces SecurityEvent
  → Detection Rule Catalog Snapshot
  → 根据该 snapshot 计算真正 applicable rules
```

- durable inbox 方案必须随 inbox item 持久化可恢复的 `ParserSetSnapshot`，至少包含
  Parser ID/Version；同时持久化 `DetectionRuleCatalogRevision` 或等价的不可变 Rule
  Version Set，使后续 Parser 输出只能在该冻结 Rule 世界中求值。
- durable inbox 不要求 Source 阶段预先列出最终 applicable rules；真正 applicable rules
  在 SecurityEvent 产生后，从已冻结 Rule Catalog 中确定。
- 对应 Parser/Rule revision 必须是不可变记录，并保留到所有引用它的 inbox item 进入
  ProcessingComplete；也可以在 inbox 中保存经验证、可恢复的自包含 snapshot。只保存
  已经无法解析回旧定义的 ID/Version 不构成可恢复计划。
- receipt pipeline 方案中，必要结果与 receipt 同事务提交。crash 前未提交的记录允许
  在重启后使用当前 Active Parser/Rule 重新求值；这属于明确的 re-evaluation，不声称
  为严格同版本 replay。
- 已有 terminal processing record 的 Delivery 禁止因 Parser/Rule 升级再次自动处理；
  历史重处理必须是显式运维动作，并使用新的 reprocess identity。
- Detection 结果的幂等键必须包含稳定 Event ID 与 Rule ID/Version。

---

## 8. Source Durability、Processing Completion、checkpoint 与重放 Contract

M0 禁止继续用一个含混的 `ACK` 同时表达“Source 可以推进 checkpoint”和“业务处理已
完成”。冻结两个独立概念：

```text
SourceDurable
  = Guard 已经获得足够持久化证据，可以安全推进 Source checkpoint

ProcessingComplete
  = Parser / Detection / 必要业务 outcome / Critical Audit 已进入成功或终态拒绝
```

两者在不同 delivery model 中的先后关系不同。

### 8.1 SourceDurable 屏障

“写入内存 Channel”不构成 SourceDurable。

receipt pipeline：

```text
Parser / Detection
  → terminal processing record
  → Alert / Decision / Critical Audit 等必要结果同事务提交
  → ProcessingComplete
  → SourceDurable
  → contiguous checkpoint 可推进
```

durable inbox：

```text
RawRecord
  + SourcePosition
  + stable Delivery ID
  + recoverable Processing Plan
  → durable inbox commit
  → SourceDurable
  → contiguous checkpoint 可推进

之后异步：
  Parser / Detection / outcome / Critical Audit
  → ProcessingComplete
```

因此 durable inbox 模式中，Parser/Detection 尚未完成时 Source checkpoint 可以已经推进；
这是 durable inbox 的核心语义，不得再用“所有 Parser 完成后才能 ACK”覆盖它。

Firewall Apply、Firewall Revoke 和 SMTP 发送不属于 SourceDurable 或
ProcessingComplete 前置条件。

### 8.2 ProcessingComplete 与多 Parser

一条 RawRecord 只有在本次不可变 Processing Plan 中所有已调度 Parser 任务，以及其
产生 Event 对应的全部适用 Detection Rule，都进入成功或终态拒绝后，才能标记
`ProcessingComplete`。

M0 只冻结上述跨模块不变量。Parser 的 first-match、all-match、priority 和 error
continuation 语义在 M3 开始前冻结。Fake Slice 默认使用一个 Parser，不得把该默认行为
扩展成产品 Contract。

在 receipt pipeline 中，`ProcessingComplete` 同时是 `SourceDurable` 的必要条件；在
 durable inbox 中不是。

### 8.3 连续 checkpoint

Checkpoint Manager 只能按同一 Source 的 DeliverySequence 提交：

```text
highest contiguous SourceDurable position
```

后续记录已经 SourceDurable，但前序记录尚未 SourceDurable 时，禁止越过空洞推进
checkpoint。

DeliverySequence 只负责本 processing session 的连续性排序；跨重启幂等仍由稳定
Delivery ID / SourcePosition 负责。

### 8.4 重放幂等

M0-B 必须在以下两种方案中选择并验证一种：

- durable inbox + 唯一 Delivery/Event ID；或
- 同步 Detection transaction + terminal processing record / 唯一 Event ID。

无论选择哪种方案，都必须证明：

- 未达到 SourceDurable 的记录不会因 checkpoint 提前推进而丢失。
- 已达到 SourceDurable 的 durable inbox item 即使 Source checkpoint 已推进，仍能在
  crash 后完成 ProcessingComplete。
- 重放不会创建重复 Alert、Decision、Critical Audit 或其他持久化副作用。
- 重放是否重新进入内存 Detection Window 必须有明确产品语义。

receipt pipeline 中，即使一条记录没有产生 Alert 或 Decision，也必须写入 terminal
processing record；该记录、必要业务结果和对应幂等键必须在同一个事务中提交。

用于去重的 terminal processing record、inbox identity 或 tombstone，在对应恢复语义
仍需要它们时禁止删除：

- receipt pipeline：不得早于对应 Source checkpoint 已持久化安全越过该 Position；
- durable inbox：不得早于 inbox item 已 ProcessingComplete，且不存在 replay/reprocess
  引用及其保留策略要求。

Alert、Decision 等下游持久化副作用仍必须拥有独立唯一键，不能只依赖 terminal record。

未来 Notification Contract 必须复用稳定 Event/Decision ID 建立唯一幂等键；在该
Contract 冻结前，Notification Job 不进入 M0 Slice 的强制证明范围。

Phase 1 保持 V0.4 的内存 Window 语义：Agent restart 后 Window 清空。已经
ProcessingComplete 的稳定 Event ID 即使因任何恢复路径再次出现，也不得自动再次贡献
窗口计数；此前尚未形成 Decision 的部分内存窗口状态可能丢失。这是一项明确的检测
连续性限制，Source at-least-once 不等于 Detection 跨重启 exactly-once。

### 8.5 Poison Record / Terminal Processing Record

确定性解析失败、超长行或资源限制拒绝不能永久阻塞连续 checkpoint 或 durable inbox
处理。

抽象终态记录称为：

```text
Terminal Processing Record
```

具体落地：

```text
receipt pipeline
  → processing_receipt terminal row

durable inbox
  → inbox terminal state / durable tombstone
```

禁止因为文档使用 `receipt` 一词而要求 durable inbox 方案额外维护一套重复 receipt 表。

Poison Record 的 Terminal Processing Record 至少包含：

1. Delivery ID 唯一幂等键。
2. SourcePosition、failure stage、error code、sanitized error、terminal action、occurred time。
3. 提交后增加低基数 Metric；Metric 不是 SourceDurable 屏障或恢复凭据。
4. 对影响 Source 连续性的拒绝写 Critical Audit；受限错误样本是否保留由配置决定。
5. 禁止记录凭据或未经截断的敏感日志内容。

receipt pipeline 完成上述事务后同时达到 ProcessingComplete 和 SourceDurable；durable
inbox 中原始 inbox durable commit 已经可以使 SourceDurable 成立，但只有 Poison
terminal state、Critical Audit 等同事务提交后才能达到 ProcessingComplete 并进入可清理
状态。

### 8.6 Critical Audit

Audit 分为：

```text
Critical Audit
Operational Audit / Telemetry
```

Critical Audit 至少包括：

- Decision 创建、到期和人工撤销。
- Manual Ban replace。
- Poison Record 终态。
- Allowlist 和 Protected Target 变更。
- Maintenance enable/disable。
- 管理员开启新的 Retry Epoch。

Critical Audit 必须与对应业务状态或 Terminal Processing Record 在同一个 SQLite 事务中
提交，并具有数据库级幂等约束。

- receipt pipeline：Critical Audit 未提交时禁止达到 ProcessingComplete / SourceDurable，
  因而 checkpoint 不得推进。
- durable inbox：原始 inbox commit 可以先使 SourceDurable 成立，但 Critical Audit 未提交
  时禁止把 inbox item 标记 ProcessingComplete 或删除；已经持久化推进的 Source checkpoint
  无需回退。
- 管理操作：Critical Audit 未提交时禁止返回成功。

Operational Audit 和高频 Telemetry 可以异步批量写入，但不得被表述为安全关键操作已经
可靠审计的证据。

### 8.7 Detection Window 幂等边界

同一个 `EventID + RuleID/Version` 在一个活跃 Rule/Group Window 中最多贡献一次。

必须覆盖以下失败序列：

```text
Event E
  → 已写入内存 Window
  → threshold 计算完成
  → SQLite outcome transaction 失败
  → E 被同进程重试
```

重试不得让 E 第二次增加 count 或 distinct_count，也不得因为事务回滚制造额外 Decision。

M0-D 不强制具体实现，可以使用 EventID membership、staged window mutation 或等价机制；
但必须有可执行 Contract Test 证明同一 Event 在事务失败、同进程重试和重放路径下最多
贡献一次。

---

## 9. Queue 与优雅停机 Contract

### 9.1 核心队列

核心安全流水线必须使用 bounded queue + backpressure。

禁止因 queue full 静默丢弃 RawRecord 或 SecurityEvent。

允许的 drop 仅限：

- 超过 max line 的记录。
- 明确配置为 discard 的 malformed 记录。
- 资源策略明确拒绝且已进入 Poison Record 终态的记录。

所有 queue 必须暴露：

```text
capacity
depth
backpressure duration
rejected total
oldest item age
```

### 9.2 上游数据丢失

Backpressure 不等于日志永不丢失。File rotation 和 Journald vacuum 仍可能在长期
积压时删除未读取数据。

系统必须检测并暴露：

- source lag time/bytes/records。
- inode/cursor gap。
- rotation/vacuum 导致的不可恢复区间。
- 明确的 Data Loss Audit 和 unhealthy/degraded 状态。

Source at-least-once 只保证 Guard 已经建立稳定 SourcePosition 后的内部投递，不保证
抵御日志生产端 destructive truncate、删除或超过 Guard lag 的轮转。

`copytruncate` 存在 fast-regrow 盲区：文件可能在两次 stat 之间先截断，再快速增长到
旧 offset 以上，仅凭 inode 与 size 无法可靠发现。能够检测时必须报告
`DataLossSuspected`；无法检测的场景是 Phase 1 known limitation，禁止宣称绝不丢失。

### 9.3 优雅停机

SIGTERM 后按以下顺序执行：

```text
停止接受管理面的新写操作
  → Source 停止读取新记录
  → drain 已进入 pipeline 的记录
  → flush contiguous checkpoint
  → flush 必须持久化的 Audit
  → 关闭 DB
  → exit
```

默认 `shutdown_timeout` 在 M0-B 压测后冻结，M0-D 不得保留 TBD。

超时后允许直接退出；下次启动依靠 at-least-once replay 和幂等约束恢复。

---

## 10. Decision Contract

### 10.1 职责

```text
Decision = Why / What should happen
```

Decision 只表示有效的安全意图，不表示 Firewall 是否成功执行。

Firewall 错误禁止把 Decision 改成 `Failed`。

### 10.2 状态

Phase 1 Decision 只保留：

```text
Active
Expired
Revoked
```

终止原因使用独立 EndReason：

```text
expired
manual
manual_replace
rule_disabled
system_cleanup
```

不新增与 `Revoked + EndReason` 重叠的 `Cancelled`。

### 10.3 创建与终止

- 自动检测确认后，在 DB 事务中直接创建 `Active` Decision。
- `ExpiresAt` 从 Decision 创建时计算，不从 Firewall Apply 成功时计算。
- Decision 在首次成功 Apply 前已经过期时，禁止再执行 Ban。
- 到期时先在 DB 事务中终止 Decision 并重算 Desired Ban Projection，Firewall 删除由
  Reconciler 完成。
- 人工撤销使用同一事务原则。

### 10.4 Allowlist 是正交 Policy Exception

Allowlist 不表示攻击事实或安全意图消失，默认不得把 Active Decision 改为 Revoked。

默认语义是“只抑制 Firewall 副作用，不抑制检测事实”：来源命中 Allowlist 时，Parser、
Detection、Alert 和 Decision 创建照常执行，但该 Target 不进入实际 Ban 效果。只有 Rule
显式配置 `exclude.source.allowlisted=true` 时，该 Rule 才跳过 Allowlist 来源，不创建
对应 Detection、Alert 或 Decision。删除 Allowlist 后，默认语义下期间创建且尚未过期的
Decision 会重新产生 Ban 效果；显式排除的 Rule 因未创建 Decision，不追溯补建。

完整行为：

```text
Active Decisions
  → Desired Ban Projection

Desired Ban Projection
+ Allowlist
+ Protected Targets
  → Desired Firewall Snapshot
```

Allowlist 与 blacklist 同时保留在 Desired Firewall Snapshot 中，Firewall 继续使用
`allowlist RETURN → blacklist DROP` 的顺序。临时 Allowlist 删除后，尚未过期的
Decision 自动重新产生实际 Ban 效果。

CIDR Allowlist 只对其覆盖的地址范围形成 Policy Exception。`/32` Allowlist 禁止撤销
与其重叠的整个 `/24` Decision。

Policy Resolver 必须计算 Allowlist/Protected Targets 与每个 Ban Target 的范围关系，至少区分
`None / Partial / Full`。Partial coverage 不改变 BanMembership；Firewall Snapshot 继续同时保留
原 blacklist CIDR 与更高优先级的 policy range。

### 10.5 Automatic Decision 幂等

同一 Rule + canonical Target 同时最多存在一个 Active Decision。

该约束必须由 SQLite 唯一索引保证，禁止只做应用层“先查后插”。

SQLite 原生支持 partial unique index。M0-B 必须在下列方案中选择一个，不得同时
保留两套概念：

```text
UNIQUE(rule_id, canonical_target) WHERE state = 'active'
```

或：

```text
active_key + UNIQUE
```

最终唯一键是否包含 NodeID、Action、Scope，必须由 Phase 1 数据范围测试证明并在
M0-D 冻结。

发生唯一冲突时必须原子更新：

```text
last_triggered_at
suppressed_alert_count
```

且不得延长 Ban。

### 10.6 Manual Decision identity

Automatic 与 Manual Decision 使用不同业务幂等键：

```text
Automatic:
  rule_id + canonical_target

Manual:
  decision_source=manual + canonical_target
```

Phase 1 同一 canonical Target 最多存在一个 Active Manual Decision。重复执行 manual
ban 默认返回 `AlreadyBanned`，不得静默修改 duration。

只有显式 `--replace` 才允许替换：在同一个 SQLite 事务中把旧 Manual Decision 改为
`Revoked/EndReason=manual_replace`，写 Critical Audit，再创建新的 Active Manual
Decision。Automatic 与 Manual Decision 可以同时存在，最终由 Desired Ban Projection
聚合。

### 10.7 正常运行期 Expiration

Decision 到期检测与 Firewall Reconciler 是两个独立职责，但不强制采用特定 goroutine、
timer 或 min-heap 实现。

必须满足：

- 默认最大 expiration detection lag 不超过 V0.4 的 Reconcile 基线 `60s`。
- 到期批次在一个 SQLite 事务中完成 `Active → Expired`、Critical Audit、受影响
  Desired Ban Projection 重算和对应 `TargetProjectionRevision` 递增。
- 只有当到期造成该 Target 的规范化最终 Firewall Intent 发生变化时，才递增
  `TargetEnforcementGeneration`；例如两个 Decision 的最大 `EffectiveUntil` 未变化时，
  仅 DecisionCount 变化不得获得新的 Target Retry budget。
- 任一 Target 的规范化最终 Firewall Intent 发生变化时递增 `SnapshotRevision`；仅
  Desired Ban Projection 的解释性字段变化而最终 Firewall Intent 不变时，可以不递增
  SnapshotRevision。
- 事务提交后，对发生 Target Enforcement 变化的 Target 立即唤醒 Reconciler，不等待下一次
  周期轮询。
- Backend 健康时，无 native timeout 后端从 `ExpiresAt` 到实际删除的延迟不得超过
  expiration lag、调度延迟和一次 Firewall operation timeout 之和。
- Maintenance Mode 期间 Decision 仍然正常到期并更新 Desired Ban Projection；暂停的
  只是 Firewall 外部副作用。

M0 Fake Backend 的冻结基线为：

```text
max_expiration_detection_lag = 60s
max_reconcile_dispatch_lag = 1s
fake_firewall_operation_timeout = 1s
```

因此 M0-C 的健康 Fake Backend 从 `ExpiresAt` 到 Firewall Snapshot 实际变为 `Absent`
必须不超过 `62s` 虚拟时间。真实 Backend 在 M6 分别冻结 operation timeout，并继续满足
“expiration detection lag + reconcile dispatch lag + 一次 operation timeout”的公式。
M0-B 必须用可注入时钟验证检测延迟，M0-D 不得留下“运行时如何发现过期”的开放语义。

---

## 11. Desired、Observed 与 Reconcile Contract

### 11.1 Desired Ban Projection

同一 canonical Target 的所有 Active Decisions 聚合为一个 Desired Ban Projection。

规则：

```text
active decision count > 0
  → BanProjection = Present

active decision count == 0
  → BanProjection = Absent
```

`EffectiveUntil`：

- 全部 Active Decisions 都有到期时间时，取最大值。
- 任一 Active Decision 为永久时，`EffectiveUntil = nil`。

Desired Ban Projection 是安全意图投影，不等于最终 Firewall 执行意图。Policy Resolver
必须把它与 Allowlist、Protected Targets 和 scope/config 合成为
`Normalized Target Enforcement Intent`。

典型结果：

```text
BanProjection=Present + no overlapping policy exception
  → BanMembership=Present
  → PolicyCoverage=None

BanProjection=Present + /32 Target fully covered by Allowlist
  → BanMembership=Present
  → PolicyCoverage=Full

BanProjection=Present + /24 Target only overlapped by /32 Allowlist
  → BanMembership=Present
  → PolicyCoverage=Partial

BanProjection=Absent
  → BanMembership=Absent
  → PolicyCoverage=None
```

支持 native timeout 的 Backend 对最终 `BanMembership=Present` 且有限期 Target 使用：

```text
native_timeout = EffectiveUntil + SafetyGrace
```

Native timeout 只是 Agent crash 时的 failsafe，不终止 Decision，也不更新 Desired Ban
Projection。`EffectiveUntil` 后移时必须刷新 timeout；永久 Target 禁止遗留旧 timeout。

只有规范化最终 Firewall Intent 发生变化时才递增 `TargetEnforcementGeneration`。以下是
必须冻结的例子：

```text
DecisionCount 2 → 1，但 BanMembership / EffectiveUntil / Scope / PolicyCoverage 不变
  → TargetProjectionRevision++
  → TargetEnforcementGeneration 不变
  → Retry budget 不重置

Allowlist add/remove 与 Target 发生 Full 或 Partial overlap，导致 PolicyCoverage / PolicyRelationDigest 变化
  → PolicyRevision++
  → 受影响 TargetEnforcementGeneration++

Allowlist 变化与某 Target 完全不相交
  → PolicyRevision++
  → 该 TargetEnforcementGeneration 不变

Allowlist remove 重新暴露此前被 Policy 覆盖的攻击范围
  → 受影响 Target 获得新一代合法 Target retry budget
```

### 11.2 Observed Firewall State

Observed State 必须分层表达，而不是只保存 target membership：

```text
ObservedFirewallSnapshot
  ├─ InfrastructureObserved
  ├─ PolicyObserved
  └─ TargetObserved[]
```

Target Observed 至少包含：

```text
CanonicalTarget
ObservedAt
Backend
ObservedTargetEnforcementGeneration
BanMembership: Unknown/Present/Absent
PolicyCoverage: None/Partial/Full/Unknown（由 Target + Policy Snapshot 派生）
PolicyRelationDigest
TimeoutMode: none/native
NativeExpiry
Scopes: INPUT/FORWARD
AddressFamily
OwnerVersion
LastError
```

对于通过 allowlist RETURN + blacklist DROP 表达的 Backend，Allowlist 命中时 blacklist membership
仍应保持 `Present`。Policy 与 Ban CIDR 的关系必须支持 `None/Partial/Full` 三态：例如 `/24`
Ban 与 `/32` Allowlist 是 `Partial`，禁止把整个 `/24` Target 简化成 Suppressed 或从 blacklist
删除。Snapshot 必须同时有足够的 Target membership、Policy ranges 和 relation digest 证明实际
执行语义与 Desired 一致。

Observed State 是缓存。真正的当前状态只能通过 Firewall Snapshot/Probe 获得。

Observed State 只能来自成功的 Snapshot/Probe，或者 Backend Contract 明确保证具有权威
确认语义的 Ensure 返回值。操作超时、连接中断或结果不确定时必须写 `Unknown`，并在
再次执行写操作前先 Probe。

`Drifted` 是 Desired 与最新 Observed 的派生结论。Converged 必须比较规范化后的全部
相关属性，不能只比较 Present/Absent。NativeExpiry 按 Backend 支持的时间精度设置明确
容差，避免倒计时或取整差异造成永久误判；永久 Desired 必须观察到
`TimeoutMode=none`。

### 11.3 Reconcile Failure Domains

统一状态枚举：

```text
Pending
Applying
Converged
RetryWaiting
Degraded
```

但必须分别维护三个 failure domain：

#### Infrastructure Domain

负责：

```text
table
chain
hook/jump
owner/version
backend schema
INPUT/FORWARD managed infrastructure
```

Retry key：

```text
InfrastructureRevision + RetryEpoch
```

`OwnershipConflict`、table/hook 创建失败等属于 Infrastructure failure，不得复制成所有
Target 的失败。

#### Policy Domain

负责：

```text
allowlist set/state
protected target policy state
与 PolicyRevision 直接相关的 managed policy objects
```

Retry key：

```text
PolicyRevision + RetryEpoch
```

Policy Apply 失败不得消耗 Target budget。Policy 事实变化如果改变某 Target 的规范化最终
Firewall Intent，仍必须同时递增该 Target 的 `TargetEnforcementGeneration`。

#### Target Domain

负责：

```text
ban target presence
native timeout
per-target scope/attributes
```

Retry key：

```text
CanonicalTarget + TargetEnforcementGeneration + RetryEpoch
```

必须满足：

- Apply 失败后 Decision 保持 Active。
- Revoke/target removal 失败时 Desired 保持当前最终 Intent，Observed 不伪装收敛。
- 三个 domain 都使用有界退避，不允许普通 Reconcile 无限复活。
- 达到上限后对应 domain 进入 `Degraded`，Desired/Policy/Infrastructure Intent 不被删除。
- 普通 Reconcile、Probe、Agent restart 和 Backend 状态抖动不得重置预算。
- Backend unhealthy→healthy 只触发立即 Probe；预算尚有剩余时允许执行下一次尝试，已
  耗尽时仍保持 Degraded。
- 管理员显式 Retry 只能为指定 failure domain 创建新的 RetryEpoch，并写 Critical Audit。
- 日志级别等无关配置变化不得影响任何 retry budget。
- 每次外部写操作前必须先持久化 attempt；调用期间 crash 仍计入对应 domain budget。
- M0-D migration 必须持久化三个 domain 所需的 generation/revision、retry epoch、attempt
  count、next/last attempt、status 和结构化 last error；具体列名以 migration SQL 为唯一
  权威源。

V0.4 的 `guard-agent decision retry <id>` 在新模型中不再成立。M7 开发前必须冻结按
Infrastructure / Policy / Target Enforcement domain 重试的 CLI/API；Retry 禁止直接修改
Decision。

### 11.4 Fencing 与并发

同一 Agent 内只能有一个 Firewall 外部写执行器，防止多个 goroutine 并发修改
Guard-owned Firewall 对象；Reconcile planner 可以并发计算，但外部 mutation 必须串行。

Fencing 规则：

- Infrastructure operation 开始和回写校验 `InfrastructureRevision`；必要时同时校验本次
  full-snapshot 的 `SnapshotRevision`。
- Policy operation 开始和回写校验 `PolicyRevision`。
- Target operation 开始和回写必须校验该 Target 的 `TargetEnforcementGeneration`。
- Target operation **不得仅因为无关 Target 导致全局 SnapshotRevision 前进而丢弃已经
  权威确认的成功结果**。
- 如果 SnapshotRevision 变化可能影响当前 Target（例如 Allowlist、scope、backend semantics
  变化），Policy Resolver 必须已经为受影响 Target 递增 `TargetEnforcementGeneration`；
  Target fencing 以该 generation 为准。
- 外部调用结果未知时 Observed=Unknown；下一次 mutation 前必须先 Probe。

M0-B 必须验证：Target B 高频变化不会导致与其无关的 Target A 永久无法进入 Converged。

---

## 12. Persist Intent 与崩溃恢复 Contract

### 12.1 Ban / Policy Intent 持久化顺序

Automatic / Manual Decision 变化：

```text
BEGIN DB TX
  insert/update/terminate Decision
  recompute Desired Ban Projection
  increment affected TargetProjectionRevision
  resolve normalized Target Enforcement Intent
  if normalized intent changed:
      increment TargetEnforcementGeneration
      increment SnapshotRevision
  write Critical Audit
COMMIT

compose Desired Firewall Snapshot
Reconciler Snapshot actual Firewall
Reconciler plan domain operations
persist attempt for selected failure domain
Firewall operation
Update observed state using matching revision/generation fencing
```

Policy 变化：

```text
BEGIN DB TX
  update Allowlist / Protected Target policy
  increment PolicyRevision
  resolve affected Target Enforcement Intent
  increment each actually changed TargetEnforcementGeneration
  increment SnapshotRevision
  write Critical Audit
COMMIT
```

Infrastructure 配置变化：

```text
BEGIN DB TX
  update authoritative config/domain state as allowed
  increment InfrastructureRevision
  resolve affected target semantics if any
  increment affected TargetEnforcementGeneration
  increment SnapshotRevision
  write Critical Audit when security relevant
COMMIT
```

禁止先修改 Firewall，再写 Decision/Policy/Infrastructure Intent。

系统不追求 SQLite 与 Firewall 的跨系统 ACID 事务：

```text
DB transaction atomic
Firewall operation atomic where supported
System-level eventual consistency
```

### 12.2 启动顺序

启动时必须：

1. 读取并验证打开 DB 所需的 Runtime Config。
2. 根据 Runtime Config 获取单实例锁。
3. 按配置路径打开 DB 并验证 migration 与 SQLite durability 配置。
4. 加载并验证 SQLite domain config。
5. 使用当前时钟，在同一事务中把所有 `ExpiresAt <= now` 的 Active Decision 转为
   Expired，写 Critical Audit，重算受影响 Target 的 Desired Ban Projection，并递增
   `TargetProjectionRevision`；只有规范化最终 Firewall Intent 变化的 Target 才递增
   `TargetEnforcementGeneration`，并相应递增 `SnapshotRevision`。
6. 从剩余 Active Decisions 校验或重建 Desired Ban Projection，并与 Allowlist、
   Protected Targets、Firewall Config 和 Infrastructure Schema 组成 Desired Firewall
   Snapshot；校验 revision/generation 一致性。
7. 如果 Maintenance 功能已启用，读取其持久化状态。
8. Probe Backend。

后续必须显式分支：

```text
Maintenance enabled
  → 不执行 Firewall Repair
  → 启动 Source/Detection 和管理面
  → Health 标记 Maintenance

Probe/Snapshot success
  → Initial Reconcile
  → 启动 Source/Detection 和管理面

Probe/Snapshot failure
  → Enforcement 标记 NotReady/Degraded
  → 启动 Source/Detection 和管理面
  → 后台 Probe 恢复
```

Backend 不可用时仍必须持久化安全意图。Backend 恢复只触发 Probe，并严格遵守对应
Infrastructure / Policy / Target Retry budget，不得因 healthy 状态变化自动清零预算。

### 12.3 必测 Crash Points

| Crash Point | 重启后的预期行为 |
|---|---|
| Decision commit 前 | 不存在新 Decision，不产生 Firewall 副作用 |
| Decision commit 后、Firewall 前 | 从 Desired Ban Projection 与 Policy/Config 恢复完整 Snapshot 并 Apply |
| Firewall 成功后、Target Observed 回写前 | Snapshot/Probe 识别已存在；若 TargetEnforcementGeneration 未变化则确认当前结果，否则按新 generation 收敛 |
| Decision 终止后、Firewall Revoke 前 | 最终 Target Intent 已变化，重启后继续按新 TargetEnforcementGeneration 收敛 |
| Firewall Revoke 后、Observed 回写前 | Snapshot 识别已不存在并收敛 |
| Infrastructure/Policy/Target attempt 已持久化、外部调用前 | 重启先 Probe；对应 attempt 不回退，继续使用同一 domain 剩余预算 |
| Firewall 调用超时、结果未知 | 对应 Observed=Unknown；重试写操作前必须先 Probe |
| 无关 Target 更新导致 SnapshotRevision 前进 | 不得使已确认且 generation 未变化的另一个 Target 永久无法 Converged |

每个 Crash Point 必须有自动故障注入测试。

---

## 13. SQLite 最小 v1 Contract

M0 只冻结两条 Fake Slice 使用的最小表，不一次设计 Phase 1 全量数据库。

最小集合：

```text
schema_migrations
sources（最小 identity）
parsers（最小 identity/version）
rules（最小 identity/version）
parser_versions/rule_versions（durable inbox 方案需要）
allowlists（最小 canonical range）
source_file_generations
source_checkpoints
processing_receipts 或 durable_inbox（二选一）
alerts
decisions
enforcement_states
infrastructure_reconcile_state
policy_reconcile_state
target_reconcile_state
audit_logs
```

具体是否合并 Reconcile state 表由 migration Spike 决定，但逻辑上必须能分别持久化三个
failure domain，禁止只有一个无法区分 Infrastructure/Policy/Target 的 retry counter。

M0-D migration 必须明确：

- 主键、外键和 `PRAGMA foreign_keys = ON`。
- nullable、check、unique 和 index。
- 状态枚举的稳定存储值。
- 时间统一格式和精度。
- canonical CIDR 表示。
- event identity、replay key，以及 Terminal Processing Record 的保留期和清理水位。
- receipt pipeline 的 terminal record 不得早于该 Source 已持久化且不会回退的 checkpoint
  安全边界被清理。
- durable inbox item 在 ProcessingComplete、无 replay/reprocess 引用及保留条件满足前不得
  清理。
- File generation 必须先于该 generation 第一条 RawRecord durable persist。
- durable inbox 引用的 Parser/Rule revision 必须不可变，并在最后一个引用 item 终结前
  禁止清理。
- Automatic/Manual Active Decision 各自的唯一约束与 replace 事务。
- `TargetProjectionRevision` 与 `TargetEnforcementGeneration` 的持久化约束。
- Infrastructure / Policy / Target 三类 retry domain 的 revision/generation、RetryEpoch、
  attempt budget 和状态持久化约束。
- Critical Audit 与业务状态同事务提交，并具有数据库级幂等约束。
- schema version 和 forward migration 规则。
- WAL、busy_timeout 和短事务要求。

### 13.1 Durability Failure Domain

M0-D 必须明确文档中 `durable commit/persist` 抵御的故障域，至少逐项声明：

```text
process crash / SIGKILL
OS crash / reboot
machine power loss
```

禁止只写“durable”而不说明保证等级。

M0-B 必须验证 SQLite WAL 下所选 `PRAGMA synchronous` 模式及 fsync 行为。若 Phase 1
只承诺 process-crash durability，必须显式写入 Contract；若承诺 OS/power-loss 后
Source checkpoint 也不得越过真正持久化的 inbox/receipt，则所选 synchronous 模式必须
通过故障测试证明。

`synchronous`、checkpoint 策略和性能取舍最终由 M0-D Config/SQLite Contract 冻结，不能
仅依赖 SQLite 默认值。

必须验证：

- 空库可以一次迁移到 v1。
- 重复执行 migration 不破坏数据。
- migration 失败不会留下半迁移状态。
- 两个并发触发最多生成一个 Active Automatic Decision。
- Automatic/Manual 并发重复操作分别符合第 10.5、10.6 节语义。
- Decision、Critical Audit 与 Desired Ban Projection 不会出现部分提交。
- rename/create 后新 generation 已处理、旧 generation checkpoint 未推进时 crash，
  重启后新 generation Delivery ID 不变。
- 同一 Target 的投影内部变化但规范化执行 Intent 不变时，不产生新的 Target retry budget。
- Allowlist add/remove 改变 Target 最终执行 Intent 时，产生新的
  `TargetEnforcementGeneration`。
- Infrastructure failure 不批量消耗 Target budgets；Target failure 不消耗 Infrastructure
  或 Policy budget。

`sources`、`parsers`、`rules`、`allowlists` 在 M0 只包含满足 Fake Slice 和外键完整性所需
的最小身份、版本或 canonical range 字段；完整业务列以及 `notifications`、`users` 等表
在对应里程碑开始前通过后续 migration 冻结。

---

## 14. Firewall Backend 行为 Contract

### 14.1 声明式行为

Backend 必须提供下列行为，但最终 Go 方法签名只能在 M0-B Spike 后冻结：

```text
Probe capabilities
Ensure managed infrastructure
Snapshot managed state and read-only foreign context
Converge complete Desired Firewall Snapshot
Remove managed infrastructure
```

禁止以一次性 `Add/Delete` 作为唯一抽象。

Desired Firewall Snapshot 至少包含：

```text
schema revision
managed infrastructure
allowlist
protected targets
desired ban targets
normalized target enforcement intents
target timeout attributes
INPUT/FORWARD scopes
IPv4/IPv6 scopes
snapshot revision
policy revision
infrastructure revision
per-target projection revision
per-target enforcement generation
```

Backend operation plan 必须能够标注每个 mutation 属于 Infrastructure、Policy 或 Target
failure domain，以便 attempt/retry budget 正确记账。

### 14.2 所有权边界

Backend 只能修改稳定命名且带 owner/version 标记的 Guard-owned：

```text
table
chain
set
rule
hook/jump
```

必须满足：

- Snapshot 区分 ManagedState 与只读 ForeignContext。
- Sync 禁止删除非 Guard 对象。
- Cleanup 必须明确为 `RemoveManagedInfrastructure` 语义。
- Probe 失败时不得根据 DB 推测实际规则。
- uninstall/purge 只能删除 Guard-owned 对象。

发现同名对象但 owner/version 标记缺失或不匹配时，Backend 必须返回
`OwnershipConflict`、停止本次变更、保持对象不变并进入 Degraded。Ensure、Sync、
uninstall 和 purge 均禁止接管、覆盖或删除该对象。

### 14.3 Golden State

每个生产 Backend 在进入 M6 实现前必须有 Golden State fixture，至少包含：

```text
object names
owner/version markers
hook and relative priority
jump position
set flags
timeout behavior
IPv4/IPv6 scope
INPUT/FORWARD scope
idempotent reinstall behavior
uninstall behavior
```

每个可执行 fixture 必须包含：

```text
environment and capabilities
initial managed snapshot
initial read-only foreign context
desired snapshot revision
policy revision
infrastructure revision
per-target enforcement generations
expected operation plan + failure domain classification
expected managed snapshot
expected unchanged foreign context
repeated-apply snapshot
```

动态 handle、counter、输出排序等非确定字段必须在比较前 canonicalize。

### 14.4 M0 nftables Spike

M0 只要求一个真实 nftables Spike，用于验证：

- inet table/base chain 是否进入实际 INPUT/FORWARD 路径。
- 与 UFW、Docker 的相对执行顺序。
- batch transaction 的原子能力。
- Snapshot 和 drift 检测。
- 重复 Ensure 的幂等性。
- foreign rules 不被修改。

具体 hook priority 和命令必须经过实验后冻结，禁止在文档中凭空指定。

Spike 只能在隔离 VM 或专用测试环境执行，禁止在生产 Firewall 上试验。

Spike 产物必须记录发行版、内核、nft、UFW、Docker 的版本和启用状态。结论只对
已验证的基线组合有效；其他组合必须标记为 `Unverified` 或 `Unsupported`，留到 M6
对应 Backend 子里程碑验证，禁止从单一环境外推。

iptables-nft、iptables-legacy、ipset、UFW 和 Docker 的完整 Golden State 在 M6
对应子里程碑前分别冻结。

### 14.5 Apply-confirm（M6/M10 前置 Gate）

Apply-confirm 不进入两条 M0 核心 Slice，也不属于 M0 Frozen 的强制实现产物；它在
开放真实 Firewall 配置修改和高风险管理操作前必须单独冻结 Schema、状态机、跨重启
回滚测试和 Go/No-Go Gate。

Apply-confirm 只用于可能导致管理面失联的高风险配置变化：

- Firewall integration/hook 变化。
- Web listen 变化。
- 影响当前管理来源的 Allowlist 变化。
- 手动 Ban 当前管理 CIDR。

回滚计划、确认截止时间和 Guard-owned 前一 `SnapshotRevision` 必须在 Apply 前持久化。
Agent 重启后发现未确认变更时必须继续回滚。

普通自动 Ban 不需要管理员确认。

---

## 15. Config、API、CLI 与权限边界

### 15.1 Config Ownership

不能只用“YAML=runtime、SQLite=domain”代替逐字段设计。

M0-D 必须产出 ownership matrix：

| 配置项 | 唯一权威源 | 可热更新 | 需要重启 | 是否敏感 |
|---|---|---:|---:|---:|
| 进程监听地址 | YAML | 否 | 是 | 否 |
| DB 路径 | YAML | 否 | 是 | 否 |
| 日志级别 | YAML | 由 M0 冻结 | 由 M0 冻结 | 否 |
| Detection Rule | SQLite | 是 | 否 | 否 |
| Allowlist | SQLite | 是 | 否 | 否 |
| SMTP Credential | M8 前冻结 | M8 前冻结 | M8 前冻结 | 是 |

同一字段禁止同时由 YAML 和 Web/SQLite 写入。Web 对 YAML-owned 字段只能只读
展示，除非未来引入明确的配置写回机制。

### 15.2 API 与 CLI

M0 只冻结：

- API version prefix，例如 `/api/v1/`。
- 统一错误 envelope、request ID 和错误码稳定性原则。
- CLI 的 stdout/stderr 分工、`0/非 0` 语义和查询命令 `--json` 原则。

完整资源 API、分页和全部退出码在对应里程碑开始前冻结，不进入 M0 核心 Gate。

### 15.3 进程权限 ADR

M0 必须明确选择并记录以下方案之一：

1. root 单进程，并明确接受 Web/Parser 漏洞等同主机 root 的风险及 systemd hardening；或
2. 非特权 Agent + 最小权限 Enforcer，并冻结 IPC、Unix Socket owner/mode、允许
   的命令集合和身份校验。

无论选择哪种方案：

- CLI 禁止绕过业务层直接写 SQLite。
- 配置、DB、Socket 和 secret 必须有明确 owner/mode。
- systemd 必须冻结 User/Group、Capabilities、NoNewPrivileges、ProtectSystem 和
  ReadWritePaths。
- 日志、Audit、Doctor、Web 和 CLI 禁止输出 secret。

### 15.4 Web Security 里程碑 Gate

Web/Auth 不属于两条 M0 核心 Slice，但在 M1 Auth 开发前必须另行冻结：

```text
管理员 bootstrap
Argon2id 参数与升级策略
服务端 Session 存储、TTL、轮换和吊销
Cookie Secure/HttpOnly/SameSite
CSRF 与 Origin 校验
登录限速和失败审计
reverse proxy 信任边界
session key 存储与轮换
```

该 Gate 未完成时，只能创建不含登录和管理写操作的 HTTP 空壳。

---

## 16. Resource Limits Contract

以下 V0.4 数值作为对应里程碑的设计基线：

| 项目 | 基线 |
|---|---:|
| max log line | 64 KiB |
| max regex | 4 KiB |
| max capture fields | 32 |
| max parsers/source | 100 |
| max distinct values/group | 1024 |
| Reconcile interval | 60s |
| Firewall SafetyGrace | 5m |
| Enforcement automatic retries | 5 次，1s/5s/30s/5m/15m |

M0-B 只需要通过 Fake Slice 和故障测试冻结：

- Raw/Event/Reconcile queue capacity。
- checkpoint interval 和 record threshold。
- graceful shutdown timeout。

以下数值不进入 M0 Gate，分别在 M3/M4 开始前基于实际实现和基准冻结，禁止为取得
数值而在 M0 擅自引入依赖：

- Grok expansion 上限。
- CEL static/runtime cost 和 wall-clock timeout。
- global/per-rule active groups。
- window size 合法范围。
- Parser/Detection 相关数值是否沿用上表基线。

每个限制必须同时定义：

```text
配置键
默认值
合法范围
触顶行为
错误码
Metric
Health 影响
```

---

## 17. M0 Contract Tests

### 17.1 Source Slice

至少覆盖：

1. receipt pipeline：Parser/Detection 完成前 crash，SourceDurable 不成立，checkpoint 不前进。
2. durable inbox：inbox durable commit 后、Parser/Detection 完成前 crash，checkpoint 可以按
   contiguous SourceDurable 前进；重启后 item 仍继续直到 ProcessingComplete。
3. durable outcome/terminal record commit 后、checkpoint flush 前 crash，重放不产生重复副作用。
4. 两条记录乱序达到 SourceDurable，只提交最高连续 SourceDurable position。
5. 多 Parser 中一个失败时，按冻结策略进入成功或终态拒绝；receipt pipeline 未
   ProcessingComplete 前不得 checkpoint，durable inbox 已 SourceDurable 的 checkpoint 不回退。
6. queue full 产生 backpressure，不静默 drop。
7. Poison Record 进入终态后不永久阻塞 receipt pipeline checkpoint 或 durable inbox completion。
8. SIGTERM drain 成功时 checkpoint 与 Terminal Processing Record 正确 flush。
9. shutdown timeout 后重启，允许重复读取/恢复处理但不丢未完成记录。
10. rename/create 后新旧 generation 乱序完成，checkpoint 仍按 DeliverySequence 连续推进。
11. 新 generation 第一条 RawRecord 发出前 generation 已持久化。
12. 新 generation 已处理、旧 generation checkpoint 未推进时 crash，重启后 Delivery ID 不变。
13. durable inbox 绑定可恢复 Parser Set + Rule Catalog snapshot；receipt pipeline 的未提交
    记录按当前版本重评，两种方案都不产生重复持久化副作用。
14. receipt pipeline 的 Critical Audit 提交失败时业务事务回滚且 checkpoint 不推进；durable
    inbox 的 Critical Audit 提交失败时 outcome 和 ProcessingComplete 标记回滚，inbox item
    保持可恢复，已基于 inbox commit 推进的 Source checkpoint 无需回退。
15. copytruncate fast-regrow 能检测时产生 DataLossSuspected；不能检测时由能力测试确认
    known limitation，不伪造 at-least-once 保证。
16. Generation registry 在 checkpoint 尚未安全越过，或仍有 inbox/terminal/replay 引用时
    不得清理；满足全部清理条件后重启不再依赖该 row。
17. 同一个 `EventID + RuleID/Version` 已进入 Window 后 SQLite outcome transaction 失败，
    同进程 retry 不得再次增加 count/distinct_count。
18. durable inbox Source 阶段不要求预知最终 applicable rules；SecurityEvent 产生后只能从
    inbox 绑定的冻结 Rule Catalog 中求值。
19. 所选 SQLite durability 配置通过声明故障域的 crash/reboot/power-loss 测试或明确标注
    不承诺的故障域。

### 17.2 Decision/Enforcement Slice

至少覆盖：

1. 五个失败事件只生成一个 Active Automatic Decision。
2. 同 Rule + Target 并发触发只生成一个 Active Automatic Decision。
3. 重复触发只更新 suppression 字段，不延长到期时间。
4. 多 Decision 同 Target，仅最后一个结束后 Ban Projection 才为 Absent。
5. 永久 Decision 与有限 Decision 共存时最终 Present Target 不设置 native timeout。
6. Apply 失败后 Decision 保持 Active，Target Domain 进入 Retry/Degraded。
7. Decision 在首次 Apply 前过期，不再执行 Ban。
8. Revoke 失败时最终 Target Intent=Absent，Observed 不伪装为 Absent。
9. Firewall drift 可以收敛，foreign rules 保持不变。
10. `TargetEnforcementGeneration` 变化时旧 Target Reconcile 结果不能覆盖新状态。
11. 无关 Target 仅导致 SnapshotRevision 前进时，不得使 generation 未变化的已确认结果永久
    无法回写或 Converged。
12. Agent 停机期间跨过 ExpiresAt，重启时先 Expire，禁止短暂重新 Ban。
13. 有限期 Target 使用 `EffectiveUntil + SafetyGrace`，到期后移会刷新 timeout。
14. 同名 foreign 对象触发 OwnershipConflict，属于 Infrastructure Domain；Ensure/uninstall
    均保持对象不变。
15. 调用超时且结果未知时先 Probe，不盲目重复写操作。
16. Agent restart 不清零同一 InfrastructureRevision / PolicyRevision /
    TargetEnforcementGeneration 的对应 retry budget。
17. Backend health 反复抖动不重置任一 domain Retry budget、不突破最大 attempt。
18. 管理员 Retry 只为指定 failure domain 创建新 RetryEpoch 并写 Critical Audit。
19. 临时 Allowlist 不终止 Decision；移除后未过期 Ban 恢复。
20. `/32` Allowlist 不撤销与其重叠的 `/24` Decision。
21. Allowlist 来源默认仍创建 Detection、Alert 和 Decision，但不产生实际 Ban；Rule 显式
    `exclude.source.allowlisted=true` 时不创建这些产物。
22. 完整 Desired Firewall Snapshot 同时收敛 infrastructure、Allowlist、Protected Targets
    和 Ban Projection。
23. 重复 Manual Ban 返回 AlreadyBanned；显式 replace 原子终止旧 Decision 并创建新 Decision。
24. BanMembership 相同但 PolicyCoverage/PolicyRelationDigest、NativeExpiry、TimeoutMode 或 Scope 不同仍判定为 drift。
25. Agent 持续运行跨过 ExpiresAt，在冻结延迟上限内终止 Decision、唤醒 Reconciler，并
    断言最终 Firewall Snapshot 为 `Absent`；M0 Fake Backend 上限为 `62s` 虚拟时间。
26. 启动时 Probe/Snapshot 失败仍启动 Source/Detection，Enforcement 为 NotReady/Degraded；
    后台恢复健康后先 Probe，且不重置已有 retry budget。
27. 两个 Active Decision 同 Target，其中一个 Expire 但最终 BanMembership/EffectiveUntil/Scope/PolicyCoverage
    完全不变：`TargetProjectionRevision` 递增，`TargetEnforcementGeneration` 不变，已耗尽
    Target Retry budget 不得自动复活。
28. Allowlist add/remove 与 Target 存在 Full/Partial overlap 时均更新 `PolicyCoverage/PolicyRelationDigest`
    并产生新的 `TargetEnforcementGeneration`；完全不相交的 Policy 变化不得更新该 Target generation；
    remove 后允许受影响 Target 的新 generation 使用新的有界 budget。
29. Infrastructure Ensure 连续失败只消耗 Infrastructure budget，不为 N 个 Target 各扣一次；
    单个 Target Apply 失败不消耗 Infrastructure/Policy budget。
30. Policy Apply 失败只消耗 Policy budget；PolicyRevision 变化但未影响某 Target 最终 Intent
    时不得重置该 Target budget。

### 17.3 Crash Matrix

第 12.3 节列出的全部 crash points 必须自动化，并断言：

```text
最终 DB Desired Ban Projection / Normalized Enforcement Intent
最终 Firewall Snapshot
最终 Observed Infrastructure / Policy / Target State
Decision History
Retry Domain State
Audit
```

---

## 18. M0 Go/No-Go Gate

只有全部满足以下条件，才允许标记 M0 Frozen：

### 18.1 Contract 完整性

- 七组关键 Contract 不存在影响实现的 TBD。
- SecurityEvent、Decision、Desired Ban Projection、Normalized Target Enforcement Intent
  和三个 Reconcile failure domain 只有一个可编译权威模型。
- 文档 precedence 已冻结；V0.4 中旧 Decision/Retry/CLI/Metrics/ADR 语义已在同一变更中
  同步修订，并通过一致性检查。
- Allowlist 是正交 Policy Exception，不会默认终止 Decision。
- Desired Ban Projection 与完整 Desired Firewall Snapshot 已分层。
- `TargetProjectionRevision` 与 `TargetEnforcementGeneration` 已分离，Retry budget 只绑定
  实际执行语义 generation。
- SourceDurable 与 ProcessingComplete 已分离；选定 delivery model 后不存在含混 ACK 语义。
- 重放身份、Poison/Terminal Processing Record 和 shutdown 行为已经冻结。
- File generation 持久化时点和 Processing Plan 版本边界已经冻结。
- Automatic/Manual Decision identity、Critical Audit 事务语义和 Detection Window 幂等边界
  已冻结。
- SQLite `durable` 的故障域及 synchronous/fsync 策略已经冻结。
- Infrastructure / Policy / Target failure domain 与各自 RetryEpoch/attempt budget 已冻结。
- 权限边界 ADR 已批准。

### 18.2 可执行验证

- 两条 Fake Vertical Slice 全部通过。
- 第 12.3 节全部 crash points 均能够最终收敛。
- receipt pipeline 或 durable inbox 的 SourceDurable 语义与 checkpoint 行为符合所选模型。
- kill/restart 测试证明未持久化记录不会被 checkpoint 越过；durable inbox 已 checkpoint 的
  未完成 item 仍可恢复到 ProcessingComplete。
- 重放和同进程 transaction retry 不会产生重复持久化副作用或重复 Window 贡献。
- 并发测试证明同 Rule + Target 最多一个 Active Automatic Decision。
- Manual Ban 重复/replace、Allowlist 可逆例外和完整 Firewall Snapshot 测试通过。
- DB 可以从空库迁移并在重启后恢复。
- Firewall drift 可以修复且 foreign rules 不变。
- TimeoutMode、NativeExpiry 和 Scope drift 可以被 Snapshot 识别。
- Target 投影内部变化但最终执行 Intent 不变时不得刷新 Target Retry budget。
- Allowlist/Policy 改变 Target 最终执行 Intent 时正确产生新 Target Enforcement Generation。
- Infrastructure / Policy / Target Retry budget 相互隔离，任何一个 domain 达到上限后只使
  对应 domain Degraded，不发生无限 Reconcile。
- Backend health flap 不会重置预算；管理员 Retry 使用指定 domain 的新 RetryEpoch。
- 正常运行期 Expiration、Critical Audit 和 Detection Window transaction failure 测试通过。
- 无关 Target 高频变化不会导致另一个 Target 永久无法 Converged。
- SQLite durability 测试与声明的 process/OS/power-loss 保证一致。

### 18.3 产物一致性

- migration SQL 是存储 Schema 的唯一权威源。
- Config Schema 是配置字段的唯一权威源。
- Contract Tests 是行为验收的唯一可执行证据。
- Markdown 中不存在与代码、SQL 或 Schema 冲突的复制定义。

任一项不满足时，结论必须为 No-Go；不得以“实现时再决定”放行。

---

## 19. M0 产物清单

建议产物按以下目录组织；实际代码路径由工程语言 ADR 冻结：

```text
docs/contracts/
  guard-phase-1-m0-contract-freeze.md
  core-model.md
  decision-enforcement.md
  source-delivery.md
  firewall-behavior.md

docs/adr/
  ...

schema/
  config-v1.schema.*

migrations/
  ...

tests/contracts/
  ...
```

不要求为了满足目录清单创建空文件。只有对应产物已有可验证内容时才落盘。

---

## 20. 里程碑衔接

M0 Frozen 后：

```text
M1 Runtime
  可以实现已批准的语言、配置、DB、Auth 和进程权限方案

M2 Sources
  复用 SourcePosition、SourceDurable、ProcessingComplete、checkpoint 和 replay Contract

M3 Parser
  冻结多 Parser 匹配及错误语义

M4 Detection
  复用稳定 Event ID，并冻结窗口触发和清理语义

M5 Decision Model
  直接实现已冻结的 Decision/Desired Ban Projection

M6 Firewall
  按 Backend 子里程碑逐个完成 Golden State 和真实实现

M7 Reconciliation
  实现 TargetProjectionRevision / TargetEnforcementGeneration、三类 failure domain、有界重试、Degraded 和 drift recovery
```

### 20.1 Maintenance Mode 的 M7 前置 Gate

Maintenance 不进入 M0 两条核心 Slice，但在 M7 实现前必须冻结：

- Maintenance 状态持久化，并记录操作者、时间、原因和 Critical Audit。
- Startup 在 Initial Reconcile 前读取 Maintenance。
- Maintenance 开启时 Source、Parser、Detection、Decision 到期和 Desired Ban Projection
  更新继续运行，仅暂停 Firewall Apply/Revoke/Repair。
- Maintenance 期间 Agent crash/restart 禁止自动执行 Full Reconcile。
- disable 后立即执行一次完整 Desired Firewall Snapshot Reconcile。
- Health 必须区分主动 Maintenance 与故障 Degraded。

Web、Notification、Operational Audit 查询与 Retention、Upgrade 等模块仍需在相应
里程碑前完成各自 Contract，不因 M0 Frozen 自动视为已经设计完成。

---

## 21. 最终准入结论模板

M0 评审只能使用以下结论之一：

```text
GO

所有 M0 Gate 已通过。
Phase 1 可以按已冻结 Contract 拆分并行开发任务。
```

或：

```text
NO-GO

未通过项：<Gate ID / Contract / Test>
影响：<会产生的实现分歧或一致性风险>
责任产物：<代码 / migration / Schema / Test / ADR>
重新评审条件：<可验证条件>
```

禁止使用“基本通过”“开发时再补”“不影响主流程”等模糊结论绕过 Gate。
