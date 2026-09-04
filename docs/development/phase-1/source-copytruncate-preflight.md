# Source copytruncate 观测范围

2026-09-04；第15条实施前预检，当前状态：GAP。本文不改变产品契约或授予API/schema变更权限。

## 现有依据

主契约§6.2、§9.2及§17.1第15条要求：检测到copytruncate时报告DataLossSuspected；
同inode也必须为offset 0的新数据分配新generation。两次观测之间截断再增长的盲区保留known limitation。
internal/source/reader.go目前定义Reader/DeliverySink边界；Source目录没有生产File reader。
本次CodeGraph及Source/Core/Processor/Store文字检索未定位copytruncate或DataLossSuspected实现。
第179项coverage与第181项退休提供持久状态原语，不提供文件截断探测。

## 下一最小单元

先明确观测与失败语义，再实现一个有限的M0观测原语和真实临时文件fixture：

- 同一已打开文件的身份、读取offset与前后size观测如何输入；不能把路径指向另一inode当作copytruncate。
- 观测到同inode的size下降时产生截断疑似事件；size未下降只表示未发现证据，不表示未发生截断。
- 同一文件当前size小于已知读取offset也属于可见截断证据；初始化与重启时的观测基线须明确，不能只比较相邻stat。
- DataLossSuspected的Audit/Health报告所有者、最小字段、持久化失败后的停止/重试边界。
- 检测后停止旧代际继续读新字节；旧未完成范围仍保留，不能靠缩小final_eof或coverage伪造完成。

该单元不包含完整File reader循环、生产接线、文件指纹扫描、任务调度、清理或历史reprocess。
纯size比较测试不能单独算第15条COVERED：必须验证报告路径及失败传播。
若接口或跨模块事件需要新增/改变，给出具体签名与错误语义后依项目规则确认。

## 验证目标

1. 同inode真实truncate可观测时，产生准确的疑似丢失报告，已有checkpoint/receipt/coverage不变。
2. 普通append与rename/create身份变化不被误判为同inode截断。
3. 截断后在下一次观测前恢复到原size以上，保留不可检测边界，不伪造检测成功。
4. Audit/Health失败能向调用方传播，不能在未定义恢复边界时继续接收新代际记录。

本轮仅预检及覆盖映射同步，Go/Race/integration：NOT_RUN；代码交付审查不适用。
第181项保持REVIEW / Implemented待用户验收；第16条PARTIAL、C1 IN_PROGRESS、G18 FAIL、M0 NO-GO保持。
