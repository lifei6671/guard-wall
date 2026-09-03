package store

// ParserTerminalOutcomeColumns names the parser_terminal_outcomes table columns
// used by runtime queries.
var ParserTerminalOutcomeColumns = struct {
	DeliveryID    string
	ParserID      string
	ParserVersion string
	Kind          string
	EmittedCount  string
	FailureCode   string
	CompletedAtUS string
}{
	DeliveryID:    "delivery_id",
	ParserID:      "parser_id",
	ParserVersion: "parser_version",
	Kind:          "kind",
	EmittedCount:  "emitted_count",
	FailureCode:   "failure_code",
	CompletedAtUS: "completed_at_us",
}

// DetectionTerminalOutcomeColumns names the detection_terminal_outcomes table
// columns used by runtime queries.
var DetectionTerminalOutcomeColumns = struct {
	DeliveryID    string
	EventID       string
	RuleID        string
	RuleVersion   string
	Kind          string
	FailureCode   string
	CompletedAtUS string
}{
	DeliveryID:    "delivery_id",
	EventID:       "event_id",
	RuleID:        "rule_id",
	RuleVersion:   "rule_version",
	Kind:          "kind",
	FailureCode:   "failure_code",
	CompletedAtUS: "completed_at_us",
}

// AlertColumns names the alerts table columns used by runtime queries.
var AlertColumns = struct {
	AlertID         string
	NodeID          string
	EventID         string
	RuleID          string
	RuleVersion     string
	CanonicalTarget string
	ObservedAtUS    string
	CreatedAtUS     string
}{
	AlertID:         "alert_id",
	NodeID:          "node_id",
	EventID:         "event_id",
	RuleID:          "rule_id",
	RuleVersion:     "rule_version",
	CanonicalTarget: "canonical_target",
	ObservedAtUS:    "observed_at_us",
	CreatedAtUS:     "created_at_us",
}

// DecisionColumns names the decisions table columns used by runtime queries.
var DecisionColumns = struct {
	DecisionID        string
	NodeID            string
	Source            string
	RuleID            string
	RuleVersion       string
	AlertID           string
	CanonicalTarget   string
	CreatedAtUS       string
	UpdatedAtUS       string
	LastTriggeredAtUS string
	ExpiresAtUS       string
	EndedAtUS         string
	State             string
	EndReason         string
	SuppressedCount   string
}{
	DecisionID:        "decision_id",
	NodeID:            "node_id",
	Source:            "source",
	RuleID:            "rule_id",
	RuleVersion:       "rule_version",
	AlertID:           "alert_id",
	CanonicalTarget:   "canonical_target",
	CreatedAtUS:       "created_at_us",
	UpdatedAtUS:       "updated_at_us",
	LastTriggeredAtUS: "last_triggered_at_us",
	ExpiresAtUS:       "expires_at_us",
	EndedAtUS:         "ended_at_us",
	State:             "state",
	EndReason:         "end_reason",
	SuppressedCount:   "suppressed_count",
}

// CriticalAuditColumns names the audit_logs table columns used by runtime
// queries.
var CriticalAuditColumns = struct {
	AuditID        string
	IdempotencyKey string
	NodeID         string
	Category       string
	Action         string
	Result         string
	Severity       string
	Critical       string
	ActorType      string
	DeliveryID     string
	AlertID        string
	DecisionID     string
	ErrorCode      string
	DetailsJSON    string
	CreatedAtUS    string
}{
	AuditID:        "audit_id",
	IdempotencyKey: "idempotency_key",
	NodeID:         "node_id",
	Category:       "category",
	Action:         "action",
	Result:         "result",
	Severity:       "severity",
	Critical:       "critical",
	ActorType:      "actor_type",
	DeliveryID:     "delivery_id",
	AlertID:        "alert_id",
	DecisionID:     "decision_id",
	ErrorCode:      "error_code",
	DetailsJSON:    "details_json",
	CreatedAtUS:    "created_at_us",
}

// DetectionContributionColumns names the detection_contributions table
// columns used by runtime queries.
var DetectionContributionColumns = struct {
	EventID         string
	RuleID          string
	RuleVersion     string
	DeliveryID      string
	ContributedAtUS string
}{
	EventID:         "event_id",
	RuleID:          "rule_id",
	RuleVersion:     "rule_version",
	DeliveryID:      "delivery_id",
	ContributedAtUS: "contributed_at_us",
}

// DesiredBanProjectionColumns names the desired_ban_projections table columns
// used by runtime queries.
var DesiredBanProjectionColumns = struct {
	NodeID                   string
	CanonicalTarget          string
	State                    string
	ActiveCount              string
	EffectiveUntilUS         string
	TargetProjectionRevision string
	UpdatedAtUS              string
}{
	NodeID:                   "node_id",
	CanonicalTarget:          "canonical_target",
	State:                    "state",
	ActiveCount:              "active_count",
	EffectiveUntilUS:         "effective_until_us",
	TargetProjectionRevision: "target_projection_revision",
	UpdatedAtUS:              "updated_at_us",
}

// ProcessingReceiptColumns names the processing_receipts table columns used
// by runtime queries.
var ProcessingReceiptColumns = struct {
	DeliveryID          string
	SourceID            string
	PositionKind        string
	Generation          string
	DeviceID            string
	Inode               string
	StartOffset         string
	EndOffset           string
	JournaldCursor      string
	Kind                string
	FailureStage        string
	FailureCode         string
	SanitizedError      string
	TerminalAction      string
	FailureOccurredAtUS string
	CommittedAtUS       string
}{
	DeliveryID:          "delivery_id",
	SourceID:            "source_id",
	PositionKind:        "position_kind",
	Generation:          "generation",
	DeviceID:            "device_id",
	Inode:               "inode",
	StartOffset:         "start_offset",
	EndOffset:           "end_offset",
	JournaldCursor:      "journald_cursor",
	Kind:                "kind",
	FailureStage:        "failure_stage",
	FailureCode:         "failure_code",
	SanitizedError:      "sanitized_error",
	TerminalAction:      "terminal_action",
	FailureOccurredAtUS: "failure_occurred_at_us",
	CommittedAtUS:       "committed_at_us",
}
