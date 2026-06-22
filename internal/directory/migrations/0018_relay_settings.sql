-- relay_settings holds the outbound delivery retry policy: the base backoff before the
-- first retry (in seconds; it still doubles per attempt and is capped internally) and
-- the number of attempts before a recipient is abandoned. These were hardcoded
-- constants in the relay worker; storing them in a single row lets an operator tune
-- them from the admin panel without a rebuild. The defaults match the previous
-- constants (300 s, 10 attempts). The MTA polls it and applies a change without a
-- restart. Applied once by the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS relay_settings (
	id              TINYINT UNSIGNED NOT NULL,
	backoff_seconds INT NOT NULL DEFAULT 300,
	max_attempts    INT NOT NULL DEFAULT 10,
	updated_at      BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
