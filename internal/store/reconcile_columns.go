package store

// NodeIdentityColumns maps runtime field names for the singleton durable node
// identity shared by Store business transactions.
var NodeIdentityColumns = struct {
	Singleton   string
	NodeID      string
	CreatedAtUS string
}{
	Singleton:   "singleton",
	NodeID:      "node_id",
	CreatedAtUS: "created_at_us",
}

// InfrastructureReconcileStateColumns maps runtime field names for the
// singleton Infrastructure retry ledger.
var InfrastructureReconcileStateColumns = struct {
	Singleton              string
	InfrastructureRevision string
	RetryEpoch             string
	Status                 string
	AttemptCount           string
	LastAttemptAtUS        string
	NextAttemptAtUS        string
	LastErrorCode          string
	UpdatedAtUS            string
}{
	Singleton:              "singleton",
	InfrastructureRevision: "infrastructure_revision",
	RetryEpoch:             "retry_epoch",
	Status:                 "status",
	AttemptCount:           "attempt_count",
	LastAttemptAtUS:        "last_attempt_at_us",
	NextAttemptAtUS:        "next_attempt_at_us",
	LastErrorCode:          "last_error_code",
	UpdatedAtUS:            "updated_at_us",
}

// PolicyReconcileStateColumns maps runtime field names for the singleton
// Policy retry ledger.
var PolicyReconcileStateColumns = struct {
	Singleton       string
	PolicyRevision  string
	RetryEpoch      string
	Status          string
	AttemptCount    string
	LastAttemptAtUS string
	NextAttemptAtUS string
	LastErrorCode   string
	UpdatedAtUS     string
}{
	Singleton:       "singleton",
	PolicyRevision:  "policy_revision",
	RetryEpoch:      "retry_epoch",
	Status:          "status",
	AttemptCount:    "attempt_count",
	LastAttemptAtUS: "last_attempt_at_us",
	NextAttemptAtUS: "next_attempt_at_us",
	LastErrorCode:   "last_error_code",
	UpdatedAtUS:     "updated_at_us",
}

// TargetReconcileStateColumns maps runtime field names for the node-scoped
// Target retry ledger.
var TargetReconcileStateColumns = struct {
	NodeID                      string
	CanonicalTarget             string
	TargetEnforcementGeneration string
	RetryEpoch                  string
	Status                      string
	AttemptCount                string
	LastAttemptAtUS             string
	NextAttemptAtUS             string
	LastErrorCode               string
	UpdatedAtUS                 string
}{
	NodeID:                      "node_id",
	CanonicalTarget:             "canonical_target",
	TargetEnforcementGeneration: "target_enforcement_generation",
	RetryEpoch:                  "retry_epoch",
	Status:                      "status",
	AttemptCount:                "attempt_count",
	LastAttemptAtUS:             "last_attempt_at_us",
	NextAttemptAtUS:             "next_attempt_at_us",
	LastErrorCode:               "last_error_code",
	UpdatedAtUS:                 "updated_at_us",
}

// ReconcileProbeRequirementColumns maps runtime field names for the durable
// Probe requirement composite identity.
var ReconcileProbeRequirementColumns = struct {
	NodeID                      string
	Domain                      string
	CanonicalTarget             string
	InfrastructureRevision      string
	PolicyRevision              string
	TargetEnforcementGeneration string
	SnapshotRevision            string
	FenceSnapshotRevision       string
	RetryEpoch                  string
	AttemptCount                string
	RecordedAtUS                string
}{
	NodeID:                      "node_id",
	Domain:                      "domain",
	CanonicalTarget:             "canonical_target",
	InfrastructureRevision:      "infrastructure_revision",
	PolicyRevision:              "policy_revision",
	TargetEnforcementGeneration: "target_enforcement_generation",
	SnapshotRevision:            "snapshot_revision",
	FenceSnapshotRevision:       "fence_snapshot_revision",
	RetryEpoch:                  "retry_epoch",
	AttemptCount:                "attempt_count",
	RecordedAtUS:                "recorded_at_us",
}
