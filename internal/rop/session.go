// Package rop implements the EMSMDB ROP layer ([MS-OXCROPS]) over the MAPI
// object store. It owns the per-session object/handle table and the per-ROP
// dispatch that the MAPI/HTTP Execute request drives. v1 targets the
// online-mode mail read core: this increment implements Logon and Release;
// folder/table browse, message read, and stream/attachment read follow.
package rop

import "hermex/internal/objectstore"

// objKind classifies the server object behind a handle. The message + stream/
// attachment increments add further kinds.
type objKind uint8

const (
	kindLogon  objKind = iota // an open mailbox store (the logon root)
	kindFolder                // an opened folder
	kindTable                 // a contents or hierarchy table
)

// object is a server-side MAPI object referenced by a uint32 handle. Fields are
// populated per kind: a logon holds the open mailbox store, a folder its
// objectstore id, a table its in-memory row snapshot and column set.
type object struct {
	kind     objKind
	store    *objectstore.Store // kindLogon
	folderID int64              // kindFolder
	table    *tableState        // kindTable
}

// Session is one MAPI/HTTP session's object/handle table — the analogue of a
// per-logon object graph. It is created on Connect and closed on Disconnect.
// Access is serialized by the MAPI/HTTP sequence cookie (exactly one Execute
// proceeds per session at a time), so the table carries no lock of its own.
type Session struct {
	mailbox string
	handles map[uint32]*object
	next    uint32
}

// NewSession builds an empty session bound to a mailbox maildir path. The store
// is not opened until RopLogon.
func NewSession(mailbox string) *Session {
	return &Session{mailbox: mailbox, handles: make(map[uint32]*object), next: 1}
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
