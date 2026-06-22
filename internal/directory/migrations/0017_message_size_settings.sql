-- message_size_settings holds the inbound SMTP message size limit (in bytes, 0 = no
-- limit) in a single row, so an operator can set it from the admin panel instead of
-- it being a build-time constant. The MTA polls it and applies the change without a
-- restart, advertising it as the SMTP SIZE extension. The default of 0 preserves the
-- prior behavior (no limit). Applied once by the runner and recorded in
-- schema_migrations.
CREATE TABLE IF NOT EXISTS message_size_settings (
	id                TINYINT UNSIGNED NOT NULL,
	max_inbound_bytes BIGINT NOT NULL DEFAULT 0,
	updated_at        BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
