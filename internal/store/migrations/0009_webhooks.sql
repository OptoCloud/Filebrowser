-- Webhooks: addresses a user has the server call when something happens to
-- their files, so that other software can react (import a document, send a
-- notification). Each call is signed with the hook's secret.
CREATE TABLE webhooks (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    name        TEXT NOT NULL,
    url         TEXT NOT NULL,
    secret      BYTEA NOT NULL,        -- AES-GCM encrypted
    -- Any of: file.uploaded, drop.received, share.created, share.accessed.
    events      TEXT[] NOT NULL,
    -- Only events at or below this path ('' is everywhere).
    path        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled    BOOLEAN NOT NULL DEFAULT false,
    last_at     TIMESTAMPTZ,
    last_status INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    -- Deliveries failed in a row; the hook is disabled when it never works.
    failures    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX webhooks_user ON webhooks (user_id);
