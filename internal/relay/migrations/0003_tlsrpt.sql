-- TLS-RPT (RFC 8460) session counters. Each outbound delivery attempt that
-- negotiated (or was required to negotiate) TLS bumps one row, keyed by the UTC
-- report day, the recipient domain, the policy type in effect (tlsa/sts/
-- no-policy-found), the mail exchanger, and the result type. An empty result
-- type counts a successful session; a non-empty one is a failure type. The daily
-- aggregate report is built by summing these rows for one (day, domain).

CREATE TABLE IF NOT EXISTS tlsrpt_counters (
	report_day    TEXT    NOT NULL,
	policy_domain TEXT    NOT NULL,
	policy_type   TEXT    NOT NULL,
	mx_host       TEXT    NOT NULL,
	result_type   TEXT    NOT NULL DEFAULT '',
	sessions      INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (report_day, policy_domain, policy_type, mx_host, result_type)
);
