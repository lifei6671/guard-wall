-- Guard Phase 1 M0 minimal SQLite schema.
-- Time values are UTC Unix microseconds. Canonical targets are masked CIDR text.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 128),
    checksum_sha256 TEXT NOT NULL CHECK (length(checksum_sha256) = 64),
    applied_at_us INTEGER NOT NULL CHECK (applied_at_us > 0)
) STRICT;

CREATE TABLE node_identity (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    node_id TEXT NOT NULL UNIQUE CHECK (
        length(node_id) = 32 AND node_id NOT GLOB '*[^0-9a-f]*'
    ),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0)
) STRICT;

CREATE TABLE sources (
    source_id TEXT PRIMARY KEY CHECK (length(source_id) BETWEEN 1 AND 128),
    node_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('file', 'journald')),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    config_revision INTEGER NOT NULL DEFAULT 1 CHECK (config_revision > 0),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us >= created_at_us),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE parsers (
    parser_id TEXT PRIMARY KEY CHECK (length(parser_id) BETWEEN 1 AND 128),
    active_version TEXT,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us >= created_at_us),
    FOREIGN KEY (parser_id, active_version)
        REFERENCES parser_versions(parser_id, version) DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE parser_versions (
    parser_id TEXT NOT NULL,
    version TEXT NOT NULL CHECK (length(version) BETWEEN 1 AND 128),
    definition TEXT NOT NULL,
    definition_sha256 TEXT NOT NULL CHECK (length(definition_sha256) = 64),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    PRIMARY KEY (parser_id, version),
    FOREIGN KEY (parser_id) REFERENCES parsers(parser_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE rules (
    rule_id TEXT PRIMARY KEY CHECK (length(rule_id) BETWEEN 1 AND 128),
    active_version TEXT,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us >= created_at_us),
    FOREIGN KEY (rule_id, active_version)
        REFERENCES rule_versions(rule_id, version) DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE rule_versions (
    rule_id TEXT NOT NULL,
    version TEXT NOT NULL CHECK (length(version) BETWEEN 1 AND 128),
    definition TEXT NOT NULL,
    definition_sha256 TEXT NOT NULL CHECK (length(definition_sha256) = 64),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    PRIMARY KEY (rule_id, version),
    FOREIGN KEY (rule_id) REFERENCES rules(rule_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE allowlists (
    node_id TEXT NOT NULL,
    canonical_target TEXT NOT NULL CHECK (length(canonical_target) BETWEEN 3 AND 64),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us >= created_at_us),
    PRIMARY KEY (node_id, canonical_target),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE protected_targets (
    node_id TEXT NOT NULL,
    canonical_target TEXT NOT NULL CHECK (length(canonical_target) BETWEEN 3 AND 64),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    policy_revision INTEGER NOT NULL CHECK (policy_revision > 0),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us >= created_at_us),
    PRIMARY KEY (node_id, canonical_target),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE source_file_generations (
    source_id TEXT NOT NULL,
    generation TEXT NOT NULL CHECK (
        length(generation) = 32 AND generation NOT GLOB '*[^0-9a-f]*'
    ),
    device_id INTEGER NOT NULL CHECK (device_id >= 0),
    inode INTEGER NOT NULL CHECK (inode >= 0),
    path TEXT NOT NULL CHECK (length(path) BETWEEN 1 AND 4096),
    state TEXT NOT NULL CHECK (state IN ('open', 'draining', 'sealed', 'retired')),
    observed_size INTEGER NOT NULL DEFAULT 0 CHECK (observed_size >= 0),
    final_eof INTEGER CHECK (final_eof >= 0),
    max_delivery_sequence INTEGER CHECK (max_delivery_sequence >= 0),
    opened_at_us INTEGER NOT NULL CHECK (opened_at_us > 0),
    draining_at_us INTEGER,
    sealed_at_us INTEGER,
    retired_at_us INTEGER,
    PRIMARY KEY (source_id, generation),
    UNIQUE (generation),
    CHECK (draining_at_us IS NULL OR draining_at_us >= opened_at_us),
    CHECK (sealed_at_us IS NULL OR sealed_at_us >= COALESCE(draining_at_us, opened_at_us)),
    CHECK (retired_at_us IS NULL OR (sealed_at_us IS NOT NULL AND retired_at_us >= sealed_at_us)),
    CHECK (
        (state = 'open'
            AND draining_at_us IS NULL AND sealed_at_us IS NULL AND retired_at_us IS NULL
            AND final_eof IS NULL AND max_delivery_sequence IS NULL)
        OR
        (state = 'draining'
            AND draining_at_us IS NOT NULL AND sealed_at_us IS NULL AND retired_at_us IS NULL
            AND final_eof IS NULL AND max_delivery_sequence IS NULL)
        OR
        (state = 'sealed'
            AND sealed_at_us IS NOT NULL AND retired_at_us IS NULL
            AND final_eof IS NOT NULL AND max_delivery_sequence IS NOT NULL)
        OR
        (state = 'retired'
            AND sealed_at_us IS NOT NULL AND retired_at_us IS NOT NULL
            AND final_eof IS NOT NULL AND max_delivery_sequence IS NOT NULL)
    ),
    FOREIGN KEY (source_id) REFERENCES sources(source_id) ON DELETE RESTRICT
) STRICT;

CREATE UNIQUE INDEX source_file_generations_one_open_uq
    ON source_file_generations(source_id)
    WHERE state = 'open';

CREATE TABLE source_checkpoints (
    source_id TEXT PRIMARY KEY,
    delivery_sequence INTEGER NOT NULL CHECK (delivery_sequence >= 0),
    position_kind TEXT NOT NULL CHECK (position_kind IN ('file', 'journald')),
    generation TEXT,
    device_id INTEGER,
    inode INTEGER,
    start_offset INTEGER,
    end_offset INTEGER,
    journald_cursor TEXT,
    persisted_at_us INTEGER NOT NULL CHECK (persisted_at_us > 0),
    CHECK (
        (position_kind = 'file'
            AND generation IS NOT NULL AND device_id IS NOT NULL AND inode IS NOT NULL
            AND start_offset IS NOT NULL AND end_offset IS NOT NULL
            AND start_offset >= 0 AND end_offset >= start_offset
            AND journald_cursor IS NULL)
        OR
        (position_kind = 'journald'
            AND journald_cursor IS NOT NULL AND length(journald_cursor) > 0
            AND generation IS NULL AND device_id IS NULL AND inode IS NULL
            AND start_offset IS NULL AND end_offset IS NULL)
    ),
    FOREIGN KEY (source_id) REFERENCES sources(source_id) ON DELETE RESTRICT,
    FOREIGN KEY (source_id, generation)
        REFERENCES source_file_generations(source_id, generation) ON DELETE RESTRICT
) STRICT;

CREATE TABLE processing_receipts (
    delivery_id TEXT PRIMARY KEY CHECK (
        length(delivery_id) = 57
        AND substr(delivery_id, 1, 5) = 'dlv1_'
        AND substr(delivery_id, 6) NOT GLOB '*[^0-9a-v]*'
    ),
    source_id TEXT NOT NULL,
    position_kind TEXT NOT NULL CHECK (position_kind IN ('file', 'journald')),
    generation TEXT,
    device_id INTEGER,
    inode INTEGER,
    start_offset INTEGER,
    end_offset INTEGER,
    journald_cursor TEXT,
    kind TEXT NOT NULL CHECK (kind IN ('success', 'record_permanent')),
    failure_stage TEXT,
    failure_code TEXT,
    sanitized_error TEXT,
    terminal_action TEXT,
    failure_occurred_at_us INTEGER,
    committed_at_us INTEGER NOT NULL CHECK (committed_at_us > 0),
    CHECK (
        (position_kind = 'file'
            AND generation IS NOT NULL AND device_id IS NOT NULL AND inode IS NOT NULL
            AND start_offset IS NOT NULL AND end_offset IS NOT NULL
            AND start_offset >= 0 AND end_offset >= start_offset
            AND journald_cursor IS NULL)
        OR
        (position_kind = 'journald'
            AND journald_cursor IS NOT NULL AND length(journald_cursor) > 0
            AND generation IS NULL AND device_id IS NULL AND inode IS NULL
            AND start_offset IS NULL AND end_offset IS NULL)
    ),
    CHECK (
        (kind = 'success'
            AND failure_stage IS NULL AND failure_code IS NULL AND sanitized_error IS NULL
            AND terminal_action IS NULL AND failure_occurred_at_us IS NULL)
        OR
        (kind = 'record_permanent'
            AND failure_stage IS NOT NULL AND length(failure_stage) BETWEEN 1 AND 64
            AND failure_code IS NOT NULL AND length(failure_code) BETWEEN 1 AND 128
            AND sanitized_error IS NOT NULL AND length(sanitized_error) BETWEEN 1 AND 2048
            AND terminal_action IS NOT NULL AND length(terminal_action) BETWEEN 1 AND 64
            AND failure_occurred_at_us IS NOT NULL AND failure_occurred_at_us > 0)
    ),
    CHECK (failure_occurred_at_us IS NULL OR failure_occurred_at_us <= committed_at_us),
    FOREIGN KEY (source_id) REFERENCES sources(source_id) ON DELETE RESTRICT,
    FOREIGN KEY (source_id, generation)
        REFERENCES source_file_generations(source_id, generation) ON DELETE RESTRICT
) STRICT;

CREATE TRIGGER source_checkpoints_reject_retired_insert
BEFORE INSERT ON source_checkpoints
WHEN NEW.position_kind = 'file' AND EXISTS (
    SELECT 1 FROM source_file_generations
    WHERE source_id = NEW.source_id AND generation = NEW.generation AND state = 'retired'
)
BEGIN
    SELECT RAISE(ABORT, 'source checkpoint references retired generation');
END;

CREATE TRIGGER source_checkpoints_reject_retired_update
BEFORE UPDATE ON source_checkpoints
WHEN NEW.position_kind = 'file' AND EXISTS (
    SELECT 1 FROM source_file_generations
    WHERE source_id = NEW.source_id AND generation = NEW.generation AND state = 'retired'
)
BEGIN
    SELECT RAISE(ABORT, 'source checkpoint references retired generation');
END;

CREATE TRIGGER processing_receipts_reject_retired_insert
BEFORE INSERT ON processing_receipts
WHEN NEW.position_kind = 'file' AND EXISTS (
    SELECT 1 FROM source_file_generations
    WHERE source_id = NEW.source_id AND generation = NEW.generation AND state = 'retired'
)
BEGIN
    SELECT RAISE(ABORT, 'processing receipt references retired generation');
END;

CREATE TRIGGER processing_receipts_reject_retired_update
BEFORE UPDATE ON processing_receipts
WHEN NEW.position_kind = 'file' AND EXISTS (
    SELECT 1 FROM source_file_generations
    WHERE source_id = NEW.source_id AND generation = NEW.generation AND state = 'retired'
)
BEGIN
    SELECT RAISE(ABORT, 'processing receipt references retired generation');
END;

CREATE TABLE parser_terminal_outcomes (
    delivery_id TEXT NOT NULL CHECK (
        length(delivery_id) = 57
        AND substr(delivery_id, 1, 5) = 'dlv1_'
        AND substr(delivery_id, 6) NOT GLOB '*[^0-9a-v]*'
    ),
    parser_id TEXT NOT NULL,
    parser_version TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('success', 'no_match', 'record_permanent')),
    emitted_count INTEGER NOT NULL CHECK (emitted_count >= 0),
    failure_code TEXT CHECK (failure_code IS NULL OR length(failure_code) BETWEEN 1 AND 128),
    completed_at_us INTEGER NOT NULL CHECK (completed_at_us > 0),
    PRIMARY KEY (delivery_id, parser_id, parser_version),
    CHECK (
        (kind = 'success' AND emitted_count > 0 AND failure_code IS NULL)
        OR (kind = 'no_match' AND emitted_count = 0 AND failure_code IS NULL)
        OR (kind = 'record_permanent' AND emitted_count = 0 AND failure_code IS NOT NULL)
    ),
    FOREIGN KEY (parser_id, parser_version)
        REFERENCES parser_versions(parser_id, version) ON DELETE RESTRICT,
    FOREIGN KEY (delivery_id)
        REFERENCES processing_receipts(delivery_id) ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE detection_contributions (
    event_id TEXT NOT NULL CHECK (
        length(event_id) = 57
        AND substr(event_id, 1, 5) = 'evt1_'
        AND substr(event_id, 6) NOT GLOB '*[^0-9a-v]*'
    ),
    rule_id TEXT NOT NULL,
    rule_version TEXT NOT NULL,
    delivery_id TEXT NOT NULL CHECK (
        length(delivery_id) = 57
        AND substr(delivery_id, 1, 5) = 'dlv1_'
        AND substr(delivery_id, 6) NOT GLOB '*[^0-9a-v]*'
    ),
    contributed_at_us INTEGER NOT NULL CHECK (contributed_at_us > 0),
    PRIMARY KEY (event_id, rule_id, rule_version),
    FOREIGN KEY (rule_id, rule_version)
        REFERENCES rule_versions(rule_id, version) ON DELETE RESTRICT,
    FOREIGN KEY (delivery_id)
        REFERENCES processing_receipts(delivery_id) ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE alerts (
    alert_id TEXT PRIMARY KEY CHECK (length(alert_id) BETWEEN 1 AND 160),
    node_id TEXT NOT NULL,
    event_id TEXT NOT NULL CHECK (
        length(event_id) = 57
        AND substr(event_id, 1, 5) = 'evt1_'
        AND substr(event_id, 6) NOT GLOB '*[^0-9a-v]*'
    ),
    rule_id TEXT NOT NULL,
    rule_version TEXT NOT NULL,
    canonical_target TEXT NOT NULL CHECK (length(canonical_target) BETWEEN 3 AND 64),
    observed_at_us INTEGER NOT NULL CHECK (observed_at_us > 0),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    UNIQUE (event_id, rule_id, rule_version),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT,
    FOREIGN KEY (rule_id, rule_version)
        REFERENCES rule_versions(rule_id, version) ON DELETE RESTRICT,
    FOREIGN KEY (event_id, rule_id, rule_version)
        REFERENCES detection_contributions(event_id, rule_id, rule_version) ON DELETE RESTRICT
) STRICT;

CREATE TABLE decisions (
    decision_id TEXT PRIMARY KEY CHECK (length(decision_id) BETWEEN 1 AND 160),
    node_id TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('automatic', 'manual')),
    rule_id TEXT,
    rule_version TEXT,
    alert_id TEXT,
    canonical_target TEXT NOT NULL CHECK (length(canonical_target) BETWEEN 3 AND 64),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us >= created_at_us),
    last_triggered_at_us INTEGER NOT NULL CHECK (last_triggered_at_us >= created_at_us),
    expires_at_us INTEGER,
    ended_at_us INTEGER,
    state TEXT NOT NULL CHECK (state IN ('active', 'expired', 'revoked')),
    end_reason TEXT CHECK (end_reason IN (
        'expired', 'manual', 'manual_replace', 'rule_disabled', 'system_cleanup'
    )),
    suppressed_count INTEGER NOT NULL DEFAULT 0 CHECK (suppressed_count >= 0),
    CHECK (
        (source = 'automatic' AND rule_id IS NOT NULL AND rule_version IS NOT NULL)
        OR
        (source = 'manual' AND rule_id IS NULL AND rule_version IS NULL AND alert_id IS NULL)
    ),
    CHECK (
        (state = 'active' AND ended_at_us IS NULL AND end_reason IS NULL)
        OR
        (state = 'expired' AND ended_at_us IS NOT NULL AND end_reason = 'expired')
        OR
        (state = 'revoked' AND ended_at_us IS NOT NULL
            AND end_reason IN ('manual', 'manual_replace', 'rule_disabled', 'system_cleanup'))
    ),
    CHECK (end_reason <> 'manual_replace' OR source = 'manual'),
    CHECK (end_reason <> 'rule_disabled' OR source = 'automatic'),
    CHECK (expires_at_us IS NULL OR expires_at_us >= created_at_us),
    CHECK (ended_at_us IS NULL OR ended_at_us >= created_at_us),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT,
    FOREIGN KEY (rule_id, rule_version)
        REFERENCES rule_versions(rule_id, version) ON DELETE RESTRICT,
    FOREIGN KEY (alert_id) REFERENCES alerts(alert_id) ON DELETE RESTRICT
) STRICT;

CREATE UNIQUE INDEX decisions_active_automatic_uq
    ON decisions(node_id, rule_id, canonical_target)
    WHERE source = 'automatic' AND state = 'active';

CREATE UNIQUE INDEX decisions_active_manual_uq
    ON decisions(node_id, canonical_target)
    WHERE source = 'manual' AND state = 'active';

CREATE INDEX decisions_active_expiry_idx
    ON decisions(state, expires_at_us)
    WHERE state = 'active' AND expires_at_us IS NOT NULL;

CREATE TABLE desired_ban_projections (
    node_id TEXT NOT NULL,
    canonical_target TEXT NOT NULL CHECK (length(canonical_target) BETWEEN 3 AND 64),
    state TEXT NOT NULL CHECK (state IN ('absent', 'present')),
    active_count INTEGER NOT NULL CHECK (active_count >= 0),
    effective_until_us INTEGER,
    target_projection_revision INTEGER NOT NULL CHECK (target_projection_revision > 0),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us > 0),
    PRIMARY KEY (node_id, canonical_target),
    CHECK (
        (state = 'absent' AND active_count = 0 AND effective_until_us IS NULL)
        OR
        (state = 'present' AND active_count > 0)
    ),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE enforcement_states (
    node_id TEXT NOT NULL,
    canonical_target TEXT NOT NULL CHECK (length(canonical_target) BETWEEN 3 AND 64),
    desired_membership TEXT NOT NULL CHECK (desired_membership IN ('absent', 'present')),
    observed_membership TEXT NOT NULL CHECK (observed_membership IN ('unknown', 'absent', 'present')),
    effective_until_us INTEGER,
    timeout_mode TEXT NOT NULL CHECK (timeout_mode IN ('none', 'native')),
    scopes INTEGER NOT NULL CHECK (scopes BETWEEN 1 AND 3),
    address_family INTEGER NOT NULL CHECK (address_family IN (4, 6)),
    policy_coverage TEXT NOT NULL CHECK (policy_coverage IN ('none', 'partial', 'full')),
    policy_relation_digest TEXT NOT NULL CHECK (length(policy_relation_digest) = 64),
    backend_attributes_digest TEXT NOT NULL CHECK (length(backend_attributes_digest) = 64),
    target_enforcement_generation INTEGER NOT NULL CHECK (target_enforcement_generation >= 0),
    confirmed_target_enforcement_generation INTEGER,
    confirmed_snapshot_revision INTEGER,
    observed_at_us INTEGER,
    PRIMARY KEY (node_id, canonical_target),
    CHECK (confirmed_target_enforcement_generation IS NULL
        OR confirmed_target_enforcement_generation >= 0),
    CHECK (confirmed_snapshot_revision IS NULL OR confirmed_snapshot_revision >= 0),
    CHECK (observed_at_us IS NOT NULL OR observed_membership = 'unknown'),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE infrastructure_reconcile_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    infrastructure_revision INTEGER NOT NULL CHECK (infrastructure_revision >= 0),
    retry_epoch INTEGER NOT NULL CHECK (retry_epoch >= 0),
    status TEXT NOT NULL CHECK (status IN (
        'pending', 'applying', 'converged', 'retry_waiting', 'degraded'
    )),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_attempt_at_us INTEGER,
    next_attempt_at_us INTEGER,
    last_error_code TEXT CHECK (last_error_code IS NULL OR length(last_error_code) <= 128),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us > 0)
) STRICT;

CREATE TABLE policy_reconcile_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    policy_revision INTEGER NOT NULL CHECK (policy_revision >= 0),
    retry_epoch INTEGER NOT NULL CHECK (retry_epoch >= 0),
    status TEXT NOT NULL CHECK (status IN (
        'pending', 'applying', 'converged', 'retry_waiting', 'degraded'
    )),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_attempt_at_us INTEGER,
    next_attempt_at_us INTEGER,
    last_error_code TEXT CHECK (last_error_code IS NULL OR length(last_error_code) <= 128),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us > 0)
) STRICT;

CREATE TABLE target_reconcile_state (
    node_id TEXT NOT NULL,
    canonical_target TEXT NOT NULL CHECK (length(canonical_target) BETWEEN 3 AND 64),
    target_enforcement_generation INTEGER NOT NULL CHECK (target_enforcement_generation >= 0),
    retry_epoch INTEGER NOT NULL CHECK (retry_epoch >= 0),
    status TEXT NOT NULL CHECK (status IN (
        'pending', 'applying', 'converged', 'retry_waiting', 'degraded'
    )),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_attempt_at_us INTEGER,
    next_attempt_at_us INTEGER,
    last_error_code TEXT CHECK (last_error_code IS NULL OR length(last_error_code) <= 128),
    updated_at_us INTEGER NOT NULL CHECK (updated_at_us > 0),
    PRIMARY KEY (node_id, canonical_target),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE audit_logs (
    audit_id TEXT PRIMARY KEY CHECK (length(audit_id) BETWEEN 1 AND 160),
    idempotency_key TEXT NOT NULL UNIQUE CHECK (length(idempotency_key) BETWEEN 1 AND 256),
    node_id TEXT NOT NULL,
    category TEXT NOT NULL CHECK (length(category) BETWEEN 1 AND 64),
    action TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 128),
    result TEXT NOT NULL CHECK (result IN ('success', 'rejected', 'failure')),
    severity TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    critical INTEGER NOT NULL CHECK (critical IN (0, 1)),
    actor_type TEXT NOT NULL CHECK (actor_type IN ('system', 'administrator', 'source')),
    delivery_id TEXT,
    alert_id TEXT,
    decision_id TEXT,
    error_code TEXT CHECK (error_code IS NULL OR length(error_code) <= 128),
    details_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(details_json)),
    created_at_us INTEGER NOT NULL CHECK (created_at_us > 0),
    CHECK (
        delivery_id IS NULL OR (
            length(delivery_id) = 57
            AND substr(delivery_id, 1, 5) = 'dlv1_'
            AND substr(delivery_id, 6) NOT GLOB '*[^0-9a-v]*'
        )
    ),
    FOREIGN KEY (node_id) REFERENCES node_identity(node_id) ON DELETE RESTRICT,
    FOREIGN KEY (alert_id) REFERENCES alerts(alert_id) ON DELETE RESTRICT,
    FOREIGN KEY (decision_id) REFERENCES decisions(decision_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX audit_logs_created_idx ON audit_logs(created_at_us, audit_id);
CREATE INDEX audit_logs_delivery_idx ON audit_logs(delivery_id) WHERE delivery_id IS NOT NULL;
