// Package objectstore is hermEX's MAPI-object mailbox store. Each mailbox is a
// directory holding two SQLite databases and two content directories:
//
//   - objects.sqlite3 — the authoritative MAPI object store: folders, messages,
//     recipients, attachments, and generic (proptag, propval) property tables.
//   - imapindex.sqlite3 — the IMAP/POP3 index that owns UIDs and denormalizes
//     per-message flags and envelope fields for fast listing.
//   - cid/ — content files (SHA3-256 addressed, zstd compressed) holding large
//     property values such as PR_BODY, PR_HTML, and PR_ATTACH_DATA_BIN.
//   - eml/ — cached RFC822 wire forms served to IMAP/POP3.
//
// Property values are stored as native SQLite types for scalars and as
// length-prefixed blobs for complex MAPI types. Folder identifiers and change
// numbers are stored bare (without the replica id) and are wrapped only when
// emitted on the wire. The schema is grounded in the internal spec (a local,
// uncommitted design doc) and follows the MS-OX* property model.
package objectstore
