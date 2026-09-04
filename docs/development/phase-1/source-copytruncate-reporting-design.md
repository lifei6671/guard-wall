# Source 截断观测与报告

状态：Accepted，2026-09-04（D-014）。以下接口与报告语义已获用户确认；实施交付与验收见STATUS第183项。

## 现有接口约束

Store.UnitOfWork.AppendCriticalAudit固定写critical=1，且BeginProcessing的提交归Processing Coordinator。
audit_logs支持critical=0；Source报告通过专用Operational Audit入口写入。
reconcile的BackendHealthSource/BackendHealthStatus描述防火墙后端健康，不承担Source状态。
依据：主契约§8.6区分Critical Audit与Operational Audit；§6.2/§9.2要求DataLossSuspected报告。

## 归属与接口

Source观测组件判断截断并维护本次观测的Health结果，Store负责专用审计事务。
Store.RecordSourceDataLoss(ctx context.Context, event SourceDataLossAudit) error负责专用写入。
SourceDataLossAudit仅包含NodeID、SourceID、Generation、DeviceID、Inode、PreviousSize、ReadOffset、ObservedSize、ObservedAt。
Source侧增加有限FileTruncationObserver与Observe/Health读取边界；由单个读取所有者串行调用，
通过窄reporter调用上述Store方法，不创建Processing UnitOfWork，不分配DeliverySequence。

本次事件定义为Operational Audit：category=source、action=data_loss_suspected、result=failure、
severity=warning、actor_type=source、critical=0、error_code=DataLossSuspected。同步落库只是本原语选择，不改变Critical Audit契约。
审计不携带日志内容或路径，不伪造Delivery/receipt，不修改checkpoint、coverage或generation状态。
Store从已注册Source/generation核对节点和文件身份；按Source+generation记录首次疑似事件，
重试保留首次证据，不覆盖后续观测。重复键不能吞掉不相符的Source/generation身份。

## 观测与失败语义

同一文件身份下，size较既知基线下降或小于已知读取offset，产生疑似事件。
不同身份返回独立的身份变化结果，交还调用方处理rename/create，不伪装为copytruncate。
没有可见证据只表示未检测到；两次观测之间截断再快速增长仍为known limitation。

一旦检测到截断，观测组件保持该代际Degraded/停止读取标记；报告成功也不自动解除。
报告失败或取消原样传播可识别错误，同时保留疑似事件与停止标记，不报告审计成功。
同一组件可显式重试尚未成功的报告；无后台重试、TTL或自动恢复。已成功报告不重复写。
Health是当前组件状态，非持久Health服务；本批不声称跨重启自动恢复Health或恢复File reader。
新generation的建立、旧代际seal及读取恢复由独立后续范围决定；本批不缩小旧范围以通过退休。

## 复杂度与验证

新增Source观测及Store专用writer/数据类型；复用现有audit_logs，不新增表、migration、依赖或配置。
主要风险为报告失败被误当成功、身份误判与重复审计；需验证真实临时文件truncate/append/rename/fast-regrow，
数据库拒绝/取消与重试、首次证据保留、Health保持及原checkpoint/receipt/coverage不变。
本批公开符号和跨模块报告契约已获确认；完整File reader、生产接线和物理清理保持独立范围。

验证与独立审查见artifacts/evidence/M0/worktree/m0-c/source-copytruncate/；实现只提供有限观测原语，各Gate保持。
