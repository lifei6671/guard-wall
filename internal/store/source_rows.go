package store

// sourceRow maps the configured source identity. Schema remains owned by
// checksummed migrations; this model only defines GORM's business-data mapping.
type sourceRow struct {
	SourceID    string `gorm:"column:source_id;primaryKey;autoIncrement:false"` // 来源身份主键。
	NodeID      string `gorm:"column:node_id"`                                  // 所属节点身份。
	Kind        string `gorm:"column:kind"`                                     // 受控来源类型枚举。
	CreatedAtUS int64  `gorm:"column:created_at_us"`                            // 创建 Unix 微秒。
	UpdatedAtUS int64  `gorm:"column:updated_at_us"`                            // 更新 Unix 微秒。
}

func (sourceRow) TableName() string { return "sources" }

type sourceCheckpointRow struct {
	SourceID         string  `gorm:"column:source_id;primaryKey;autoIncrement:false"` // 来源身份主键。
	DeliverySequence int64   `gorm:"column:delivery_sequence"`                        // 已持久化的投递序号。
	PositionKind     string  `gorm:"column:position_kind"`                            // 受控 checkpoint 位置类型。
	Generation       *string `gorm:"column:generation"`                               // 文件世代，NULL 表示不适用。
	DeviceID         *int64  `gorm:"column:device_id"`                                // 文件设备号，NULL 表示不适用。
	Inode            *int64  `gorm:"column:inode"`                                    // 文件 inode，NULL 表示不适用。
	StartOffset      *int64  `gorm:"column:start_offset"`                             // 起始偏移，NULL 表示不适用。
	EndOffset        *int64  `gorm:"column:end_offset"`                               // 结束偏移，NULL 表示不适用。
	JournaldCursor   *string `gorm:"column:journald_cursor"`                          // journald 游标，NULL 表示不适用。
	PersistedAtUS    int64   `gorm:"column:persisted_at_us"`                          // 持久化 Unix 微秒。
}

func (sourceCheckpointRow) TableName() string { return "source_checkpoints" }

type sourceFileGenerationRow struct {
	SourceID            string `gorm:"column:source_id;primaryKey;autoIncrement:false"`  // 来源身份，复合主键。
	Generation          string `gorm:"column:generation;primaryKey;autoIncrement:false"` // 文件世代，复合主键。
	DeviceID            int64  `gorm:"column:device_id"`                                 // 文件设备号。
	Inode               int64  `gorm:"column:inode"`                                     // 文件 inode。
	Path                string `gorm:"column:path"`                                      // 受控来源路径。
	State               string `gorm:"column:state"`                                     // 受控世代状态枚举。
	ObservedSize        int64  `gorm:"column:observed_size"`                             // 最近观测文件大小。
	FinalEOF            *int64 `gorm:"column:final_eof"`                                 // 封存终点偏移，NULL 表示未封存。
	MaxDeliverySequence *int64 `gorm:"column:max_delivery_sequence"`                     // 最大投递序号，NULL 表示尚未投递。
	OpenedAtUS          int64  `gorm:"column:opened_at_us"`                              // 打开 Unix 微秒。
	DrainingAtUS        *int64 `gorm:"column:draining_at_us"`                            // draining Unix 微秒，NULL 表示非 draining。
	SealedAtUS          *int64 `gorm:"column:sealed_at_us"`                              // sealed Unix 微秒，NULL 表示未封存。
	RetiredAtUS         *int64 `gorm:"column:retired_at_us"`                             // retired Unix 微秒，NULL 表示未退役。
}

func (sourceFileGenerationRow) TableName() string { return "source_file_generations" }
