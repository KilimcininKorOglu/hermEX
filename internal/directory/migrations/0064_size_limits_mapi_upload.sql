-- Add the cap on one uploaded MAPI message stream to size_limits. An ICS upload
-- ([MS-OXCFXICS] RopFastTransferDestinationPutBuffer) sends a message in as many
-- chunks as the client chooses, and the server holds every byte until the message
-- is committed, so the per-request cap does not bound it. The default, 150 MiB,
-- matches the server's built-in constant, so applying this migration changes no
-- deployment's behaviour.
-- Idempotent ALTER (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the runner
-- and recorded in schema_migrations.
ALTER TABLE size_limits ADD COLUMN IF NOT EXISTS mapi_upload_message_bytes BIGINT NOT NULL DEFAULT 157286400;
