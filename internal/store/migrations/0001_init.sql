CREATE TABLE users (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    quota_bytes   BIGINT NOT NULL DEFAULT 0,
    totp_secret   BYTEA,          -- AES-GCM encrypted
    totp_pending  BYTEA,          -- AES-GCM encrypted, during enrollment
    totp_last_step BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled      BOOLEAN NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX users_username_key ON users (lower(username));

CREATE TABLE oidc_identities (
    user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    issuer     TEXT NOT NULL,
    subject    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (issuer, subject)
);
CREATE INDEX oidc_identities_user ON oidc_identities (user_id);

CREATE TABLE sessions (
    id_hash      BYTEA PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    ip           TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT '',
    auth_method  TEXT NOT NULL,          -- password, oidc, invite
    mfa          BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE grants (
    user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    path       TEXT NOT NULL,            -- relative to files_root, '' = everything
    permission TEXT NOT NULL CHECK (permission IN ('read', 'write')),
    PRIMARY KEY (user_id, path)
);

CREATE TABLE shares (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash     BYTEA NOT NULL UNIQUE,
    owner_id       BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    path           TEXT NOT NULL,
    is_dir         BOOLEAN NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('download', 'upload')),
    password_hash  TEXT NOT NULL DEFAULT '',
    expires_at     TIMESTAMPTZ NOT NULL,
    max_downloads  INTEGER NOT NULL DEFAULT 0,
    download_count INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at     TIMESTAMPTZ
);
CREATE INDEX shares_owner ON shares (owner_id);

CREATE TABLE invites (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash BYTEA NOT NULL UNIQUE,
    role       TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    grant_path TEXT,
    grant_perm TEXT CHECK (grant_perm IN ('read', 'write')),
    created_by BIGINT REFERENCES users ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    used_by    BIGINT REFERENCES users ON DELETE SET NULL
);

CREATE TABLE trash (
    id            TEXT PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    original_path TEXT NOT NULL,
    trash_path    TEXT NOT NULL,
    is_dir        BOOLEAN NOT NULL,
    size          BIGINT NOT NULL DEFAULT 0,
    trashed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX trash_user ON trash (user_id, trashed_at);

CREATE TABLE media_probe (
    path       TEXT PRIMARY KEY,
    size       BIGINT NOT NULL,
    mtime      BIGINT NOT NULL,
    probe_json JSONB NOT NULL,
    probed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE media_cache (
    source_path  TEXT NOT NULL,
    source_size  BIGINT NOT NULL,
    source_mtime BIGINT NOT NULL,
    variant      TEXT NOT NULL,
    cache_path   TEXT NOT NULL,
    bytes        BIGINT NOT NULL,
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_path, variant)
);
CREATE INDEX media_cache_lru ON media_cache (last_used_at);

CREATE TABLE thumbnails (
    path       TEXT PRIMARY KEY,
    size       BIGINT NOT NULL,
    mtime      BIGINT NOT NULL,
    thumb_path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ts           TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id      BIGINT REFERENCES users ON DELETE SET NULL,
    ip           TEXT NOT NULL DEFAULT '',
    action       TEXT NOT NULL,
    target       TEXT NOT NULL DEFAULT '',
    details_json JSONB
);
CREATE INDEX audit_log_ts ON audit_log (ts DESC);
CREATE INDEX audit_log_action ON audit_log (action, ts DESC);
