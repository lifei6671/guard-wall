# ADR-0005：GORM PutDetectionOutcome UnitOfWork 窄化例外

## 状态

```text
Decision: Accepted
Validation maturity: DONE / Implemented
Date: 2026-09-01
```

用户已明确确认 GORM-1b。本 ADR 只授权将 `UnitOfWork.PutDetectionOutcome` 的单条
七列 `INSERT` 从手写 SQL 迁移为 GORM `Create`。实现、验证、独立 Tier-3 checkpoint
与 final FULL_SCOPE 审查已经完成；Evidence repair round 1 的 fresh delta review 解决唯一
P1 后，最终为 `APPROVED / COMPLETE / FRESH / PASSED`、P0-P3 全无。用户 Code Review
已明确通过，本批当前为 `DONE / Implemented`；不提升 M0 Gate，也不扩大 ADR-0004
已冻结的 UnitOfWork 边界。

## Context

ADR-0004 为 `PutParserOutcome` 建立了绑定现有 raw `*sql.Tx` 的私有 GORM session，并保留
UnitOfWork 对事务终结的唯一所有权。`PutDetectionOutcome` 与该路径形态接近：它在领域校验
和枚举转换之后执行一次显式七列 `INSERT`，没有 `ON CONFLICT`、`RETURNING`、CAS、fence、
read-back、生成 ID 或隐式时间字段，适合作为第二条独立授权的生产 GORM 迁移。

相邻的 `PutDetectionContribution` 不具备同样边界。它使用
`ON CONFLICT(event_id, rule_id, rule_version) DO NOTHING`，读取 `RowsAffected`，并在重复时
回读、比较 stable delivery identity。该幂等与 read-back 语义继续由 raw SQL 承担，不因
本批迁移而改变。

## Decision

### 1. 唯一迁移对象

GORM-1b 只迁移：

```text
UnitOfWork.PutDetectionOutcome
```

该方法继续先执行现有 `ready(ctx)`、`DetectionTerminalOutcome.Validate()` 和
`detectionOutcomeKindValue`。只有最终持久化语句改用既有 private session 的
`WithContext(ctx).Create(...)`。现有错误上下文和 UnitOfWork 首错粘滞行为必须保持；
GORM 错误不得被吞掉、翻译、重试或降级为 raw SQL fallback。

### 2. 复用既有 raw transaction session

本批复用 ADR-0004 已建立的 private GORM session，不新增 transaction wrapper、opener、
DSN 或数据库连接。`Store.BeginProcessing` 创建的 raw `*sql.Tx` 继续是当前 UnitOfWork 的
唯一事务，UnitOfWork lifecycle 继续独占：

- `Commit` 与 `Rollback`；
- 成功、失败和 context 取消后的事务终结；
- 首错粘滞与失败后的提交拒绝。

GORM session 不得 commit、rollback、close、unwrap 或重新 begin，不得切回 Store root
pool，也不得持有可逃逸当前事务的连接。它继续使用 ADR-0003 的项目自有 modernc
Dialector 和固定配置，保持 `SkipDefaultTransaction=true`；每次写入必须显式
`WithContext(ctx)`。

### 3. 显式七列持久化模型

新增未导出的持久化 model，显式声明表 `detection_terminal_outcomes` 与以下七列：

```text
delivery_id
event_id
rule_id
rule_version
kind
failure_code
completed_at_us
```

表名、列名和全部七个写入字段不得依赖 GORM 命名推断。model 必须显式保持数据库契约：

- composite primary key 为 `(event_id, rule_id, rule_version)`，不得把 `delivery_id` 误作主键；
- `(rule_id, rule_version)` 继续引用冻结的 rule revision；
- `delivery_id` 继续使用指向 `processing_receipts(delivery_id)` 的
  `DEFERRABLE INITIALLY DEFERRED` foreign key；
- `success` 必须写入 `failure_code = NULL`；
- `record_permanent` 必须写入非空且不超过既有边界的 `failure_code`；
- `completed_at_us` 继续使用经 UTC 规范化后的 Unix microseconds。

model 不嵌入 `gorm.Model`，不包含隐式主键、soft-delete 或 create/update timestamp。禁止
association、hook 或自动保存关联对象；延迟 foreign key 只在既有 raw transaction 最终
commit 时由 SQLite 检查。

### 4. 继续保持 raw SQL 的边界

以下 SQL 不在 GORM-1b 授权范围内：

- `PutDetectionContribution` 的 `ON CONFLICT ... DO NOTHING`、`RowsAffected`、重复回读和
  stable delivery identity 比较；
- `PutParserOutcome` 之外的其他既有/后续 GORM 批次状态；
- 其他 UnitOfWork 写入、receipt、checkpoint 与 crash/replay 路径；
- migration、PRAGMA、identity/read-back、CAS、generation/revision fence、幂等和
  partial-unique 决策；
- observed、desired、reconcile 与 transaction-consistent snapshot。

checksummed raw migration 继续是唯一 Schema authority。本批不新增或修改表、列、索引、
migration、公共 API、依赖、配置或 PRAGMA；禁止 `AutoMigrate`、官方 `gorm.io/driver/*`、
第二个 SQLite driver、GORM 默认事务、显式 GORM `Transaction` 和嵌套事务。

## Consequences

### 正向结果

- detection terminal outcome 复用已经收窄验证的 transaction session，不增加事务所有者。
- 写入字段、复合主键和 NULL/非 NULL 语义显式可审查。
- parser 与 detection terminal outcome 可在同一 raw transaction 中使用 GORM，同时其余
  幂等、read-back 和 crash/replay SQL 保持 raw。
- 本批可独立回滚，不暗示其他生产 SQL 已获迁移许可。

### 代价与残余风险

- UnitOfWork 继续混用 GORM 与 raw SQL，exact SQL 与同事务测试仍是发布阻断项。
- composite primary key 标签错误可能令 GORM 错判 identity 或未来 API 行为；本批只允许
  显式 `Create`，不得借此启用 update/save。
- 延迟 `delivery_id` foreign key 只能在最终 commit 暴露错误，必须证明错误仍通过原
  UnitOfWork lifecycle 传播。
- 本批不证明 `PutDetectionContribution` 或其他关键 SQL 可以安全迁移。

## Alternatives Considered

### 1. 同时迁移 PutDetectionContribution

拒绝。其冲突忽略、affected rows 与 stable delivery read-back 是独立的幂等契约，需要
单独 ADR、测试和用户授权。

### 2. 为 detection outcome 新建 GORM transaction

拒绝。第二事务会破坏与 contribution、receipt、checkpoint 及其他处理写入的原子性，也会
绕过 UnitOfWork 的唯一 commit/rollback 所有权。

### 3. 使用 Store root GORM handle

拒绝。写入可能逃逸到 pool 上的另一连接，无法依赖当前 raw transaction 的延迟 foreign
key 与整体回滚语义。

### 4. 一次迁移全部 outcome 或 UnitOfWork SQL

拒绝。不同语句包含幂等、read-back、fence 和 crash/replay 约束，会扩大审查面与回滚面。

## Verification

GORM-1b 至少验证：

- exact SQL 只写入 `detection_terminal_outcomes` 的显式七列，placeholder 顺序稳定，不生成
  隐式字段、时间戳、association SQL 或第二条事务控制语句；
- raw SQL read-back 证明 `success -> failure_code IS NULL`、
  `record_permanent -> failure_code` 非空，以及 UTC microseconds 保持等价；
- composite primary key `(event_id, rule_id, rule_version)` 的重复错误进入现有首错粘滞状态，
  后续 `Commit` 被拒绝并回滚；
- GORM 写入与 contribution、receipt 及其他 raw 写入位于同一 `*sql.Tx`：commit 后整体可见，
  rollback 后整体不可见；
- 缺失 `processing_receipts` 时，`delivery_id` 的延迟 foreign key 仍在最终 commit 失败并按
  现有 lifecycle 回滚，不被 GORM 提前提交或吞掉；
- 已取消 context 通过 `WithContext(ctx)` 失败传播，不 fallback，也不逃逸到 root pool；
- `PutDetectionContribution` 的 SQL、`RowsAffected` 和 stable delivery identity read-back 测试
  保持不变并继续通过；
- migration ledger、Schema fingerprint、PRAGMA、依赖、compiled/runtime driver registry 前后不变；
- 既有 processing transaction、receipt、checkpoint、SIGKILL/restart/replay 测试继续通过；
- targeted/full race、vet、module verify、`git diff --check` 与 Windows/Linux CGo-free 构建矩阵
  按项目现有验证规则通过。

真实 Linux runtime、CI、漏洞扫描、生产性能与 commit-bound Evidence 未完成前，不得把
GORM-1b 描述为 `Verified`，也不得提升 M0 Gate。

## Rollback

本批无 Schema 或数据格式迁移。回滚代码时恢复 `PutDetectionOutcome` 的原 raw `INSERT`，
删除仅为该路径新增的 private persistence model 和对应测试；保留 ADR-0004 已建立且仍由
`PutParserOutcome` 使用的 raw transaction private session。现有数据库文件、migration
ledger、`PutDetectionContribution` 和其他 UnitOfWork SQL 无需转换。
