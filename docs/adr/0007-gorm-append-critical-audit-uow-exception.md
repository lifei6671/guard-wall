# ADR-0007：GORM AppendCriticalAudit UnitOfWork 窄化例外

## 状态

```text
Decision: Accepted
Validation maturity: DONE / Implemented
Date: 2026-09-01
```

用户已明确确认 GORM-1d，并已通过本批 Code Review。本 ADR 只授权将 `UnitOfWork.AppendCriticalAudit` 的单条
15 列 `INSERT` 从手写 SQL 迁移为 GORM `Create`，并复用 ADR-0004 已建立的
raw-tx-bound 私有 GORM 会话。本批不得描述为 `Verified` 或 Gate `PASS`，也不提升 M0 Gate。

## Context

`AppendCriticalAudit` 在 SQL 前完成必填字段、JSON 与时间校验，随后追加一条无 read-back、
无 `ON CONFLICT`、无 `RETURNING`、无 CAS/fence 的审计记录。它适合作为独立小批次，但
`critical=1`、幂等唯一键、可空引用、JSON 文本和 immediate foreign key 均属于持久化契约，
不能交给 GORM 默认值或 association 推断。

## Decision

### 1. 唯一迁移对象

GORM-1d 只迁移：

```text
UnitOfWork.AppendCriticalAudit
```

现有 `ready(ctx)`、字段校验、空 details 到 `{}` 的归一化和 `json.Valid` 检查保持在 SQL 前。
只有最终写入改用既有 private session 的 `WithContext(ctx).Create(...)`。错误继续保留
`append critical audit <id>` 上下文，并进入 UnitOfWork sticky first-error；禁止 retry、
fallback 或错误吞并。

### 2. 复用同一 raw transaction

本批不新增 transaction wrapper、opener、DSN、连接或 pool。`Store.BeginProcessing` 创建的
raw `*sql.Tx` 仍是唯一事务，UnitOfWork 独占 Commit/Rollback。GORM session 不得 commit、
rollback、close、unwrap、重新 begin 或逃逸到 Store root pool；继续保持
`SkipDefaultTransaction=true` 并显式使用调用者 context。

### 3. 显式 15 列 criticalAuditRow

新增未导出的 `criticalAuditRow`，显式声明表 `audit_logs`，并用 `Select` 固定以下列及顺序：

```text
audit_id
idempotency_key
node_id
category
action
result
severity
critical
actor_type
delivery_id
alert_id
decision_id
error_code
details_json
created_at_us
```

表名、列名和全部写入字段不得依赖命名推断。row 不嵌入 `gorm.Model`，不包含 hook、
association、soft delete 或隐式时间字段。每个映射字段必须有紧邻的简体中文用途注释，
明确 primary key、unique、foreign key、SQL NULL、枚举或 UTC Unix 微秒语义。

### 4. 值与约束保持不变

- `critical` 必须显式写入 `1`；不得依赖数据库默认值或 Go 零值。
- `delivery_id`、`alert_id`、`decision_id` 为 nil 时写 SQL `NULL`；空 `error_code` 也写 NULL。
- 空 `DetailsJSON` 继续写 `{}`；非空合法 JSON 按原文本写入，不重新编码或 canonicalize。
- `created_at_us` 继续使用 `audit.CreatedAt.UTC().UnixMicro()`。
- `audit_id` primary key 与 `idempotency_key` unique constraint 继续由 SQLite 强制执行。
- `node_id`、`alert_id`、`decision_id` immediate foreign key 必须在本次 Create 当场失败。
- `delivery_id` 只有格式 CHECK，不新增不存在的 Receipt association 或 foreign key。

任何约束失败均进入 sticky state，后续 writer 不得执行，Commit 必须拒绝并回滚整个
UnitOfWork。GORM 不得自动创建或更新 node、alert、decision 或 receipt。

### 5. 保持 raw SQL 的边界

`PutDetectionContribution`、`PutDecision`、`PutProjection`、`PutReceipt`、migration、PRAGMA、
identity/read-back、CAS、generation/revision fence、commit-unknown、snapshot 与其余 production
SQL 均不在本批。checksummed migration 继续是唯一 Schema authority。本批不修改表、列、
索引、migration、公共 API、依赖、配置、Store pool 或事务最终化所有权。

## Consequences

### 正向结果

- Critical audit 写入复用现有受限 transaction session，不增加事务所有者。
- 15 列、四个 SQL NULL、`critical=1`、JSON 与时间转换均显式可审查。
- PK、idempotency UNIQUE 和三类 immediate FK 继续由现有 SQLite Schema 强制执行。
- 本批可独立回滚，不授权其他 SQL 迁移。

### 代价与残余风险

- UnitOfWork 继续混用 GORM 与 raw SQL；exact SQL、同事务和 crash/replay 回归仍需留证。
- GORM callback 或配置漂移可能产生额外 SQL，必须由专项测试和全局 adapter contract 阻断。
- 真实 Linux runtime、CI、漏洞/SBOM、性能和 durability 仍不由本批证明。

## Alternatives Considered

### 1. 同时迁移 Decision、Projection 或 Receipt

拒绝。它们分别承载 partial-unique/lifecycle、revision fence/read-back 与 crash/replay union，
需要独立授权与契约测试。

### 2. 使用 Store root GORM handle 或新事务

拒绝。它会破坏 audit 与其解释的业务状态之间的事务原子性和唯一终结所有权。

### 3. 使用 association、hook 或数据库默认值

拒绝。它会产生未授权 SQL，掩盖引用顺序错误，或让 `critical=1` 与 NULL 语义漂移。

## Verification

GORM-1d 至少验证：

- exact SQL/Vars、表名、model、15 列顺序和单次 Create callback；
- raw SQL 全列回读、四个 SQL NULL、非空引用/error、`critical=1`、JSON 原文与 UTC 微秒；
- nil/零长度 details 均归一化为 `{}`，非法 JSON 在 Create 前失败；
- primary key 与 idempotency unique 正交失败；node/alert/decision immediate FK 分别失败；
- sticky first-error、后续 writer 抑制、Commit 拒绝与整体 rollback；
- 已取消 context、同一 raw transaction 内可见性、commit/rollback 和无 root pool 逃逸；
- 既有 processing、decision lifecycle、receipt、checkpoint 与 SIGKILL/replay 回归；
- 字段级简体中文用途注释、migration/schema/PRAGMA/依赖/driver registry 不变；
- targeted/full race、vet、module verify、`git diff --check` 与三目标 CGo-free 构建。

未完成真实 native Linux race、非 WSL Linux、CI、漏洞/SBOM、性能/soak、OS reboot、
power-loss 和 commit-bound Evidence 前，不得将 GORM-1d 描述为 `Verified` 或提升 M0 Gate。

## Rollback

本批无 Schema 或数据格式迁移。回滚代码时恢复 `AppendCriticalAudit` 的原 raw INSERT，删除
仅为该路径新增的 private `criticalAuditRow` 和专项测试；保留其他批次仍在使用的
raw-tx-bound private session。现有数据库文件、migration ledger 与其他 UnitOfWork SQL
无需转换。
