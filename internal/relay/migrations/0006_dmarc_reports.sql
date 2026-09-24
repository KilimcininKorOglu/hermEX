-- DMARC aggregate report (RFC 7489 §7.2) counters and dispatch bookkeeping. Each
-- inbound message whose From domain asks for aggregate reports bumps one counter
-- row, keyed by the UTC report day, the policy domain, and the identifiers and
-- results a report row carries; dkim_results and spf_results hold the raw
-- authentication results as JSON. The daily pass builds one report per (day,
-- domain) from these rows, and dmarc_reports_sent records that it was sent so a
-- later pass does not send it again. Old rows of both tables are pruned.

CREATE TABLE IF NOT EXISTS dmarc_counters (
	report_day    TEXT    NOT NULL,
	policy_domain TEXT    NOT NULL,
	source_ip     TEXT    NOT NULL,
	envelope_from TEXT    NOT NULL DEFAULT '',
	dkim_eval     TEXT    NOT NULL,
	spf_eval      TEXT    NOT NULL,
	disposition   TEXT    NOT NULL,
	dkim_results  TEXT    NOT NULL DEFAULT '[]',
	spf_results   TEXT    NOT NULL DEFAULT '[]',
	messages      INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (report_day, policy_domain, source_ip, envelope_from, dkim_eval, spf_eval, disposition, dkim_results, spf_results)
);

CREATE TABLE IF NOT EXISTS dmarc_reports_sent (
	report_day    TEXT    NOT NULL,
	policy_domain TEXT    NOT NULL,
	sent_at       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (report_day, policy_domain)
);
