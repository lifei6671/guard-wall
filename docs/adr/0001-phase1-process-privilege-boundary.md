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
production Go DTO/parser、response shape 与错误码仍由后续 M0-D 可编译接口冻结。本文不复制
完整 Schema 或生成代码。

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

这些结果只证明初步 framing fail-closed、peer credential 读取和 operation allowlist 路径在
该 WSL2 环境可运行，并证明 IPC v1 request Schema/golden contract 在当前 worktree 的 test-only
evaluator 中闭合。它们不是 production IPC parser、真实 Snapshot/owner/capability validator、
生产 `guard` 用户、root Enforcer、systemd 或 `CAP_NET_ADMIN` 边界的验证证据。

## Not Verified

以下项目仍未验证，任一项都不得从本文的 `Accepted` 状态推导为 B4 `Verified`：

1. 使用生产 `guard` 用户执行的跨 UID 拒绝。
2. 两个真实 systemd unit 的 hardening 和精确 `ReadWritePaths`。
3. production parser 中的 canonical Prefix、owner/version、单 domain/operation count、真实 Snapshot
   digest、capability 与 Guard-owned object-role mapping 校验；request Schema/golden 已落盘，尚未接入
   production Enforcer。
4. Agent/Enforcer restart、timeout、cancellation 和 unknown result 恢复。
5. root Enforcer 的 `CAP_NET_ADMIN` bounding、Agent 无 capability 及实际 nftables 权限。
6. production IPC parser/validator 集成与持续 fuzz campaign；当前 framing Spike 和 B4-g test-only
   evaluator 已覆盖截断、非法 JSON、未知字段/版本/操作、资源 exact/one-over 和 seed invariants，
   但不代表 production predecoder 或持续 fuzz。
7. 非 WSL2 的目标 Linux 发行版、内核和 systemd 支持矩阵。

完成以上生产用户、systemd、capability、validator 和恢复测试，并生成可定位 Evidence
Manifest 后，B4 才能进入 `Verified` 评审。

## Validation Plan

M0-B/M0-D 必须补齐：

- 真实 `guard` service UID 与另一个非授权 UID 的正反测试；
- socket 目录创建、权限漂移、陈旧 socket 和 Enforcer restart 测试；
- 将已冻结的四 operation request Schema/golden vectors 接入 production predecoder/typed validator，
  并在调用 executor 前验证大小、深度、token、canonical target、owner、Snapshot、capability 和
  Guard-owned object-role mapping；
- Agent 无 capability、Enforcer 仅 `CAP_NET_ADMIN` 的运行时检查；
- systemd security 属性和精确文件写权限测试；
- 将已在 Spike 通过的 malformed/truncated/oversized/version mismatch table/fuzz seed
  合入 production parser/validator，并执行持续 fuzz campaign；
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
