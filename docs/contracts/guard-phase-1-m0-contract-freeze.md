# Guard Phase 1 M0 — Contract Freeze 技术规格 V0.1

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
  → ACK/checkpoint
```

```text
Decision
  → Desired Enforcement State
  → Reconciler
  → Fake Firewall
  → Observed Enforcement State
```

---

## 3. 范围边界

### 3.1 M0 必须完成

- SecurityEvent、SourcePosition 和稳定事件身份。
- Decision 与 EnforcementState 的职责、状态和不变量。
- Source ACK、checkpoint、重放、背压和优雅停机。
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

Desired Enforcement State
    = 可从 Active Decisions 重建的物化投影

Firewall Snapshot
    = 外部执行状态的当前观察结果

DB Observed State
    = 最近一次 Firewall 观察缓存，不是外部事实源
```

必须满足：

1. Decision 写入、终止和对应 Desired Projection 更新必须在同一个 SQLite
   写事务内完成。
2. Desired Projection 损坏或缺失时，必须能够从 Active Decisions 全量重建。
3. Reconciler 禁止根据 DB 中旧的 Observed State 猜测 Firewall 当前状态。
4. Probe 或 Snapshot 失败时，Observed State 必须为 `Unknown`。
5. 所有投影和 Reconcile 操作必须携带 generation，旧 generation 禁止覆盖新状态。

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
  ├─ ACK 屏障
  └─ Crash Matrix
          ↓
M0-B 风险 Spike
  ├─ SQLite 并发唯一约束
  ├─ Source replay identity
  ├─ nftables hook/priority/atomic batch
  └─ Backend Snapshot/Ensure 可行性
          ↓
M0-C 可执行 Slice
  ├─ Source → durable outcome → ACK
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

`DeliverySequence` 是 Guard 为每个 Source 持久化分配的单调递增序号，只用于处理
顺序和连续 ACK 判断。外部 Position 负责恢复日志源，Sequence 负责表达 Guard 内部
的全序，两者不得互相替代。

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

- `file_generation` 在 Guard 识别出一个新的文件代际时生成并持久化。
- 同一路径 rename/create 后，新旧文件必须属于不同 generation。
- 记录字节范围统一为半开区间
  `[record_start_offset, record_end_offset)`；`record_end_offset` 是下一条未读记录的
  起点，checkpoint 恢复时从该位置开始。
- generation 切换必须有明确屏障：旧 generation 已读取记录与新 generation 记录都
  分配同一 Source 下连续的 DeliverySequence；checkpoint 只能提交最高连续 Sequence
  对应的 generation/offset。
- canonical identity 禁止只使用日志内容 hash。

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

---

## 8. ACK、checkpoint 与重放 Contract

### 8.1 ACK 屏障

“写入 Detection 内存 Channel”不构成 ACK。

一条 RawRecord 只有在满足以下任一条件时才能 ACK：

1. 所有适用 Parser 和 Detection Rule 已处理完成，并且本记录产生的 Alert、
   Decision、Audit 等必要持久化结果已经提交；或
2. 该记录和处理状态已经写入可恢复的 durable inbox，后续可以在重启后继续处理；或
3. 该记录已按明确的 poison/resource policy 进入终态拒绝，并已写入错误记录、
   Metric 和 Audit。

Firewall Apply、Firewall Revoke 和 SMTP 发送不属于 ACK 前置条件。

### 8.2 多 Parser ACK

一条 RawRecord 只有在所有适用 Parser 的全部输出均达到 ACK 条件后才能 ACK。

M0 只冻结“所有已调度 Parser 任务均已进入成功或终态拒绝后，RawRecord 才能
ACK”这一跨模块不变量。Parser 的 first-match、all-match、priority 和 error
continuation 语义在 M3 开始前冻结。Fake Slice 默认使用一个 Parser，不得把该默认
行为扩展成产品 Contract。

### 8.3 连续 checkpoint

Checkpoint Manager 只能按同一 Source 的 DeliverySequence 提交：

```text
highest contiguous ACKed position
```

后续记录已经完成但前序记录未完成时，禁止越过空洞推进 checkpoint。

### 8.4 重放幂等

M0-B 必须在以下两种方案中选择并验证一种：

- durable inbox + 唯一 Delivery/Event ID；或
- 同步 Detection transaction + 持久化 receipt/唯一 Event ID。

无论选择哪种方案，都必须证明：

- 未完成记录不会因 checkpoint 提前推进而丢失。
- 重放不会创建重复 Alert、Decision 或 Audit 副作用。
- 重放是否重新进入内存 Detection Window 必须有明确产品语义。

同步 Detection transaction 方案中，即使一条记录没有产生 Alert 或 Decision，也必须
写入 processing receipt；receipt、必要业务结果和对应幂等键必须在同一个事务中提交。

用于去重的 receipt、inbox identity 或 tombstone，在对应 Source checkpoint 已经
持久化越过该 Position 前禁止删除。Alert、Decision 等下游持久化副作用仍必须拥有
独立唯一键，不能只依赖 receipt。

未来 Notification Contract 必须复用稳定 Event/Decision ID 建立唯一幂等键；在该
Contract 冻结前，Notification Job 不进入 M0 Slice 的强制证明范围。

Phase 1 选择保持 V0.4 的内存 Window 语义：Agent restart 后 Window 清空。
已经完成处理的稳定 Event ID 即使因 checkpoint 未 flush 而重放，也不得再次贡献
窗口计数；此前尚未形成 Decision 的部分窗口状态可能丢失。这是一项明确的检测
连续性限制，Source at-least-once 不等于 Detection 跨重启 exactly-once。

### 8.5 Poison Record

确定性解析失败、超长行或资源限制拒绝不能永久阻塞连续 checkpoint。

Poison Record 必须：

1. 在数据库中写入以 Delivery ID 为唯一键的终态 receipt。
2. receipt 至少包含 SourcePosition、failure stage、error code、sanitized error、
   terminal action 和 occurred time。
3. receipt 提交后增加低基数 Metric；Metric 不是 ACK 屏障或恢复凭据。
4. 对影响 Source 连续性的拒绝写 Audit；受限错误样本是否保留由配置决定。
5. 禁止记录凭据或未经截断的敏感日志内容。

完成上述动作后允许 ACK。

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
rule_disabled
allowlisted
system_cleanup
maintenance_cleanup
```

不新增与 `Revoked + EndReason` 重叠的 `Cancelled`。

### 10.3 创建与终止

- 自动检测确认后，在 DB 事务中直接创建 `Active` Decision。
- `ExpiresAt` 从 Decision 创建时计算，不从 Firewall Apply 成功时计算。
- Decision 在首次成功 Apply 前已经过期时，禁止再执行 Ban。
- 到期时先在 DB 事务中终止 Decision 并重算 Desired Projection，Firewall 删除由
  Reconciler 完成。
- 人工撤销和 Allowlist 导致的撤销使用同一事务原则。

### 10.4 同 Rule + Target 幂等

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

---

## 11. Desired 与 Observed Enforcement Contract

### 11.1 Desired Projection

同一 canonical Target 的所有 Active Decisions 聚合为一个 Desired Projection。

规则：

```text
active decision count > 0
  → Desired = Present

active decision count == 0
  → Desired = Absent
```

`EffectiveUntil`：

- 全部 Active Decisions 都有到期时间时，取最大值。
- 任一 Active Decision 为永久时，`EffectiveUntil = nil`，不得设置 native timeout。

支持 native timeout 的 Backend 对有限期 Target 使用：

```text
native_timeout = EffectiveUntil + SafetyGrace
```

Native timeout 只是 Agent crash 时的 failsafe，不终止 Decision，也不更新 Desired
Projection。`EffectiveUntil` 后移时必须刷新 timeout；永久 Target 禁止遗留旧 timeout。

### 11.2 Observed State

Observed State 至少包含：

```text
Unknown
Present
Absent
```

并记录：

```text
ObservedAt
Backend
Generation
LastError
```

Observed State 是缓存。真正的当前状态只能通过 Firewall Snapshot/Probe 获得。

Observed State 只能来自成功的 Snapshot/Probe，或者 Backend Contract 明确保证具有
权威确认语义的 Ensure 返回值。操作超时、连接中断或结果不确定时必须写
`Unknown`，并在再次执行写操作前先 Probe。

`Drifted` 不是独立的外部事实状态，而是 Desired 与最新 Observed 不一致时计算出的
派生结论：

```text
Desired=Present, Observed=Absent
或
Desired=Absent, Observed=Present
```

### 11.3 Reconcile Status

执行状态冻结为：

```text
Pending
Applying
Converged
RetryWaiting
Degraded
```

必须满足：

- Apply 失败后 Decision 保持 Active。
- Revoke 失败时保持 `Desired=Absent, Observed=Present/Unknown`。
- 自动重试使用 V0.4 的有界退避，不允许每次 Reconcile 无限复活。
- 达到上限后进入 `Degraded`，Desired 不被删除。
- 管理员 Retry、Backend unhealthy→healthy，以及确实改变该 Target Desired State 或
  Backend 执行语义的配置变化可以重新触发。日志级别等无关配置变化不得重置预算。
- Reconcile 开始和回写时必须校验 generation。
- 每次外部写操作前必须先持久化 attempt；调用期间 crash 仍计入预算。
- Retry budget 按 Desired generation 维护，普通进程重启不得清零。

### 11.4 单写者

同一 Agent 内只能有一个 Firewall Reconcile 写执行器。

多个事件可以并发产生 Decision，但 Desired Projection 写入和 Firewall Sync 必须通过
明确的 SQLite 写事务与单写者执行器序列化。

---

## 12. Persist Intent 与崩溃恢复 Contract

### 12.1 Ban 顺序

```text
BEGIN DB TX
  insert/update Active Decision
  recompute Desired Projection
  increment generation
COMMIT

Reconciler Snapshot
Reconciler Ensure/Sync
Firewall operation
Update observed projection for the same generation
```

禁止先修改 Firewall，再写 Decision。

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
3. 按配置路径打开 DB 并验证 migration。
4. 加载并验证 SQLite domain config。
5. 使用当前时钟，在同一事务中把所有 `ExpiresAt <= now` 的 Active Decision 转为
   Expired，重算受影响 Target 的 Desired Projection 并递增 generation。
6. 从剩余 Active Decisions 校验或重建 Desired Projection。
7. Probe Backend 并获取 Snapshot。
8. 执行一次完整 Reconcile。
9. 启动 Source 消费和管理面。

Backend 不可用时，Source/Detection 必须继续运行并持久化安全意图；Health 标记为
Degraded，enforcement readiness 标记为 Not Ready。Backend 恢复后按有界重试规则
重新收敛，禁止丢弃已经形成的安全意图。

### 12.3 必测 Crash Points

| Crash Point | 重启后的预期行为 |
|---|---|
| Decision commit 前 | 不存在新 Decision，不产生 Firewall 副作用 |
| Decision commit 后、Firewall 前 | 从 Desired Projection 恢复并 Apply |
| Firewall 成功后、Observed 回写前 | Snapshot 识别已存在，不重复产生副作用 |
| Decision 终止后、Firewall Revoke 前 | Desired=Absent，重启后继续 Revoke |
| Firewall Revoke 后、Observed 回写前 | Snapshot 识别已不存在并收敛 |
| attempt 已持久化、外部调用前 | 重启先 Probe；attempt 不回退，继续使用剩余预算 |
| Firewall 调用超时、结果未知 | Observed=Unknown；重试写操作前必须先 Probe |

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
source_checkpoints
processing_receipts 或 durable_inbox（二选一）
alerts
decisions
enforcement_states
audit_logs
```

M0-D migration 必须明确：

- 主键、外键和 `PRAGMA foreign_keys = ON`。
- nullable、check、unique 和 index。
- 状态枚举的稳定存储值。
- 时间统一格式和精度。
- canonical CIDR 表示。
- event identity、replay key，以及 processing receipt 的保留期和清理水位。
- receipt 不得早于该 Source 已持久化且不会回退的 checkpoint 安全边界被清理。
- schema version 和 forward migration 规则。
- WAL、busy_timeout 和短事务要求。

必须验证：

- 空库可以一次迁移到 v1。
- 重复执行 migration 不破坏数据。
- migration 失败不会留下半迁移状态。
- 两个并发触发最多生成一个 Active Decision。
- Decision 与 Desired Projection 不会出现部分提交。

`sources`、`parsers`、`rules` 在 M0 只包含满足 Fake Slice 和外键完整性所需的最小
身份/版本字段；完整业务列以及 `notifications`、`users` 等表在对应里程碑开始前
通过后续 migration 冻结。

---

## 14. Firewall Backend 行为 Contract

### 14.1 声明式行为

Backend 必须提供下列行为，但最终 Go 方法签名只能在 M0-B Spike 后冻结：

```text
Probe capabilities
Ensure managed infrastructure
Snapshot managed state and read-only foreign context
Converge desired target state
Remove managed infrastructure
```

禁止以一次性 `Add/Delete` 作为唯一抽象。

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
desired snapshot + generation
expected operation plan
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

回滚计划、确认截止时间和 Guard-owned 前一 generation 必须在 Apply 前持久化。
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

1. Parser/Detection 完成前 crash，checkpoint 不前进。
2. durable outcome commit 后、checkpoint flush 前 crash，重放不产生重复副作用。
3. 两条记录乱序完成，只提交最高连续 ACK。
4. 多 Parser 中一个失败时，按冻结策略处理且不越过未终结记录。
5. queue full 产生 backpressure，不静默 drop。
6. Poison Record 进入终态后允许连续 checkpoint 前进。
7. SIGTERM drain 成功时 checkpoint 正确 flush。
8. shutdown timeout 后重启，允许重复但不丢未完成记录。
9. rename/create 后新旧 generation 乱序完成，checkpoint 仍按 DeliverySequence 连续推进。

### 17.2 Decision/Enforcement Slice

至少覆盖：

1. 五个失败事件只生成一个 Active Decision。
2. 同 Rule + Target 并发触发只生成一个 Active Decision。
3. 重复触发只更新 suppression 字段，不延长到期时间。
4. 多 Decision 同 Target，仅最后一个结束后 Desired 才为 Absent。
5. 永久 Decision 与有限 Decision 共存时不设置 native timeout。
6. Apply 失败后 Decision 保持 Active，执行状态进入 Retry/Degraded。
7. Decision 在首次 Apply 前过期，不再执行 Ban。
8. Revoke 失败时 Desired=Absent，Observed 不伪装为 Absent。
9. Firewall drift 可以收敛，foreign rules 保持不变。
10. generation 变化时旧 Reconcile 结果不能覆盖新状态。
11. Agent 停机期间跨过 ExpiresAt，重启时先 Expire，禁止短暂重新 Ban。
12. 有限期 Target 使用 `EffectiveUntil + SafetyGrace`，到期后移会刷新 timeout。
13. 同名 foreign 对象触发 OwnershipConflict，Ensure/uninstall 均保持对象不变。
14. 调用超时且结果未知时先 Probe，不盲目重复写操作。
15. Agent restart 不清零同一 Desired generation 的 retry budget。

### 17.3 Crash Matrix

第 12.3 节列出的全部 crash points 必须自动化，并断言：

```text
最终 DB Desired
最终 Firewall Snapshot
最终 Observed Projection
Decision History
Audit
```

---

## 18. M0 Go/No-Go Gate

只有全部满足以下条件，才允许标记 M0 Frozen：

### 18.1 Contract 完整性

- 七组关键 Contract 不存在影响实现的 TBD。
- SecurityEvent、Decision、EnforcementState 只有一个可编译权威模型。
- ACK 屏障、重放身份、Poison Record 和 shutdown 行为已经冻结。
- 权限边界 ADR 已批准。

### 18.2 可执行验证

- 两条 Fake Vertical Slice 全部通过。
- 第 12.3 节全部 crash points 均能够最终收敛。
- kill/restart 测试证明未完成记录不会被 checkpoint 越过。
- 重放不会产生重复持久化副作用。
- 并发测试证明同 Rule + Target 最多一个 Active Decision。
- DB 可以从空库迁移并在重启后恢复。
- Firewall drift 可以修复且 foreign rules 不变。
- 达到自动重试上限后进入 Degraded，不发生无限 Reconcile。

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
  复用 SourcePosition、ACK、checkpoint 和 replay Contract

M3 Parser
  冻结多 Parser 匹配及错误语义

M4 Detection
  复用稳定 Event ID，并冻结窗口触发和清理语义

M5 Decision Model
  直接实现已冻结的 Decision/Desired Projection

M6 Firewall
  按 Backend 子里程碑逐个完成 Golden State 和真实实现

M7 Reconciliation
  实现 generation、有界重试、Degraded 和 drift recovery
```

Web、Notification、完整 Audit、Retention、Upgrade 等模块仍需在相应里程碑前完成
各自 Contract，不因 M0 Frozen 自动视为已经设计完成。

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
