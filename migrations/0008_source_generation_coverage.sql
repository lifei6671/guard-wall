-- NULL preserves unknown historical coverage; only a session-bound reader can
-- establish responsibility for the complete prefix beginning at byte zero.
ALTER TABLE source_file_generations ADD COLUMN durable_end_offset INTEGER
    CHECK (durable_end_offset IS NULL OR
        (durable_end_offset >= 0 AND (final_eof IS NULL OR durable_end_offset <= final_eof)));

ALTER TABLE source_file_generations ADD COLUMN coverage_session_id TEXT
    CHECK ((durable_end_offset IS NULL AND coverage_session_id IS NULL) OR
        (durable_end_offset IS NOT NULL AND coverage_session_id IS NOT NULL AND
         length(coverage_session_id) = 32 AND coverage_session_id NOT GLOB '*[^0-9a-f]*'));
