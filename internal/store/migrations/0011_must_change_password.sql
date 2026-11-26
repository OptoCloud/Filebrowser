-- Set when someone else chose the account's password (an administrator
-- creating the account): the user has to replace it before anything else.
ALTER TABLE users ADD COLUMN must_change_password BOOLEAN NOT NULL DEFAULT false;
