-- outbound_settings holds the outbound abuse limiter's on/off toggle and its
-- tunables (the external-recipient cap and the window length in seconds), in a single
-- row separate from the inbound rate-limit settings since it acts on authenticated
-- submission, not inbound intake. The MTA polls it and applies a change without a
-- restart; outbound limiting is off by default. Applied once by the runner and
-- recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS outbound_settings (
	id             TINYINT UNSIGNED NOT NULL,
	enabled        TINYINT(1) NOT NULL DEFAULT 0,
	recipient_cap  INT NOT NULL DEFAULT 500,
	window_seconds INT NOT NULL DEFAULT 3600,
	updated_at     BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
