-- Add the MAPI/HTTP request-body cap to size_limits as the MAPI/HTTP daemon is wired
-- to read it. The default matches the daemon's built-in constant (33554432 = 32 MiB),
-- so applying this migration changes no deployment's behaviour.
-- Idempotent ALTER (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner and
-- recorded in schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS mapi_request_bytes BIGINT NOT NULL DEFAULT 33554432;
