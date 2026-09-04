# ADR-0013：SQLite durability 与恢复证据边界

## 状态

```text
Decision: Accepted
Validation maturity: Implemented
Date: 2026-09-04
```

## 背景

SQLite 的连接参数、事务成功与物理介质在 OS crash 或 power loss 后的持久性是不同层次的
事实。把“数据库能够打开”或 WAL checkpoint 混同为 Source checkpoint，都会掩盖 recovery
contract 的真实范围。

现有 Store 使用唯一 opener，为每个 physical connection 设置并 read-back 验证 SQLite
PRAGMA；M0 自动化验证已覆盖 committed/uncommitted 边界的 `SIGKILL → reopen`。受支持
filesystem、fsync/barrier、OS reboot 和 power loss 尚无相应环境证据。

## 决定

1. Phase 1 SQLite 基线固定为：`journal_mode=WAL`、`synchronous=FULL`、
   `foreign_keys=ON`、`busy_timeout=5000`、`wal_autocheckpoint=1000`。Store 只在每个
   connection 的实际 read-back 全部匹配后才可返回 ready。
2. 业务状态、Critical Audit、retry ledger 和需要保持原子性的 Store transition 必须在短 SQLite
   transaction 内提交；transaction 内不得调用 Firewall、SMTP 或其他外部系统。
3. M0 对 process crash / `SIGKILL` 的承诺仅为：已返回成功的事务可以在 reopen 后按覆盖范围读回；
   未提交写入不会被当作已提交，也不会造成重复的持久化副作用。
4. SQLite WAL checkpoint 与 Source checkpoint 是不同概念。WAL checkpoint 不构成
   `SourceDurable` 的条件，也不能替代 receipt/checkpoint 的 transaction/replay 语义。
5. OS crash/reboot、machine power loss、filesystem fsync/barrier、磁盘损坏、控制器虚假 flush
   和未验证网络文件系统的耐久性保持 `NOT VERIFIED`。不得从 PRAGMA 或 process-level
   SIGKILL 测试推导这些结论。

## 后果

- 配置漂移会在 Store 打开阶段失败，而不是在后续业务路径中静默降级。
- 已完成的短事务与外部副作用保持边界清晰；外部系统结果仍由对应 Reconcile/Source contract 处理。
- process-level recovery 可作为 M0 自动化输入，但更高成本的故障域留给 M7/M10 和 Release Evidence。

## 验证

- `TestPragmasOnEveryPhysicalConnection` 验证每个 SQLite connection 的 PRAGMA read-back；
- `TestM0Recovery001SQLiteLinuxSIGKILLReopenDurability` 验证 committed/uncommitted Store
  transaction 的 process-level SIGKILL/reopen；
- `TestM0Recovery003SQLiteProcessingTransactionSIGKILLReplay` 验证 Processing UnitOfWork
  未提交写入回滚与重放后单一 committed result；
- `scripts/test-m0-process-recovery.ps1` 汇总运行 M0-RECOVERY-001 至 004，并保留各用例的
  覆盖范围，而不是把未涉及 domain 伪装为已验证。

上述验证不证明 OS reboot、power loss 或 filesystem barrier/fsync；不提升 B1、D2、D4、
G18 或 M0 Gate 至 `Verified`/`PASS`。

## 回滚

若需要修改 PRAGMA、transaction boundary 或 durability 承诺，必须先更新 Contract、Store opener
和对应 crash/reopen 测试；若扩大到 OS/power-loss 承诺，还必须新增目标环境故障注入 Evidence。

## 重新评估条件

- 更换 SQLite driver、filesystem、存储设备或部署模型；
- 修改 migration/connection opener/transaction ownership；
- 进入 M7/M10 或准备 Release Evidence。
