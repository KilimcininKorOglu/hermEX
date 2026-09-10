-- conn_limit_settings holds the concurrent-connection cap's on/off toggle and its
-- tunables: how many connections one daemon serves at once, and how many one client
-- address may hold. It is separate from http_rate_limit_settings, which counts HTTP
-- requests per window: this one bounds concurrency on the connection-oriented
-- protocols (IMAP, POP3, SMTP) and an operator tunes it independently. Every such
-- daemon polls this row and applies a change without a restart; the cap is off by
-- default, and the values are sized so a desktop client's several connections and a
-- whole office behind one NAT address both fit. Applied once by the runner and
-- recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS conn_limit_settings (
	id             TINYINT UNSIGNED NOT NULL,
	enabled        TINYINT(1) NOT NULL DEFAULT 0,
	max_total      INT NOT NULL DEFAULT 1000,
	max_per_client INT NOT NULL DEFAULT 20,
	updated_at     BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
