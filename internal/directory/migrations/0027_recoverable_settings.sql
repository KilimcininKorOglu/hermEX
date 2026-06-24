-- recoverable_settings holds the Recoverable Items retention window (in days) in a
-- single row, so an operator can set it from the admin panel instead of it being a
-- build-time constant. A sweep polls it and permanently purges soft-deleted items
-- older than the window without a restart. The default of 14 matches Exchange;
-- 0 disables auto-purge (items are kept until manually purged). Applied once by the
-- runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS recoverable_settings (
	id             TINYINT UNSIGNED NOT NULL,
	retention_days INT NOT NULL DEFAULT 14,
	updated_at     BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
