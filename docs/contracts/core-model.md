# Guard Phase 1 Core Model Contract

## 1. 文档定位

本文是 Phase 1 M0-A1 Core Model 产物，负责把主 Contract 中的权威关系、
SecurityEvent、Decision、Desired/Observed 和 Reconcile 核心语义整理成可由 Go
代码唯一表达的模型边界。

本文不复制完整 DDL、Config Schema、OpenAPI、IPC Schema 或具体实现。各类产物的
权威源保持如下：

| 内容 | 唯一权威源 |
|---|---|
| 架构取舍与业务不变量 | Phase 1 主 Contract、已批准 ADR 与本文 |
| Go 类型、构造器和接口 | M0-D 编译通过的 Go 代码 |
| SQLite 表、列、约束和索引 | migration SQL |
| 配置字段与默认值 | Config Schema |
| 对外 HTTP / IPC 契约 | 对应版本化 Schema 与 golden tests |
| 行为是否成立 | 自动化 Contract Tests |

若本文与 migration 或 M0-D Go 代码出现结构性差异，不得靠运行时转换长期维持两套
模型；必须回到主 Contract 判断业务语义，再在同一变更中同步所有权威产物。

## 2. 权威关系

```text
Active Decisions
    = 唯一安全意图事实
    ↓ 可重建
Desired Ban Projection
    = 安全意图物化投影

Desired Ban Projection
    + Allowlist
    + Protected Targets
    + Firewall Config
    + Backend Infrastructure Schema
    ↓ 规范化
Desired Firewall Snapshot
    = Reconciler 的完整期望输入

Firewall Snapshot / Probe
    = 外部执行状态的当前观察事实
    ↓ 经 fencing 确认后缓存
DB Observed State
    = 最近一次已确认观察，不是 Firewall 事实源
```

必须满足：

1. Decision 不保存 Firewall 执行成功、失败或重试状态。
2. Desired Ban Projection 必须能够从 Active Decisions 全量重建。
3. Policy 只改变最终执行效果，默认不终止 Decision。
4. Reconciler 每次从完整 Desired Firewall Snapshot 规划，禁止只同步 blacklist。
5. DB Observed State 不能替代启动 Probe，也不能作为跳过 Probe 的依据。
6. 一个全局 revision 不得同时承担投影版本、Policy 版本、Infrastructure 版本、
   Target fencing 和 Retry budget。

## 3. Go 模型边界

本节给出 M0-D Go 代码必须能够表达的最小类型集合。字段名可以在实现中调整，
但不得合并语义不同的类型，也不得以裸 `string`、`map[string]any` 或单个全局版本号
绕过这里的不变量。

### 3.1 强类型身份与版本

```go
type NodeID string
type SourceID string
type DeliveryID string
type EventID string
type ParserID string
type ParserVersion string
type RuleID string
type RuleVersion string
type AlertID string
type DecisionID string

type TargetProjectionRevision uint64
type TargetEnforcementGeneration uint64
type PolicyRevision uint64
type InfrastructureRevision uint64
type SnapshotRevision uint64
type RetryEpoch uint64
```

这些类型禁止相互隐式替代。ID 必须由边界构造器校验后进入 domain；从 SQLite
读取的值同样属于不可信边界，必须验证。`NodeID`、File Generation、Delivery ID 和
Event ID 的编码遵循主 Contract，业务层不得自行生成另一种编码。

### 3.2 SourcePosition

`SourcePosition` 必须是封闭的类型化联合，至少支持 File 与 Journald；禁止只把位置
塞入 RawRecord Metadata。

```go
type FilePosition struct {
    Generation  string
    DeviceID    uint64
    Inode       uint64
    StartOffset uint64
    EndOffset   uint64
}

type JournaldPosition struct {
    Cursor string
}

type SourcePosition struct {
    file     *FilePosition
    journald *JournaldPosition
}
```

`SourcePosition` 必须通过 File/Journald 构造器创建，私有字段仅表示“封闭联合”的一种
可编译形态；实现也可使用等价的私有 discriminator。一个 Position 必须恰好是 File 或
Journald，不能同时为空或同时有效。File 范围固定为半开区间
`[StartOffset, EndOffset)`，且 `StartOffset <= EndOffset`。

### 3.3 SecurityEvent

```go
type SecurityEvent struct {
    ID EventID

    ObservedAt time.Time
    Timestamp  *time.Time

    NodeID         NodeID
    SourceID       SourceID
    SourcePosition SourcePosition
    ParserID       ParserID
    ParserVersion  ParserVersion
    EmittedIndex   uint32

    EventType string
    Source    Endpoint
    Target    Endpoint
    User      *UserInfo
    HTTP      *HTTPInfo
    Service   string
    Labels    map[string]string
    Fields    map[string]any
}
```

模型必须区分：

- System-owned：ID、ObservedAt、NodeID、SourceID、SourcePosition、ParserID、
  ParserVersion、EmittedIndex。
- Parser-owned：Timestamp、EventType、Source、Target、User、HTTP、Service、
  Labels、Fields。

Parser API 不得接收可修改 system-owned 字段的 builder。`ObservedAt` 用于 Detection
Window；`Timestamp` 只表达日志声明时间，解析失败不得回退成 ObservedAt。

Event ID 使用主 Contract 冻结的 `evt1_` 编码，输入为 NodeID、DeliveryID、ParserID、
ParserVersion 和 EmittedIndex。Rule ID/Version 不进入 Event ID；Detection outcome 的
幂等键是 `EventID + RuleID + RuleVersion`。

### 3.4 Decision

```go
type DecisionSource uint8

const (
    DecisionSourceAutomatic DecisionSource = iota + 1
    DecisionSourceManual
)

type DecisionState uint8

const (
    DecisionActive DecisionState = iota + 1
    DecisionExpired
    DecisionRevoked
)

type DecisionEndReason string

const (
    EndReasonExpired       DecisionEndReason = "expired"
    EndReasonManual        DecisionEndReason = "manual"
    EndReasonManualReplace DecisionEndReason = "manual_replace"
    EndReasonRuleDisabled  DecisionEndReason = "rule_disabled"
    EndReasonSystemCleanup DecisionEndReason = "system_cleanup"
)

type Decision struct {
    ID              DecisionID
    NodeID          NodeID
    Source          DecisionSource
    RuleID          *RuleID
    RuleVersion     *RuleVersion
    AlertID         *AlertID
    CanonicalTarget netip.Prefix
    CreatedAt       time.Time
    UpdatedAt       time.Time
    LastTriggeredAt time.Time
    ExpiresAt       *time.Time
    EndedAt         *time.Time
    State           DecisionState
    EndReason       *DecisionEndReason
    SuppressedCount uint64
}
```

Phase 1 Automatic Action 只有 `ban`，不为它建立可被错误赋值的开放枚举。Scope 是
Policy/Firewall 派生执行属性，不属于 Decision 身份。

合法组合：

| State | EndReason | 附加条件 |
|---|---|---|
| Active | `nil` | `EndedAt=nil`；参与 Projection |
| Expired | `expired` | 只能由到期路径产生 |
| Revoked | `manual` | 显式人工撤销 |
| Revoked | `manual_replace` | 仅 Manual |
| Revoked | `rule_disabled` | 仅 Automatic |
| Revoked | `system_cleanup` | 仅明确清理流程 |

只允许 `Active → Expired` 或 `Active → Revoked`。终态不可复活，重复终止不得改写
原 EndReason。Automatic 必须有 RuleID；Manual 不使用 RuleID 构成身份。

逻辑唯一键为：

```text
Automatic Active = NodeID + RuleID + CanonicalTarget
Manual Active    = NodeID + CanonicalTarget
```

Action、Scope 和 RuleVersion 均不进入键。唯一冲突只能原子更新 LastTriggeredAt 与
SuppressedCount，禁止延长 ExpiresAt。具体 partial unique index 只在 migration 中定义。

### 3.5 Desired Ban Projection

```go
type BanProjectionState uint8

const (
    BanProjectionAbsent BanProjectionState = iota
    BanProjectionPresent
)

type DesiredBanProjection struct {
    NodeID           NodeID
    CanonicalTarget  netip.Prefix
    State            BanProjectionState
    ActiveCount      uint64
    EffectiveUntil   *time.Time
    Revision         TargetProjectionRevision
}
```

- ActiveCount 大于 0 时 Present；等于 0 时 Absent。
- 全部 Active Decision 都有限期时，EffectiveUntil 取最大值。
- 任一 Active Decision 永久时，EffectiveUntil 必须为 nil。
- Decision ID 列表等解释字段可以另存，但不能进入 Firewall Intent 等价判断。

### 3.6 Normalized Target Enforcement Intent

```go
type BanMembership uint8
type TimeoutMode uint8
type PolicyCoverage uint8
type AddressFamily uint8
type EnforcementScope uint8

type NormalizedTargetEnforcementIntent struct {
    NodeID                 NodeID
    CanonicalTarget        netip.Prefix
    BanMembership          BanMembership
    EffectiveUntil         *time.Time
    TimeoutMode            TimeoutMode
    Scopes                 EnforcementScope
    AddressFamily          AddressFamily
    PolicyCoverage         PolicyCoverage
    PolicyRelationDigest   string
    BackendAttributesDigest string
    Generation             TargetEnforcementGeneration
}
```

各枚举必须是封闭集合：

- BanMembership：Absent / Present。
- TimeoutMode：None / Native。
- PolicyCoverage：None / Partial / Full。
- AddressFamily：IPv4 / IPv6。
- EnforcementScope：INPUT、FORWARD 的受控组合。

只有上述对 Firewall 有意义的规范化属性变化时，才产生新的
TargetEnforcementGeneration。仅 ActiveCount 或 Decision 构成变化而最终属性不变时，
只增加 TargetProjectionRevision，不刷新 Target Retry budget。

### 3.7 Desired Firewall Snapshot

```go
type DesiredFirewallSnapshot struct {
    SnapshotRevision       SnapshotRevision
    InfrastructureRevision InfrastructureRevision
    PolicyRevision         PolicyRevision
    Infrastructure         ManagedInfrastructureIntent
    Policy                 ManagedPolicyIntent
    Targets                []NormalizedTargetEnforcementIntent
}
```

Snapshot 创建后不可变。传给 Reconciler 或 Firewall Backend 前必须完成深拷贝或所有权
转移；禁止并发修改 slice/map。Targets 必须按 CanonicalTarget 稳定排序，便于 digest、
测试和 drift 比较，但排序本身不构成业务身份。

### 3.8 Physical Observed 与 DB Confirmed State

```go
type ObservedMembership uint8
type ObservedPolicyCoverage uint8

type PhysicalTargetObserved struct {
    CanonicalTarget      netip.Prefix
    ObservedAt           time.Time
    Backend              string
    BanMembership        ObservedMembership
    PolicyCoverage       ObservedPolicyCoverage
    PolicyRelationDigest string
    TimeoutMode          TimeoutMode
    NativeExpiry         *time.Time
    Scopes               EnforcementScope
    AddressFamily        AddressFamily
    OwnerVersion         string
    LastErrorCode        string
}

type ConfirmedTargetState struct {
    PhysicalTargetObserved
    ConfirmedGeneration TargetEnforcementGeneration
}
```

Backend 只能返回 Physical Observed，不得填应用层 generation。只有 Reconciler 在完整
物理属性与当前 Desired Intent 匹配、且回写 fencing 仍有效时，才能写入
ConfirmedGeneration。Probe 失败、timeout 或结果不确定时，ObservedMembership 必须为
Unknown，ObservedPolicyCoverage 必须为 Unknown；不得沿用旧值冒充当前事实。

### 3.9 Reconcile Domain State

```go
type ReconcileStatus uint8

const (
    ReconcilePending ReconcileStatus = iota + 1
    ReconcileApplying
    ReconcileConverged
    ReconcileRetryWaiting
    ReconcileDegraded
)

type InfrastructureRetryKey struct {
    Revision InfrastructureRevision
    Epoch    RetryEpoch
}

type PolicyRetryKey struct {
    Revision PolicyRevision
    Epoch    RetryEpoch
}

type TargetRetryKey struct {
    Target     netip.Prefix
    Generation TargetEnforcementGeneration
    Epoch      RetryEpoch
}

type RetryState struct {
    Status        ReconcileStatus
    AttemptCount  uint32
    LastAttemptAt *time.Time
    NextAttemptAt *time.Time
    LastErrorCode string
}
```

三个 key 必须分别持久化，禁止改成一个接受任意字段的通用 map。依赖阻塞是 Planner
派生原因，不是第四个持久化状态，也不消耗 attempt。

## 4. 事务边界

### 4.1 Processing UnitOfWork

`processor` / Processing Coordinator 是 processing attempt 唯一事务协调者：

```text
freeze attempt-local Parser/Rule snapshot
  → begin UnitOfWork
  → Parser outcome
  → Detection contribution/outcome
  → Alert / Automatic Decision request
  → Decision / Projection / revisions
  → Critical Audit
  → processing_receipt
  → commit or rollback all
```

所有 domain service 只能接收显式 UnitOfWork/transaction handle，不得自行 begin、
commit、rollback 或使用旁路连接。Parser、Detection、Decision、Audit、Receipt 任一子写
失败，整次事务必须回滚，SourceDurable 不成立，checkpoint 不前移。

### 4.2 Decision 事务

以下操作必须分别在单个 SQLite 写事务中完成：

- 新建 Automatic Decision、处理重复触发、重算 Projection、更新 revision、写 Critical Audit。
- Manual ban；Manual replace 必须先终止旧 Decision，再创建新 Decision、重算 Projection、
  更新 revision、写 Critical Audit。
- Expiration batch：Active → Expired、Audit、Projection 与受影响 revision/generation。
- Manual revoke、Rule disable 或 system cleanup：终止、Audit、Projection 与 revision/generation。

事务提交前不得调用 Firewall。事务提交后才允许唤醒 Reconciler。

### 4.3 Policy 与 Infrastructure 事务

Policy 变化必须原子更新 Policy 事实、PolicyRevision、受影响 Target 的规范化 Intent、
必要的 TargetEnforcementGeneration、SnapshotRevision 和 Critical Audit。

Infrastructure 配置变化必须原子更新 Infrastructure 事实、InfrastructureRevision、受影响
Target generation、SnapshotRevision，以及安全相关 Critical Audit。

### 4.4 Reconcile attempt 与外部副作用

Firewall mutation 禁止包含在 SQLite 事务中：

```text
短 DB 事务：persist attempt_count++ / Applying
  → commit
  → 外部 Firewall mutation
  → Probe / Snapshot confirm
  → 短 DB 事务：按 revision/generation fencing 回写结果
```

调用期间 crash、timeout 或结果未知均消费已持久化 attempt。旧 revision/generation 的
返回结果只能结束旧 attempt，禁止覆盖当前 Desired 或 Confirmed State。

## 5. 模块职责

| 模块 | 允许 | 禁止 |
|---|---|---|
| `source` | RawRecord、SourcePosition、checkpoint/receipt 协调 | 调用 Firewall |
| `processor` | 冻结 attempt snapshot、唯一 begin/commit/rollback | 把事务所有权下放给 domain service |
| `parser` | RawRecord → SecurityEvent/终态解析结果 | 覆盖 system-owned 字段 |
| `detection` | Event → Alert/Automatic Decision request | 直接写 Firewall |
| `decision` | 管理安全意图事实与 Projection | 保存 Apply/Retry 状态 |
| `enforcement` | Projection + Policy → Desired Intent/Snapshot | 把 Observed 当 Desired |
| `firewall` | Probe、Snapshot、Guard-owned mutation | 修改 foreign 对象；生成业务 generation |
| `reconcile` | 比较 Desired/Observed、Plan、fencing、Retry | 修改 Decision 以表示执行失败 |
| Web/API/CLI | 调用统一应用服务 | 直接写 SQLite |
| `notification` | 消费已提交业务事件 | 反向改变 Decision/Firewall 结果 |

不得为 Phase 2 创建空接口、空服务或兼容壳。

## 6. 构造与校验边界

必须通过构造器或等价校验入口建立以下对象：

- NodeID、DeliveryID、EventID 及 Parser/Rule identity。
- 已 `Mask()` 的 canonical `netip.Prefix`。
- 合法 State/EndReason 的 Decision。
- 枚举取值完整且内部一致的 Normalized Intent。
- generation/revision 非回退的 Desired Snapshot。
- 与指定 domain key 一致的 RetryState。

非法 SQLite 值、未知枚举、未 mask Prefix、空必填 ID、负逻辑时间关系或不可能的
State/EndReason 必须快速失败；禁止静默归零、猜测修复或映射为 Unknown。只有外部
Firewall 的观察不确定性使用 Observed Unknown。

## 7. 必须保持的核心不变量

1. 同一 Node + Rule + Target 最多一个 Active Automatic Decision。
2. 同一 Node + Target 最多一个 Active Manual Decision。
3. Automatic 与 Manual 可以同时存在，最终聚合为一个 Projection。
4. 重复 Automatic trigger 不延长 ExpiresAt。
5. Allowlist 默认不终止 Decision；Partial coverage 不删除整个 blacklist CIDR。
6. 任一永久 Active Decision 使 Projection EffectiveUntil 为 nil。
7. DecisionCount 变化但最终 Firewall 属性不变时，不生成新 Target retry budget。
8. Policy 变化只有影响某 Target 最终 Intent 时才更新该 Target generation。
9. Native timeout 是 failsafe，不是 Decision 或 Expiration 事实源。
10. Apply/Revoke 失败不改变 Decision；Desired 保持，Observed 不伪装收敛。
11. Infrastructure、Policy、Target retry budget 相互隔离。
12. 无关 Target 引起 SnapshotRevision 前进时，不得使已确认 Target 结果失效。
13. Backend 返回物理状态；Confirmed generation 由 Reconciler 在 fencing 后写入。
14. Critical Audit 与对应安全关键业务状态同事务提交。

## 8. 验证清单

### 8.1 类型与身份

- [ ] Go core model 编译通过，revision/generation 类型不能误传。
- [ ] File/Journald Delivery ID 及 Event ID golden vectors 固定。
- [ ] Event ID 跨进程、跨重启稳定；Node/ParserVersion/EmittedIndex 变化会改变 ID。
- [ ] Parser 无法覆盖任何 system-owned 字段。
- [ ] 非 canonical Prefix、未知枚举和非法 Decision 组合被拒绝。

### 8.2 Decision 与 Projection

- [ ] 并发 Automatic trigger 最多创建一个 Active Decision。
- [ ] 重复 trigger 只更新允许字段且不延长 ExpiresAt。
- [ ] Manual AlreadyBanned/replace 符合原子语义。
- [ ] State/EndReason 矩阵、终态不可逆和重复终止已覆盖。
- [ ] 多 Decision 聚合、永久/有限期混合、最后一个终止均正确。
- [ ] Projection 可从 Active Decisions 全量重建并与现存投影一致。
- [ ] Decision/Projection/Audit 任一写失败都会整体回滚。

### 8.3 Generation 与 Observed

- [ ] 解释字段变化只增加 TargetProjectionRevision。
- [ ] Intent 属性变化才增加 TargetEnforcementGeneration。
- [ ] 不相交 Policy 变化不更新无关 Target generation。
- [ ] Backend 无法伪造 Confirmed generation。
- [ ] Probe 失败/timeout 写 Unknown；旧 Confirmed 状态不冒充当前事实。
- [ ] stale completion 不能覆盖新 generation。
- [ ] Target B 高频变化不阻止 Target A Converged。

### 8.4 事务与崩溃

- [ ] Processing 子写错误导致 receipt/outcome/audit 整体回滚，checkpoint 不前移。
- [ ] Decision commit 前 crash 不产生 Firewall 副作用。
- [ ] Decision commit 后、Firewall 前 crash 可从 Desired 恢复。
- [ ] Firewall 成功后、Confirmed 回写前 crash 可通过 Probe 收敛。
- [ ] attempt commit 后、外部调用前 crash 不回退 attempt。
- [ ] Firewall timeout 后下一次 mutation 前必先 Probe。

## 9. 仍需 Spike / Gate 证明

本文冻结模型目标，不把以下事项写成已经验证：

1. SQLite partial unique index 在真实并发 trigger 下的行为与错误映射。
2. Event/Delivery ID 的跨实现 golden vectors。
3. UnitOfWork 在 Parser、Detection、Decision、Audit、Receipt 各注入点的整体回滚。
4. Projection rebuild、revision/generation 持久化与 crash recovery。
5. Fake Firewall 对完整属性比较、Unknown、Confirmed generation 和 stale fencing 的证明。
6. 三个 Reconcile domain 的独立预算、dependency blocked 和管理员 RetryEpoch。
7. 真实 nftables apply-confirm 与 crash window；该项不由 Core Model 单独证明。

### 9.1 A1 文档完成与后续验证

A1 文档在以下条件满足后可以标记 `COMPLETE / Implemented`：

- 本文完整表达主 Contract 的权威关系、强类型边界与核心不变量；
- 与 A2/A3/A4 交叉审查没有未解决 P0/P1；
- review 结果形成可定位 Evidence。

类型编译、SQLite 并发、UnitOfWork 故障注入、Fake Firewall 和 crash recovery 属于
C1/C2/D1/D2/D5 的后续验证义务，不作为启动 C1/C2 前要求 A1 `Verified` 的循环前置。
只有对应自动化证据落盘后，相关模型能力才能从 `Implemented` 升级为 `Verified`；
文件存在或类型可编译本身不能代替这些运行证据。

## 10. 来源

- Phase 1 主 Contract §4：权威源、派生状态与 revision/generation 分层。
- Phase 1 主 Contract §7：SecurityEvent、system-owned 字段与 Event ID。
- Phase 1 主 Contract §10：Decision 生命周期、identity 与 Projection 输入。
- Phase 1 主 Contract §11：Desired/Observed、failure domains、fencing 与 Retry。
- Phase 1 主 Contract §22.2：模块边界与 Processing UnitOfWork 所有权。
