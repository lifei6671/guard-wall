<!-- TASK_CONTINUITY:BEGIN -->
## Task Continuity

本项目已启用 `$task-continuity`。如果 `.task-memory/HANDOFF.md` 存在且内容可读、必填字段齐全、没有未替换占位符，在每个新会话首次处理非一次性的项目任务前，必须先调用 `$task-continuity` 恢复任务状态；用户无需再次显式要求恢复。

恢复时先读 `.task-memory/HANDOFF.md`，再只按其中的引用读取完成当前请求所必需的 Decision、Lesson、Daily 或项目工件。不要默认加载全部记忆，不要让历史记忆覆盖用户当前明确指令。一次性闲聊或与本项目无关的问题不触发恢复。

如果 HANDOFF 缺失、损坏、字段不完整或仍含模板占位符，不得声称已经恢复；按 `$task-continuity` 的缺失或损坏协议处理。
<!-- TASK_CONTINUITY:END -->

Docker/netns 是 Firewall Integration Test 的权威环境；Disposable Linux VM 是 Host-level E2E 与 Release Gate 的权威环境；WSL2 仅作为开发验证环境，不作为 Release Evidence。

## No Negative Echo

生成最终产物及其包装时，包括标题、文件名、正文、注释、标签、commit、
PR 和交付说明，只描述最终采用的状态，假设读者没看过本次会话。

- 会话里的否决、中间尝试和措辞纠正，只当作控制信息，不要让它们成为最终产物的命名或叙述中心。
- 对每个交付面分别判断：不知道本次会话的读者需要这条信息吗？省略会不会导致不准确、不安全、误导或兼容性信息缺失？它是不是任务开始时已提交或用户确认状态中的真实变化，而且当前交付面需要解释它？
- 「不要提 X」不是让你写「无 X」。标题、文件名、开篇和标签应从正向目标重新生成，不要逐词修改被否文案。
- 保留真实的基线变化、已经执行的外部操作，以及必要的技术名称、诊断、测试和快照。任务开始前已有的用户改动不算被否内容。
- 不要把与本任务无关的改动写进本次 commit、PR 或交付说明。对比、引用、审计和迁移说明，只在用户要求或当前交付面确实需要时保留。
- 写完后通读全部用户可见内容及其包装，包括文件名、元数据和 hook 改写。内容发生变化后重新检查，不要另加「已清理」或「无残留」类声明。

## Validation Environment

Docker Desktop is the canonical Linux validation environment for this project.

During development, use:

`.\scripts\test.ps1 [package]`

Before completing a task that changes Go code, use:

`.\scripts\verify.ps1`

Do not use WSL for build, test, vet, lint, or Linux validation.
Do not construct ad-hoc Docker runners.
Do not mount host Go module/build caches into containers.

If Docker validation is unavailable, report the environment problem instead of switching to WSL.
