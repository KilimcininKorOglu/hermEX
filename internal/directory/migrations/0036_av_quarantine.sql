-- Antivirus quarantine: one row per message the ClamAV scanner held because it
-- matched a signature. The message is not delivered; its raw bytes are stored on
-- disk at config.QuarantinePath(id) and the recipient plus the domain/org admins
-- are notified. The admin panel lists these rows, scoped by domain_id (the
-- recipient domain for inbound, the sender domain for outbound). Idempotent.
CREATE TABLE IF NOT EXISTS av_quarantine (
	id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	direction     VARCHAR(16) NOT NULL,
	mail_from     VARCHAR(320) NOT NULL DEFAULT '',
	rcpt_to       TEXT NOT NULL,
	subject       VARCHAR(255) NOT NULL DEFAULT '',
	virus_name    VARCHAR(255) NOT NULL DEFAULT '',
	infected_file VARCHAR(255) NOT NULL DEFAULT '',
	domain_id     INT UNSIGNED NOT NULL DEFAULT 0,
	created_at    BIGINT NOT NULL,
	status        VARCHAR(16) NOT NULL DEFAULT 'held',
	PRIMARY KEY (id),
	KEY domain_created (domain_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
