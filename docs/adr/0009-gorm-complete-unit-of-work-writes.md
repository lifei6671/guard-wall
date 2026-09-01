# ADR-0009：一次性完成剩余 UnitOfWork GORM 写入迁移

## 状态

```text
Decision: Accepted
Validation maturity: DONE / Implemented
Date: 2026-09-01
```

用户明确要求不再逐项迁移，而是一次性完成当前 GORM-1 范围内全部剩余代码。本 ADR 取代
ADR-0003 对这三个函数“逐项授权”的节奏限制，但不改变其永久 raw 边界：migration、PRAGMA、
transaction-consistent snapshot、Store/Decision/source/reconcile lifecycle SQL 仍不在本批。

本批不修改 Schema、migration、公共 API、依赖、配置、Store pool 或事务最终化所有权；也不
代表 `Verified`、G18 PASS 或 M0 GO。

## Context

GORM-1a 至 GORM-1e 已将五个普通 UnitOfWork INSERT 迁移到绑定 existing raw `*sql.Tx` 的
private GORM session。`internal/store/uow.go` 仍有三个 raw SQL writer：

1. `PutDetectionContribution`：五列 INSERT、复合冲突 DO NOTHING、RowsAffected 与稳定
   DeliveryID read-back；
2. `PutProjection`：七列 revision-fenced upsert、RowsAffected 与同 revision 幂等 read-back；
3. `PutReceipt`：十六列终态 INSERT，承载 file/journald、success/permanent、deferred FK 与
   crash/replay 边界。

用户已明确授权把这三个剩余 writer 作为一个 Tier-3 delivery unit 整体迁移、验证与审查。

## Decision

### 1. 完整且封闭的迁移范围

本批只迁移：

```text
UnitOfWork.PutDetectionContribution
UnitOfWork.PutProjection
UnitOfWork.PutReceipt
```

迁移完成后，`internal/store/uow.go` 不再直接调用 `u.tx.ExecContext`、
`u.tx.QueryContext` 或 `u.tx.QueryRowContext`。三个函数的 Create/upsert 与必要 read-back 均通过
`u.transactionORM.WithContext(ctx)` 执行，但仍绑定同一个 raw transaction。

### 2. 私有显式 row model

新增三个未导出模型，所有映射字段均有紧邻简体中文用途注释：

- `detectionContributionRow`：`detection_contributions` 五列；
- `desiredBanProjectionRow`：`desired_ban_projections` 七列；
- `processingReceiptRow`：`processing_receipts` 十六列。

每个写入使用显式 `Select` 固定列及顺序。模型不嵌入 `gorm.Model`，不使用 association、hook、
soft delete、隐式时间、默认列或自动 migration。

### 3. DetectionContribution 冲突与稳定身份

Create 使用 `(event_id, rule_id, rule_version) DO NOTHING`。`RowsAffected == 1` 仍表示本事务首次
写入并返回 `true`；`RowsAffected == 0` 时必须在同一 private transaction session 读取既有
`delivery_id`：相同则返回 `false, nil`，不同则进入 sticky first-error。
Create 显式挂载 `gorm.WithResult()` 并重新调用底层 `sql.Result.RowsAffected()`，不得依赖 GORM
会吞掉该错误的 `DB.RowsAffected`；读回无记录时把 `gorm.ErrRecordNotFound` 归一回原契约的
`sql.ErrNoRows`。

五列顺序保持：

```text
event_id, rule_id, rule_version, delivery_id, contributed_at_us
```

复合主键、rule-version immediate FK、receipt deferred FK、UTC Unix microseconds 与 context
取消语义不变。

### 4. Projection revision fence 与幂等读回

Create 使用 `(node_id, canonical_target) DO UPDATE`，只显式更新：

```text
state, active_count, effective_until_us, target_projection_revision, updated_at_us
```

更新条件必须保持：

```sql
excluded.target_projection_revision
    > desired_ban_projections.target_projection_revision
```

`RowsAffected == 1` 表示 insert 或更高 revision update。零行时，在同一 private transaction
session 读取 `node_id`、target、state、active count、effective-until 和 revision；完全相同视为
幂等成功，其他情况仍以 stale/conflicting revision 进入 sticky first-error。read-back 继续不比较
`updated_at_us`，保持原契约。
该 Create 同样通过 `gorm.WithResult()` 保留底层 `sql.Result.RowsAffected()` 错误，不得把错误
降格为普通零行冲突。

七列顺序保持：

```text
node_id, canonical_target, state, active_count, effective_until_us,
target_projection_revision, updated_at_us
```

absent/present closed union、SQL NULL、node immediate FK、UTC Unix microseconds 和 revision
单调性不变。

### 5. Receipt 终态与 crash/replay

Receipt 继续在 Create 前执行 `ready(ctx)`、`ProcessingReceipt.Validate`、`encodePosition` 与
`encodeReceipt`。显式十六列顺序保持：

```text
delivery_id, source_id, position_kind, generation, device_id, inode,
start_offset, end_offset, journald_cursor, kind, failure_stage,
failure_code, sanitized_error, terminal_action, failure_occurred_at_us,
committed_at_us
```

file/journald 和 success/record_permanent 两组 closed union、十一个 nullable 列、file uint64 到
SQLite int64 的范围检查、DeliveryID 与 source/position 绑定、UTC Unix microseconds、delivery
primary key、source/source-generation immediate FK、retired-generation trigger，以及 parser/
detection outcome deferred FK 在 Receipt 写入后提交的顺序全部保持。

### 6. 事务、错误与保留 raw 边界

`Store.BeginProcessing` 创建的 raw `*sql.Tx` 仍是唯一事务；UnitOfWork 继续独占 Commit/Rollback。
GORM session 不得 begin、commit、rollback、close、unwrap 或逃逸到 root pool。所有调用显式传递
caller context；任一验证、Create、冲突读回或约束错误都进入 sticky first-error，后续 writer 被
抑制，Commit 拒绝并整体回滚。

以下内容继续保持 raw，且不属于“完成剩余 GORM-1 writer”范围：

- checksummed migration、migration ledger 与所有 PRAGMA；
- transaction-consistent snapshot、commit-unknown 与 checkpoint/source generation lifecycle；
- Decision create/suppress/expire/revoke/replace lifecycle；
- desired/observed/reconcile state 的 CAS、generation/revision fence 与 read-back；
- 测试 fixture、Schema cross-check 与底层 GORM Dialector ConnPool 适配。

## Consequences

### 正向结果

- UnitOfWork 八个 processing writer 全部通过同一个受限 private GORM transaction session；
- 普通 INSERT、DO NOTHING、revision-fenced upsert 和必要 read-back 均有显式模型、列与测试；
- 不再需要为 GORM-1f/1g/1h 分别等待授权和审查。

### 代价与残余风险

- 本批同时覆盖冲突语义、revision fence 与 crash/replay，必须采用分区测试和最终集成审查；
- GORM clause builder 或 RowsAffected 行为漂移会影响幂等性，必须冻结 exact SQL/Vars 与读回路径；
- 真实 native Linux Race、非 WSL Linux、CI、漏洞/SBOM、性能与 power-loss durability 仍需独立证据。

## Verification

至少验证：

- 三个 model/table、显式 5/7/16 列、exact SQL/Vars 与 callback 次数；
- Contribution 首次/相同重复/不同 DeliveryID、PK/FK、RowsAffected、读回、sticky/cancel；
- Projection insert/更高 revision/same revision 幂等/stale/conflict、present/absent NULL、PK/FK；
- Receipt 四种 closed-union 组合、十一项 NULL、整数边界、时间 CHECK、PK/immediate/deferred FK、
  retired generation 与 DeliveryID 绑定；
- 同一 raw transaction 内可见、commit/rollback、root-pool nonescape 与 sticky first-error；
- processing semantic、receipt/commit-unknown、source-generation、Decision 与 SIGKILL/replay 回归；
- gofmt、focused/full Race、Vet/module、依赖闭包、字段注释、三目标 CGo-free build、Ubuntu WSL2
  current-hash replay、`git diff --check` 与 Tier-3 分区加集成审查。

## Rollback

本批无 Schema 或数据格式迁移。代码回滚时恢复三个函数原 raw INSERT/upsert/read-back，删除三个
private row model 和本批专项测试；其他已验收 GORM-1a 至 GORM-1e writer 与 private transaction
session 保持不变，数据库文件和 migration ledger 无需转换。
