-- Uploads that go through the R2 buffer: the browser sends the file to a
-- bucket in parts, and the server then pulls it from there, places it and
-- deletes it from the bucket (see internal/uploads/r2.go).
CREATE TABLE r2_uploads (
    id          TEXT PRIMARY KEY,
    owner       TEXT NOT NULL,            -- "u:<user id>" or "s:<share id>": who may continue it
    user_id     BIGINT NOT NULL REFERENCES users ON DELETE CASCADE, -- whose quota it counts against
    dest_dir    TEXT NOT NULL,            -- destination folder, relative to files_root
    name        TEXT NOT NULL,
    size        BIGINT NOT NULL,
    part_size   BIGINT NOT NULL,
    object_key  TEXT NOT NULL,
    upload_id   TEXT NOT NULL,            -- the bucket's multipart upload
    -- uploading -> uploaded -> pulling -> done; or failed, expired
    state       TEXT NOT NULL DEFAULT 'uploading',
    parts       JSONB,                    -- the parts as completed, to check the pull against
    attempts    INTEGER NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT '',
    final_path  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX r2_uploads_state ON r2_uploads (state, updated_at);
