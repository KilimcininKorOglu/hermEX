-- login_lockout_settings holds the failed-login limiter's tuning in a single row:
-- how many failures inside the rolling window trip a lockout, how long that window
-- is, and how long the lockout lasts. Every daemon with a login chokepoint (IMAP,
-- POP3, webmail, the admin panel) polls this row and applies a change without a
-- restart, so a credential-stuffing wave can be answered by tightening the
-- threshold and a lockout storm hitting legitimate users by loosening it. The
-- defaults match the limiter's own built-in tuning, so an install that never opens
-- this page behaves exactly as before. Unlike the rate limiters this one has no
-- on/off toggle: an online brute-force guard is always on, and an operator who
-- needs it out of the way raises the threshold. Applied once by the runner and
-- recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS login_lockout_settings (
	id              TINYINT UNSIGNED NOT NULL,
	max_fails       INT NOT NULL DEFAULT 5,
	window_seconds  INT NOT NULL DEFAULT 900,
	lockout_seconds INT NOT NULL DEFAULT 900,
	updated_at      BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
