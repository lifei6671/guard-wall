# ADR-0012：固定 nftables 所有权与 clean-target 验证边界

## 状态

```text
Decision: Accepted
Validation maturity: Implemented
Date: 2026-09-04
```

## 背景

Phase 1 的特权 Enforcer 不能把 table、chain、set、二进制路径或任意 Firewall 对象名暴露为
可配置输入。若 Guard 无法证明一个同名对象属于自身，或者外部 Firewall manager 已控制
packet path，再对该对象写入会破坏最小所有权边界。

当前 native backend 已实现固定的 `inet guard` 布局、`guard/v1` owner/version 标记、单次
nft batch 写入与写后 Snapshot。隔离 Docker 环境已验证 Guard 自身表的 INPUT/FORWARD packet
path、foreign table preservation 与 cleanup；它不代表带 UFW、Docker 或生产主机的兼容性。

## 决定

1. native nftables backend 只调用固定的 `nft` 二进制，只管理 `inet guard` 私有 table 及其
   固定 infrastructure/policy/target objects。调用方不能选择 binary、table、chain 或 set。
2. 每次 Probe/Snapshot 都读取完整 ruleset。Guard table 必须同时满足固定 backend、owner version、
   schema version 与 layout digest；同名但不匹配的对象是 ownership conflict，拒绝 mutation。
3. Apply 与 Remove 都在同一 attempt 内重新 Probe/Snapshot，并要求 capabilities、ownership 与
   snapshot basis 精确匹配。只发送一个固定 batch；dispatch 后无法证明 post-state 时返回 `Unknown`，
   由 Reconcile 的 Probe-first 恢复，而不是重放写入。
4. Guard 只写自身 table。foreign objects 只作为 digest 化的 ForeignContext 参与 basis 检查；
   任意 Guard 写入后 foreign digest 改变或 postcondition 不成立，都不得返回 confirmed。
5. 已识别的 UFW、Docker 或未知 packet path 使 mutation readiness 为 false。当前支持的验证
   baseline 是无 UFW、无 Docker、`--network none` 的 disposable clean target；它不声明非 clean
   topology 可安全写入。

## 后果

- 同名或漂移的 Guard table 会停在 ownership conflict，而不会被“修复”或覆盖。
- 执行路径不接受通用命令和物理对象输入，减少特权侧 mutation surface。
- 写后确认多一次 ruleset 读取；外部命令失败或读回不确定会进入 Unknown/Probe-first，而不是伪造成功。
- clean-target Docker 证据可以证明固定布局、foreign preservation 和 packet path，但不能作为
  production Firewall、UFW/Docker coexistence、OS reboot 或 Release evidence。

## 验证

- `TestNftablesBackendIntegration` 覆盖固定布局、ownership conflict、已知 manager 的 fail-closed 与
  Guard table cleanup；
- `TestNftablesBackendIntegrationRejectsSameNameForeignTable`、
  `TestNftablesBackendIntegrationRejectsOwnerVersionMismatch` 验证同名/owner 漂移拒绝；
- `TestNftablesBackendIntegrationRejectsForeignContextAppearingAfterAuthorization` 与
  `TestApplyForeignDriftAfterDispatchIsUnknown` 验证 ForeignContext 与写后不确定性；
- `tests/integration/nftables/golden-state.sh` 在 disposable namespace 验证 IPv4/IPv6 INPUT/FORWARD
  packet path、foreign table preservation、failed batch atomicity 与 Guard cleanup。

这些是隔离的实现级验证，不提升 B3、D4、G18 或 M0 Gate 至 `Verified`/`PASS`。

## 回滚

若未来需要支持不同 layout、backend 或外部 Firewall manager，必须新增 versioned ownership
contract 和对应 Probe/Snapshot/foreign-preservation 测试；不得通过放宽当前 fixed layout 校验实现兼容。

## 重新评估条件

- 支持 UFW、Docker、iptables 或新的 packet-path topology；
- 引入真实目标 Linux 或生产部署验证；
- 修改 owner/version、managed layout、hook/priority 或 backend capability contract。
