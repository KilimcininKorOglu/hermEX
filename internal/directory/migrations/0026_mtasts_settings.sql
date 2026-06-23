-- mtasts_settings selects whether and how the server PUBLISHES an MTA-STS policy
-- (RFC 8461) for its own domains, so a sending MTA can require validated TLS when
-- delivering to this server. It is the inbound counterpart to internal/mtasts, which
-- consumes other domains' policies on the outbound path. Publishing is off until the
-- operator opts in (an upgrade must not silently start advertising a policy). mode is
-- 'testing' by default: a sender surfaces TLS failures but still delivers, so a
-- certificate problem cannot lose inbound mail — switching to 'enforce' is a
-- deliberate, warned action. max_age is the policy cache lifetime in seconds a sender
-- honours; a short default limits the blast radius of a misconfiguration during
-- rollout. A single row mirrors the other settings tables; the absent row means
-- disabled/testing. Applied once by the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS mtasts_settings (
	id         TINYINT UNSIGNED NOT NULL,
	enabled    TINYINT(1) NOT NULL DEFAULT 0,
	mode       VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'testing',
	max_age    INT NOT NULL DEFAULT 86400,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
