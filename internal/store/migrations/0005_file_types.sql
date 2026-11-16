-- The verified type of a file, taken from its content rather than its name
-- (see files.Verify). Keyed like the other per-file caches: by path, valid
-- while size and mtime are unchanged.
CREATE TABLE file_types (
    path       TEXT PRIMARY KEY,
    size       BIGINT NOT NULL,
    mtime      BIGINT NOT NULL,
    info_json  JSONB NOT NULL,
    sniffed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX file_types_age ON file_types (sniffed_at);
