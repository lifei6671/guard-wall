-- Session identity scopes delivery_sequence without changing recovery positions.
ALTER TABLE sources ADD COLUMN active_session_id TEXT
    CHECK (active_session_id IS NULL OR
        (length(active_session_id) = 32 AND active_session_id NOT GLOB '*[^0-9a-f]*'));

ALTER TABLE source_checkpoints ADD COLUMN checkpoint_session_id TEXT
    CHECK (checkpoint_session_id IS NULL OR
        (length(checkpoint_session_id) = 32 AND checkpoint_session_id NOT GLOB '*[^0-9a-f]*'));
