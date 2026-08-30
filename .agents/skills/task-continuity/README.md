# Task Continuity Skill

面向长会话与长任务的任务连续性 Skill。它在项目内维护一个轻量的
`.task-memory/` 目录，让 Agent 在上下文压缩、切换会话或重启后，仍能恢复：

- 原始目标、成功标准、范围与约束；
- 当前阶段、进度、阻塞项和下一步；
- 已确认的工程决策；
- 有证据支持、可复用的经验；
- 需要继续验证的当日观察。

它不是聊天归档系统。默认只读取 `HANDOFF.md`，再按引用加载必要的决策、
经验或近期日志，避免记忆本身无限占用上下文。

## 安装

### 方式一：使用 npx（推荐）

需要本机已安装 Node.js。运行：

```bash
npx skills add lifei6671/task-continuity-skill
```

`skills` CLI 会下载 Skill，并按终端提示把它配置给所选的 AI Agent。

如果不希望发送匿名遥测，可临时设置 `DISABLE_TELEMETRY=1`：

```powershell
$env:DISABLE_TELEMETRY='1'
npx skills add lifei6671/task-continuity-skill
```

```bash
DISABLE_TELEMETRY=1 npx skills add lifei6671/task-continuity-skill
```

### 方式二：让 Codex Agent 自动安装

在 Codex 中发送下面这段话：

```text
使用 $skill-installer 从
https://github.com/lifei6671/task-continuity-skill
安装 task-continuity Skill。安装到用户级 Skill 目录，保留已有 Skills；
如果同名目录已经存在，不要覆盖，先检查并告诉我应该更新还是保留。
安装完成后校验 SKILL.md，并告诉我是否需要重启 Codex。
```

也可以要求 Agent 安装到当前项目：

```text
从 https://github.com/lifei6671/task-continuity-skill 安装
task-continuity Skill 到当前项目的 .agents/skills/，不要修改其他 Skills。
安装后校验目录结构和 SKILL.md。
```

### 方式三：手动安装

下载本仓库后，将完整目录复制到以下任一位置。

用户级安装，对所有项目可用：

```text
$HOME/.agents/skills/task-continuity/
```

项目级安装，只对当前仓库可用：

```text
$REPO_ROOT/.agents/skills/task-continuity/
```

目录中必须直接包含 `SKILL.md`，不要多套一层仓库目录：

```text
.agents/skills/task-continuity/
├── SKILL.md
├── agents/
├── references/
└── templates/
```

Codex 通常会自动检测新安装的 Skill。如果 `/skills` 中没有出现
`task-continuity`，请重启 Codex 或新建会话后再试。

安装规范参考：

- [OpenAI：Build skills](https://developers.openai.com/codex/skills)
- [Skills CLI 文档](https://www.skills.sh/docs/cli)

## 使用

### 首次启用：必须显式调用

```text
使用 $task-continuity 为当前项目初始化任务记忆，并根据当前需求建立 Task Contract。
```

如果项目已经进行了一段时间，Skill 不会立即把当前仓库状态写成长期记忆。它会：

1. 读取适用的 `AGENTS.md` / `AGENTS.override.md`；
2. 定向读取 README、设计文档、计划、版本历史、相关代码和测试；
3. 区分用户已确认内容、仓库事实、推断和未知项；
4. 向用户展示拟议的 Task Contract 与 Current State；
5. 等用户确认后创建 `.task-memory`，并向项目 AGENTS 文件加入自动恢复块。

首次显式调用只会启动只读发现与确认流程，不等于立即授权写文件。Skill 会同时
预览 Task Contract、AGENTS managed block 和目标文件；用户确认后才会落盘。

### 后续新会话：自动恢复

项目中已经存在有效的 `.task-memory/HANDOFF.md`，且自动恢复块安装成功后，用户
不需要再次显式调用 Skill。项目指令会要求 Codex 在新会话处理首个项目请求前恢复
HANDOFF，再按引用读取必要的 Decision、Lesson 或 Daily。

用户可以直接说：

```text
继续实现下一步。
```

Codex 只有收到用户消息后才会开始工作；这里的“自动恢复”不是在空白会话中后台
运行，而是在从该项目或其子目录启动的新会话中，处理第一个项目请求前自动完成。
自动恢复依靠项目 AGENTS managed block，而不是单纯依赖 Skill 的模糊语义匹配。
根目录存在非空 `AGENTS.override.md` 时，managed block 写入该文件；否则写入或创建
根 `AGENTS.md`，避免把恢复指令放进下一次运行不会采用的同层文件。

`allow_implicit_invocation` 保持开启，是为了让已经启用的项目能够在后续会话恢复；
它不授权首次隐式启用。没有有效 HANDOFF 时，隐式匹配不会创建记忆或修改 AGENTS。

### 自动恢复的边界

- Codex 会在新运行开始时加载从项目根到当前目录适用的 AGENTS 指令链；Skill 用
  managed block 把“先恢复、再工作”写进这条指令链。
- 检查 HANDOFF、最小化读取和处理冲突，是本 Skill 约定的 Agent 行为，不是后台
  常驻服务；Agent 仍需正确遵循项目指令。
- 若新会话不是从该项目或其子目录启动、AGENTS 指令因冲突或预算未生效，或用户
  尚未发送任何请求，就不能依赖自动恢复。

Skill 会在项目根目录维护：

```text
.task-memory/
├── HANDOFF.md
├── DECISIONS.md
├── LESSONS.md
└── daily/
    └── YYYY-MM-DD.md
```

## 核心规则

1. 普通总结不能改写 Task Contract。
2. 未验证的观察只能进入 Daily，不能直接写入 Lessons。
3. 旧 Decision 不删除；新决策通过 Supersede 保留历史关系。
4. HANDOFF 只保存当前可恢复状态，不保存完整过程。
5. 记忆按事件更新，不在每轮对话后机械写入。
6. 用户当前明确指令始终优先。

详细运行协议见 [SKILL.md](SKILL.md)。

## 当前范围

当前实现以文件协议为核心，不包含数据库、向量检索、后台服务、完整项目管理、
自动修改业务代码或自动创建 Git 提交。
