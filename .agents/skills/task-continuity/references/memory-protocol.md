# 任务记忆协议

本协议用于维护可跨会话恢复的任务状态。记忆归属于 Task / Project，
而不是 Conversation；会话标识只能作为来源信息，不能作为任务身份或恢复前提。

## 1. 存储结构

MVP 使用项目内的 `.task-memory/`：

```text
.task-memory/
├── HANDOFF.md
├── DECISIONS.md
├── LESSONS.md
└── daily/
    └── YYYY-MM-DD.md
```

- `HANDOFF.md`：当前任务的最小可恢复快照，覆盖式维护。
- `DECISIONS.md`：已提出或已确认的工程选择，追加式维护。
- `LESSONS.md`：有证据支持且可复用的经验，追加式维护。
- `daily/`：按日期分片的过程日志，可包含未验证信息。

不要在 MVP 中引入数据库、Embedding、后台服务或自动 Git 操作。是否跟踪
这些文件由项目自行决定；Skill 不得擅自修改 `.gitignore`、提交或推送。
首次启用经用户确认后，可以在项目当前有效的 AGENTS 指令文件中维护唯一的
Task Continuity 自动恢复块；这项例外只用于恢复入口，不能改写其他项目规则。
HANDOFF 是最终启用标志，应在辅助记忆文件和 managed block 写入、回读成功后
最后创建。只有有效 HANDOFF 与生效的 managed block 同时存在，才算完整启用。

## 2. 权威顺序与冲突处理

同一事项出现矛盾时，按以下顺序确定当前行为：

1. 用户当前明确指令
2. Task Contract
3. Accepted Decision
4. Verified Lesson
5. Daily Observation
6. 模型假设

高权威信息可以推翻低权威信息，但不得静默改写历史。若用户明确改变目标、
范围、成功标准或约束，应建立新 Decision、标记被替代的旧 Decision，并更新
Task Contract。

若两个互相冲突的 Decision 都是 `Accepted`，且没有 `Superseded By` 关系：

- 将冲突明确标出，不自行猜测哪个有效；
- 当前操作仅可遵循用户最新的明确指令；
- 没有最新明确指令时，暂停受冲突影响的步骤并请求裁决；
- 裁决后新增或更新 Decision 状态，保留原记录。

## 3. 最小读取集

恢复任务时采用渐进式读取，不默认加载全部历史：

1. 只读 `HANDOFF.md`。
2. 若 HANDOFF 引用了 Decision 或 Lesson，只读取完成当前请求所需的对应条目。
3. 仍缺少事实、实验过程或来源时，再读取相关日期的 Daily 条目。
4. 只有无法定位条目时，才扩大读取范围。

读完后应能回答：当前目标是什么、做到哪里、有哪些未解阻塞、下一步是什么。
若这些信息已经足够，停止加载更多记忆。

## 4. 写入分类

每次产生实质进展后，先分类，再决定是否写入：

| 类型 | 写入位置 | 判定标准 |
| --- | --- | --- |
| `STATE_CHANGE` | HANDOFF | 当前目标、阶段、进度、阻塞或下一步发生变化 |
| `DECISION` | DECISIONS | 用户或团队明确选择了做法、边界或取舍 |
| `OBSERVATION` | 当日 DAILY | 尝试、现象、假设、失败或待验证结论 |
| `VERIFIED_LESSON` | LESSONS | 证据足以支持一个带适用条件的可复用结论 |
| `NONE` | 不写 | 没有值得恢复或复用的新信息 |

不要为了“保持记忆”而机械写文件。一项进展可能同时产生多种记录，但各文件
只保存其职责范围内的信息，不重复整段内容。

### HANDOFF 最低内容

HANDOFF 包含两个部分：

- 稳定的 Task Contract：Task ID、Original Goal、Success Criteria、Scope、
  Constraints。
- 动态的 Current State：Current Objective、Current Phase、Progress、Blockers、
  Next Actions、相关 Decision / Lesson 引用、来源锚点、更新时间。

普通总结不得修改 Task Contract。只有用户明确变更，或 Accepted Decision 明确
改变任务约束时，才能同步修改。

### Decision 条目

每项 Decision 使用稳定 ID（如 `D-017`），至少包含：

- 标题、状态、日期；
- Decision、Context、Rationale、Trade-offs；
- 必要时记录 Alternatives、Revisit When、Source、Related；
- 被替代时记录 `Superseded By`。

新增条目前先扫描已有 Decision ID，使用当前最大数字加一；不得复用已删除、拒绝、
废弃或被替代的编号。Lesson ID 使用相同规则独立递增。

### Lesson 条目

每项 Lesson 使用稳定 ID（如 `L-012`），必须包含：

- `Claim`
- `Evidence`
- `Applicable Conditions`
- `Not Proven`
- `Confidence`
- `Source`

缺少上述证据边界时，不得写入 LESSONS。

### Daily 条目

Daily 按日期分文件，以时间或事件为条目标题。按需记录 `Action`、
`Observation`、`Hypothesis`、`Status` 和 `Next Validation`。Daily 允许保留
错误、失败、猜测和未完成实验，它是证据日志，不是权威结论。

## 5. 记忆晋升规则

信息按可信度晋升：

```text
临时观察或猜测 -> DAILY
DAILY + 足够证据 -> LESSONS
明确工程选择 -> DECISIONS
与当前任务相关的 Decision / Lesson -> HANDOFF 中只保存引用
```

以下内容只能先留在 Daily：模型猜测、未验证推断、单次偶发现象、用户主观
猜测、未经项目验证的外部观点。

晋升为 Lesson 前，确认：

1. 结论有可追溯证据，而非仅有解释；
2. 适用环境、输入或边界已写清；
3. 未证明的范围已显式排除；
4. Confidence 与证据强度一致；
5. Source 能定位到 Daily、实验、测试结果、制品或提交。

项目 Lesson 不自动晋升为跨项目经验；MVP 不自动维护 Global Lesson。

## 6. Decision 状态机

合法状态为：

```text
Proposed -> Accepted
Proposed -> Rejected
Accepted -> Deprecated
Accepted -> Superseded
```

- `Proposed`：尚未获得明确确认。
- `Accepted`：当前有效的规范性选择。
- `Rejected`：已明确不采用。
- `Deprecated`：仍保留历史，但不建议继续采用。
- `Superseded`：已被另一 Decision 替代，必须记录替代者 ID。

Decision 不得直接删除。修正历史记录时保留原条目，通过状态和关联关系表达
变化。Task Contract 只能引用当前有效的 Accepted Decision。

## 7. 事件驱动 Checkpoint

Checkpoint 不依赖“上下文快满了”的猜测。发生以下事件时更新 HANDOFF：

- 用户改变 Goal、Scope、Success Criteria 或 Constraint；
- 当前阶段完成或 Current Objective 改变；
- 新 Blocker 出现或既有 Blocker 解决；
- 产生 Accepted Decision；
- 完成会影响下一步的重要测试；
- 即将切换到另一个 Task；
- 当前回复完成了明显、可恢复的任务进展。

纯解释、短暂讨论或无状态变化的回复不触发写入。Checkpoint 应在任务进展
已经确认后执行，不能提前把计划写成已完成事实。

## 8. HANDOFF 压缩

HANDOFF 建议保持在 100～150 行内。达到预算时：

1. 永不删除 Task Contract；
2. 保留当前目标、阶段、未解 Blocker、下一步和仍相关的引用；
3. 删除已完成的微步骤和过期的临时措辞；
4. 将连续的历史进展折叠成一句阶段结论；
5. 用 Daily 路径或 Decision / Lesson ID 指向细节，不复制全文；
6. 不为压缩而改变状态含义或丢失未决事项。

压缩结果必须是当前快照，而不是精简版聊天记录。

## 9. 缺失、损坏与不确定状态

### HANDOFF 缺失

- 明确视为“没有可恢复状态”，不得假装找到历史；
- 从当前用户指令和可见项目事实建立新的 Task Contract；
- 不确定的历史内容保持未知，不从仓库痕迹反推为用户决定。

### 文件损坏或无法解析

- 保留原文件，禁止覆盖；
- 在同目录创建带时间戳的 recovery copy 或新的恢复草稿；
- 只从仍可验证的片段重建状态，并标注来源和缺口；
- 在替换正式记忆前让用户确认有语义影响的恢复结果。

### Lesson 证据不足

- 保留在 Daily，并标记 `待验证`；
- 列出下一步验证方式；
- 不因多次总结或重复出现而自动提高 Confidence。

### 来源缺失

优先记录稳定的项目锚点，例如文件、章节、分支、提交或实验 ID。运行时可用时
再补充 conversation / turn ID；它们不是强依赖，也不能代替证据本身。
