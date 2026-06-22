-- Per-user and per-domain spam-threshold overrides. A message's score is intrinsic
-- to the message, but the score at or above which it is filed to Junk can be
-- overridden for a single mailbox or a whole domain; NULL means inherit (the domain
-- override, then the global antispam_settings threshold). This mirrors the
-- ActiveSync sync_policy override columns. Idempotent ALTERs (MariaDB ADD COLUMN IF
-- NOT EXISTS); applied once by the runner and recorded in schema_migrations.
ALTER TABLE users ADD COLUMN IF NOT EXISTS spam_threshold INT DEFAULT NULL;
ALTER TABLE domains ADD COLUMN IF NOT EXISTS spam_threshold INT DEFAULT NULL;
