// Package store is hermEX's per-mailbox object store. Its physical schema is
// original (not a copy of any reference layout), but it is MAPI-native: a
// mailbox is folders and messages, each carrying a property bag (proptag →
// value), because faithful Outlook/ROP fidelity requires properties to be
// retrievable without re-parsing MIME on every request.
//
// Storage rules:
//   - Scalar property values are stored as native SQLite types (INTEGER, REAL,
//     TEXT) so they stay SQL-queryable; only complex types (multivalue, GUID,
//     server EID, restriction, rule actions) are stored as ext-serialized blobs.
//   - The stored blob encoding is frozen as a storage-format contract
//     (storeExtFlags) independent of any wire encoding, so a wire change can
//     never alter bytes already on disk.
//   - IMAP needs are first-class: messages carry indexed internal_date, size,
//     and flags columns, and every folder owns a monotonic, never-reused UID
//     allocator plus a UIDVALIDITY.
//   - The raw RFC822 message is retained alongside the property bag in the early
//     phase; later slices decompose more of it into properties.
//
// Each mailbox is a single SQLite database, opened in WAL mode with foreign
// keys enforced per connection.
package store
