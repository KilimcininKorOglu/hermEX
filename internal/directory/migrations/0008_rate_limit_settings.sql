-- rate_limit_settings holds the inbound per-IP rate limiter's on/off toggle and its
-- tunables (the message burst and the window length in seconds), in a single row
-- separate from the spam-scoring settings since rate limiting acts at MAIL FROM, not
-- in the scorer. The MTA polls it and applies a change without a restart; rate
-- limiting is off by default. Applied once by the runner and recorded in
-- schema_migrations.
CREATE TABLE IF NOT EXISTS rate_limit_settings (
	id             TINYINT UNSIGNED NOT NULL,
	enabled        TINYINT(1) NOT NULL DEFAULT 0,
	burst          INT NOT NULL DEFAULT 60,
	window_seconds INT NOT NULL DEFAULT 60,
	updated_at     BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
