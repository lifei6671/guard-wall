CREATE TABLE enforcement_states_v6 (
    node_id TEXT NOT NULL,
    canonical_target TEXT NOT NULL CHECK (length(canonical_target) BETWEEN 3 AND 64),
    desired_membership TEXT NOT NULL CHECK (desired_membership IN ('absent', 'present')),
    observed_membership TEXT NOT NULL CHECK (observed_membership IN ('unknown', 'absent', 'present')),
    effective_until_us INTEGER,
    timeout_mode TEXT NOT NULL CHECK (timeout_mode IN ('none', 'native')),
    scopes INTEGER NOT NULL CHECK (scopes BETWEEN 1 AND 3),
    address_family INTEGER NOT NULL CHECK (address_family IN (4, 6)),
    policy_coverage TEXT NOT NULL CHECK (policy_coverage IN ('none', 'partial', 'full')),
    policy_relation_digest TEXT NOT NULL,
    backend_attributes_digest TEXT NOT NULL CHECK (length(backend_attributes_digest) = 64),
    target_enforcement_generation INTEGER NOT NULL
        CHECK (target_enforcement_generation BETWEEN 1 AND 9223372036854775807),
    confirmed_target_enforcement_generation INTEGER,
    confirmed_snapshot_revision INTEGER CHECK (confirmed_snapshot_revision IS NULL),
    observed_at_us INTEGER,
    observed_evidence TEXT NOT NULL DEFAULT 'complete'
        CHECK (observed_evidence IN ('complete', 'managed_snapshot')),
    observed_backend TEXT NOT NULL DEFAULT '',
    observed_policy_coverage TEXT NOT NULL DEFAULT 'unknown'
        CHECK (observed_policy_coverage IN ('unknown', 'none', 'partial', 'full')),
    observed_policy_relation_digest TEXT NOT NULL DEFAULT '',
    observed_timeout_mode TEXT NOT NULL DEFAULT 'none'
        CHECK (observed_timeout_mode IN ('none', 'native')),
    observed_native_expiry_us INTEGER,
    observed_scopes INTEGER NOT NULL DEFAULT 0 CHECK (observed_scopes BETWEEN 0 AND 3),
    observed_address_family INTEGER NOT NULL DEFAULT 0 CHECK (observed_address_family IN (0, 4, 6)),
    observed_owner_version TEXT NOT NULL DEFAULT '',
    observed_last_error_code TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (node_id, canonical_target),
    CHECK (
        (policy_coverage = 'none' AND policy_relation_digest = '')
        OR
        (policy_coverage IN ('partial', 'full') AND length(policy_relation_digest) = 64)
    ),
    CHECK (confirmed_target_enforcement_generation IS NULL
        OR confirmed_target_enforcement_generation BETWEEN 1 AND 9223372036854775807),
    CHECK (observed_native_expiry_us IS NULL OR observed_native_expiry_us > 0),
    CHECK (
        (observed_at_us IS NULL
            AND observed_membership = 'unknown'
            AND observed_evidence = 'complete'
            AND observed_backend = ''
            AND observed_policy_coverage = 'unknown'
            AND observed_policy_relation_digest = ''
            AND observed_timeout_mode = 'none'
            AND observed_native_expiry_us IS NULL
            AND observed_scopes = 0
            AND observed_address_family = 0
            AND observed_owner_version = ''
            AND observed_last_error_code = ''
            AND confirmed_target_enforcement_generation IS NULL)
        OR (observed_at_us IS NOT NULL AND observed_at_us > 0 AND (
            (observed_membership = 'unknown'
                AND observed_evidence = 'complete'
                AND observed_backend = ''
                AND observed_policy_coverage = 'unknown'
                AND observed_policy_relation_digest = ''
                AND observed_timeout_mode = 'none'
                AND observed_native_expiry_us IS NULL
                AND observed_scopes = 0
                AND observed_address_family = 0
                AND observed_owner_version = ''
                AND length(observed_last_error_code) > 0
                AND confirmed_target_enforcement_generation IS NULL)
            OR (observed_membership = 'absent'
                AND observed_backend = ''
                AND observed_policy_coverage = 'unknown'
                AND observed_policy_relation_digest = ''
                AND observed_timeout_mode = 'none'
                AND observed_native_expiry_us IS NULL
                AND observed_scopes = 0
                AND observed_address_family = 0
                AND observed_owner_version = ''
                AND observed_last_error_code = '')
            OR (observed_membership = 'present'
                AND observed_evidence = 'complete'
                AND length(observed_backend) > 0
                AND observed_policy_coverage IN ('none', 'partial', 'full')
                AND ((observed_policy_coverage = 'none' AND observed_policy_relation_digest = '')
                    OR (observed_policy_coverage IN ('partial', 'full')
                        AND length(observed_policy_relation_digest) > 0))
                AND ((observed_timeout_mode = 'none' AND observed_native_expiry_us IS NULL)
                    OR (observed_timeout_mode = 'native' AND observed_native_expiry_us > 0))
                AND observed_scopes BETWEEN 1 AND 3
                AND observed_address_family IN (4, 6)
                AND length(observed_owner_version) > 0
                AND observed_last_error_code = '')
            OR (observed_membership = 'present'
                AND observed_evidence = 'managed_snapshot'
                AND observed_backend = ''
                AND observed_policy_coverage = 'unknown'
                AND observed_policy_relation_digest = ''
                AND ((observed_timeout_mode = 'none' AND observed_native_expiry_us IS NULL)
                    OR (observed_timeout_mode = 'native' AND observed_native_expiry_us > 0))
                AND observed_scopes BETWEEN 1 AND 3
                AND observed_address_family IN (4, 6)
                AND observed_owner_version = ''
                AND observed_last_error_code = '')
        ))
    ),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

INSERT INTO enforcement_states_v6(
    node_id, canonical_target, desired_membership, observed_membership,
    effective_until_us, timeout_mode, scopes, address_family,
    policy_coverage, policy_relation_digest, backend_attributes_digest,
    target_enforcement_generation, confirmed_target_enforcement_generation,
    confirmed_snapshot_revision, observed_at_us, observed_evidence,
    observed_backend, observed_policy_coverage, observed_policy_relation_digest,
    observed_timeout_mode, observed_native_expiry_us, observed_scopes,
    observed_address_family, observed_owner_version, observed_last_error_code
)
SELECT
    node_id, canonical_target, desired_membership, observed_membership,
    effective_until_us, timeout_mode, scopes, address_family,
    policy_coverage, policy_relation_digest, backend_attributes_digest,
    target_enforcement_generation, confirmed_target_enforcement_generation,
    confirmed_snapshot_revision, observed_at_us, 'complete',
    observed_backend, observed_policy_coverage, observed_policy_relation_digest,
    observed_timeout_mode, observed_native_expiry_us, observed_scopes,
    observed_address_family, observed_owner_version, observed_last_error_code
FROM enforcement_states;

DROP TABLE enforcement_states;
ALTER TABLE enforcement_states_v6 RENAME TO enforcement_states;

-- A policy-only node has no Target write to advance this global fence. Preserve
-- empty nodes at revision zero, but advance the fence for each policy-row
-- mutation in the writer's SQLite transaction.
UPDATE desired_firewall_state
SET snapshot_revision = 1
WHERE singleton = 1 AND snapshot_revision = 0
  AND (EXISTS (SELECT 1 FROM allowlists) OR EXISTS (SELECT 1 FROM protected_targets));

CREATE TRIGGER desired_firewall_snapshot_advance_allowlist_insert
AFTER INSERT ON allowlists
BEGIN
    UPDATE desired_firewall_state
    SET snapshot_revision = snapshot_revision + 1
    WHERE singleton = 1 AND snapshot_revision < 9223372036854775807;
    SELECT CASE WHEN changes() = 0 THEN RAISE(ABORT, 'desired snapshot revision is exhausted') END;
END;

CREATE TRIGGER desired_firewall_snapshot_advance_allowlist_update
AFTER UPDATE OF canonical_target, enabled, policy_revision ON allowlists
WHEN OLD.canonical_target IS NOT NEW.canonical_target
  OR OLD.enabled IS NOT NEW.enabled
  OR OLD.policy_revision IS NOT NEW.policy_revision
BEGIN
    UPDATE desired_firewall_state
    SET snapshot_revision = snapshot_revision + 1
    WHERE singleton = 1 AND snapshot_revision < 9223372036854775807;
    SELECT CASE WHEN changes() = 0 THEN RAISE(ABORT, 'desired snapshot revision is exhausted') END;
END;

CREATE TRIGGER desired_firewall_snapshot_advance_allowlist_delete
AFTER DELETE ON allowlists
BEGIN
    UPDATE desired_firewall_state
    SET snapshot_revision = snapshot_revision + 1
    WHERE singleton = 1 AND snapshot_revision < 9223372036854775807;
    SELECT CASE WHEN changes() = 0 THEN RAISE(ABORT, 'desired snapshot revision is exhausted') END;
END;

CREATE TRIGGER desired_firewall_snapshot_advance_protected_target_insert
AFTER INSERT ON protected_targets
BEGIN
    UPDATE desired_firewall_state
    SET snapshot_revision = snapshot_revision + 1
    WHERE singleton = 1 AND snapshot_revision < 9223372036854775807;
    SELECT CASE WHEN changes() = 0 THEN RAISE(ABORT, 'desired snapshot revision is exhausted') END;
END;

CREATE TRIGGER desired_firewall_snapshot_advance_protected_target_update
AFTER UPDATE OF canonical_target, enabled, policy_revision ON protected_targets
WHEN OLD.canonical_target IS NOT NEW.canonical_target
  OR OLD.enabled IS NOT NEW.enabled
  OR OLD.policy_revision IS NOT NEW.policy_revision
BEGIN
    UPDATE desired_firewall_state
    SET snapshot_revision = snapshot_revision + 1
    WHERE singleton = 1 AND snapshot_revision < 9223372036854775807;
    SELECT CASE WHEN changes() = 0 THEN RAISE(ABORT, 'desired snapshot revision is exhausted') END;
END;

CREATE TRIGGER desired_firewall_snapshot_advance_protected_target_delete
AFTER DELETE ON protected_targets
BEGIN
    UPDATE desired_firewall_state
    SET snapshot_revision = snapshot_revision + 1
    WHERE singleton = 1 AND snapshot_revision < 9223372036854775807;
    SELECT CASE WHEN changes() = 0 THEN RAISE(ABORT, 'desired snapshot revision is exhausted') END;
END;
