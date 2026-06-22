-- size_limits holds the per-protocol request/body size caps that were hardcoded
-- constants in each protocol server, in a single row so an operator can tune them from
-- the admin panel without a rebuild. Each daemon polls the row and applies its own
-- column without a restart. This starts with the IMAP literal cap; further protocol
-- limits (EWS/ActiveSync request body, DAV iCal/vCard) are added as ADD COLUMN
-- migrations as each daemon is wired. The default matches the previous constant
-- (52428800 = 50 MiB). Applied once by the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS size_limits (
	id                 TINYINT UNSIGNED NOT NULL,
	imap_literal_bytes BIGINT NOT NULL DEFAULT 52428800,
	updated_at         BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
