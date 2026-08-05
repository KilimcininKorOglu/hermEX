-- http_rate_limit_settings holds the per-client HTTP request limiter's on/off toggle
-- and its tunables (the request burst and the window length in seconds), in a single
-- row separate from rate_limit_settings, which governs inbound SMTP: the two act on
-- different protocols and an operator tunes them independently. Every HTTP daemon
-- polls this row and applies a change without a restart; the limiter is off by
-- default, and the burst is sized to absorb an Outlook or ActiveSync initial-sync
-- burst rather than trip on it. Applied once by the runner and recorded in
-- schema_migrations.
CREATE TABLE IF NOT EXISTS http_rate_limit_settings (
	id             TINYINT UNSIGNED NOT NULL,
	enabled        TINYINT(1) NOT NULL DEFAULT 0,
	burst          INT NOT NULL DEFAULT 600,
	window_seconds INT NOT NULL DEFAULT 60,
	updated_at     BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
