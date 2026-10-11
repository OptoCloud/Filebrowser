-- WebAuthn (security keys and passkeys). The user handle is random so that
-- passkeys do not carry the internal user id; it is assigned on first use.
ALTER TABLE users ADD COLUMN webauthn_handle BYTEA UNIQUE;

CREATE TABLE webauthn_credentials (
    id           BYTEA PRIMARY KEY,           -- credential id from the authenticator
    user_id      BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    name         TEXT NOT NULL,
    credential   JSONB NOT NULL,              -- webauthn.Credential: public key, flags, sign count
    passkey      BOOLEAN NOT NULL DEFAULT false, -- discoverable (usable without a username)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);
CREATE INDEX webauthn_credentials_user ON webauthn_credentials (user_id);
