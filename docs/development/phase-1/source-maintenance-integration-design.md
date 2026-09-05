# Source 排空后的维护接入点

状态：D-016维护接入点已实施；2026-09-05已实施并验收Source借用Store迁移，DONE / Implemented。保留策略D-015保持。

## 接入位置

RunSourceIntakeRuntime负责本次Source排空，调用者持有Store最终关闭权：

Reader结束全部sink调用 → Seal队列 → worker正常退出 → Flush成功 → 同步维护 → runtime返回 → 调用者等待其他共享组件结束并Close数据库。

当前依据：internal/processor/source_intake_runtime.go及source/reader.go的返回契约。
worker退出可能因为处理失败，不等于队列处理完成；只有汇总错误为空时才允许维护。
Reader受停止请求产生的预期取消沿用现有归一化，其他Reader错误均阻止维护。
归一化仅发生在现有主动停止Reader路径；取消混合真实错误仍阻止维护，不扩大吞错范围。

## 接口变更

RunSourceIntakeRuntime末尾接收维护回调，Store通过既有处理依赖借用，接口不接收database io.Closer：

```go
maintenance func(context.Context) error
```

nil保持现有行为；现有调用者显式传nil。不开新配置、选项结构、插件接口或后台调度器。
维护函数由拥有本次运行生命周期的组合层提供，不由外部请求任意提交。
当前接口为带Reader所有权的RunSourceIntakeRuntime，RunSourceRuntime保持原接口。

## 执行与失败语义

- Reader已返回，队列已Seal且worker正常完成，初始错误为空。
- checkpoint Flush成功，且现有shutdown context仍有效。
- 满足以上条件后同步调用maintenance至多一次；它返回后才允许执行Close。
- 任一前置错误、超时或Flush失败都跳过维护，由runtime传播错误，调用者依据关闭结果处理Close。
- 维护返回错误且deadline未过期时，由调用者Close并合并维护错误与Close错误。
- 关闭预算耗尽时，返回错误同时匹配ErrSourceIntakeShutdownTimeout和context.DeadlineExceeded；调用者跳过Close并保持进程锁至退出，不复用Store。
- 组件自身返回DeadlineExceeded但关闭预算仍有效时，保留普通错误，不产生专用超时标识。
- 维护使用同一剩余shutdown deadline，不延长预算、不重试、不另起goroutine。
- 维护实现必须配合context取消并在返回前结束自身数据库使用。回调阻塞且不配合取消时，
  同步调用无法保证按deadline返回；不得用后台强行Close绕过这一限制。

维护是可选操作，不运行或失败均不授予删除成功。checkpoint失败后不得通过维护补造完成证明。

## 本接入点能证明什么

它证明本次Reader与worker按上述顺序结束、checkpoint已保存且Store尚未关闭。
它不证明同库其他Source、另一个Coordinator或其他进程已停止；这需要实际组合层的所有权依据。
它也不证明File重启发现不会再次投递已清理日志，或旧generation身份不会重新登记。
因此，本接入点本身不是删除授权，也不能代替source-cleanup-interface-design.md中的安全资格。

## 已授权实施包

维护回调沿用上述内部传递与调用顺序；直接调用者承担Close，测试区分组件错误与关闭预算耗尽。
测试中maintenance仅为记录顺序、等待取消或返回错误的桩；不接Store删除方法、不新增SQL。
完成后可准确交付“维护接入点已实现”，不能把第16条物理清理标成完成。

必要测试：

1. 正常Reader结束和受控停止：全部sink结束、worker排空、Flush、maintenance、Close的严格顺序。
2. Reader失败、worker失败且队列仍有待处理记录、Flush失败：maintenance零次。
3. Reader/worker等待超时、Flush期间超时：maintenance零次，保持现有Close边界。
4. maintenance成功/失败、Close失败及错误合并，maintenance至多一次。
5. maintenance观察deadline并返回取消：Close零次，原deadline未被重置。
   超期时同时保留maintenance错误与DeadlineExceeded；维护未返回期间不调用Close。
6. nil回归与现有shutdown/crash-replay测试保持原行为。

## 后续真正删除的门槛

先在真实组合层证明数据库所有者排他及File恢复/发现行为，再接入有限删除。
复用已有机制优先；尚不能证明的目标继续保留，不预建task、lease、reference或tombstone。
原始日志文件、业务历史清理、生产调度及新的时间基础设施保持独立范围。

设计只读核对：/root/maintenance_hook_reviewer；实现验证与独立交付审查另见
artifacts/evidence/M0/worktree/m0-c/source-maintenance/及STATUS第184项。
用户批准接口、nil适配及测试，不授权删除；既有用户验收及C1/G18/M0状态保持。

当前关闭所有权契约见[source-ownership-recovery-design.md](source-ownership-recovery-design.md)的
“Source借用Store与关闭超时交接”。该接口迁移已实施，证据见artifacts/evidence/M0/worktree/m0-c/source-store-ownership/与STATUS第187项；用户验收已通过，见该证据目录acceptance.md。
