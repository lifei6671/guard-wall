# ADR-0011：Reconcile Retry 与结果不确定性边界

## 状态

```text
Decision: Accepted
Validation maturity: Implemented
Date: 2026-09-04
```

## Context

Decision 表示安全意图，不能承载 Firewall 调用的 transient failure、重试次数或
外部调用结果。外部 mutation 可能在调用方超时、连接中断或持久化确认丢失后已经执行，
因此不能把重试实现为对 Decision 的再次写入，也不能把不确定结果当作未执行。

Phase 1 已在 Reconcile Controller 和持久化 retry ledger 中实现三条独立 failure domain、
按 revision/generation fencing 的预算，以及 `Unknown → Probe` 恢复路径。本 ADR 固化该
已实现合同，不改变 public API、SQLite schema 或 retry 次数。

## 决定

1. Retry state 只属于 Reconcile，不属于 Decision。Infrastructure、Policy、Target 分别持久化
   attempt、last error、next attempt 与状态；一个 domain 的失败不得消耗另一个 domain 的预算。
2. Retry key 固定为：
   - Infrastructure：`InfrastructureRevision + RetryEpoch`；
   - Policy：`PolicyRevision + RetryEpoch`；
   - Target：`CanonicalTarget + TargetEnforcementGeneration + RetryEpoch`。
3. 每个 key 最多一次首次 mutation 与五次自动重试，退避固定为
   `1s / 5s / 30s / 5m / 15m`。restart、Backend health flap 与普通 Reconcile 不重置预算；
   只有受影响 revision/generation 前进或管理员显式创建新 RetryEpoch 才从 attempt 0 开始。
4. 外部 mutation 前必须先持久化 `Applying` 与 attempt。crash、timeout 或结果不确定均保留
   已消耗 attempt，并将相应 Observed domain 标记为 `Unknown`。
5. `Unknown` 后的下一次 mutation 前必须取得该 domain 的权威 Probe/Snapshot。Probe 可能确认
   已收敛，也可能提供新 Plan basis；不得盲目重放原 mutation。
6. OwnershipConflict、不支持能力与非法 Plan 为 non-retryable。管理员 Retry 只创建指定 domain 的
   新 RetryEpoch，并与 Critical Audit 在同一 SQLite 事务中提交；不得直接修改 Decision。

## 后果

- Decision 历史保持业务原因与安全意图，运行级失败只影响对应 Reconcile ledger。
- 进程重启与 health recovery 可继续使用同一个 durable budget，不会形成无限 mutation loop。
- Unknown 恢复多一次 Probe，但避免把“可能已执行”的 mutation 当作安全重试。
- 该边界不证明真实 Firewall provider、非 clean topology、OS/power-loss durability 或 Release readiness。

## 验证

- `TestRetryBudgetsAreIsolatedByDomain` 验证 domain budget 隔离；
- `TestUnknownAppliedResultIsConfirmedByProbeWithoutAnotherMutation` 与
  `TestUnknownNotAppliedResultBuildsFreshPlanAfterProbe` 验证 Unknown 后的 Probe-first；
- `TestRetryBudgetIsBoundedAndAuditFailureDoesNotPublishEpoch` 验证预算和管理员 epoch 原子性；
- `TestPersistentControllerDoesNotResetExhaustedBudgetAcrossSQLiteReopen` 验证 SQLite reopen 后预算连续性。

以上为 Controller/SQLite 的实现级验证，不提升 D4、G18 或 M0 Gate 至 `Verified`/`PASS`。

## 回滚

若产品需要调整 retry 次数、退避、domain key 或管理员 retry 的事务语义，必须以新的 ADR
替代本决定，并同步更新 Contract、持久化迁移与对应测试。

## 重新评估条件

- 引入真实 Firewall provider 或支持新的 backend/topology；
- 引入跨进程 health source、可执行进程 restart 或新的 operator retry surface；
- 进入 M7/M10 或准备 Release Evidence。
