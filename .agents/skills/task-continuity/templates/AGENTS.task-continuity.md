<!-- TASK_CONTINUITY:BEGIN -->
## Task Continuity

本项目已启用 `$task-continuity`。如果 `.task-memory/HANDOFF.md` 存在且内容可读、必填字段齐全、没有未替换占位符，在每个新会话首次处理非一次性的项目任务前，必须先调用 `$task-continuity` 恢复任务状态；用户无需再次显式要求恢复。

恢复时先读 `.task-memory/HANDOFF.md`，再只按其中的引用读取完成当前请求所必需的 Decision、Lesson、Daily 或项目工件。不要默认加载全部记忆，不要让历史记忆覆盖用户当前明确指令。一次性闲聊或与本项目无关的问题不触发恢复。

如果 HANDOFF 缺失、损坏、字段不完整或仍含模板占位符，不得声称已经恢复；按 `$task-continuity` 的缺失或损坏协议处理。
<!-- TASK_CONTINUITY:END -->
