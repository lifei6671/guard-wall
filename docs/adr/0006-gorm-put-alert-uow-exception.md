# ADR-0006：GORM PutAlert UnitOfWork 窄化例外

## 状态

```text
Decision: Accepted
Validation maturity: DONE / Implemented
Date: 2026-09-01
```

用户已明确确认 GORM-1c。本 ADR 只授权将 `UnitOfWork.PutAlert` 的单条八列
`INSERT` 从手写 SQL 迁移为 GORM `Create`，并复用 ADR-0004 已建立的 raw-tx-bound
私有 GORM 会话。用户 Code Review 已明确通过；该结论仍不得描述为 `Verified` 或
Gate `PASS`，也不提升 M0 Gate。

## Context

ADR-0004 建立了绑定现有 raw `*sql.Tx` 的 private GORM session，ADR-0005 继续复用该
session 迁移 detection terminal outcome。`PutAlert` 也是一次显式、无 read-back 的
append-only `INSERT`：领域对象完成校验后写入八列，没有 `ON CONFLICT`、`RETURNING`、
CAS、fence、生成 ID 或隐式时间字段，适合作为第三个独立授权的小批次。

Alert 同时受多项数据库约束保护：`alert_id` primary key、
`(event_id, rule_id, rule_version)` unique constraint，以及 node identity、frozen rule
revision 和 detection contribution 的 immediate foreign keys。迁移不得改变这些约束的
检查时机，也不得让 GORM 自动保存关联对象来掩盖写入顺序错误。

## Decision

### 1. 唯一迁移对象

GORM-1c 只迁移：

```text
UnitOfWork.PutAlert
```

该方法继续先执行现有 `ready(ctx)` 和 `Alert.Validate()`；只有最终持久化语句改用既有
private session 的 `WithContext(ctx).Create(...)`。现有 `put alert <id>` 错误上下文和
UnitOfWork 首错粘滞行为必须保持。GORM 错误不得被吞掉、翻译、重试或降级为 raw SQL
fallback；失败后的 `Commit` 必须继续被拒绝并由原 lifecycle 回滚。

### 2. 复用现有 raw-tx-bound private session

本批不新增 transaction wrapper、opener、DSN 或数据库连接。`Store.BeginProcessing`
创建的 raw `*sql.Tx` 继续是 UnitOfWork 的唯一事务，UnitOfWork lifecycle 独占：

- `Commit` 与 `Rollback`；
- 成功、失败和 context 取消后的事务终结；
- 首错粘滞与失败后的提交拒绝。

GORM session 不得 commit、rollback、close、unwrap 或重新 begin，不得切回 Store root
pool，也不得持有可逃逸当前事务的连接。它继续使用 ADR-0003 的项目自有 modernc
Dialector 和冻结配置，保持 `SkipDefaultTransaction=true`；本次 `Create` 必须显式
`WithContext(ctx)`。

### 3. 显式八列 alertRow

新增未导出的 `alertRow`，显式声明表 `alerts` 与以下八列，并通过显式 `Select` 固定写入
集合和顺序：

```text
alert_id
node_id
event_id
rule_id
rule_version
canonical_target
observed_at_us
created_at_us
```

表名、列名和全部八个写入字段不得依赖 GORM 命名推断。`alertRow` 不嵌入
`gorm.Model`，不包含隐式字段、soft delete 或 create/update timestamp。每一个 GORM
映射字段都必须在字段声明的紧邻上方写一条简体中文字段级用途注释，至少清楚表达：

- `AlertID`：Alert 持久身份与 SQLite primary key；
- `NodeID`：Alert 所属节点及 `node_identity` immediate foreign key；
- `EventID`：触发 Alert 的事件及 detection membership identity；
- `RuleID`：触发 Alert 的规则身份；
- `RuleVersion`：触发 Alert 的冻结规则版本；
- `CanonicalTarget`：已经 canonicalize 的网络前缀文本；
- `ObservedAtUS`：事件观测时间的 UTC Unix microseconds；
- `CreatedAtUS`：Alert 创建时间的 UTC Unix microseconds。

禁止用结构体级总注释替代字段级用途注释。实现必须保持以下转换语义：

- `canonical_target` 继续写入 `alert.CanonicalTarget.String()`，不得写入未 canonicalize 的
  用户文本或其他序列化形式；
- `observed_at_us` 和 `created_at_us` 继续分别使用
  `alert.ObservedAt.UTC().UnixMicro()` 与 `alert.CreatedAt.UTC().UnixMicro()`；
- `Alert.Validate()` 继续保证 target 有效且 masked，并保证 `created_at` 不早于
  `observed_at`。

### 4. 约束与写入顺序保持不变

GORM model 必须显式保持数据库既有契约：

- primary key 为 `alert_id`；
- unique constraint 为 `(event_id, rule_id, rule_version)`，一份 detection membership
  最多产生一个 Alert；
- `node_id -> node_identity(node_id)` 为 immediate foreign key；
- `(rule_id, rule_version) -> rule_versions(rule_id, version)` 为 immediate foreign key；
- `(event_id, rule_id, rule_version) -> detection_contributions(...)` 为 immediate foreign key。

本批禁止 association、hook 或自动保存关联对象。引用行缺失时，SQLite 必须在 Alert
`Create` 语句处失败，错误进入 sticky state；GORM 不得补建 node、rule revision 或
detection contribution。

### 5. 继续保持 raw SQL 的边界

以下 SQL 不在 GORM-1c 授权范围内：

- `PutDetectionContribution` 的 `ON CONFLICT ... DO NOTHING`、`RowsAffected`、重复回读和
  stable delivery identity 比较；
- `PutDecision` 的自动/人工来源、nullable references、lifecycle 与 projection 语义；
- receipt、checkpoint、audit、desired projection 及其他 UnitOfWork 写入；
- migration、PRAGMA、identity/read-back、CAS、generation/revision fence、幂等和
  partial-unique 决策；
- observed、desired、reconcile 与 transaction-consistent snapshot。

checksummed raw migration 继续是唯一 Schema authority。本批不新增或修改表、列、索引、
migration、公共 API、依赖、配置或 PRAGMA；禁止 `AutoMigrate`、官方 `gorm.io/driver/*`、
第二个 SQLite driver、GORM 默认事务、显式 GORM `Transaction` 和嵌套事务。

## Consequences

### 正向结果

- Alert 写入复用已经收窄的 transaction session，不增加事务所有者或连接池入口。
- 八个字段、canonical target 与微秒时间转换显式可审查。
- primary key、detection-level unique constraint 与 immediate foreign keys 继续由既有
  SQLite Schema 强制执行。
- 本批可独立回滚，不暗示 decision、contribution 或其他 SQL 已获迁移许可。

### 代价与残余风险

- UnitOfWork 继续混用 GORM 与 raw SQL，exact SQL 与同事务验证仍是发布阻断项。
- association 或 hook 配置漂移可能产生额外 SQL；实现与边界测试必须双重禁止。
- `canonical_target` 或时间转换漂移会改变 durable representation，必须用 raw SQL
  read-back 验证精确值。
- immediate foreign key 与 unique constraint 错误必须证明仍进入 sticky failure 并整体回滚。

## Alternatives Considered

### 1. 同时迁移 PutDecision

拒绝。Decision 包含来源相关 nullable references、生命周期状态与后续 projection 约束，
需要独立 ADR、测试和用户授权。

### 2. 让 GORM 自动保存 association

拒绝。它会掩盖 processing pipeline 的写入顺序错误，产生未授权 SQL，并改变 immediate
foreign key 的失败边界。

### 3. 为 Alert 新建 GORM transaction 或使用 Store root handle

拒绝。第二事务或 pool 上的另一连接会破坏 Alert 与 contribution、receipt、checkpoint
及其他处理写入的原子性，并绕过 UnitOfWork 的唯一 commit/rollback 所有权。

### 4. 一次迁移全部 UnitOfWork SQL

拒绝。其他语句包含幂等、read-back、nullable state、fence 和 crash/replay 约束，会扩大
审查面与回滚面。

## Verification

GORM-1c 至少验证：

- exact SQL 只写入 `alerts` 的显式八列，placeholder 顺序稳定，不生成隐式字段、时间戳、
  association SQL 或第二条事务控制语句；
- raw SQL read-back 逐列证明 alert/node/event/rule identities、canonical target 字符串与
  两个 UTC microseconds 保持等价；
- duplicate `alert_id` primary key 与 duplicate `(event_id, rule_id, rule_version)` unique
  constraint 均进入 sticky error，后续 `Commit` 被拒绝并整体回滚；
- 分别缺失 node identity、rule revision 或 detection contribution 时，immediate foreign key
  在 `Create` 处失败，不创建关联行，也不延迟到 GORM 自有事务；
- Alert 与 contribution、outcome、receipt 及其他 raw 写入位于同一个 `*sql.Tx`：commit 后
  整体可见，rollback 后整体不可见；
- 已取消 context 通过 `WithContext(ctx)` 失败传播，不 fallback，也不逃逸到 root pool；
- 每个 `alertRow` GORM 映射字段都有简体中文字段级用途注释，且 table/column tag、primary
  key 与 `Select` 列表均显式；
- `PutDetectionContribution`、`PutDecision` 及其他被排除 SQL 的实现与行为测试保持不变；
- migration ledger、Schema fingerprint、PRAGMA、依赖、compiled/runtime driver registry 前后不变；
- 既有 processing transaction、receipt、checkpoint、SIGKILL/restart/replay 测试继续通过；
- targeted/full race、vet、module verify、`git diff --check` 与 Windows/Linux CGo-free 构建矩阵
  按项目现有验证规则通过。

真实 Linux runtime、CI、漏洞扫描、生产性能和 commit-bound Evidence 未完成前，不得把
GORM-1c 描述为 `Verified`，也不得提升 M0 Gate。

## Rollback

本批无 Schema 或数据格式迁移。回滚代码时恢复 `PutAlert` 的原 raw `INSERT`，删除仅为
该路径新增的 private `alertRow` 和对应测试；保留 ADR-0004 已建立且仍由既有 GORM 批次
使用的 raw-tx-bound private session。现有数据库文件、migration ledger、
`PutDetectionContribution`、`PutDecision` 与其他 UnitOfWork SQL 无需转换。
