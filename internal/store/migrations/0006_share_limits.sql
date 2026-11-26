-- Limits on what a drop (upload) link accepts; 0, or no types, means the
-- server default. upload_bytes and upload_files count what the link has
-- accepted, uploads in progress included.
ALTER TABLE shares
    ADD COLUMN max_bytes      BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN max_files      INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN max_file_bytes BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN allowed_types  TEXT[]  NOT NULL DEFAULT '{}',
    ADD COLUMN single_session BOOLEAN NOT NULL DEFAULT false,
    -- Hash of the cookie of the one browser a single-session link admits.
    ADD COLUMN session_hash   BYTEA,
    ADD COLUMN upload_bytes   BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN upload_files   INTEGER NOT NULL DEFAULT 0;
