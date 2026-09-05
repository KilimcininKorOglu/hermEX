-- autoreply_settings holds the prefix an out-of-office reply uses when the
-- mailbox stores no subject of its own. EWS and ActiveSync carry no subject
-- field at all, so an account that turns out of office on from Outlook or a
-- phone has an empty one, and every reply it sends would otherwise go out under
-- a fixed string compiled into the binary.
--
-- One row, id = 1, like every other operator-tunable settings table, so the
-- MTA's poll observes a change and applies it without a restart.
CREATE TABLE IF NOT EXISTS autoreply_settings (
	id             TINYINT      NOT NULL,
	subject_prefix VARCHAR(128) NOT NULL DEFAULT '',
	updated_at     BIGINT       NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
