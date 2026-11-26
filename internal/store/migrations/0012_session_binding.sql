-- A session is bound to the browser and the network it was started from;
-- a user for whom that is a nuisance (a phone moving between networks) can
-- turn it off for their account.
ALTER TABLE users ADD COLUMN session_binding BOOLEAN NOT NULL DEFAULT true;
