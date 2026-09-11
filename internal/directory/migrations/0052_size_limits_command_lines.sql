-- Add the per-protocol command-line caps to size_limits. One line of a
-- connection-oriented protocol (a command, and a SASL continuation on the same
-- reader) is read before the client authenticates, so without a cap a client that
-- never sends a line terminator grows the daemon's memory without limit. The IMAP
-- figure is the generous one, because a long UID set or SEARCH key is legitimate;
-- SMTP keeps the 512 octets RFC 5321 4.5.3.1.4 gives a command line. Each default
-- matches the daemon's own built-in constant, so applying this migration changes no
-- deployment's behaviour.
-- Idempotent ALTERs (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner
-- and recorded in schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS imap_command_line_bytes BIGINT NOT NULL DEFAULT 65536;
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS pop3_command_line_bytes BIGINT NOT NULL DEFAULT 8192;
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS smtp_command_line_bytes BIGINT NOT NULL DEFAULT 512;
