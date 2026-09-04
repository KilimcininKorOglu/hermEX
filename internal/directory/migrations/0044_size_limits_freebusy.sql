-- Add the free/busy target cap to size_limits as the four availability surfaces are
-- wired to read it. Unlike its siblings this column is a count, not a byte size: it
-- bounds how many mailboxes one availability request may fan out to, because each
-- target costs a store open and a full calendar scan. The default matches the
-- daemons' built-in constant (100), so applying this migration changes no
-- deployment's behaviour.
-- Idempotent ALTER (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner and
-- recorded in schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS freebusy_max_targets BIGINT NOT NULL DEFAULT 100;
