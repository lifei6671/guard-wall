CREATE TABLE infrastructure_observed_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    node_id TEXT NOT NULL UNIQUE,
    presence TEXT NOT NULL CHECK (presence IN ('unknown', 'absent', 'present')),
    observed_at_us INTEGER NOT NULL CHECK (observed_at_us > 0),
    backend TEXT NOT NULL,
    owner_version TEXT NOT NULL,
    digest TEXT NOT NULL,
    confirmed_infrastructure_revision INTEGER,
    last_error_code TEXT NOT NULL,
    CHECK (confirmed_infrastructure_revision IS NULL
        OR confirmed_infrastructure_revision BETWEEN 1 AND 9223372036854775807),
    CHECK (
        (presence = 'unknown'
            AND backend = '' AND owner_version = '' AND digest = ''
            AND confirmed_infrastructure_revision IS NULL
            AND length(last_error_code) > 0)
        OR (presence = 'absent'
            AND backend = '' AND owner_version = '' AND digest = ''
            AND confirmed_infrastructure_revision IS NULL
            AND last_error_code = '')
        OR (presence = 'present'
            AND length(backend) > 0 AND length(owner_version) > 0 AND length(digest) > 0
            AND last_error_code = '')
    ),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE policy_observed_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    node_id TEXT NOT NULL UNIQUE,
    presence TEXT NOT NULL CHECK (presence IN ('unknown', 'absent', 'present')),
    observed_at_us INTEGER NOT NULL CHECK (observed_at_us > 0),
    relation_digest TEXT NOT NULL,
    confirmed_policy_revision INTEGER,
    last_error_code TEXT NOT NULL,
    CHECK (confirmed_policy_revision IS NULL
        OR confirmed_policy_revision BETWEEN 1 AND 9223372036854775807),
    CHECK (
        (presence = 'unknown'
            AND relation_digest = '' AND confirmed_policy_revision IS NULL
            AND length(last_error_code) > 0)
        OR (presence = 'absent'
            AND relation_digest = '' AND confirmed_policy_revision IS NULL
            AND last_error_code = '')
        OR (presence = 'present'
            AND length(relation_digest) > 0 AND last_error_code = '')
    ),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE enforcement_states_v5 (
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
        ))
    ),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

-- v4 did not persist enough physical Target attributes to reconstruct an
-- authoritative observation. Preserve Desired state but deliberately discard
-- that incomplete cache and every claimed confirmation during migration.
INSERT INTO enforcement_states_v5(
    node_id, canonical_target, desired_membership, observed_membership,
    effective_until_us, timeout_mode, scopes, address_family,
    policy_coverage, policy_relation_digest, backend_attributes_digest,
    target_enforcement_generation, confirmed_target_enforcement_generation,
    confirmed_snapshot_revision, observed_at_us
)
SELECT
    node_id, canonical_target, desired_membership, 'unknown',
    effective_until_us, timeout_mode, scopes, address_family,
    policy_coverage, policy_relation_digest, backend_attributes_digest,
    target_enforcement_generation, NULL, NULL, NULL
FROM enforcement_states;

DROP TABLE enforcement_states;
ALTER TABLE enforcement_states_v5 RENAME TO enforcement_states;
