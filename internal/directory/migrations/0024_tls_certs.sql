-- tls_certs holds operator-uploaded TLS serving certificates so the listeners can
-- present a certificate without a file on disk and pick up a replacement without a
-- restart: the serving daemons poll this table and rebuild their certificate
-- snapshot when it changes, so renewing a certificate applies live (TLS must
-- already be active — switching a plaintext listener to TLS still needs a restart).
-- name is the SNI host the certificate serves, '' being the default presented when
-- no host-specific certificate matches; a single uploaded certificate (the
-- on-premise case) lives in that default row. cert_pem is the full chain and
-- key_pem the private key — a secret never returned by the admin API or logged.
-- not_after is the leaf's expiry (unix ms) for display and expiry warnings;
-- updated_at is a millisecond version token the poll compares to detect a change.
-- Applied once by the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS tls_certs (
	name       VARCHAR(255) CHARACTER SET ascii NOT NULL,
	cert_pem   MEDIUMTEXT NOT NULL,
	key_pem    MEDIUMTEXT NOT NULL,
	not_after  BIGINT NOT NULL,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
