package store

// schemaVersion is the current on-disk schema revision. No production data
// exists yet, so the schema grows by additive migration; bump this when the
// DDL changes and add an upgrade step.
const schemaVersion = 2

// schemaDDL is the ordered set of statements that create an empty store. Every
// statement is idempotent (IF NOT EXISTS) so applying it to an existing store
// is a no-op.
var schemaDDL = []string{
	`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value
	) WITHOUT ROWID`,

	`CREATE TABLE IF NOT EXISTS folders (
		folder_id    INTEGER PRIMARY KEY,
		parent_id    INTEGER REFERENCES folders(folder_id) ON DELETE CASCADE,
		display_name TEXT NOT NULL,
		uid_validity INTEGER NOT NULL,
		next_uid     INTEGER NOT NULL DEFAULT 1,
		subscribed   INTEGER NOT NULL DEFAULT 1
	)`,
	`CREATE INDEX IF NOT EXISTS idx_folders_parent ON folders(parent_id)`,

	`CREATE TABLE IF NOT EXISTS folder_properties (
		folder_id INTEGER NOT NULL REFERENCES folders(folder_id) ON DELETE CASCADE,
		proptag   INTEGER NOT NULL,
		value,
		PRIMARY KEY (folder_id, proptag)
	) WITHOUT ROWID`,

	`CREATE TABLE IF NOT EXISTS messages (
		message_id    INTEGER PRIMARY KEY,
		folder_id     INTEGER NOT NULL REFERENCES folders(folder_id) ON DELETE CASCADE,
		imap_uid      INTEGER NOT NULL,
		internal_date INTEGER NOT NULL,
		rfc822_size   INTEGER NOT NULL,
		flags         INTEGER NOT NULL DEFAULT 0,
		mime          BLOB,
		UNIQUE (folder_id, imap_uid)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_folder ON messages(folder_id, imap_uid)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_date ON messages(folder_id, internal_date)`,

	`CREATE TABLE IF NOT EXISTS message_properties (
		message_id INTEGER NOT NULL REFERENCES messages(message_id) ON DELETE CASCADE,
		proptag    INTEGER NOT NULL,
		value,
		PRIMARY KEY (message_id, proptag)
	) WITHOUT ROWID`,
}
