# 启用与跨会话恢复协议

本文只处理两件事：首次把 Task Continuity 引入一个项目，以及已启用项目在新会话中的恢复。首次启用必须由用户显式调用 `$task-continuity`；隐式匹配不得替用户初始化记忆或修改项目指令。

## 状态与入口

先定位项目根目录，再检查 `.task-memory/HANDOFF.md`：

```text
显式调用 $task-continuity
  ├─ HANDOFF 存在 ──> RECOVER：按最小恢复协议读取，不重建契约
  └─ HANDOFF 不存在 ─> ADOPT：只读发现 -> 澄清 -> 等待确认 -> 初始化

新会话中的首次项目工作
  └─ AGENTS managed block 发现 HANDOFF ─> 调用 Skill -> RECOVER -> 执行请求
```

“自动恢复”不是在用户发送消息前后台运行。它表示 Agent 在新运行中加载项目 `AGENTS.md` 指令后，若发现既有 HANDOFF，必须在处理该会话的首次项目工作前调用本 Skill 并恢复最小必要上下文。

## 首次显式启用

### 1. 先做只读发现

HANDOFF 不存在时，不要立即创建 `.task-memory/`，也不要先写 AGENTS 文件。先读取足以识别项目约束、已有进度和用户最终需求的证据，读到能提出可靠契约草案为止：

1. 用户当前明确请求及当前会话中已确认的选择。
2. 从工作目录到项目根目录生效的 `AGENTS.override.md`、`AGENTS.md` 和项目声明的其他代理规则；越靠近目标工件的规则越具体。
3. 项目的权威需求与设计文档，例如 README、规格、方案、路线图、任务清单或用户指明的文档。
4. 当前交付物与验证入口，例如源码、测试、构建清单、进度文件和 issue 引用。
5. 必要时读取只读的版本控制状态与近期相关历史，用来识别已经完成或尚未提交的工作；不得把提交历史等同于用户最终意图。

只读取与当前任务有关的材料。不要遍历整个仓库，不要为了启用连续性而运行修改型命令、构建、迁移或自动格式化。

### 2. 分清证据强度

整理发现时必须分为四类：

- **用户已确认**：用户当前或可追溯的既有明确指令。
- **仓库事实**：可由项目工件、测试或版本历史直接证明。
- **推断**：由多个事实推导，但用户或权威文档尚未确认。
- **未知**：证据缺失、互相冲突，或会实质改变 Scope、Success Criteria、约束与交付物的选择。

不得用推断填充 Task Contract。历史实现只能证明“项目现在是什么样”，不能单独证明“用户最终想要什么”。

### 3. 交互式澄清并等待确认

向用户展示一份拟议 Task Contract，至少包含：

- Task ID
- Original Goal
- Success Criteria
- In Scope / Out of Scope
- Constraints
- Current Objective 与已知进度
- 仍需用户裁决的未知或冲突

问题应少而关键，优先询问会改变最终结果、边界或验收方式的事项。若项目已进行到一定程度，应同时说明哪些内容来自现有工件，哪些只是推断。用户回答后更新草案；在用户明确确认契约前保持只读。

同一轮还应展示将写入项目根 `AGENTS.md` 的 Task Continuity managed block，并说明其用途是让后续新会话在首次项目工作前自动恢复。以下两项必须一并获得明确确认：

1. 以确认后的契约创建 `.task-memory/`；
2. 创建或增量更新项目根 `AGENTS.md` 中的 managed block。

显式调用 Skill 只代表用户发起启用流程，不代表用户已经批准文件写入。若用户只确认契约、未确认 AGENTS 改动，继续保持只读并请求补充确认。

### 4. 确认后初始化

确认后按 [memory-protocol.md](memory-protocol.md) 和模板创建记忆：

1. 准备并校验 DECISIONS、LESSONS、Daily 等辅助文件；此时不要创建 HANDOFF。
2. 将可验证的已有进度准备为 Current State；需要保留的原始观察写入当日 Daily。
3. 只有存在已被用户或项目权威来源确认的选择时才创建 Decision；只有满足证据门槛时才创建 Lesson。
4. 按下文规则安装并回读 managed block，确认块外项目指令未被改写。
5. 最后用确认后的 Task Contract 写入 HANDOFF，不把未知项写成既定事实，并回读确认最低字段齐全、没有模板占位符。
6. 只有有效 HANDOFF 与当前生效的 managed block 都验证成功后，才报告项目已经启用。任一步骤失败时不得声称成功；保留可审计的现状并说明未完成部分。

## AGENTS managed block

使用 [templates/AGENTS.task-continuity.md](../templates/AGENTS.task-continuity.md) 中的稳定标记：

```markdown
<!-- TASK_CONTINUITY:BEGIN -->
...
<!-- TASK_CONTINUITY:END -->
```

先确定项目根在下一次运行实际会读取的指令文件：根目录存在非空
`AGENTS.override.md` 时使用它，否则使用已有 `AGENTS.md`，两者都没有时创建
`AGENTS.md`。在预览中明确显示目标文件。

安装规则：

- 目标文件已存在：保留其全部既有内容，只追加一个 managed block。
- 两个标记都已存在：只替换标记之间的内容，不重复追加。
- 项目根没有任何指令文件：经用户确认后创建 `AGENTS.md`，仅写入该 managed block。
- 只有一个标记、标记顺序错误或存在多个块：视为损坏，停止写入并向用户说明，不猜测修复范围。
- 更具体目录规则或其他项目权威指令若禁止修改、规定了不同恢复方式，或与模板语义冲突：停止写入，展示冲突证据并请用户裁决。
- 检查运行时 `project_doc_max_bytes` 及完整指令链预算；若 managed block 可能因截断而不被加载，停止并说明，不能宣称自动恢复已建立。
- 不得覆盖、重排、格式化或顺手清理 managed block 之外的内容。

Codex 在每次新运行开始时构建一次 AGENTS 指令链。当前会话写入的块只保障后续
新会话；不要声称本会话已重新加载它。新会话还必须从该项目目录或其子目录启动，
否则项目级 AGENTS 可能不在发现链中。

任何 AGENTS 写入都属于首次启用的一部分，必须在预览后得到用户确认。后续升级模板时仍遵循同样的幂等替换与冲突检查；不得静默扩大 managed block 的职责。

## 已有记忆的恢复

HANDOFF 可读、最低 Task Contract 字段齐全且没有未替换占位符时，才可判定为有效记忆：

1. 读取 HANDOFF，确认 Task Contract、Current Objective、Progress、Blockers 与 Next Actions。
2. 只按 HANDOFF 引用读取执行当前请求所需的 Decision、Lesson、Daily 或项目工件。
3. 发现记忆损坏或与当前明确指令冲突时，按主协议处理，不用首次启用流程覆盖它。
4. 若 managed block 缺失，不影响本次显式恢复；但要把“后续会话无法稳定自动恢复”作为配置缺口说明。只有在用户确认预览后才能补写该块。

恢复不要求用户再次说“恢复记忆”。只要项目规则中的 managed block 已生效，Agent 就应在新会话首次处理项目任务前执行上述步骤。一次性闲聊或与项目无关的问题不需要读取项目记忆。
