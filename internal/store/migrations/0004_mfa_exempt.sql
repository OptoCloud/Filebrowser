-- Admins may exempt selected (non-admin) users from the two-factor
-- requirement that write access otherwise brings; an invite can carry the
-- exemption to the account it creates.
ALTER TABLE users ADD COLUMN mfa_exempt BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE invites ADD COLUMN mfa_exempt BOOLEAN NOT NULL DEFAULT false;
