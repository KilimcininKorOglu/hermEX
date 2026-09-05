-- Add the webmail inline-preview cap to size_limits. A PDF preview renders through
-- an <object>, which the browser does not defer the way it defers a lazy <img>, so
-- every previewed PDF downloads in full the moment the message opens. This bounds
-- what a reader pays for on a metered or slow link; above it the preview waits for a
-- click. The default matches the daemon's built-in constant (2 MiB), so applying this
-- migration changes no deployment's behaviour.
-- Idempotent ALTER (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner and
-- recorded in schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS webmail_preview_max_bytes BIGINT NOT NULL DEFAULT 2097152;
