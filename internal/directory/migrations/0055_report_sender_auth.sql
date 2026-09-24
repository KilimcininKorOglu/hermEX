-- How a stored report reached this server, and whether the message that carried
-- it was DKIM-signed. A report is the sender's claim; these columns record what
-- this server could verify about that sender. via is "mail" for a report mailed
-- to a postmaster address and "https" for one posted to the TLS report endpoint.
-- sender_dkim is the DKIM result of the carrying message (pass, fail or none),
-- empty when the message was not checked, and sender_dkim_domains lists the d=
-- domains of the signatures that verified. The names carry the sender_ prefix
-- because dmarc_failure_reports.dkim_domain already names the reported message's
-- signer, not the report's. Rows stored before this migration came by mail and
-- were not checked. Idempotent ADD COLUMN (MariaDB ADD COLUMN IF NOT EXISTS).
ALTER TABLE dmarc_reports
	ADD COLUMN IF NOT EXISTS via                 VARCHAR(8) NOT NULL DEFAULT 'mail',
	ADD COLUMN IF NOT EXISTS sender_dkim         VARCHAR(8) NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS sender_dkim_domains VARCHAR(512) NOT NULL DEFAULT '';

ALTER TABLE tlsrpt_reports
	ADD COLUMN IF NOT EXISTS via                 VARCHAR(8) NOT NULL DEFAULT 'mail',
	ADD COLUMN IF NOT EXISTS sender_dkim         VARCHAR(8) NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS sender_dkim_domains VARCHAR(512) NOT NULL DEFAULT '';

ALTER TABLE dmarc_failure_reports
	ADD COLUMN IF NOT EXISTS via                 VARCHAR(8) NOT NULL DEFAULT 'mail',
	ADD COLUMN IF NOT EXISTS sender_dkim         VARCHAR(8) NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS sender_dkim_domains VARCHAR(512) NOT NULL DEFAULT '';
