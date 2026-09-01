# ADR-0003：GORM Core 与 modernc SQLite 适配边界

## 状态

```text
Decision: Accepted
Validation maturity: REVIEW / Implemented
Date: 2026-09-01
```

用户已明确确认 GORM-0b。本 ADR 只接受 GORM core 和项目自有 modernc 适配层；
不接受业务 SQL 迁移、Schema 变化或第二个 SQLite driver。`REVIEW / Implemented`
表示依赖、适配器和边界测试已落盘，仍等待用户 Code Review；它不提升 M0 Gate，
也不代表 GORM-1 业务迁移已获批准。

## Context

Store 已有大量手写 SQL。盘点结果同时存在两类语句：

- 普通、低风险 CRUD，可在后续独立批次评估迁移到 GORM；
- migration、PRAGMA、CAS/fence、`RETURNING` read-back、commit-unknown、
  transaction-consistent snapshot 和现有 UnitOfWork 等关键语义，必须保持 raw SQL。

最初 GORM-0 兼容性探针使用官方 `gorm.io/driver/sqlite v1.6.0`，结论为
`NO-GO / STOPPED`：该包无条件 blank-import `github.com/mattn/go-sqlite3`，会同时
注册 `sqlite3` driver；其 `Initialize` 还会用 `context.Background()` 查询 SQLite
版本。GORM core 在 Dialector 初始化失败时会尝试取得并关闭底层 `*sql.DB`。这些行为
分别违反 ADR-0002 的唯一编译 driver、调用方 context 和 Store 唯一关闭所有权。

一手实现依据：

- [GORM v1.31.2 `gorm.Open`](https://github.com/go-gorm/gorm/blob/v1.31.2/gorm.go#L210-L219)
- [official SQLite Dialector v1.6.0](https://github.com/go-gorm/sqlite/blob/v1.6.0/sqlite.go)

## Decision

### 1. 依赖边界

唯一新增直接依赖为：

```text
gorm.io/gorm v1.31.2
```

必须满足：

1. production、test 和 tooling 源码禁止 import `gorm.io/driver/*`、
   `github.com/mattn/go-sqlite3` 或其他第二个 `database/sql` SQLite driver。
2. 编译依赖闭包、二进制 module metadata 和运行时 `sql.Drivers()` 必须只有
   ADR-0002 选定的 modernc SQLite driver；`sqlite3` 注册即为发布阻断。
3. GORM v1.31.2 自身的 upstream test module 会使 `gorm.io/driver/sqlite v1.6.0`
   和 `github.com/mattn/go-sqlite3 v1.14.22` 出现在 selected module graph 与
   `go.sum`。这属于供应链/SBOM 暴露，不得描述为“依赖图中不存在第二 driver”，
   也不得误报为已编译或已注册。
4. GORM 或 modernc 升级必须重新检查 selected graph、compiled packages、二进制
   metadata、driver registry、许可证、漏洞、体积与 CGo-free 构建矩阵。

### 2. 唯一 opener 与连接池所有权

ADR-0002 的唯一 opener 顺序保持不变：modernc connector、`sql.OpenDB`、首个
`PingContext`、checksummed raw migration、migration 后 PRAGMA read-back。
GORM adapter 只在这些步骤成功后构造，不调用 `sql.Open`、不解析 DSN、不注册 driver、
不 Ping、不执行初始化查询。

GORM 只能收到命名字段包装的 `nonClosingConnPool`。该包装器转发 Context-aware
Prepare/Exec/Query/QueryRow 和 `BeginTx`，但不得：

- 匿名嵌入或导出 `*sql.DB`；
- 实现 `Close`、`gorm.GetDBConnector`、Ping 或第二个 opener；
- 改写 connection limits、PRAGMA 或 Store 的关闭顺序。

因此 `gorm.DB.DB()` 必须返回 `gorm.ErrInvalidDB`，`gorm.DB.Connection` 禁止使用；
`Store.Close()` 仍是唯一 pool close owner。显式 `Transaction` 委托给同一 `*sql.DB`；
Dialector 不实现 SavePoint/RollbackTo，嵌套事务必须返回 unsupported error，不得静默降级。

### 3. Project-owned modernc Dialector

项目维护最小 `gorm.Dialector`，只提供 GORM core 所需的 SQLite SQL 语法：

- `?` bind variable 和反引号 identifier quoting；
- SQLite offset-only `LIMIT -1 OFFSET n`；
- 抑制 SQLite 不支持的 `FOR UPDATE`；
- modernc v1.57.0 内置 SQLite 已支持的 Create/Update/Delete `RETURNING` callback；
- SQLite 基础 data type/default expression 映射，仅用于满足 Dialector contract。

`Initialize` 只能注册 callback 与 clause builder，必须零数据库 I/O、零 goroutine、
零 schema 副作用。升级 GORM/modernc 或扩大 GORM API 使用面时，必须重跑 exact SQL、
placeholder、quoting、CRUD、transaction 和 RETURNING 兼容测试。

### 4. Migration 与 Schema 所有权

checksummed SQL migration 仍是唯一 Schema authority。生产代码和测试 fixture 均不得用
GORM `AutoMigrate` 创建或修改 Guard Schema。Dialector 返回完整的 fail-fast
`disabledMigrator`：有 error 通道的方法返回稳定 sentinel；GORM 接口中没有 error 通道
的 schema introspection 方法直接 panic，以阻止内部程序员误用。

GORM-0b 不新增/修改表、列、索引、migration SQL 或 migration ledger。测试专用表只能由
raw SQL 在临时数据库中创建，并用 raw SQL 交叉验证结果。

### 5. SQL 与 API 使用范围

GORM handle 只保存在 `Store` 的未导出字段中，不新增公共 Store API。GORM-0b 不迁移任何
生产业务 SQL；后续 GORM-1 必须作为独立小批次逐项授权、测试和 Review。

未来允许评估的范围仅是显式 table/column、无 association/hook/soft-delete/implicit
timestamp 的普通 CRUD。以下路径保持 raw SQL，除非新的 ADR 和契约测试证明等价：

- checksummed migration 与所有 PRAGMA；
- CAS、generation/revision fence、幂等/partial-unique 决策；
- `RETURNING`/read-back、commit-unknown 和 stale completion；
- transaction-consistent snapshot、现有 UnitOfWork 与 crash/replay 边界。

禁止默认使用 `Save`、`FirstOrCreate`、association、hook、soft delete、隐式表名/列名/
时间字段或全局 update/delete。

所有 GORM 持久化映射结构体必须保持未导出，并为每个映射字段提供字段级简体中文注释，
说明对应列的业务用途。涉及主键/外键、SQL NULL、枚举值、时间单位或只读回填的字段，
注释还必须明确这些约束；不得只重复 Go 字段名或数据库列名。

### 6. Context、日志与运行配置

GORM adapter 固定：

```text
SkipDefaultTransaction = true
DisableAutomaticPing = true
PrepareStmt = false
DisableForeignKeyConstraintWhenMigrating = true
IgnoreRelationshipsWhenMigrating = true
AllowGlobalUpdate = false
TranslateError = false
Logger = logger.Discard
```

构造入口必须先检查调用方 context，且不保存启动 context。每一次未来业务操作必须显式
调用 `WithContext(ctx)`。Dialector `Explain` 保留 placeholder，不把路径、credential 或
业务值插入 SQL 文本。

## Alternatives Considered

### 1. 官方 `gorm.io/driver/sqlite`

拒绝。它会编译并注册 mattn CGO driver，且初始化/关闭行为突破当前冻结边界。

### 2. 更换为 mattn driver 或提供双 driver selector

拒绝。违反 ADR-0002 的 modernc-only、CGo-free 发布边界，并扩大 DSN、错误码、构建和
durability 矩阵。

### 3. Fork/replace 官方 Dialector

拒绝。当前只需小型稳定适配面；fork/replace 会扩大供应链和升级维护成本。

### 4. 保持永久 raw SQL

可安全回滚，但不满足用户希望逐步用 GORM 替代普通硬编码 SQL 的方向。本文用严格边界
保留关键 raw SQL，同时为后续低风险 CRUD 建立可验证入口。

## Consequences

### 正向结果

- GORM 与现有 Store 复用同一个 modernc pool、migration 和 PRAGMA contract。
- GORM 初始化失败无法关闭 Store-owned pool，所有 cleanup 仍由 `Open`/`Close` 负责。
- 后续普通 CRUD 可逐项迁移，不需要同时改 Schema 或关键一致性语义。
- AutoMigrate、第二 driver、默认 SQL/value 日志和全局写被实现与测试双重阻断。

### 代价与残余风险

- 项目需要自行维护 Dialector 的 quoting、clause 和 callback 兼容性。
- selected module graph/SBOM 包含未编译的官方 SQLite driver 与 mattn driver；仍需接受其
  供应链扫描结果并持续证明未进入产物。
- GORM 增加编译体积、启动和运行开销；当前未做生产负载基准。
- GORM root handle 的默认 context 不是业务授权；任何漏掉 `WithContext(ctx)` 的后续调用
  都是代码审查阻断项。
- 当前只在临时表上验证适配器语义，未证明任何现有生产 SQL 可安全迁移。

## Validation Plan

GORM-0b 至少验证：

- adapter 初始化零 I/O、已取消 context fail-fast；
- init failure 不关闭 raw pool，`gorm.DB.DB()` 不能解包 pool；
- compiled dependency 与 runtime driver registry 不含官方/mattn SQLite driver；
- 初始化和被拒绝的 AutoMigrate 前后 Schema fingerprint 不变；
- 参数化 CRUD、无 WHERE delete 拒绝、显式 transaction commit/rollback、嵌套 transaction 拒绝；
- Store Close 后 raw/GORM 同时失效，PRAGMA contract 不漂移；
- targeted/full race、vet、module verify、`git diff --check`；
- Windows runtime 与 Linux amd64/arm64 `CGO_ENABLED=0` test binary cross-build。

真实 Linux runtime、CI、漏洞扫描、SBOM、生产体积/性能、业务 SQL 等价迁移与 commit-bound
Evidence 未完成前，不得把 GORM-0b 标记为 Verified，也不得提升 M0 Gate。

## Rollback

GORM-0b 没有数据库或数据回滚。回滚代码时删除未导出的 adapter/Store 字段和
`gorm.io/gorm` import/require，执行 `go mod tidy` 并重跑 Store/全仓测试即可；
现有 Schema、migration ledger 与数据库文件不需要转换。
