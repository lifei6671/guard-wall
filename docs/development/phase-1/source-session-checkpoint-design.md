# Source session 与 checkpoint 持久化方案

状态：最小实施包REVIEW / Implemented，待用户验收；2026-09-04。细化[退休资格草案](source-generation-retirement-design.md)第7节第1项。
用户在详细方案后回复“继续下一步”，授权第8节最小包；实际API与验证见本批交付记录，未授权生产部署。

## 1. 决策摘要

保留主契约§6.1的session-local DeliverySequence：每次新session从1分配。
新增每Source的当前session身份，用同session水位比较替代跨session数字比较。
新session建立时保留旧checkpoint的稳定Position，不把旧Sequence变成新session起点。
generation退休资格仍须有独立的持久完成证明；session切换本身不授予资格。

实施分界：先交付session/checkpoint的Store与Source原语和测试；generation恢复范围证明、引用登记与生产reader接线为后续单元。
第一单元不能使第16条从PARTIAL变为COVERED，不能继续以未绑定session的旧max_delivery_sequence授予新退休资格。

## 2. 实施前依据与约束

- 主契约§6.1、§8.3：Sequence只在当前session内排序；Position和DeliveryID跨重启稳定。
- `internal/store/source_state.go`的AdvanceSourceCheckpoint按Source持久Sequence CAS；RetireFileGeneration直接比较该值与generation最大Sequence。
- `internal/source/checkpoint.go`维护当前session连续完成水位，可从1构造；SQLite适配器目前不绑定session。
- `cmd/guard-agent/main_linux.go`目前只构造Reconcile runtime；本次检查的入口没有Source实例互斥接线。
- 主契约§12启动流程要求单实例锁；不能把此规范当作当前Source生产保证，也不能把数据库session标记当作完整单实例锁。

实际风险：同Source重启后新seq1可能被历史seq100拒绝；反过来，仅放松数值CAS会让过期session候选覆盖新位置。
因此增加session条件属于身份区分所必需，不引入进程存活探测、TTL、租约、分布式锁或自动接管服务。

## 3. 候选持久模型

| 位置 | 候选字段 | 语义 |
|---|---|---|
| sources | active_session_id，可空 | 当前写入session；空仅表示迁移前/尚未开始 |
| source_checkpoints | checkpoint_session_id，可空 | 该checkpoint是谁提交的；历史row可空 |
| source_checkpoints | 既有delivery_sequence/Position | 原值保留；Sequence仅与相同session的候选比较 |

SessionID建议128-bit CSPRNG、小写hex，Source范围绑定；它不进入DeliveryID/EventID，不参与大小排序。
不建session历史表，不维护全局序列，不增加配置。active_session_id与checkpoint_session_id可以不同：
新session尚未提交时，稳定Position仍来自旧session。禁止为“字段看起来一致”而重写旧checkpoint的归属或序号。

Begin必须在一个写事务内更新active_session_id并读取恢复checkpoint；业务调用者不能自行分别读写组装。
新session无任何完成时，旧checkpoint完整行保持；再次crash仍从该Position恢复。

## 4. 候选API操作与状态规则

以下是批准设计的语义描述；实际Go API见第10节。所有Store操作接受context并传播取消/事务错误。

### BeginSourceSession

输入：SourceID、expectedActiveSession（可空）、newSessionID。
输出：绑定Source和SessionID的session handle，以及可选的稳定恢复checkpoint。

- newSessionID由本次启动生成一次。对expected值作CAS，不能无条件覆盖active_session。
- 首次Source要求expected为空；已有Source必须匹配当前active值。竞争失败返回SessionConflict，不自动重试或抢占。
- 同一启动意图在提交结果不确定时可以读回：active等于newSessionID则恢复该handle；active已变为其他ID则返回冲突。
- 读回必须在同一读快照取得active与恢复checkpoint；读回失败保持结果未知，不启动处理。active仍为expected只说明本次未生效，不自动创建或启动session。
- 该读回不是“重新启动session”。已有handle的实时调用者继续其内存Sequence；进程真的重启必须生成新ID并从1开始。
- Begin不是存活探测或接管许可。正常同进程重开要求旧Reader/worker/checkpoint flush均已结束；shutdown timeout后必须退出进程，不能在旧worker存活时Begin下一session。
- 生产重启接线前必须落实契约单实例ownership；仅凭expectedActiveSession匹配不证明旧进程已经死亡。

### CommitSourceCheckpoint

输入：session handle、当前session的连续候选Sequence与类型化Position、提交时间。
仅CheckpointManager/SQLiteStateStore提交候选，不向HTTP/IPC暴露任意Position写入。

事务内先验证Source.active_session_id等于handle；不匹配返回StaleSession，数据库保持原状。

| 已存checkpoint | 候选处理 |
|---|---|
| 不存在，或属于其他/历史session | 接受当前session的首个连续候选，不比较历史Sequence大小 |
| 同session且候选Sequence更大 | 更新 |
| 同session、同Sequence、同Position | 幂等成功，保留原提交时间 |
| 同session较小，或同Sequence不同Position | CheckpointConflict，不更新 |

首个连续候选可以是seqN：只有1..N均已SourceDurable时Tracker才产生它。
新session恢复入口必须使用Begin返回的Position，按恢复计划重新分配Sequence；不能由Store根据offset数值推断跨generation先后。
Store验证Position身份与合法generation状态；无孔推进由可信Tracker/Reader构造保证，不能拿一条任意调用测试证明reader恢复正确。

事务必须把session校验与checkpoint条件写绑定；不得先在事务外查active再写。并发session切换与提交的合法结果：
旧提交先成功则Begin读到该最新Position；Begin先成功则旧提交被拒绝或事务失败，不能覆盖新session。
数据库忙/提交不确定等按现有错误传播，不自动把失败映射为幂等成功。
已确认提交前失败或回滚时原状态保持；提交结果不确定时可能未提交或完整提交，但不能半更新。
调用者通过一致性读回确认；无法确认则保留未知并停止相关推进，不能把Commit返回错误一概解释为回滚。

### LoadSourceCheckpoint

继续返回最后持久Position，并增加其session归属；它可能属于历史session，调用者不得用返回Sequence设定新Tracker起点。
新的SQLiteStateStore实例绑定session handle；CheckpointManager构造仍从1开始。
既有不带session的写入口在迁移完成后不可作为旁路保留，所有生产调用方与测试必须一起更新。

## 5. generation完成证明的边界

session字段只解决身份与CAS；不能证明某个旧generation没有未完成尾部。
后续建议增加每generation持久资格记录，绑定generation、证明session与稳定seal边界，由CheckpointManager权威同checkpoint事务写入。
不能公开提供MarkSafe(bool)，不能以currentSequence >= 历史maxSequence授予资格。

对当前session内seal：需要Reader保证最终读取范围已确定、尾部处理规则已执行，并报告该session完整接受集合的终点；
Tracker跨过该终点且无旧代际孔洞后，才能取得资格。
对重启前未取得证明的generation：需要稳定的恢复起点/最终边界、未完成范围枚举和重新编号规则。
单个跨generationcheckpoint不能自行补出这些信息，必须与reader恢复契约一起设计；本单元不新造“无任务即安全”的兜底。
空generation也要有明确空范围证明。缺失旧inode时保持未获资格并进入既有DataLossSuspected设计范围，不凭空完成。

第一实施单元的保守过渡建议：未具备新资格证明的Sealed generation拒绝新增退休，返回明确的资格未建立错误。
已有Retired row保留历史状态，不复活、不批量删除；仅session迁移不能为其补发已验证证明。
这是实际行为变化，会延后退休及空间回收，须随API/migration实施审批显式确认。

## 6. migration与兼容

新增顺序migration，不修改已执行的0001；通过仓库迁移器完成，具体编号在实施时确认。
新增两个可空字段和格式约束；历史checkpoint/Sequence/Position完整保留，active为空。
首次Begin建立身份，第一次当前session提交接管checkpoint归属；不把旧Sequence归零写回，不批量推断历史资格。
既有Retired且checkpoint仍指向的row仅作为恢复锚点读回；不得向其写新checkpoint，恢复计划须处理后续可用代际。

升级要求旧二进制停机；新Schema上禁止旧二进制继续写入，因为它不携带session条件。
回退方案是恢复升级前一致性备份并使用匹配二进制，不声称可直接降级Schema。备份/停机执行不属于本轮授权。
字段约束、GORM映射、所有写调用点和migration fresh/upgrade测试必须在同一实施单元闭合。

## 7. 验收矩阵

| 场景 | 必须断言 |
|---|---|
| A提交seq100，Begin B | Begin保留checkpoint完整行；B Tracker从1开始 |
| B提交seq1 | 持久归属变B，Position为新连续候选，稳定Delivery/Event身份未变 |
| A延迟Flush | StaleSession；B完整checkpoint与业务表不变 |
| 两个Begin竞争 | 同expected最多一个成功，无自动接管循环 |
| Begin与旧Commit竞争 | 两种合法串行结果；不出现旧写覆盖新session |
| Begin后crash、B首次Commit前crash | fresh process继续读回A的稳定Position |
| 同session乱序/重复/冲突 | 保留孔洞；同值幂等不改时间；非法回退拒绝 |
| 提交前失败、已确认回滚 | 原session/checkpoint全行保持；无半更新 |
| 提交确认丢失 | 原状态或完整提交状态，一致性读回判定；无半更新 |
| Begin读回失败/已被替代 | 保持未知且不启动/明确冲突；不恢复错误handle |
| 旧Schema升级 | 原Position/receipt/业务全行保持；不伪造session或退休资格 |
| 未获资格的Sealed代际 | session变化/数值变大均不能新增退休 |

使用 `scripts/test.ps1` 定向包、`scripts/verify.ps1`、既有Docker integration/Race；命令参数按最终实施范围确定。
设计阶段测试NOT_RUN；矩阵是验收要求，实施后的实际结果以第10节证据为准。

## 8. 待批准的最小实施包

建议批准范围：上述两个session字段的新增migration，Source/Store session绑定API与CAS、调用方适配、
退休资格保守过渡及对应确定性/进程级测试；同步主契约与注释中的持久化规则。
Material影响：跨模块API和数据库升级，旧二进制停机要求、退休暂缓。没有依赖升级、生产部署或配置变更。
排除：生产File/Journald reader、实例锁实现、恢复范围资格证明、replay/reprocess任务owner、retention删除。
这些前置条件未满足前，不能将该原语接入生产Source或宣称第16条完整通过。

本方案第8节最小包已获实施授权；实现交付仍需验证与独立审查。C1、第16条覆盖以及G18/M0状态保持。

## 9. 设计核对

独立代理multiparser_reviewer核对设计，指出提交报错与回滚不能等同；已细分确认回滚、提交结果未知及读回失败验收。
上述设计阶段仅说明性文档，未运行代码测试；当时`git diff --check`通过。设计核对不授予用户验收。

## 10. 实施对应

实际交付入口：`artifacts/evidence/M0/worktree/m0-c/source-session-checkpoint/README.md`。

- `store.SourceSessionID`及`NewSourceSessionID`生成身份；`SourceSession`私有字段，提供SourceID()/ID()读访问。
- `BeginSourceSession`以expected/new身份切换并返回handle、恢复checkpoint和found。
- `LoadSourceSessionState`同快照返回active身份与checkpoint；`ConfirmSourceSession`只确认同一启动意图。
- `AdvanceSourceCheckpoint`改为接受handle，承担本设计CommitSourceCheckpoint语义；原无session签名已替换。
- `NewSQLiteStateStore`增加必需的session参数；适配器拒绝跨Source写入，Tracker仍由调用方按新session从1创建。
- migration `0007_source_sessions.sql`新增两字段；新Sealed退休请求在保留状态/引用检查后返回资格未建立。

旧driver Commit错误未做真实注入；确认丢失测试以丢弃成功Begin返回值验证读回。原生Journald在当前Docker容器缺journalctl，
该项本批UNAVAILABLE；通用游标/会话测试不替代原生采集证据。生产Source接线与第16条完整资格仍未交付。
