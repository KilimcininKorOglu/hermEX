-- The Bayes spam-probability cutoff and the SpamAssassin summed-score threshold were
-- hardcoded constants in the scorer; this makes them operator-editable alongside the
-- other anti-spam settings so they can be tuned from the admin panel without a
-- rebuild. The defaults match the previous constants (0.95 and 5.0), so existing
-- rows keep their prior scoring behavior. The MTA hot-reloads the row within about a
-- minute, no restart. Idempotent ALTERs (MariaDB ADD COLUMN IF NOT EXISTS); applied
-- once by the runner and recorded in schema_migrations.
ALTER TABLE antispam_settings ADD COLUMN IF NOT EXISTS bayes_prob   DOUBLE NOT NULL DEFAULT 0.95;
ALTER TABLE antispam_settings ADD COLUMN IF NOT EXISTS sa_threshold DOUBLE NOT NULL DEFAULT 5.0;
