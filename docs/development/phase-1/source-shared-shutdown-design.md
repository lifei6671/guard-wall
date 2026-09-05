# Source与Reconcile共享关闭预算

状态：Proposed，2026-09-05。承接STATUS第187项已验收的Source借用Store接口。
本页确定下一个最小接口实施包；生产Reader接线仍按独立范围推进。

## 当前接入缺口

Source在Reader返回、worker失败或运行context取消后，内部创建shutdownTimeout预算。
该context在RunSourceIntakeRuntime返回时取消；外层所有者无法继续使用其剩余预算。
成功路径能通过maintenance参数访问deadline，但Reader/worker/Flush错误会跳过回调。
因此，仅在maintenance里停止Reconcile会遗漏错误路径；返回后另起完整超时会延长总关闭时间。

当前guard-agent只运行Reconcile，Store.Close与目录锁Close均为无条件defer。
Source尚未接入该链路，因此本轮没有已发生的提前关闭问题；生产组合时须先完成预算交接。
实现依据：internal/processor/source_intake_runtime.go与cmd/guard-agent/main_linux.go；
Reconcile.Run等待全部内部组件返回，见internal/reconcile/runtime.go的runComponents。

## 最小接口实施包

将Source入口的shutdownTimeout参数替换为由调用者提供的开始关闭函数：

```go
func RunSourceIntakeRuntime(
    ctx context.Context,
    beginShutdown func() context.Context,
    reader source.Reader,
    queue *source.DeliveryQueue,
    coordinator *Coordinator,
    checkpoints *source.CheckpointManager,
    maintenance func(context.Context) error,
) error
```

beginShutdown由运行所有者构造，在首次关闭触发时创建带deadline的context；
同一组合中的多次或并发调用返回同一个context，起始时间与deadline固定。
调用者用局部sync.Once闭包实现，超时时长沿用当前调用方选择，不新增配置、导出类型或调度包。
context脱离正常运行context的取消，最终cancel由所有者执行，Source不得提前cancel共享预算。
函数须及时返回，返回非nil且具有deadline的context；这是内部组合契约，不增加重试或猜测修复。
Source启动前校验beginShutdown非nil，其他已有输入校验保持。

Source在当前三个停止分支取得此context，Reader停止、Seal、worker等待、Flush、维护均继续使用它。
专用ErrSourceIntakeShutdownTimeout及普通组件Deadline的区分保持。
参数校验失败不调用beginShutdown；尚未启动的资源仍由调用者清理。

本包只修改Source入口与两个直接调用者测试文件，并同步当前接口文档。
测试用局部闭包持有共享预算，验证runtime返回后context仍归调用者所有。
RunSourceRuntime保持现有接口。生产Agent组合、Reconcile内部故障通知与Reader配置不在本接口包实施。

## 生产组合时必须落实的使用顺序

正常进程停止时，所有者先开始共享关闭预算并停止Source读取；Reconcile的运行context不直接跟随进程取消。
Source排空后，在维护回调前置阶段停止并等待Reconcile，成功后才执行维护。
Source返回错误导致回调跳过时，所有者同样停止并等待Reconcile，使用已开始的同一关闭context。
Reconcile先返回时，所有者开始预算并停止Source；已取得的Reconcile错误与Source错误合并。
Reconcile结果由组合层唯一接收并保存；维护前置等待和错误收尾复用该结果，不能竞争消费结果通道。
单Source正常结束也会开始组合退出；未来多Source的存活策略须随真实Reader接线确认。

共享预算覆盖所有者或Source首次可观测到关闭事件之后的收尾；
Reconcile内部一个组件失败而Run仍等待另一组件时，外层尚未收到返回，不宣称本接口能检测该内部事件。
如果未来要求从该内部故障时刻计时，需要独立确认其通知契约。

仅收到Source与Reconcile均已结束的结果，且没有预算耗尽标识时，才允许Store.Close后释放目录锁。
等待任一组件耗尽预算时，所有者取消其运行context，跳过维护/Close/解锁并进入进程退出路径。
后续Agent接线必须同时调整这两个defer，不能只将Source错误返回给当前runGuardAgent。
共享context.CancelFunc只负责结束预算生命周期，不代替等待组件结束。
维护仍同步执行并要求协作取消；生产owner须同时观察共享deadline，不能只阻塞等待Source返回。
预算到期不会强制终止不返回的回调；即时退出保证须由实际进程所有者落实。

## 验收条件

1. 正常结束、主动停止、worker失败均通过beginShutdown取得唯一预算；首次触发前不开始倒计时。
2. owner先开始预算与Source先开始预算两种顺序，维护观察到相同deadline；重复调用不重置预算。
3. Source返回普通错误并跳过维护后，调用者仍可取得有效的剩余预算，未被Source的defer取消。
4. 外部运行context取消不立即取消关闭预算；预算原有deadline仍约束Reader/worker/Flush/维护。
5. 已过期的共享预算保持专用超时身份；普通组件Deadline保持普通错误。
6. Reader排空、维护至多一次与前置失败跳过、真实SQLite借用和owner关闭重开断言保持。
7. Processor定向回归、integration Race与仓库verify通过，完成独立交付审查。

## 本轮结果与下一入口

本轮只核对现有实现、同步第187项用户验收并形成此接口提案；Go验证NOT_RUN，沿用第187项历史证据范围。
第185项DONE，第186项REVIEW，第187项DONE；C1 IN_PROGRESS、G18 FAIL、M0 NO-GO保持。
下一实施入口为本页“最小接口实施包”。导出内部函数参数和跨模块关闭契约需确认后实施。
