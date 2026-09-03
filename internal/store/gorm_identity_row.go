package store

// nodeIdentityRow maps the singleton durable node identity. It is intentionally
// limited to the business columns managed by the Store's GORM query boundary.
type nodeIdentityRow struct {
	Singleton   int64  `gorm:"column:singleton;primaryKey;autoIncrement:false"` // 单例主键固定为一。
	NodeID      string `gorm:"column:node_id"`                                  // 持久化节点身份。
	CreatedAtUS int64  `gorm:"column:created_at_us"`                            // 创建 Unix 微秒。
}

func (nodeIdentityRow) TableName() string {
	return "node_identity"
}
