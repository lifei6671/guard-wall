# ADR-0014：Linux systemd 交付边界

## 状态

```text
Decision: Accepted
Validation maturity: Implemented
Date: 2026-09-04
```

## 背景

Phase 1 有一个持有 Firewall capability 的 Core 和一个处理配置、SQLite 与业务逻辑的
Agent。交付工件必须把二者的运行身份、可写目录和启动顺序固定下来，不能依赖安装者
自行猜测 service name、socket 路径或文件权限。

现有包装工件已经定义 `guard-wall-core`、`guard-wall-agent`、`guard` service account、
`/etc/guard/guard-wall.yaml`、`/var/lib/guard` 与 `/run/guard`。这些是可审查的部署输入，
不是已经在目标 Linux 主机完成安装或 service lifecycle 验证的声明。

## 决定

1. Phase 1 发布名固定为 `guard-wall-core` 与 `guard-wall-agent`。源码内部的
   `guard-enforcer`、`guard-agent` 逻辑角色保持不变；`guard-wall-server` 留给后续阶段，
   当前不安装或启用。
2. `guard-wall-core.service` 以 `root:guard` 运行，只保留 `CAP_NET_ADMIN`，负责固定
   `/run/guard/enforcer.sock` 的 runtime directory/socket 生命周期。Agent unit 必须依赖并在
   Core 后启动。
3. `guard-wall-agent.service` 以无特权 `guard:guard` 运行，不拥有 capability。它唯一的
   持久化应用 state 是 `StateDirectory=/var/lib/guard`，读取 `root:guard 0640` 的配置；不得获得
   `/etc/guard` 写权限或直接 Firewall capability。
4. packaging 必须通过 sysusers 创建固定 `guard` 身份；安装流程必须明确安装两份 binary、
   unit、配置和 sysusers 工件，再执行 daemon reload 与 enable/start。升级时先停止两个
   unit，重载后先启动 Core 再启动 Agent；数据和配置删除另需显式、独立审批的 purge 流程。
5. 交付工件应采用最小必要的 systemd sandbox/capability 设置，并保持 Core/Agent 的
   目录所有权与可写路径分离。它们不替代 socket runtime read-back、真实 parent trust、
   production config watcher 或目标主机 service restart 证据。

## 后果

- 安装者有唯一的服务名、身份、配置和 state/runtime 路径，不需要从源码推导。
- Agent 即使被攻陷也不获得 Core 的 `CAP_NET_ADMIN` 或配置写权限。
- 静态 packaging 验证可在开发环境尽早发现 unit/name/path drift，但只证明工件内容。
- 真实 systemd install/start/stop/restart、跨 UID socket、host Firewall 兼容性和 production
  service hardening 仍需目标 Linux Evidence，不能由本 ADR 或静态检查推导。

## 验证

- `scripts/verify-packaging.ps1` 校验 Core/Agent unit、sysusers 与 config 的必要行，以及
  Agent 不拥有 `/etc/guard` write path；
- `docs/deployment/linux-systemd.md` 提供构建、安装、`systemd-analyze verify` 与运行状态的
  操作步骤；
- `TestEnforcerRuntimeNftablesIntegration/M0-RECOVERY-004` 仅在 disposable clean target 中
  覆盖 Guard-owned socket/进程 reopen，不构成 systemd deployment 验证。

这些验证不提升 B4、C2、D4、G18 或 M0 Gate 至 `Verified`/`PASS`。

## 回滚

若需要更改 unit 名称、服务身份、binary 路径、runtime/state/config 路径或 capability，必须
同步更新 packaging、部署文档、静态验证和相应目标 Linux Evidence；不得保留旧 unit 作为
隐式兼容入口。

## 重新评估条件

- 引入 `guard-wall-server`、新的 privilege split 或不同的 init system；
- 改变 IPC socket、配置 ownership 或 persistent state layout；
- 进入正式 Linux installation、upgrade/recovery 或 Release Evidence 验证。
