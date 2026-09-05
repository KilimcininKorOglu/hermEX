-- user_totp holds one account's time-based one-time password enrollment, the
-- second factor a webmail or admin-panel login must clear after the password.
-- A row exists from the moment enrollment starts; enabled stays 0 until the user
-- has proved they can produce a code, so an abandoned enrollment never locks the
-- account out.
--
-- last_step is the replay guard. A code is valid for its whole time step and the
-- accepted skew widens that window further, so the same code can be presented
-- twice inside it. A verification updates this column only when the step is
-- greater than the stored one, which makes a second use of one code fail in the
-- database rather than in whichever caller remembered to check.
CREATE TABLE IF NOT EXISTS user_totp (
	user_id    INT UNSIGNED NOT NULL,
	secret     VARCHAR(64) CHARACTER SET ascii NOT NULL,
	enabled    TINYINT      NOT NULL DEFAULT 0,
	last_step  BIGINT       NOT NULL DEFAULT 0,
	created_at BIGINT       NOT NULL,
	PRIMARY KEY (user_id),
	CONSTRAINT user_totp_user_fk FOREIGN KEY (user_id)
		REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- user_totp_recovery holds the single-use codes minted with the enrollment, for
-- the day the authenticator is gone. Only the hash is stored, so reading the
-- directory does not yield a way past the second factor. A used code keeps its
-- row with used_at set, so the user can be shown how many are left.
CREATE TABLE IF NOT EXISTS user_totp_recovery (
	id        BIGINT       NOT NULL AUTO_INCREMENT,
	user_id   INT UNSIGNED NOT NULL,
	code_hash CHAR(64) CHARACTER SET ascii NOT NULL,
	used_at   BIGINT       NOT NULL DEFAULT 0,
	PRIMARY KEY (id),
	INDEX idx_user_totp_recovery_user (user_id),
	CONSTRAINT user_totp_recovery_user_fk FOREIGN KEY (user_id)
		REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
