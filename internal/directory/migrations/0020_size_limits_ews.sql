-- Add the EWS SOAP request-body cap to size_limits as the EWS daemon is wired to read
-- it. The default matches the previous constant (8388608 = 8 MiB). Idempotent ALTER
-- (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner and recorded in
-- schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS ews_request_bytes BIGINT NOT NULL DEFAULT 8388608;
