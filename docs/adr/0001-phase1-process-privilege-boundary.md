# ADR-0001：Phase 1 进程权限边界

## 状态

```text
Decision: Accepted
Validation maturity: Implemented / not Verified
Date: 2026-08-30
```

`Accepted` 只表示 Phase 1 已选择本文的权限架构，不表示生产实现、安全边界或
systemd hardening 已经验证。当前初步 IPC Spike 仍有未覆盖域，B4 不得标记为
`Verified`。

## Context

Phase 1 同时包含两个风险等级明显不同的职责：

- 日志、Parser、Detection、Web、CLI、配置和 SQLite 会处理较大的不可信输入面；
- Firewall mutation 需要 Linux `CAP_NET_ADMIN` 或等价 root 权限。

如果整个 Agent 以 root 运行，Web、Parser 或日志处理中的漏洞会直接获得主机 root
权限。如果把任意命令、二进制路径或 Firewall 对象名交给一个通用特权 Helper，形式上
拆分了进程，实际上仍保留了接近 shell 的提权接口。

因此必须在“不可信业务处理”和“最小 Firewall mutation”之间建立可审计、可拒绝、
可故障注入的本地权限边界。

规范依据：

- [Phase 1 Contract](../contracts/guard-phase-1-m0-contract-freeze-v0.3.md) §15.3。
- 初步证据：
  [IPC Spike result](../../artifacts/evidence/M0/worktree/m0-b/ipc-result.json)。

## Decision

Phase 1 采用两个本地进程：

```text
guard-agent
  User=guard
  无 Linux capability
  负责 Web、CLI application service、Source、Parser、Detection、
  Decision、SQLite、Reconcile planning 和 observability
        │
        │ versioned Unix Socket IPC
        ↓
guard-enforcer
  root 启动
  CapabilityBoundingSet 只保留 CAP_NET_ADMIN
  只负责 Guard-owned Firewall Probe、Snapshot 和 mutation
```

必须满足：

1. `guard-agent` 不持有 `CAP_NET_ADMIN`，不得直接调用 Firewall mutation。
2. `guard-enforcer` 不承载 Web、Parser、Detection、SQLite 或业务配置编辑。
3. CLI 和 Web 只能调用 Agent application service，禁止直接写 SQLite，也禁止直接调用
   Enforcer socket。
4. Enforcer 只接受版本化、封闭枚举的 Guard-owned 操作，不提供通用命令执行能力。
5. 所有 Firewall mutation 由 Enforcer 串行执行；并发 planner 不能形成并发外部写。

## Protocol Boundary

### Transport 与身份

- Socket 路径固定为 `/run/guard/enforcer.sock`。
- 父目录 owner/mode 为 `root:guard 0750`。
- Socket owner/mode 为 `root:guard 0660`。
- Enforcer 必须通过 `SO_PEERCRED` 获取实际 peer UID，并与启动时解析的
  `guard` service UID 比较。
- 请求体中的 UID、用户名、角色或调用者声明均不是认证依据。
- 未匹配预期 UID 的连接必须在处理业务 payload 前拒绝。

目录和 socket mode 是第一层限制，`SO_PEERCRED` 是协议身份边界；两者缺一不可。
即使其他用户被错误加入 `guard` group，也不能仅凭 group 写权限获得 Enforcer 调用权。

### Framing 与版本

IPC frame 使用：

```text
uint32-be payload length
+ versioned JSON payload
```

- 单 frame 默认上限为 1 MiB。
- 长度超限、截断 frame、非法 JSON、未知协议版本、未知字段或未知操作必须稳定拒绝。
- Enforcer 不得在长度校验前分配 payload 声明的任意大小内存。
- 协议升级必须显式版本化；禁止把无法识别的请求猜测为旧版本执行。

IPC v1 request 的 wire shape、字段与资源上限由
[`schema/ipc-v1.schema.json`](../../schema/ipc-v1.schema.json) 和对应 golden vectors 冻结；
Apply/Remove mutation response 的 wire shape、稳定状态与错误码由
[`schema/ipc-v1-mutation-response.schema.json`](../../schema/ipc-v1-mutation-response.schema.json)
和对应 golden vectors 冻结。Apply/Remove production response DTO/codec/frame 与 Linux mutation client
已由下文 B4-l2 至 B4-l6 的可编译接口冻结；ProbeCapabilities/SnapshotManaged 的成功响应载荷仍由后续
可编译接口冻结。本文不复制完整 Schema 或生成代码。

### 允许的操作

IPC 只允许以下操作集合：

```text
ProbeCapabilities
SnapshotManaged
ApplyManagedPlan
RemoveManagedInfrastructure
```

每个操作都只能作用于 Contract 定义的 Guard-owned 命名空间。协议禁止接收：

- shell command 或 command fragment；
- binary path、环境变量或工作目录；
- 任意 table、chain、set、hook 或 jump 名称；
- 未规范化的 IP/CIDR；
- 无上限的 operation list、字符串、嵌套对象或错误回显。

### Enforcer 二次校验

Agent 的校验不能替代 Enforcer 边界校验。Enforcer 必须独立验证：

- canonical IP/Prefix、address family 和合法范围；
- owner/version 与 Guard-owned 对象名；
- operation kind、数量、请求大小和字段组合；
- Backend capability 与当前环境是否支持 Plan；
- Plan 不包含 foreign object mutation；
- operation timeout 与 cancellation。

校验失败必须在调用 Firewall 前快速失败。禁止通过 fallback 把非法 Plan 改写成另一种
mutation，也禁止把请求内容拼接为 shell 命令。

## Service Hardening Boundary

生产 unit 的完整内容不由本 ADR 定义，必须在 M1/M6 的 systemd 产物中落盘并验证。
这些产物至少需要满足：

- 两个 service 均设置 `NoNewPrivileges=yes`；
- 两个 service 均设置 `ProtectSystem=strict`、`ProtectHome=yes`；
- `ReadWritePaths` 只包含各自必需目录；
- Agent 以 `guard` 用户运行且无 Linux capability；
- Enforcer 只保留 `CAP_NET_ADMIN`，不得扩大 capability 集合；
- 配置、DB、socket、secret 各自有明确 owner/mode；
- 日志、Audit、Doctor、Web、CLI 和 IPC error 禁止输出 secret。

本 ADR 不写可直接部署的 systemd unit，也不把上述配置存在视为 hardening 已验证。

## Security Consequences

### 正向结果

- Web、Parser、日志处理和大部分业务逻辑不再直接运行于 root。
- Enforcer 的攻击面被限制为本地 socket、有限 framing 和四个 operation。
- 不允许 shell、binary path 和任意对象名，降低命令注入及 foreign Firewall 破坏风险。
- `SO_PEERCRED` 使身份来源独立于不可信 payload。
- 单一 mutation executor 减少并发写导致的 Firewall 状态竞争。
- Agent/Enforcer 可以分别实施最小文件权限、systemd hardening 和故障注入。

### 残余风险

- 已攻陷的 `guard-agent` 仍能通过合法协议滥用 Guard-owned ban 能力；进程拆分不能消除
  这一风险。
- 本设计不声称抵御已经获得本机 root 的攻击者。
- Enforcer 的 JSON decoder、frame parser、Plan validator 和 Firewall adapter 仍是特权
  攻击面，必须接受 fuzz、边界和集成测试。
- 错误的 `guard` 用户/group 管理、socket 生命周期或目录权限可能扩大本地调用面。
- Enforcer crash、restart 或调用结果未知时，Agent 必须按 Reconcile Contract 先 Probe，
  不能盲目重放 mutation。
- `CAP_NET_ADMIN` 本身权限较大；Phase 1 只能通过缩小进程职责和协议能力降低风险，
  不能把它描述成单一 Firewall object 级 capability。

## Operational Consequences

- 安装、升级、启动、停止和卸载需要管理两个 service 及一个 runtime socket。
- Agent Ready 状态必须能区分 Enforcer 未启动、socket 不可达、协议不兼容和 Backend
  不可用。
- Enforcer 重启后不能依赖内存状态；Agent 必须重新 Probe/Snapshot。
- IPC timeout、cancellation 和 unknown result 必须进入对应 Reconcile failure domain，
  不能转换为 Decision `Failed`。
- 协议或 object schema 变化需要显式兼容决策和升级测试，不允许隐式容错。

## Alternatives Considered

### 1. 单一 root Agent

拒绝。实现简单，但 Web、Parser、日志输入和依赖漏洞都直接拥有 root 权限，风险面过大。

### 2. 为 `guard-agent` 直接授予 `CAP_NET_ADMIN`

拒绝。虽然避免完整 root，仍把 Firewall 权限赋予整个 Web/Parser/Detection 进程，不能形成
最小特权隔离。

### 3. Agent 通过 sudo、shell 或通用命令 Helper 执行 Firewall

拒绝。命令字符串、binary path、环境变量和任意参数会扩大协议能力，难以证明只修改
Guard-owned 对象。

### 4. CLI/Web 直接写 SQLite，由 Enforcer 轮询执行

拒绝。它绕过统一 application service、权限检查、Critical Audit 和事务不变量，并使
数据库成为未约束的特权命令队列。

### 5. 额外引入通用 RPC 框架

Phase 1 不采用。当前四个本地操作可由长度帧和版本化 JSON 表达；新增框架会扩大依赖、
协议面和供应链审计范围。若未来需求改变，必须以新 ADR 取代本决策。

## Preliminary Validation

初步证据文件为
[IPC Spike result](../../artifacts/evidence/M0/worktree/m0-b/ipc-result.json)。记录状态是
`PASS_WITH_UNVERIFIED_DOMAINS`，证据成熟度为 `worktree_preliminary`，base commit 为
`3aca38a`，运行时工作树为 dirty。
Spike 使用 Windows 主机 Go `go1.27.0` 交叉构建 Linux/amd64、`CGO_ENABLED=0`，并在
Ubuntu 22.04 WSL2、普通 UID 1000、kernel
`6.18.33.2-microsoft-standard-WSL2` 环境运行。

该 Spike 观察到：

| 检查 | 初步结果 |
|---|---|
| socket mode `0660` | PASS |
| runtime directory mode `0750` | PASS |
| `SO_PEERCRED` UID 与当前调用者匹配 | PASS |
| allowlisted operation 接受 | PASS |
| 非允许 operation 拒绝 | PASS |
| oversized frame 拒绝 | PASS |
| truncated length / payload 拒绝 | PASS |
| 非法 JSON、未知字段、多 JSON value 拒绝 | PASS |
| 未知版本拒绝 | PASS |
| Contract 四个 allowlisted operation 接受 | PASS |
| fuzz seed corpus：合法、超限、截断、未知字段 | PASS |
| 临时测试二进制已移除 | PASS |

B4-f 在 baseline `9eec36c` 的 dirty worktree 上补充 Linux-only table-driven test 与 fuzz
target；Ubuntu WSL2 初跑、`count=20` 和原 Spike smoke 均通过。Windows 主机全仓 race
受 `linux` build tag 限制，不覆盖本 Spike；本批 targeted Evidence 是 CGO-free Linux/amd64
test binary、Linux vet 与 Linux amd64/arm64 test/main 编译，不得写成 targeted race。

B4-g 在同一 baseline 的 dirty worktree 上新增正式
[`ipc-v1.schema.json`](../../schema/ipc-v1.schema.json) request Schema 和
[`schema/testdata/ipc-v1/`](../../schema/testdata/ipc-v1/) golden vectors。Schema 将四个 request
建模为互斥 closed union；所有 object 递归拒绝未知字段，`ApplyManagedPlan` 一次只能携带一个
failure domain 和一个 typed operation。wire DTO 不存在 shell、binary、env、cwd 或物理
table/chain/set/hook/jump/object-name 字段；owner 固定为 `guard/v1`，request 为 64 KiB、深度 8、
token 4096，policy prefix 总数为 1024。6 个 valid、23 个 invalid fixture，以及 request size、
depth、token、prefix exact/one-over 和 fuzz seed invariants 已由 stdlib-only test evaluator 验证。
code checkpoint 的两个 P2 经 delta 修复后，独立审查为 `COMPLETE / FRESH / APPROVED / PASSED`，
P0/P1/P2/P3 均无。

B4-h 在 baseline `a61d8c2` 上新增 `internal/ipc` production JSON payload predecoder、
sealed read-only typed Request/ManagedPlan DTO 与确定性 semantic validator。production decoder
在构造 DTO 前执行 64 KiB、UTF-8、单 JSON value、duplicate key、深度 8、token 4096、递归
unknown/missing/type、closed operation/domain、`guard/v1`、digest/revision、canonical/sorted/unique
Prefix、prefix 总数 1024、mandatory loopback、protected target、timeout/expiry 与 scope order 校验；
错误只返回固定内部分类，不建立 response wire contract。冻结的 6 valid + 23 invalid golden 已直接
运行于 production decoder。

初次 Tier-3 code checkpoint 发现 Draft 2020-12 mathematical integer 被词法整数解析误拒的 P1，
以及导出 concrete DTO 零值可伪装为已验证 union 的 P2。repair round 1 使用精确 `big.Rat`
integer/const 数值语义，并将导出 variant 改为由未导出 concrete value 实现的 sealed read-only
interface；fresh delta review 将两项标为 resolved，最终代码分区 P0/P1/P2/P3 均无。

Windows targeted/full race、repeat、vet、module、格式检查，Linux amd64/arm64 CGo-free test/build
以及 Ubuntu 22.04.5 WSL2 amd64 初跑/count=20 已通过。WSL 首次 standalone 配方因未复制 golden
相对路径而在 fixture load 阶段失败；重建 package-shaped 隔离 fixture 后通过。Linux native race
因 WSL 无 Go toolchain 为 `NOT RUN`；CI 为 `NOT CONFIGURED`。Windows cross-build fixture 因 host
删除策略阻止仍保留为非阻断清理项。

初次 final full-scope review 发现 Evidence 只有命令摘要、不能独立重建交叉构建与 WSL corpus
布局的 P1。Evidence repair round 1 补齐 PowerShell 工作目录、`CGO_ENABLED`/`GOOS`/`GOARCH`、
输出路径、checksum/size readback、错误配方、修正配方、count=1/20 与 cleanup absent readback；
fresh delta review 将 P1 标为 resolved，最终为 `COMPLETE / FRESH / PASSED /
APPROVED_WITH_FOLLOWUPS`。P0/P1/P3 均无；仅 Windows Temp fixture cleanup 保留为非阻断 P2。

B4-i 在同一 baseline 上新增 production `uint32-be` frame reader，并将每个完整 frame 直接交给
B4-h `DecodeRequest`。4-byte header、1 MiB frame cap 与 64 KiB request cap 均在 payload 分配/
读取前处理；header/payload 截断、frame 超限使用固定且不回显输入的内部分类。成功路径只消费一帧，
允许同一 reader 连续解码；任一错误后 caller 必须丢弃 stream，避免在未 drain 的攻击者声明长度后
继续猜测边界。6 个 frozen valid fixture、64 KiB/1 MiB exact/one-over、连续帧、错误脱敏与 fuzz
seeds 已直接通过 production frame reader。

Windows targeted/full race、repeat、vet/module 与 Linux amd64/arm64 CGo-free test/build 通过；Ubuntu
22.04 WSL2 package-shaped fixture 的初跑和 count=20 通过，host/WSL 临时产物均清理并回读 absent。
独立 Tier-3 code checkpoint 为 `COMPLETE / FRESH / APPROVED / PASSED`，P0/P1/P2/P3 均无。
final full-scope review 的一项 production-library 边界措辞 P2 经 ADR repair round 1 修复，最终为
`COMPLETE / FRESH / APPROVED / PASSED`，P0/P1/P2/P3 均无。Linux native race、持续 fuzz 与 CI
仍为 `NOT RUN / NOT CONFIGURED`。用户 Code Review 已明确通过，B4-i 当前为
`DONE / Implemented`；这不提升 B4 总项或任何 M0 Gate。

B4-j 新增 Linux-only production accepted-connection identity gate：`DecodeUnixFrame` 先通过
`SO_PEERCRED` 读取实际 peer UID，与 Enforcer 启动时解析并注入的 `guard` UID 比较，只有匹配后
才调用 B4-i `DecodeFrame`。credential 获取失败或 UID mismatch 都返回固定脱敏分类；函数不使用
root Enforcer 的 `os.Getuid()`，不关闭连接，也不接管 caller lifecycle。

真实 WSL2 Unix socket 测试证明 same-UID 合法 frame 通过、mismatch 在任何 frame byte 读取前拒绝，
并可在拒绝后由 `DecodeFrame` 完整读出原 frame；closed connection 与 UID/OS error 脱敏也通过。
Windows 全仓回归、Linux amd64/arm64 CGo-free vet/test/build、WSL targeted/full count=1/count=20 和
临时产物 cleanup/absent readback 均通过。独立 Tier-3 code checkpoint 为
`COMPLETE / FRESH / APPROVED / PASSED`、repair round 0、P0/P1/P2/P3 全无。final full-scope review
初次报告精确命令 Evidence P1；Evidence repair round 1 使用全新 fixture 重跑并补齐 build、hash/size、
WSL、失败配方和 cleanup 命令后，fresh delta 最终为 `COMPLETE / FRESH / APPROVED / PASSED`，
P0/P1/P2/P3 全无。Linux native race、真实 `guard`/另一 OS UID、root/systemd/capability 与 CI 仍未运行。
用户 Code Review 已明确通过，B4-j 当前为 `DONE / Implemented`；这不提升 B4 总项或任何 M0 Gate。

B4-k 新增 Linux-only production listener/socket lifecycle library：`ListenUnix` 固定使用
`/run/guard/enforcer.sock`，由 root Enforcer 在启动时注入 `guard` GID；父目录与 socket 分别按
`root:guard 0750`、`root:guard 0660` 创建并以 path/fd read-back 校验。已验证目录 fd 的非阻塞
独占 `flock` 从 stale detection、identity-guarded removal、bind 一直持有到 listener Close 后的
owned-socket cleanup，避免两个协作 Enforcer 并发启动形成 unlinked listener 或 split-brain。
活跃 socket、symlink、普通文件、owner/mode drift 与 device/inode replacement 均 fail-closed；只有
owner/mode 匹配、connect 明确 `ECONNREFUSED` 且二次 identity 未变化的 Unix socket 才可视为 stale。

`AcceptRequest` 支持 context cancellation/deadline；accepted connection 必须先经过 B4-j
`DecodeUnixFrame`，任一失败都由 listener 关闭连接，只有成功才把连接所有权交给 caller。Ubuntu
22.04.5 WSL2 的真实临时 socket 初跑、`count=20` 与完整 IPC package 运行通过；Linux amd64/arm64
CGo-free vet/test/build、Windows 全仓回归、格式与 cleanup read-back 通过。独立 Tier-3 checkpoint
repair round 1 修复目录配置失败残留与并发 stale takeover 两个 P1；测试又暴露并修复 mkdir error
shadow、deadline 分类窄竞态及 replacement cleanup 分类漂移。final full-scope review 的 ADR P2 与
Evidence 精确命令 P1 分别在 docs/Evidence repair round 1 修复并通过 fresh delta，最终为
`COMPLETE / FRESH / APPROVED / PASSED`，P0/P1/P2/P3 全无。用户 Code Review 已明确通过，
B4-k 当前为 `DONE / Implemented`；这不提升 B4 总项或任何 M0 Gate。

B4-l1 冻结 mutation-only IPC v1 response contract。`ApplyManagedPlan` 只能返回一个明确 domain
（`infrastructure`、`policy` 或 `target`）及 `confirmed`、`rejected`、`unknown` 三态之一；
`RemoveManagedInfrastructure` 使用同一三态但禁止携带 domain。`confirmed` 禁止 `error_code`；
`rejected` 只能使用 operation-specific allowlist；`unknown` 只能使用 `unknown_result`。wire object
递归 closed，不存在 payload、message、details、cause、command、request_id 或任意扩展元数据。

mutation response 最大 4 KiB、实例深度 2、JSON token 32，并继续受 1 MiB frame 上限约束。12 个
valid 与 28 个 invalid golden 覆盖六个互斥 union 分支、错误码矩阵、duplicate key、非法 UTF-8、
多 JSON value、资源边界及 closed-property audit；精确 4096/4097 bytes、depth 2/3、token 32/33
边界由程序化测试验证。

连接处理保持 fail-closed：`SO_PEERCRED` 不可用或 UID 不匹配、frame 截断/超限、非法 UTF-8/JSON、
duplicate key、未知版本/operation/field 或 request schema/semantic rejection 均在响应前断开；已认证且
有效的 Apply/Remove 请求至多写一个 typed response 后关闭。编码或写入失败只关闭连接，不尝试第二个
响应。mutation caller 对缺失、截断或非法响应必须判定 `Unknown`，先 Probe/Snapshot reconciliation，
不得盲目重试写操作。shutdown/cancel 导致无法写响应时同样按 `Unknown` 处理。

B4-l1 仅证明 Schema/golden/test contract；用户 Code Review 已明确通过，当前为
`DONE / Implemented`。这不证明 production
response DTO/codec/writer/client、accept-loop/executor round-trip，也不冻结 ProbeCapabilities 或
SnapshotManaged 的成功载荷。这不提升 B4 总项或任何 M0 Gate。

B4-l2 将 B4-l1 已冻结的 mutation-only response contract 落为 production typed DTO 与 payload
codec。`MutationResponse`、`ApplyManagedPlanResponse` 和
`RemoveManagedInfrastructureResponse` 均为 sealed read-only interface；六个构造入口只允许创建
Apply 三 domain 与 Remove 的 confirmed/rejected/unknown 合法分支。wire status/error code 与本地
codec validation error 使用不同类型，避免把 Backend 内部字符串或攻击者输入带入稳定错误分类。

`EncodeMutationResponse` 使用固定字段顺序输出 compact JSON，拒绝 nil/typed-nil 与非法内部状态；
`DecodeMutationResponse` 按 4096-byte cap、UTF-8、response-specific duplicate/depth/token/single-value
scanner、closed union 的顺序 fail-closed。version 接受 JSON 数学整数 `1`、`1.0`、`1e0`，拒绝分数、
非正数、字符串、null 与 int64 overflow。40 个既有 golden、六分支 constructor matrix、operation-specific
allowlist、精确资源边界、分类优先级、nil、错误脱敏和 seed fuzz invariants 已由 production codec 测试复用。

B4-l2 用户 Code Review 已明确通过，当前为 `DONE / Implemented`；仍不描述为 `Verified`。本批不含
uint32-be response frame、raw payload writer、Unix client、accept-loop/executor、fake/backend 映射、
connection deadline/partial-write/close/retry、ProbeCapabilities/SnapshotManaged success payload，亦未修改
Schema、依赖、配置、数据库或 systemd。以上边界不提升 B4 总项、G18.1-G18.3 或 M0 Gate。

B4-l3 将 mutation response 接入 platform-neutral `uint32-be` framing。导出的
`DecodeMutationResponseFrame(io.Reader)` 先验证完整 header，再按 1 MiB frame cap、4 KiB mutation
payload cap、完整 payload、B4-l2 codec 的顺序 fail-closed；cap 均在正文分配/读取前生效。frame 或
payload 失败返回既有稳定脱敏 error classification，不伪造 wire `unknown_result`。

`WriteMutationResponseFrame(io.Writer, MutationResponse)` 在首次写前完成 typed encode；encode 失败零
写入。package-private raw helper 只完成同一 frame：positive short-write 续写剩余 suffix，error、`0,nil`
或非法 write count 立即返回稳定 `write_failed`，不写第二帧、不关闭 writer、不设置 deadline，也不承担
mutation retry。successful return 仅表示完整 frame 已交付给 `io.Writer`，不表示 peer 已读取或确认。

B4-l3 用户 Code Review 已明确通过，当前为 `DONE / Implemented`；仍不描述为 `Verified`。本批不含
exported raw payload writer、request encoder/constructors、Unix client、accept-loop/executor、Backend/result
mapping、connection deadline/close/shutdown、Unknown/Probe-first orchestration、Probe/Snapshot success，亦未
修改 Schema、依赖、配置、数据库或 systemd。B4 总项、G18.1-G18.3 与 M0 Gate 均不提升。

B4-l4 将 frozen IPC v1 mutation request contract 落为安全 outbound payload API。sealed
`MutationRequest` 仅允许 `ApplyManagedPlanRequest` 与 `RemoveManagedInfrastructureRequest`；Apply
Infrastructure/Policy/Target 和 Remove 四个 domain-specific 构造入口固定 version、owner、operation、
kind 与 schema version，不接收 raw JSON/map、command、binary/env/cwd 或 Firewall 物理对象名。Policy
构造复制 caller slices；非法 Prefix、顺序、timeout、expiry 或 scope 均按既有 decoder 的稳定分类
fail-closed，不自动 mask、排序、去重或修正。

`EncodeMutationRequest` 使用私有固定字段顺序 wire struct 输出 deterministic compact JSON，拒绝
nil/typed-nil 与 package-private 非法状态；Policy 空 allowlist 输出 `[]`，Target 无 effective-until 输出
显式 `null`。encoder 复用构造校验和 production `DecodeRequest` 闭环，不泄漏攻击者提供的 owner 或其他
非法字段。6 valid + 23 invalid golden、非-golden caller value round-trip、slice aliasing、资源 exact/
one-over、错误脱敏与 seed fuzz 已由专项测试复用；完整验证和独立 implementation checkpoint 已通过。

B4-l4 用户 Code Review 已明确通过，当前为 `DONE / Implemented`；仍不描述为 `Verified`。本批不含
request frame writer、Unix client、Dial/deadline/Close、response routing、semantic retry、Probe/Snapshot
success、accept-loop/executor、Backend/Firewall，亦未修改 Schema、依赖、配置、数据库或 systemd。
B4 总项、G18.1-G18.3 与 M0 Gate 均不提升。

B4-l5 将 B4-l4 typed mutation request encoder 接入 platform-neutral `uint32-be` framing。导出的
`WriteMutationRequestFrame(io.Writer, MutationRequest)` 必须在首次写入前完成 `EncodeMutationRequest`；
validation failure 对包括 nil writer 在内的所有 writer 保持零写入。编码成功后仅复用 package-private
`writeFramePayload` / `writeAll`：positive short-write 续写剩余 suffix，error、`0,nil`、负数或 overlong
write count 立即返回稳定脱敏 `write_failed`，不泄露底层 writer 错误。

writer 和 stream 始终由 caller 持有；函数不 Close、Flush、SetDeadline，不写第二帧，不自动重试 mutation，
也不构造 response 或 `Unknown`。successful return 只表示完整 frame 已交付给 `io.Writer`，不表示 peer 已
读取、执行或确认。现有 `FrameErrorCodeWriteFailed` 仅将注释从 response-specific 泛化为 IPC frame，错误码、
cap 与运行时语义均未改变。

B4-l5 用户 Code Review 已明确通过，当前为 `DONE / Implemented`；仍不描述为 `Verified`。本批不含
Unix client、Dial/deadline/Close、response routing/correlation、semantic retry、Unknown/Probe-first、
Probe/Snapshot success、accept-loop/executor、Backend/Firewall，亦未修改 Schema、依赖、配置、数据库或
systemd。B4 总项、G18.1-G18.3 与 M0 Gate 均不提升。

B4-l6 增加 Linux-only `RoundTripMutation(context.Context, MutationRequest)`，production path 固定为
`/run/guard/enforcer.sock`，server peer UID 固定为 root/0；调用方不能注入 path、UID、network 或 dialer。
typed request 在触碰 transport 前预校验，Dial 成功后先通过 private `verifyUnixPeerUID` 完成
`SO_PEERCRED` 身份校验，再装配 caller context deadline/cancellation；arm 完成后、首次写入前还必须同步
检查 context，避免 cancellation watcher 尚未调度时抢先发送 mutation。

每次调用只建立一个连接、写一个 typed mutation request frame、读一个 typed mutation response frame并
best-effort Close；不复用连接、不写第二帧、不重连或自动重试。Apply response 必须匹配 operation/type/
domain，Remove response 必须匹配 operation/type；不匹配返回稳定脱敏 `response_mismatch`。完整且关联正确
的 response 是成功线性化点，优先于随后 cancellation；合法 wire `unknown/unknown_result` 作为正常 typed
response 返回。client 不构造 wire Unknown；写入开始后未取得完整关联 response 时，上层必须按结果不确定
执行 Probe/Snapshot-first，再决定后续动作。

B4-l6 的 contract guard pre-write cancellation 与 Linux evidence 两个 P1
均已修复并通过独立 closure；Linux 真实临时 Unix socket IPC 全包 Race `count=20`、Windows/Linux 全仓
normal/Vet/module、Windows full Race、三目标 CGo-free compile 均通过。post-Dial cancellation 测试使用
调用栈定位 arm 阶段，保留一个非阻塞 P2 稳健性 follow-up。final FULL_SCOPE/INTEGRATION 已以
`APPROVED_WITH_FOLLOWUPS / CHILD_AGENT / COMPLETE / FRESH / PASSED` 通过；用户随后明确回复
`B4-l6 Code Review 通过`，本 Delivery Unit 当前为 `DONE / Implemented`，但不描述为 `Verified`。
本批不含 Probe/Snapshot success、Probe-first 编排、accept-loop/executor、Backend/
Firewall、配置、Schema、依赖、数据库或 systemd；B4、G18.1-G18.3 与 M0 Gate 均不提升。

B4-l7 在 observation wire contract 之前冻结 platform-neutral Firewall capability domain authority。
`BackendKind` 是仅含 `nftables-native`、`iptables-nft`、`iptables-legacy` 的 closed enum；
`FirewallCapabilities` 仅能由 validating constructor 创建，字段私有且只读。工具版本限制为 1..128 字节、
trimmed printable ASCII；至少支持一个 IP family 与一个 INPUT/FORWARD scope。native timeout 依赖 native set，
crash-safe expiry 依赖 native timeout，Docker 安全集成证明依赖 FORWARD，mutation readiness 依赖 ownership
证明与 CIDR；mutation-ready native nftables 还必须具备 native set 与 atomic batch。iptables 的 atomic batch
不被错误绑定到 native set，UFW 安全集成证明也不被错误绑定到 INPUT。非法组合返回零值和稳定脱敏错误，
不得携带 command、binary、任意物理对象名或 raw backend error。

B4-l7 的 final delta closure 与记录 freshness closure 均为
`APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无；用户随后明确回复
`B4-l7 Code Review 通过`，当前为 `DONE / Implemented`，但不描述为 `Verified`。本批没有
修改 Backend interface，也不含 ManagedState/ForeignContext、Probe/Snapshot response Schema/DTO/codec/
frame/client、executor/serve loop、真实 nftables/iptables、配置、依赖、数据库、systemd 或 runtime
composition；B4、G18.1-G18.3 与 M0 Gate 均不提升。

B4-l8 在 B4-l7 domain authority 之上冻结 success-only `ProbeCapabilities` wire Schema。root 精确为
`{version,operation,payload}`；payload 精确要求 15 个字段，一对一映射
`FirewallCapabilitiesSpec`。backend 仅允许三个 closed kind，tool version 为 1..128 字节、无首尾空格的
printable ASCII，其余 13 个 capability 均为必须显式出现的 boolean。结构通过后，semantic test 必须使用
`NewFirewallCapabilities` 重建领域值；构造失败稳定归类为 `semantic_rejected`。iptables native set 与
atomic batch 保持双向独立，UFW proof 不绑定 INPUT。response 上限为 4 KiB、instance depth 2、JSON token
64，并继承 1 MiB frame metadata；duplicate key、非法 UTF-8、多 JSON value、未知/缺失/类型混淆及
command/error 等注入均 fail-closed 且分类脱敏。

B4-l8 的独立终审与记录新鲜度闭环最终为
`APPROVED / CHILD_AGENT / COMPLETE / FRESH / PASSED`、P0-P3 全无；用户随后明确回复
`B4-l8 Code Review 通过`，当前为 `DONE / Implemented`，但不描述为 `Verified`。本批只定义成功事实；
`mutation_ready=false` 仍是合法 Probe success，不表示 Probe 执行失败。未知 backend/tool/topology 或执行失败
必须由后续独立 closed failure envelope 表达。本批未新增 production DTO/codec/frame/client，不含
Snapshot、Backend interface、executor/serve loop、真实 Firewall、配置、依赖、数据库、systemd 或 runtime
composition；B4、G18.1-G18.3 与 M0 Gate 均不提升。

B4-l9 将 B4-l7 domain authority 与 B4-l8 success Schema 接入一个完整的 `ProbeCapabilities` IPC transport。
failure root 精确为 `{version,operation,error_code}`，不复用 mutation status machine；`error_code` 仅允许
`unsupported` 与 `not_ready`。前者表示当前 backend/tool/topology 属于明确不支持的闭集，后者表示未能取得
完整、可信且当前的 Probe 事实。failure 不允许 payload、message、details、cause、command、binary、env、cwd、
socket、UID、物理对象名或 raw backend error。已取得完整事实但当前不能 mutation 时仍必须返回 B4-l8 success，
并明确 `mutation_ready=false`，不得降格为 failure。

production transport 使用 sealed typed response、deterministic codec、uint32-be frame、固定
`/run/guard/enforcer.sock` 与 root peer UID。Agent 每次只建立一个连接、认证后写一个固定空 payload Probe request、
读取一个关联 response，不自动重连或重试；caller context 是 Dial 与 I/O 的唯一时间预算。服务端单次适配器必须
在 `UnixListener.AcceptRequest` 完成 peer authentication 与 request decode 后才调用 typed handler，并取得连接
所有权、最多写一个 response、在所有路径关闭连接。handler 只能返回已验证的 success/failure DTO，不能返回
任意 string/map/raw error。success 在编码前再次调用 `FirewallCapabilities.Validate`，decode 必须通过
`NewFirewallCapabilities` 重建，不维护第二套宽松语义。若 context 在 frame 尚未完整写入时终止，服务端
必须保留 `context.Canceled`/`context.DeadlineExceeded` identity；完整 frame 已写入后即达到 delivery point，
随后 cancellation 不得覆盖已完成结果。

B4-l9 只证明 closed observation transport 与 injected provider plumbing。它不定义或注册 production Firewall
Backend，不证明 backend auto-detection、tool/version、ownership、UFW/Docker coexistence、packet path 或生产
`mutation_ready=true`，也不包含 SnapshotManaged、通用 Enforcer accept loop/executable、配置、依赖、数据库、
systemd 或真实 Firewall mutation。用户已明确回复 `确认 B4-l9`，允许按上述公共 wire/API 边界实施；完成状态、
验证结论与用户 Code Review 门仍分别记录。

以上不证明真实 `/run/guard` root:guard、生产跨 UID、executable accept loop、response/executor、
systemd、root capability 或 Firewall 行为，也不提升 B4 总项或任何 M0 Gate。

这些结果证明 IPC v1 request Schema/golden contract、production frame reader、payload decoder/typed
validator、accepted-connection peer identity gate 与 listener/socket lifecycle library 在当前 worktree
闭合。它们不证明真实 Snapshot/owner/capability validator、生产跨 UID 环境、root Enforcer、systemd
或 `CAP_NET_ADMIN` 边界。

## Not Verified

以下项目仍未验证，任一项都不得从本文的 `Accepted` 状态推导为 B4 `Verified`：

1. 使用生产 `guard` 用户执行的跨 UID 拒绝。
2. 两个真实 systemd unit 的 hardening 和精确 `ReadWritePaths`。
3. production payload decoder 已校验 canonical Prefix、owner/version 和单 domain/operation count；
   真实 Snapshot digest、installed owner、capability 与 Guard-owned object-role mapping 尚未接入。
4. Agent/Enforcer restart、timeout、cancellation 和 unknown result 恢复。
5. root Enforcer 的 `CAP_NET_ADMIN` bounding、Agent 无 capability 及实际 nftables 权限。
6. production frame reader/payload decoder、accepted Unix connection peer credential gate 与 listener/
   socket lifecycle library 已集成；仍无真实 `/run/guard` root:guard、生产跨 UID、executable accept
   loop、response/executor 或持续 fuzz campaign。当前只证明 frozen golden、framing/JSON 资源
   exact/one-over、seed invariants 与临时 same-UID/mismatched-expected-UID socket 通过 production library。
7. 非 WSL2 的目标 Linux 发行版、内核和 systemd 支持矩阵。

完成以上生产用户、systemd、capability、validator 和恢复测试，并生成可定位 Evidence
Manifest 后，B4 才能进入 `Verified` 评审。

## Validation Plan

M0-B/M0-D 必须补齐：

- 真实 `guard` service UID 与另一个非授权 UID 的正反测试；
- socket 目录创建、权限漂移、陈旧/活跃 socket、并发 ownership 与安全 cleanup 已通过 production
  library 测试；仍需真实 `/run/guard` root:guard、跨 UID 与进程 restart 测试；
- 将已冻结的四 operation request Schema/golden vectors 接入 production predecoder/typed validator，
  并在调用 executor 前验证大小、深度、token、canonical target、owner、Snapshot、capability 和
  Guard-owned object-role mapping；
- Agent 无 capability、Enforcer 仅 `CAP_NET_ADMIN` 的运行时检查；
- systemd security 属性和精确文件写权限测试；
- production frame reader/payload validator 已覆盖 malformed/truncated/oversized/version mismatch
  table/fuzz seeds，并已接入 accepted-connection peer credential gate 与 listener/socket lifecycle；
  仍需真实跨 UID、executable accept loop 与持续 fuzz campaign；
- timeout/cancel 后 Probe-first 的 Reconcile 集成测试；
- 在 Phase 1 发布支持 Linux 环境中的重复验证。

验证产物必须记录 commit、精确命令、OS/kernel/systemd/nft 版本、passed/failed/not-run、
已知限制、checksum 和 reviewer。初步 JSON 不能单独替代这些证据。

## Consequence for M0

- 权限架构选择已接受，可作为 M1 Runtime 与 M6 Firewall 的实现方向。
- 当前 Spike 只支持 B4 `Implemented / not Verified`。
- 在 Not Verified 项完成前，G18.1 的“权限边界 ADR 已批准”可以满足设计决策要求，
  但涉及权限实现与可执行验证的 Gate 仍必须保持 `FAIL` 或 `NOT RUN`。
- 任何扩大 IPC operation、capability、可写路径或 Enforcer 职责的修改，都必须通过新的
  ADR 或明确取代本文，并重跑权限与 Firewall 回归测试。
