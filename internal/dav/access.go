package dav

import (
	"net/http"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// Access to another account's collections is decided by the folder permissions
// of the owner's mailbox, the same rights webmail2, EWS and MAPI read
// (ResolvePermission, which also raises a store owner to full rights). The
// owner's delegate list is not a grant on its own: in Exchange it is the
// send-on-behalf list, and a delegate reaches a folder only through the
// permission that folder holds for them.

// permissionName is the name a folder grant is stored under for user.
// Directory addresses are lower case, while a Basic login may arrive in any case.
func permissionName(user string) string { return strings.ToLower(user) }

// canOpenMailbox reports whether user may address the mailbox at all: as a store
// owner, or with a grant of their own on some folder. The always-present default
// free/busy grant does not count, so a stranger is refused before any folder is
// looked at.
func canOpenMailbox(st *objectstore.Store, user string) (bool, error) {
	if owner, err := st.IsStoreOwner(permissionName(user)); err != nil || owner {
		return owner, err
	}
	return st.HasFolderGrant(permissionName(user))
}

// methodRights is the folder right each method needs on a shared collection or
// on the collection that holds a shared object. PUT both creates and replaces,
// so it needs both rights.
func methodRights(method string) (uint32, bool) {
	switch method {
	case "PROPFIND", "REPORT", http.MethodGet, http.MethodHead:
		return mapi.FrightsReadAny, true
	case http.MethodPut:
		return mapi.FrightsCreate | mapi.FrightsEditAny, true
	case http.MethodDelete:
		return mapi.FrightsDeleteAny, true
	case "PROPPATCH":
		return mapi.FrightsOwner, true
	}
	return 0, false
}

// sharedCollectionFID resolves the folder a collection path names in the owner's
// mailbox. ok is false when the mailbox holds no such collection; the handler then
// answers 404 itself.
func sharedCollectionFID(st *objectstore.Store, kind resourceKind, coll string) (int64, bool, error) {
	switch kind {
	case kindAddressbook, kindObject:
		return cardCollectionFID(st, coll)
	case kindCalendar, kindCalObject:
		return calCollectionFID(st, coll)
	}
	return 0, false, nil
}

// hasRights reports whether user holds every bit of need on fid.
func hasRights(st *objectstore.Store, fid int64, user string, need uint32) (bool, error) {
	rights, err := st.ResolvePermission(fid, permissionName(user))
	if err != nil {
		return false, err
	}
	return rights&need == need, nil
}

// sharedRequestAllowed decides one request on another account's mailbox before
// any handler runs, so a refused write never reaches the import or scheduling side
// effects of PUT and DELETE.
func sharedRequestAllowed(st *objectstore.Store, r *http.Request, user string, kind resourceKind, coll string) (bool, error) {
	if r.Method == "OPTIONS" {
		return true, nil
	}
	switch kind {
	case kindRoot, kindPrincipal, kindHomeSet, kindCalHomeSet:
		// Discovery only; the listing is cut down to the readable collections.
		return r.Method == "PROPFIND", nil
	case kindScheduleInbox, kindScheduleInboxObject, kindScheduleOutbox:
		return scheduleRequestAllowed(st, r, user, kind)
	}
	switch r.Method {
	case "MKCOL", "MKCALENDAR":
		return hasRights(st, collectionRoot(kind), user, mapi.FrightsCreateSubfolder)
	case "COPY", "MOVE":
		return copyMoveAllowed(st, r, user, kind, coll)
	}
	need, known := methodRights(r.Method)
	if !known {
		return false, nil
	}
	fid, ok, err := sharedCollectionFID(st, kind, coll)
	if err != nil || !ok {
		return ok || err == nil, err
	}
	return hasRights(st, fid, user, need)
}

// collectionRoot is the well-known folder a new collection of kind is created under.
func collectionRoot(kind resourceKind) int64 {
	if kind == kindAddressbook || kind == kindObject {
		return int64(mapi.PrivateFIDContacts)
	}
	return int64(mapi.PrivateFIDCalendar)
}

// scheduleRequestAllowed gates the owner's scheduling Inbox, a view over the
// meeting requests in their Inbox folder, and refuses their Outbox: a POST there
// would schedule on the owner's behalf, which no folder right grants.
func scheduleRequestAllowed(st *objectstore.Store, r *http.Request, user string, kind resourceKind) (bool, error) {
	if kind == kindScheduleOutbox {
		return r.Method == "PROPFIND", nil
	}
	need, known := methodRights(r.Method)
	if !known {
		return false, nil
	}
	return hasRights(st, int64(mapi.PrivateFIDInbox), user, need)
}

// copyMoveAllowed checks both ends of a COPY or MOVE: the source must be readable
// (and, for MOVE, deletable) and the destination writable like a PUT. A request
// the parser refuses is let through for the handler to answer.
func copyMoveAllowed(st *objectstore.Store, r *http.Request, user string, kind resourceKind, coll string) (bool, error) {
	req, failure, _ := parseCopyMove(r)
	if failure != "" {
		return true, nil
	}
	srcNeed := mapi.FrightsReadAny
	if r.Method == "MOVE" {
		srcNeed |= mapi.FrightsDeleteAny
	}
	for _, end := range []struct {
		coll string
		need uint32
	}{{coll, srcNeed}, {req.dstColl, mapi.FrightsCreate | mapi.FrightsEditAny}} {
		fid, ok, err := sharedCollectionFID(st, kind, end.coll)
		if err != nil || !ok {
			return err == nil, err
		}
		if allowed, err := hasRights(st, fid, user, end.need); err != nil || !allowed {
			return false, err
		}
	}
	return true, nil
}

// privilegeSetFor is the DAV:current-user-privilege-set (RFC 3744 5.4) a set of
// folder rights amounts to, so a client shows a shared collection read-only when
// the grant is read-only instead of offering edits the server then refuses.
func privilegeSetFor(rights uint32) *privilegeSet {
	ps := &privilegeSet{}
	add := func(p privilege) { ps.Privileges = append(ps.Privileges, p) }
	if rights&mapi.FrightsReadAny != 0 {
		add(privilege{Read: empty})
		add(privilege{ReadCurrentUserPrivilegeSet: empty})
		add(privilege{ReadFreeBusy: empty})
	}
	if rights&(mapi.FrightsCreate|mapi.FrightsEditAny) == mapi.FrightsCreate|mapi.FrightsEditAny {
		add(privilege{WriteContent: empty})
		add(privilege{Bind: empty})
	}
	if rights&mapi.FrightsDeleteAny != 0 {
		add(privilege{Unbind: empty})
	}
	if rights&mapi.FrightsOwner != 0 {
		add(privilege{WriteProperties: empty})
		add(privilege{ReadACL: empty})
	}
	return ps
}

// restrictSharedResponses applies the caller's folder rights to a PROPFIND answer
// on another account's mailbox: a collection or object the caller may not read is
// dropped, and every privilege set left reports what the caller really holds.
func restrictSharedResponses(st *objectstore.Store, user string, in []msResponse) ([]msResponse, error) {
	out := in[:0]
	for _, resp := range in {
		dp := classify(hrefPath(resp.Href))
		fid, ok, err := sharedCollectionFID(st, dp.kind, dp.collection)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, resp)
			continue
		}
		rights, err := st.ResolvePermission(fid, permissionName(user))
		if err != nil {
			return nil, err
		}
		if rights&mapi.FrightsReadAny == 0 {
			continue
		}
		setPrivileges(&resp, rights)
		out = append(out, resp)
	}
	return out, nil
}

// setPrivileges replaces every privilege set a response carries.
func setPrivileges(resp *msResponse, rights uint32) {
	for i := range resp.Propstat {
		if resp.Propstat[i].Prop.CurrentUserPrivSet != nil {
			resp.Propstat[i].Prop.CurrentUserPrivSet = privilegeSetFor(rights)
		}
	}
}

// hrefPath strips any scheme and host from an href, leaving the path classify reads.
func hrefPath(h string) string {
	if i := strings.Index(h, "/dav/"); i > 0 {
		return h[i:]
	}
	return h
}
