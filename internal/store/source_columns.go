package store

// SourceColumns contains the runtime column names for sources. Schema remains
// owned by checksummed migrations; callers use this map for GORM clauses.
var SourceColumns = struct {
	ActiveSessionID string
	SourceID        string
	NodeID          string
	Kind            string
	CreatedAtUS     string
	UpdatedAtUS     string
}{
	ActiveSessionID: "active_session_id",
	SourceID:        "source_id",
	NodeID:          "node_id",
	Kind:            "kind",
	CreatedAtUS:     "created_at_us",
	UpdatedAtUS:     "updated_at_us",
}

// SourceCheckpointColumns contains the runtime column names for
// source_checkpoints.
var SourceCheckpointColumns = struct {
	CheckpointSessionID string
	SourceID            string
	DeliverySequence    string
	PositionKind        string
	Generation          string
	DeviceID            string
	Inode               string
	StartOffset         string
	EndOffset           string
	JournaldCursor      string
	PersistedAtUS       string
}{
	CheckpointSessionID: "checkpoint_session_id",
	SourceID:            "source_id",
	DeliverySequence:    "delivery_sequence",
	PositionKind:        "position_kind",
	Generation:          "generation",
	DeviceID:            "device_id",
	Inode:               "inode",
	StartOffset:         "start_offset",
	EndOffset:           "end_offset",
	JournaldCursor:      "journald_cursor",
	PersistedAtUS:       "persisted_at_us",
}

// SourceFileGenerationColumns contains the runtime column names for
// source_file_generations.
var SourceFileGenerationColumns = struct {
	DurableEndOffset    string
	CoverageSessionID   string
	SourceID            string
	Generation          string
	DeviceID            string
	Inode               string
	Path                string
	State               string
	ObservedSize        string
	FinalEOF            string
	MaxDeliverySequence string
	OpenedAtUS          string
	DrainingAtUS        string
	SealedAtUS          string
	RetiredAtUS         string
}{
	DurableEndOffset:    "durable_end_offset",
	CoverageSessionID:   "coverage_session_id",
	SourceID:            "source_id",
	Generation:          "generation",
	DeviceID:            "device_id",
	Inode:               "inode",
	Path:                "path",
	State:               "state",
	ObservedSize:        "observed_size",
	FinalEOF:            "final_eof",
	MaxDeliverySequence: "max_delivery_sequence",
	OpenedAtUS:          "opened_at_us",
	DrainingAtUS:        "draining_at_us",
	SealedAtUS:          "sealed_at_us",
	RetiredAtUS:         "retired_at_us",
}
