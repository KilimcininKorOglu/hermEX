-- app_passwords holds the per-client credentials a mail program uses in place of
-- the account password. IMAP, POP3, SMTP submission, ActiveSync, EWS, DAV and
-- MAPI have no way to ask for a second factor, so an account that enrolls one
-- would otherwise still be reachable with the password alone on every one of
-- them. Each program gets its own credential, named by the user, revocable on
-- its own without touching the others or the account password.
--
-- Only the hash is stored. The secret is generated rather than chosen, with
-- enough entropy that there is no dictionary to run against a fast hash, which
-- is why it is a plain digest and not the account password's slow one: a mail
-- client re-authenticates on every connection, and a 600k-round hash there would
-- cost the deployment far more than it buys against an unguessable secret.
CREATE TABLE IF NOT EXISTS app_passwords (
	id           BIGINT       NOT NULL AUTO_INCREMENT,
	user_id      INT UNSIGNED NOT NULL,
	name         VARCHAR(64)  NOT NULL,
	secret_hash  CHAR(64) CHARACTER SET ascii NOT NULL,
	created_at   BIGINT       NOT NULL,
	last_used_at BIGINT       NOT NULL DEFAULT 0,
	PRIMARY KEY (id),
	INDEX idx_app_passwords_user (user_id),
	CONSTRAINT app_passwords_user_fk FOREIGN KEY (user_id)
		REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
