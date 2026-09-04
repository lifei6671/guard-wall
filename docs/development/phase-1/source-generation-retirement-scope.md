# Phase 1 Generation 退休范围

状态：实施中，2026-09-04，依据用户明确指令及D-013；交付验证与用户验收分别记录。

## 当前范围

Generation Retired只依据当前已实现的checkpoint、receipt和crash-replay恢复需求判断。
必须具备Sealed及持久连续范围到达final_eof的完成事实，并确认当前恢复路径不再需要该代际。
历史未知coverage保持未知，不能用跨session序号比较或receipt数量代替连续完成证明。

Phase 1不支持显式历史日志reprocess，不为未来reprocess建设task/lease/reference系统。
未来新增历史reprocess时，通过Contract Change Review引入generation pin/reference语义。
普通crash-replay继续使用稳定DeliveryID和receipt幂等，不改变已提交业务结果。

## 下一实施单元

在现有Store事务及恢复路径中核对退休资格，覆盖未完成、未知coverage、receipt仍引用、
checkpoint仍需该代际、崩溃重开及符合条件的成功退休。
当前checkpoint即使恰好位于sealed EOF，也阻止该代际退休；BeginSourceSession保留其Position，
LoadSourceCoverageState与RecoverFileGenerations恢复全部非Retired代际。待checkpoint移走后再检查退休资格。
已知空代际不要求制造checkpoint；完整coverage本身为其提供零字节完成证明。

Retired是状态转换，不是物理删除。保留现有外键、receipt保留期及retention约束；
本范围不授权新表、公共API扩展、生产reader、copytruncate或自动清理。

## 当前证据

第179项coverage已验收；本次退休实现与验证证据见artifacts/evidence/M0/worktree/m0-c/source-generation-retirement/。
现有API内以同一事务读取资格并CAS写入state/retired_at；并发写导致快照升级失败时传播数据库错误，保留原状态。
第16条PARTIAL、C1 IN_PROGRESS / Implemented、G18 FAIL、M0 NO-GO保持。
