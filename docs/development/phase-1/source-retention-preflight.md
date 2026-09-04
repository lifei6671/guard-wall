# Source 恢复记录保留与清理预检

日期：2026-09-04。状态：只读发现完成，保留策略待确认。
承接STATUS第181项退休原语、第183项截断观测；两项用户验收独立保留。

## 已确定的边界

- 主契约§6.2：Sealed完整coverage、无receipt及当前checkpoint引用、无crash-replay恢复需求，才可退休。
- Retired只改变状态与退休时间并保留registry row；物理删除还必须满足retention。
- 主契约§8.4及source-delivery.md §7：receipt只有在持久checkpoint安全越过、retention满足且恢复不再需要时才可删除。
- 主契约§17.1第16条还要求满足全部清理条件后，重启不再依赖被清理row；当前仍PARTIAL。
- D-013定义Phase 1恢复范围：checkpoint、receipt、crash-replay；未来历史reprocess经独立Contract Change Review处理。

## 当前实现与关系

`internal/store/source_state.go:486`的RetireFileGeneration同事务验证资格，只写state/retired_at。
`internal/store/source_state.go:330`的恢复查询排除Retired，其余代际仍返回。
`internal/store/uow.go:707`的PutReceipt负责处理终态写入；清理不能使旧投递重新被当作首次处理。

`migrations/0001_m0.sql`已建立RESTRICT外键：

- parser_terminal_outcomes、detection_contributions引用processing_receipts。
- alerts引用detection_contributions；decisions引用alerts；audit_logs引用alerts/decisions。
- receipt与当前Source checkpoint引用generation。

因此，删除receipt前需要处理实际引用关系。不能因receipt到期而提前删除仍需保留的业务或审计记录。
可以保守保留仍有引用的候选，不应以关闭外键或级联删除替代生命周期契约。

## 待确认的保留策略

V0.4 §93规定Decision History 90d、Alerts 30d、Audit 90d、Notification History与Failed Notifications 30d；
这些默认建议不包含receipt或generation。当前config schema也没有这两类保留期定义。

实施前需要明确：

1. receipt最短保留时长与起算事件；提交时间和checkpoint安全越过时间含义不同，不能自动等同。
2. Retired generation最短保留时长及是否从retired_at起算。
3. 保留期配置权威，以及业务引用未释放时的延后清理规则。
4. 清理与迟到处理的并发边界、同一事务内重核资格、崩溃后幂等重试及恢复证明。

这些是持久生命周期决策，尚不授权新增API、配置、migration或删除行为。

## 建议的下一实施边界

先形成File Source恢复记录保留契约，明确上述策略与小批量清理的事务边界；
保守跳过仍有引用或恢复依据不完整的候选。仅处理数据库恢复记录，原始日志文件、
全业务历史清理、调度器、VACUUM、生产reader及Journald cursor比较保持独立范围。

最小验证应覆盖：时间未到、checkpoint仍引用、coverage未知/未完成、业务外键仍引用、
失败回滚、并发迟到投递以及合格清理后新进程恢复。具体实现需在契约确认后进行。

## 本轮结果

仅读取契约、schema和现有代码，未执行删除或改变运行配置；Go/Race/integration NOT_RUN。
本文件是预检，不是实现验收，不提升C1/G18/M0；既有交付证据保持原快照。
