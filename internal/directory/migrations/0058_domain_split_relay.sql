-- Split domain: the host that serves the addresses of this domain that have no
-- mailbox here. Empty (the default) keeps refusing unknown local addresses; when
-- set, mail a local user or this server sends to such an address is relayed to
-- that host instead of MX. Idempotent ADD COLUMN.
ALTER TABLE domains ADD COLUMN IF NOT EXISTS split_relay_host VARCHAR(255) NOT NULL DEFAULT '';
