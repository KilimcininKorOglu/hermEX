-- log_settings holds the central-log retention window (in days) that was a static
-- config value (log_retention_days) applied as a Mongo TTL index at daemon startup.
-- Moving it here lets an operator change retention from the admin panel without a
-- restart: the admin daemon polls this row and prunes the log store to match, so the
-- TTL index is gone and retention is enforced by deletion. A single row, mirroring the
-- other settings tables. retention_days 0 (or negative) means keep logs forever —
-- nothing is pruned, which is the safe default. The admin seeds this once from the
-- config value on first run, then this row is the source of truth. Applied once by the
-- runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS log_settings (
	id             TINYINT UNSIGNED NOT NULL,
	retention_days INT NOT NULL DEFAULT 0,
	updated_at     BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
