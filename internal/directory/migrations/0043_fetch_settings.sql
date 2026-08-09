-- fetch_settings holds the fetch worker's source policy in a single row. Today that
-- is one decision: whether a configured POP3/IMAP source may resolve to a loopback,
-- link-local or private address. It defaults to off, so the worker refuses an
-- internal source unless a system administrator turns it on, which keeps a
-- domain-scoped admin from pointing a source at this deployment's own internal
-- services or using the poll to fingerprint the internal network. An install that
-- legitimately fetches from an on-premises server on a private range turns it on
-- from the panel, with no restart. Applied once by the runner and recorded in
-- schema_migrations.
CREATE TABLE IF NOT EXISTS fetch_settings (
	id                     TINYINT UNSIGNED NOT NULL,
	allow_internal_sources TINYINT(1) NOT NULL DEFAULT 0,
	updated_at             BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
