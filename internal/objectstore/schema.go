package objectstore

// Schema versions for the two per-mailbox databases. Stored in the object
// store's configurations table (config_id = cfgSchemaVersion) and the IMAP
// index's PRAGMA user_version. A mismatch is refused (dev data is disposable; no
// migration ladder yet).
const (
	objectSchemaVersion = 25
	indexSchemaVersion  = 4
)

// configurations config_id rows: store-wide counters and metadata on the object
// store root, stored bare.
const (
	cfgMailboxGUID       = 1
	cfgCurrentEID        = 2
	cfgMaximumEID        = 3
	cfgLastChangeNumber  = 4
	cfgLastArticleNumber = 5
	cfgLastCID           = 6
	cfgSearchState       = 7
	cfgDefaultPermission = 8
	cfgAnonymousPerm     = 9
	cfgSchemaVersion     = 10
	cfgMappingSignature  = 11
)

// objectDDL is the authoritative MAPI object store schema: folders and messages
// with denormalized hot columns, generic (proptag, propval) property tables for
// every object kind (store/folder/message/recipient/attachment), change-number
// and EID allocation bookkeeping, named-property mapping, permissions, rules,
// search folders, and the receive-folder map. Identifiers and change numbers are
// stored bare. Each CREATE and its indexes are separate statements
// (database/sql runs one statement per Exec).
var objectDDL = []string{
	`CREATE TABLE configurations (
		config_id INTEGER PRIMARY KEY,
		config_value BLOB NOT NULL)`,

	`CREATE TABLE allocated_eids (
		range_begin INTEGER NOT NULL,
		range_end INTEGER NOT NULL,
		allocate_time INTEGER NOT NULL,
		is_system INTEGER DEFAULT NULL)`,
	`CREATE INDEX time_index ON allocated_eids(allocate_time)`,

	`CREATE TABLE named_properties (
		propid INTEGER PRIMARY KEY AUTOINCREMENT,
		name_string TEXT COLLATE NOCASE NOT NULL)`,
	`CREATE UNIQUE INDEX namedprop_unique ON named_properties(name_string)`,

	`CREATE TABLE store_properties (
		proptag INTEGER UNIQUE NOT NULL,
		propval BLOB NOT NULL)`,

	`CREATE TABLE folders (
		folder_id INTEGER PRIMARY KEY,
		parent_id INTEGER,
		change_number INTEGER UNIQUE NOT NULL,
		is_search INTEGER DEFAULT 0,
		search_flags INTEGER DEFAULT NULL,
		search_criteria BLOB DEFAULT NULL,
		cur_eid INTEGER NOT NULL,
		max_eid INTEGER NOT NULL,
		is_deleted INTEGER DEFAULT 0,
		FOREIGN KEY (parent_id) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX search_index10 ON folders(is_search)`,

	`CREATE TABLE folder_properties (
		folder_id INTEGER NOT NULL,
		proptag INTEGER NOT NULL,
		propval BLOB NOT NULL,
		FOREIGN KEY (folder_id) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX fid_properties_index3 ON folder_properties(folder_id)`,
	`CREATE UNIQUE INDEX folder_property_index3 ON folder_properties(folder_id, proptag)`,

	`CREATE TABLE permissions (
		member_id INTEGER PRIMARY KEY AUTOINCREMENT,
		folder_id INTEGER NOT NULL,
		username TEXT COLLATE NOCASE NOT NULL,
		permission INTEGER NOT NULL,
		FOREIGN KEY (folder_id) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX fid_permissions_index ON permissions(folder_id)`,
	`CREATE UNIQUE INDEX folder_username_index ON permissions(folder_id, username)`,
	`CREATE UNIQUE INDEX folder_username_index2 ON permissions(username, folder_id)`,

	`CREATE TABLE rules (
		rule_id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT COLLATE NOCASE,
		provider TEXT COLLATE NOCASE NOT NULL,
		sequence INTEGER NOT NULL,
		state INTEGER NOT NULL,
		level INTEGER,
		user_flags INTEGER,
		provider_data BLOB,
		condition BLOB NOT NULL,
		actions BLOB NOT NULL,
		folder_id INTEGER NOT NULL,
		FOREIGN KEY (folder_id) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX fid_rules_index on rules(folder_id)`,

	`CREATE TABLE messages (
		message_id INTEGER PRIMARY KEY,
		parent_fid INTEGER,
		parent_attid INTEGER,
		is_deleted INTEGER DEFAULT 0,
		is_associated INTEGER,
		change_number INTEGER UNIQUE NOT NULL,
		read_cn INTEGER UNIQUE DEFAULT NULL,
		read_state INTEGER DEFAULT 0,
		message_size INTEGER NOT NULL,
		group_id INTEGER DEFAULT NULL,
		timer_id INTEGER DEFAULT NULL,
		mid_string TEXT DEFAULT NULL,
		FOREIGN KEY (parent_fid) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE,
		FOREIGN KEY (parent_attid) REFERENCES attachments (attachment_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX pid_messages_index8 ON messages(parent_fid)`,
	`CREATE INDEX attid_messages_index8 ON messages(parent_attid)`,
	`CREATE INDEX assoc_index8 ON messages(is_associated)`,
	`CREATE INDEX parent_assoc_index8 ON messages(parent_fid, is_associated)`,
	`CREATE INDEX parent_read_assoc_index8 ON messages(parent_fid, read_state, is_associated)`,

	`CREATE TABLE message_properties (
		message_id INTEGER NOT NULL,
		proptag INTEGER NOT NULL,
		propval BLOB NOT NULL,
		FOREIGN KEY (message_id) REFERENCES messages (message_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX mid_properties_index4 ON message_properties(message_id)`,
	`CREATE UNIQUE INDEX message_property_index4 ON message_properties(message_id, proptag)`,

	`CREATE TABLE message_changes (
		message_id INTEGER NOT NULL,
		change_number INTEGER NOT NULL,
		indices BLOB NOT NULL,
		proptags BLOB NOT NULL,
		FOREIGN KEY (message_id) REFERENCES messages (message_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX mid_changes_index ON message_changes(message_id)`,

	`CREATE TABLE recipients (
		recipient_id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id INTEGER NOT NULL,
		FOREIGN KEY (message_id) REFERENCES messages (message_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX mid_recipients_index ON recipients(message_id)`,

	`CREATE TABLE recipients_properties (
		recipient_id INTEGER NOT NULL,
		proptag INTEGER NOT NULL,
		propval BLOB NOT NULL,
		FOREIGN KEY (recipient_id) REFERENCES recipients (recipient_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX rid_properties_index5 ON recipients_properties(recipient_id)`,
	`CREATE UNIQUE INDEX recipient_property_index5 ON recipients_properties(recipient_id, proptag)`,

	`CREATE TABLE attachments (
		attachment_id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id INTEGER NOT NULL,
		FOREIGN KEY (message_id) REFERENCES messages (message_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX mid_attachments_index ON attachments(message_id)`,

	`CREATE TABLE attachment_properties (
		attachment_id INTEGER NOT NULL,
		proptag INTEGER NOT NULL,
		propval BLOB NOT NULL,
		FOREIGN KEY (attachment_id) REFERENCES attachments (attachment_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX attid_properties_index6 ON attachment_properties(attachment_id)`,
	`CREATE UNIQUE INDEX attachment_property_index6 ON attachment_properties(attachment_id, proptag)`,

	`CREATE TABLE receive_table (
		class TEXT COLLATE NOCASE UNIQUE NOT NULL,
		folder_id INTEGER NOT NULL,
		modified_time INTEGER NOT NULL,
		FOREIGN KEY (folder_id) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX fid_receive_index ON receive_table(folder_id)`,

	`CREATE TABLE search_scopes (
		folder_id INTEGER NOT NULL,
		included_fid INTEGER NOT NULL,
		FOREIGN KEY (folder_id) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE,
		FOREIGN KEY (included_fid) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX fid_scope_index ON search_scopes(folder_id)`,
	`CREATE INDEX included_scope_index ON search_scopes(included_fid)`,

	`CREATE TABLE search_result (
		folder_id INTEGER NOT NULL,
		message_id INTEGER NOT NULL,
		FOREIGN KEY (folder_id) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE,
		FOREIGN KEY (message_id) REFERENCES messages (message_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX fid_result_index ON search_result(folder_id)`,
	`CREATE INDEX mid_result_index ON search_result(message_id)`,
	`CREATE UNIQUE INDEX search_message_index ON search_result(folder_id, message_id)`,

	`CREATE TABLE autoreply_ts (
		peer VARCHAR(320) PRIMARY KEY,
		ts INTEGER)`,

	`CREATE TABLE replguidmap (
		replid INTEGER PRIMARY KEY AUTOINCREMENT,
		replguid VARCHAR(40))`,
	`CREATE UNIQUE INDEX replguidmap_guid ON replguidmap(replguid)`,
	// Bump the autoincrement so the first allocated replid is 6 (1-5 reserved).
	`REPLACE INTO replguidmap (replid) VALUES (5)`,
	`DELETE FROM replguidmap WHERE replid=5`,

	`CREATE TABLE msgtime_index (
		folder_id INTEGER NOT NULL,
		message_id INTEGER NOT NULL,
		mtime INTEGER,
		rcvtime INTEGER,
		sndtime INTEGER,
		PRIMARY KEY (folder_id, message_id),
		FOREIGN KEY (folder_id) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE,
		FOREIGN KEY (message_id) REFERENCES messages (message_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE UNIQUE INDEX msgtime_mt_idx ON msgtime_index (folder_id, mtime, message_id)`,
	`CREATE UNIQUE INDEX msgtime_rt_idx ON msgtime_index (folder_id, rcvtime, message_id)`,
	`CREATE UNIQUE INDEX msgtime_st_idx ON msgtime_index (folder_id, sndtime, message_id)`,
}

// indexDDL is the IMAP/POP3 index schema: per-folder UID allocation
// (folders.uidnext) and a denormalized per-message row carrying the IMAP uid,
// flags, and envelope projections for fast listing and search.
var indexDDL = []string{
	`CREATE TABLE folders (
		folder_id INTEGER PRIMARY KEY,
		parent_fid INTEGER NOT NULL,
		commit_max INTEGER NOT NULL,
		name TEXT COLLATE NOCASE UNIQUE,
		uidnext INTEGER DEFAULT 0,
		uidvalidity INTEGER NOT NULL,
		unsub INTEGER DEFAULT 0,
		sort_field INTEGER DEFAULT 0)`,
	`CREATE INDEX parent_fid_index3 ON folders(parent_fid)`,

	`CREATE TABLE messages (
		message_id INTEGER PRIMARY KEY,
		folder_id INTEGER NOT NULL,
		mid_string TEXT NOT NULL UNIQUE,
		idx INTEGER DEFAULT NULL,
		mod_time INTEGER DEFAULT 0,
		uid INTEGER NOT NULL,
		unsent INTEGER DEFAULT 0,
		recent INTEGER DEFAULT 1,
		read INTEGER DEFAULT 0,
		flagged INTEGER DEFAULT 0,
		replied INTEGER DEFAULT 0,
		forwarded INTEGER DEFAULT 0,
		deleted INTEGER DEFAULT 0,
		subject TEXT NOT NULL,
		sender TEXT NOT NULL,
		rcpt TEXT NOT NULL,
		size INTEGER NOT NULL,
		ext TEXT DEFAULT NULL,
		received INTEGER NOT NULL,
		FOREIGN KEY (folder_id) REFERENCES folders (folder_id) ON DELETE CASCADE ON UPDATE CASCADE)`,
	`CREATE INDEX folder_id_index ON messages(folder_id)`,
	`CREATE INDEX fid_idx_index ON messages(folder_id, idx)`,
	`CREATE INDEX fid_recent_index ON messages(folder_id, recent)`,
	`CREATE INDEX fid_read_index ON messages(folder_id, read)`,
	`CREATE INDEX fid_received_index ON messages(folder_id, received)`,
	`CREATE INDEX fid_uid_index ON messages(folder_id, uid)`,
	`CREATE INDEX fid_flagged_index ON messages(folder_id, flagged)`,
	`CREATE INDEX fid_subject_index ON messages(folder_id, subject)`,
	`CREATE INDEX fid_from_index ON messages(folder_id, sender)`,
	`CREATE INDEX fid_rcpt_index ON messages(folder_id, rcpt)`,
	`CREATE INDEX fid_size_index ON messages(folder_id, size)`,

	`CREATE TABLE mapping (
		message_id INTEGER PRIMARY KEY,
		mid_string TEXT NOT NULL,
		flag_string TEXT)`,
}
