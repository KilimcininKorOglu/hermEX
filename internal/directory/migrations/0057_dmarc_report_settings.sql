-- Whether this server sends DMARC aggregate reports (RFC 7489 §7.2) to the
-- domains whose mail it receives, one row. It is off until an operator turns it
-- on, because a report discloses which addresses sent mail here. The MTA reads it
-- every minute, so a change applies without a restart.
CREATE TABLE IF NOT EXISTS dmarc_report_settings (
	id         TINYINT UNSIGNED NOT NULL,
	enabled    TINYINT(1) NOT NULL DEFAULT 0,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
