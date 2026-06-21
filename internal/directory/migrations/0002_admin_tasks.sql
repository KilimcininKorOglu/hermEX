-- admin_tasks is the async admin task queue: one row per long-running admin
-- operation (LDAP directory sync, domain purge) so the Task queue page can show
-- its progress. status is pending/running/done/failed; params carries the
-- operation's argument (an org or domain id); message holds the result or error.
-- This is the first migration past the baseline, applied once by the runner and
-- recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS admin_tasks (
	id         INT UNSIGNED NOT NULL AUTO_INCREMENT,
	task_type  VARCHAR(32) CHARACTER SET ascii NOT NULL,
	status     VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'pending',
	params     VARCHAR(255) NOT NULL DEFAULT '',
	message    VARCHAR(512) NOT NULL DEFAULT '',
	created_by VARCHAR(320) CHARACTER SET ascii NOT NULL DEFAULT '',
	created_at BIGINT NOT NULL,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (id),
	KEY status_created (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
