// Package oxvcard converts between vCard (RFC 6350) and the MAPI contact object
// model (IPM.Contact, MS-OXVCARD / MS-OXOCNTC).
//
// Import parses a vCard into an oxcmail.Message — a property bag plus, for a
// contact photo, one attachment — and Export renders such a message back into a
// vCard. Import accepts vCard 3.0 and 4.0 and rejects 2.1; Export always emits
// 4.0. It is the contact analogue of package oxcmail: the same Message type and
// the same store seam (CreateMessage / OpenMessage) carry a contact unchanged.
//
// The package never touches storage. The contact email slots, work address,
// file-as, instant-messaging address, and has-picture flag are named properties
// (PSETID_Address); they are resolved to store property ids through an injected
// resolver (see Options), exactly as package oxcmail resolves named properties.
package oxvcard
