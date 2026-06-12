// Package oxcmail converts between internet mail (RFC 5322 / MIME) and the MAPI
// message object model (MS-OXCMAIL).
//
// Import parses a raw message into a Message: a top-level property bag, a
// recipient table, and an attachment table. Export renders a Message back into
// a wire-format message, synthesizing fresh headers, MIME structure, and
// transfer encodings — it does not reproduce the original bytes.
//
// The package never touches storage. Named properties are resolved through an
// injected resolver (see Options), so the caller's store owns the name-to-id
// mapping; oxcmail only decides which named properties a message needs.
package oxcmail
