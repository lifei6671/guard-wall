# Source 引用任务所有者预检

2026-09-04；历史预检记录，非当前计划。当前范围由D-013及source-generation-retirement-scope.md确定。
下文保留预检过程；显式任务owner设计建议已被D-013替代，不构成Phase 1退休前置条件。

## 已有能力与缺口

session写入隔离及逐代际连续coverage已实现；新退休请求继续保守拒绝。
普通进程重启按持久范围和原DeliveryID重放，receipt保证幂等，不需要给每条记录另造任务引用。
显式运维任务如果需要跨越正常Source推进仍保护某代际，才需要持久任务引用。

主契约§6.2/§8.4/§17.1第16条规定引用阻止退休或清理，但未给出显式任务的接受、完成、放弃和恢复所有者。
Parser/Rule snapshot契约另要求历史重处理使用新的reprocess identity；该身份如何传播到业务幂等键和副作用尚待定义。
本轮结构检索未定位对应owner；internal/migrations/schema中精确文本检索未发现reprocess、replay_task、
replay_job、reference_kind、reference_id定义。不能把不存在的任务表当作已证明无任务引用。

## 推荐的下一范围

先确定普通显式重放任务的所有者契约：复用原DeliveryID与receipt幂等，不触发已有记录的再次业务处理。
设计须明确任务目的与固定读取范围、持久任务ID、何时接受、何种持久结果代表完成、谁可明确放弃、
崩溃恢复沿用哪个身份，以及任务终态与引用释放如何原子关联。
完成、失败、取消与放弃不能混为同一终态；解除保护前须明确如何确认在途处理已停止。
这一步不等于创建可执行重放服务；具体API/Schema在这些语义确认后才能冻结。

引用模型必须保证：接受任务前持久保护；任务与引用同库同事务；恢复不按PID/TTL自动释放；
终态不能重新Acquire；Acquire与Retire事务串行化；缺失owner保持阻塞。
单独提供Release(reference_id)而没有任务终态依据，不能闭合安全边界。

历史重处理、新业务幂等身份、调度器、UI/CLI、真实Reader、retention、物理删除及退休成功路径另批。
不先创建通用任务表、空owner或自动清理框架。

## 待确认

下一设计是否只覆盖普通显式重放任务的owner与引用保护，将历史重处理保留为独立任务？
该选择不授予实现、公共API或migration权限；不修改当前SourceDurable/receipt契约。

本轮仅验收同步与预检。Go/Race/integration为NOT_RUN；实施交付审查不适用（Review not required）。
第16条PARTIAL、C1 IN_PROGRESS / Implemented、G18 FAIL、M0 NO-GO保持。

独立代理/root/coverage_delivery_reviewer只读核对主契约§6.2/§6.4/§7.4/§7.5/§8.4/§17.1：
普通恢复无需逐条任务；显式任务owner、释放条件和reprocess身份传播尚未冻结。核对未授予实现权限。
