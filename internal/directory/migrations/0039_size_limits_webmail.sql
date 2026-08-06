-- Add the webmail request-body cap to size_limits as the webmail daemon is wired to
-- read it. The default matches the daemon's built-in constant (41943040 = 40 MiB),
-- the largest legitimate body on that path (a base64 .eml import).
-- Idempotent ALTER (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner and
-- recorded in schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS webmail_request_bytes BIGINT NOT NULL DEFAULT 41943040;
