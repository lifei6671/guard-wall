CREATE TABLE reconcile_probe_requirements (
    node_id TEXT NOT NULL,
    domain TEXT NOT NULL CHECK (domain IN ('infrastructure', 'policy', 'target')),
    canonical_target TEXT NOT NULL,
    infrastructure_revision INTEGER NOT NULL CHECK (infrastructure_revision >= 0),
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= 0),
    target_enforcement_generation INTEGER NOT NULL CHECK (target_enforcement_generation >= 0),
    snapshot_revision INTEGER NOT NULL CHECK (snapshot_revision >= 0),
    fence_snapshot_revision INTEGER NOT NULL CHECK (fence_snapshot_revision IN (0, 1)),
    retry_epoch INTEGER NOT NULL CHECK (retry_epoch >= 0),
    attempt_count INTEGER NOT NULL CHECK (attempt_count BETWEEN 1 AND 6),
    recorded_at_us INTEGER NOT NULL CHECK (recorded_at_us > 0),
    PRIMARY KEY (
        node_id,
        domain,
        canonical_target,
        infrastructure_revision,
        policy_revision,
        target_enforcement_generation,
        snapshot_revision,
        fence_snapshot_revision,
        retry_epoch,
        attempt_count
    ),
    UNIQUE (
        node_id,
        domain,
        canonical_target,
        infrastructure_revision,
        policy_revision,
        target_enforcement_generation,
        snapshot_revision,
        fence_snapshot_revision
    ),
    CHECK (
        (domain = 'infrastructure'
            AND canonical_target = ''
            AND infrastructure_revision > 0
            AND policy_revision = 0
            AND target_enforcement_generation = 0)
        OR (domain = 'policy'
            AND canonical_target = ''
            AND infrastructure_revision = 0
            AND policy_revision > 0
            AND target_enforcement_generation = 0
            AND snapshot_revision = 0
            AND fence_snapshot_revision = 0)
        OR (domain = 'target'
            AND length(canonical_target) BETWEEN 3 AND 64
            AND infrastructure_revision = 0
            AND policy_revision = 0
            AND target_enforcement_generation > 0
            AND snapshot_revision = 0
            AND fence_snapshot_revision = 0)
    ),
    CHECK (
        (fence_snapshot_revision = 0 AND snapshot_revision = 0)
        OR (fence_snapshot_revision = 1 AND snapshot_revision > 0)
    ),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;
