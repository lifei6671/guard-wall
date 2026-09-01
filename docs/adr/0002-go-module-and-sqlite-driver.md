# ADR-0002：Go Module、Toolchain 与 SQLite Driver

## 状态

```text
Decision: Accepted
Validation maturity: Implemented / not Verified
Date: 2026-08-30
```

`Accepted` 只表示 Phase 1 已冻结本文的 module、Go 版本策略和 SQLite driver 选择。
`Implemented / not Verified` 表示 module、driver、统一 opener、migration 与第一批 Store
测试已经落盘；目标 Linux 的运行、SIGKILL/reboot/power-loss 与完整 Store Contract 尚未
验证，不能据此把 M0、B1、D2、D4 或 SQLite durability 标记为 `Verified`。

## Context

Phase 1 需要两个 Linux 进程、SQLite 本地存储、Windows 开发环境到 Linux 的构建路径，
以及可复现的 SIGKILL/reboot/power-loss 验证。主 Contract 已冻结 SQLite 语义为 WAL、
`synchronous=FULL`、外键、5 秒 busy timeout 和 1000 页 WAL auto-checkpoint，但尚需为
Go 实现选定唯一 driver，并消除以下歧义：

- module path 与产品名不能相互推导；
- Go 最低语言版本、实际构建 patch 版本和自动 toolchain 下载不能混为一个字段；
- `database/sql` 是连接池，只初始化首个连接不能满足“每个 connection 设置并回读
  connection-local PRAGMA”的 Contract；
- CGO driver 会把 C compiler、libc、交叉编译和动态链接差异带入发布矩阵；
- “能打开数据库”不等于已证明事务回滚、进程崩溃、重启或掉电 durability。

规范依据：

- [Phase 1 Contract](../contracts/guard-phase-1-m0-contract-freeze-v0.3.md) §5.1、§13、
  §22.2、§23。
- [M0 执行矩阵](../development/phase-1/M0-EXECUTION.md) B1、D2、D4。
- 初步证据：
  [SQLite Spike result](../../artifacts/evidence/M0/worktree/m0-b/sqlite-result.json)。

## Decision

### 1. Go Module

Phase 1 根 module 唯一使用：

```text
module github.com/lifei6671/guard-wall
```

必须满足：

1. 根 `go.mod` 是 module path 和 Go directive 的唯一权威源；README、二进制名、service
   名称和包发布名不得反向覆盖它。
2. Phase 1 不使用多 module workspace，也不为 `guard-agent`、`guard-enforcer` 分拆 module。
3. 本地开发可以使用 `go.work`，但不得提交一个会改变 CI/release 依赖解析结果的
   `go.work`。
4. 依赖必须由 `go.mod`/`go.sum` 精确记录；禁止依赖未固定 branch、未标 tag 的 HEAD 或
   本地 `replace` 形成发布构建。

### 2. Go 版本策略

首个根 `go.mod` 必须写：

```text
go 1.27.0
```

版本策略冻结为：

1. `go` directive 表示 Phase 1 的最低 Go 语言与 module 语义基线；Go 1.27.0 是
   2026-08-30 可用的最新稳定大版本，且当前开发主机已观察到 `go1.27.0 windows/amd64`。
2. CI 和 release 必须显式选择 Go 1.27 的具体 patch 版本，并把 `go version` 写入
   Evidence Manifest。当前基线是 `go1.27.0`；同一 1.27 系列的安全/修复 patch 应及时
   升级，不需要新 ADR，但必须重跑编译、测试、race 和发布矩阵。
3. 升级到 Go 1.28 或更高 minor，必须先完成依赖兼容性、Linux 支持矩阵和产物回归，
   并修订或取代本文；不得只因开发机自动升级就改写 `go` directive。
4. Phase 1 根 `go.mod` 不写 `toolchain` directive。CI/release 禁止依赖一次未记录的隐式
   toolchain 下载；构建环境必须预装并记录所选 patch 版本。
5. release 构建必须包含 `CGO_ENABLED=0` 的 Linux 目标验证。任何需要改为
   `CGO_ENABLED=1` 的变更都属于构建与部署边界变化，必须由新 ADR 取代本文。

Go 官方发布记录说明 Go 1.27.0 于 2026-08-19 发布；官方支持策略是一个大版本持续支持到
两个更新大版本发布为止：
[Go Release History](https://go.dev/doc/devel/release)。

### 3. SQLite Driver

Phase 1 唯一 SQLite driver 为：

```text
modernc.org/sqlite v1.57.0
database/sql driver name: sqlite
```

必须满足：

1. `go.mod` 必须精确要求 `modernc.org/sqlite v1.57.0`；禁止使用 `@latest`、伪版本或
   浮动 branch 形成可发布产物。
2. 禁止同时编译第二个 SQLite driver，也不提供运行时 driver selector、CGO fallback
   或 system `libsqlite3` fallback。
3. release 构建使用 driver 自带的 CGo-free SQLite port，不链接目标机的
   `libsqlite3.so`。
4. `modernc.org/sqlite` 的 `go.mod` 对 `modernc.org/libc` 存在生成代码 ABI 约束；不得
   单独 `replace`、强制升级或降级 `modernc.org/libc`。依赖更新若改变解析出的 libc
   版本，必须作为 driver 更新一并审查和验证。
5. driver、其传递依赖和内置 SQLite 版本必须进入 SBOM/依赖清单；driver 升级必须重跑
   migration、并发唯一约束、事务回滚、PRAGMA、crash/reopen 和 Linux release 测试。

选择理由：

- 官方包文档将 `modernc.org/sqlite` 定义为 CGo-free 的 `database/sql` driver，并明确
  列出 `linux/amd64`、`linux/arm64`、`windows/amd64` 等支持目标；它与 Phase 1
  `CGO_ENABLED=0` 的构建边界一致：
  [modernc.org/sqlite package](https://pkg.go.dev/modernc.org/sqlite@v1.57.0)。
- v1.57.0 是决策日可用的稳定 tag，其 `go.mod` 最低版本为 Go 1.25.0，低于本文的
  Go 1.27.0 基线，并固定 `modernc.org/libc v1.74.4`：
  [v1.57.0 go.mod](https://gitlab.com/cznic/sqlite/-/raw/v1.57.0/go.mod)。
- driver 提供连接 hook 和受校验的 PRAGMA DSN shorthand，可在每个物理连接打开时执行并
  拒绝错误配置；这些职责不需要 ORM 或另一套 migration framework。后续获批的 GORM core
  适配层不得接管这些职责，具体边界见
  [ADR-0003](0003-gorm-core-modernc-adapter.md)。

这项选择只降低构建和部署变量，不代表纯 Go port 的并发、性能或 durability 已在 Guard
负载上证明。

### 4. 唯一连接初始化路径

所有生产 Store、migration、Doctor 和测试数据库必须复用同一个 Store opener。禁止模块
自行调用 `sql.Open` 绕过该 opener。

唯一初始化顺序为：

```text
取得单实例锁
  → 注册唯一 connection hook（必须早于首个连接）
  → 用 sqlite.NewConnector 构造内部 DSN
  → sql.OpenDB
  → 首个 Ping/Open 触发 PRAGMA 设置与逐项 read-back
  → schema migration
  → migration 后再次 read-back
  → Store Ready
```

内部 DSN 必须由代码生成，不接受配置文件、CLI、HTTP 或数据库内容提供的原始 DSN。其
等价设置必须是：

```text
_journal_mode=WAL
_synchronous=FULL
_foreign_keys=ON
_busy_timeout=5000
_pragma=wal_autocheckpoint(1000)
```

`_journal_mode`、`_synchronous`、`_foreign_keys` 和 `_busy_timeout` 必须使用 driver 的
受校验 shorthand；只有当前无 shorthand 的 `wal_autocheckpoint` 可以使用 `_pragma`。
`_pragma` 值必须是代码常量，禁止拼接不可信输入。driver 文档说明 `_pragma` 会逐项执行且
不预校验，而 shorthand 会先校验再按固定顺序应用：
[Driver.Open DSN contract](https://pkg.go.dev/modernc.org/sqlite@v1.57.0#Driver.Open)。

在打开首个连接前，Store 必须注册一个 `modernc.org/sqlite.ConnectionHookFn`。每个物理
连接完成 DSN 初始化后，hook 必须在任何业务事务开始前逐项查询并验证：

| PRAGMA | 接受的 read-back |
|---|---|
| `journal_mode` | 字符串大小写归一化后严格等于 `wal` |
| `synchronous` | 整数严格等于 `2`（`FULL`） |
| `foreign_keys` | 整数严格等于 `1` |
| `busy_timeout` | 整数严格等于 `5000` |
| `wal_autocheckpoint` | 整数严格等于 `1000` |

任一 PRAGMA 设置、查询或 read-back 不匹配时，该物理连接必须返回错误；首个连接不匹配时
进程必须保持 Not Ready 并快速失败，禁止降级到 DELETE journal、NORMAL/OFF synchronous、
关闭外键或其他 timeout。hook 只做连接初始化和校验，不运行 migration 或业务 SQL。

`journal_mode=WAL` 虽会跨连接和 reopen 持久存在，仍必须显式设置并验证，不能依赖旧 DB
状态。SQLite 官方文档说明 WAL 是持久 journal mode：
[PRAGMA journal_mode](https://www.sqlite.org/pragma.html#pragma_journal_mode)。

SQLite 官方文档还明确：外键默认值不应被依赖，`busy_timeout` 绑定到每个 connection，
而 WAL + `synchronous=FULL` 会在每次事务 commit 后增加 WAL sync。因此本文要求按物理
连接设置和 read-back，而不是只对连接池执行一次：
[PRAGMA foreign_keys](https://www.sqlite.org/pragma.html#pragma_foreign_keys)、
[PRAGMA busy_timeout](https://www.sqlite.org/pragma.html#pragma_busy_timeout)、
[PRAGMA synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous)。

### 5. 事务与文件系统边界

1. PRAGMA 初始化和 read-back 必须发生在业务 transaction 之外；尤其不得在 transaction
   内尝试切换 `foreign_keys` 或 `journal_mode`。
2. 所有写 transaction 必须保持短事务；Firewall、SMTP、HTTP、IPC 或其他外部调用禁止
   位于 SQLite transaction 内。
3. Phase 1 只支持本机、受验证的本地 filesystem 上的 SQLite 文件。网络文件系统、同步
   盘目录、容器临时层和未知 VFS 不属于已支持 durability 域。
4. DB、`-wal`、`-shm` 及父目录的 owner/mode 必须与
   [ADR-0001](0001-phase1-process-privilege-boundary.md) 的非特权 Agent 边界一致。
5. 备份、复制和恢复必须使用 SQLite/WAL-aware 流程；禁止运行时仅复制主 DB 文件并宣称
   得到一致备份。

## Alternatives Considered

### 1. `github.com/mattn/go-sqlite3`

拒绝作为 Phase 1 driver。决策日核对的稳定版本为 v1.14.50；它成熟、使用广泛，并支持
多种编译选项，但官方文档明确要求 `CGO_ENABLED=1` 和 GCC，交叉编译时还可能需要目标 C
compiler：
[go-sqlite3 v1.14.50](https://pkg.go.dev/github.com/mattn/go-sqlite3@v1.14.50)、
[official README](https://github.com/mattn/go-sqlite3/blob/v1.14.50/README.md#installation)。

这会扩大 Windows→Linux、amd64→arm64、libc/static linking 和 CI toolchain 矩阵，与
本文冻结的 CGo-free 发布边界冲突。拒绝理由是 Phase 1 的构建/部署约束，不是对该项目
安全性或质量的否定。

### 2. 动态链接系统 `libsqlite3`

拒绝。目标机发行版会决定 SQLite patch、编译选项和安全更新时点，使同一 Guard 版本在
不同主机上具有不同 SQL 行为，也重新引入 CGO 和部署依赖。

### 3. 同时支持 pure Go 与 CGO driver

拒绝。双 driver 会形成两套 DSN、错误码、构建、migration 和 durability 验证矩阵；
Phase 1 没有需要运行时切换 driver 的需求。

### 4. 仅在 `sql.DB` 创建后执行一次 PRAGMA

拒绝。`sql.DB` 是连接池，后续新建的物理 connection 不会继承另一个 connection 的
`foreign_keys`、busy handler 等 connection-local 状态，无法满足主 Contract。

### 5. 依赖 `@latest` 或每次构建自动更新 driver

拒绝。内置 SQLite 和生成的 libc binding 会随 driver 变化，浮动解析不能产生可复现的
发布产物或可归属的 crash evidence。

## Consequences

### 正向结果

- `guard-agent` 和 `guard-enforcer` 可沿用 `CGO_ENABLED=0` 的统一 Go 发布路径。
- 发布产物不依赖目标机安装 GCC development package 或特定 `libsqlite3.so`。
- module、Go minor、driver 和 PRAGMA 初始化都只有一条实现路径。
- 每个物理连接在进入连接池前验证 durability 相关设置，配置漂移会快速失败。
- driver 版本、内置 SQLite 和传递 libc 版本可进入可复现证据与供应链清单。

### 代价与残余风险

- modernc 的 transpiled SQLite/libc 会增加依赖图、构建时间和二进制体积；必须实测，不得
  以 CGo-free 推导出性能更好。
- pure Go port 与 upstream C build 可能存在行为、性能或平台缺陷；driver tag 的 CI
  结果不能替代 Guard workload 测试。
- package-level connection hook 必须在首个连接之前注册；测试若绕过统一 opener，可能
  得到错误 PRAGMA 状态，因此绕过路径必须由测试/静态检查拦截。
- WAL 仍会生成 `-wal`/`-shm` 文件，并依赖 filesystem、VFS、fsync/barrier 和存储设备
  正确实现；driver 选择不能证明掉电 durability。
- 5 秒 busy timeout 不是 retry budget，也不能掩盖长事务或错误的并发模型。
- 依赖升级可能改变内置 SQLite、libc binding、错误码或 PRAGMA 行为，必须显式评审。

## Preliminary Validation

现有初步证据为
[SQLite Spike result](../../artifacts/evidence/M0/worktree/m0-b/sqlite-result.json)。它在
Windows 11、Python 3.12.10、SQLite 3.49.1 上观察到：

- `journal_mode=wal`、`synchronous=2`、`foreign_keys=1`、
  `busy_timeout=5000`、`wal_autocheckpoint=1000`；
- 并发 partial unique index 只成功插入一条；
- Critical Audit 原子回滚后 Decision/Projection 都为零。

该 Python 证据状态是 `PASS_WITH_UNVERIFIED_DOMAINS`，成熟度是
`worktree_preliminary`。后续 Go 实现证据见
[D1/D2 worktree result](../../artifacts/evidence/M0/worktree/m0-d/code-migration/result.md)：
module/driver 解析、逐物理连接 PRAGMA、空库/重复/失败 migration、未来版本拒绝、并发
partial unique、原子回滚、race/vet 及 Linux amd64/arm64 CGo-free cross-build 已通过。
这些结果仍不能验证目标 Linux 运行、SIGKILL/reboot 或 power-loss 边界。

## Not Verified

以下项目仍未验证：

1. 目标 Linux 上的运行测试；当前只有 Windows 运行与 Linux/amd64、Linux/arm64 cross-build。
2. driver 取消、DB busy、多进程竞争和完整短事务边界。
3. process crash/SIGKILL 后已成功 commit 的恢复以及 WAL reopen/checkpoint 行为。
4. 目标 Linux、本地 filesystem 和存储设备上的 reboot/power-cut durability。
5. 完整 Processing UoW、terminal receipt read-back、checkpoint/generation Store 端口。
6. driver/libc 依赖的 license、漏洞、SBOM、二进制体积、启动时间和负载性能审查。

以上项目完成并生成可定位 Evidence Manifest 前，B1、D2、D4、G18.1 和 G18.2 必须保持
`Implemented / not Verified`、`FAIL` 或 `NOT RUN`，不得以本文 `Accepted` 绕过 Gate。

## Validation Plan

M0-B1/M0-D2/M0-D4 必须至少执行并留证：

- `go env`、`go version`、`go list -m all`、`go mod verify` 和 clean module build；
- Windows/amd64 开发构建，以及 `CGO_ENABLED=0` 的 Linux/amd64、Linux/arm64 构建；
- 空库、已有库、重复启动、失败 migration rollback 和 schema version 测试；
- 多连接 hook 计数与逐连接 PRAGMA read-back，非法/漂移设置的 fail-fast 测试；
- 并发 Automatic/Manual partial unique index、busy timeout 和事务原子回滚测试；
- commit 前、commit 后、WAL write/checkpoint 周围的 SIGKILL/reopen 故障注入；
- 受支持 Linux VM 上的 service restart、OS reboot 和隔离 power-cut 测试；
- 本地 filesystem/VFS/挂载选项记录，以及网络/未知 filesystem 的启动拒绝或明确
  Unsupported 证据；
- `go test -race`、依赖/许可证/SBOM 检查和代表性负载基准。

Evidence 必须记录 commit、dirty 状态、精确命令、Go/driver/SQLite/libc 版本、
`CGO_ENABLED`、GOOS/GOARCH、OS/kernel/filesystem/mount、passed/failed/not-run、checksum、
限制和 reviewer。

## Consequence for M0

- module path、Go 版本策略、SQLite driver 和 PRAGMA 初始化路径已 `Accepted`，可作为
  B1、D2、D4 的唯一实现方向。
- 本 ADR 的实现现已创建 `go.mod`、migration 和第一批 Store 代码；Python Spike 与 Go
  driver Evidence 分开记录，二者都不得冒充目标 Linux durability 证据。
- D2 migration 只能在本文 driver/opener 上实现；任何绕过 opener 的数据库访问都属于
  Contract violation。
- 只有 Not Verified 和 Validation Plan 中对应必测项产生证据后，相关 Gate 才能申请
  `Verified`。
- 改变 module path、Go minor、CGO 边界、SQLite driver、内置/系统 SQLite 选择或 PRAGMA
  基线，必须以新 ADR 明确取代本文并重跑相关回归。

## 一手来源

- [Go Release History](https://go.dev/doc/devel/release)
- [modernc.org/sqlite v1.57.0 package documentation](https://pkg.go.dev/modernc.org/sqlite@v1.57.0)
- [modernc.org/sqlite v1.57.0 go.mod](https://gitlab.com/cznic/sqlite/-/raw/v1.57.0/go.mod)
- [mattn/go-sqlite3 v1.14.50 package documentation](https://pkg.go.dev/github.com/mattn/go-sqlite3@v1.14.50)
- [mattn/go-sqlite3 v1.14.50 README](https://github.com/mattn/go-sqlite3/blob/v1.14.50/README.md)
- [SQLite PRAGMA documentation](https://www.sqlite.org/pragma.html)
