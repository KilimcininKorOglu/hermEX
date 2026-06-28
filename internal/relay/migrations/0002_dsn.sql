-- DSN (RFC 3461) parameters carried alongside each spooled delivery. RET and
-- ENVID are per-message; NOTIFY and ORCPT are per-recipient. Every column
-- defaults to the empty string, so an in-flight row from before this migration
-- backfills to "no DSN preference": a delivery failure still generates a bounce,
-- exactly the pre-DSN behavior. NOT NULL keeps the worker's claim scan able to
-- read NOTIFY into a plain string.

ALTER TABLE messages ADD COLUMN ret   TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN envid TEXT NOT NULL DEFAULT '';

ALTER TABLE recipients ADD COLUMN notify TEXT NOT NULL DEFAULT '';
ALTER TABLE recipients ADD COLUMN orcpt  TEXT NOT NULL DEFAULT '';
