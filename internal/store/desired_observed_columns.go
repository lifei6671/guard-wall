package store

// DesiredFirewallStateColumns maps desired_firewall_state runtime columns.
var DesiredFirewallStateColumns = struct {
	Singleton        string
	SnapshotRevision string
}{
	Singleton:        "singleton",
	SnapshotRevision: "snapshot_revision",
}

// EnforcementStateColumns maps enforcement_states runtime columns.
var EnforcementStateColumns = struct {
	NodeID                               string
	CanonicalTarget                      string
	DesiredMembership                    string
	ObservedMembership                   string
	EffectiveUntilUS                     string
	TimeoutMode                          string
	Scopes                               string
	AddressFamily                        string
	PolicyCoverage                       string
	PolicyRelationDigest                 string
	BackendAttributesDigest              string
	TargetEnforcementGeneration          string
	ConfirmedTargetEnforcementGeneration string
	ConfirmedSnapshotRevision            string
	ObservedAtUS                         string
	ObservedEvidence                     string
	ObservedBackend                      string
	ObservedPolicyCoverage               string
	ObservedPolicyRelationDigest         string
	ObservedTimeoutMode                  string
	ObservedNativeExpiryUS               string
	ObservedScopes                       string
	ObservedAddressFamily                string
	ObservedOwnerVersion                 string
	ObservedLastErrorCode                string
}{
	NodeID:                               "node_id",
	CanonicalTarget:                      "canonical_target",
	DesiredMembership:                    "desired_membership",
	ObservedMembership:                   "observed_membership",
	EffectiveUntilUS:                     "effective_until_us",
	TimeoutMode:                          "timeout_mode",
	Scopes:                               "scopes",
	AddressFamily:                        "address_family",
	PolicyCoverage:                       "policy_coverage",
	PolicyRelationDigest:                 "policy_relation_digest",
	BackendAttributesDigest:              "backend_attributes_digest",
	TargetEnforcementGeneration:          "target_enforcement_generation",
	ConfirmedTargetEnforcementGeneration: "confirmed_target_enforcement_generation",
	ConfirmedSnapshotRevision:            "confirmed_snapshot_revision",
	ObservedAtUS:                         "observed_at_us",
	ObservedEvidence:                     "observed_evidence",
	ObservedBackend:                      "observed_backend",
	ObservedPolicyCoverage:               "observed_policy_coverage",
	ObservedPolicyRelationDigest:         "observed_policy_relation_digest",
	ObservedTimeoutMode:                  "observed_timeout_mode",
	ObservedNativeExpiryUS:               "observed_native_expiry_us",
	ObservedScopes:                       "observed_scopes",
	ObservedAddressFamily:                "observed_address_family",
	ObservedOwnerVersion:                 "observed_owner_version",
	ObservedLastErrorCode:                "observed_last_error_code",
}

// AllowlistColumns maps allowlists runtime columns.
var AllowlistColumns = struct {
	NodeID          string
	CanonicalTarget string
	Enabled         string
	PolicyRevision  string
	CreatedAtUS     string
	UpdatedAtUS     string
}{
	NodeID:          "node_id",
	CanonicalTarget: "canonical_target",
	Enabled:         "enabled",
	PolicyRevision:  "policy_revision",
	CreatedAtUS:     "created_at_us",
	UpdatedAtUS:     "updated_at_us",
}

// ProtectedTargetColumns maps protected_targets runtime columns.
var ProtectedTargetColumns = struct {
	NodeID          string
	CanonicalTarget string
	Enabled         string
	PolicyRevision  string
	CreatedAtUS     string
	UpdatedAtUS     string
}{
	NodeID:          "node_id",
	CanonicalTarget: "canonical_target",
	Enabled:         "enabled",
	PolicyRevision:  "policy_revision",
	CreatedAtUS:     "created_at_us",
	UpdatedAtUS:     "updated_at_us",
}

// InfrastructureObservedStateColumns maps infrastructure_observed_state runtime columns.
var InfrastructureObservedStateColumns = struct {
	Singleton                       string
	NodeID                          string
	Presence                        string
	ObservedAtUS                    string
	Backend                         string
	OwnerVersion                    string
	Digest                          string
	ConfirmedInfrastructureRevision string
	LastErrorCode                   string
}{
	Singleton:                       "singleton",
	NodeID:                          "node_id",
	Presence:                        "presence",
	ObservedAtUS:                    "observed_at_us",
	Backend:                         "backend",
	OwnerVersion:                    "owner_version",
	Digest:                          "digest",
	ConfirmedInfrastructureRevision: "confirmed_infrastructure_revision",
	LastErrorCode:                   "last_error_code",
}

// PolicyObservedStateColumns maps policy_observed_state runtime columns.
var PolicyObservedStateColumns = struct {
	Singleton               string
	NodeID                  string
	Presence                string
	ObservedAtUS            string
	RelationDigest          string
	ConfirmedPolicyRevision string
	LastErrorCode           string
}{
	Singleton:               "singleton",
	NodeID:                  "node_id",
	Presence:                "presence",
	ObservedAtUS:            "observed_at_us",
	RelationDigest:          "relation_digest",
	ConfirmedPolicyRevision: "confirmed_policy_revision",
	LastErrorCode:           "last_error_code",
}
