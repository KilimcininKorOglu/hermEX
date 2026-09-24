-- Machine reports other mail servers send to a hosted domain's reporting address:
-- DMARC aggregate reports (RFC 7489), SMTP TLS reports (RFC 8460) and DMARC
-- failure reports (RFC 6591). The MTA stores a report after it delivers the
-- message to the postmaster mailbox, and the admin panel lists them, scoped by
-- domain_id like av_quarantine. A reporter resends a report it is not sure
-- arrived, so a report is unique per domain, reporter and report id. The child
-- rows go with their report, so the retention sweep deletes reports only.
CREATE TABLE IF NOT EXISTS dmarc_reports (
	id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	domain_id      INT UNSIGNED NOT NULL,
	org_name       VARCHAR(255) NOT NULL DEFAULT '',
	report_id      VARCHAR(255) NOT NULL DEFAULT '',
	reporter_email VARCHAR(320) NOT NULL DEFAULT '',
	date_begin     BIGINT NOT NULL,
	date_end       BIGINT NOT NULL,
	policy_p       VARCHAR(16) NOT NULL DEFAULT '',
	policy_sp      VARCHAR(16) NOT NULL DEFAULT '',
	policy_pct     VARCHAR(8) NOT NULL DEFAULT '',
	adkim          VARCHAR(8) NOT NULL DEFAULT '',
	aspf           VARCHAR(8) NOT NULL DEFAULT '',
	received_at    BIGINT NOT NULL,
	mail_from      VARCHAR(320) NOT NULL DEFAULT '',
	remote_addr    VARCHAR(64) NOT NULL DEFAULT '',
	PRIMARY KEY (id),
	UNIQUE KEY one_report (domain_id, org_name, report_id),
	KEY domain_begin (domain_id, date_begin),
	KEY received (received_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS dmarc_report_records (
	id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	dmarc_report_id BIGINT UNSIGNED NOT NULL,
	source_ip       VARCHAR(45) NOT NULL,
	msg_count       BIGINT NOT NULL,
	disposition     VARCHAR(16) NOT NULL DEFAULT '',
	dkim_eval       VARCHAR(16) NOT NULL DEFAULT '',
	spf_eval        VARCHAR(16) NOT NULL DEFAULT '',
	header_from     VARCHAR(255) NOT NULL DEFAULT '',
	envelope_from   VARCHAR(255) NOT NULL DEFAULT '',
	dkim_results    TEXT NOT NULL,
	spf_results     TEXT NOT NULL,
	PRIMARY KEY (id),
	KEY report (dmarc_report_id),
	CONSTRAINT dmarc_report_records_report FOREIGN KEY (dmarc_report_id)
		REFERENCES dmarc_reports (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tlsrpt_reports (
	id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	domain_id   INT UNSIGNED NOT NULL,
	org_name    VARCHAR(255) NOT NULL DEFAULT '',
	report_id   VARCHAR(255) NOT NULL DEFAULT '',
	contact     VARCHAR(320) NOT NULL DEFAULT '',
	date_begin  BIGINT NOT NULL,
	date_end    BIGINT NOT NULL,
	received_at BIGINT NOT NULL,
	mail_from   VARCHAR(320) NOT NULL DEFAULT '',
	remote_addr VARCHAR(64) NOT NULL DEFAULT '',
	PRIMARY KEY (id),
	UNIQUE KEY one_report (domain_id, org_name, report_id),
	KEY domain_begin (domain_id, date_begin),
	KEY received (received_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS tlsrpt_report_policies (
	id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	tlsrpt_report_id BIGINT UNSIGNED NOT NULL,
	policy_type      VARCHAR(32) NOT NULL DEFAULT '',
	policy_domain    VARCHAR(255) NOT NULL DEFAULT '',
	mx_host          VARCHAR(255) NOT NULL DEFAULT '',
	success_count    BIGINT NOT NULL,
	failure_count    BIGINT NOT NULL,
	failure_details  MEDIUMTEXT NOT NULL,
	PRIMARY KEY (id),
	KEY report (tlsrpt_report_id),
	CONSTRAINT tlsrpt_report_policies_report FOREIGN KEY (tlsrpt_report_id)
		REFERENCES tlsrpt_reports (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- A failure report carries the failed message's header block, which is personal
-- data, so it has its own shorter retention window in mail_report_settings.
CREATE TABLE IF NOT EXISTS dmarc_failure_reports (
	id                     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
	domain_id              INT UNSIGNED NOT NULL,
	received_at            BIGINT NOT NULL,
	arrival_date           BIGINT NOT NULL DEFAULT 0,
	source_ip              VARCHAR(64) NOT NULL DEFAULT '',
	auth_failure           VARCHAR(64) NOT NULL DEFAULT '',
	original_mail_from     VARCHAR(320) NOT NULL DEFAULT '',
	original_rcpt_to       TEXT NOT NULL,
	dkim_domain            VARCHAR(255) NOT NULL DEFAULT '',
	delivery_result        VARCHAR(64) NOT NULL DEFAULT '',
	authentication_results TEXT NOT NULL,
	original_headers       MEDIUMTEXT NOT NULL,
	mail_from              VARCHAR(320) NOT NULL DEFAULT '',
	remote_addr            VARCHAR(64) NOT NULL DEFAULT '',
	PRIMARY KEY (id),
	KEY domain_received (domain_id, received_at),
	KEY received (received_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- The retention windows in days, one row. The admin daemon's sweep reads it every
-- minute, so a change applies without a restart. 0 keeps reports forever.
CREATE TABLE IF NOT EXISTS mail_report_settings (
	id                       TINYINT UNSIGNED NOT NULL,
	aggregate_retention_days INT NOT NULL DEFAULT 180,
	failure_retention_days   INT NOT NULL DEFAULT 30,
	updated_at               BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
