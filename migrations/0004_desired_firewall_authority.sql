CREATE TABLE desired_firewall_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    snapshot_revision INTEGER NOT NULL
        CHECK (snapshot_revision BETWEEN 0 AND 9223372036854775807)
) STRICT;

INSERT INTO desired_firewall_state(singleton, snapshot_revision)
SELECT 1, max(
    CASE WHEN EXISTS(SELECT 1 FROM enforcement_states) THEN 1 ELSE 0 END,
    coalesce((SELECT max(confirmed_snapshot_revision) FROM enforcement_states), 0),
    coalesce((SELECT max(snapshot_revision) FROM reconcile_probe_requirements), 0)
);

CREATE TABLE enforcement_states_v4 (
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
    confirmed_snapshot_revision INTEGER,
    observed_at_us INTEGER,
    PRIMARY KEY (node_id, canonical_target),
    CHECK (
        (policy_coverage = 'none' AND policy_relation_digest = '')
        OR
        (policy_coverage IN ('partial', 'full') AND length(policy_relation_digest) = 64)
    ),
    CHECK (confirmed_target_enforcement_generation IS NULL
        OR confirmed_target_enforcement_generation BETWEEN 1 AND 9223372036854775807),
    CHECK (confirmed_snapshot_revision IS NULL
        OR confirmed_snapshot_revision BETWEEN 1 AND 9223372036854775807),
    CHECK (observed_at_us IS NOT NULL OR observed_membership = 'unknown'),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

INSERT INTO enforcement_states_v4(
    node_id, canonical_target, desired_membership, observed_membership,
    effective_until_us, timeout_mode, scopes, address_family,
    policy_coverage, policy_relation_digest, backend_attributes_digest,
    target_enforcement_generation, confirmed_target_enforcement_generation,
    confirmed_snapshot_revision, observed_at_us
)
SELECT
    node_id, canonical_target, desired_membership, observed_membership,
    effective_until_us, timeout_mode, scopes, address_family,
    policy_coverage,
    CASE WHEN policy_coverage = 'none' THEN '' ELSE policy_relation_digest END,
    backend_attributes_digest,
    max(
        1,
        target_enforcement_generation,
        coalesce(confirmed_target_enforcement_generation, 0),
        coalesce((
            SELECT target_reconcile_state.target_enforcement_generation
            FROM target_reconcile_state
            WHERE target_reconcile_state.node_id = enforcement_states.node_id
              AND target_reconcile_state.canonical_target = enforcement_states.canonical_target
        ), 0),
        coalesce((
            SELECT max(reconcile_probe_requirements.target_enforcement_generation)
            FROM reconcile_probe_requirements
            WHERE reconcile_probe_requirements.node_id = enforcement_states.node_id
              AND reconcile_probe_requirements.domain = 'target'
              AND reconcile_probe_requirements.canonical_target = enforcement_states.canonical_target
        ), 0)
    ),
    CASE WHEN confirmed_target_enforcement_generation = 0 THEN NULL
         ELSE confirmed_target_enforcement_generation END,
    CASE WHEN confirmed_snapshot_revision = 0 THEN NULL ELSE confirmed_snapshot_revision END,
    observed_at_us
FROM enforcement_states;

DROP TABLE enforcement_states;
ALTER TABLE enforcement_states_v4 RENAME TO enforcement_states;

UPDATE target_reconcile_state
SET target_enforcement_generation = (
    SELECT enforcement_states.target_enforcement_generation
    FROM enforcement_states
    WHERE enforcement_states.node_id = target_reconcile_state.node_id
      AND enforcement_states.canonical_target = target_reconcile_state.canonical_target
)
WHERE EXISTS (
    SELECT 1
    FROM enforcement_states
    WHERE enforcement_states.node_id = target_reconcile_state.node_id
      AND enforcement_states.canonical_target = target_reconcile_state.canonical_target
)
AND target_enforcement_generation < (
    SELECT enforcement_states.target_enforcement_generation
    FROM enforcement_states
    WHERE enforcement_states.node_id = target_reconcile_state.node_id
      AND enforcement_states.canonical_target = target_reconcile_state.canonical_target
);
