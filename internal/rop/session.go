// Package rop implements the EMSMDB ROP layer ([MS-OXCROPS]) over the MAPI
// object store. It owns the per-session object/handle table and the per-ROP
// dispatch that the MAPI/HTTP Execute request drives. v1 targets the
// online-mode mail read core: this increment implements Logon and Release;
// folder/table browse, message read, and stream/attachment read follow.
package rop

import (
	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// objKind classifies the server object behind a handle. The message + stream/
// attachment increments add further kinds.
type objKind uint8

const (
	kindLogon         objKind = iota // an open mailbox store (the logon root)
	kindFolder                       // an opened folder
	kindTable                        // a contents, hierarchy, or attachment table
	kindMessage                      // an opened message
	kindStream                       // an open property stream
	kindAttachment                   // an opened attachment
	kindNewMessage                   // a message being composed in memory (pre-save)
	kindSync                         // an ICS/FastTransfer sync context
	kindUploadMessage                // an ICS-imported message awaiting its body + save
	kindFastUpload                   // a FastTransfer destination feeding an upload message
)

// object is a server-side MAPI object referenced by a uint32 handle. Fields are
// populated per kind: a logon holds the open mailbox store, a folder its
// objectstore id, a table its in-memory row snapshot and column set, a message
// its objectstore id, a stream its in-memory bytes and read cursor, an
// attachment its property bag.
type object struct {
	kind         objKind
	store        *objectstore.Store            // kindLogon, and inherited by every child object
	folderID     int64                         // kindFolder
	table        *tableState                   // kindTable
	messageID    int64                         // kindMessage
	pendingProps mapi.PropertyValues           // kindMessage: in-place edits buffered until SaveChangesMessage
	stream       *streamState                  // kindStream
	attachProps  mapi.PropertyValues           // kindAttachment
	newMsg       *newMessageState              // kindNewMessage
	fastSrc      fastTransferSource            // kindSync: what GetBuffer drains
	stateSink    stateStreamSink               // kindSync: what the state-stream ROPs populate
	upload       *objectstore.UploadCollector  // kindSync (upload): the import target
	uploadMsg    *objectstore.UploadMessage    // kindUploadMessage: the message being imported
	msgCollector *objectstore.MessageCollector // kindFastUpload: the body parser
}

// newMessageState accumulates a message being composed over the ROP write
// sequence: CreateMessage opens it, SetProperties merges into props,
// ModifyRecipients replaces recipients, and SaveChangesMessage persists it via
// objectstore.CreateMessage. It is in memory until the first save; savedID then
// holds the persisted message EID so a re-save updates in place rather than
// inserting a duplicate.
type newMessageState struct {
	folderID   int64
	props      mapi.PropertyValues
	recipients []mapi.PropertyValues
	saved      bool
	savedID    int64
}

// Session is one MAPI/HTTP session's object/handle table — the analogue of a
// per-logon object graph. It is created on Connect and closed on Disconnect.
// Access is serialized by the MAPI/HTTP sequence cookie (exactly one Execute
// proceeds per session at a time), so the table carries no lock of its own.
//
// accounts and owner are the submit context: the recipient directory the MTA
// bridge resolves against and the session owner's SMTP address (the From of a
// submitted message). They are nil/empty for a read-only session (the read-core
// tests), in which case RopSubmitMessage reports MAPI_E_NO_SUPPORT.
type Session struct {
	mailbox  string
	accounts directory.Accounts
	owner    string
	handles  map[uint32]*object
	next     uint32
}

// NewSession builds an empty session bound to a mailbox maildir path. accounts
// and owner supply the submit context (see Session); pass nil/"" for a read-only
// session. The store is not opened until RopLogon.
func NewSession(mailbox string, accounts directory.Accounts, owner string) *Session {
	return &Session{mailbox: mailbox, accounts: accounts, owner: owner, handles: make(map[uint32]*object), next: 1}
}

// alloc registers an object under a fresh handle and returns the handle. Handles
// start at 1 so that 0 and the 0xFFFFFFFF null-handle sentinel are never minted.
func (s *Session) alloc(o *object) uint32 {
	h := s.next
	s.next++
	s.handles[h] = o
	return h
}

// get returns the object behind a handle, or nil when the handle is unknown
// (including the 0xFFFFFFFF null handle).
func (s *Session) get(h uint32) *object { return s.handles[h] }

// release frees a handle, closing the mailbox store if it was a logon root.
func (s *Session) release(h uint32) {
	o := s.handles[h]
	if o == nil {
		return
	}
	if o.kind == kindLogon && o.store != nil {
		_ = o.store.Close()
	}
	delete(s.handles, h)
}

// Close releases every handle (Disconnect), closing any open store.
func (s *Session) Close() {
	for h := range s.handles {
		s.release(h)
	}
}
