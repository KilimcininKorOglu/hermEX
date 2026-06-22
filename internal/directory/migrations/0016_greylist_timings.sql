-- Greylist timings (in seconds) were hardcoded constants in the greylister; this adds
-- them to the existing greylist_settings row so an operator can tune them from the
-- admin panel without a rebuild. The defaults match the previous constants — 300 s
-- (5 min) minimum delay, 86400 s (24 h) unconfirmed TTL, 3110400 s (36 d) confirmed
-- TTL — so existing installs keep their prior behavior. The MTA hot-reloads the row
-- within about a minute, no restart. Idempotent ALTERs (MariaDB ADD COLUMN IF NOT
-- EXISTS); applied once by the runner and recorded in schema_migrations.
ALTER TABLE greylist_settings ADD COLUMN IF NOT EXISTS min_delay       INT NOT NULL DEFAULT 300;
ALTER TABLE greylist_settings ADD COLUMN IF NOT EXISTS unconfirmed_ttl INT NOT NULL DEFAULT 86400;
ALTER TABLE greylist_settings ADD COLUMN IF NOT EXISTS confirmed_ttl   INT NOT NULL DEFAULT 3110400;
