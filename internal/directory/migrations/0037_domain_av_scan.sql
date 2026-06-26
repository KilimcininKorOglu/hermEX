-- Per-tenant antivirus scanning toggles. av_scan_inbound gates scanning of mail
-- arriving for this domain's recipients; av_scan_outbound gates scanning of mail
-- this domain's users send. Both default off, so AV is opt-in per domain even
-- when a clamd is configured. Idempotent ADD COLUMN.
ALTER TABLE domains ADD COLUMN IF NOT EXISTS av_scan_inbound TINYINT(1) NOT NULL DEFAULT 0;
ALTER TABLE domains ADD COLUMN IF NOT EXISTS av_scan_outbound TINYINT(1) NOT NULL DEFAULT 0;
