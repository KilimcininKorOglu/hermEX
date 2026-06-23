package webmail

import (
	"net/http"
	"net/url"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// sharedMailboxGroup is one shared mailbox the signed-in user may open, listed in
// the sidebar with the visible folders inside it. Addr is the mailbox address;
// AddrQS is its URL-escaped form for link building (every shared-scoped link
// carries ?mbox=AddrQS).
type sharedMailboxGroup struct {
	Addr    string
	AddrQS  string
	Folders []folderView
}

// callerMayOpenShared reports whether user may open the shared store at all: they
// are an additional store owner, hold a folder grant under their own address, or
// are a designated delegate. Ownership and delegate matches are case-folded; the
// folder-grant check follows the store's own exact-match convention (the same one
// every ResolvePermission caller uses). This is only the store-open gate;
// per-folder visibility and write rights are resolved separately per operation.
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

// openSharedFor validates that addr names a shared mailbox the session user may
// open and returns its store. The address is matched case-insensitively against
// the directory's SharedMailboxes() so the store path is always server-derived,
// never taken from the request: a forged mbox naming an arbitrary path can never
// reach a store. ok is false when shared mailboxes are unconfigured, addr is not
// a shared mailbox, the store cannot be opened, or the caller has no access. The
// caller owns the returned store and must Close it when ok. canon is the
// directory's canonical address spelling, used for compose-as and link building.
func (s *Server) openSharedFor(sess *session, addr string) (st *objectstore.Store, canon string, ok bool) {
	if s.Shared == nil {
		return nil, "", false
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, "", false
	}
	boxes, err := s.Shared.SharedMailboxes()
	if err != nil {
		return nil, "", false
	}
	want := strings.ToLower(addr)
	var path string
	for _, b := range boxes {
		if strings.ToLower(b.Address) == want {
			path = b.StorePath
			canon = b.Address
			break
		}
	}
	if path == "" {
		return nil, "", false
	}
	st, err = objectstore.Open(path)
	if err != nil {
		return nil, "", false
	}
	if !callerMayOpenShared(st, sess.user) {
		st.Close()
		return nil, "", false
	}
	return st, canon, true
}

// mboxParam returns the request's shared-mailbox selector (the mbox query
// parameter), trimmed. Empty means the request targets the caller's own mailbox.
// It is read from the URL query (never the form body), so it is uniform across
// GET reads and POST mutations and never disturbs multipart form parsing.
func mboxParam(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("mbox"))
}

// denyShared rejects a shared-mailbox-scoped request with 403, for an endpoint
// not yet wired to act on a shared mailbox. It reports whether it handled
// (rejected) the request. A mutating endpoint rejects outright rather than
// trusting the view to hide its controls, so a control left in a shared view — or
// a forged request — can never misfire against the caller's own store; the same
// guard on a not-yet-shared-aware read keeps mbox from being silently ignored.
func denyShared(w http.ResponseWriter, r *http.Request) bool {
	if mboxParam(r) != "" {
		http.Error(w, "shared mailbox is not available here", http.StatusForbidden)
		return true
	}
	return false
}

// listAccessibleSharedMailboxes returns the shared mailboxes the signed-in user
// may open, each with the folders they may see (FrightsVisible) and live counts —
// the data behind the mailbox sidebar's shared-mailboxes section. Returns nil when
// shared mailboxes are unconfigured. Each store is opened, access-checked, and
// closed in turn; a mailbox the caller cannot open, or in which they can see no
// folder, is omitted.
func (s *Server) listAccessibleSharedMailboxes(sess *session) []sharedMailboxGroup {
	if s.Shared == nil {
		return nil
	}
	boxes, err := s.Shared.SharedMailboxes()
	if err != nil {
		return nil
	}
	var out []sharedMailboxGroup
	for _, b := range boxes {
		st, err := objectstore.Open(b.StorePath)
		if err != nil {
			continue
		}
		if !callerMayOpenShared(st, sess.user) {
			st.Close()
			continue
		}
		folders, err := st.ListFolders()
		if err != nil {
			st.Close()
			continue
		}
		var vis []folderView
		for _, v := range buildFolderViews(folders) {
			rights, err := st.ResolvePermission(v.ID, sess.user)
			if err != nil || rights&mapi.FrightsVisible == 0 {
				continue
			}
			if total, unread, err := st.CountMessages(v.ID); err == nil {
				v.Total = total
				v.Unread = unread
			}
			vis = append(vis, v)
		}
		st.Close()
		if len(vis) == 0 {
			continue
		}
		out = append(out, sharedMailboxGroup{Addr: b.Address, AddrQS: url.QueryEscape(b.Address), Folders: vis})
	}
	return out
}
