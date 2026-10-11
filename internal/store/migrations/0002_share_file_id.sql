-- The identity (device and inode, or volume and file index) of the shared
-- file or folder, so a share stops working when something else later appears
-- at its path. Empty for shares created before this column, until first use.
ALTER TABLE shares ADD COLUMN file_id TEXT NOT NULL DEFAULT '';
