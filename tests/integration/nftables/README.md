# nftables 隔离集成验证

本目录的验证只在一次性 Docker 容器中运行。CI 使用 `--network none`、
`--cap-drop ALL` 与显式补回的 `CAP_NET_ADMIN`、`CAP_NET_RAW`、`CAP_SYS_ADMIN`；容器只读，`/run` 与 `/tmp`
为 tmpfs；`/tmp` 为 1 GiB，以容纳并执行 Go test 编译缓存，因此它是唯一以
`exec` 显式挂载的可写路径。容器不挂载 Docker socket、宿主 Firewall、工作目录或
Go 缓存。

镜像构建阶段从当前 checkout 复制源代码并下载受 `go.sum` 约束的模块。运行阶段
不联网，执行 `go test -tags=integration,nftables ./internal/firewall/nftables`。该目录
缺失、没有可运行的 `Test` 函数或测试失败都会让 CI 失败，防止以空的根包测试替代
真实 nftables 集成验证。专用入口固定为带 `linux && integration && nftables` build tag 的
`TestNftablesBackendIntegration` 与 `TestEnforcerRuntimeNftablesIntegration`；runner 会先
精确检查两个函数，再在同一隔离容器执行 `./internal/firewall/nftables` 和
`./internal/enforcer` 两个 tagged package。这样 CI 同时覆盖真实 Backend 生命周期与
Enforcer Runtime/IPC 组合，而非只验证后端直连。

这两个函数仅是 runner 的入口存在性前置检查。Firewall 包会完整执行全部适用的
`integration,nftables` 测试；Enforcer 包仅运行其固定的 E2E 函数。仓库另有需要 root:guard
用户 fixture 的通用 `integration` 测试，它不属于 nftables 容器的身份模型，不能因共享 build tag 被隐式执行。
Runner 只在完成容器与 capability 前置检查后导出
`GUARD_NFTABLES_INTEGRATION=1` 和 `GUARD_NFTABLES_ISOLATED=1`，使该测试不会
以 Skip 冒充隔离验证。

Runner 还会先运行 `run-nftables-golden-state`：它在同一个 disposable network namespace 中
创建三个容器内 network namespace，并通过两对临时 veth 发送真实 IPv4/IPv6 INPUT 与 FORWARD
ICMP 流量，验证 Provider 固定布局的 allow/protected-before-ban、ban 生效、失败 batch 原子性、
foreign table preservation 和 Guard table cleanup。该拓扑需要 `CAP_NET_ADMIN`、`CAP_NET_RAW`
与 `CAP_SYS_ADMIN`，并对这个一次性、
`--network none`、无挂载/无 Docker socket 容器禁用 seccomp；它们只用于创建和清理容器内部
network namespace。该高权限 job 仅在受信任的 `master` push 或手动触发时运行；容器外没有任何
Firewall 或 Docker 状态写入。

基础镜像固定为在本批 Docker Desktop 构建中解析到的官方
`golang:1.27-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b`。

运行时 `nft` 仅访问容器自身的 network namespace。测试应负责建立和清理
全部 Guard-owned nftables 对象，并在退出前验证 foreign 对象保持不变。Golden State runner
记录的 hook/priority 只对这个无 UFW、无 Docker、`--network none` 的 disposable baseline 有效；
它不替代目标 Linux、UFW 或 Docker 的 Golden State 验证。

本地与 CI 都应通过仓库工作流执行；默认 Go 验证入口为
`./scripts/verify.ps1`。nftables 任务由 `.github/workflows/nftables-integration.yml`
调用上述镜像，避免使用宿主 namespace 或主机 Go 缓存。
