-- Add the TLS report endpoint's request cap to size_limits: the largest body, as
-- it arrives (gzipped or plain JSON), that an RFC 8460 HTTPS report POST may
-- carry. Anyone can post to the endpoint, so the body is bounded before it is
-- read; the parser separately bounds the decompressed document.
-- The default matches the MTA's own built-in constant, so applying this
-- migration changes no deployment's behaviour.
-- Idempotent ALTER (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner
-- and recorded in schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS tlsrpt_report_bytes BIGINT NOT NULL DEFAULT 8388608;
