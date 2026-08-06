-- admin_sessions records each administration-panel login as a server-side session
-- so it can be revoked. The panel token alone is a self-signed JWT: it is valid
-- for its whole lifetime no matter what the operator does, so signing out or
-- changing a password left a captured cookie working until it expired. The token
-- carries a jti that keys a row here; a request is authorized only while its row
-- exists and has not expired, so deleting the row revokes that session on its next
-- request. Rows are pruned by expiry.
CREATE TABLE IF NOT EXISTS admin_sessions (
	jti        VARCHAR(64)  NOT NULL,
	login      VARCHAR(255) NOT NULL,
	created_at BIGINT NOT NULL,
	expires_at BIGINT NOT NULL,
	PRIMARY KEY (jti),
	INDEX idx_admin_sessions_login (login)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
