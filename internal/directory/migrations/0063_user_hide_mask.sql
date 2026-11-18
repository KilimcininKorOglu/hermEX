-- Each user's address-book hide mask, as a number a GAL query can filter on
-- before its row limit. It is a copy: the PR_ATTR_HIDDEN properties in
-- user_properties stay the source, and the daemons compute this column from them
-- in Go (directory.HideMask), because the stored text ("3", "0x03" or the legacy
-- boolean form) is not something SQL parses the same way. The column therefore
-- starts at 0 and is filled when a daemon starts.
ALTER TABLE users ADD COLUMN IF NOT EXISTS hide_mask INT UNSIGNED NOT NULL DEFAULT 0;
