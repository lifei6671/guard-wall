package store

// desiredFirewallStateRow maps the singleton Desired snapshot counter.
type desiredFirewallStateRow struct {
	Singleton        int64 `gorm:"column:singleton;primaryKey;autoIncrement:false"` // 单例主键固定为一。
	SnapshotRevision int64 `gorm:"column:snapshot_revision"`                        // 单调递增的 Desired 快照版本。
}

func (desiredFirewallStateRow) TableName() string { return "desired_firewall_state" }

// targetEnforcementStateRow maps Desired Target intent and its physical
// observation columns. Desired readers select only the Desired fields.
type targetEnforcementStateRow struct {
	NodeID                      string `gorm:"column:node_id;primaryKey;autoIncrement:false"`          // 所属节点，复合主键。
	CanonicalTarget             string `gorm:"column:canonical_target;primaryKey;autoIncrement:false"` // 规范目标，复合主键。
	DesiredMembership           string `gorm:"column:desired_membership"`                              // 受控的期望成员状态。
	ObservedMembership          string `gorm:"column:observed_membership"`                             // 最近观测成员状态。
	EffectiveUntilUS            *int64 `gorm:"column:effective_until_us"`                              // 期望到期 Unix 微秒，NULL 表示无到期。
	TimeoutMode                 string `gorm:"column:timeout_mode"`                                    // 受控超时模式枚举。
	Scopes                      int64  `gorm:"column:scopes"`                                          // 生效范围位集。
	AddressFamily               int64  `gorm:"column:address_family"`                                  // 受控地址族枚举。
	PolicyCoverage              string `gorm:"column:policy_coverage"`                                 // 策略覆盖状态。
	PolicyRelationDigest        string `gorm:"column:policy_relation_digest"`                          // 策略关系摘要。
	BackendAttributesDigest     string `gorm:"column:backend_attributes_digest"`                       // 后端属性摘要。
	TargetEnforcementGeneration int64  `gorm:"column:target_enforcement_generation"`                   // 单调目标执行世代。
	ConfirmedTargetGeneration   *int64 `gorm:"column:confirmed_target_enforcement_generation"`         // 已确认目标世代，NULL 表示未确认。
	ConfirmedSnapshotRevision   *int64 `gorm:"column:confirmed_snapshot_revision"`                     // 已确认快照版本，NULL 表示未确认。
	ObservedAtUS                *int64 `gorm:"column:observed_at_us"`                                  // 最近观测 Unix 微秒，NULL 表示未观测。
}

func (targetEnforcementStateRow) TableName() string { return "enforcement_states" }

type allowlistRow struct {
	NodeID          string `gorm:"column:node_id;primaryKey;autoIncrement:false"`          // 所属节点，复合主键。
	CanonicalTarget string `gorm:"column:canonical_target;primaryKey;autoIncrement:false"` // 规范目标，复合主键。
	Enabled         int64  `gorm:"column:enabled"`                                         // SQLite 布尔值，1 为启用。
	PolicyRevision  int64  `gorm:"column:policy_revision"`                                 // 写入该项的策略版本。
	CreatedAtUS     int64  `gorm:"column:created_at_us"`                                   // 创建 Unix 微秒。
	UpdatedAtUS     int64  `gorm:"column:updated_at_us"`                                   // 更新 Unix 微秒。
}

func (allowlistRow) TableName() string { return "allowlists" }

type protectedTargetRow struct {
	NodeID          string `gorm:"column:node_id;primaryKey;autoIncrement:false"`          // 所属节点，复合主键。
	CanonicalTarget string `gorm:"column:canonical_target;primaryKey;autoIncrement:false"` // 规范目标，复合主键。
	Enabled         int64  `gorm:"column:enabled"`                                         // SQLite 布尔值，1 为启用。
	PolicyRevision  int64  `gorm:"column:policy_revision"`                                 // 写入该项的策略版本。
	CreatedAtUS     int64  `gorm:"column:created_at_us"`                                   // 创建 Unix 微秒。
	UpdatedAtUS     int64  `gorm:"column:updated_at_us"`                                   // 更新 Unix 微秒。
}

func (protectedTargetRow) TableName() string { return "protected_targets" }
