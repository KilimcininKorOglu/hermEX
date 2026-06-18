// Package rop implements the EMSMDB ROP layer ([MS-OXCROPS]) over the MAPI
// object store. It owns the per-session object/handle table and the per-ROP
// dispatch that the MAPI/HTTP Execute request drives. v1 targets the
// online-mode mail read core: this increment implements Logon and Release;
// folder/table browse, message read, and stream/attachment read follow.
package rop

import (
	"sync"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
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
	kindAttachWrite                  // a created attachment being filled (CreateAttachment → SaveChangesAttachment)
	kindEmbedded                     // a message encapsulated in an attachment, opened via OpenEmbeddedMessage
	kindSubscription                 // a RopRegisterNotification interest in mailbox events
)

// attachWrite is an attachment created on an open message and accumulating its
// properties before SaveChangesAttachment persists them. CreateAttachment inserts
// the (empty) row up front so the attach number can be assigned and returned, then
// SetProperties buffers the payload/filename into pending and SaveChangesAttachment
// flushes it to the stored row. messageID is the parent so the parent can be
// marked touched (its change number advances on the message's own save, not here).
//
// inMem is non-nil when the parent message is still being composed (not yet
// persisted): there is no store row to attach to, so the attachment is staged in
// the parent's newMessageState and written together when SaveChangesMessage calls
// CreateMessage. In that mode messageID/attachmentID are unset and
// SaveChangesAttachment merges pending into inMem.props rather than the store.
type attachWrite struct {
	messageID      int64
	attachmentID   int64
	attachNum      uint32
	pending        mapi.PropertyValues
	pendingDeletes []mapi.PropTag
	inMem          *newAttachment
}

// embeddedMessage is a message encapsulated in an attachment (a message/rfc822
// part, PR_ATTACH_METHOD = afEmbeddedMessage). hermEX stores the embedded message
// as the raw RFC822 bytes in the parent attachment's PR_ATTACH_DATA_BIN rather
// than as a recursive store row; OpenEmbeddedMessage imports those bytes into msg
// to serve reads, and a compose/edit exports msg back into the parent attachment.
//
// writeback is the parent created-attachment a composed (MAPI_CREATE) embedded
// message exports itself into on SaveChangesMessage: the export bytes, method, and
// MIME tag are buffered into the attachment's pending bag, which the client's
// SaveChangesAttachment then persists through the ordinary attachment write path.
// It is nil for a read-only embedded message (one opened over an existing
// attachment), which has no write-back target.
type embeddedMessage struct {
	msg       *oxcmail.Message
	writeback *attachWrite
}

// newAttachment is an attachment staged on a not-yet-persisted compose message.
// CreateAttachment assigns its attachNum (per-message MAX+1, mirroring the store)
// and stamps the opening properties; SaveChangesAttachment merges the client's
// filename/payload into props; SaveChangesMessage hands the whole set to
// CreateMessage, which writes the attachment rows.
type newAttachment struct {
	attachNum uint32
	props     mapi.PropertyValues
}

// object is a server-side MAPI object referenced by a uint32 handle. Fields are
// populated per kind: a logon holds the open mailbox store, a folder its
// objectstore id, a table its in-memory row snapshot and column set, a message
// its objectstore id, a stream its in-memory bytes and read cursor, an
// attachment its property bag.
type object struct {
	kind           objKind
	store          *objectstore.Store            // kindLogon, and inherited by every child object
	folderID       int64                         // kindFolder
	table          *tableState                   // kindTable
	messageID      int64                         // kindMessage
	pendingProps   mapi.PropertyValues           // kindMessage: in-place edits buffered until SaveChangesMessage
	pendingDeletes []mapi.PropTag                // kindMessage: property removals buffered until SaveChangesMessage
	touched        bool                          // kindMessage: an attachment add/delete dirtied the message (bump CN on save)
	attachW        *attachWrite                  // kindAttachWrite
	embedded       *embeddedMessage              // kindEmbedded
	stream         *streamState                  // kindStream
	attachProps    mapi.PropertyValues           // kindAttachment
	newMsg         *newMessageState              // kindNewMessage
	fastSrc        fastTransferSource            // kindSync: what GetBuffer drains
	stateSink      stateStreamSink               // kindSync: what the state-stream ROPs populate
	upload         *objectstore.UploadCollector  // kindSync (upload): the import target
	uploadMsg      *objectstore.UploadMessage    // kindUploadMessage: the message being imported
	msgCollector   *objectstore.MessageCollector // kindFastUpload: the body parser
	sub            subscription                  // kindSubscription: the registered notification interest
	subSnapshot    folderSnapshot                // kindSubscription (folder/message scope): the subscribed folder's poll baseline
	subFolders     map[int64]folderSnapshot      // kindSubscription (whole-store): a poll baseline per content folder, refreshed as folders come and go
}

// newMessageState accumulates a message being composed over the ROP write
// sequence: CreateMessage opens it, SetProperties merges into props,
// ModifyRecipients replaces recipients, CreateAttachment stages attachments, and
// SaveChangesMessage persists it (with its attachments) via
// objectstore.CreateMessage. It is in memory until the first save; savedID then
// holds the persisted message EID so a re-save updates in place rather than
// inserting a duplicate.
type newMessageState struct {
	folderID    int64
	props       mapi.PropertyValues
	recipients  []mapi.PropertyValues
	attachments []*newAttachment // staged before the first save (compose-with-attachment)
	saved       bool
	savedID     int64
}

// Session is one MAPI/HTTP session's object/handle table — the analogue of a
// per-logon object graph. It is created on Connect and closed on Disconnect.
//
// Execute requests are serialized by the MAPI/HTTP sequence cookie, but a
// NotificationWait long-poll runs on a parallel connection without that cookie,
// concurrently with an Execute. mu therefore guards the object table, the snapshots,
// and the pending queue: Dispatch, PollForChange, and Close each take it at their
// boundary, while the internal alloc/get/release/poll helpers assume the caller
// already holds it.
//
// accounts and owner are the submit context: the recipient directory the MTA
// bridge resolves against and the session owner's SMTP address (the From of a
// submitted message). They are nil/empty for a read-only session (the read-core
// tests), in which case RopSubmitMessage reports MAPI_E_NO_SUPPORT.
type Session struct {
	mu       sync.Mutex
	mailbox  string
	accounts directory.Accounts
	owner    string
	handles  map[uint32]*object
	next     uint32
	pending  []queuedNotify // notifications awaiting delivery in an Execute response (the notify drain)
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

// persistedMessageID returns the store message id behind a message object when it
// refers to a row that already exists — an opened message, or a compose message
// after its first save — so attachment writes can target the real message. It
// reports false for a compose message still in memory (no row yet) or a non-message
// object.
func persistedMessageID(o *object) (int64, bool) {
	switch o.kind {
	case kindMessage:
		return o.messageID, true
	case kindNewMessage:
		if o.newMsg != nil && o.newMsg.saved {
			return o.newMsg.savedID, true
		}
	}
	return 0, false
}

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

// Close releases every handle (Disconnect), closing any open store. It takes the
// lock because a parked NotificationWait may be reading the object table on a
// parallel connection when Disconnect lands.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h := range s.handles {
		s.release(h)
	}
}

// PollForChange runs one notification poll under the session lock and reports
// whether any notification is now queued for delivery. It is the wake signal a
// NotificationWait long-poll calls each iteration: a true result means the next
// Execute will drain RopNotify bytes, so the wait returns FLAG_NOTIFICATION_PENDING.
// The caller sleeps between calls outside the lock, so an Execute is never blocked
// for longer than one poll.
func (s *Session) PollForChange() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueChanges()
	return len(s.pending) > 0
}
