-- dkim_keys holds one outbound DKIM signing key per domain: the PEM private key the
-- MTA and webmail sign with, the selector, and the public TXT record value to publish.
-- A key is stored DISABLED — signing begins only after the operator publishes the DNS
-- record and explicitly enables it, so a freshly generated key never produces
-- DKIM=fail at receivers. The signer only ever reads an enabled key. Applied once by
-- the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS dkim_keys (
	domain      VARCHAR(255) CHARACTER SET ascii NOT NULL,
	selector    VARCHAR(63) CHARACTER SET ascii NOT NULL,
	private_key TEXT NOT NULL,
	public_txt  TEXT NOT NULL,
	enabled     TINYINT(1) NOT NULL DEFAULT 0,
	created_at  BIGINT NOT NULL,
	PRIMARY KEY (domain)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
