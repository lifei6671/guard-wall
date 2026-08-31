# Guard Phase 1 全面开发技术规格 V0.3

> M0 Contract Freeze + M1–M10 Development Baseline

---

## 1. 文档状态

| 项目 | 内容 |
|---|---|
| 文档定位 | Phase 1 规范性开发基线；文档存在不构成实现或验证证据 |
| 状态转换 | M0 按第 18 节判定 `Frozen/GO`；Phase 1 按第 36 节判定 `Released` |
| 实时执行状态 | [Phase 1 STATUS](../development/phase-1/STATUS.md) |
| 适用范围 | Guard Phase 1（M0–M10） |
| 架构基线 | [Guard 分布式日志驱动主机防护系统技术方案 V0.4](../guard-distributed-log-driven-host-protection-system-technical-design-v0.4.md) |
| 文件路径 | 保留 `m0-contract-freeze` 历史稳定入口；标题与正文已扩展为完整 Phase 1 规格 |
| 开发执行入口 | [Phase 1 开发状态与 M0 执行矩阵](../development/phase-1/README.md) |
| 目标 | 将架构语义下沉为可编译、可迁移、可故障注入验证的 Contract，并给出 M1–M10 可直接拆分的开发规格与验收 Gate |

本文中的“必须”“禁止”“只能”是规范性要求。本文使用 `Guard`
作为系统简称；产品展示名称、Go module path 与发布包名称不在本文中互相推导，
分别以 README、`go.mod` 与发布配置为权威源。

本文同时承担两种职责：

1. 第 2–18 节冻结 M0 核心正确性 Contract，指导 Spike、Fake Slice 和故障注入验证。
2. 第 22 节起规定 Phase 1 的统一工程基线、M1–M10 开发包、测试证据和 Release Gate。

只有第 18 节全部通过后，`M0 Core Contract` 才能从 `Specified` 改为
`Frozen/GO`，Phase 1 才允许按里程碑拆分为并行开发任务；这不代表 M1–M10
已经完成，也不改变整份文档的 `Not Released` 状态。
在此之前，可以执行 M0-A–M0-D 和第 3.3 节允许的骨架工作，不得把
“开始 M0 实验/实现”表述为“M0 已通过”。

### 1.1 权威关系

- V0.4 继续承担已发布的产品范围和总体架构基线。
- 本文是 Phase 1 开发期的规范性实现目标。对 Source、SecurityEvent、Decision、
  Enforcement、Firewall、Resource Limits 和故障恢复语义，本文明确修正
  V0.4 的地方以本文为准；但未通过对应 Gate 前，这些条款只能驱动
  Spike、Slice 和开发中代码，不得被表述为已验证的发布 Contract。
- M0 Frozen 后，对本文覆盖的 Source、SecurityEvent、Decision、Enforcement、
  Firewall、Resource Limits 和故障恢复 Contract，以 Frozen M0、对应 ADR 及可执行
  产物为实现权威。
- M0 Frozen 的同一变更必须同步修订 V0.4 中已过期的 Decision 状态、Retry CLI、
  Metrics 和 ADR。禁止长期保留两套权威语义。

至少需要同步处理：V0.4 的 `Pending/Revoking/Failed` Decision、Decision 级 Retry
字段、`guard-agent decision retry`、`guard_decision_failed_total` 和 ADR-017；相应能力
迁移到 Enforcement/Reconcile Contract。

### 1.2 规范状态与证据语义

每一项 Phase 1 能力只能处于以下状态之一：

```text
Specified
  = 规范已给出唯一实现目标，可编写 Spike/Slice/代码

Implemented
  = 权威代码、migration 或 Schema 已落盘，但不代表 Gate 已通过

Verified
  = 对应自动化测试、故障注入、Spike 或人工安全检查已形成可定位证据

Frozen
  = 所属里程碑的必需 Gate 全部通过，且权威产物已同步
```

文档中写明默认值、算法或状态机，只能证明 `Specified`；不得取代测试、
Spike 结果或 Release Evidence。

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

## 3. Phase 1 与 M0 范围边界

### 3.1 Phase 1 In Scope

Phase 1 交付单机 Standalone 的最小完整产品闭环：

- Linux 日志文件采集、解析、规则检测和持久化处理回执。
- 自动封禁、人工封禁、解封、过期和 allowlist / protected target 约束。
- nftables、iptables-nft/legacy、UFW 与 Docker 防护集成，以及声明式对账、失败重试和降级可观测性。
- SQLite 本地状态、版本化 migration、备份恢复和崩溃恢复。
- 最小管理 API、Web 管理面、CLI、通知与审计查询。
- 内置日志源、解析器和检测规则，以及安装、升级、卸载和运维文档。
- Contract、单元、集成、崩溃恢复、权限、安全和安装验收证据。

Phase 1 不以“代码已写完”为完成标准，而以第 36 节 Release Gate 全部通过为准。

### 3.2 M0 必须完成

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

### 3.3 可以与 M0 并行，但不得反向冻结 Contract

- 仓库目录和构建骨架。
- 日志、测试、lint、CI 基础设施。
- SQLite migration runner 骨架。
- HTTP router 空壳。
- Fake Backend 测试工具。

如果工程语言、前端技术栈或第三方依赖尚未通过 ADR 确认，禁止以“搭骨架”
为由新增依赖或锁文件。

### 3.4 不进入 M0，但仍属于 Phase 1 后续里程碑

- 第 32 节规定的最小管理 API、Web 页面和 OpenAPI 资源。
- React、Vite、shadcn/ui 或其他具体前端栈选型。
- 通知适配器、内置规则包和产品化安装能力。
- nftables、iptables-nft/legacy、UFW 与 Docker 集成的完整 Apply / Confirm / Recovery 实现。

这些内容不能反向阻塞 M0 核心链路；进入对应里程碑前，必须完成所需的
技术选型、接口细化和 Gate 用例。

### 3.5 Phase 1 Out of Scope

- Phase 2 Cluster、多节点协同和集中式控制面。
- GeoIP、Threat Intelligence、机器学习、自动 CIDR 聚合。
- Progressive Ban 和复杂关联检测。
- 跨节点全局速率、分布式一致性和高可用数据库。

Out of Scope 能力不得以预留扩展点为由增加 Phase 1 的依赖、状态机或兼容层。

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
| HTTP API | OpenAPI 文档及其生成 drift check |
| CLI 参数、输出和退出码 | CLI Contract/golden tests |
| Agent/Enforcer IPC v1 request frame 与操作 | `schema/ipc-v1.schema.json` 与 `schema/testdata/ipc-v1/` golden tests |
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
  ├─ Contract Tests
  └─ V0.4 旧 Decision/Retry/CLI/Metric/ADR 同步清理
```

M0-A、M0-B 可以并行处理互不依赖的研究项。

M0-C 必须基于 M0-A 的第一版行为不变量；Fake Slice 不是和 Contract 完全无依赖
的独立开发任务。

M0-D 只能冻结已经被 Spike 或 Slice 验证过的接口。

### 5.1 Phase 1 单轨实现定案

为避免同一模块出现两套不兼容实现，Phase 1 开发只允许使用下列路径。
这些选择当前状态为 `Specified`；对应 Spike 和 Contract Tests 通过前不得标记
`Verified` 或 `Frozen`。

| 主题 | Phase 1 唯一选择 | 必须的验证边界 |
|---|---|---|
| 工程语言 | Go；确切 toolchain 以 `go.mod` 为权威 | 编译、race、Linux 支持矩阵 |
| Source delivery | receipt pipeline + terminal `processing_receipt` | crash/replay、事务幂等、checkpoint 不越洞 |
| Parser 调度 | enabled Parser 全部执行（all-match），`priority + parser_id` 稳定排序 | 多 Parser 错误与版本切换 |
| Event ID | 版本化长度帧 + SHA-256 + lowercase base32hex | golden vectors、跨重启稳定 |
| Automatic Decision 唯一键 | `node_id + rule_id + canonical_target` partial unique index | SQLite 并发触发 |
| Manual Decision 唯一键 | `node_id + canonical_target` partial unique index | replace 原子性 |
| Reconcile | Infrastructure / Policy / Target 三个 failure domain，每个外部 batch 只属于一个 domain | 顺序、fencing、预算隔离、unknown result |
| SQLite | WAL + `synchronous=FULL` + `foreign_keys=ON` + `busy_timeout=5000ms` | PRAGMA read-back、SIGKILL/reboot/power-loss 证据边界 |
| 权限 | 非特权 `guard-agent` + 最小 root `guard-enforcer` | Unix Socket 身份、协议白名单、systemd hardening |
| Firewall 写入 | 单一 Enforcer 串行执行，声明式 Snapshot/Plan | 隔离环境中的 atomicity、ownership、drift |

Phase 1 明确不实现 durable inbox。它可以作为未来 ADR 的备选方案，但不得出现在
Phase 1 生产 Schema、恢复路径或必测矩阵中。

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

Phase 1 的 `DeliverySequence` 是 session-local，每个 Source processing session 从 1
开始分配。crash 后从已持久化 checkpoint 重读并重新分配，跨重启幂等依靠稳定
Delivery ID 和 `processing_receipt`，不依靠 Sequence 值相等。

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

`file_generation` 使用 128-bit CSPRNG 生成并编码为小写 hex，创建后不可变。
`lifecycle_state` 冻结为单向状态机：

```text
Open → Draining → Sealed → Retired
  └─────────→ Sealed
```

- `Open`：当前可继续读取的 generation。
- `Draining`：rename/create 后旧文件仍通过已打开 fd 读到 EOF。
- `Sealed`：不再产生新 RawRecord，但仍可能被 checkpoint、receipt 或 replay 引用。
- `Retired`：checkpoint 已安全越过本 generation 的最大 DeliverySequence，且无
  receipt/replay/reprocess 引用；达到 retention 后才允许删除。

generation row 必须在首条记录的 outcome/receipt 事务之前或同一事务提交。
rename/create 时旧 generation 进入 `Draining`，新 `(device_id, inode)` 创建 `Open`
generation；copytruncate 即使 inode 未变，也必须 seal 旧 generation 并为 offset 0
开始的数据创建新 generation。状态禁止回退或复活。

旧 generation 尚未 checkpoint、但新 generation 已产生 receipt 时发生 crash，重启后
新 generation 的 Delivery ID 必须保持不变。

Generation registry row 只有在该 generation 已被持久化 checkpoint 安全越过、没有
receipt/replay/reprocess 引用、且确认不存在需要重放的记录后才能进入 `Retired`。清理
前必须保留 Delivery ID 重建所需字段。

重启时必须恢复全部非 `Retired` generation。找不到旧 inode 时，已提交 receipt 的
结果保持完成；尚未建立稳定 Position/receipt 的字节不得声称可恢复，必须写
`DataLossSuspected` Audit/Health。fast-regrow 盲区仍是 Phase 1 known limitation。

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

Delivery ID 的文本与二进制编码冻结为：

```text
delivery_id = "dlv1_" + lowercase(base32hex-no-padding(SHA-256(frame)))

File frame = UTF-8("guard.delivery.file.v1\0")
           + field(SourceID)
           + field(FileGeneration)
           + uint64-be(StartOffset)
           + uint64-be(EndOffset)

Journald frame = UTF-8("guard.delivery.journald.v1\0")
              + field(SourceID)
              + field(Cursor)

field(value) = uint32-be(byte_length) + exact UTF-8 bytes
```

offset 必须是非负整数且 `StartOffset <= EndOffset`。`FileGeneration` 使用 generation
registry 中持久化的小写 hex 原值；Journald Cursor 按 API 返回字符串的精确 UTF-8
字节编码，不进行 Unicode normalization、大小写或结构猜测。M0-D 必须为 File 与
Journald 各提供 golden vectors，并验证 receipt key 与重启重放稳定。

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
    EmittedIndex   uint32
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
EmittedIndex
NodeID
```

Phase 1 `NodeID` 在首次初始化时使用 128-bit CSPRNG 生成并编码为小写 hex，
在 `node_identity` 中持久化。重启、升级和配置热更新不得改变 NodeID；只有
`purge` 后的全新初始化才可生成新值。

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
node_id
delivery_id
parser_id
parser_version
emitted_index
```

具体编码冻结为：

```text
event_id = "evt1_" + lowercase(base32hex-no-padding(SHA-256(frame)))

frame = UTF-8("guard.security-event.v1\0")
      + field(NodeID)
      + field(DeliveryID)
      + field(ParserID)
      + field(ParserVersion)
      + uint32-be(EmittedIndex)

field(value) = uint32-be(byte_length) + exact UTF-8 bytes
```

`EmittedIndex` 从 0 开始，必须是 Parser 单次输出中的稳定顺序。Parser ID/Version
使用已持久化的原始标识字节，禁止实施 Unicode normalization 或大小写猜测。
Rule ID/Version 不进入 Event ID；Detection outcome 使用
`EventID + RuleID + RuleVersion` 作为唯一键。M0-D 必须提供固定 golden vectors
和跨进程、跨重启稳定性测试。

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

- 每条 RawRecord 进入 processing attempt 时，以当前 Active Parser Set 建立不可变的
  attempt-local snapshot；每个 SecurityEvent 产生后，再从本 attempt 首次使用时冻结的
  Rule Catalog snapshot 计算 applicable rules。
- Parser/Rule revision 在一次 attempt 中不可变；必要 outcome、Critical Audit 和
  `processing_receipt` 必须由第 22.2 节 Processing Coordinator 在同一 SQLite
  UnitOfWork 中提交。
- crash 前未提交 receipt 的记录允许在重启后使用当前 Active Parser/Rule
  重新求值；这是明确的 re-evaluation，不声称为严格同版本 replay。
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

在 Phase 1 receipt pipeline 中，二者由同一个成功事务建立；概念仍分离，
以防未来把外部副作用错误当成 Source checkpoint 屏障。

### 8.1 SourceDurable 屏障

“写入内存 Channel”不构成 SourceDurable。

```text
Parser / Detection
  → terminal processing record (`processing_receipt`)
  → Alert / Decision / Critical Audit 等必要结果同事务提交
  → ProcessingComplete
  → SourceDurable
  → contiguous checkpoint 可推进
```

Firewall Apply、Firewall Revoke 和 SMTP 发送不属于 SourceDurable 或
ProcessingComplete 前置条件。

### 8.2 ProcessingComplete 与多 Parser

一条 RawRecord 只有在本次不可变 Processing Plan 中所有已调度 Parser 任务，以及其
产生 Event 对应的全部适用 Detection Rule，都进入成功或终态拒绝后，才能标记
`ProcessingComplete`。

Phase 1 使用 all-match：每个 Source 的 enabled Parser Set 按 `priority + parser_id`
稳定排序并全部执行。每个 Parser 可产生 `0..N` 个 Event，`EmittedIndex`
按单次输出从 0 稳定编号。cheap prefilter 未命中与 parse-not-match 是成功的
`NotApplicable`，不是错误。

确定性的单 Parser 错误对该 Parser 形成终态拒绝，其他 Parser 继续；DB 错误、
context cancellation 和无法建立不可变版本 snapshot 的系统错误终止整个
processing attempt，不得产生 receipt。M3 可以冻结 DSL 与具体资源上限，
不得再改变这些跨模块 completion 语义。

`ProcessingComplete` 同时是 `SourceDurable` 的必要条件。

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

Phase 1 使用同步 outcome transaction + terminal `processing_receipt` + 唯一
Delivery/Event ID。必须证明：

- 未达到 SourceDurable 的记录不会因 checkpoint 提前推进而丢失。
- 重放不会创建重复 Alert、Decision、Critical Audit 或其他持久化副作用。
- 已存在 receipt 的 Delivery 不得再次进入内存 Detection Window。
- crash 前未提交 receipt 的 Delivery 可以重新进入新的内存 Window；这是
  at-least-once 恢复的允许重复，但下游持久化副作用仍必须幂等。

receipt pipeline 中，即使一条记录没有产生 Alert 或 Decision，也必须写入 terminal
processing record；该记录、必要业务结果和对应幂等键必须在同一个事务中提交。

用于去重的 `processing_receipt` 在对应 Source checkpoint 已持久化安全越过
该 Position，且 retention / explicit reprocess 语义不再需要它前，禁止删除。

Alert、Decision 等下游持久化副作用仍必须拥有独立唯一键，不能只依赖 terminal record。

未来 Notification Contract 必须复用稳定 Event/Decision ID 建立唯一幂等键；在该
Contract 冻结前，Notification Job 不进入 M0 Slice 的强制证明范围。

Phase 1 保持 V0.4 的内存 Window 语义：Agent restart 后 Window 清空。已经
ProcessingComplete 的稳定 Event ID 即使因任何恢复路径再次出现，也不得自动再次贡献
窗口计数；此前尚未形成 Decision 的部分内存窗口状态可能丢失。这是一项明确的检测
连续性限制，Source at-least-once 不等于 Detection 跨重启 exactly-once。

### 8.5 Poison Record / Terminal Processing Record

确定性解析失败、超长行或资源限制拒绝不能永久阻塞连续 checkpoint。

抽象终态记录称为：

```text
Terminal Processing Record
```

具体落地为 `processing_receipt` terminal row。

处理错误必须先分类：

| 类别 | 语义 | 是否可写终态 receipt |
|---|---|---:|
| `RecordPermanent` | 相同 bytes + 相同不可变 Processing Plan 必然重复失败 | 是 |
| `PlanBlocked` | Parser/Rule revision 缺失、损坏或无法加载 | 否，Health 进入 Degraded |
| `Transient` | DB busy、临时 IO、worker 或资源故障 | 否，保留重试 |
| `Cancelled` | shutdown 或 context cancellation | 否 |

`NoMatch` 是成功的零 Event 结果，不是 Poison。只有 `RecordPermanent` 可以进入终态；
禁止把系统错误或计划损坏包装为 Poison 来推进 checkpoint。

Poison Record 的 Terminal Processing Record 至少包含：

1. Delivery ID 唯一幂等键。
2. SourcePosition、failure stage、error code、sanitized error、terminal action、occurred time。
3. 提交后增加低基数 Metric；Metric 不是 SourceDurable 屏障或恢复凭据。
4. 对影响 Source 连续性的拒绝写 Critical Audit；受限错误样本是否保留由配置决定。
5. 禁止记录凭据或未经截断的敏感日志内容。

终态 receipt、Critical Audit 和必要的业务 outcome 在同一事务提交后，
该 Delivery 同时达到 ProcessingComplete 和 SourceDurable。

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

- Critical Audit 未提交时禁止达到 ProcessingComplete / SourceDurable，
  因而 checkpoint 不得推进。
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

默认 `shutdown_timeout=30s`，合法范围 `5s–300s`。M0-B 必须验证该默认值；
若证据要求修订，必须同步 Config Schema、测试和本文，禁止在代码中静默修改。

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

合法状态与 EndReason 组合冻结为：

| State | EndReason | 附加条件 |
|---|---|---|
| `Active` | `NULL` | 只有 Active 可参与 Desired Ban Projection |
| `Expired` | `expired` | 只能由到期路径产生 |
| `Revoked` | `manual` | 显式人工撤销 |
| `Revoked` | `manual_replace` | 只适用于 Manual Decision |
| `Revoked` | `rule_disabled` | 只适用于 Automatic Decision |
| `Revoked` | `system_cleanup` | 只能由明确系统清理流程产生 |

只允许 `Active → Expired` 或 `Active → Revoked`；终态不可复活。重复终止必须是幂等无变更，
或返回稳定的冲突错误，禁止改写已存在的 EndReason。

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

Phase 1 使用 SQLite partial unique index，不使用派生 `active_key`：

```text
UNIQUE(node_id, rule_id, canonical_target)
WHERE decision_source = 'automatic' AND state = 'active'
```

Phase 1 Automatic Action 只有 `ban`，因此 Action 不进入键；Scope 是 Policy/Firewall
派生执行属性，不是 Decision 身份，不进入键；RuleVersion 不进入键，禁止通过
Rule 升级绕过“不自动续期”语义。

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
  node_id + rule_id + canonical_target

Manual:
  node_id + canonical_target
```

Phase 1 同一 canonical Target 最多存在一个 Active Manual Decision。重复执行 manual
ban 默认返回 `AlreadyBanned`，不得静默修改 duration。
该约束必须由下列 partial unique index 保证：

```text
UNIQUE(node_id, canonical_target)
WHERE decision_source = 'manual' AND state = 'active'
```

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
ConfirmedTargetEnforcementGeneration
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

Backend Snapshot 只返回物理状态和 Guard owner marker，不得声称从 Firewall 观察到应用层
generation。Reconciler 必须把完整物理状态与当前 Desired Intent 比较；只有全部属性
匹配且回写时 fencing 仍有效，才能由 Reconciler 写入当前
`ConfirmedTargetEnforcementGeneration`。重启后也使用相同规则；该字段表示
“Reconciler 已证实当前物理状态匹配该 generation”，不表示 Firewall 内嵌了 generation。

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

#### 三个 Domain 的安全依赖顺序

一次 Reconcile 必须从完整 Desired Firewall Snapshot 构建新 Plan，并按下列安全顺序执行：

```text
Probe / Snapshot
  → Ensure Guard-owned Infrastructure
  → 增加或扩大 Allowlist / Protected Policy
  → 增加、延长 timeout 或扩大 scope 的 Target tightening
  → 在替代保护已确认后执行 Target relaxation
  → 为待删除/缩小 Policy 的受影响 Target 建立 TargetPrepared
  → 删除或缩小不再需要的 Policy
  → Confirm Snapshot
```

必须满足：

- Infrastructure 当前 revision 未 Converged 时，禁止 Policy/Target 外部写。
- 保护性 Policy 增加/扩大未收敛时，禁止对受影响 Target 执行 tightening。
- Target tightening 默认先于 relaxation；例如 `/24 → /32` 必须先确认 `/32` 已存在，
  再移除 `/24`。只有 Backend 能用一个原子 batch 同时完成单 Target 替换时才可合并。
- `TargetPrepared` 只是一项 Plan 内前置条件，不新增持久化状态。它表示在旧 Policy
  仍存在时，受影响 Target 的 ban membership、timeout、scope 和其他非 Policy 属性
  已与新 Intent 匹配；不得要求旧 Policy 已经消失。
- Policy 删除/缩小只能在所有受影响 Target 均 `TargetPrepared` 后执行；删除后必须
  重新 Snapshot，只有 PolicyRevision 与完整 Target generation 同时匹配才可 Converged。
- 一个外部 batch 只能属于一个 failure domain；Phase 1 Target batch 每次只包含
  一个 CanonicalTarget。无法精确归属错误的 Backend 禁止跨 domain 合批。
- dependency blocked 不消耗 attempt；`BlockedByInfrastructure/Policy` 是调度原因，
  不新增第四个持久化状态。
- 每个 domain 成功后必须重新 Snapshot；禁止只凭命令返回码声称 Converged。
- Contract Tests 必须覆盖 Allowlist/Protected Target 删除、`/24 → /32` 替换、
  tightening 成功后 relaxation 失败，以及 Policy 删除失败，证明不存在保护空窗或循环等待。

#### Retry 状态机与预算

```text
Pending
  → persist attempt_count++ and status=Applying
  → external mutation
  → authoritative confirm
      ├─ match                         → Converged
      ├─ retryable + retry budget left → RetryWaiting
      └─ non-retryable / exhausted     → Degraded
```

- 每个 domain key 允许 1 次首次调用和 5 次自动重试；5 次重试分别在
  `1s / 5s / 30s / 5m / 15m` 后可执行。
- attempt 在外部写入前持久化；调用期间 crash、timeout 或结果未知均已消耗本次 attempt。
- Probe/Snapshot 不消耗 mutation attempt，但必须有独立的超时、退避与 Health/Metric。
- revision/generation 变化产生新业务 key，从 attempt 0 开始；同一 key 的 restart、
  health flap 和普通 Reconcile 不得重置预算。
- stale revision/generation 的返回结果只能结束旧 attempt，禁止覆盖新状态；
  随后立即 Probe 并为当前 Desired 重建 Plan。
- non-retryable 包括 OwnershipConflict、不支持能力和不合法 Plan；不得用自动重试隐藏设计错误。

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

V0.4 的 `guard-agent decision retry <id>` 在新模型中不再成立。Phase 1 CLI 冻结为：

```text
guard-agent reconcile retry infrastructure
guard-agent reconcile retry policy
guard-agent reconcile retry target <canonical-target>
```

API 必须表达同样的 domain key。Retry 只能使指定 domain 的 `RetryEpoch++`、
attempt 归零并写 Critical Audit，禁止直接修改 Decision。

Phase 1 使用下列低基数 Reconcile Metrics 替代 `guard_decision_failed_total`：

```text
guard_reconcile_mutations_total{domain,result}
guard_reconcile_duration_seconds{domain,result}
guard_reconcile_unknown_results_total{domain}
guard_reconcile_degraded{domain}
guard_firewall_probes_total{backend,result}
```

`domain` 只能是 `infrastructure|policy|target`；`result` 和 `backend` 必须使用
Schema 中的有限枚举。CanonicalTarget、DecisionID、错误文本或 Retry key 禁止进入 label。

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
node_identity
sources（最小 identity）
parsers（最小 identity/version）
rules（最小 identity/version）
parser_versions/rule_versions
allowlists（最小 canonical range）
protected_targets（最小 canonical range）
source_file_generations
source_checkpoints
processing_receipts
alerts
decisions
desired_ban_projections
enforcement_states
infrastructure_reconcile_state
policy_reconcile_state
target_reconcile_state
audit_logs
```

Phase 1 使用三类独立 Reconcile state 表，不合并为带可空外键的通用表。
三个 failure domain 必须分别持久化，禁止只有一个全局 retry counter。

M0-D migration 必须明确：

- 主键、外键和 `PRAGMA foreign_keys = ON`。
- nullable、check、unique 和 index。
- 状态枚举的稳定存储值。
- 时间统一格式和精度。
- canonical CIDR 表示。
- event identity、replay key，以及 Terminal Processing Record 的保留期和清理水位。
- `processing_receipt` 不得早于该 Source 已持久化且不会回退的 checkpoint
  安全边界被清理。
- File generation 必须先于该 generation 第一条 RawRecord durable persist。
- Parser/Rule revision 只能通过新版本记录和 Active pointer 切换，禁止就地改写历史版本。
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

Phase 1 SQLite 基线冻结为：

```text
PRAGMA journal_mode = WAL
PRAGMA synchronous = FULL
PRAGMA foreign_keys = ON
PRAGMA busy_timeout = 5000
PRAGMA wal_autocheckpoint = 1000
所有写事务保持短事务，禁止在事务内调用 Firewall、SMTP 或其他外部系统
```

每个 SQLite connection 必须设置并 read-back 校验 connection-local PRAGMA；打开 DB 后必须
确认 `journal_mode` 实际返回 `wal`。SQLite WAL checkpoint 与 Source checkpoint 必须使用
不同术语，WAL checkpoint 不是 SourceDurable 的条件。

承诺边界：

- process crash / SIGKILL：承诺已返回成功的事务可恢复，必须通过 kill/reopen 测试。
- OS crash / reboot / power loss：只在受支持的本地 filesystem 与正确实现
  fsync/barrier 的存储设备上承诺；对应 VM/power-cut 测试未通过前必须标记
  `NOT VERIFIED`。
- 不承诺磁盘损坏、控制器虚假报告 flush 或未验证网络文件系统上的掉电持久性。

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

Backend 方法集冻结为：

```text
Probe(ctx) -> FirewallCapabilities
Snapshot(ctx) -> ManagedState + read-only ForeignContext
Plan(current snapshot, desired snapshot) -> domain-scoped OperationPlan
Apply(ctx, one domain-scoped OperationPlan) -> ApplyResult
RemoveManagedInfrastructure(ctx, expected OwnerVersion) -> ApplyResult
```

Go 的具体 struct、error type 和字段名以 M0-D 编译通过的代码为权威，但方法集、
domain-scoped Plan、context cancellation 和“Snapshot 是外部事实”的语义不得改变。
`ApplyResult` 必须区分 `Confirmed`、`Rejected`、`Unknown`；timeout/连接中断一律是
`Unknown`，不得猜测失败或成功。

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

高风险 Apply 前，Agent 还必须把仅包含本次待确认 config/policy delta、反向操作、
deadline 和 plan digest 的最小 rollback journal 交给 Enforcer 并确认已持久化。Enforcer
watchdog 负责在 deadline 到达时执行回滚；机器重启后由 Enforcer 启动恢复或独立
systemd rollback unit 接管，不能依赖 Agent 必须在 deadline 前恢复。

回滚只能撤销待确认的 config/policy delta，禁止恢复旧的完整 Ban Snapshot。回滚后
Agent 必须从当前 Active Decisions 重建 Desired Firewall Snapshot 并重新 Reconcile；
确认窗口中新产生的 Decision 不得被旧 Snapshot 覆盖。Apply-confirm Gate 必须注入
Agent crash、Enforcer crash、整机重启、deadline race 和确认窗口内新 Decision。

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
| 日志级别 | YAML | 是，原子更新 | 否 | 否 |
| Detection Rule | SQLite | 是 | 否 | 否 |
| Allowlist | SQLite | 是 | 否 | 否 |
| SMTP Credential | YAML 引用的 credential file 内容 | 否 | 是 | 是 |

同一字段禁止同时由 YAML 和 Web/SQLite 写入。Web 对 YAML-owned 字段只能只读
展示，除非未来引入明确的配置写回机制。
`smtp.credential_file` 由 YAML 持有路径，文件内容是 secret 唯一权威源；文件必须
`root:guard 0640` 或更严格，读取错误必须阻止 SMTP worker Ready，禁止回退到环境变量
或 SQLite 明文值。

配置 loader 必须在 Schema validation 前 fail-closed 执行固定资源上限：原始 YAML 文档
（包括注释和尾随空白）不得超过 64 KiB；解码后的 YAML AST 最多包含 512 个唯一 Node，
其中 `DocumentNode` 计入总数；`DocumentNode` 深度为 0，任一 Node 深度不得超过 32。
任何超限错误都不得回显配置内容。这些是 loader 安全边界，不是 Config 字段，不在
Config Schema 中复制定义。

### 15.2 API 与 CLI

M0 只冻结：

- API version prefix，例如 `/api/v1/`。
- 统一错误 envelope、request ID 和错误码稳定性原则。
- CLI 的 stdout/stderr 分工、`0/非 0` 语义和查询命令 `--json` 原则。

完整资源 API、分页和全部退出码在对应里程碑开始前冻结，不进入 M0 核心 Gate。

### 15.3 进程权限 Contract

Phase 1 冻结为非特权 Agent + 最小 root Enforcer：

```text
guard-agent (User=guard, no Linux capability)
  ↕ versioned Unix Socket IPC
guard-enforcer (root, CapabilityBoundingSet=CAP_NET_ADMIN)
  ↕ nftables / netfilter
```

- Socket 使用 `/run/guard/enforcer.sock`；目录 `root:guard 0750`，socket `root:guard 0660`。
- IPC 使用 `uint32-be length + versioned JSON`；单 frame 默认上限 1 MiB，超限直接拒绝。
- Enforcer 必须用 `SO_PEERCRED` 校验调用者 UID，不得只相信请求字段。
- IPC 只允许 `ProbeCapabilities`、`SnapshotManaged`、`ApplyManagedPlan`、
  `RemoveManagedInfrastructure`；禁止传入 shell command、binary path 或任意 table/chain 名称。
- Enforcer 必须再次校验 canonical Prefix、owner/version、Guard-owned 对象命名、
  operation count 和请求大小，并串行执行所有 Firewall mutation。
- Agent 可以拥有 DB；CLI/Web 只能调用 Agent 业务服务，禁止直接写 DB。
- 该拆分降低 Web/Parser 漏洞直接获得主机 root 的风险，但不能阻止已攻陷 Agent
  通过合法 Guard 协议滥用 Guard-owned ban 能力；这是 Phase 1 明确的残余风险。

同时必须满足：

- CLI 禁止绕过业务层直接写 SQLite。
- 配置、DB、Socket 和 secret 必须有明确 owner/mode。
- 两个 systemd service 均使用 `NoNewPrivileges=yes`、`ProtectSystem=strict`、
  `ProtectHome=yes` 和精确 `ReadWritePaths`；Enforcer 只保留 `CAP_NET_ADMIN`。
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

Phase 1 开发默认值冻结为：

| 配置键 | 默认值 | 合法范围 | 触顶/到期行为 |
|---|---:|---:|---|
| `runtime.raw_queue_capacity` | 512 | 1–65536 | 可取消阻塞背压，不 drop |
| `runtime.event_queue_capacity` | 1024 | 1–65536 | 可取消阻塞背压，不 drop |
| `runtime.reconcile_queue_capacity` | 256 | 1–65536 | 按 domain key 合并 wakeup，worker 重读最新 Desired |
| `source.checkpoint_interval` | 1s | 100ms–30s | interval/threshold 任一先到即尝试推进连续 checkpoint |
| `source.checkpoint_record_threshold` | 256 | 1–10000 | 同上 |
| `runtime.shutdown_timeout` | 30s | 5s–300s | 到期后 cancel worker 并退出，重启依靠 replay |

这些值是 `Specified` 开发默认，不是性能结论。M0 Fake Slice 与 Phase 1 目标负载基准
未通过前，对应 Gate 保持 `NOT VERIFIED`。

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

1. Parser/Detection/outcome 事务提交前 crash，SourceDurable 不成立，checkpoint 不前进。
2. outcome/`processing_receipt` 提交后、checkpoint flush 前 crash，重放不产生重复副作用。
3. 没有产生 Alert/Decision 的记录仍会写唯一 receipt 并安全推进 checkpoint。
4. 两条记录乱序达到 SourceDurable，只提交最高连续 SourceDurable position。
5. 多 Parser 中一个确定性失败时，该 Parser 进入终态拒绝且其他 Parser 继续；
   系统错误则整个 attempt 不得产生 receipt/checkpoint。
6. queue full 产生 backpressure，不静默 drop。
7. `RecordPermanent` Poison 进入终态后不永久阻塞 checkpoint；`Transient/PlanBlocked/Cancelled`
   不得被终态化。
8. SIGTERM drain 成功时 checkpoint 与 Terminal Processing Record 正确 flush。
9. shutdown timeout 后重启，允许重复读取但不丢未完成记录，持久化副作用保持幂等。
10. rename/create 后新旧 generation 乱序完成，checkpoint 仍按 DeliverySequence 连续推进。
11. 新 generation 第一条 outcome/receipt 提交前 generation 已持久化。
12. 新 generation 已处理、旧 generation checkpoint 未推进时 crash，重启后 Delivery ID 不变。
13. crash 前未提交记录按当前 Active Parser/Rule 重评，不产生重复持久化副作用。
14. Critical Audit 提交失败时业务 outcome/receipt 事务回滚，checkpoint 不推进。
15. copytruncate fast-regrow 能检测时产生 DataLossSuspected；不能检测时标记
    known limitation，不伪造 at-least-once 保证。
16. Generation registry 在 checkpoint 尚未安全越过，或仍有 receipt/replay/reprocess 引用时
    不得进入 Retired；满足全部清理条件后重启不再依赖该 row。
17. 同一 `EventID + RuleID/Version` 已进入 Window 后 SQLite outcome transaction 失败，
    同进程 retry 不得再次增加 count/distinct_count。
18. 一次 attempt 内 Parser Set/Rule Catalog snapshot 不受 Active 版本切换影响；未提交重放重新建立当前 snapshot。
19. SQLite durability 配置通过已声明故障域的 crash/reboot/power-loss 测试，
    未验证故障域明确标记 `NOT VERIFIED`。

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
- receipt pipeline 的 SourceDurable 语义与 checkpoint 行为符合第 8 节。
- kill/restart 测试证明未提交 outcome/receipt 的记录不会被 checkpoint 越过。
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

产物按以下责任分层组织；若 Go 生态对具体路径有更强约定，可经 ADR 调整，
但不得合并权威责任：

```text
docs/contracts/
  guard-phase-1-m0-contract-freeze-v0.3.md
  core-model.md
  decision-enforcement.md
  source-delivery.md
  firewall-behavior.md

docs/adr/
  ...

api/openapi/
  guard-v1.yaml

schema/
  config-v1.schema.*

migrations/
  ...

cmd/
  guard-agent/
  guard-enforcer/

internal/
  runtime/ config/ store/ source/ parser/ detection/
  decision/ enforcement/ firewall/ reconcile/ notification/
  auth/ web/ audit/ retention/ health/ doctor/

packaging/systemd/
  ...

tests/
  contracts/ integration/ e2e/ security/ upgrade/ fixtures/

benchmarks/
  ...

artifacts/evidence/
  <milestone>/<commit>/manifest.*
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
  实现已冻结的 all-match/completion 语义，并冻结 DSL 和资源限制

M4 Detection
  复用稳定 Event ID，并冻结窗口触发和清理语义

M5 Decision Model
  直接实现已冻结的 Decision/Desired Ban Projection

M6 Firewall
  按 Backend 子里程碑逐个完成 Golden State 和真实实现

M7 Reconciliation
  实现 TargetProjectionRevision / TargetEnforcementGeneration、三类 failure domain、有界重试、Degraded 和 drift recovery

M8 Notification
  实现独立持久队列、SMTP TLS、有界重试和 Cooldown

M9 Built-in Rules
  交付 SSH/Nginx Parser + Rule 包和真实 fixture 纵向测试

M10 Productization
  完成 API/Web、Audit/Retention、安装升级、全矩阵测试和 Release Evidence
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

M1–M10 的完整 Entry Gate、产物、测试和 Exit Gate 见第 22–36 节。
M0 Frozen 只表示核心 Contract 可实现，不表示后续里程碑已完成。

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

---

## 22. Phase 1 全面开发执行规则

### 22.1 使用边界

- Phase 1 采用 `Standalone-first, Cluster-ready`，只交付单机 Agent 产品。
- `NodeID` 等稳定身份可为 Phase 2 保留，但禁止实现 `guard-server`、Enrollment、
  Cluster Rule/Decision、跨节点缓冲或一致性协议。
- M0 Frozen 前，M1–M10 只能做不依赖未验证 Contract 的只读设计、Fake 实验或
  第 3.3 节允许的骨架工作。
- 每个里程碑必须先通过 Entry Gate，再实现 Required Artifacts，最后用 Exit Gate 产生证据。
- 测试条目表示“必须实现和通过”，不代表当前已通过。

### 22.2 模块边界

1. `source` 只产生 RawRecord 和持久化投递状态，不调用 Firewall。
2. `processor` / Pipeline Coordinator 是 processing attempt 的唯一事务协调者：冻结
   Parser/Rule snapshot，开启 Store UnitOfWork，调用 transaction-aware Parser outcome、
   Detection、Decision、Critical Audit 与 Receipt writer，并且是唯一允许 commit/rollback 的模块。
3. `parser` 只把 RawRecord 转换为 SecurityEvent 或终态解析结果。
4. `detection` 只消费 SecurityEvent，生成 Alert 和 Automatic Decision request。
5. `decision` 是安全意图事实源，不直接执行 Firewall。
6. `enforcement` 从 Active Decisions 和 Policy 构造规范化 Desired Intent。
7. `firewall` 只负责 Probe、Snapshot 和 Guard-owned 对象变更。
8. `reconcile` 比较 Desired/Observed，管理 Plan、fencing、重试和 Degraded。
9. Web、API 和 CLI 必须调用同一应用服务，禁止绕过业务层直接写 DB。
10. `notification` 与 Enforcement 解耦，通知失败不得改变 Ban 结果。
11. 禁止为 Phase 2 创建空接口、空服务、兼容壳或假想扩展点。

除 Processing Coordinator 外，所有 domain service 只能接收显式 UnitOfWork/transaction
handle 并写入，不得内部 begin、commit 或使用独立连接绕开事务。M0-D 必须冻结
UnitOfWork 接口；Source Slice 必须对 Parser outcome、Detection contribution、Decision、
Audit、Receipt 任一子写失败注入错误并断言整体回滚、checkpoint 不前移。

### 22.3 通用工程 Contract

- 所有 IO、SQLite、HTTP、SMTP、Firewall 与 IPC 调用必须支持 timeout、cancellation
  或明确失败传播。
- 内部状态不合法时必须快速失败，禁止用静默 fallback、无限 retry 或猜测修复隐藏。
- 外部边界错误使用稳定 error code；内部 error 保留 cause，对外输出必须脱敏。
- 关键路径使用 OpenTelemetry span，命名格式 `<package>.<FuncName>`；HTTP 透传 trace context，
  异步链路使用 `delivery_id/event_id/decision_id/reconcile_attempt_id` 关联。
- 结构化日志至少包含 `requestID` 或 `traceID`；安全事件额外包含低基数 action/result。
- `/metrics` 使用 Prometheus 格式；IP、CIDR、path、username、request ID、group key
  禁止作为 label。
- 一个功能包只有在编译、format、lint、相关 unit/contract/integration 测试和
  `git diff --check` 都通过后才可标记完成。

---

## 23. M1 Runtime

目标：建立后续所有里程碑共享的进程、配置、存储、权限、管理面与可观测性运行底座。

### 23.1 Entry Gate

- M0 已 `Frozen/GO`。
- Go module path、toolchain、构建方式和第三方依赖已由 ADR 批准。
- 第 15.3 节权限 Contract、Config ownership 和 SQLite durability 已 Verified。
- Web Security Gate 已冻结 bootstrap、Argon2id 基准、Session、Cookie、CSRF/Origin、
  登录限速、失败审计和 reverse proxy 信任边界。

### 23.2 Required Artifacts

- `[M1-WP1 Process]` 可编译的 `guard-agent` 和 `guard-enforcer`，统一 context cancellation 与 SIGTERM lifecycle。
- `[M1-WP2 Config/CLI]` `run`、`config validate`、`version` 基础 CLI；Config Schema、加载器、未知字段拒绝。
- `[M1-WP3 Store]` SQLite migration runner、空库初始化、PRAGMA read-back、单实例锁和
  仅由 Processing Coordinator 提交的 Store UnitOfWork。
- `[M1-WP4 Privilege]` 非特权 Agent/Enforcer IPC，systemd units，目录与文件 owner/mode。
- `[M1-WP5 Management]` 结构化日志、tracing、基础 Metrics、`/health`、`/ready`，loopback-only HTTP 与单管理员认证。
- `[M1-WP6 Maintenance]` Maintenance 持久化框架；完整 Firewall 行为在 M7 实现。

### 23.3 Required Tests

- 空库/已有库迁移、重复启动、迁移失败、DB busy、SIGKILL 和重启恢复。
- 无效/未知配置、文件权限、secret 脱敏、非 loopback 明文监听未授权时启动失败。
- 管理员 bootstrap、Session 过期/轮换/吊销、CSRF、Origin、登录限速和失败审计。
- IPC 未授权 UID、畸形/超大 frame、任意对象名、命令注入、socket mode 和 Enforcer restart。
- SIGTERM 在 30s 内 drain；超时后安全退出。Health/Ready 对 DB、Config、Enforcer 给出稳定状态码。

### 23.4 Exit Gate

隔离 Linux 验证环境可使用开发构建与测试 unit 完成初始化、启动、停止和重启；
migration 可恢复；Web 写操作
具备认证/CSRF/Critical Audit；Agent 无 `CAP_NET_ADMIN`；M2/M3/M6/M8 可复用稳定
Runtime/Store/Config 接口。生产安装包、升级和卸载仍由 M10 负责。

---

## 24. M2 Sources

目标：实现可恢复、可背压、可审计的 File/Journald 原始记录采集与连续 checkpoint。

### 24.1 Entry Gate

M1 Runtime/Store/Config 已通过 Exit Gate，receipt pipeline、RawRecord、SourcePosition、
Delivery ID、generation 状态机与第 16 节队列/checkpoint 参数已 Frozen。

### 24.2 Required Artifacts

- `[M2-WP1 Source Core]` Source registry 和统一 Source 接口。
- `[M2-WP2 File]` File Source：append、rename/create、copytruncate、symlink、restart、generation registry。
- `[M2-WP3 Journald]` Journald Source：cursor、cursor unavailable、resume policy 和 vacuum gap。
- `[M2-WP4 Durability]` `processing_receipts`、连续 SourceDurable checkpoint manager、Poison/Terminal record、
  DataLossSuspected Audit/Health/Metric。
- `[M2-WP5 Verification]` Fake Source、轮转 fixtures 和第 17.1 节 Contract Tests。

### 24.3 Required Tests

- 第 17.1 节全部 receipt pipeline 测试；未选 durable inbox 为 `N/A`，不实现生产路径。
- copytruncate fast-regrow、rename/create 乱序、旧 inode EOF、symlink 重解析、
  Journald cursor vacuum 和 Source lag 不可恢复区间。
- queue full 必须背压；checkpoint 不越过未 SourceDurable 记录；crash/restart 不丢未持久化记录。
- 日志内容视为不可信输入，单行上限 64 KiB，错误日志不保存完整敏感原文。

### 24.4 Exit Gate

File/Journald 在声明支持的恢复场景下满足 at-least-once 与 receipt 幂等语义；
Source Slice 与第 12.3、17.3 节中 Source/receipt/checkpoint 相关 crash points 通过；
未发现 checkpoint 越过未持久化记录的路径；M3 可稳定消费 RawRecord。完整跨模块
Crash Matrix 仍由 M7/M10 Gate 负责。

---

## 25. M3 Parser

目标：把 RawRecord 以确定、受限、可版本化的方式转换为一个或多个 SecurityEvent。

### 25.1 Entry Gate

M2 提供稳定 RawRecord 和 receipt completion 边界；Parser DSL/依赖与 Grok expansion
经 ADR 和基准冻结。

### 25.2 Required Artifacts

- `[M3-WP1 Catalog]` Parser/ParserVersion、Source binding、`contains/prefix` cheap prefilter。
- `[M3-WP2 Runtime]` JSON、logfmt、Go RE2 Regex 和 Grok 编译/执行；禁止 PCRE 危险回溯语义。
- `[M3-WP3 Ownership]` system-owned/parser-owned 字段强制边界，IP/CIDR 使用 `netip` 严格校验。
- `[M3-WP4 Versioning]` `Validate → Compile → Resource Check → Test → Persist → Atomic Swap` 版本更新。
- `[M3-WP5 Tooling]` CLI/API Parser Test、Playground 后端、golden fixtures、错误码、Metric 和 Health。

### 25.3 Required Tests

- 每种格式的 match/unmatched/invalid/边界 fixture，超长 Regex、capture/Grok 资源超限。
- Parser 无法覆盖 ID、NodeID、SourceID、ParserID、ObservedAt。
- Atomic Swap 并发测试不混用版本；新版本失败时旧 Active Version 继续运行。
- 确定性 Parser 错误按第 8.2/8.5 节终态化，系统错误不得被伪装为 Poison。

### 25.4 Exit Gate

JSON、logfmt、RE2 Regex、Grok 四类 Parser 格式、prefilter、all-match、版本切换及
资源限制通过 Contract Tests；
SecurityEvent 字段权属无旁路；M4 只依赖稳定 SecurityEvent，不依赖 Parser 实现。

---

## 26. M4 Detection

目标：在硬资源上限内完成可重放幂等的窗口检测，并生成稳定 Alert 与 Decision request。

### 26.1 Entry Gate

M3 已稳定产生 SecurityEvent；Event ID 和 `EventID + RuleID + RuleVersion` 幂等边界已 Verified；
CEL 依赖、static/runtime cost、timeout、global/per-rule groups、window 范围和 distinct
capacity 已经 ADR/基准冻结。

### 26.2 Required Artifacts

- `[M4-WP1 Rule Catalog]` DetectionRule/RuleVersion/Rule Catalog，event match、group_by、Sliding Window、count、
  exact `distinct_count`、saturated 状态和 active group 硬上限。
- `[M4-WP2 Engine]` 基于 ObservedAt 的内存窗口、受 cost/timeout 约束的 CEL 条件、
  Rule Exclusion、Alert 持久化，幂等 contribution ledger 或等价机制。
- `[M4-WP3 Tooling]` Rule Validate/Test、atomic swap、Group Key/Count/Threshold/Would Trigger/Cost 输出。

### 26.3 Required Tests

- Timestamp 伪造、未来/旧/乱序日志不得改变 ObservedAt Window。
- CEL 编译失败、static/runtime cost 超限与 timeout 必须快速失败或产生稳定终态，
  不得阻塞 Pipeline Coordinator 或绕开 processing receipt。
- 同一 Event/Rule 在事务重试、重放与同进程 retry 中最多贡献一次。
- group 超限拒绝新 group，不使用无界 LRU；distinct 超限进入 saturated，不继续增长内存。
- allowlisted 来源默认仍可产生 Detection/Alert/Decision，只有 Rule 显式 exclusion 时才跳过。
- restart 后 Window 清空必须作为 Phase 1 known limitation 出现在运维文档和 Doctor 输出中。

### 26.4 Exit Gate

count、distinct_count、exclusion、版本切换和幂等测试通过；资源攻击测试证明
group/distinct/window 有硬上限；M5 获得稳定、幂等的 Automatic Decision request。

---

## 27. M5 Decision Model

目标：把检测或人工请求转换为唯一、可审计、可重建投影的安全意图事实。

### 27.1 Entry Gate

M4 提供幂等 Decision request；Decision state/EndReason、Automatic/Manual unique index、
Critical Audit 原子性和 Projection 规则已 Verified。

### 27.2 Required Artifacts

- `[M5-WP1 Lifecycle]` 唯一 Decision 状态 `Active/Expired/Revoked`，duplicate suppression、Manual AlreadyBanned/replace。
- `[M5-WP2 Projection]` Active Decisions → Desired Ban Projection，多 Decision 合并、EffectiveUntil、可重建投影。
- `[M5-WP3 Expiry/Policy]` 正常运行到期调度和启动时先 Expire，Allowlist 正交 Policy Exception，
  TargetProjectionRevision/TargetEnforcementGeneration 计算。
- `[M5-WP4 Verification]` Fake Enforcement Adapter、Decision History、Critical Audit 和 Contract Tests。

### 27.3 Required Tests

- 第 17.2 节中不依赖真实 Firewall 的全部测试。
- 同 Rule+Target 并发最多一个 Active Automatic Decision，重复命中不延长 ExpiresAt。
- Manual replace、Decision/Projection/Audit 原子事务，非法 State/EndReason 组合和终态不可逆。
- 临时 Allowlist 不终止 Decision，`/32` Allowlist 不错误撤销重叠 `/24` Decision。

### 27.4 Exit Gate

Decision→Projection Fake Slice 及并发、到期、重放、崩溃测试通过；Projection 可从
Active Decisions 全量重建；代码、CLI、Metric 和文档中不存在 Decision
`Pending/Revoking/Failed` 或 Decision 级 Retry。

---

## 28. M6 Firewall

目标：在最小权限进程中安全探测、规划和变更 Guard-owned Firewall 对象。

### 28.1 Entry Gate

M1 权限/IPC/timeout 可用；Firewall ownership、Snapshot、Plan/Apply 接口已 Frozen；
nftables Spike 通过。Apply-confirm Schema、状态机和跨重启回滚 Gate 未通过前，
只允许 Probe/Snapshot 与隔离环境写测试。

### 28.2 Required Artifacts

- `[M6-WP1 Contract]` Firewall Backend 接口、FirewallCapabilities、Fake Backend 和通用 Golden State 套件。
- `[M6-WP2 Backends]` nftables-native、iptables-nft/legacy + ipset、无 ipset fallback，UFW 和 Docker INPUT/FORWARD 集成。
- `[M6-WP3 Planning]` Probe、Snapshot、domain-scoped Plan/Apply、RemoveManagedInfrastructure、owner conflict 与 drift 检测。
- `[M6-WP4 Recovery]` native timeout = `EffectiveUntil + SafetyGrace`，Apply-confirm/rollback 能力，Doctor capability report。

Phase 1 Release 必选支持矩阵如下；Release Evidence 必须把“当前受支持发行版”解析为
确切 OS/Image/Kernel/Firewall/Docker 版本，禁止只写 `latest`：

| 类别 | 环境 / Backend | Release 要求 |
|---|---|---|
| Required | Ubuntu 受支持 LTS + nftables-native | INPUT、IPv4/IPv6、timeout、drift 全部通过 |
| Required | Ubuntu 受支持 LTS + UFW | 保留 foreign/UFW ownership，重载后收敛 |
| Required | Debian stable + iptables-nft + ipset | INPUT、IPv4/IPv6、timeout、重启恢复全部通过 |
| Required | Debian stable + iptables-legacy + ipset | Golden State 与 foreign preservation 全部通过 |
| Required | Debian stable + iptables，无 ipset fallback | 明确能力降级、cleanup 与 Doctor 结果全部通过 |
| Required | Docker Engine + 已支持宿主 Backend | Host INPUT 与 published-port FORWARD 链路全部通过 |
| Detected but Unsupported | 未知 Firewall Backend、无法证明 ownership 的自定义拓扑 | Probe 后拒绝 mutation，并输出稳定原因 |
| Future / Out of Scope | Kubernetes / CNI enforcement | 不实现、不进入 Phase 1 支持声明 |

### 28.3 Required Tests

- Firewall 写测试只能在 disposable VM/network namespace/专用容器环境执行，禁止修改开发机或生产 Firewall。
- 每个 Backend 运行同一 Golden State；Allowlist 只能 `RETURN`，禁止 `ACCEPT`。
- 只修改 Guard-owned 对象；同名 foreign 对象产生 OwnershipConflict 且保持不变。
- timeout/unknown 后先 Probe；nftables batch 原子性、iptables 降级、IPv4/IPv6、Manual CIDR、
  INPUT/FORWARD 和 Docker published port 全覆盖。
- Apply-confirm 的计划持久化、超时回滚、Agent/Enforcer restart 和管理来源防自锁测试通过。

### 28.4 Exit Gate

上表所有 `Required` 组合都有能力、Golden State、foreign preservation、
timeout/drift/install/uninstall 证据；任一 Required 组合未通过时，M6 与 Phase 1 Release
均为 `NO-GO`，不得通过缩小支持矩阵规避。只有上表明确列为 `Detected but Unsupported`
或 `Future / Out of Scope` 的组合可拒绝执行并不阻塞发布。

---

## 29. M7 Reconciliation

目标：以持久化 fencing、隔离 retry budget 和安全顺序把 Desired Intent 收敛为真实 Firewall 状态。

### 29.1 Entry Gate

M5 Decision/Projection 与 M6 Backend 已通过 Exit Gate；第 11 节安全顺序、fencing、
三个 failure domain 和 RetryEpoch 已 Frozen；Maintenance Contract 已冻结。

### 29.2 Required Artifacts

- `[M7-WP1 State]` Desired/Observed Firewall Snapshot builder，以及 `InfrastructureRevision`、
  `PolicyRevision`、`TargetProjectionRevision`、`TargetEnforcementGeneration`、
  `SnapshotRevision` 五类 revision/generation、Observed 侧的
  `ConfirmedTargetEnforcementGeneration` 和三个 domain retry ledger。
- `[M7-WP2 Planner]` Reconcile planner、单一 mutation executor、fencing、启动 Initial Reconcile、周期/事件唤醒。
- `[M7-WP3 Operations]` Maintenance enable/disable/status，Degraded/NotReady，domain retry CLI/API，drift recovery。
- `[M7-WP4 Verification]` 第 12.3、17.2、17.3 节完整故障注入和 Contract Tests。

### 29.3 Required Tests

- 三 domain 预算隔离，health flap/restart 不重置预算，管理员 Retry 只创建指定 RetryEpoch。
- Infrastructure/Policy 依赖阻塞不消耗 Target attempt，Policy 未收敛时不得提前扩大 Ban。
- 无关 Target 高频变化不得阻止稳定 Target Converged；stale result 不覆盖新 generation。
- Maintenance 中 Source/Detection/Decision 到期/Desired 更新继续，Firewall mutation 暂停；
  crash/restart 不自动 Full Reconcile，disable 后立即完整收敛。

### 29.4 Exit Gate

Fake 和所有已支持真实 Backend 可从 drift 最终收敛；Crash Matrix 的 DB Desired、
Firewall Snapshot、Observed、History、Retry 和 Audit 结果正确；任一 domain 耗尽预算后
只该 domain Degraded，不产生无限循环。

---

## 30. M8 Notification

目标：在不影响 Enforcement 的前提下，可靠地持久化、发送并审计 SMTP 通知。

### 30.1 Entry Gate

M1 Store/Config/Auth/secret ownership 可用，M5 Decision ID 与 `DecisionActivated` 提交边界
稳定；SMTP credential 权威源、Job 状态机、幂等键、退避、终态与 Cooldown 已冻结。

### 30.2 Required Artifacts

- `[M8-WP1 SMTP]` SMTP `none/starttls/tls`，默认证书验证、custom CA，secret 文件权限与脱敏。
- `[M8-WP2 Delivery]` `notification_jobs` 持久队列、worker、有界 retry/recovery，RuleID+Target cooldown。
- `[M8-WP3 Operations]` Notification history/API/Metric/Health 和 Fake SMTP/TLS 测试工具。

Phase 1 只为新建 Active Decision 定义 `DecisionActivated` 通知触发；其 Job 幂等键为
`node_id + decision_id + "decision_activated" + channel + template_version`。
Processing Coordinator 必须在持久化 Decision、Projection 和 Critical Audit 的同一
SQLite UnitOfWork 中写入 `notification_jobs`；worker 只消费
已提交 Job。Phase 1 保证 Job 至少一次处理，不承诺 SMTP exactly-once：连接中断发生在远端
已接收但本地未持久化成功之间时允许重复邮件。每次发送尝试必须持久化 attempt 与结构化结果；
模板中包含稳定的 `notification_job_id`，供收件方识别重复。重复风险不得反向改变 Decision、
Projection 或 Firewall 状态。

`notification_jobs` 写入失败属于 processing outcome 事务失败：整个 UnitOfWork 回滚，
receipt pipeline 重试该 RawRecord；它不是“通知失败不影响 Ban”的例外。只有 Job 已提交后
发生的 SMTP 连接、协议、TLS 或远端错误不得改变已提交的 Decision、Projection 或 Firewall。

### 30.3 Required Tests

SMTP 失败、timeout、临时/永久错误、crash-after-send、restart recovery、cooldown 并发、
STARTTLS downgrade、无效证书/hostname/custom CA 全部通过；credential 不进入日志/Metric/API；
同一 Decision Event 并发入队只产生一个 Job；ambiguous send 重启后允许重复投递但沿用稳定
`notification_job_id`；通知失败不回滚或阻塞 Ban。

### 30.4 Exit Gate

Notification Job 入队、发送、重试、恢复、Cooldown、TLS 与审计证据全部通过；SMTP
Degraded 不使 Agent NotReady；API/Metric 不泄漏 credential。Phase 1 不实现其他通知渠道。

---

## 31. M9 Built-in Rules

目标：交付经过真实发行版日志验证的 SSH/Nginx 开箱即用解析与检测闭环。

### 31.1 Entry Gate

M2–M5 已通过 Exit Gate 后，允许启动 `[M9-WP1]`–`[M9-WP3]`；M7 通过 Exit Gate 后
才允许启动 `[M9-WP4]` 和 M9 Exit。Built-in manifest、版本、用户覆盖/升级/回退语义
已冻结；每个 fixture 记录发行版/软件版本与脱敏来源。

### 31.2 Required Artifacts

- `[M9-WP1 SSH]` SSH Failed password、Invalid user、Authentication failure Parser，统一
  `event_type=auth.login_failed`，默认 Rule `5/10m` + Ban `1h`。
- `[M9-WP2 Nginx]` 单一 Nginx Access Parser，输出 source IP/method/path/status，交付 401/403/404 Rules。
- `[M9-WP3 Safety]` Nginx client IP/trusted proxy 边界、管理路径防自锁提示、Rule Exclusion 示例。
  默认只信任 socket `remote_addr`；只有配置非空 `trusted_proxies` 后才能启用 forwarded
  client-IP header，来源不在 trusted proxies 时忽略该 header。缺少或非法 trust boundary
  必须拒绝配置激活，禁止仅告警后继续运行。
- `[M9-WP4 Packaging/E2E]` Built-in manifest/version/fixtures 和日志→Event→Alert→Decision→Firewall E2E。

### 31.3 Required Tests

SSH 三类消息映射一致；Nginx IP 使用 `netip`；可伪造 `X-Forwarded-For` 的配置必须拒绝激活；
404 不默认排除整个静态目录；升级不静默覆盖用户修改。至少一条 SSH 和一条 Nginx
真实纵向链路通过，断言具体字段与状态，不只断言“无错误”。

### 31.4 Exit Gate

SSH/Nginx manifest、fixtures、解析、规则、升级保护和真实 Firewall E2E 全部通过；
发行版/软件版本证据可追溯，默认规则不会因 proxy 或管理路径配置造成已知自锁。

---

## 32. M10 Productization

目标：把 M1–M9 的能力封装为可安全安装、操作、升级、诊断和发布的 Standalone 产品。

### 32.1 Entry Gate

M1–M9 各自 Exit Gate 通过；OpenAPI、CLI exit code、Retention、upgrade、uninstall/purge
与前端技术栈/依赖已冻结并得到批准。

### 32.2 Required Artifacts

- `[M10-WP1 Web]` Dashboard、Sources、Parsers、Rules、Alerts、Bans、Notifications、Audit、Settings 页面。
- `[M10-WP2 API/CLI]` `/api/v1/` OpenAPI 与 CLI 功能闭环；Decision History、append-only 应用层 Audit、Retention cleanup。
- `[M10-WP3 Operations]` `/health`、`/ready`、`/metrics`、Doctor，service install/uninstall、purge、upgrade migration 与恢复说明。
- `[M10-WP4 Docs/Performance]` Benchmark harness、安装/配置/安全/Firewall/Docker/备份恢复/排障文档。
- `[M10-WP5 Release]` 版本化发布包、checksum 和绑定 commit 的 Release Evidence Manifest。

### 32.3 Required Tests

- 所有管理写操作经认证、CSRF/Origin 校验并写 Critical Audit；remote HTTP 必须显式开启并给出安全警告。
- API/CLI/Web 业务语义一致；Retention 不删除 Active Decision、未完成 Notification 和恢复所需记录。
- uninstall 仅删除服务与 Guard-owned Firewall 对象并保留 config/data；purge 二次确认后删除明确列出的 Guard-owned 数据。
- upgrade 覆盖成功、失败、kill/断电与备份恢复；Phase 1 不承诺自动 downgrade。
- 基准环境 `1 vCPU, 512MB–1GB RAM`，1,000 lines/s 时 Average CPU `<10%`、P95 CPU `<25%`、RSS `<100MB`；这是开发验收，不是 SLA。

### 32.4 Exit Gate

全部 Phase 1 测试矩阵、安装/首启/升级/重启/卸载/purge/恢复演练和性能目标通过；
OpenAPI、CLI、Web、Config Schema 与实现一致；无 Critical/High 安全缺陷；发布包、
migration、文档与证据绑定同一 commit。

---

## 33. 跨里程碑依赖图

```text
M0 Frozen → M1 Runtime → M2 Sources → M3 Parser → M4 Detection → M5 Decision ─┐
                └─→ M6 Firewall ───────────────────────────────────├→ M7 Reconcile
M1 Runtime → M8 Notification foundation; M5 → M8 Decision integration        │
M2 + M3 + M4 + M5 → M9 Built-ins; M7 → M9 real Firewall E2E           │
M1 + M2 + M3 + M4 + M5 + M6 + M7 + M8 + M9 ──────────────────────┘
                                      ↓
                                  M10 Release
```

M6 可与 M2–M5 并行，但 M7 必须等待 M5/M6；M8 队列/SMTP 可提前，Decision enqueue
集成等待 M5；M10 可提前建设只读 UI/CI 骨架，Exit Gate 必须等待 M1–M9。
任一里程碑要改变 M0 Frozen Contract，必须先走 Contract Change Review 并补 ADR/回归证据。

---

## 34. Phase 1 通用 Definition of Done

每个 Work Package 只有同时满足以下条件才可标记 Done：

1. Entry Gate 通过，接口、Schema、依赖、数据库或权限变更已按规则批准。
2. 只实现当前范围，没有 Phase 2 空壳、假想扩展点或未请求的 fallback。
3. 单元测试覆盖正常、错误、边界和并发路径，关键断言验证具体业务状态。
4. Contract/integration/crash/security/upgrade 中相关测试已执行，未执行项标记 `NOT RUN`。
5. compile、format、lint、race、migration 和派生产物 drift 检查通过。
6. Config Schema、migration、OpenAPI、CLI help、Metric/Health/Audit 和运维文档已同步。
7. 没有静默吞错、无限 retry、secret 泄漏、高基数 label、foreign Firewall 变更或 CLI 直写 DB。
8. Evidence Manifest 记录 commit、环境、精确命令、数量、结果、失败项、已知限制和产物摘要。

---

## 35. CI 与验证证据

```text
static      → format + lint + compile + generated/schema drift
test-fast   → unit + race + migration + fake contract slices
test-linux  → file rotation + journald + signal/restart
firewall    → nftables + UFW + iptables variants + Docker（隔离环境）
security    → auth/session/CSRF + secret + permission/systemd + dependency scan
e2e         → SSH + Nginx + maintenance/apply-confirm + notification + upgrade/purge
benchmark   → 1,000 lines/s resource target
package     → binaries + config/schema/migrations + systemd/docs + checksum/manifest
```

Firewall job 无隔离环境时必须标记 `NOT RUN` 并阻塞 Release，禁止改用宿主机验证。
每个里程碑 Evidence Manifest 至少保存 Milestone、Commit、Build/Test Environment、
Exact Command、Start/Finish Time、Passed/Failed/Skipped、Failure Summary、Artifact Checksum、
Known Limitations 和 Reviewer。“测试文件存在”“Schema VALID”“成功构建”都不能单独代替 Gate。

---

## 36. Phase 1 Release Gate

只有以下条件全部满足，才允许标记 `Phase 1 Released`：

### 36.1 Contract 一致性

- M0 保持 Frozen，所有变更均有 ADR/Contract Change 记录与回归证据。
- Decision、Retry CLI、Metric、History 不存在被废弃的 Decision `Failed` 语义。
- migration、Config Schema、OpenAPI、CLI golden tests、代码和行为测试无冲突。
- Phase 1 文档不存在影响实现、安全或运维的 TBD。

### 36.2 功能、故障与安全

- File/Journald→Parser→Detection→Decision→Reconcile→Firewall 完整链路通过。
- Notification 独立失败不影响 Enforcement；SSH/Nginx 真实纵向链路通过。
- M0 Crash Matrix 和 Backend/OS 发布矩阵通过，foreign Firewall 对象保持不变。
- Maintenance、Apply-confirm、upgrade、uninstall/purge、Auth/CSRF、权限/secret、
  remote HTTP 和管理面自锁边界通过。
- 不存在 Critical/High 安全缺陷或影响安全意图一致性的未解决缺陷。

### 36.3 运维、性能与发布

- Health/Ready/Metrics/Audit/Doctor 能定位所有声明的 Maintenance、Degraded、NotReady 和降级能力。
- 资源上限均具备配置、默认/范围、触顶行为、错误码、Metric 和 Health 影响。
- 性能目标、安装/备份恢复/升级演练通过，文档可执行。
- 发布包、版本、migration、文档、checksum 和 Evidence Manifest 绑定同一 commit。

### 36.4 最终结论

```text
GO

M0 与 M1–M10 全部 Gate 已通过。
Phase 1 Standalone Agent 可以发布。
```

或：

```text
NO-GO

未通过项：<Gate / 证据缺口 / 责任里程碑>
影响：<正确性 / 安全 / 一致性 / 可运维性>
修复后重新验证：<命令 / 环境 / 期望证据>
```

禁止使用“后续补测试”“实现时处理”或未记录的人工 waiver 放行。
