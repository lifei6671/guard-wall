# Source generation 引用生命周期与退休资格

状态：历史设计草案；日期：2026-09-04。当前Phase 1退休范围以D-013及主契约§6.2为准。
下文方案与实现快照仅作历史记录，不作为当前实施入口；当前入口见source-generation-retirement-scope.md。
用户已授权本轮设计；API、migration、配置及生产接线须在确认具体方案后另行实施。

## 1. 目标与现状

让generation只有在连续处理已持久完成、没有恢复或重处理引用时退休，并在进程重启后保持这些约束。
本批不实现File reader、copytruncate、运维任务调度或物理清理。

依据：

- 主契约 `docs/contracts/guard-phase-1-m0-contract-freeze-v0.3.md` §6.1：DeliverySequence为session-local，重启后从1开始；稳定Position/DeliveryID承担恢复身份。
- §6.2/§8.3/§17.1第16条：连续checkpoint安全越过、无receipt/replay/reprocess引用且无待重放记录，才允许Retired；删除另受retention约束。
- `internal/store/source_state.go`：当前checkpoint CAS比较持久delivery_sequence；退休检查Sealed、receipt数和checkpoint序号，不含session身份或显式任务引用。
- `migrations/0001_m0.sql`：checkpoint/receipt通过外键限制generation删除；没有replay/reprocess引用表，Retired写入触发器不等于退休资格检查。

因此，单独增加引用表不能闭合第16条。旧session的generation最大序号与新session的checkpoint序号不可直接比较。
当前测试通过只证明既有fixture范围；本设计未运行新的跨session回归，也不据此改写历史结果。

## 2. 推荐分层

```text
同session连续完成 → 持久安全越过证据
                             ↓
Sealed + 安全越过 + 无receipt/任务引用 → Retired
                                             ↓
                     retention满足 + 无任何依赖 → 删除
```

Retired表示恢复列表不再需要该generation，不等于row已删除。
当前checkpoint若仍通过外键指向该row，可以保留Retired row；物理清理必须等待该引用迁移或解除。
任何新replay/reprocess不得复活Retired generation。

## 3. 安全越过证据：先决设计

推荐保留session-local编号，不把Sequence改为跨进程永久递增。
由Source连续checkpoint权威持久写入generation级“安全越过”证据，退休调用方不能通过传入bool或任意时间戳自行授予资格。

证据必须同时绑定Source、generation以及产生证明的processing session；其含义为：该generation已Sealed，
读入与seal屏障完整，属于该generation的全部待完成记录已在同一session的连续完成水位内，且不存在未消费尾部。
它与相应checkpoint更新同事务提交；失败两者均不生效。证据授予后保留，重启不拿新session数字重新比较旧水位。
单个新generation的较大offset、较大序号或时间较晚都不能代替该证明。

重启后：

- 有已提交证据的Sealed generation继续保留资格，但仍受引用围栏限制。
- 没有证据的generation保持可恢复；必须恢复并完成其剩余范围后重新取得证明，不能把“未加载到任务”当作“无剩余记录”。
- 空generation同样需要明确的seal/空范围证明；不能仅用max_sequence=0推定安全。
- 旧数据库缺证据时保守保留；不得通过migration对已有Sealed row批量回填已完成。

实现前必须先冻结：session身份的创建/重启交接、旧owner写入拒绝、checkpoint跨session CAS、
恢复范围与新session序号映射，以及“连续水位覆盖已seal generation”的权威输入。
当前接口不能直接满足这些条件。第一个实施单元应是该session/checkpoint契约及确定性回归；具体字段和API须随其冻结。
旧owner围栏先核对现有单实例与生命周期保证；只有仍存在实际并发写入路径时才增加机制，不默认新增锁协议。
本草案不把外部调用方声明的“已无待重放记录”视作持久事实。

## 4. replay/reprocess引用

推荐持久显式引用，不采用内存计数、PID存活判断、心跳租约或自动TTL释放。
实际风险是：已接受的恢复/重处理工作在崩溃后仍需重建Delivery身份，而退休先发生会使该工作失去恢复依据。

候选持久模型为一张generation引用表，最少包含：

| 字段 | 建议含义 |
|---|---|
| source_id / generation | 被保护代际，外键限制删除 |
| reference_kind | 闭集replay、reprocess |
| reference_id | owner持久任务身份；相同逻辑任务恢复沿用，不能按每次重试重建 |

组合身份唯一；具体ID格式与资源上限在API冻结时定义，不增加可配置项。
引用表只保存活跃引用，不在此复制任务状态机。需要历史追溯时使用任务权威/既有Audit，而不是第二套任务历史。

候选操作（语义名称，不是已批准的Go签名）：

1. Acquire：在启动工作、读源数据或发布“任务已接受”之前持久取得引用；验证generation存在且非Retired。
2. Recover/List：新进程先恢复持久引用，与owner的持久任务状态对账，再继续工作。
3. Release：只在任务已持久完成或明确持久放弃后解除；同身份重复释放可幂等，不能批量释放其他owner引用。

同身份Acquire重试幂等；Release后的同逻辑任务不得重新Acquire。
这个终态约束由持久任务owner负责；在owner终态模型尚未定义时，不提供面向外部的可启动任务API。
没有任务owner记录的引用视为待核对阻塞，不能自动删除；完成已持久化但释放失败时可在对账后重试释放。
引用获取与任务接受需同一事务，或采用“先引用、后接受”的可恢复协议；后者可能留下保守阻塞引用，须有明确的人工放弃路径。
本阶段只推荐同库同事务方案；跨库任务服务不在范围内。

自动重启恢复不需要为每条receipt增加引用：没有安全越过证据的generation已被资格围栏保护。
需要跨越正常推进仍保留某generation的显式replay/reprocess工作，才持有上述任务引用。
reprocess identity不是普通receipt重放身份；本批不设计其下游重复执行与副作用语义。

## 5. 事务与竞态

Acquire与Retire必须在同一SQLite权威下进行状态检查和写入，不能在事务外先查后写。
必须由数据库事务/条件写保证互斥结果，不仅依赖单进程mutex：

- Acquire先提交：Retire看到引用并拒绝。
- Retire先提交：Acquire看到Retired并拒绝。
- 锁竞争或提交失败：返回明确错误，不转换为成功，不新增自动重试策略。

Retire在一个事务中复核Sealed、持久安全越过证据、receipt为零、任务引用为零，再条件更新状态。
取消、读写或提交失败不能留下部分资格/状态变化。现有late receipt/checkpoint拒绝规则继续保留。
具体事务模式须结合Store现有事务实现验证，不在草案中预设新的连接配置。

## 6. Retention与物理删除

退休资格与删除资格分别验证。现阶段不新增retention时长、默认值或清理任务，不手工删row来代替产品清理。
receipt仍存在时退休被阻止；receipt删除还受§8.4的checkpoint、retention和explicit reprocess约束。
后续物理清理设计必须明确保留期权威/计时起点、receipt及关联幂等记录的清理顺序、当前checkpoint外键与任务引用处理。
这些规则未冻结前，允许保留Retired row，不承诺自动回收空间。

## 7. 最小实施顺序与验收

1. session/checkpoint身份与跨重启资格证明：先形成可执行契约，验证旧session高序号不能授予新session资格，
   新session从1开始仍能合法推进，旧owner不能覆盖新checkpoint，事务失败不留下资格，跨generation空洞不被跳过。
2. 持久引用与退休围栏：待第一步及任务owner契约明确后实施引用登记与事务竞争回归；不接入生产reader或调度器。
3. fresh-process恢复：取得引用后crash，重启仍阻止退休；完成/放弃后释放，合格generation退休；
   独立进程恢复列表排除该代际，当前代际/checkpoint与其他业务全行保持。该验证不等于物理删除验收。
4. 物理清理作为独立任务，需另行批准retention和清理契约。

必要负向用例：未seal、未越过、receipt存在、任一任务引用存在、未知/Retired代际Acquire、重复调用、
Acquire/Retire并发、取消/提交失败、旧数据库无资格证据、空generation与乱序恢复。
使用仓库Docker脚本及既有integration环境；本轮为设计，测试NOT_RUN。

## 8. 复杂度与审批范围

这是Material变更：跨Source/Store会话契约、持久checkpoint、至少一张引用表及资格证据migration、
owner恢复协议和并发验证。它不能包装为单个退休函数的小修补。
不引入租约服务、定时扫描框架、第三方依赖或后台自动清理。

建议下一次批准仅覆盖第7节第1项的详细API/Schema设计与确定性回归方案；具体生产实现另行确认。
保留主契约session-local语义；引用owner与retention未冻结部分继续作为显式依赖。
本轮不提升第16条PARTIAL、C1 IN_PROGRESS / Implemented、G18 FAIL或M0 NO-GO；此前测试批次人工验收状态不变。

## 9. 设计核对

独立代理 `/root/multiparser_reviewer` 只读核对草案与契约，未发现阻塞矛盾；结论仅为可作为Proposed设计交付。
实现交付审查不适用（Review not required）：本轮仅说明性文档；测试NOT_RUN，`git diff --check`通过。
