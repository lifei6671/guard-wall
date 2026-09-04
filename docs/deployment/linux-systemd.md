# Linux systemd 部署

Phase 1 的部署名称与逻辑角色固定映射如下：

| 逻辑角色 | 发布二进制 | systemd unit |
| --- | --- | --- |
| Privileged Enforcer | `guard-wall-core` | `guard-wall-core.service` |
| Unprivileged Agent | `guard-wall-agent` | `guard-wall-agent.service` |

`guard-wall-server` 预留给后续阶段；Phase 1 不安装、启用或实现该服务。源码目录和内部逻辑角色继续使用
`guard-enforcer`、`guard-agent`，不改变固定 IPC socket `/run/guard/enforcer.sock` 或 `guard` 运行身份。

## 构建

在 Linux 构建机的仓库根目录执行：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o guard-wall-core ./cmd/guard-enforcer
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o guard-wall-agent ./cmd/guard-agent
```

## 安装

以下命令需要 root。它们创建固定的无特权 `guard` 身份、安装两份二进制、配置和 systemd 工件；不会创建或修改
`guard-wall-server`。

```sh
install -d -m 0755 /usr/lib/sysusers.d /etc/guard /etc/systemd/system
install -m 0644 packaging/systemd/guard-wall.sysusers.conf /usr/lib/sysusers.d/guard-wall.conf
systemd-sysusers /usr/lib/sysusers.d/guard-wall.conf
install -d -o root -g guard -m 0750 /etc/guard
install -m 0640 -o root -g guard packaging/config/guard-wall.yaml /etc/guard/guard-wall.yaml
install -m 0755 guard-wall-core /usr/local/bin/guard-wall-core
install -m 0755 guard-wall-agent /usr/local/bin/guard-wall-agent
install -m 0644 packaging/systemd/guard-wall-core.service /etc/systemd/system/guard-wall-core.service
install -m 0644 packaging/systemd/guard-wall-agent.service /etc/systemd/system/guard-wall-agent.service
systemctl daemon-reload
systemctl enable --now guard-wall-core.service guard-wall-agent.service
```

`StateDirectory=guard` and `StateDirectoryMode=0750` create `/var/lib/guard` as `guard:guard 0750`; the Agent may write only that directory. The configuration
directory and configuration file remain `root:guard 0750` and `root:guard 0640`, so the Agent can read but cannot rewrite them.
The Core runs as root with primary group `guard`; `RuntimeDirectory=guard` and `RuntimeDirectoryMode=0750` create
`/run/guard` as `root:guard 0750` before sandboxing, then the Core validates that directory and owns the socket as `root:guard 0660`.

## 验证与诊断

```sh
systemd-analyze verify /etc/systemd/system/guard-wall-core.service /etc/systemd/system/guard-wall-agent.service
systemctl status guard-wall-core.service guard-wall-agent.service
ps -eo user,args | grep '[g]uard-wall-'
```

Linux limits the kernel `comm` field to 15 bytes, so `guard-wall-agent` may be truncated in views that display only `comm`.
Use `systemctl` or `ps ... args` to inspect the full executable name. `SyslogIdentifier` keeps the full names in service logs.

## 升级与卸载边界

Stop both units before replacing binaries, then run `systemctl daemon-reload` and restart Core before Agent. Removing these units
does not remove `/etc/guard` or `/var/lib/guard`; data and configuration require an explicit, separately reviewed purge process.
