# GuardWall 代码审查标准与流程

> 本文件是 GuardWall 的代码审查操作规范，定义审查基线、风险分级、项目检查点、
> 复审闭环和证据要求。它不创建新的产品 Contract、公共接口、配置、数据库结构、
> Work Package 状态或 Gate 结论。

---

## 0. 定位、范围与权威关系

本规范适用于工作区 Diff、暂存区 Diff、单个 Commit、Commit Range 和 Pull Request。
审查默认只读；除非用户明确要求修复，审查者不得修改文件、暂存区、任务状态或 Gate。

### 0.1 权威来源

| 领域 | 权威来源 |
|---|---|
| 协作规则、变更边界、验证选择 | 当前路径适用的 `AGENTS.md` / `AGENTS.override.md` |
| 产品范围与总体架构 | `docs/guard-distributed-log-driven-host-protection-system-technical-design-v0.4.md` |
| Phase 1 规范、状态机、不变量与 Gate | `docs/contracts/guard-phase-1-m0-contract-freeze-v0.3.md` |
| Core、Source、Decision、Enforcement、Firewall 细化语义 | `docs/contracts/*.md` |
| 已接受工程决策 | `docs/adr/*.md` |
| 数据库结构 | `migrations/*.sql` |
| 配置字段、默认值与范围 | `schema/config-v1.schema.json` |
| 当前工作包、Blocker 和 Gate 状态 | `docs/development/phase-1/STATUS.md` |
| M0 产物、完成条件与 Evidence 模板 | `docs/development/phase-1/M0-EXECUTION.md` |
| 实际通过证据 | 与审查对象绑定的测试输出和 `artifacts/evidence/` 产物 |

发生冲突时，先判断是实现漂移、文档漂移还是尚未批准的 Contract Change，不能为了让
当前实现通过而静默放宽权威要求。公共 API、协议、依赖、数据库、配置、权限、CI/CD
或跨模块契约变更必须先取得项目规则要求的确认。

### 0.2 审查与 Gate 分离

- `APPROVED` 只表示冻结的审查对象没有未解决的阻断性 Finding。
- `Implemented`、`Verified`、`Frozen`、`PASS` 和 `GO` 只能按 Phase 1 Contract 与
  `STATUS.md` 的证据规则更新。
- 脏工作区测试只能作为开发反馈，不能冒充 commit-bound Evidence、独立环境验证或 CI。
- M0 最终准入只能使用 Contract 第 21 节的 `GO` / `NO-GO`，不能用代码审查结论替代。

---

## 1. 审查基线

### 1.1 冻结审查对象

开始审查前必须记录：

| 对象 | 必须记录 |
|---|---|
| 工作区 Diff | 仓库根、staged/unstaged/untracked 清单、纳入与排除路径 |
| 暂存区 Diff | 当前 Index；目标文件存在 unstaged 修改时必须单独说明 |
| 单个 Commit | Commit SHA 与 Parent SHA |
| Commit Range | Base SHA、Head SHA、two-dot 或 three-dot 语义 |
| Pull Request | PR Head SHA、Base SHA、实际 required checks 及最新结果 |

最低只读检查：

```powershell
git status --short
git diff --stat
git diff
git diff --cached
```

未跟踪文件不会出现在普通 `git diff` 中，必须单独读取。审查期间 Head、Index 或目标文件
发生变化时，旧结论失效；先重新冻结基线，再复审变化范围。

### 1.2 审查范围声明

审查报告开头必须声明：

```text
Review target: <worktree | index | commit | range | PR>
Base / Head: <SHA 或 worktree 基线>
Included: <路径或包>
Excluded: <用户无关改动；没有则写 None>
Work Package / Gate: <例如 C1 / G18.2；不适用则写 N/A>
Authorities: <本次实际读取的 Contract / ADR / Schema / migration>
Environment: <OS、架构、Go 版本；未验证项写 NOT RUN>
```

---

## 2. 问题分类、Finding 严重性与证据标准

审查输出必须先分类，不能把所有不确定性都包装成代码缺陷：

| 类型 | 含义 | 是否使用 P0–P3 |
|---|---|---:|
| **Finding** | 有可定位代码/文档和触发路径，可以证明实际行为违反权威预期 | 是 |
| **Evidence Gap** | 实现可能正确，但缺少目标平台、crash/durability、独立复审、commit-bound 或 CI 证据 | 否 |
| **Question / Assumption** | 意图、范围或权威冲突尚未裁决 | 否 |

Evidence Gap 使用 `GAP-<范围>-<序号>` 标识，并说明缺失证据、阻塞的 Work Package/Gate
以及补证条件。关键 Evidence Gap 可以使结论为 `UNABLE_TO_VERIFY`，并使 Gate 保持
`NOT RUN`/`FAIL`，但不能在没有实现缺陷证据时虚构 P0/P1。

分级依据是可触发性、影响范围、可恢复性和证据，而不是看到“并发”“安全”“校验”
等关键词后机械套级别。

| 等级 | 判定 | 处理要求 |
|---|---|---|
| **P0 / Blocker** | 会破坏安全边界或核心 Contract，导致 Secret 泄漏、权限绕过、数据/安全意图损坏、foreign Firewall 对象被修改、不可恢复状态、稳定死锁/竞态或生产不可用 | 必须修复；不得批准、合并或放行 Gate |
| **P1 / Must Fix** | 确定性正确性错误、事务/幂等/恢复缺陷、关键失败分支缺失、资源泄漏、错误状态转换、严重测试缺口，或会让验收结论失真 | 批准前必须修复并复验 |
| **P2 / Follow-up** | 不影响当前正确性的局部可维护性、可观测性、性能或测试质量问题 | 可当前修复，或说明理由并形成明确后续项 |
| **P3 / Nit** | 命名、注释、格式或不影响行为的微小建议 | 不阻断；不得借此扩大范围 |

每条 Finding 使用稳定 ID `CR-<范围>-<序号>`。每条 P0–P2 必须同时具备：

1. 可定位的代码或文档位置；
2. 可触发的输入、状态或执行路径；
3. 实际行为与权威预期的差异；
4. 可观察影响；
5. 可执行的修复方向与复验条件。

证据不足时写为“问题/待确认项”，不能伪装成 Finding。审美偏好、没有当前复用需求的
抽象建议、假想的 Phase 2 扩展点，不应分级为 P0/P1。

---

## 3. GuardWall 项目检查点

### 3.1 通用检查维度

| 维度 | 核心问题 |
|---|---|
| 范围与 Gate | Entry Gate 是否满足？是否越过 M0 `NO-GO` 边界或实现 Phase 2 空壳？ |
| Contract 正确性 | 类型、状态机、唯一键、默认值和失败语义是否与权威源一致？ |
| 事务与持久性 | 原子提交、幂等、checkpoint、crash/restart 后是否保持已声明保证？ |
| 并发与生命周期 | owner、取消、停止等待、锁顺序、fencing 和 generation 是否明确？ |
| 安全与权限 | 外部输入、Secret、UID、路径、对象所有权和最小权限是否守住边界？ |
| 错误与可观测性 | 错误是否传播且脱敏？Health/Metric/Audit 是否反映真实状态？ |
| 测试与证据 | 正常、失败、边界、并发和恢复路径是否有具体断言与可复现证据？ |
| 可维护性 | 是否是完成当前需求所需的最小、清晰实现，没有兼容壳和过早抽象？ |

### 3.2 Source、Parser、Detection 与 Processing Coordinator

```text
□ Source 只产生 RawRecord/投递状态，不直接调用 Firewall？
□ processing attempt 是否冻结 Parser Set 与 Rule Catalog snapshot？
□ 只有 Processing Coordinator 能 begin/commit/rollback UnitOfWork？
□ Parser outcome、Detection contribution、Decision、Critical Audit、Receipt
  是否在同一事务边界内提交或整体回滚？
□ outcome/receipt 提交前，SourceDurable/checkpoint 是否绝不前进？
□ 重放和同进程 retry 是否不会重复 Window contribution 或持久化副作用？
□ RecordPermanent 与 Transient/PlanBlocked/Cancelled 是否按 Contract 分类？
□ queue full、shutdown timeout、rotation/generation 是否显式处理，不静默丢记录？
```

### 3.3 Decision、Enforcement 与 Reconcile

```text
□ Decision 是否只表达安全意图，不直接操作 Firewall？
□ Automatic/Manual identity、duplicate suppression、replace 和 expiry 是否符合 Contract？
□ Allowlist 是否保持正交 Policy Exception，不误终止 Decision？
□ Desired Ban Projection 与完整 Desired Firewall Snapshot 是否分层？
□ TargetProjectionRevision 与 TargetEnforcementGeneration 是否分离？
□ Infrastructure/Policy/Target 三类 retry budget 是否隔离且有上限？
□ stale generation/fencing 结果是否无法覆盖新状态？
□ timeout 结果未知时是否先 Probe，而不是盲目重复 Apply/Revoke？
□ Observed 是否只记录已观测事实，不把期望状态伪装成已收敛？
```

### 3.4 SQLite、migration 与崩溃恢复

```text
□ migration SQL 是否仍是数据库结构唯一权威，代码/测试未复制第二套 Schema？
□ 唯一约束和事务边界是否真正承载幂等，而非只靠进程内判断？
□ Commit/Rollback/Close 等有业务意义的错误是否传播？
□ WAL、synchronous、PRAGMA read-back 与声明的 durability failure domain 是否一致？
□ 空库、已有库、重复启动、迁移失败、SIGKILL/restart 是否覆盖？
□ 证据是否区分 process crash、OS crash、reboot 与 power-loss，未验证项是否写 NOT VERIFIED？
```

### 3.5 Firewall、权限与安全边界

```text
□ Firewall Backend 是否保持 Probe/Snapshot/Plan/Apply 的声明式边界？
□ 只修改 Guard-owned 对象，并保留 foreign rules/sets/chains？
□ OwnershipConflict、capability 缺失和部分失败是否快速失败且可观测？
□ 隔离 namespace/VM 外是否禁止对宿主机 Firewall 做破坏性验证？
□ Agent 是否保持非特权，只有 Enforcer 获得必要权限？
□ IPC 是否校验 peer UID、frame 大小、命令白名单、对象名和 timeout？
□ Secret、credential、完整配置、日志正文、IP/CIDR 是否未进入不允许的日志、错误或 Metric label？
```

### 3.6 Config、平台、资源与可观测性

```text
□ 配置字段、默认值、范围和 unknown-field 拒绝是否以 Config Schema 为准？
□ credential 是否 no-follow、限大小、校验 owner/mode，并在错误中脱敏？
□ Linux/Windows/unsupported 文件是否各自给出明确行为，交叉编译未冒充原生 Smoke？
□ IO/SQLite/Firewall/IPC 调用是否有 timeout、cancellation 或清晰失败传播？
□ retry、queue、batch、record size 等资源是否有上限和触顶行为？
□ span、结构化日志、Health、Metric、Critical Audit 是否记录真实结果且避免高基数 label？
```

---

## 4. 风险路由与必需验证

先按变更类型确定权威源、审批边界和验证，不得凭文件后缀套固定命令。

| 变更类型 | 审查重点 | 最小验证方向 |
|---|---|---|
| 纯文档 | 引用、权威关系、状态是否被误写 | `git diff --check`、链接/路径与术语检查 |
| Go 逻辑 | Contract、错误、边界、具体副作用 | 改动包测试，再执行全仓 test/vet/mod verify |
| 并发/事务 | race、幂等、rollback、取消、restart | 定向 race + 全仓 race + 失败注入/恢复测试 |
| migration/Store | Schema 权威、升级、约束、durability | 空库/已有库/重复迁移/失败恢复测试 |
| Config/credential | Schema、unknown、owner/mode、脱敏 | Schema 解析 + config/credential 定向测试 |
| Firewall/权限 | ownership、隔离、UID、timeout、恢复 | Fake 测试；真实 Backend 仅隔离 Linux 环境 |
| 公共契约/依赖/权限/CI | 是否已获得明确确认并同步权威产物 | 未确认先停止，不通过代码补丁既成事实 |

当前仓库约定明确时使用约定命令；没有约定时，Go 变更的通用本地验证基线为：

```powershell
$env:GOTOOLCHAIN = 'local'
go version
# 作者对本次修改的 .go 文件执行 gofmt -w；只读审查者使用 gofmt -d 检查
go mod verify
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

需要记录实际 Go 版本并与 `go.mod` 的精确版本要求核对。命令成功只证明当前环境中的
该次运行，不自动证明 Linux 原生、Firewall 隔离、SIGKILL/restart 或 power-loss Gate。

若 `.github/workflows/` 尚无实际工作流，Contract 第 35 节的 CI 矩阵只是目标，不是已运行
证据；报告必须写 `CI: NOT RUN / NOT CONFIGURED`。若已有工作流，则以实际 workflow 和
branch protection 为准，不能在本文复制一份易漂移的 required job 清单。

---

## 5. 标准审查流程

### 5.1 作者准备审查包

作者提交审查前必须提供：

1. 目标工作包/任务、范围和非目标；
2. 冻结的 Diff/Commit/Range；
3. 本次读取的 Contract/ADR/Schema/migration；
4. 风险面与需要 Ask First 的变更；
5. 实际执行的命令、环境、结果和未运行项；
6. 工作区中明确排除的用户改动。

### 5.2 审查者四轮扫描

1. **边界轮**：确认基线、Scope、Entry Gate、Ask First 和权威源。
2. **正确性轮**：沿输入→状态→副作用→失败→恢复路径检查 Contract 与安全不变量。
3. **测试轮**：确认测试会在错误实现下失败，断言具体业务状态而非只有 `NoError/NotNil`。
4. **证据轮**：核对命令、环境、commit、skip/not-run、独立复审和 Evidence 的可复现性。

首轮尽量集中返回已发现问题；修复引入的新回归仍可在复审中新增 Finding。

### 5.3 修复与复审闭环

```text
冻结对象
  → 首轮审查
  → 作者逐条回复
  → 修复 P0/P1（P2 处理或明确延期）
  → 运行受影响验证
  → 复审最新 Diff
  → 关闭/保留 Finding
  → 输出代码审查结论
  → 按 Contract 单独判断 Evidence、STATUS 与 Gate
```

- 每条 Finding 只能标记为 `OPEN`、`RESOLVED`、`ACCEPTED_FOLLOWUP`、`OUT_OF_SCOPE`
  或 `REJECTED_BY_RULING`。
- `RESOLVED` 必须引用修复位置和复验结果，不能只写“已改”。
- P0/P1 不能只靠解释关闭；只能修复并验证，或由有权者完成明确裁决/Contract Change。
- `ACCEPTED_FOLLOWUP` 只适用于 P2/P3；`OUT_OF_SCOPE` 必须说明不影响当前正确性的理由。
- 修复改变了审查基线时，至少复审新 Diff；重大重写需重新执行完整四轮扫描。
- P0/P1 存在分歧时，双方给出触发条件和证据，交由独立审查者或用户裁决；裁决前不批准。

### 5.4 独立复审

Contract、M0/里程碑 Gate 或 Evidence 模板要求“独立复审”时：

- 复审者不能是同一实现者；Agent 场景下也不能只是同一上下文自我确认；
- 输入必须是冻结 Diff/Commit 与明确的权威文档；
- 复审报告要记录 reviewer、时间、结论和未解决 Finding；
- 重新运行作者命令只能验证可复现性，不能替代代码与 Contract 审查；
- 缺少独立证据时保持 `Implemented`/`NOT RUN`，不得写成 `Verified`。

---

## 6. 反馈与结论格式

### 6.1 单条 Finding

```markdown
**[P1] [事务正确性] 提交失败后 checkpoint 仍可能前进**
`CR-C1-001` · `OPEN`
`internal/processor/coordinator.go:123`

- **触发条件：** <输入、状态、并发或故障注入点>
- **实际行为：** <当前代码会做什么>
- **权威预期：** <Contract/ADR/Schema/migration 的具体锚点>
- **影响：** <数据、安全意图、可恢复性或证据影响>
- **建议：** <最小修复方向>
- **复验：** <应新增/运行的测试与关键断言>
```

### 6.2 审查报告

```markdown
## Review Baseline
<target、base/head、included/excluded、authorities、environment>

## Findings
<按 P0 → P3 排序；没有则写“未发现可定位的 P0–P3 Finding”>

## Evidence Gaps
<GAP ID、缺失证据、阻塞的 WP/Gate、补证条件；没有则写 None>

## Questions / Assumptions
<尚未裁决的意图或权威冲突；没有则写 None>

## Verification
<命令、平台、PASS/FAIL/NOT RUN；区分脏工作区与 commit-bound Evidence>

## Remaining Risks
<未运行平台、隔离测试、真实 Backend、durability failure domain 等>

## Conclusion
<CHANGES_REQUESTED | APPROVED_WITH_FOLLOWUPS | APPROVED | UNABLE_TO_VERIFY>

## State Boundary
<本结论是否仅为代码审查；不得自动写成 Verified/Frozen/PASS/GO>
```

### 6.3 代码审查结论

| 结论 | 条件 |
|---|---|
| `CHANGES_REQUESTED` | 存在未解决 P0/P1 |
| `APPROVED_WITH_FOLLOWUPS` | 无 P0/P1；存在已接受的 P2 或明确的未验证风险 |
| `APPROVED` | 无 P0/P1、无待跟踪 P2；必需的本地验证已通过 |
| `UNABLE_TO_VERIFY` | 缺少源码、权威契约、稳定基线或必要环境，无法可靠判断 |

验证失败且能定位实现缺陷时，形成与根因对应的 Finding；环境不可用、证据缺失或未运行项
写入 Evidence Gaps/Remaining Risks，不能伪造通过，也不能凭空升级为 P0/P1。

---

## 7. 快速检查卡

### 7.1 作者提交前

```text
□ 工作包 Entry Gate 允许本次改动？
□ Review target、Base/Head、纳入和排除范围已冻结？
□ Contract/ADR/Schema/migration 权威源已列出？
□ Ask First 变更已获得明确确认？
□ 改动是完成当前需求所需的最小实现？
□ 正常、失败、边界、并发/恢复测试有具体业务断言？
□ Go 版本、test/race/vet/mod verify 与 diff check 结果已记录？
□ Secret、foreign Firewall 对象和用户无关改动未被带入？
□ NOT RUN、known limitation 和脏工作区边界已如实说明？
```

### 7.2 审查者快速扫描

```text
□ 有没有越过 M0/里程碑 Gate 或复制第二套权威语义？
□ Coordinator 之外有没有私自 begin/commit/rollback？
□ checkpoint 会不会越过未提交 outcome/receipt？
□ retry/replay/crash 会不会制造重复 Window/Decision/副作用？
□ stale generation 能不能覆盖新状态或重置 retry budget？
□ Firewall 是否只改 Guard-owned 对象？
□ timeout、取消、shutdown、restart 路径是否明确？
□ Secret、IP/CIDR、path、username 是否进入日志或高基数 Metric label？
□ 测试是否真的验证状态、副作用和失败恢复？
□ Evidence 是否绑定真实对象、环境与命令，没有把 NOT RUN 写成 PASS？
```

快速卡只用于导航，不能代替逐路径审查、故障注入或 Contract Gate。
