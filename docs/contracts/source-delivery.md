# Source Delivery 与 Processing Coordinator Contract

> 文档类型：M0-A2 实现子 Contract
>
> 规范成熟度：`Specified`；是否已验证、是否可进入下一 Gate 仅以
> [Phase 1 STATUS](../development/phase-1/STATUS.md) 为准
>
> 上位规范：[Phase 1 Contract](guard-phase-1-m0-contract-freeze-v0.3.md)
>
> 执行入口：[M0-EXECUTION](../development/phase-1/M0-EXECUTION.md)

## 1. 目的与权威边界

本文把上位 Contract 第 6–9、17.1、22.2 节收敛为 Source delivery 与
Processing Coordinator 的实现边界，供 M0 Slice、接口冻结和代码审查使用。

本文不重新定义以下内容：

- `DeliveryID`、`EventID` 的字节编码与 golden vector；以上位 Contract 第 6.4、7.4 节为准。
- SQLite 物理表、列名、索引与 migration；以 M0-D migration SQL 为唯一权威源。
- Parser DSL、Rule DSL、Firewall 或 Notification 行为。
- 当前工作包状态、负责人和 Gate 结果；只在 `STATUS.md` 维护。

若本文与上位 Contract 冲突，以上位 Contract 为准；冲突必须阻塞 A2 验收，禁止由实现自行选择。

## 2. 单轨模型

Phase 1 只实现以下单轨：

```text
Source 建立稳定 Position / DeliveryID
  → bounded queue（满时背压）
  → Processing Coordinator 冻结 attempt-local Processing Plan
  → 单一 SQLite UnitOfWork 提交全部必要 outcome + Critical Audit + receipt
  → ProcessingComplete 与 SourceDurable 同时成立
  → Completion Tracker 按 session-local DeliverySequence 连续确认
  → Checkpoint Manager 持久化最高连续 SourcePosition
```

禁止同时实现 durable inbox、含混 `ACK` 或以 queue enqueue 代替持久化屏障。Firewall
Apply/Revoke、SMTP 发送和其他事务外副作用不是 SourceDurable 前置条件。

## 3. 概念与不变量

### 3.1 三种身份各司其职

| 概念 | 责任 | 禁止用途 |
|---|---|---|
| `SourcePosition` | 从外部日志源恢复读取位置 | 不替代业务幂等键 |
| `DeliverySequence` | 单个 Source processing session 内的连续完成排序 | 不跨重启充当稳定身份 |
| `DeliveryID` | 同一原始记录跨 crash/replay 的稳定幂等身份 | 不以内容 hash 或 session sequence 代替 |

`DeliveryID` 必须由统一 identity 组件按上位 Contract 第 6.4 节生成。下游只接收并引用该值，
不得从 `Metadata`、日志内容或局部字段重新推导。receipt、重放去重和验证 evidence 必须引用同一值。

### 3.2 两个完成概念

- `ProcessingComplete`：不可变 Processing Plan 中所有已调度 Parser 及适用 Rule 已成功或终态拒绝，
  且必要业务 outcome、Critical Audit 和 terminal receipt 已成功提交。
- `SourceDurable`：已有足够持久化证据允许推进 Source checkpoint。

二者是不同事实；在 Phase 1 receipt pipeline 中，由同一个成功 UnitOfWork 同时建立。
任何内存状态、异步消息或事务提交前结果均不得声称二者之一成立。

### 3.3 Completion token

Coordinator 只有在以下任一条件成立时才可向 Completion Tracker 返回 durable completion：

1. 本次 UnitOfWork 已明确提交成功；或
2. 提交结果不确定后，按 `DeliveryID` 从 Store 重新读到有效 terminal receipt。

completion 至少携带 `SourceID`、`DeliveryID`、本 session 的 `DeliverySequence` 和
`SourcePosition`。不提供可表达“仅 ProcessingComplete”或“仅 SourceDurable”的成功分支。

## 4. 模块责任与接口约束

### 4.1 `source`

`source` 负责：

- 建立类型化 `SourcePosition`、稳定 `DeliveryID` 和 session-local `DeliverySequence`；
- 在发出 File generation 首条 RawRecord 前持久化 generation registry；
- 通过 bounded、可取消的阻塞 enqueue 把 RawRecord 交给 pipeline；
- 接收 durable completion，并驱动连续 checkpoint；
- 检测可观察的 lag、gap 和已知 data-loss 风险。

`source` 不解析 SecurityEvent，不写 Decision，不调用 Firewall，也不提交 processing outcome 事务。

### 4.2 `processor` / Processing Coordinator

Coordinator 是一次 processing attempt 的唯一事务协调者，按以下顺序负责：

1. 按 `DeliveryID` 检查 terminal receipt；已存在时跳过 Parser/Detection Window，直接形成可验证 completion。
2. 冻结 Active Parser Set；首次需要 Detection Rule 时冻结本 attempt 的 Rule Catalog。
3. 开启一个 Store UnitOfWork。
4. 在该 UnitOfWork 上调用 transaction-aware 的 Parser outcome、Detection contribution、
   Alert/Decision、Critical Audit 与 receipt writer。
5. 仅在所有必须写入成功后 commit；任一子写失败则 rollback。
6. commit 成功或 receipt read-back 证实成功后，才发布 durable completion。

Coordinator 是唯一允许 begin、commit、rollback 的模块。Domain service 只能接收显式
UnitOfWork/transaction handle；禁止内部开启事务、使用独立连接或把 receipt 延后到另一个事务。

### 4.3 Store 所需语义端口

M0-D 负责冻结可编译的具体 Go 接口；其接口必须完整表达以下操作，且不得增加第二条提交路径：

- 按 `DeliveryID` 查询 terminal receipt；
- 开始 processing UnitOfWork；
- 在同一 handle 上写 Parser terminal outcome、Detection contribution、Alert/Decision、
  Critical Audit 和 terminal receipt；
- commit / rollback，提交失败或结果不确定时显式返回；
- 持久化并读取 Source checkpoint；
- 持久化、恢复和按安全条件迁移 File generation lifecycle。

物理 Schema 可以把上述语义拆表，但不能改变单事务责任。

## 5. Processing attempt 状态闭环

| attempt 结果 | 可写 terminal receipt | 可形成 durable completion | checkpoint 可参与连续推进 |
|---|---:|---:|---:|
| 全部处理成功，包括零 Event / `NoMatch` | 是 | 是 | 是 |
| `RecordPermanent` 且 Critical Audit 同事务成功 | 是 | 是 | 是 |
| 已存在有效 receipt | 不重复写 | 是 | 是 |
| `PlanBlocked` | 否 | 否 | 否 |
| `Transient` | 否 | 否 | 否 |
| `Cancelled` | 否 | 否 | 否 |
| 任一必要 outcome / Audit / receipt 写失败 | 否，整体回滚 | 否 | 否 |
| commit 结果不确定且 read-back 未证实 receipt | 否 | 否 | 否 |

无法建立不可变 Parser/Rule snapshot 属于 `PlanBlocked` 或系统失败，不得伪装为
`RecordPermanent`。DB busy、临时 IO、worker 故障与 shutdown cancellation 同样不得终态化。

## 6. File generation 与 Journald 恢复

### 6.1 File generation

File Source 必须遵守上位 Contract 第 6.2 节的单向 lifecycle：
`Open → Draining → Sealed → Retired`，其中 `Open` 也允许直接进入 `Sealed`；并满足：

- 128-bit CSPRNG generation 原值在首条 RawRecord 发出前 durable persist；
- rename/create 为新旧文件分配不同 generation；copytruncate 即使 inode 不变也 seal 旧 generation；
- 新旧 generation 的 RawRecord 仍在同一 Source session 中分配连续 DeliverySequence；
- checkpoint 未安全越过、仍有 receipt/replay/reprocess 引用或仍需重放时，禁止 `Retired`；
- 清理前保留重建稳定 DeliveryID 所需字段；重启恢复全部非 `Retired` generation；
- 找不到旧 inode 时不得伪造恢复成功，按 Contract 写 `DataLossSuspected` Audit/Health。

generation registry 与 processing receipt 可以落在不同业务时点，但首条记录发出前持久化是硬屏障。

### 6.2 Journald

Journald cursor 作为 opaque `SourcePosition` 保存和引用；禁止排序、normalization 或结构猜测。
cursor 无效时必须执行已冻结的 `resume_policy` 并产生 Audit、Metric 和 Health 变化。

## 7. Receipt、幂等与 retention

Terminal Processing Record 的 Phase 1 物理实现是 `processing_receipt`。每条 Delivery
无论产生多个 Event 还是零 Event，都必须在成功路径提交唯一 terminal receipt。

receipt 的语义内容至少能证明唯一 Delivery 已进入成功或永久拒绝终态。Poison receipt
还必须保存上位 Contract 第 8.5 节要求的诊断字段：

- 唯一 `DeliveryID`；
- 对应 `SourcePosition`、failure stage、稳定 error code、sanitized error、terminal action 和发生时间；
- 与必要业务 outcome、幂等键和 Critical Audit 同事务提交。

具体列名由 migration 冻结。Alert、Decision、Detection outcome 和 Critical Audit 仍必须拥有各自的
数据库唯一约束；receipt 不能替代它们。

receipt 只有在对应 Source checkpoint 已持久化越过其 Position，且 retention、replay 和
explicit reprocess 均不再引用时才可删除。显式历史重处理必须使用新的 reprocess identity，
不得删除 receipt 后伪装成首次投递。

## 8. 连续 checkpoint

Completion Tracker 按每个 Source session 的 `DeliverySequence` 维护完成洞：

```text
next_expected = 已持久化 checkpoint 后本 session 的首个 sequence
收到 completion(seq):
  记录 seq 已 SourceDurable
  while next_expected 已完成:
    candidate_position = position(next_expected)
    next_expected++
  按 interval 或 record threshold 尝试持久化 candidate_position
```

约束：

- 后序完成不得越过前序空洞；乱序 completion 只能暂存，不能提前 checkpoint。
- checkpoint 持久化是 processing UnitOfWork 成功后的独立动作；checkpoint flush 前 crash
  允许重读，但 receipt 必须阻止重复持久化副作用和重复进入 Detection Window。
- checkpoint 写失败不得撤销已经提交的 receipt，也不得在内存中声称持久化成功；继续重试或重启恢复。
- Journald checkpoint 保存最高连续 sequence 对应 cursor；File checkpoint 保存对应 generation/offset。
- SQLite WAL checkpoint 与 Source checkpoint 是不同概念，禁止在接口、日志或指标中混称。

## 9. Poison、Critical Audit 与敏感数据

只有 `RecordPermanent` 可以形成 Poison 终态。`NoMatch` 是成功的零 Event 结果，不是 Poison。

Poison receipt 与影响连续性的 Critical Audit 必须在同一 UnitOfWork 中提交；Audit 失败时整体
rollback，delivery 保持未完成。错误内容必须使用稳定低基数 code，截断并清洗 message；禁止写入
凭据或未经截断的原始敏感日志。Metric 和 Operational Audit 仅用于观测，不是持久化屏障。

## 10. 背压、数据丢失边界与 shutdown

### 10.1 背压

RawRecord 与 SecurityEvent 核心队列必须 bounded。queue full 时生产者执行可取消阻塞，禁止静默
drop。仅上位 Contract 第 9.1 节允许的永久拒绝才能终态化后释放连续 checkpoint。

每个 queue 必须暴露 capacity、depth、backpressure duration、rejected total 和 oldest item age；
Source 还必须暴露 lag 与 inode/cursor gap。rotation、vacuum、destructive truncate 和
copytruncate fast-regrow 超出可恢复边界时，必须报告 Data Loss Audit/Health，不得扩大
at-least-once 声明。

### 10.2 Shutdown

SIGTERM 顺序固定为：停止管理面新写入 → Source 停止读取 → drain 已入 pipeline 的记录 →
flush 连续 checkpoint → flush 必须持久化的 Audit → 关闭 DB → 退出。

默认 timeout 与合法范围只引用上位 Contract 第 9.3、16 节。timeout 到期后 cancellation
不得产生 receipt；允许退出并在下次启动通过 replay 恢复。成功 drain 的记录必须满足正常
UnitOfWork 与 checkpoint 规则，禁止建立 shutdown 专用提交路径。

## 11. 故障时序

| 故障点 | 必须观察到的结果 |
|---|---|
| outcome UnitOfWork commit 前 crash | 无 receipt，无 durable completion，checkpoint 不前移 |
| commit 后、completion 发布前 crash | 重启按 DeliveryID 命中 receipt，跳过 Window，安全形成 completion |
| completion 后、checkpoint flush 前 crash | 允许重读；持久化副作用不重复 |
| 任一 Parser/Detection/Decision/Audit/receipt 子写失败 | UnitOfWork 整体回滚，checkpoint 不前移 |
| checkpoint 持久化失败 | receipt 保留；不得声称 checkpoint 已成功 |
| 新 generation 已处理、旧 generation 仍有洞时 crash | 新 DeliveryID 稳定；恢复后仍不得越洞 |
| shutdown timeout | 未提交 attempt 无 receipt；重启重放，已提交结果保持幂等 |

## 12. M0 Source Slice 验证映射

| 验证主题 | 上位测试 | 必须提供的 evidence |
|---|---|---|
| 事务屏障与 crash/replay | §17.1 #1–3、#13–14 | crash point 日志、DB 快照、重复副作用断言 |
| 连续 checkpoint 与 generation | §17.1 #4、#10–12、#16 | 乱序完成轨迹、checkpoint 前后值、restart 结果 |
| 多 Parser 与不可变计划 | §17.1 #5、#18 | snapshot identity、版本切换轨迹、receipt 断言 |
| 背压与 shutdown | §17.1 #6、#8–9 | queue 指标、drain/timeout 时序、重启结果 |
| Poison 分类 | §17.1 #7 | 四类错误注入、receipt/Health/checkpoint 断言 |
| Detection Window 幂等 | §17.1 #17 | 同进程 retry 的 count/distinct_count 前后值 |
| data-loss 边界 | §17.1 #15 | 可检测告警与 known limitation 声明 |
| SQLite durability | §17.1 #19 | 已声明故障域、PRAGMA read-back、crash/reboot 结果 |

Evidence 放置规则引用 `M0-EXECUTION.md` 的 A2、C1、D7 路径；测试条目存在不代表已通过。

## 13. A2 文档产物完成条件

A2 的文档审查必须同时确认：

1. receipt pipeline 是唯一 delivery model，不含 durable inbox 或含混 `ACK` 分支。
2. 只有 Coordinator 拥有 processing UnitOfWork 的 begin/commit/rollback 权限。
3. DeliveryID、generation、完成屏障、checkpoint、Poison、背压和 shutdown 均有唯一责任模块。
4. 第 12 节已把行为约束映射到上位 Contract 的全部 19 条 Source Slice 测试。
5. 物理 Schema、具体 Go 签名和实现策略均委托给各自权威产物，没有在本文重复冻结。
6. 尚无 Spike/evidence 的行为继续标记 `NOT VERIFIED`，没有以文档审查伪装实现 Gate 已通过。

A2 完成不依赖 C1 或 M0-D 已通过，否则会与 `A2 → C1 → M0-D` 的执行依赖形成循环。
C1、M0-D 和 M0 Gate 后续以本文为输入产生实现级证据；实时结论仍只在 `STATUS.md` 更新。

## 14. 尚待 Spike / Evidence 验证

以下项目是已指定约束的实现验证，不是重新开放双轨设计：

- `NOT VERIFIED`：SQLite PRAGMA、事务与 power-loss 声明故障域；由 M0-B1 / §17.1 #19 验证。
- `NOT VERIFIED`：File generation 在 rename/create、copytruncate、crash 下的持久化与清理；由 C1 验证。
- `NOT VERIFIED`：File/Journald DeliveryID golden vectors、receipt key 与跨进程稳定性；由 M0-D 验证。
- `NOT VERIFIED`：Detection Window 在 outcome rollback 后的同进程幂等策略；实现可选，行为由 §17.1 #17 验证。
- `NOT VERIFIED`：bounded queue 背压、30s 默认 shutdown timeout 与超时恢复；由 M0-B/C1 验证。
- `NOT VERIFIED`：UnitOfWork 具体 Go 签名、SQLite migration 和 error injection seam；由 M0-D 冻结并编译验证。
