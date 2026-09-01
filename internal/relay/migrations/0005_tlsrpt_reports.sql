-- TLS-RPT (RFC 8460) report dispatch bookkeeping. One row records that the daily
-- aggregate report for a (UTC report day, recipient policy domain) has been sent,
-- so the daily pass never re-sends the same report. The row is written once the
-- report has been delivered (or once discovery found the domain publishes no rua
-- endpoint, so there is nothing to send). Old rows are pruned alongside the
-- counters they summarize, bounding the table.

CREATE TABLE IF NOT EXISTS tlsrpt_reports_sent (
	report_day    TEXT    NOT NULL,
	policy_domain TEXT    NOT NULL,
	sent_at       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (report_day, policy_domain)
);
