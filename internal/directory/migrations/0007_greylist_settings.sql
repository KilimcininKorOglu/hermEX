-- greylist_settings holds the single on/off toggle for greylisting (a separate row
-- from the spam-scoring settings, since greylisting acts at RCPT, not in the
-- scorer). The MTA polls it and applies a change without a restart; greylisting is
-- off by default. Applied once by the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS greylist_settings (
	id         TINYINT UNSIGNED NOT NULL,
	enabled    TINYINT(1) NOT NULL DEFAULT 0,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
