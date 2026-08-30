---
name: task-continuity
description: Preserve and restore long-running project task state across conversations, compaction, or agent restarts. Use when the user explicitly invokes $task-continuity to adopt a project for the first time, when an already-adopted project has .task-memory/HANDOFF.md and project instructions require automatic recovery, or when checkpointing its decisions, lessons, and progress. Never initialize an unadopted project from implicit matching alone; do not use for one-off questions or full chat archiving.
---

# Task Continuity

把任务状态归属于 Task / Project，而不是某一次 Conversation。目标是用最小工作集恢复“为什么做、做到哪里、下一步是什么”，而不是保存完整聊天记录。

## 工作边界

- 默认在项目根目录使用 `.task-memory/`，除非用户已指定其他目录。
- 第一版只使用 Markdown 文件；不要引入数据库、Embedding、后台服务、全局知识晋升或自动 Git 提交。
- 不要主动修改业务代码。记忆文件是否纳入 Git 由用户或项目规则决定。
- 用户当前明确指令的权威最高；任何记忆都不能覆盖它。
- 首次启用必须来自显式调用；项目已有有效 HANDOFF 后，后续会话可以自动恢复。

首次接入项目、回溯已有进展或配置自动恢复时，必须读取 [references/adoption.md](references/adoption.md)。需要创建或校验文件结构、字段、编号、来源和压缩规则时，读取 [references/memory-protocol.md](references/memory-protocol.md)。需要判断话题关系、暂停或恢复任务时，读取 [references/topic-shift.md](references/topic-shift.md)。不要在普通恢复时一次性加载全部参考资料和历史记忆。

## 启用状态机

先解析项目根目录，再检查 `.task-memory/HANDOFF.md` 与当前生效的 AGENTS 自动恢复块：

1. **有效 HANDOFF + 生效的 managed block**：视为已经完整启用。按“恢复任务”自动恢复，不要求用户再次显式调用，也不重复初始化。
2. **有效 HANDOFF，但 managed block 缺失或不生效**：本轮可以恢复，但后续自动恢复没有可靠入口。说明配置缺口；只有用户确认预览后才能补写 managed block。
3. **HANDOFF 不存在，且本轮是显式调用**：进入“首次启用”。显式调用只包括用户通过 UI / 运行时主动选择本 Skill，或用户在请求中点名 `$task-continuity`；路由器或模型因语义匹配自动加载不算显式调用。
4. **HANDOFF 不存在，且只是隐式匹配**：不得创建 `.task-memory`、不得修改项目指令，也不要打断当前请求；按未启用状态继续工作。

HANDOFF 必须可读，包含 Task ID、Original Goal、Success Criteria、Scope、Constraints 和 Current State，且没有未替换模板占位符，才算有效。存在目录、空文件、损坏文件或仅有 Daily 都不代表已经启用。

## 首次启用

首次启用时先读取 adoption 协议，并区分新项目与已有进展的项目：

1. 先读取当前目录适用的 `AGENTS.override.md` / `AGENTS.md` 指令链，再按项目实际结构定向读取 README、设计文档、计划、任务记录、版本历史、相关实现和测试证据。不要无目的地加载整个仓库。
2. 若项目已有实质进展，把发现内容分为“用户已确认”“仓库事实”“推断”“未知”，据此拟定 Task Contract 和 Current State。仓库现状只能证明发生过什么，不能单独证明用户最终想要什么。
3. 向用户展示拟议的 Original Goal、Success Criteria、Scope、Constraints、Current Objective、已完成进展与未决问题，并标注关键来源。明确说明确认后会创建 `.task-memory`，并在项目有效指令文件中加入自动恢复块；等待用户确认或修正。
4. 用户确认前不得写入正式记忆，不得把推断晋升为 Decision / Lesson，也不得修改 AGENTS 文件。若存在多个合理任务解释，必须让用户选择，不能自行合并。
5. 新项目若用户的显式调用已完整给出目标、成功标准、范围与约束，可直接据此形成草案，不必为已明确字段重复提问；但正式写入前仍要展示 Task Contract、managed block 与目标文件，并取得一次明确确认。缺少会改变执行方向的字段时，只询问最小必要问题。
6. 确认后，Task ID 应稳定且不依赖 Conversation ID。按需使用以下模板，而不是自行发明并行格式：
   - [templates/HANDOFF.md](templates/HANDOFF.md)
   - [templates/DECISIONS.md](templates/DECISIONS.md)
   - [templates/LESSONS.md](templates/LESSONS.md)
   - [templates/DAILY.md](templates/DAILY.md)，实例保存为 `.task-memory/daily/YYYY-MM-DD.md`
7. 复制模板时替换所有尖括号占位符；不可用的可选字段删除或明确写 `None`，不要把占位符留在正式记忆中。
8. 使用 [templates/AGENTS.task-continuity.md](templates/AGENTS.task-continuity.md) 向项目根当前有效的 AGENTS 指令文件追加或更新唯一的 managed block。若根目录存在非空 `AGENTS.override.md`，它是该层目标；否则使用或创建 `AGENTS.md`。保留全部既有内容；标记损坏、指令预算不足、规则冲突或无权修改时停止，并明确说明自动恢复尚未建立。
9. 为避免部分写入被误判为启用，按以下顺序提交：准备并校验辅助记忆文件 → 写入并回读 managed block → 最后写入并回读 HANDOFF。只有 HANDOFF 与生效块都验证成功后才能报告“已启用”。

Task Contract 至少保存 Task ID、Original Goal、Success Criteria、Scope 和 Constraints。普通总结不得修改它；只有用户明确变更契约，或 Accepted Decision 明确改变约束时才可修改，并同步记录 Decision。

## 恢复任务

项目 AGENTS 指令触发本 Skill 且 HANDOFF 有效时，直接恢复；不要再次询问“是否启用”，也不要要求用户重复 `$task-continuity`。在用户没有发送任何消息或任务时，Skill 不能自行运行；“自动恢复”是指从该项目或其子目录启动的新会话处理首个项目请求前自动执行。首次写入 managed block 的当前会话不会重新加载 AGENTS 指令链，该块从后续新会话开始生效。

按渐进式顺序恢复，读到足够执行当前请求就停止：

1. 先读 `HANDOFF.md`，确认 Task Contract、Current Objective、Progress、Blockers 和 Next Actions。
2. 只读取 HANDOFF 明确引用的 Decision 与 Lesson 条目。
3. 仍缺少必要证据时，再读取相关的近期 Daily 条目或项目工件。

不要默认加载整个 `DECISIONS.md`、`LESSONS.md` 或全部 Daily。若状态有效且无冲突，用一句话说明已恢复的 Current Objective / Next Action 后继续用户请求，不要求重复确认 Task Contract。若记忆与用户当前指令冲突，以当前指令为准，并按“冲突与损坏”处理历史关系。

## 每轮处理

在执行请求前做轻量分类：

- `CONTINUE`：同一 Project、Goal、Deliverable 或 Artifact 下的直接推进，正常继续。
- `TANGENT`：与当前任务相关的解释、调查或短支线；回答后保留 Active Task，不提示切换。
- `SWITCH`：Project、Goal、Deliverable、Artifact 与 Intent 均明显不同的独立任务。

不能只按关键词相似度判断，必须结合项目身份、目标、交付物、工件、实体、阶段和用户意图。对 `SWITCH`，仅当存在已形成明显上下文的 Active Task，且新任务预计不是一句话即可结束时：

1. 先为旧任务执行检查点；
2. 建议新开会话，但不要强制；
3. 若用户留在当前会话，继续响应，但不要把新任务内容写入旧任务记忆。MVP 不自动维护多 Task 栈；新任务也需要持久连续性时，先与用户确定独立记忆归属，再初始化新的 Task Contract。

完成用户请求后，只在产生有价值的新状态时分类记忆候选：

| 类型 | 写入位置 |
| --- | --- |
| `STATE_CHANGE` | `HANDOFF.md` |
| `DECISION` | `DECISIONS.md`，必要时更新 HANDOFF 引用 |
| `OBSERVATION` | 当日 Daily |
| `VERIFIED_LESSON` | `LESSONS.md`，必要时更新 HANDOFF 引用 |
| `NONE` | 不写 |

不要为了“有记忆”而每轮机械写文件。同一轮可以产生多个类型，但每条信息只承担其对应语义。

## 写入与晋升

- HANDOFF 是当前最小可恢复状态快照，采用覆盖式维护，不是历史日志。保留 Task Contract、当前状态、未解决 Blocker、下一步和必要引用；压缩已完成的微步骤，并将细节指向 Daily。
- DAILY 是按日期分片的原始事件和待验证观察，可包含 Action、Observation、Hypothesis、Status 与 Next Validation。
- DECISIONS 记录用户或团队确认的工程选择及理由。旧 Decision 不得删除；变更时创建新 Decision，并用 `Superseded` / `Superseded By` 保留历史链。
- LESSONS 只保存经实际证据支持、可复用的经验。每条必须包含 Claim、Evidence、Applicable Conditions、Not Proven 和 Confidence。
- 猜测、单次偶发现象、未经验证的外部观点和主观推断只能留在 Daily，证据充分后才可晋升为 Lesson。
- 尽量记录稳定来源：项目工件、分支、提交、测试或时间戳与文本锚点。Conversation ID 可选，不能成为恢复的强依赖。

以下事件触发 HANDOFF 检查点：目标、Scope、Success Criteria 或 Constraint 改变；阶段或 Current Objective 改变；Blocker 出现或解除；产生 Accepted Decision；完成重要测试；即将切换任务；本轮取得明显任务进展。没有事件就不更新。

## 冲突与损坏

- **Memory Missing**：从当前上下文与项目证据初始化新 Task Contract，并明确历史状态未知；不要假装已恢复。
- **Memory Corrupted**：保留损坏原件，禁止原地覆盖；先创建带时间戳的恢复副本，再仅从可验证来源重建。无法安全重建时停止写入并向用户说明。
- **Conflicting Decisions**：两个冲突的 Accepted Decision 若无 supersedes 关系，显式标记 Conflict，不自行猜测。当前用户明确指令可指导本轮执行；同时创建新 Decision、关联并 supersede 旧项后，再更新 Task Contract。
- **Uncertain Lesson**：证据不足时留在 Daily，不得通过措辞包装成长期经验。

## 不可变规则

1. Conversation 不等于 Task；任务可以跨会话，一个会话也可包含多个任务。
2. Task Contract 不得因普通总结自动变化。
3. 用户当前明确指令 > Task Contract > Accepted Decision > Verified Lesson > Daily Observation > Model Assumption。
4. 未验证信息不得进入 LESSONS。
5. 旧 Decision 不得删除，只能保留、拒绝、废弃或被新 Decision 取代。
6. HANDOFF 只保存当前工作状态，不保存完整历史，并应控制在约 100～150 行。
7. 默认恢复只读取 HANDOFF，再按引用最小化加载。
8. 相关支线不得误判为任务切换；新会话建议不能阻断用户。
9. 记忆写入由事件触发，不按轮次强制执行。
10. 记忆系统自身必须服从上下文预算。
