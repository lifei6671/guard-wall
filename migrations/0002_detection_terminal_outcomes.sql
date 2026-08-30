CREATE TABLE detection_terminal_outcomes (
    delivery_id TEXT NOT NULL CHECK (
        length(delivery_id) = 57
        AND substr(delivery_id, 1, 5) = 'dlv1_'
        AND substr(delivery_id, 6) NOT GLOB '*[^0-9a-v]*'
    ),
    event_id TEXT NOT NULL CHECK (
        length(event_id) = 57
        AND substr(event_id, 1, 5) = 'evt1_'
        AND substr(event_id, 6) NOT GLOB '*[^0-9a-v]*'
    ),
    rule_id TEXT NOT NULL,
    rule_version TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('success', 'record_permanent')),
    failure_code TEXT CHECK (failure_code IS NULL OR length(failure_code) BETWEEN 1 AND 128),
    completed_at_us INTEGER NOT NULL CHECK (completed_at_us > 0),
    PRIMARY KEY (event_id, rule_id, rule_version),
    CHECK (
        (kind = 'success' AND failure_code IS NULL)
        OR (kind = 'record_permanent' AND failure_code IS NOT NULL)
    ),
    FOREIGN KEY (rule_id, rule_version)
        REFERENCES rule_versions(rule_id, version) ON DELETE RESTRICT,
    FOREIGN KEY (delivery_id)
        REFERENCES processing_receipts(delivery_id) ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE INDEX detection_terminal_outcomes_delivery_idx
    ON detection_terminal_outcomes(delivery_id);
