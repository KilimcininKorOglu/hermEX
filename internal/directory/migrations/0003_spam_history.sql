-- spam_history records one row per scored inbound message so the admin Spam
-- History page can show what was scored and why. The MTA writes it fail-open
-- (a delivery never fails because history could not be recorded) and bounds it to
-- a recent window by pruning on insert, so the table cannot grow unbounded.
-- reasons is the joined verdict reasons (the signals that fired). Applied once by
-- the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS spam_history (
	id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	ts          BIGINT NOT NULL,
	mail_from   VARCHAR(320) NOT NULL DEFAULT '',
	remote_addr VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
	score       INT NOT NULL DEFAULT 0,
	spam        TINYINT(1) NOT NULL DEFAULT 0,
	reasons     VARCHAR(512) NOT NULL DEFAULT '',
	PRIMARY KEY (id),
	KEY ts (ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
