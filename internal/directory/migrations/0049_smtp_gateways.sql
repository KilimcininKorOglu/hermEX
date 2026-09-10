-- smtp_gateways holds the outbound SMTP gateway (smart-host) configuration: the server
-- outgoing mail is handed to instead of resolving each recipient domain's MX. A network
-- that blocks direct port-25 delivery, or an organization that routes every outgoing
-- message through one relay, has no other way to send at all.
--
-- The domain column is the sending domain the row applies to, and the empty string is the
-- global default every domain inherits. A row for a domain overrides the global one for
-- mail whose envelope sender is in that domain, so one table answers both lookups and the
-- MTA reads the whole set in a single query on its settings poll.
--
-- password holds the AUTH password wrapped at rest by the directory's key wrapping (the
-- same treatment DKIM and TLS private keys get), so a database dump carries ciphertext.
-- encryption is one of starttls, tls or none. Applied once by the runner and recorded in
-- schema_migrations.
CREATE TABLE IF NOT EXISTS smtp_gateways (
	domain     VARCHAR(255) NOT NULL,
	enabled    TINYINT(1) NOT NULL DEFAULT 0,
	host       VARCHAR(255) NOT NULL DEFAULT '',
	port       INT NOT NULL DEFAULT 587,
	encryption VARCHAR(16) NOT NULL DEFAULT 'starttls',
	username   VARCHAR(255) NOT NULL DEFAULT '',
	password   TEXT NOT NULL,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (domain)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
