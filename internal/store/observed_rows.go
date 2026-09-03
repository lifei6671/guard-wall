package store

import "database/sql"

// observedNodeIdentityRow is the narrow NodeID fence used by the Observed
// persistence path. The migration remains the schema authority.
type observedNodeIdentityRow struct {
	Singleton int64  `gorm:"column:singleton;primaryKey;autoIncrement:false"` // 单例主键固定为一。
	NodeID    string `gorm:"column:node_id"`                                  // 被 fence 校验的节点身份。
}

func (observedNodeIdentityRow) TableName() string { return "node_identity" }

type infrastructureObservedRow struct {
	Singleton         int64         `gorm:"column:singleton;primaryKey;autoIncrement:false"` // 单例主键固定为一。
	NodeID            string        `gorm:"column:node_id"`                                  // 被观测节点身份。
	Presence          string        `gorm:"column:presence"`                                 // 受控资源存在状态。
	ObservedAtUS      int64         `gorm:"column:observed_at_us"`                           // 观测 Unix 微秒。
	Backend           string        `gorm:"column:backend"`                                  // 观测后端标识。
	OwnerVersion      string        `gorm:"column:owner_version"`                            // 后端所有者版本。
	Digest            string        `gorm:"column:digest"`                                   // 观测内容摘要。
	ConfirmedRevision sql.NullInt64 `gorm:"column:confirmed_infrastructure_revision"`        // 已确认基础设施版本，Valid=false 表示 NULL。
	LastErrorCode     string        `gorm:"column:last_error_code"`                          // 最近观测失败码。
}

func (infrastructureObservedRow) TableName() string { return "infrastructure_observed_state" }

type policyObservedRow struct {
	Singleton         int64         `gorm:"column:singleton;primaryKey;autoIncrement:false"` // 单例主键固定为一。
	NodeID            string        `gorm:"column:node_id"`                                  // 被观测节点身份。
	Presence          string        `gorm:"column:presence"`                                 // 受控策略存在状态。
	ObservedAtUS      int64         `gorm:"column:observed_at_us"`                           // 观测 Unix 微秒。
	RelationDigest    string        `gorm:"column:relation_digest"`                          // 观测策略关系摘要。
	ConfirmedRevision sql.NullInt64 `gorm:"column:confirmed_policy_revision"`                // 已确认策略版本，Valid=false 表示 NULL。
	LastErrorCode     string        `gorm:"column:last_error_code"`                          // 最近观测失败码。
}

func (policyObservedRow) TableName() string { return "policy_observed_state" }

// targetObservedRow deliberately contains only Desired fence and Observed
// cache columns. Desired intent ownership remains in desired_state.go.
type targetObservedRow struct {
	NodeID              string        `gorm:"column:node_id;primaryKey;autoIncrement:false"`          // 所属节点，复合主键。
	CanonicalTarget     string        `gorm:"column:canonical_target;primaryKey;autoIncrement:false"` // 规范目标，复合主键。
	TargetGeneration    int64         `gorm:"column:target_enforcement_generation"`                   // 对应的目标执行世代。
	ObservedMembership  string        `gorm:"column:observed_membership"`                             // 最近观测成员状态。
	ObservedAtUS        sql.NullInt64 `gorm:"column:observed_at_us"`                                  // 观测 Unix 微秒，Valid=false 表示 NULL。
	ObservedEvidence    string        `gorm:"column:observed_evidence"`                               // 观测证据摘要。
	ObservedBackend     string        `gorm:"column:observed_backend"`                                // 观测后端标识。
	PolicyCoverage      string        `gorm:"column:observed_policy_coverage"`                        // 观测策略覆盖状态。
	PolicyDigest        string        `gorm:"column:observed_policy_relation_digest"`                 // 观测策略关系摘要。
	TimeoutMode         string        `gorm:"column:observed_timeout_mode"`                           // 观测超时模式。
	NativeExpiryUS      sql.NullInt64 `gorm:"column:observed_native_expiry_us"`                       // 原生到期 Unix 微秒，Valid=false 表示 NULL。
	Scopes              int64         `gorm:"column:observed_scopes"`                                 // 观测生效范围位集。
	AddressFamily       int64         `gorm:"column:observed_address_family"`                         // 观测地址族枚举。
	OwnerVersion        string        `gorm:"column:observed_owner_version"`                          // 后端所有者版本。
	LastErrorCode       string        `gorm:"column:observed_last_error_code"`                        // 最近观测失败码。
	ConfirmedGeneration sql.NullInt64 `gorm:"column:confirmed_target_enforcement_generation"`         // 已确认目标世代，Valid=false 表示 NULL。
}

func (targetObservedRow) TableName() string { return "enforcement_states" }
