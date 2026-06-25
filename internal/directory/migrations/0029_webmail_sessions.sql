-- webmail_sessions records each webmail2 login as a server-side session so a user
-- can list their active sessions and revoke one - the stateless JWT alone cannot be
-- listed or revoked. The JWT carries a jti that keys a row here; an API request is
-- authorized only while its jti row exists and has not expired, so deleting the row
-- revokes that session on its next request. Rows are pruned by expiry.
CREATE TABLE IF NOT EXISTS webmail_sessions (
	jti          VARCHAR(64)  NOT NULL,
	email        VARCHAR(255) NOT NULL,
	device_type  VARCHAR(255) NOT NULL DEFAULT '',
	user_agent   VARCHAR(512) NOT NULL DEFAULT '',
	client_ip    VARCHAR(64)  NOT NULL DEFAULT '',
	created_at   BIGINT NOT NULL,
	last_active  BIGINT NOT NULL,
	expires_at   BIGINT NOT NULL,
	PRIMARY KEY (jti),
	INDEX idx_webmail_sessions_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
