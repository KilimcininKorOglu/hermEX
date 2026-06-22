-- Add the CalDAV/CardDAV PUT body caps to size_limits as the DAV daemon is wired to
-- read them. The defaults match the previous constants (4194304 = 4 MiB each).
-- Idempotent ALTERs (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner and
-- recorded in schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS dav_ical_bytes  BIGINT NOT NULL DEFAULT 4194304;
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS dav_vcard_bytes BIGINT NOT NULL DEFAULT 4194304;
