# File Source 清理接口设计

状态：Proposed，2026-09-04；D-015保留期限已接受，以下接口与生命周期衔接待确认。

## 目标与最小范围

只清理明确指定Source/generation的数据库恢复记录，按D-015分别执行两个30天最短期限。
采用单代际目标，暂不提供全库扫描、批量调度或原始日志文件删除。
业务引用存在时保留记录；不代替Parser、Detection、Alert、Decision及Audit的历史清理。

## 当前接入前置条件

`internal/processor/coordinator.go:134-151`先读取receipt；不存在时调用prepare。
`internal/store/source_state.go:282-315`在generation不存在时允许登记，存在时核对不可变身份与Open状态。
当前保留receipt/registry的路径有相应保护；以上代码本身不构成已证实的重复副作用故障。
但引入删除后，不能仅依赖“receipt不存在”区分首次投递与已清理旧投递，也不能仅凭缺row证明是新文件。

安全闭合需要证明两件事：

1. 目标代际没有仍可能执行的旧投递；清理和新的处理准入不会交错破坏此事实。
2. 重启只恢复仍需处理的代际；新文件登记不复用已经清理的旧代际身份。

现有Store事务可保护数据库资格与写入，不独自拥有Source读取、队列、Coordinator pending与文件发现生命周期。
因此不接受调用方传入`safe=true`或仅凭session ID授予删除资格。
生产File reader尚未接线，本设计不能把未实现的生命周期保证当作现有事实。

独立核对确认`internal/processor/source_intake_runtime.go:93-164`已有Reader结束、Seal队列、
等待worker、Flush checkpoint、Close数据库顺序；`internal/source/reader.go:14-18`要求返回前结束sink调用。
第184项已实现D-016维护接入点：正常排空和Flush成功后、Close之前同步运行maintenance。
接入点共享剩余shutdown deadline；实现及验证入口为source-maintenance-integration-design.md。
它提供本次运行的维护窗口，跨运行所有权与下一次File发现/恢复的删除前提仍待证明。

## 候选接口形状

以下为拟议签名，不是本轮新增的Go代码，也不是已经可调用的API：

```go
func (s *Store) PruneFileGenerationReceipts(
    ctx context.Context, sourceID core.SourceID, generation string,
) (FileCleanupResult, error)

func (s *Store) DeleteRetiredFileGeneration(
    ctx context.Context, sourceID core.SourceID, generation string,
) (FileCleanupResult, error)
```

FileCleanupResult拟包含一个结果状态与删除行数。结果区分Deleted、Retained、Absent；
Retained附闭集原因，例如NotDue、RecoveryRequired、Referenced，不能把数据库错误包装成保守跳过。
Absent只说明当前不存在，不证明前次调用曾成功；上下文取消及未知提交结果必须保留可识别错误。
Source不存在或不是File、generation格式非法属于错误，不等同于Absent。

这两个Store操作必须在生命周期前提落地后才可开放；方法签名本身不提供停读/排空保证。
清理方不持有Processing UnitOfWork，也不修改或伪造checkpoint、coverage和receipt终态。

## 单代际receipt清理

首批候选保守限定整个Sealed代际，不做Open/Draining代际的部分前缀清理。
在同一事务内读取并重核：

- 已知完整coverage、final_eof一致且当前checkpoint不引用该代际。
- 现有恢复边界能证明全部Position已经持久安全越过；不能以跨session Sequence或仅文件时间排序代替。
- 全部receipt均达到committed_at_us加30天，且无入向业务外键引用。
- 前述生命周期前提已经建立，不存在可以在清理后重新进入处理的旧投递。

任一保护条件未满足则整代际保留，不提前删除一部分。检查和DELETE失败均回滚。
不得由清理动作提前seal、缩小final_eof或修改coverage以取得资格。
receipt清理成功不自动把generation退休；随后复用现有退休资格操作，退休时间开始独立计时。
这个范围可能清不掉大部分有业务关联的receipt，不能宣传为完整空间回收。

## Retired generation清理

同事务核对Retired、合法retired_at且已满30天、无receipt/当前checkpoint/其他外键及恢复依赖。
仅删除精确registry row；保留审计与其他Source、代际及业务数据。
删除后新进程必须能从剩余记录恢复；旧文件发现不能把已清理身份重新登记为Open。

## 时间与失败处理

30天按30×24小时、UTC微秒截止比较，等于截止时间可成为候选。
拟由内部受控时钟读取当前时间，不给公共接口增加任意cutoff或保留期覆盖参数；测试使用内部注入。
此方案仍依赖系统墙钟，不能保证墙钟大幅前跳时真实经过30天。
生产自动清理接线前需明确可接受的时钟假设或异常处理；本轮不新增持久时钟、配置或探测服务。
单目标事务传递取消与数据库失败；不做后台重试。提交结果不确定时返回错误，不推断删除成功。

## 建议推进顺序与复杂度

具体接口与行为方案见[File Source运行所有权与重启恢复](source-ownership-recovery-design.md)。
首个可独立实施单元为Agent单实例准入；共享Store关闭所有权与生产Reader恢复另按该方案衔接。

### 生命周期衔接预检（2026-09-05）

本次核对基线为提交ab71d7e，工作区起始干净。以下为设计建议，未授予实施或删除资格。

当前实现依据：

- `internal/processor/source_intake_runtime.go`：finishSourceIntakeRuntime仅在读取与处理无错误、Flush成功时运行维护。
- `internal/processor/coordinator.go`：Process的flight串行范围属于单个Coordinator实例及Delivery ID。
- `internal/store/source_state.go`：RegisterFileGeneration允许登记不存在的generation；恢复查询只返回非Retired行。
- `internal/source/sqlite_state.go`：恢复适配器调用上述查询；此查询不承担文件系统发现策略。

下一最小设计单元为File Source运行所有权与重启发现契约，按以下顺序形成可实施方案：

1. 明确唯一组合所有者：列出同库、同Source的读取者、投递入口和Coordinator；说明第二个运行实例的准入与退出规则。
   同一运行的维护窗口只能排除其拥有的工作；Source session的checkpoint CAS不能替代整个处理生命周期的排他证明。
2. 明确文件发现规则：区分登记代际恢复与新文件发现，规定旧路径再次出现、rename、inode复用及身份不明时的行为。
   随机生成新generation只能避免ID重复，不能单独证明同一旧日志不会被再次处理。
3. 明确删除后的证据来源：逐项说明剩余持久状态或可验证的文件生命周期如何区分旧日志与新日志。
   若某类发现无法区分，保留该类目标并记录限制；不能以数据库查无记录作为新文件证明。
4. 用真实临时文件和SQLite构造受控交错与新进程恢复测试；先证明准入、排空和发现规则，再接两个清理事务。

拟议验收矩阵：

| 场景 | 必须直接验证的结果 |
|---|---|
| 本次停读与维护交错 | 最后一次sink返回、worker排空、Flush、维护严格有序；维护中无本次新处理 |
| 第二个运行实例进入 | 在修改session或处理状态前按明确所有权规则拒绝或等待；退出后可正常接替 |
| 已处理旧文件在重启后仍存在 | 使用剩余权威识别并保持原有处理副作用，不从零重新贡献Window |
| 新文件、rename及inode复用 | 新日志可正常处理；身份不明确时执行已确认策略，不静默当作旧日志跳过 |
| 清理事务提交前后进程退出 | 新进程核对完整剩余行及恢复集合，保留旧投递幂等与新投递处理能力 |

物理清理会改变恢复与处理准入契约；实施请求必须列明具体接口、所有权机制、文件发现语义和受影响调用者。
该请求按项目AGENTS的跨模块契约确认规则取得授权后实施；现有D-015期限和D-016维护授权保持各自范围。
本次文档预检不选择锁实现或新增持久模型，Go/Race/integration为NOT_RUN。

下一最小设计单元先闭合“处理准入—停读排空—重启发现”边界，再冻结上述删除接口。
优先复用现有SourceRuntime/Coordinator所有权；若现有保证不足，单独列出必要的跨模块变更请求。
该变化涉及恢复与处理契约，属于Material变更，不能包装成只增加两条DELETE的Store小修补。
不预设新表、lease、task、reference或tombstone；D-013范围保持。
无法证明安全的目标继续保留；也不新增一个永远拒绝的删除API作为“已实现”。

## 验收条件

- 30天前/等于截止、未来或非法时间；receipt与generation两段独立起算。
- 未完成/未知coverage、当前checkpoint引用、业务引用、混合新旧receipt均保留。
- 清理与迟到投递的受控交错，清理后旧Delivery不重新贡献Window；并验证不误拒绝新Delivery。
- 删除后旧generation登记/发现路径、新generation建立、进程重启恢复。
- 事务中失败/取消、提交不确定、重复调用与其他业务完整行保持。

本轮仅设计，Go/Race/integration NOT_RUN；未修改代码、API、schema、配置或执行删除。
独立只读核对：/root/cleanup_design_reviewer，支持作为Proposed接口及测试矩阵交付；
未授予删除实施或用户验收。文档格式检查通过。
第16条PARTIAL、C1 IN_PROGRESS、G18 FAIL、M0 NO-GO及既有批次待验收状态保持。
