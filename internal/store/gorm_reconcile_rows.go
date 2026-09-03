package store

// infrastructureReconcileStateRow maps the singleton Infrastructure retry
// ledger. Schema ownership remains with checksummed SQLite migrations.
type infrastructureReconcileStateRow struct {
	Singleton              int64   `gorm:"column:singleton;primaryKey;autoIncrement:false"` // 单例主键固定为一。
	InfrastructureRevision int64   `gorm:"column:infrastructure_revision"`                  // 基础设施期望版本。
	RetryEpoch             int64   `gorm:"column:retry_epoch"`                              // 本次重试世代。
	Status                 string  `gorm:"column:status"`                                   // 受控的重试状态枚举。
	AttemptCount           int64   `gorm:"column:attempt_count"`                            // 当前世代累计尝试次数。
	LastAttemptAtUS        *int64  `gorm:"column:last_attempt_at_us"`                       // 最近尝试的 Unix 微秒，NULL 表示尚未尝试。
	NextAttemptAtUS        *int64  `gorm:"column:next_attempt_at_us"`                       // 下次尝试的 Unix 微秒，NULL 表示未排期。
	LastErrorCode          *string `gorm:"column:last_error_code"`                          // 最近失败码，NULL 表示没有失败。
	UpdatedAtUS            int64   `gorm:"column:updated_at_us"`                            // 行最后更新的 Unix 微秒。
}

func (infrastructureReconcileStateRow) TableName() string {
	return "infrastructure_reconcile_state"
}

// policyReconcileStateRow maps the singleton Policy retry ledger.
type policyReconcileStateRow struct {
	Singleton       int64   `gorm:"column:singleton;primaryKey;autoIncrement:false"` // 单例主键固定为一。
	PolicyRevision  int64   `gorm:"column:policy_revision"`                          // 策略期望版本。
	RetryEpoch      int64   `gorm:"column:retry_epoch"`                              // 本次重试世代。
	Status          string  `gorm:"column:status"`                                   // 受控的重试状态枚举。
	AttemptCount    int64   `gorm:"column:attempt_count"`                            // 当前世代累计尝试次数。
	LastAttemptAtUS *int64  `gorm:"column:last_attempt_at_us"`                       // 最近尝试的 Unix 微秒，NULL 表示尚未尝试。
	NextAttemptAtUS *int64  `gorm:"column:next_attempt_at_us"`                       // 下次尝试的 Unix 微秒，NULL 表示未排期。
	LastErrorCode   *string `gorm:"column:last_error_code"`                          // 最近失败码，NULL 表示没有失败。
	UpdatedAtUS     int64   `gorm:"column:updated_at_us"`                            // 行最后更新的 Unix 微秒。
}

func (policyReconcileStateRow) TableName() string {
	return "policy_reconcile_state"
}

// targetReconcileStateRow maps the node-scoped Target retry ledger.
type targetReconcileStateRow struct {
	NodeID                      string  `gorm:"column:node_id;primaryKey;autoIncrement:false"`          // 所属节点，复合主键。
	CanonicalTarget             string  `gorm:"column:canonical_target;primaryKey;autoIncrement:false"` // 规范目标，复合主键。
	TargetEnforcementGeneration int64   `gorm:"column:target_enforcement_generation"`                   // 目标执行世代。
	RetryEpoch                  int64   `gorm:"column:retry_epoch"`                                     // 本次重试世代。
	Status                      string  `gorm:"column:status"`                                          // 受控的重试状态枚举。
	AttemptCount                int64   `gorm:"column:attempt_count"`                                   // 当前世代累计尝试次数。
	LastAttemptAtUS             *int64  `gorm:"column:last_attempt_at_us"`                              // 最近尝试的 Unix 微秒，NULL 表示尚未尝试。
	NextAttemptAtUS             *int64  `gorm:"column:next_attempt_at_us"`                              // 下次尝试的 Unix 微秒，NULL 表示未排期。
	LastErrorCode               *string `gorm:"column:last_error_code"`                                 // 最近失败码，NULL 表示没有失败。
	UpdatedAtUS                 int64   `gorm:"column:updated_at_us"`                                   // 行最后更新的 Unix 微秒。
}

func (targetReconcileStateRow) TableName() string {
	return "target_reconcile_state"
}

// reconcileProbeRequirementRow maps the full durable Probe identity. Every
// key field participates in the composite primary key.
type reconcileProbeRequirementRow struct {
	NodeID                      string `gorm:"column:node_id;primaryKey;autoIncrement:false"`                       // 所属节点，复合主键。
	Domain                      string `gorm:"column:domain;primaryKey;autoIncrement:false"`                        // 受控 reconcile 域，复合主键。
	CanonicalTarget             string `gorm:"column:canonical_target;primaryKey;autoIncrement:false"`              // 目标域的规范目标；单例域为空，复合主键。
	InfrastructureRevision      int64  `gorm:"column:infrastructure_revision;primaryKey;autoIncrement:false"`       // 基础设施版本，复合主键。
	PolicyRevision              int64  `gorm:"column:policy_revision;primaryKey;autoIncrement:false"`               // 策略版本，复合主键。
	TargetEnforcementGeneration int64  `gorm:"column:target_enforcement_generation;primaryKey;autoIncrement:false"` // 目标执行世代，复合主键。
	SnapshotRevision            int64  `gorm:"column:snapshot_revision;primaryKey;autoIncrement:false"`             // Desired 快照版本，复合主键。
	FenceSnapshotRevision       int64  `gorm:"column:fence_snapshot_revision;primaryKey;autoIncrement:false"`       // fence 快照版本，复合主键。
	RetryEpoch                  int64  `gorm:"column:retry_epoch;primaryKey;autoIncrement:false"`                   // 重试世代，复合主键。
	AttemptCount                int64  `gorm:"column:attempt_count;primaryKey;autoIncrement:false"`                 // 尝试次数，复合主键。
	RecordedAtUS                int64  `gorm:"column:recorded_at_us"`                                               // 写入要求的 Unix 微秒。
}

func (reconcileProbeRequirementRow) TableName() string {
	return "reconcile_probe_requirements"
}
