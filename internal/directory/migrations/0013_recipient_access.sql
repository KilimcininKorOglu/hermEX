-- recipient_access holds a mailbox owner's personal allow/block rules, applied per
-- recipient at delivery after spam scoring: an allowlisted sender is rescued from
-- score-based junking (a hard DMARC failure still wins), a blocklisted sender is
-- filed to Junk. These rules narrow but never override the operator's server-wide
-- list: an operator block beats a user allow, while an operator allow can be narrowed
-- by a user block. pattern is a lowercased email address or bare domain, UNIQUE per
-- user so a pattern carries exactly one action (re-adding it with the other action
-- flips it). The row is owned by the user and cascades away when the user — or its
-- domain — is deleted, so a purged mailbox leaves no dangling rules. Applied once by
-- the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS recipient_access (
	id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	user_id    INT UNSIGNED NOT NULL,
	pattern    VARCHAR(320) NOT NULL,
	action     VARCHAR(8) NOT NULL,
	created_at BIGINT NOT NULL,
	PRIMARY KEY (id),
	UNIQUE KEY user_pattern (user_id, pattern),
	CONSTRAINT recipient_access_user_fk FOREIGN KEY (user_id)
		REFERENCES users (id) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
