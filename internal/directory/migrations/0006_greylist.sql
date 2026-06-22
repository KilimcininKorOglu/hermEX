-- greylist_triplets backs greylisting: the first contact from an (ip_key, sender,
-- recipient) triplet is deferred with a temporary failure so a legitimate MTA
-- retries; the retry after a short delay is accepted and the triplet confirmed.
-- ip_key is the sender's masked network (a /24 for IPv4, /64 for IPv6) so a
-- provider that retries from a different IP in the same pool is not deferred again.
-- The MTA decides; this table just stores. It is bounded by pruning unconfirmed
-- triplets past their expiry and confirmed ones past their TTL. Applied once by the
-- runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS greylist_triplets (
	id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	ip_key     VARCHAR(64) CHARACTER SET ascii NOT NULL,
	sender     VARCHAR(320) NOT NULL,
	recipient  VARCHAR(320) NOT NULL,
	first_seen BIGINT NOT NULL,
	last_seen  BIGINT NOT NULL,
	confirmed  TINYINT(1) NOT NULL DEFAULT 0,
	PRIMARY KEY (id),
	UNIQUE KEY triplet (ip_key, sender, recipient),
	KEY confirmed_seen (confirmed, last_seen)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
