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
可复用这些所有权与排空能力，但当前返回时数据库已关闭，尚无保持Store开放的清理接入点，
也未证明其与下一次File发现/恢复共同满足删除前提；不是从头建设一套排空机制。

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
