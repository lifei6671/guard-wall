# Guard 分布式日志驱动主机防护系统技术方案 V0.4

## 1. 文档定位

本文承担产品范围和总体架构基线。开发期的规范性实现目标见
[Phase 1 技术规格](contracts/guard-phase-1-m0-contract-freeze-v0.3.md)，
其中明确修正本文的语义按其 §1.1 权威关系执行。实时完成度、验证证据与 Gate 结论见
[Phase 1 开发状态](development/phase-1/STATUS.md)。本文中的功能、CLI 和 Metrics
描述为交付目标；实现与发布状态由对应证据和 Gate 判定。

Guard 是一个基于日志分析、规则检测与主机防火墙执行的轻量级主动防护系统。

系统采用：

> **Standalone-first，Cluster-ready**

的演进策略。

Phase 1 交付完整单机 Agent：

```text
guard-agent
```

Phase 2 在 Agent 基础上增加：

```text
guard-server
```

本版本 V0.4 的目标不是继续增加功能，而是冻结 Phase 1 开发所需的关键语义，使实现过程中不需要开发者自行猜测：

- 日志轮转后从哪里继续读
- Detection Window 到底使用什么时间
- 同一个 IP 被多条规则 Ban 时何时真正 Unban
- Firewall Apply 失败如何重试
- Allowlist 在防火墙链中到底是 RETURN 还是 ACCEPT
- CIDR 自动 Ban 到底是否存在
- Agent 崩溃后 Ban 如何过期
- Docker / UFW / iptables-nft 如何共存

---

# 2. Phase 规划

## 2.1 Phase 1：Standalone Agent

Phase 1 交付：

```text
guard-agent
```

功能：

- CLI 启动
- YAML 配置
- systemd
- Web 管理
- SQLite
- File Source
- Journald Source
- Parser
- Detection Rule
- IPv4 / IPv6
- 手动 CIDR Ban
- Allowlist
- nftables
- iptables
- ipset
- UFW 兼容
- Docker Forward 防护
- Decision
- Target Enforcement State
- Reconciler
- 邮件告警
- Ban History
- Audit
- Metrics
- Doctor
- Maintenance Mode

Agent 不依赖 Server。

---

## 2.2 Phase 2：Managed Cluster

Phase 2 新增：

```text
guard-server
```

实现：

- Agent Enrollment
- 节点管理
- Cluster Rule
- Cluster Rule Distribution
- Cluster Evaluation
- Cluster Decision
- Cluster Ban
- Selector
- Revision
- Snapshot / Delta
- Offline Recovery

Phase 1 的：

```text
Source
Parser
SecurityEvent
Detection
Alert
Decision
Target State Resolver
Enforcer
Reconciler
```

全部继续使用。

---

# 3. Phase 1 总体数据流

完整链路：

```text
Log Source
    │
    ▼
RawRecord
    │
    ▼
Parser
    │
    ▼
SecurityEvent
    │
    ▼
Detection Engine
    │
    ▼
Alert
    │
    ▼
Decision Engine
    │
    ▼
Decision
    │
    ▼
Target State Resolver
    │
    ▼
Desired Enforcement State
    │
    ▼
Reconciler
    │
    ▼
Firewall
```

Notification 独立异步：

```text
Decision
    │
    └────────→ Notification Queue
                     │
                     ▼
                  SMTP
```

---

# 4. 核心架构原则

## 4.1 Parser ≠ Detection Rule

Parser：

> 把日志转换成 SecurityEvent。

Detection Rule：

> 判断 SecurityEvent 是否构成攻击。

禁止：

```text
Regex → Firewall
```

---

## 4.2 Alert ≠ Decision

Alert：

> 检测到了安全行为。

Decision：

> 系统决定需要采取 Ban。

---

## 4.3 Decision ≠ Firewall Rule

这是 V0.4 的重要修正。

一个目标可能同时被多条 Decision 命中。

例如：

```text
Decision A:
SSH brute force
1.2.3.4
Expires 21:00

Decision B:
Nginx scan
1.2.3.4
Expires 22:00
```

Firewall 中只需要：

```text
1.2.3.4 → DROP
```

21:00 时 Decision A 到期，但不能 Unban。

只有：

```text
所有 Active Decision 都结束
```

才解除 Firewall Ban。

因此增加：

```text
Target Enforcement State
```

---

# 5. Target Enforcement State

以下为聚合安全意图的概念模型。可编译类型、Projection 与最终 Firewall Intent 的职责
见 [Core Model](contracts/core-model.md) 和
[Decision / Enforcement Contract](contracts/decision-enforcement.md)。

概念模型：

```go
type EnforcementState struct {
    Target netip.Prefix

    DesiredBanned bool

    EffectiveUntil *time.Time

    ActiveDecisionCount int

    UpdatedAt time.Time
}
```

Resolver 计算：

```text
Target
  │
  ├── Decision A Active
  ├── Decision B Active
  └── Decision C Expired
```

结果：

```text
ActiveDecisionCount = 2
DesiredBanned = true
EffectiveUntil = max(A.ExpiresAt, B.ExpiresAt)
```

当：

```text
ActiveDecisionCount == 0
```

才：

```text
DesiredBanned = false
```

---

# 6. Decision 数据模型

下列字段展示业务概念；身份、合法状态、终止原因及持久化约束以
[Decision Contract](contracts/decision-enforcement.md#3-decision-contract) 为准。

```go
type Decision struct {
    ID string

    Target netip.Prefix

    Action DecisionAction

    RuleID  string
    AlertID string

    Source DecisionSource

    Reason string

    CreatedAt time.Time
    UpdatedAt time.Time

    ExpiresAt *time.Time

    State DecisionState

    EndReason *DecisionEndReason

}
```

---

# 7. Decision 状态机

状态：

```text
Active
Revoked
Expired
```

状态流：

```text
                ┌─────────────┐
                │   Active    │
                └──────┬──────┘
                       │
          ┌────────────┴────────────┐
          │                         │
     ExpiresAt                 Explicit Revoke
          │                         │
          ▼                         ▼
      Expired                   Revoked
```

Decision 表示安全意图，不承载 Firewall Apply、Revoke、Probe 或重试结果。创建后直接为
`Active`；只允许 `Active → Expired` 或 `Active → Revoked`，终态不可复活。

---

# 8. Decision EndReason

终止原因独立于 State。

例如：

```text
expired
manual
manual_replace
rule_disabled
system_cleanup
```

例如：

```text
State = Revoked
EndReason = manual
```

表示人工解除。

---

# 9. Reconcile failure-domain 重试

Retry 只属于 Reconcile，不属于 Decision。Infrastructure、Policy、Target 各自持久化
attempt、last error、next attempt 和状态；一个 domain 的失败不消耗另一个 domain 的预算。
三个 retry key 固定为：

```text
Infrastructure = InfrastructureRevision + RetryEpoch
Policy = PolicyRevision + RetryEpoch
Target = CanonicalTarget + TargetEnforcementGeneration + RetryEpoch
```

每个 revision/generation key 最多执行一次首次 mutation 与五次自动重试，退避固定为：

```text
1s
5s
30s
5m
15m
```

每次外部 mutation 前必须先持久化 `Applying` 与 attempt。crash、timeout 或结果不确定时，
对应 attempt 保持已消耗，Observed domain 标记为 `Unknown`。`Unknown` 结果必须先通过该
domain 的权威 Probe/Snapshot 确认后，才允许建立新的 mutation plan，不得盲目重放原 mutation。

普通 Reconcile、Probe、Agent restart 与 Backend health flap 不重置预算。达到上限后，
对应 domain 进入 `Degraded`，但 Decision 和 Desired intent 保持不变。`OwnershipConflict`、
不支持能力与非法 Plan 为 non-retryable，直接进入 `Degraded`，不得以自动重试掩盖设计错误。

---

# 10. Reconcile Retry 恢复

管理员 Retry 只能为指定 failure domain 创建新的 `RetryEpoch`，并与 Critical Audit 在同一
事务中提交；不得直接修改 Decision。Phase 1 CLI 为：

```bash
guard-agent reconcile retry infrastructure
guard-agent reconcile retry policy
guard-agent reconcile retry target <canonical-target>
```

Backend 从 unhealthy → healthy 只触发立即 Probe。若该 domain 预算已经耗尽，它保持
`Degraded`，直到受影响 revision/generation 前进或管理员显式创建新的 RetryEpoch。

---

# 11. 同一 Rule + Target 重复命中

Phase 1 固定：

> 同一 Rule + 同一 Target 存在 Active Decision 时，不创建新的 Decision，也不自动续期。

例如：

```text
20:00
SSH Rule
BAN until 21:00

20:30
再次触发 SSH Rule
```

结果：

```text
仍然到 21:00
```

只更新：

```text
last_triggered_at
suppressed_alert_count
```

不延长 Ban。

未来 Progressive Ban 单独设计。

---

# 12. 不同 Rule 命中同一 Target

允许多个 Decision。

例如：

```text
ssh-bruteforce
1.2.3.4
Expires 21:00

nginx-404
1.2.3.4
Expires 22:00
```

Target State：

```text
DesiredBanned = true
EffectiveUntil = 22:00
```

只有 22:00 后才真正 Revoke Firewall Entry。

---

# 13. CIDR 支持边界

Phase 1 数据模型原生支持：

```text
IPv4
IPv6
IPv4 CIDR
IPv6 CIDR
```

内部统一：

```go
netip.Prefix
```

---

# 14. Phase 1 自动 Detection 只 Ban 单 IP

Phase 1 Detection Rule：

```text
source.ip
```

触发后：

IPv4：

```text
1.2.3.4 → 1.2.3.4/32
```

IPv6：

```text
2001:db8::1 → 2001:db8::1/128
```

Phase 1 不支持：

```text
单 IP Alert
自动升级成 /24 或 /64
```

---

# 15. Phase 1 CIDR Ban 仅支持手动

CLI：

```bash
guard-agent ban 30.2.2.0/24 --duration 24h
```

Web：

```text
Manual Ban
Target:
30.2.2.0/24
```

Allowlist 同样允许：

```text
10.0.0.0/8
2001:db8::/64
```

---

# 16. 自动 CIDR Ban 后置

未来可以实现：

```text
CIDR grouping

distinct source IP

subnet aggregation
```

例如：

```text
同一 /24
10 分钟
20 个不同 source.ip
```

才：

```text
BAN /24
```

不进入 Phase 1。

---

# 17. SecurityEvent 最小模型

继续保持 V0.3 设计，不引入 ECS 风格复杂字段。

```go
type SecurityEvent struct {
    ID        string
    Timestamp time.Time

    EventType string

    Source Endpoint
    Target Endpoint

    User *UserInfo
    HTTP *HTTPInfo

    Service string
    NodeID  string

    Labels map[string]string
    Fields map[string]any
}
```

---

# 18. SecurityEvent 字段职责

系统自动生成：

```text
id
node_id
source_id
parser_id
observed_at
```

Parser 允许映射：

```text
event_type

source.ip
source.port

target.ip
target.port

user.name

http.method
http.path
http.status

service

labels
fields
```

---

# 19. Detection 时间语义

Phase 1 Sliding Window 明确使用：

```text
ObservedAt
```

即：

> Guard 实际观察并处理该 RawRecord 的时间。

不使用日志中用户提供的时间作为实时检测窗口依据。

---

# 20. Timestamp 与 ObservedAt

用户可解析：

```text
Timestamp
```

表示：

> 日志声称的事件发生时间。

Guard 自动生成：

```text
ObservedAt
```

表示：

> Guard 实际观察到该日志的时间。

Detection Window 使用：

```text
ObservedAt
```

---

# 21. 采用 ObservedAt 的原因

避免：

- 日志时间格式错误
- 时区错误
- 攻击者伪造时间
- Journald 延迟
- File rotate 重读
- 日志乱序
- 未来时间
- 极旧时间

影响实时 Ban 行为。

---

# 22. Detection Window

内部：

```text
RuleID + GroupKey
```

维护：

```text
deque<ObservedAt>
```

例如：

```text
ssh-bruteforce
+
1.2.3.4
```

---

# 23. Window Persistence

Phase 1：

```text
只存内存
```

Agent restart：

```text
window reset
```

这是明确已知限制。

不因为该限制引入高频 SQLite checkpoint。

---

# 24. File Source Delivery Semantics

Phase 1 File Source 在建立稳定 SourcePosition 后，内部投递采用：

```text
at-least-once
```

允许 Agent crash 后少量日志重复读取。

日志生产端 destructive truncate、删除及超过 Guard lag 的轮转可能造成外部日志丢失。
检测与审计要求、copytruncate fast-regrow 盲区见
[Phase 1 技术规格 §8](contracts/guard-phase-1-m0-contract-freeze-v0.3.md)。

不保证：

```text
exactly-once
```

---

# 25. File Source Position Identity

File Source 使用：

```text
source_id
inode
byte offset
```

标识读取位置。

不使用：

```text
hash(log line)
```

做全局去重。

因为两条完全相同的日志可能是两个真实事件。

---

# 26. File Source Contract

持久化：

```text
configured_path
resolved_path
inode
offset
last_size
last_read_at
```

---

# 27. File 正常 Append

```text
inode unchanged
size >= offset
```

继续从：

```text
offset
```

读取。

---

# 28. copytruncate

如果：

```text
inode unchanged
size < offset
```

视为：

```text
truncate
```

执行：

```text
offset = 0
```

---

# 29. rename + create

Path inode 改变：

```text
旧 inode
继续读取到 EOF

新 inode
从 0 开始
```

---

# 30. File Restart Recovery

如果保存 inode 仍存在：

```text
resume saved offset
```

如果 inode 不存在：

使用：

```yaml
resume_policy: end
```

支持：

```text
beginning
end
```

默认：

```text
end
```

优先防止历史日志 replay 造成误 Ban。

---

# 31. Symlink

保存：

```text
configured_path
resolved_path
inode
```

每次轮转重新 resolve。

---

# 32. Offset Flush

不每条日志写 SQLite。

例如：

```text
5s
或
N records
```

周期持久化。

---

# 33. Journald Source Contract

Phase 1 必须和 File Source 一样定义恢复语义。

持久化：

```text
source_id
cursor
last_read_at
```

---

# 34. Journald Restart

如果保存 cursor 有效：

```text
resume after cursor
```

如果 cursor 已因 journal vacuum 等原因失效：

使用：

```yaml
resume_policy: now
```

支持：

```text
now
head
```

默认：

```text
now
```

避免重放历史安全事件。

---

# 35. Source

Phase 1 支持：

```text
File
Journald
```

RawRecord：

```go
type RawRecord struct {
    SourceID   string
    ObservedAt time.Time
    Content    []byte
    Metadata   map[string]string
}
```

---

# 36. Parser

支持：

```text
JSON
logfmt
Grok
Regex
```

Parser 必须绑定 Source。

---

# 37. Parser Pre-filter

执行顺序：

```text
Source Scoped Parser
        ↓
Cheap Matcher
        ↓
Compiled Parser
```

支持：

```text
contains
prefix
```

禁止：

```text
每条日志 × 全部 Parser
```

---

# 38. Parser Runtime 限制

建议默认：

```text
max log line            64 KiB
max regex               4 KiB
max capture fields      32
max parser/source       100
max Grok expansion      configurable
```

---

# 39. Regex

使用：

```text
Go RE2
```

不支持危险 PCRE 回溯特性。

Regex 只在 Rule 保存时：

```text
compile once
```

Runtime 复用。

---

# 40. CEL

用于：

```text
字段过滤
Boolean 表达式
```

资源控制：

```text
Static Cost Estimate
Runtime Cost Limit
Wall-clock Safety Timeout
```

优先使用：

```text
Cost Limit
```

---

# 41. Detection Rule

示例：

```yaml
apiVersion: guard/v1
kind: DetectionRule

metadata:
  name: ssh-bruteforce

match:
  event_type: auth.login_failed

group_by:
  - source.ip

window: 10m

condition:
  count:
    gte: 5

decision:
  action: ban
  duration: 1h
```

---

# 42. Phase 1 聚合能力

支持：

```text
count
distinct_count
```

不支持：

```text
rate
sequence
CEP
复杂关联
CIDR 自动关联
```

---

# 43. distinct_count

使用精确集合。

例如：

```yaml
condition:
  distinct_count:
    field: user.name
    gte: 10
```

Phase 1 不引入 HLL。

---

# 44. distinct_count 容量限制

例如：

```text
max_distinct_values_per_group = 1024
```

达到上限：

```text
group saturated
```

不再新增 distinct value。

同时记录：

```text
guard_rule_distinct_saturated_total
```

---

# 45. Active Group 限制

必须设置：

```text
global max groups
per-rule max groups
```

触顶：

> 默认拒绝创建新 group。

不采用攻击者可利用的无界 LRU 淘汰。

记录：

```text
guard_rule_group_overflow_total
```

并在 Web 产生 Warning。

---

# 46. Rule Exclusion

新增通用：

```text
exclude
```

机制。

例如：

```yaml
exclude:
  source:
    allowlisted: true

  http:
    paths:
      - /internal/*
      - /guard/*
```

用于避免：

- 管理页面
- Health Check
- 内部 API
- Trusted Proxy
- 特定业务路径

误触发。

---

# 47. Guard 管理面自锁

Guard 不硬编码：

```text
/guard
```

因为反代路径由用户决定。

如果用户通过 Nginx 暴露 Guard Web，且同时监控该 Nginx 日志：

Web 应提示：

> 请确认 Guard 管理路径是否加入相关 Detection Rule 的 Exclusion。

Allowlist 仍是管理 IP 最可靠的保护方式。

---

# 48. Nginx Client IP 信任边界

内置 Nginx Parser 默认以日志中的：

```text
$remote_addr
```

为来源字段。

如果用户配置：

```text
real_ip_header
X-Forwarded-For
```

必须正确设置：

```text
set_real_ip_from
```

Guard 不负责验证外部代理信任链。

文档必须明确：

> 如果日志中的 Client IP 可被客户端任意伪造，Guard 可能被利用对第三方实施误 Ban。

---

# 49. IP 校验

Parser 输出：

```text
source.ip
target.ip
```

必须通过：

```go
netip.ParseAddr
```

CIDR：

```go
netip.ParsePrefix
```

严格校验。

---

# 50. Allowlist 语义

Allowlist 表示：

> Guard 不对目标执行动态 Ban。

不是：

> Firewall 强制 ACCEPT。

---

# 51. Firewall Allowlist Contract

Guard Chain 必须遵循：

```text
1. allowlist match
       ↓
     RETURN

2. blacklist match
       ↓
      DROP

3. otherwise
       ↓
     RETURN
```

绝不能：

```text
allowlist → ACCEPT
```

---

# 52. nftables Chain

概念：

```text
INPUT
  ↓
guard_input
       │
       ├── allowlist → return
       ├── blacklist → drop
       └── return
```

Forward：

```text
FORWARD
  ↓
guard_forward
```

同样语义。

---

# 53. Protected Targets

系统自动保护：

```text
127.0.0.0/8
::1/128
```

Phase 2 再自动保护：

```text
当前 Guard Server 控制面连接地址
```

不自动白名单全部 Cluster Node。

---

# 54. 当前 SSH 管理来源

Doctor 在存在：

```text
SSH_CONNECTION
SSH_CLIENT
```

时检测来源。

不存在时不推测。

例如 Ansible、systemd、cron 场景可能无法获取。

提示：

```text
Current SSH source 203.0.113.20
is not in Guard Allowlist.
```

只建议，不自动加入。

---

# 55. Firewall Backend

内部区分：

```text
nftables-native

iptables-nft

iptables-legacy
```

ipset 是 iptables 后端能力。

UFW 是环境兼容层。

---

# 56. FirewallCapabilities

```go
type FirewallCapabilities struct {
    IPv4 bool
    IPv6 bool
    CIDR bool

    NativeSet bool
    NativeTimeout bool
    CrashSafeExpiry bool

    HostInput bool
    Forward bool

    Docker bool
}
```

---

# 57. Auto Detection

默认：

```yaml
firewall:
  backend: auto
```

Probe：

```text
nft CLI
nf_tables capability

iptables binary
iptables -V

iptables-nft / legacy

ip6tables

ipset

UFW state

Docker state

Docker firewall backend

IPv4

IPv6

FORWARD
```

---

# 58. Backend 选择

优先：

```text
nftables-native
      ↓
iptables + ipset
      ↓
iptables fallback
```

同一时间只使用一条主要管理路径。

禁止：

```text
nftables native
+
iptables-nft
```

双写。

---

# 59. nftables

建立：

```text
table inet guard
```

维护：

```text
guard_blacklist_v4
guard_blacklist_v6

guard_allowlist_v4
guard_allowlist_v6
```

使用 Set，而不是一个 IP 一条 Rule。

---

# 60. iptables + ipset

使用：

```text
hash:net
```

支持：

```text
IP
CIDR
```

Chain：

```text
GUARD_INPUT
GUARD_FORWARD
```

---

# 61. iptables fallback

无 ipset 时允许：

```text
iptables native dynamic rules
```

但是必须标记：

```text
NativeTimeout = false
CrashSafeExpiry = false
```

Doctor/Web 同时显示：

```text
Performance Warning
No Crash-safe Expiration
```

---

# 62. iptables fallback Crash 语义

如果 Agent 停止且不再启动：

> 动态 Ban 规则可能永久留在 iptables 中。

这是明确的 Backend 能力退化。

不为了这一点增加第二个清理 Daemon。

---

# 63. Firewall Native Timeout

支持的 Backend：

例如：

```text
nftables
ipset
```

Native timeout：

```text
Decision EffectiveUntil
+
SafetyGrace
```

SafetyGrace 默认建议：

```text
5m
```

---

# 64. Expiration 权威

始终：

```text
Decision / Enforcement State
= Source of Truth

Firewall timeout
= failsafe
```

不是反过来。

---

# 65. Reconciler

默认：

```text
60s
```

负责：

```text
missing desired entry
expired undesired entry
unexpected Guard-owned entry
backend drift
```

---

# 66. Reconciler 不处理无限失败重试

Reconciler 不得无限重放已耗尽预算的 domain。达到上限后对应 domain 保持 `Degraded`；
普通周期、Probe、Agent restart 与 Backend 状态抖动都不重置预算。恢复由 revision/generation
前进或管理员创建新的 RetryEpoch 控制，且不能改写 Decision。

---

# 67. Docker

Phase 1 必须识别：

```text
Host INPUT
Forwarded Traffic
```

配置：

```yaml
firewall:
  protect:
    host: true
    forwarded: true
```

---

# 68. Docker + iptables

如果 Docker 使用 iptables：

优先利用：

```text
DOCKER-USER
```

挂接：

```text
GUARD_FORWARD
```

Guard 不修改：

```text
DOCKER
DOCKER-FORWARD
```

内部规则。

---

# 69. Docker + nftables

Guard 创建自己的：

```text
inet guard
```

不修改 Docker 创建的 nftables tables。

Doctor 必须识别 Docker backend。

---

# 70. Kubernetes

Phase 1 不保证 Kubernetes / CNI enforcement 完整正确。

Doctor 检测到 Kubernetes 环境：

```text
WARN
```

不作为 Phase 1 正式支持场景。

---

# 71. Maintenance Mode

提供：

```bash
guard-agent maintenance enable
guard-agent maintenance disable
guard-agent maintenance status
```

Web 同样支持。

---

# 72. Maintenance Mode 行为

开启时：

继续：

```text
Source
Parser
Detection
Alert
Web
Metrics
```

暂停：

```text
New Firewall Apply

Automatic Revoke

Reconciler Repair
```

也就是说：

> 检测继续，执行暂停。

Web 必须显示醒目警告。

---

# 73. Maintenance Mode 安全

进入 Maintenance Mode：

必须 Audit。

退出：

```text
立即执行一次完整 Reconcile
```

使 Actual State 收敛至 Desired State。

---

# 74. Manual Firewall 修改

正常模式下：

> 手工修改 Guard-owned Chain/Set 会被 Reconciler 恢复。

需要人工调试 Firewall 时，必须进入：

```text
Maintenance Mode
```

这是正式运维路径。

---

# 75. Service Install

支持：

```bash
guard-agent service install
guard-agent service uninstall
```

以及：

```text
start
stop
restart
status
```

---

# 76. service uninstall 语义

默认：

```text
stop service

remove systemd unit

remove Guard-owned Firewall Hook / Table / Chain / Set

preserve:
  /etc/guard
  /var/lib/guard
```

即：

> 卸载服务，但保留数据。

---

# 77. Purge

另外提供：

```bash
guard-agent purge
```

用于：

```text
remove service

remove Guard firewall objects

remove config

remove SQLite data
```

必须显式二次确认。

---

# 78. Admin

Phase 1：

```text
single admin
```

密码：

```text
Argon2id
```

---

# 79. Password Reset

CLI：

```bash
guard-agent admin reset-password
```

支持：

```bash
guard-agent admin reset-password --stdin
```

需要：

```text
root
或
Guard DB write permission
```

---

# 80. Web Security

默认：

```text
127.0.0.1:8080
```

认证：

```text
HttpOnly Cookie
SameSite
CSRF
```

---

# 81. Remote HTTP

非 loopback 明文 HTTP 必须显式：

```yaml
web:
  security:
    allow_remote_http: true
```

启动打印：

```text
SECURITY WARNING
```

Guard 不强制自己实现 TLS。

支持前置：

```text
Caddy
Nginx
```

---

# 82. Notification

Phase 1：

```text
SMTP Email
```

Rule 显式控制是否告警。

---

# 83. SMTP TLS

支持：

```text
none
starttls
tls
```

即：

```text
Plain SMTP
STARTTLS
SMTPS
```

默认：

```text
certificate verification = true
```

允许配置：

```text
custom CA
```

不默认提供：

```text
skip TLS verification
```

Web 开关。

---

# 84. Notification Queue

SQLite 持久化：

```text
notification_jobs
```

状态：

```text
Pending
Sending
Sent
Retry
Failed
```

---

# 85. Notification Pipeline

```text
Decision
   ├────→ Enforcement
   │
   └────→ Notification Queue
                 ↓
               SMTP
```

SMTP 失败不得影响 Ban。

---

# 86. Notification Cooldown

Key：

```text
RuleID + Target
```

默认：

```text
10m
```

避免邮件风暴。

---

# 87. Built-in SSH

支持：

```text
Failed password
Invalid user
Authentication failure
```

统一：

```text
event_type = auth.login_failed
```

默认：

```text
5 / 10m
Ban 1h
```

---

# 88. Built-in Nginx Parser

只做一个 Access Parser：

```text
event_type = http.request
```

输出：

```text
source.ip
http.method
http.path
http.status
```

---

# 89. Nginx Rules

提供：

```text
401
403
404
```

建议默认：

```text
401:
10 / 5m
30m

403:
20 / 5m
30m

404:
100 / 5m
30m
```

均可修改。

---

# 90. 404 不默认排除整个静态目录

不默认排除：

```text
/static/*
/assets/*
```

因为扫描行为也可能发生在这些路径。

依靠：

```text
合理阈值
+
Rule Exclusion
```

控制误报。

---

# 91. Decision History

保留：

```text
Active
Expired
Revoked
```

以及：

```text
EndReason
```

Web 显示：

```text
Target

Rule

Reason

Created

Expires

Remaining

State

End Reason

Backend
```

---

# 92. Audit

Audit 与 Decision History 分离。

记录：

```text
Login
Password Change

Source Change
Parser Change
Rule Change

Allowlist Change

Manual Ban
Manual Unban

Automatic Ban
Automatic Revoke

Maintenance Mode

Firewall Error

Notification Change
```

应用 API：

```text
append-only
```

不宣称抵御 root 本地篡改。

---

# 93. 数据 Retention

默认建议：

```text
Decision History       90d
Alerts                 30d
Audit                   90d
Notification History   30d
Failed Notifications   30d
```

全部可配置。

---

# 94. Retention Cleanup

每天执行一次：

```text
retention cleanup
```

SQLite 删除后不要求每次 VACUUM。

采用低频：

```text
incremental vacuum
```

或等价策略。

不允许高频 full VACUUM 影响 Agent。

---

# 95. SQLite

使用：

```text
WAL
busy_timeout = 5000ms
```

核心原则：

```text
Short Transaction
```

禁止：

```text
DB transaction
  ↓
SMTP
  ↓
Firewall
  ↓
Commit
```

---

# 96. Audit Write

Audit：

```text
async batch
```

例如：

```text
100 records
或
1s
```

flush。

---

# 97. Metrics Label 基数约束

禁止将以下字段作为 Prometheus Label：

```text
source_ip
target
username
request_id
path
group_key
CIDR
```

允许：

```text
source_type
parser
rule
backend
domain
result
state
status
```

所有 Metrics Label 必须是低基数。

---

# 98. Metrics

至少：

```text
guard_source_records_total

guard_source_errors_total

guard_parser_evaluations_total

guard_parser_matches_total

guard_parser_errors_total

guard_parser_duration_seconds

guard_events_total

guard_rule_evaluations_total

guard_rule_duration_seconds

guard_rule_group_overflow_total

guard_rule_distinct_saturated_total

guard_alerts_total

guard_decisions_total

guard_reconcile_mutations_total{domain,result}

guard_reconcile_duration_seconds{domain,result}

guard_reconcile_unknown_results_total{domain}

guard_reconcile_degraded{domain}

guard_firewall_probes_total{backend,result}

guard_active_bans

guard_enforcer_errors_total

guard_notification_total

guard_notification_errors_total

guard_queue_depth

process_cpu_seconds_total

process_resident_memory_bytes
```

Reconcile Metrics 的 `domain` 只能是 `infrastructure|policy|target`；`result` 和
`backend` 必须使用 Schema 中的有限枚举。CanonicalTarget、DecisionID、错误文本和 Retry key 禁止进入 label。

---

# 99. Health

```text
/health
/ready
```

检查：

```text
SQLite

Source

Firewall Backend

Reconciler

Notification
```

SMTP 不通：

```text
Degraded
```

不导致 Agent：

```text
Not Ready
```

---

# 100. Performance 目标

基准：

```text
1 vCPU
512MB - 1GB RAM
```

Idle：

```text
CPU 接近 0%
```

1,000 lines/sec：

```text
Average CPU < 10%

P95 CPU < 25%

RSS < 100MB
```

作为开发验收指标，不作为正式 SLA。

---

# 101. Queue

所有内部 Channel：

```text
bounded
```

必须有：

```text
queue depth
overflow
backpressure
dropped
```

指标。

---

# 102. Phase 1 SQLite 表

至少：

```text
sources
source_offsets
journal_cursors

parsers
parser_versions

detection_rules
rule_versions

alerts

decisions

enforcement_states

allowlists

users

notification_configs
notification_jobs
notification_history

audit_logs

settings
```

---

# 103. Parser / Rule Update

流程：

```text
Edit
 ↓
Validate
 ↓
Compile
 ↓
Resource Check
 ↓
Test
 ↓
Persist
 ↓
Atomic Swap
```

失败：

```text
旧 Active Version 保持运行
```

---

# 104. CLI

Phase 1：

```text
guard-agent run

guard-agent doctor

guard-agent service install
guard-agent service uninstall
guard-agent service start
guard-agent service stop
guard-agent service restart
guard-agent service status

guard-agent maintenance enable
guard-agent maintenance disable
guard-agent maintenance status

guard-agent admin reset-password

guard-agent config validate

guard-agent parser test

guard-agent rule test

guard-agent ban
guard-agent unban
guard-agent list-bans

guard-agent allowlist add
guard-agent allowlist remove

guard-agent reconcile retry infrastructure
guard-agent reconcile retry policy
guard-agent reconcile retry target <canonical-target>

guard-agent purge

guard-agent version
```

---

# 105. Web 页面

```text
Dashboard

Sources

Parsers

Detection Rules

Alerts

Bans

Notifications

Audit Logs

Settings
```

Settings：

```text
General

Account

Firewall

Allowlist

Notification

Retention

System
```

---

# 106. Parser Playground

输入：

```text
Parser
Sample Log
```

输出：

```text
Match Status

Captured Fields

SecurityEvent

ObservedAt

Error
```

批量：

```text
Matched
Unmatched
Invalid
```

---

# 107. Rule Test

显示：

```text
Rule Match

Group Key

Count

Distinct Count

Threshold

Would Trigger

Resource Cost
```

---

# 108. Doctor

检测：

```text
OS
Arch
Kernel

Privileges

nftables

iptables
iptables backend

ipset

UFW

Docker

Docker firewall backend

FORWARD

IPv4

IPv6

journald

filesystem

DB

port

SSH client
```

---

# 109. Phase 1 开发前 Contract

正式编码前冻结以下 Contract。

## Contract A — File Source

```text
inode
offset
copytruncate
rename
restart
symlink
resume policy
at-least-once
```

---

## Contract B — Journald Source

```text
cursor
restart
cursor unavailable
resume policy
```

---

## Contract C — SecurityEvent

```text
event_type

source
target

user
http

service
labels
fields

ObservedAt
Timestamp
```

---

## Contract D — Decision

```text
state machine

duplicate trigger

end reason

expiration
```

---

## Contract E — Enforcement State

```text
multiple Decision → single Target

desired state

effective until

unban condition
```

---

## Contract F — Firewall

```text
nftables

iptables-nft

iptables-legacy

ipset

UFW

Docker

INPUT

FORWARD

allowlist RETURN

blacklist DROP

native timeout

crash-safe expiry
```

---

## Contract G — Resource Limits

```text
regex
Grok
CEL

group cardinality

distinct cardinality

window size

queue

metrics labels
```

---

# 110. Phase 1 里程碑

## M1 Runtime

```text
CLI
Config
SQLite
Web
Auth
systemd
Doctor
Metrics
Maintenance framework
```

---

## M2 Sources

```text
File Contract

Journald Contract

File Source

Journald

offset

cursor

rotate

restart
```

---

## M3 Parser

```text
JSON
logfmt
Regex
Grok
CEL

Parser Playground

Limits

Version
```

---

## M4 Detection

```text
SecurityEvent

ObservedAt

count

distinct_count

Sliding Window

Group Limits

Rule Exclusion

Alert
```

---

## M5 Decision Model

优先开发：

```text
Decision State Machine

Duplicate Semantics

Target Enforcement State

Expiration

Reconcile failure-domain Retry
```

这一阶段先不接真实 Firewall。

---

## M6 Firewall

```text
Probe

nftables

iptables-nft

iptables-legacy

ipset

UFW

Docker

INPUT

FORWARD

Allowlist

Manual CIDR
```

---

## M7 Reconciliation

```text
Desired State

Actual State

Native Timeout

Safety Grace

Retry Interaction

Maintenance
```

---

## M8 Notification

```text
SMTP

STARTTLS

SMTPS

Persistent Queue

Retry

Cooldown
```

---

## M9 Built-in Rules

```text
SSH

Nginx Access

401
403
404
```

---

## M10 Productization

```text
Dashboard

Audit

Retention

Health

Benchmark

Installer

Upgrade Migration

Documentation
```

Phase 1 Release。

---

# 111. Phase 1 测试矩阵

至少覆盖：

```text
Ubuntu + nftables

Ubuntu + UFW

Debian + iptables-nft

iptables-legacy

iptables without ipset

IPv4

IPv6

manual CIDR

Docker published port

File copytruncate

File rename/create

Journald cursor restore

Agent crash

Firewall apply failure

Multiple Decisions same target

Maintenance Mode
```

---

# 112. Phase 2 基础架构

Phase 2 新增：

```text
guard-server
```

Agent：

```text
mode: managed
```

---

# 113. Cluster Scope

保持：

```text
Management Scope

Evaluation Scope

Enforcement Scope
```

独立。

---

# 114. Phase 2 Event

Agent 不上传 Raw Log。

只上传：

```text
Normalized SecurityEvent
```

采用：

```text
batch
compression
gRPC
```

初版不做 Agent Partial Aggregation。

---

# 115. Phase 2 TLS

```text
TLS Required
mTLS Optional
```

默认：

```text
TLS + Agent Credential
```

---

# 116. Enrollment

首次：

```text
Enrollment Token
```

换取：

```text
Agent ID

Random Agent Credential
```

Server 只保存 Credential Hash。

Token 有效期、使用次数等在 Phase 2 单独方案冻结。

---

# 117. Phase 2 已知待设计问题

不在 Phase 1 阻塞：

```text
Local Allowlist vs Cluster Decision

Cluster Window persistence

Enrollment Token lifecycle

Standalone → Managed migration

Server restart behavior

Cluster Event buffering
```

Phase 1 完成后单独编写 Cluster Technical Design。

---

# 118. 正式 ADR

本节 ADR-001–ADR-024 是本文内的架构决策摘要编号。具体实现决策、接受状态与验证边界
记录在 [ADR 目录](adr/)；引用实现决策时使用对应文件的四位编号与标题。

## ADR-001

Agent 从 Phase 1 即为最终运行单元。

## ADR-002

Parser 与 Detection 分离。

## ADR-003

Alert 与 Decision 分离。

## ADR-004

Decision 与 Firewall Entry 分离。

## ADR-005

多个 Decision 可共同决定一个 Target Enforcement State。

## ADR-006

只有 Target 不再存在任何有效 Decision 时才真正 Unban。

## ADR-007

同 Rule + Target 重复触发默认不续期。

## ADR-008

Phase 1 自动 Rule 只能 Ban 单 IP。

## ADR-009

Phase 1 CIDR Ban 仅支持手动操作与 Allowlist。

## ADR-010

Detection Window 使用 Guard ObservedAt。

## ADR-011

File/Journald Source 在建立稳定 SourcePosition 后，内部投递采用 at-least-once 语义。
外部日志截断、删除及轮转的丢失边界见
[Phase 1 技术规格 §8](contracts/guard-phase-1-m0-contract-freeze-v0.3.md)。

## ADR-012

SecurityEvent 保持最小必要标准化。

## ADR-013

Allowlist 在 Guard Firewall Chain 中必须 RETURN，禁止 ACCEPT。

## ADR-014

Guard 只管理 Guard-owned Firewall 对象。

## ADR-015

iptables fallback 明确不具备 crash-safe expiry。

## ADR-016

Firewall Native Timeout 是 failsafe，不是 Source of Truth。

## ADR-017

Reconcile Retry 不属于 Decision；耗尽预算的 domain 保持 Degraded，直到 revision/generation
前进或管理员显式创建新的 RetryEpoch。

实现决策与验证边界见
[ADR-0011：Reconcile Retry 与结果不确定性边界](adr/0011-reconcile-retry-and-unknown-result-boundary.md)。

## ADR-018

Docker Host/Forwarded Traffic 均进入 Phase 1 防护模型。

## ADR-019

Maintenance Mode 是人工 Firewall 调试的正式路径。

## ADR-020

Rule Engine 必须有 CPU、Memory 和 Cardinality 限制。

## ADR-021

Prometheus Metrics 禁止高基数 Label。

## ADR-022

Notification 不得阻塞安全执行链。

## ADR-023

TLS Required，mTLS Optional。

## ADR-024

Phase 2 不阻塞 Phase 1 核心实现。

---

# 119. Phase 1 最终形态

```text
                         guard-agent
                              │
        ┌─────────────────────┼────────────────────┐
        │                     │                    │
       Web                   CLI                systemd
        │
        └─────────────────────┬────────────────────┘
                              │
                            Source
                              │
                            Parser
                              │
                       SecurityEvent
                              │
                      Detection Engine
                              │
                            Alert
                              │
                          Decision
                              │
                    Target State Resolver
                              │
                Desired Enforcement State
                              │
                        Reconciler
                              │
                          Firewall

Decision ───────────────→ Notification Queue
                              │
                             SMTP
```

---

# 120. V0.4 开发准入结论

V0.4 不再要求在正式开发前继续增加业务能力。

进入开发前只需要针对以下 Contract 做最终代码级接口冻结：

```text
File Source

Journald Source

SecurityEvent

Decision State Machine

Target Enforcement State

Firewall Capability

Resource Limits
```

这些 Contract 一旦冻结，Phase 1 可以按 M1～M10 顺序进入开发。

本版本之后，如果评审再次提出：

```text
更多检测类型
更多通知渠道
更多 Cluster 能力
Threat Intelligence
GeoIP
机器学习
```

原则上不再进入 Phase 1。

只有：

> 影响现有功能正确性、安全性、一致性或可运维性的缺陷

才允许继续修改 Phase 1 Technical Design。
