# Generation 恢复范围与连续完成证明

状态：DONE / Implemented；2026-09-04，承接session/checkpoint第178项。
用户已确认本批验收通过；第178项用户验收状态保持独立。
实现、验证与独立交付审查证据见`artifacts/evidence/M0/worktree/m0-c/source-generation-coverage/README.md`。

## 1. 结论

推荐按generation保存“从已确认起点开始、连续达到SourceDurable的字节前缀”。
重启按该稳定字节水位恢复，不比较不同session的DeliverySequence。
完成证明由可信Reader的范围事实、全Source连续Tracker和Store原子提交共同产生，不由退休调用方传入safe=true。

第一实施范围建议仅支持起点为0、按字节顺序完整读取的File generation原语/fixture。
非零初始起点、原生reader、残缺尾行处理、copytruncate丢失与任务引用仍需各自契约；本方案不为它们猜测策略。

## 2. 实施前基线

- 主契约§6.2的Position是半开字节区间；generation稳定，跨generation共用当前session连续Sequence。
- §8.3要求全Source不越洞；receipt已提交但checkpoint未推进时允许重读，receipt负责业务幂等。
- 第178项已经区分session，Begin保留旧Position，新会话从1开始；新Sealed退休仍保守拒绝。
- `internal/source/checkpoint.go`当前candidate只保留最后一个Position，不能表示一次推进穿过多个generation的完整范围。
  当前pending替换规则适用于单checkpoint，不能直接沿用来替换新增的多代际范围集合。
- `source_file_generations`已有final_eof，但没有持久连续字节前缀。当前单个Source checkpoint不足以恢复其他generation的证明起点。

## 3. 最小持久模型

建议在generation行新增两个可空字段，名称待实施冻结：

| 字段 | 含义 |
|---|---|
| durable_end_offset | 已证明连续完成的半开区间[0,end)；NULL为未知，不等于0 |
| coverage_session_id | 最近一次建立或推进该前缀的session身份；与end同时存在 |

复用既有generation身份、state、final_eof。end单调、非负、SQLite整数范围内；Sealed时end不得超过final_eof。
不增加每条记录表、全局Sequence、safe布尔值或独立证明历史表。
完成事实由“已Sealed且已知end==final_eof”直接推导；coverage_session_id记录来源，不要求它等于重启后的active session。
generation不可变、Sealed不再接受新字节，是这个推导成立的前提。

历史row迁移后两字段均NULL，不根据旧Sequence、observed_size或另一个generation的checkpoint补发证明。
初始未知状态只有在可信读取流程确立从0完整覆盖的责任范围后才能建立end=0；迁移本身没有该权限。

## 4. 三方职责与操作

### Reader：声明负责的范围，不声明处理成功

对于本最小范围，Reader从0建立代际读取责任，持久初始化coverage后才投递。
每条FilePosition必须覆盖实际消耗的连续字节（含分隔符），后继StartOffset等于前一EndOffset。
不能跳过无法处理的字节再推进水位；合法Poison也须有稳定Position和终态receipt。
零长度位置不推进字节前缀，不能用来填补实际字节空洞。

Reader的seal屏障须同时确认最终范围final_eof和“不再为该代际产生记录”。
暂时观察到EOF不自动等于稳定seal；最后不完整记录未被既定framing契约处置前，不得取得完整范围证明。
本切片用明确的完整记录fixture验证；不把fixture结论外推为原生文件尾行策略。

### Tracker：只释放全Source连续完成前缀

只有来自Coordinator的DurableCompletion进入Tracker。seq2先完成、seq1未完成时，不产生任何新的持久coverage。
当seq1补洞，candidate除最后Position外，还必须携带该次全Source连续推进内的每个generation的连续区间。
相邻同代际区间可合并，不得仅以max(end)代替连续性校验。

例如：seq1=gA[0,10)，seq2=gB[0,10)，seq3=gA[10,20)。三条均连续完成后，
candidate应包含gA[0,20)、gB[0,10)，最后checkpoint Position仍为seq3的位置。
任何一个中间generation都不能因“不是最后Position”而丢失覆盖信息。

失败Flush后必须保留所有尚未确认提交的区间；新candidate合并旧pending，不能用较大Sequence覆盖掉其他代际。
候选按generation合并连续范围，避免每条completion长期保留；内存成本与未确认代际数相关。
并发Complete/Flush的清除仅能移除已提交候选部分，不能清掉期间新增的coverage；具体结构与锁范围随API实施评审。

### Store：原子验证与持久证明

绑定第178项session handle，同事务校验active身份、Source checkpoint候选及generation覆盖区间。
候选必须包含已校验连续性的区间。起点大于当前durable_end即为越洞；完全已覆盖区间可幂等。
失败重试合并可能包含已覆盖前缀与新后缀：允许Start<=durable_end<End的连续区间扩展至End，
但不得合并不相邻区间后仅取max(end)。超过seal边界或未知起点拒绝整个事务。
一个候选的Source checkpoint和所有generation水位要么全部提交，要么全部保持，不能只完成其中一个代际。
提交结果未知时以同一读快照核对checkpoint与所有涉及generation；无法确认则停止推进，不能猜测回滚或成功。
跨session历史coverage继续有效，但旧session不能提交新coverage、初始化或seal事实。

不开放单独MarkGenerationComplete操作。退休方法只能读取持久事实，不能绕过Reader/Tracker提供的范围链。
现有sessionless seal/register接口若用于新coverage，需要同步绑定ownership；生产单实例条件依然独立存在。

## 5. 两种完成时序

1. 先seal后完成：final_eof已固定，最后连续完成事务把end推进到final_eof，完成事实随checkpoint一同成立。
2. 先完成后seal：持久end已到实际尾部，之后的seal事务校验当前handle与active session一致及final_eof，二者相等即成立。

seal不要求历史coverage_session_id等于当前session，也不重写该来源身份；重启后既有持久前缀仍有效。

第二种时序不要求制造一条空Delivery或再次推进checkpoint。它使用已经持久的coverage，
不违反“不能凭内存完成状态发证”的原则。空文件的已知end=0与确认final_eof=0也使用同一规则。
两字段未知的空文件不能只因final_eof=0自动完成。

## 6. 重启与恢复范围

- 恢复全部非Retired generation，而不是只加载Source checkpoint指向的一个代际。
- 对coverage已知的代际，从durable_end恢复；Sealed读到final_eof，Open/Draining按其生命周期继续读取。
- 所有恢复投递在新session内统一从1编号。旧session的max_delivery_sequence不参与范围证明。
- receipt已提交但coverage未提交：从旧end重读，Coordinator走receipt幂等路径，再取得DurableCompletion推进。
- 旧代际有洞、新代际有receipt：先保留两者身份与范围，新receipt不使旧洞自动完成。
- 未知历史coverage：保持保守未完成。完整从0重新证明必须先确认旧字节可用、旧record framing稳定以及所需receipt/幂等记录仍保留；不能自动进行全量重放。
- 旧inode丢失、截断或内容范围不可恢复：走DataLossSuspected边界，不能缩小final_eof或跳跃end来取得完成证明。

本方案把“范围恢复”绑定到仍可读取且身份稳定的数据；它不能从SQLite水位重建已经丢失的原始字节。
重启后仍按全Source连续Tracker推进当前checkpoint，generation水位是新增持久恢复依据，不是绕过全Source排序的第二通道。
这是本批获批的恢复契约扩展；主契约同步恢复起点与checkpoint关系，实现证据独立记录。

## 7. 与退休及引用的关系

完成事实仅满足退休的处理完成条件。当前checkpoint、receipt与crash-replay恢复需求仍须检查，物理清理另受retention约束。
本最小包未启用新的退休成功路径。后续范围以D-013为准：Phase 1仅覆盖当前恢复需求，历史reprocess经未来Contract Change Review引入。
Retired不等于删除；本方案不清理receipt、不缩短去重窗口、不修改物理清理策略。

## 8. 最小验收矩阵

| 场景 | 必须证明 |
|---|---|
| 同代际连续记录/Poison | 精确字节前缀推进，不能越过未终态记录 |
| gA/gB交错及乱序完成 | 全Source空洞阻止全部新coverage；补洞后两代际均正确提交 |
| 一代际覆盖校验失败/事务取消 | 所有generation与Source checkpoint完整行保持 |
| Flush失败后再完成其他代际 | 重试不丢旧pending范围；已覆盖前缀加连续后缀可推进，空洞拒绝 |
| 提交成功但确认丢失，合并新完成后重试 | 已提交前缀幂等，新后缀原子推进，无业务重复 |
| 先seal/后seal/空文件 | 三种完成顺序均可判定，无虚构Delivery |
| Begin后旧owner回调 | coverage/初始化/seal写全部拒绝，不只checkpoint拒绝 |
| receipt提交后coverage前SIGKILL | 新进程同ID幂等重放，业务完整行不增量且coverage补齐 |
| coverage事务提交前/后SIGKILL | fresh process观察全有或全无，恢复起点精确 |
| 历史NULL/缺字节/未处理尾部 | 保持未完成，不回填、不伪造完整覆盖 |

本批Docker普通/Race/integration及全仓verify已执行，具体结果见证据入口；fixture证据与原生File/Journald证据分开。
Store全包Race首次60秒总时限耗尽，180秒重跑通过。提交确认测试模拟丢失确认并使用真实SQLite快照，
不宣称注入了driver“已提交却返回错误”。缺字节/原生尾行处置仍属Reader排除边界，并非本批实现或原生证明。

## 9. 实施审批边界

建议下一最小包：两个coverage字段的顺序migration、绑定session的初始化/seal/批量checkpoint范围API、
Tracker多generation候选与失败重试合并、Store原子校验，以及起点0完整记录fixture测试。
Material影响：Source/Store跨模块契约、持久恢复依据和迁移；用户已于2026-09-04明确确认本范围。
非零起点、真实reader/framing、copytruncate诊断、引用任务、retention和退休成功路径全部排除。
在这些依赖未闭合前，第16条维持PARTIAL，C1/G18/M0及前批人工验收状态保持。
