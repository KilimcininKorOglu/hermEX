-- tls_settings selects how the front door obtains its serving certificate. The
-- default 'manual' mode keeps the Phase-2 behaviour (operator-uploaded certs in
-- tls_certs, config-file fallback). 'acme' mode lets a hosted deployment have the
-- gateway obtain and renew a Let's Encrypt certificate per tenant domain
-- automatically (TLS-ALPN-01 at :443), mirroring each obtained cert into tls_certs
-- so the mail daemons serve it unchanged. A single row, mirroring the other
-- settings tables. acme_ca_url empty means the CertMagic default CA; set it to a
-- staging or test (pebble) directory in non-production. acme_agreed records that the
-- operator accepted the CA's terms of service (ACME requires it). Switching mode is
-- structural, so the gateway reads this at startup; the admin seeds nothing — the
-- absent row means 'manual'. Applied once by the runner and recorded in
-- schema_migrations.
CREATE TABLE IF NOT EXISTS tls_settings (
	id          TINYINT UNSIGNED NOT NULL,
	mode        VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'manual',
	acme_email  VARCHAR(255) NOT NULL DEFAULT '',
	acme_ca_url VARCHAR(255) NOT NULL DEFAULT '',
	acme_agreed TINYINT(1) NOT NULL DEFAULT 0,
	updated_at  BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
