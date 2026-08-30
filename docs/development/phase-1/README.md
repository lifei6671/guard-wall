# Guard Phase 1 开发执行入口

本目录把 Phase 1 技术规格转换为可推进、可标记、可验证的执行视图。
它不重新定义技术语义，也不替代代码、Schema、migration、ADR 或测试。

## 1. 三份文件的职责

| 文件 | 职责 | 是否维护进度 |
|---|---|---:|
| [Phase 1 Contract](../../contracts/guard-phase-1-m0-contract-freeze-v0.3.md) | 唯一规范基线：状态机、不变量、接口边界、里程碑 Gate | 否 |
| [STATUS.md](STATUS.md) | 唯一可变进度源：当前阶段、工作包状态、Blocker、证据链接 | 是 |
| [M0-EXECUTION.md](M0-EXECUTION.md) | M0-A–M0-D 的执行方法、责任产物、完成条件和 Gate 判定规则 | 否 |

架构范围仍以
[总体技术方案 V0.4](../../guard-distributed-log-driven-host-protection-system-technical-design-v0.4.md)
为基线。Contract 明确修正 V0.4 的部分，按 Contract 的权威规则处理。

## 2. 实时状态入口

当前阶段、准入结论、可启动项、Blocker 和下一步只在 [STATUS.md](STATUS.md) 维护。
README 和 Contract 不复制这些实时值；开始任何工作前必须读取 STATUS。

文档存在本身只证明规范已经落盘，不能代替 Fake Slice、Spike、migration、Schema、
ADR、Contract Tests 或 Evidence Manifest。

## 3. 状态模型

执行进度与证据成熟度分开记录。

### 3.1 推进状态

```text
BLOCKED → READY → IN_PROGRESS → COMPLETE
```

- `BLOCKED`：Entry Gate 或依赖未满足。
- `READY`：可以开始，但尚无实际实现工作。
- `IN_PROGRESS`：已有实际产物，完成条件尚未满足。
- `COMPLETE`：本工作包完成条件全部满足；仍不自动等于里程碑 Frozen。

### 3.2 证据状态

沿用 Contract 第 1.2 节：

```text
Specified → Implemented → Verified → Frozen
```

- 没有可定位证据，不得标记 `Verified`。
- 只有里程碑全部必需工作包 Verified 且 Exit Gate 通过，才可标记 `Frozen`。
- `NOT RUN`、失败或缺少隔离环境的必测项都会阻塞 Gate。

## 4. 标准推进流程

1. 在 [STATUS.md](STATUS.md) 确认工作包为 `READY`。
2. 打开 Contract 对应章节，只引用条款，不把技术定义复制进任务文件。
3. 将状态改为 `IN_PROGRESS`，登记负责人、分支或任务链接。
4. 实现 Contract 要求的代码、ADR、migration、Schema、测试或 Spike。
5. 运行精确验证命令，把结果写入 `artifacts/evidence/<milestone>/<commit>/`。
6. 在 `STATUS.md` 登记 Evidence 路径，并按工作包完成条件更新状态。文档审查和
   preliminary Spike 通常最多达到 `COMPLETE / Implemented`；只有所需自动化和
   目标环境证据齐全后才能标记 `Verified`。
7. 里程碑全部工作包通过后执行 Exit Gate；Gate 通过才能解锁下游里程碑。

## 5. Evidence 最小要求

每份 Evidence Manifest 至少记录：

- milestone、work package、commit；
- 执行环境和依赖版本；
- 精确命令、开始/结束时间；
- passed、failed、skipped、not run；
- 失败摘要、已知限制；
- 产物路径与 checksum；
- reviewer 和复核时间。

`测试文件存在`、`Schema VALID`、`编译成功` 都不能单独代替 Gate 通过。

## 6. 更新边界

- 进度、负责人、Blocker 和 Evidence 只更新 `STATUS.md`。
- 技术取舍更新 Contract/ADR，不在执行文件中另写一套语义。
- 数据库结构只更新 migration，配置字段只更新 Config Schema。
- HTTP API 以 OpenAPI 为准，CLI 以 Contract/golden tests 为准。
- 修改 Frozen Contract 必须走 Contract Change Review，并重新运行受影响 Gate。
- 不创建 43 个独立 Work Package 文档；只有确有独立设计或审计价值时才新增 ADR/Contract。

## 7. 开始工作的入口

1. 在 [STATUS.md](STATUS.md) 选择推进状态为 `READY` 的工作项。
2. 在 [M0-EXECUTION.md](M0-EXECUTION.md) 或 Contract 中确认依赖、产物和完成条件。
3. 开始后立即在 STATUS 登记 `IN_PROGRESS`、负责人/任务和 Evidence 目标路径。
