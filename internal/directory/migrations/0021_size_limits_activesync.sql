-- Add the ActiveSync WBXML request-body cap to size_limits as the ActiveSync daemon is
-- wired to read it. The default matches the previous constant (4194304 = 4 MiB).
-- Idempotent ALTER (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner and
-- recorded in schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS activesync_request_bytes BIGINT NOT NULL DEFAULT 4194304;
