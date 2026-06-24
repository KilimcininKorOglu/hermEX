package webmail2api

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// mailboxCtx is the resolved store for a mail request: the caller's own mailbox,
// or a shared mailbox named by ?owner=.
type mailboxCtx struct {
	st     *objectstore.Store
	owner  string // canonical shared-mailbox address; "" for the own mailbox
	shared bool
	user   string // the authenticated caller's address
}

// openMailbox opens the caller's own mailbox, or a shared mailbox named by the
// ?owner= query param. The shared store path is ALWAYS server-derived from the
// directory's SharedMailboxes() (never from the request), and the open is gated,
// so a forged owner can never reach an arbitrary store (IDOR-safe). Writes the
// error response and returns false on failure.
func (s *Server) openMailbox(w http.ResponseWriter, r *http.Request) (*mailboxCtx, bool) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, false
	}
	want := strings.TrimSpace(r.URL.Query().Get("owner"))
	if want == "" || strings.EqualFold(want, c.Email) {
		st, err := objectstore.Open(c.Mailbox)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
			return nil, false
		}
		return &mailboxCtx{st: st, user: c.Email}, true
	}
	lister, ok := s.auth.(directory.SharedMailboxLister)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return nil, false
	}
	boxes, err := lister.SharedMailboxes()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "directory error"})
		return nil, false
	}
	for _, b := range boxes {
		if !strings.EqualFold(b.Address, want) {
			continue
		}
		st, err := objectstore.Open(b.StorePath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
			return nil, false
		}
		if !callerMayOpenShared(st, c.Email) {
			st.Close()
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return nil, false
		}
		return &mailboxCtx{st: st, owner: b.Address, shared: true, user: c.Email}, true
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	return nil, false
}

// callerMayOpenShared is the store-open gate: the caller is an additional store
// owner, holds a folder grant, or is a designated delegate. Per-folder read/write
// rights are still checked per operation.
func callerMayOpenShared(st *objectstore.Store, user string) bool {
	if owner, err := st.IsStoreOwner(user); err == nil && owner {
		return true
	}
	if granted, err := st.HasFolderGrant(user); err == nil && granted {
		return true
	}
	if dels, err := st.GetDelegates(); err == nil {
		for _, d := range dels {
			if strings.EqualFold(d, user) {
				return true
			}
		}
	}
	return false
}

// readAllowed gates a read on a shared folder via FrightsReadAny (a store owner is
// elevated by ResolvePermission). The own mailbox is always allowed.
func (mb *mailboxCtx) readAllowed(fid int64) bool {
	if !mb.shared {
		return true
	}
	rights, err := mb.st.ResolvePermission(fid, mb.user)
	return err == nil && rights&mapi.FrightsReadAny != 0
}

// writeAllowed gates a mutation on a shared folder via FrightsDeleteAny (a store
// owner is elevated by ResolvePermission). The own mailbox is always allowed.
func (mb *mailboxCtx) writeAllowed(fid int64) bool {
	if !mb.shared {
		return true
	}
	rights, err := mb.st.ResolvePermission(fid, mb.user)
	return err == nil && rights&mapi.FrightsDeleteAny != 0
}
