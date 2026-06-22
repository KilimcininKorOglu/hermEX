-- antispam_settings holds the single, operator-editable row of anti-spam tuning:
-- the signal weights, the spam threshold, and the comma-separated DNS blocklist
-- zones. The MTA seeds it from the built-in defaults on first run and reloads it
-- when updated_at advances, so an admin edit applies without a restart. The id is
-- pinned to 1 (a single row); updated_at is a millisecond version token, not a
-- display time. Applied once by the runner and recorded in schema_migrations.
CREATE TABLE IF NOT EXISTS antispam_settings (
	id           TINYINT UNSIGNED NOT NULL,
	spf_fail     INT NOT NULL,
	spf_softfail INT NOT NULL,
	dkim_fail    INT NOT NULL,
	dmarc_fail   INT NOT NULL,
	dnsbl_hit    INT NOT NULL,
	bayes_spam   INT NOT NULL,
	sa_rules_hit INT NOT NULL,
	threshold    INT NOT NULL,
	zones        VARCHAR(1024) NOT NULL DEFAULT '',
	updated_at   BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
