-- spam_history_settings holds the retention bound for the spam_history table — how
-- many of the most recent scored verdicts to keep — in a single row, so an operator
-- can change it from the admin panel instead of editing a constant and rebuilding.
-- The MTA polls it and applies a change without a restart. Applied once by the
-- runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS spam_history_settings (
	id           TINYINT UNSIGNED NOT NULL,
	retain_count INT NOT NULL DEFAULT 10000,
	updated_at   BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
