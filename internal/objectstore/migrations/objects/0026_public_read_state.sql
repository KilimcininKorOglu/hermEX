-- Per-user read tracking for public-folder messages (objects.sqlite3). Public
-- folder flags are shared org-wide, so reading a public message must never write
-- \Seen back to the shared store; instead each user records, in their OWN store,
-- which public messages they have read. A row's presence means read; its absence
-- means unread. The key is the public store's owner (its domain) plus the public
-- message_id, a monotonic never-reused EID, so a purged message's row can never
-- be mistaken for a later message.

CREATE TABLE public_read_state (
	owner      TEXT NOT NULL,
	message_id INTEGER NOT NULL,
	PRIMARY KEY (owner, message_id)
);
