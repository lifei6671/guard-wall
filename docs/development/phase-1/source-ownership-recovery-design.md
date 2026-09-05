# File Source 运行所有权与重启恢复

状态：单元一已获用户验收，DONE / Implemented，2026-09-05。
Reconcile借用Store、Agent最终关闭已获授权并实施，REVIEW / Implemented；Source借用Store与关闭超时交接已实施并获用户验收，DONE / Implemented，生产组合与恢复后续单元为Proposed。
承接D-015保留策略、D-016维护接入点和[清理接口设计](source-cleanup-interface-design.md)。
单元一实施、验证与独立审查见STATUS第185项及artifacts/evidence/M0/worktree/m0-c/agent-instance-lock/。

## 当前依据

- 主契约§12要求加载配置后取得单实例锁，再打开数据库；§6.2要求恢复全部非Retired代际。
- `cmd/guard-agent/main_linux.go`的runGuardAgent加载配置后取得目录锁，再openStore；Agent持有Store最终关闭权。
- `internal/reconcile/runtime.go`的Run借用Store并等待内部组件结束后返回，由Agent关闭Store，再释放锁。
- `internal/processor/source_intake_runtime.go`的RunSourceIntakeRuntime负责排空与维护，Store由调用者最终关闭。
- Source session保护checkpoint写入；Coordinator的flight保护单实例内相同Delivery，不覆盖第二个进程或Coordinator。
- `internal/store/source_state.go`的登记入口允许新增缺失行，恢复查询排除Retired；它们不负责识别磁盘上的历史文件。

Agent/Reconcile当前路径已统一最终关闭所有权；Source共用数据库的生产接线仍须遵循下方组合顺序。

## 实施单元一：Agent单实例准入

Linux guard-agent进程锁落实§12启动顺序。锁在openStore之前取得，
覆盖migration、NodeID、Policy bootstrap及整个runtime。竞争者立即返回可识别错误，且不打开数据库。

使用配置中DatabasePath所属目录的目录fd进行非阻塞独占flock。
锁粒度是数据库目录：同目录的两个数据库也互斥，用户已接受此行为。
复用仓库IPC listener已有的目录fd flock用法，新增实现留在cmd/guard-agent，不建立通用锁包。

内部接口：

```go
func acquireAgentInstanceLock(databasePath string) (io.Closer, error)

var errGuardAgentAlreadyRunning = errors.New("guard-agent is already running")
```

直接以目录文件句柄实现io.Closer，Close释放锁。
runGuardAgent增加内部可注入acquire函数参数，位置在loadConfig之后、openStore之前；main使用真实实现，测试使用桩。
不修改Store.Open公共契约。该锁约束使用同一准入路径的Agent，不能阻止绕过Agent的直接SQLite写入。

路径边界：使用现有绝对DatabasePath，父目录必须已存在且可打开，不自动创建或修改权限。
锁绑定实际打开的目录对象；该数据库及父目录在运行期间必须保持身份稳定。
同一数据库经不同目录的硬链接、运行中替换数据库/目录及外部直接写入不属于此机制可证明的排他范围。
上线前须由部署目录约束与真实路径验证支持这些前提，不能只做字符串路径比较后声称所有别名均受保护。

正常退出仅在runtime全部数据库使用结束并完成Close后释放锁，释放错误与原错误合并。
启动失败时，按数据库Close后释放锁的逆序清理。锁获取失败不能进入openStore。
进程被SIGKILL后的锁释放由操作系统持有的句柄生命周期决定，须以独立进程回归验证。
此单元接入当前Reconcile运行路径；未来Source超时返回且仍有工作未结束时，必须保留锁直到进程退出，
不能复用普通defer提前解锁并让第二个实例开始写库。

最小变更清单：main_linux.go内部注入及顺序、agent_instance_lock_linux.go、对应单元与Linux子进程测试。
不新增配置字段、依赖或数据库migration；不改变systemd文件。现有Agent启动失败测试同步适配。

## 实施单元二：共享数据库的最终关闭所有者

生产接入Source时，由Agent组合层持有唯一Store关闭权，同时持有进程锁。
Source与Reconcile作为子运行组件返回各自处理结果；它们不能独立关闭共享Store。
独立调用者须将最终Close迁移到对应运行所有者，保留结束时恰好关闭一次的行为。

### Reconcile借用Store，Agent最终关闭

本包已获用户确认并实施；验证与独立审查见STATUS第186项及artifacts/evidence/M0/worktree/m0-c/agent-store-ownership/。
当前Agent/Reconcile调用链：

```go
// Run等待内部组件结束后返回；Store由调用者关闭。
func (r *ReconcileRuntime) Run(ctx context.Context) error
```

Run签名保持，关闭责任从被调用方迁移到调用方；RuntimeStore接口移除Close方法要求，
保留现有读写事务能力。Run仍维护running/stopped状态、拒绝并发Run与停止后复用，
启动失败和运行失败仍等待已经启动的内部组件退出后返回原错误。

Agent的agentStore继续要求Close；runGuardAgent在成功Open后注册唯一defer，
覆盖NodeID初始化、runtime构造、Policy bootstrap及Run全部路径。
删除storeOwned转交分支；runtime返回后执行Store.Close，再执行既有目录锁Close。
Store.Close错误与已有运行错误合并，保留各自errors.Is身份；正常取消也不能掩盖Close失败。
本包沿用当前Reconcile等待组件返回的行为，不新增超时强制返回或未结束组件的提前解锁路径。

实际调用者与适配范围：

| 位置 | 改动 |
|---|---|
| internal/reconcile/runtime.go | RuntimeStore移除Close要求；Run保留组件等待与状态转换，返回运行结果；同步所有权注释 |
| cmd/guard-agent/main_linux.go | Agent始终持有Store最终Close责任；同步guardAgentRuntime接口注释 |
| internal/reconcile/runtime_test.go | runtime返回时断言Close零次且内部组件已停；独立测试调用方随后Close一次；保留停止、失败和健康恢复断言 |
| cmd/guard-agent/main_linux_test.go | 测试runtime返回结果，由Agent关闭；保留真实SQLite与NodeID持久化验证 |
| cmd/guard-agent/agent_instance_lock_linux_test.go | runtime桩不再关闭Store；最终关闭与锁释放顺序、错误合并断言保持 |

实现时核对直接调用者闭包；若发现实际运行调用方仍依赖runtime.Close，则一并迁移其最终Close。
测试不以删除关闭断言完成适配：组件停止与Close一次的断言分别放在实际负责的层级。
无需新增RunShared方法、Close选项或空Close适配器；现有独立运行调用方仍有明确的最终关闭者。

验收条件：

1. Reconcile正常取消、启动失败、运行失败后，Dispatcher/expiration/health工作已结束，Store仍可由调用者使用。
2. Agent在所有成功开库路径最终Close恰好一次；失败开库路径不调用Close，目录锁直到清理结束仍被持有。
3. 运行错误、Store.Close错误和锁Close错误的组合均完整传播，包括取消与清理失败共存。
4. 真实SQLite验证Run返回后仍能读库，由Agent结束后可重新打开并保持NodeID；真实进程锁接替回归保持。
5. Agent/Reconcile定向测试与Race、仓库verify、独立交付审查通过。

导出内部Run行为及RuntimeStore跨模块契约变更已获用户确认。本批实现待用户验收。
第186项范围为Agent/Reconcile；Source接口迁移见下一节，Reader生产接线、维护调度及物理清理另按后续组合阶段推进。

### Source借用Store与关闭超时交接

状态：DONE / Implemented。用户已明确通过本节实现验收；
由直接调用者承担关闭责任。接口说明同步于source-maintenance-integration-design.md，证据见STATUS第187项及artifacts/evidence/M0/worktree/m0-c/source-store-ownership/。

当前接口：

```go
func RunSourceIntakeRuntime(
    ctx context.Context,
    shutdownTimeout time.Duration,
    reader source.Reader,
    queue *source.DeliveryQueue,
    coordinator *Coordinator,
    checkpoints *source.CheckpointManager,
    maintenance func(context.Context) error,
) error

var ErrSourceIntakeShutdownTimeout = errors.New("run source intake runtime: shutdown timeout")
```

仅移除database io.Closer参数与内部Close。runtime继续负责Reader停止、队列Seal、worker排空、Flush和现有维护回调。
调用者通过Coordinator等原有依赖提供Store使用能力；runtime不取得最终关闭权。
参数错误时尚未启动工作，由原所有者处置资源；不新增运行入口或关闭选项。

关闭预算耗尽时，返回错误同时匹配ErrSourceIntakeShutdownTimeout与context.DeadlineExceeded，
并保留已经取得的Reader、worker、Flush或维护错误。原sourceIntakeShutdownTimeout的调用位置保持：
等Reader/worker超时、Flush前预算无效、Flush返回后预算耗尽、维护返回后预算耗尽。
该标识只表达本次Source关闭预算耗尽，不把所有DeadlineExceeded都转换成它。

| 返回情况 | 调用者职责 |
|---|---|
| 正常完成，或组件错误但关闭预算有效 | 当前Source的工作已返回；独立运行调用者Close一次并合并Close错误 |
| Reader/worker/Flush/维护自身返回DeadlineExceeded，关闭预算仍有效 | 保留普通错误，不赋予专用标识；按已结束路径处理Close |
| 匹配ErrSourceIntakeShutdownTimeout | 跳过Close，保持已有的进程退出要求；进程锁保留至退出，不复用该Store |
| 参数校验失败 | runtime未启动工作；资源仍由调用者持有和清理 |

调用者用errors.Is检查专用标识；普通错误与Close错误通过errors.Join合并。
专用标识是本runtime的生命周期结果，传入组件不主动构造该保留标识。
若其他共享用库组件仍在运行，调用者必须先结束它们；本Source正常返回本身不授予关闭整个共享Store的资格。

维护回调仍仅在本Source无前置错误且Flush成功时同步执行，共享同一剩余shutdown deadline。
该回调保持D-016范围：它证明本次Source的维护窗口，不证明其他共享组件已停止。
生产Source接线时再落实下方组合顺序，包括Source失败导致回调跳过时也要停止其余组件。
本包不将Source接入guard-agent，因此不会让现有Agent的无条件最终Close接收这种未结束状态。

实际变更范围：

- internal/processor/source_intake_runtime.go：移除Close及database参数校验，增加专用超时身份并同步注释。
- internal/processor/source_intake_runtime_test.go、source_maintenance_test.go：调用者接管Close，保留排空/停止/回调顺序及次数断言。
- 相关接口与交付文档：同步新所有权；既有冻结证据保留各自历史快照。

RunSourceRuntime继续保持现有接口；本包不调整生产Reader、配置、数据库结构或清理事务。
实施时核对直接调用者并一起适配，避免使用空Close替代真实资源生命周期。

验收要求：

1. 正常结束、受控取消、Reader/worker失败：runtime返回时Store未关闭，调用者关闭一次，原顺序与业务数据断言保留。
2. 普通组件DeadlineExceeded与关闭预算耗尽分别构造；验证专用身份、全部原始错误身份与Close次数。
3. Reader/worker等待、Flush、维护耗尽预算：专用身份与DeadlineExceeded同时成立，Close零次。
4. 回调至多一次，前置失败跳过，共享剩余deadline；回调未返回时不得关闭数据库。
5. 普通错误与调用者Close错误合并；真实SQLite返回后仍可读，owner关闭重开后checkpoint与receipt保持。
6. Processor定向普通/integration回归、相关Race及仓库verify通过，再做独立交付审查。

导出内部函数参数与错误契约变更已获确认并实施；本批用户验收已通过，见source-store-ownership/acceptance.md。
第186项仍为REVIEW / Implemented，用户验收单独记录。

下一接口实施包见[Source与Reconcile共享关闭预算](source-shared-shutdown-design.md)，状态Proposed。

### 生产组合的结束顺序

完整Source接线时，结束顺序确定为：

```text
禁止新Source运行及外部投递
  → Reader停止全部sink调用
  → Seal队列、worker正常排空、Flush
  → 停止并等待共享Store的其余组件
  → 条件满足时维护
  → 唯一所有者Close Store
  → 释放进程锁
```

所有组件共享剩余shutdown预算。任何未完成数据库使用的超时都转为进程退出路径，
跳过维护并保留锁；不在仍有数据库使用时强行Close。错误、取消和正常排空分别报告。
进程锁解决跨进程准入；同进程的所有Source、Coordinator和维护入口须由该组合层实际持有和约束。
SQLite事务继续负责资格检查与删除串行化，两者不能互相替代。

## File发现与恢复决策表

生产Reader须先读取持久代际集合和coverage，再对打开的文件句柄进行身份核对。
配置路径用于寻找文件，持久generation用于Delivery身份；路径、inode或新随机ID都不能单独证明日志内容是新内容。

| 输入事实 | 规定行为 |
|---|---|
| Open/Draining代际且文件身份有可靠连续性依据 | 沿用generation，从持久durable_end_offset恢复；receipt重放沿用原Delivery ID |
| coverage未知 | 保留未知值并报告恢复依据不足；不猜测offset或建立完成证明，未知值本身不等于数据丢失 |
| 找不到旧inode、原字节无法恢复 | 保留恢复记录，按主契约报告DataLossSuspected Audit/Health；已提交receipt仍可证明处理结果，但不能声称缺失字节已恢复 |
| 运行中持有旧fd，明确观察到rename/create | 旧代际Draining，新代际先持久化再投递；旧fd继续履行原读取责任 |
| 检测到截断 | 沿用D-014停止标记和DataLossSuspected报告；另经已确认轮转规则建立新代际 |
| 已Sealed/Retired旧代际仍被发现 | 不复活代际；身份明确时不从零投递，身份不明确时报告并停止该Source的自动发现 |
| 重启后未登记文件或疑似inode复用 | 必须依据已确认的新文件准入规则分类；证据不足时报告歧义并停止该Source的自动发现 |

停读属于可观测的恢复失败，不能伪装为成功跳过。其他Source是否继续由组合层既有故障策略决定。
恢复集合仍包含全部非Retired代际；Sealed代际用于恢复核对和生命周期收尾，不产生新的RawRecord。
首次部署的新文件准入与重启后未知文件不同，不能在每次重启时自动重新套用首次部署规则。
首次读取起点、轮转文件搜索范围与可接受的身份连续性依据应随生产Reader契约一次确认，不能由清理模块决定。

## 删除后的证据与可执行范围

receipt清理仍须保留代际完整coverage和终态，Reader及处理准入须共同拒绝已完成范围的旧投递重新进入prepare。
仅Reader跳过旧范围不足以防止另一路Coordinator投递；准入规则必须覆盖组合层全部处理入口。
清理后的重复投递不能伪造已删除receipt的DurableCompletion，具体拒绝结果应在处理接口变更中定义。

registry物理删除需要额外证明：剩余持久状态或受约束的日志生产/发现协议能区分已清理文件与真正新文件。
在允许任意旧文件重新出现、又没有剩余身份依据的输入域中，现有字段不能提供这种区分。
因此该输入域的registry继续保留；第16条物理清理保持PARTIAL。这是当前可执行范围限制，不是清理成功。
若需要扩大到该输入域，须选择并评估具体发现协议或持久身份方案，再进行Contract Change Review。

## 验收与推进

先实施单元一，其独立验收包括：

1. 子进程持锁时第二Agent在openStore前失败，数据库与session无变化。
2. 正常退出、启动错误和SIGKILL之后，新进程可接替；锁获取失败和释放错误保持可识别。
3. 同进程独立打开、同目录不同数据库、目录路径别名符合约定的互斥粒度。
4. 目录缺失/不可访问、取消与既有Agent bootstrap失败路径保持正确清理顺序。
5. 当前Agent/Reconcile包回归及仓库verify通过。Docker使用仓库test.ps1/verify.ps1；生产路径另需目标Linux证据。

单元二与Reader接线后，再验证维护与第二运行实例交错、旧文件重现、新文件正常进入、
跨session恢复、inode歧义、删除事务提交前后SIGKILL及完整业务行保持。
两个30天期限、外键保护、事务失败与提交不确定仍按清理接口设计独立验证。

单元一已完成Agent包测试、Agent/Reconcile Race与全仓verify，具体命令与运行范围以证据入口为准。
用户已明确通过单元一；关闭所有权批次的Agent/Reconcile定向测试、Race及全仓verify通过。
Source借用Store接口已实施；生产组合与恢复后续单元仍为设计，未运行相应Reader、清理或Host E2E验证。
主契约与既有验收状态保持。
