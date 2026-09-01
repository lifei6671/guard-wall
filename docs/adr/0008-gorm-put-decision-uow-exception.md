# ADR-0008：GORM PutDecision UnitOfWork 窄化例外

## 状态

```text
Decision: Accepted
Validation maturity: DONE / Implemented
Date: 2026-09-01
```

用户已明确确认 GORM-1e，并已通过本批 Code Review。本 ADR 只授权将
`UnitOfWork.PutDecision` 的单条 15 列 `INSERT` 从手写 SQL 迁移为 GORM `Create`，并复用
ADR-0004 已建立的 raw-tx-bound 私有 GORM 会话。本批不得描述为 `Verified` 或 Gate
`PASS`，也不提升 M0 Gate。

## Context

`PutDecision` 在 SQL 前完成 Decision 校验、automatic/manual 引用规则、生命周期必填时间、
source 与 state 枚举转换，随后插入一条 immutable Decision identity/lifecycle row。该写入无
`ON CONFLICT`、`RowsAffected`、`RETURNING`、read-back、CAS 或 revision fence，适合作为
独立小批次；但 automatic/manual、active/expired/revoked、SQL `NULL`、三个 immediate
foreign key、primary key 与两条 active partial-unique 均属于持久化契约，不能交给 GORM
默认值、association 或零值推断。

## Decision

### 1. 唯一迁移对象

GORM-1e 只迁移：

```text
UnitOfWork.PutDecision
```

现有 `ready(ctx)`、`decision.Validate()`、automatic 必须有 rule version、manual 不得引用
rule/rule-version/alert、生命周期时间必填，以及 source/state 转换都保持在 Create 前。只有
最终写入改用既有 private session 的 `WithContext(ctx).Select(...).Create(...)`。错误继续
保留 `put decision <id>` 上下文，并进入 UnitOfWork sticky first-error；禁止 retry、fallback
或错误吞并。

### 2. 复用同一 raw transaction

本批不新增 transaction wrapper、opener、DSN、连接或 pool。`Store.BeginProcessing` 创建的
raw `*sql.Tx` 仍是唯一事务，UnitOfWork 独占 Commit/Rollback。GORM session 不得 commit、
rollback、close、unwrap、重新 begin 或逃逸到 Store root pool；继续保持
`SkipDefaultTransaction=true` 并显式使用调用者 context。

### 3. 显式 15 列 decisionRow

新增未导出的 `decisionRow`，显式声明表 `decisions`，并用 `Select` 固定以下列及顺序：

```text
decision_id
node_id
source
rule_id
rule_version
alert_id
canonical_target
created_at_us
updated_at_us
last_triggered_at_us
expires_at_us
ended_at_us
state
end_reason
suppressed_count
```

表名、列名和全部写入字段不得依赖命名推断。row 不嵌入 `gorm.Model`，不包含 hook、
association、soft delete 或隐式时间字段。15 个映射字段都必须有紧邻的简体中文用途注释，
明确 primary key、foreign key、automatic/manual 枚举、生命周期枚举、SQL `NULL`、UTC Unix
微秒、计数或 partial-unique key 语义。

### 4. 值、生命周期与约束保持不变

- `source` 继续只写 `automatic` 或 `manual`；`state` 继续只写 `active`、`expired` 或
  `revoked`。
- automatic 必须同时带 `rule_id`、`rule_version`；manual 的 `rule_id`、`rule_version`、
  `alert_id` 必须写 SQL `NULL`。
- `expires_at_us`、`ended_at_us`、`end_reason` 继续按 nil 写 SQL `NULL`；非 nil 时间统一写
  `UTC().UnixMicro()`，不得写 Go `time.Time` 或依赖 serializer。
- `created_at_us`、`updated_at_us`、`last_triggered_at_us` 继续显式写 UTC Unix 微秒；
  `suppressed_count` 必须显式写入，不依赖数据库默认值。
- active/expired/revoked 与 `ended_at_us`、`end_reason` 的组合，以及 automatic/manual 对
  `manual_replace`、`rule_disabled` 的限制继续由现有 CHECK 约束强制执行。
- `decision_id` primary key 与两条 active partial-unique 保持正交：automatic key 为
  `(node_id, rule_id, canonical_target)`，manual key 为 `(node_id, canonical_target)`。
- `node_id`、`(rule_id, rule_version)`、`alert_id` immediate foreign key 必须在本次 Create
  当场失败；GORM 不得自动创建或更新 node、rule version 或 alert。

任何约束失败均进入 sticky state，后续 writer 不得执行，Commit 必须拒绝并回滚整个
UnitOfWork。

### 5. 保持 raw SQL 的边界

`InsertAutomaticDecision`、`InsertManualDecision`、Decision duplicate-suppression、expiry、
replace、revoke 及其他 Decision lifecycle SQL 均保持 raw。`PutDetectionContribution`、
`PutProjection`、`PutReceipt`、migration、PRAGMA、identity/read-back、CAS、generation/revision
fence、commit-unknown、snapshot 与其余 production SQL 也不在本批。

checksummed migration 继续是唯一 Schema authority。本批不修改表、列、索引、migration、
公共 API、依赖、配置、GORM adapter、Store pool 或事务最终化所有权。

## Consequences

### 正向结果

- Decision 初始写入复用现有受限 transaction session，不增加事务所有者。
- 15 列、automatic/manual 引用、生命周期值、SQL NULL 和 UTC 时间转换均显式可审查。
- PK、三个 immediate FK、生命周期 CHECK 与两条 active partial-unique 继续由现有 SQLite
  Schema 强制执行。
- 本批可独立回滚，不授权其他 Decision SQL 迁移。

### 代价与残余风险

- UnitOfWork 继续混用 GORM 与 raw SQL；exact SQL、同事务和 Decision lifecycle 回归仍需留证。
- GORM callback 或配置漂移可能产生额外 SQL，必须由专项测试和全局 adapter contract 阻断。
- 真实 Linux runtime、CI、漏洞/SBOM、性能和 durability 仍不由本批证明。

## Alternatives Considered

### 1. 同时迁移 Decision 生命周期的其余 SQL

拒绝。automatic duplicate suppression、manual replace、expiry/revoke 等路径包含 read-back、
条件更新或多语句生命周期事务，不属于本批单 INSERT 边界。

### 2. 同时迁移 Contribution、Projection 或 Receipt

拒绝。它们分别承载 deferred foreign key、revision fence/read-back 与 crash/replay union，需要
独立授权与契约测试。

### 3. 使用 association、hook、数据库默认值或新事务

拒绝。它们可能产生未授权 SQL、掩盖引用顺序错误、漂移 `suppressed_count`/NULL 语义，或破坏
业务状态与 Decision 写入的事务原子性和唯一终结所有权。

## Verification

GORM-1e 至少验证：

- exact SQL/Vars、表名、model、15 列顺序和单次 Create callback；
- raw SQL 全列回读、automatic/manual、三种 lifecycle state、nullable 字段、UTC 微秒与
  `suppressed_count`；
- automatic/manual 引用规则和生命周期非法组合在 Create 前或 SQLite CHECK 当场失败；
- primary key、automatic partial-unique、manual partial-unique 正交失败；
- node、rule-version、alert immediate FK 分别失败，且无 association SQL；
- sticky first-error、后续 writer 抑制、Commit 拒绝与整体 rollback；
- 已取消 context、同一 raw transaction 内可见性、commit/rollback 和无 root pool 逃逸；
- 既有 automatic duplicate suppression、manual replace、expiry、receipt、checkpoint 与
  SIGKILL/replay 回归；
- 字段级简体中文用途注释、migration/schema/PRAGMA/依赖/driver registry 不变；
- targeted/full race、vet、module verify、`git diff --check` 与三目标 CGo-free 构建。

未完成真实 native Linux race、非 WSL Linux、CI、漏洞/SBOM、性能/soak、OS reboot、
power-loss 和 commit-bound Evidence 前，不得将 GORM-1e 描述为 `Verified` 或提升 M0 Gate。

## Rollback

本批无 Schema 或数据格式迁移。回滚代码时恢复 `PutDecision` 的原 raw INSERT，删除仅为该
路径新增的 private `decisionRow` 和专项测试；保留其他批次仍在使用的 raw-tx-bound private
session。现有数据库文件、migration ledger 与其他 Decision/UnitOfWork SQL 无需转换。
