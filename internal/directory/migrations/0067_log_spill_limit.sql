-- spill_limit_mb caps each daemon's log spill file, the local file a daemon
-- appends events to while the central log store is unreachable. Without a cap a
-- long outage grows it on the disk the mailboxes live on until delivery stops.
-- Every daemon polls this column and applies it without a restart; events past
-- the cap are counted and reported on stderr instead of written. 0 removes the
-- cap. The default, 1024 MiB per daemon, keeps hours of a busy daemon's events.
ALTER TABLE log_settings ADD COLUMN IF NOT EXISTS spill_limit_mb INT NOT NULL DEFAULT 1024;
