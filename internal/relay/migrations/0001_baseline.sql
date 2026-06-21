-- Relay spool baseline: the durable outbound queue. A message row holds the raw
-- bytes once; a recipient row per external address carries that address's own
-- attempt count, next-eligible time, and last error. Statements use IF NOT
-- EXISTS so adopting an existing, unversioned spool is a no-op that simply
-- records version 1.

CREATE TABLE IF NOT EXISTS messages (
	id            INTEGER PRIMARY KEY,
	envelope_from TEXT    NOT NULL,
	body          BLOB    NOT NULL,
	enqueued_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS recipients (
	id           INTEGER PRIMARY KEY,
	message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	recipient    TEXT    NOT NULL,
	attempts     INTEGER NOT NULL DEFAULT 0,
	next_attempt INTEGER NOT NULL,
	last_error   TEXT    NOT NULL DEFAULT ''
);

-- The recipients-by-due index backs the worker's claim scan.
CREATE INDEX IF NOT EXISTS recipients_due ON recipients(next_attempt);
