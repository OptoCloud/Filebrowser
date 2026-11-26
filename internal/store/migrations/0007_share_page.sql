-- How a share presents itself: a link name chosen by its owner in place of
-- the random token (guessable, so its secrecy is the owner's choice), a
-- title and a note shown on the page and in link previews, and for drop
-- links whether uploaders are asked for their name.
ALTER TABLE shares
    ADD COLUMN slug     TEXT    NOT NULL DEFAULT '',
    ADD COLUMN title    TEXT    NOT NULL DEFAULT '',
    ADD COLUMN note     TEXT    NOT NULL DEFAULT '',
    ADD COLUMN ask_name BOOLEAN NOT NULL DEFAULT false;
-- A name is free again once its share is revoked or deleted.
CREATE UNIQUE INDEX shares_slug ON shares (slug) WHERE slug <> '' AND revoked_at IS NULL;
