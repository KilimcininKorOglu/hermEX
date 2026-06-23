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
	Addr      string
	AddrQS    string
	Folders   []folderView
	CanManage bool // the caller may create/rename/delete folders here (folder-management rights)
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
	path, canon, ok := s.sharedPathFor(addr)
	if !ok {
		return nil, "", false
	}
	st, err := objectstore.Open(path)
	if err != nil {
		return nil, "", false
	}
	if !callerMayOpenShared(st, sess.user) {
		st.Close()
		return nil, "", false
	}
	return st, canon, true
}

// sharedPathFor resolves a shared-mailbox address to its server-derived store
// path and canonical spelling, matched case-insensitively against the directory's
// SharedMailboxes(). ok is false when shared mailboxes are unconfigured or addr is
// not a shared mailbox. It performs no access check — callers that mutate must
// still gate on callerMayOpenShared / canSendAsShared.
func (s *Server) sharedPathFor(addr string) (path, canon string, ok bool) {
	if s.Shared == nil {
		return "", "", false
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", "", false
	}
	boxes, err := s.Shared.SharedMailboxes()
	if err != nil {
		return "", "", false
	}
	want := strings.ToLower(addr)
	for _, b := range boxes {
		if strings.ToLower(b.Address) == want {
			return b.StorePath, b.Address, true
		}
	}
	return "", "", false
}

// canSendAsShared reports whether user may send as the shared mailbox: they own it
// (additional store owner) or are a designated delegate. A mere folder grant
// (reviewer/editor) does not confer send-as — impersonating the mailbox is the
// owner/delegate privilege. Matches are case-folded against the session address.
func canSendAsShared(st *objectstore.Store, user string) bool {
	if owner, err := st.IsStoreOwner(user); err == nil && owner {
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

// withMbox appends the shared-mailbox selector to a webmail URL that already has a
// query string, leaving an own-mailbox URL (empty mbox) untouched.
func withMbox(u, mbox string) string {
	if mbox == "" {
		return u
	}
	return u + "&mbox=" + url.QueryEscape(mbox)
}

// hasFolderRight reports whether user holds all of the need bits on folder fid
// (the store owner clears any check, since ResolvePermission elevates them).
func hasFolderRight(st *objectstore.Store, user string, fid int64, need uint32) bool {
	rights, err := st.ResolvePermission(fid, user)
	return err == nil && rights&need == need
}

// sharedActionAllowed authorizes a single-message action against the caller's
// folder rights in a shared mailbox, following the MS-OXCPERM model: editing an
// item (read state, follow-up flag, categories) needs EditAny; deleting or moving
// one needs DeleteAny on the source plus Create on the destination well-known
// folder. A store owner clears every check (ResolvePermission elevates them to
// full rights). The conservative "Any" rights are required rather than the
// "Owned" variants, since item authorship is not tracked here, so an unsure case
// denies. An unknown op denies.
func sharedActionAllowed(st *objectstore.Store, user, op string, src int64, folders []objectstore.FolderInfo, r *http.Request) bool {
	can := func(fid int64, need uint32) bool { return hasFolderRight(st, user, fid, need) }
	switch op {
	case "toggleseen", "toggleflag", "flag", "flagcomplete", "flagnone", "categorize":
		return can(src, mapi.FrightsEditAny)
	case "delete":
		if src == int64(mapi.PrivateFIDDeletedItems) {
			return can(src, mapi.FrightsDeleteAny) // permanent delete from Deleted Items
		}
		return can(src, mapi.FrightsDeleteAny) && can(int64(mapi.PrivateFIDDeletedItems), mapi.FrightsCreate)
	case "junk":
		if src == int64(mapi.PrivateFIDJunk) {
			return true // already there: a no-op
		}
		return can(src, mapi.FrightsDeleteAny) && can(int64(mapi.PrivateFIDJunk), mapi.FrightsCreate)
	case "restore":
		return can(src, mapi.FrightsDeleteAny) && can(int64(mapi.PrivateFIDInbox), mapi.FrightsCreate)
	case "move":
		dst, ok := parseDst(r, folders)
		if !ok || dst == src {
			return true // the handler no-ops or rejects with 400; no rights needed to reach that
		}
		return can(src, mapi.FrightsDeleteAny) && can(dst, mapi.FrightsCreate)
	case "copy":
		dst, ok := parseDst(r, folders)
		if !ok || dst == src {
			return true
		}
		return can(dst, mapi.FrightsCreate)
	case "unschedule":
		return can(src, mapi.FrightsDeleteAny) && can(int64(mapi.PrivateFIDDraft), mapi.FrightsCreate)
	}
	return false
}

// mailboxRedirect is the list URL for a store: the own mailbox, or the open shared
// mailbox carrying its selector.
func mailboxRedirect(mbox string) string {
	if mbox == "" {
		return "/mail"
	}
	return "/mail?mbox=" + url.QueryEscape(mbox)
}

// sharedFolderAllowed authorizes a folder-management op in a shared mailbox:
// creating a top-level folder needs CreateSubfolder on the IPM subtree root;
// renaming or deleting one needs Owner (full control) on that folder. A store
// owner clears every check. A built-in target is left to the handler's own
// refusal. An unknown op denies.
func sharedFolderAllowed(st *objectstore.Store, user, op string, r *http.Request) bool {
	switch op {
	case "create":
		return hasFolderRight(st, user, int64(mapi.PrivateFIDIPMSubtree), mapi.FrightsCreateSubfolder)
	case "rename", "delete":
		id, ok := userFolderID(r.FormValue("id"))
		if !ok {
			return true // a built-in id: the handler rejects it with its own 403
		}
		return hasFolderRight(st, user, id, mapi.FrightsOwner)
	}
	return false
}

// sharedBulkAllowed authorizes a bulk (multi-select) op in a shared mailbox. Every
// selected message sits in the same source folder, so one folder-rights check
// gates the whole batch: marking read/unread, flagging, and categorizing need
// EditAny; junk/delete/move need DeleteAny on the source plus Create on the
// destination. An unknown op denies. A store owner clears every check.
func sharedBulkAllowed(st *objectstore.Store, user, op string, src int64, folders []objectstore.FolderInfo, r *http.Request) bool {
	can := func(fid int64, need uint32) bool { return hasFolderRight(st, user, fid, need) }
	switch op {
	case "read", "unread", "flag", "unflag", "categorize":
		return can(src, mapi.FrightsEditAny)
	case "junk":
		if src == int64(mapi.PrivateFIDJunk) {
			return true
		}
		return can(src, mapi.FrightsDeleteAny) && can(int64(mapi.PrivateFIDJunk), mapi.FrightsCreate)
	case "delete":
		if src == int64(mapi.PrivateFIDDeletedItems) {
			return can(src, mapi.FrightsDeleteAny)
		}
		return can(src, mapi.FrightsDeleteAny) && can(int64(mapi.PrivateFIDDeletedItems), mapi.FrightsCreate)
	case "move":
		dst, ok := parseDst(r, folders)
		if !ok || dst == src {
			return true
		}
		return can(src, mapi.FrightsDeleteAny) && can(dst, mapi.FrightsCreate)
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
		canManage := hasFolderRight(st, sess.user, int64(mapi.PrivateFIDIPMSubtree), mapi.FrightsCreateSubfolder)
		st.Close()
		if len(vis) == 0 {
			continue
		}
		out = append(out, sharedMailboxGroup{
			Addr: b.Address, AddrQS: url.QueryEscape(b.Address), Folders: vis, CanManage: canManage,
		})
	}
	return out
}
