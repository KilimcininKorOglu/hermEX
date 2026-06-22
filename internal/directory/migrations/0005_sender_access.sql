-- sender_access holds the operator-managed allow/block rules that override spam
-- scoring at delivery: an allowlisted sender is rescued from score-based junking
-- (but a hard DMARC failure still wins), a blocklisted sender is always filed to
-- Junk. pattern is a lowercased email address or bare domain; it is UNIQUE so a
-- pattern carries exactly one action (re-adding it with the other action flips it).
-- The MTA loads these and hot-reloads on change. Applied once by the runner and
-- recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS sender_access (
	id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	pattern    VARCHAR(320) NOT NULL,
	action     VARCHAR(8) NOT NULL,
	created_at BIGINT NOT NULL,
	PRIMARY KEY (id),
	UNIQUE KEY pattern (pattern)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
