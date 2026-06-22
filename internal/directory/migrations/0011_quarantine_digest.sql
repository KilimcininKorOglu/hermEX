-- Quarantine digest: digest_settings is the single-row toggle and tunables for the
-- periodic per-user summary of quarantined (Junk) mail — whether it runs, how often,
-- and the externally-reachable base URL the release links point at. digest_state is
-- the per-mailbox watermark: the highest Junk IMAP UID already summarized, so each run
-- includes only newer messages and skips a mailbox with nothing new (no empty or
-- duplicate digests). Applied once by the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS digest_settings (
	id             TINYINT UNSIGNED NOT NULL,
	enabled        TINYINT(1) NOT NULL DEFAULT 0,
	interval_hours INT NOT NULL DEFAULT 24,
	base_url       VARCHAR(255) NOT NULL DEFAULT '',
	updated_at     BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS digest_state (
	maildir    VARCHAR(255) NOT NULL,
	last_uid   BIGINT UNSIGNED NOT NULL DEFAULT 0,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (maildir)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
