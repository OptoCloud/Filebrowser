-- API tokens: credentials a user makes for scripts and other software.
-- A token carries a part of its owner's access (paths within the owner's
-- grants, and what may be done there), never more, and nothing of the
-- account itself: it cannot open a session or change sign-in settings.
CREATE TABLE api_tokens (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   BYTEA NOT NULL UNIQUE,
    -- The first characters of the token, to tell tokens apart in lists.
    hint         TEXT NOT NULL,
    -- Paths relative to files_root; '' is everything the owner may access.
    paths        TEXT[] NOT NULL,
    -- Any of: read, upload, modify, share.
    caps         TEXT[] NOT NULL,
    -- CIDRs the token may be used from; empty means anywhere.
    allowed_ips  TEXT[] NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    last_used_ip TEXT NOT NULL DEFAULT '',
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX api_tokens_user ON api_tokens (user_id);
