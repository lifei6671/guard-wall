# ADR-0004：GORM PutParserOutcome UnitOfWork 窄化例外

## 状态

```text
Decision: Accepted
Validation maturity: DONE / Implemented
Date: 2026-09-01
```

用户已明确确认 GORM-1a。本 ADR 只授权将 `UnitOfWork.PutParserOutcome` 的单条
`INSERT` 从手写 SQL 迁移为 GORM `Create`，并为它建立绑定现有 raw `*sql.Tx` 的私有
适配入口。实现、验证与独立 Tier-3 checkpoint/final review 已完成，用户 Code Review
已明确通过；本批当前为 `DONE / Implemented`。本决定不提升 M0 Gate。

## Context

ADR-0003 将现有 UnitOfWork 与 crash/replay 边界保留为 raw SQL。后续只读预检确认，
Store 级别看似简单的 receipt、checkpoint 和 recoverable-generation 查询实际参与
commit-unknown、启动恢复或重放语义，不适合作为第一条生产 GORM 迁移。

`PutParserOutcome` 是当前最窄的候选：它在业务校验和枚举转换之后执行一次七列
`INSERT`，没有 `ON CONFLICT`、`RETURNING`、CAS、fence、生成 ID、read-back 或隐式时间
字段。但是，它仍属于既有 UnitOfWork 原子事务；如果 GORM 开启第二事务、提前提交或
逃逸到 Store pool，parser outcome 就会脱离同批 detection、alert、decision、checkpoint
和 receipt 写入，破坏原子性与 crash/replay 契约。因此，本批需要显式窄化 ADR-0003
的 UnitOfWork raw SQL 冻结边界，而不是把整个 UnitOfWork 交给 GORM 管理。

## Decision

### 1. 唯一迁移对象

GORM-1a 只迁移：

```text
UnitOfWork.PutParserOutcome
```

该方法继续先执行现有 `ready(ctx)`、领域对象 `Validate()` 与
`parserOutcomeKindValue` 转换；只有最终持久化语句改用
`WithContext(ctx).Create(...)`。现有错误上下文和 UnitOfWork 首错粘滞行为必须保持，
GORM 错误不得被吞掉、重试、翻译或降级为 raw SQL fallback。

其余生产 SQL 均不在本批授权范围内，包括：

- 其他 UnitOfWork 写入；
- migration、PRAGMA 与 schema introspection；
- identity/read-back、receipt 与 commit-unknown 路径；
- checkpoint、recoverable generation 与 crash/replay 路径；
- CAS、generation/revision fence、幂等和 partial-unique 决策；
- observed、desired、reconcile 与 transaction-consistent snapshot。

### 2. 原事务唯一所有权

`Store.BeginProcessing` 创建的 raw `*sql.Tx` 仍是该 UnitOfWork 的唯一事务。现有
UnitOfWork lifecycle 仍独占：

- `Commit`；
- `Rollback`；
- 成功、失败和取消后的事务终结；
- 首错粘滞与提交拒绝。

GORM 只能收到一个绑定该 raw `*sql.Tx` 的未导出、命名字段 wrapper。wrapper 可以转发
本次 `Create` 所需的 Context-aware prepare/exec/query 能力，但不得：

- 匿名嵌入、导出或提供任何方法解包 raw `*sql.Tx`；
- 实现 `Commit`、`Rollback`、`Close`、`gorm.GetDBConnector` 或其他 finalise/unwrap 能力；
- 实现会令 GORM 开启另一事务的 transaction-begin 能力；
- 切回 Store root pool、重新打开数据库、解析 DSN 或执行初始化查询；
- 改写 PRAGMA、连接池参数、migration ledger 或 Store 关闭顺序。

绑定过程必须零数据库 I/O。GORM handle 使用 ADR-0003 的项目自有 modernc Dialector 与
固定配置，尤其保持 `SkipDefaultTransaction=true`。每次调用仍必须显式
`WithContext(ctx)`，不得保存 `BeginProcessing` 的启动 context 作为后续业务授权。

### 3. 显式七列持久化模型

新增未导出的持久化 model，显式声明表 `parser_terminal_outcomes` 与以下七列：

```text
delivery_id
parser_id
parser_version
kind
emitted_count
failure_code
completed_at_us
```

model 不嵌入 `gorm.Model`，不包含隐式主键、soft-delete 或 create/update timestamp。
`failure_code` 必须继续将空缺值写为 SQL `NULL`；`emitted_count=0` 必须显式写入；
`completed_at_us` 继续使用经 UTC 规范化后的 Unix microseconds。表名、列名和全部七个
写入字段不得依赖 GORM 命名推断。

本批禁止使用：

- `AutoMigrate` 或任何 GORM migrator；
- 官方 `gorm.io/driver/*` 或第二个 SQLite driver；
- hook、association、preload、soft delete 或隐式时间戳；
- `Save`、`FirstOrCreate`、upsert 或全局 update/delete；
- GORM 默认事务、显式 GORM `Transaction` 或嵌套事务。

### 4. Schema、API 与配置不变

checksummed raw migration 继续是唯一 Schema authority。本批不新增或修改表、列、索引、
migration、PRAGMA、依赖、环境变量或公共 Store API。GORM handle、transaction wrapper 与
持久化 model 均保持包内私有。

## Consequences

### 正向结果

- 第一条生产 GORM 迁移被限制在一条结构简单、字段完全显式的 `INSERT`。
- parser outcome 继续与同一 UnitOfWork 的其他 raw SQL 共享一个 raw `*sql.Tx`。
- GORM 不取得事务终结、底层连接池或 Schema 所有权。
- 迁移形成可独立回滚的小批次，不暗示其他 CRUD 已获得迁移许可。

### 代价与残余风险

- 同一 UnitOfWork 暂时混用 GORM 与 raw SQL，需要维护一个极窄的 transaction wrapper。
- GORM callback 或 Dialector 升级可能改变生成 SQL；exact SQL 测试因此是发布阻断项。
- 唯一约束、延迟外键、context 取消和 NULL/零值的错误与参数语义必须证明等价。
- 本批不证明其他 UnitOfWork 方法、Store 查询或关键一致性 SQL 可安全迁移。

## Alternatives Considered

### 1. 让 GORM 创建或终结 UnitOfWork 事务

拒绝。它会形成第二事务所有者，并可能改变延迟外键、首错粘滞、提交拒绝和回滚语义。

### 2. 使用 Store root GORM handle 直接写入

拒绝。写入可能逃逸到 pool 上的另一连接，脱离 `BeginProcessing` 创建的 raw transaction。

### 3. 一次迁移全部 processing outcome 或全部 UnitOfWork SQL

拒绝。不同写入包含 checkpoint、receipt、幂等和 crash/replay 约束，会扩大审查面与回滚面。

### 4. 继续保留该语句为 raw SQL

安全且可回滚，但不能验证 GORM 在现有 UnitOfWork 原子边界内的最小生产用法。本 ADR 用
单条 INSERT 和强制契约测试控制首次迁移风险。

## Verification

GORM-1a 至少验证：

- exact SQL 只包含目标表与显式七列，placeholder 顺序稳定，不生成隐式字段、时间戳或
  第二条事务控制语句；
- success、no-match 与 permanent outcome 的 raw SQL read-back 与迁移前字段语义一致，
  包括 `failure_code IS NULL`、非空 failure code、`emitted_count=0` 和 microseconds；
- GORM 写入与其余 raw SQL 位于同一个 `*sql.Tx`：commit 后整体可见，rollback 后整体不可见，
  GORM 不调用 commit、rollback、close 或第二次 begin；
- duplicate/constraint 错误进入现有首错粘滞状态，后续 `Commit` 被拒绝并回滚；
- 已取消 context 通过 `WithContext(ctx)` 失败传播，不执行 fallback，也不逃逸到 root pool；
- migration ledger、Schema fingerprint、PRAGMA contract、compiled driver 与运行时 driver registry
  前后不变；
- 既有 processing transaction、receipt、checkpoint、SIGKILL/restart/replay 测试继续通过；
- targeted/full race、vet、module verify、`git diff --check` 与 Windows/Linux CGo-free 构建矩阵
  按项目现有验证规则通过。

真实 Linux runtime、CI、漏洞扫描、生产性能和 commit-bound Evidence 未完成前，不得把
GORM-1a 描述为 `Verified`，也不得提升 M0 Gate。

## Rollback

本批无 Schema 或数据格式迁移。回滚代码时恢复 `PutParserOutcome` 的原 raw `INSERT`，删除
仅为该路径新增的私有 transaction wrapper、GORM handle 和持久化 model，并重跑上述测试。
现有数据库文件、migration ledger 和其他 UnitOfWork SQL 无需转换。
