---
name: docs-sync
description: Synchronize XTunnel Standalone technical contracts, development-plan status and evidence, README, and durable project rules after verified code, protocol, config, API, deployment, test, or Gate changes. Use for XTunnel documentation/status updates and consistency audits; do not use to mark unverified work complete or to implement the work itself.
---

# Docs Sync for XTunnel Standalone V0.1

## 目标与边界

在 XTunnel 的实现、契约、验证证据或里程碑状态变化后，同步技术基线与开发执行计划，保证“实现、机器契约、总方案、进度和验收证据”一致。

本 Skill 负责文档影响分析、最小必要修改和进度回写，不替代功能实现、契约冻结、代码审查或 Gate 验收。

## 先验证当前事实

每次使用本 Skill 时，先检查当前工作区、实际文件和执行计划，不要把计划目录树、规划命令或历史任务数当成已落盘事实。

```powershell
git status --short
git diff --stat
git diff -- <relevant-path>
git diff --cached -- <relevant-path>
rg --files
```

然后读取：

- `docs/xtunnel_standalone_v0.1_development_plan.md` 的仪表盘、相关 Task ID、Gate 和执行记录。
- `docs/xtunnel_standalone_v0.1.md` 中与改动直接相关的契约章节。
- 修改路径所在的 Proto、Schema、OpenAPI、README、CI 或部署文档（如果真实存在）。

当前基线可能变化：每次从任务表重算里程碑任务数和 `DONE` 数，不要把 95 或其他历史数字硬编码成永久常量。`PLAN-BASELINE=DONE` 属于执行记录，不是产品任务。

## 分领域权威来源

1. `docs/xtunnel_standalone_v0.1.md`
   - V0.1 产品边界、架构、安全不变量、行为语义和发布 Gate。

2. `api/proto/common.proto`、`control.proto`、`work.proto`
   - M0.5 后 Protocol v1 的唯一 Wire Contract。
   - 字段、enum、reserved 和 message 方向先改 Proto，通过 Protocol Review、Buf Gate 和 Golden Vector 后，再同步总方案的语义镜像。

3. `configs/server.schema.json`
   - Server 配置字段、类型、默认值、范围、Secret 和热加载属性的唯一机器权威。
   - Agent 没有本地业务 Schema/YAML；Connection Token Wire 以 Proto 为权威，Bootstrap Source Precedence 与输入形状由实现和契约测试证明。
   - 总方案的 YAML/限制表只能作为人类可读镜像。

4. `api/openapi/openapi.yaml`
   - M5 后 REST Request/Response、Required/Nullable、Pagination、PATCH、ETag、Error Schema 和 HTTP Status 的唯一契约。
   - Handler 和 Web 不得使用手写 DTO 反向定义 OpenAPI。

5. `docs/xtunnel_standalone_v0.1_development_plan.md`
   - 任务、依赖、状态、Gate 和执行证据的唯一跟踪入口。

6. `README.md` 与已存在的部署/运维文档
   - 只在用户可见能力、启动方式、必需配置、端口、安装或维护命令改变时更新。

如果代码与已冻结契约不一致，默认视为实现漂移。不得自动把 Proto、Schema、OpenAPI 或总方案改成迁就当前代码。先报告冲突；如确需改契约，按项目边界先取得公共 API、Protocol、数据库、依赖或配置变更的授权。

## 何时使用

- 用户明确要求同步总方案、开发计划、README、Gate 或任务状态。
- M0、M0.5、M1—M7 任务状态或 Entry/Exit Gate 证据变化。
- Proto/Protocol Runtime/Golden Vector、Config Schema/Loader、OpenAPI/REST/Web Client 契约变化。
- SQLite/Migration/Lock/Journal/Backup/Restore 行为变化。
- Server/Agent 身份、Session、WorkPool、Tunnel、Route、Ingress、Health、状态聚合或资源边界变化。
- Web 用户流程、登录/CSRF、Agent/Service 管理、状态展示或错误处理变化。
- Tools/CI/Deploy/OCI/systemd/Caddy/Nginx、维护命令、验证矩阵或支持边界变化。
- 需要对文档、机器契约和开发计划做一致性审计。

## 何时不需要

- 纯重构，且不改变外部行为、契约、持久化、部署、验证方式或任务状态。
- 只调整测试内部结构，不改变验收矩阵或 Gate 证据。
- 局部 typo、格式化、一次性日志或未采用的失败实验。
- 仅有方案、TODO、Schema/Fixture 骨架或生成代码，但任务要求的真实路径尚未实现和验证。这类情况可记录进展/风险，不得标记 `DONE` 或 Gate PASS。
- 普通实现任务本身；本 Skill 不扩大用户的实现授权。

## 改动映射

| 改动范围 | 同步目标 |
| --- | --- |
| `api/proto/`、`internal/protocol/`、`tests/golden/protocol-v1/` | Proto 权威、总方案 Protocol 语义镜像、M0.5/M1/M3 任务 |
| `configs/server.schema.json`、Server Config Loader、Limit/Timeout | Server JSON Schema 权威、Go Config/示例、总方案镜像、M0/M1/M3/M4/M7 任务 |
| Agent Bootstrap、Connection Token Input | Proto 权威、Bootstrap 实现/契约测试、总方案 Token-only 语义、M0/M1/M7 任务 |
| `migrations/`、`internal/repository/`、Lock/Journal/Backup/Restore | 总方案持久化/恢复语义、M0/M1/M3/M7 任务 |
| Agent Gateway、Session、WorkPool、Tunnel Proxy | 总方案 M1/M2 语义、并发/泄漏证据、M1/M2 任务 |
| Snapshot、Revision、ConfigAck、Origin、Health | 总方案 M3 语义、Crash/Health 证据、M3 任务 |
| Route、HTTP/TCP Ingress、WebSocket、Listener | 总方案 M4 语义、产品数据面 E2E、M4 任务 |
| `api/openapi/`、Server API、`web/src/api/`、`web/src/` | OpenAPI 权威、Generated Contract、REST/Web 语义、M5 任务 |
| Logging/Metrics/Trace/Usage | 总方案可观测性语义、M6 任务 |
| Benchmark/Fuzz/Chaos/CI/Deploy/Release | 总方案测试/发布 Gate、可复现 Artifact、M0/M7 任务 |
| `README.md` 和已存在的部署/运维文档 | 仅同步已实现且用户可见的能力、配置、命令和限制 |

映射中出现的计划路径不等于当前文件已存在。更新前必须用 `Test-Path`/`rg --files` 确认。

## 同步流程

1. **确认改动边界**
   - 区分实际修改、用户的无关 dirty changes、staged 和 unstaged 差异。
   - 保留用户变更。若目标文件同时有 staged/unstaged 差异，基于工作区最新内容编辑，交付时提醒重新暂存；不擅自改变 staging。

2. **定位 Task ID**
   - 用路径、产物、依赖和验收要点联合判断，不要只用关键词猜测。
   - 一次改动可影响多个任务，但只更新有直接证据的状态。

3. **判断契约影响**
   - 行为符合已有契约：仅回写计划和证据。
   - 已授权改变长期行为：先更新所属的唯一权威源，再做最小派生文档同步。
   - 实现与契约冲突：停止自动同步，报告冲突并请求决策。

4. **回写任务状态**
   - `READY`：依赖全部 `DONE`，输入契约已冻结。
   - `IN_PROGRESS`：已有负责人和工作分支。
   - `REVIEW`：产物已完成，任务规定的验收命令已执行。
   - `DONE`：产物、关键断言、失败分支、复审和验收证据齐备，无未处理的安全/协议/数据一致性/资源泄漏问题。
   - `BLOCKED`：写清原因、影响任务、需要谁提供什么和解除条件。
   - M0-10 完成前，`DONE` 可使用 Commit SHA + 干净 checkout 本地命令记录；M0-10 完成后，新 `DONE` 必须再附 CI Run 证据。
   - Gate 只有在全部前置任务和 Gate Checklist 都通过时才可 `DONE`。

5. **同步计数和执行记录**
   - 根据当前任务行重算每个里程碑的任务数、`DONE` 数和总数。
   - 追加 Task ID、负责人、Commit/PR、产物、命令、结果、剩余风险和解锁任务。
   - `VALID`、文件存在、可编译、浅断言、手工 Smoke 或单一 happy path 不等于产品任务/Gate 完成。

6. **验证**
   - 优先从当前 `AGENTS.md`/override、Makefile/justfile、`go.mod`、`package.json`、CI 和 README 查找真实命令。
   - 不得因为执行计划写了某个规划命令，就宣称该命令已落盘或已通过。
   - 代码、Proto、Schema、OpenAPI、Web、部署或测试有变更时，执行对应任务/Gate 定义的最小真实验证。
   - 仅修改 Skill/文档时，至少运行 Skill Validator（如可用）、`git diff --check`、本地路径检查和 Task ID/仪表盘一致性检查。
   - 无法运行某项验证时，记录原因、影响和剩余风险，不得改写成 PASS。

## 常见误判

- 把“已进入 M0”误当成 M0 已有完成项。
- 把 `READY` 误改为 `DONE`，或把 `PLAN-BASELINE=DONE` 算入产品任务。
- 把总方案中的目录树、命令和 Protobuf 片段当成真实产物。
- 把 Schema `VALID` 当成 Config Loader Gate，把 Proto 能生成当成 M0.5 Gate。
- 首次 Buf Breaking 与当前文件自比较，产生虚假 PASS。
- 把 OpenAPI Validate 当成 Handler/Client/Web 零漂移。
- Golden Fixture 和被测实现由同一路径同时生成，只证明自洽。
- 在脏工作区运行过命令，却记录成干净 checkout/CI 证据。
- M0-10 完成后仍只记录本地结果，或 M05-10 未 `DONE` 就开始 M1 Protocol Handler。

## 禁止事项

- 不得没有证据就更新 `REVIEW`/`DONE` 或勾选 Gate。
- 不得为迎合当前实现而静默弱化总方案、Proto、Schema、OpenAPI 或 Release Gate。
- 不得在总方案与 Schema/Proto/OpenAPI 中独立维护两份机器默认值或 Wire/API 字段。
- 不得把计划中未落盘的文件、脚本或命令写成已存在，也不得凭空发明验证命令。
- 不得提交密钥、Token、密码、Cookie、Private Key、真实 Pin 或私有配置；Golden Fixture 只使用明确测试密钥。
- 不得擅自更改 staging、清理无关 dirty worktree、手工修改锁文件或纳入意外副产物。
- 不得把一次性排查结论写入长期项目规则，除非已被明确裁定为长期不变量。

## 输出要求

最终回复必须说明：

- 实际检查的改动范围。
- 更新了哪些权威契约、文档、Task ID、仪表盘和执行记录。
- 哪些候选文档判定无需更新，以及原因。
- 实际运行的验证命令和结果；未运行项及原因。
- 哪些 Gate 尚未实际通过、剩余风险和阻塞项。
- 如果没有产品任务达到 `DONE`，明确写“本次未勾选任何产品任务”。
- 如果目标文件有 staged/unstaged 差异，提醒用户提交前重新暂存最新版本。
