package imap

import (
	"strings"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// The public folders shared namespace (RFC 2342). hermEX's IMAP hierarchy uses
// "/" as its delimiter, so the shared namespace presents the caller's domain
// public folders under "Public Folders/", matching Microsoft Exchange's IMAP
// convention. (Standard IMAP servers expose no public folders; this surface is
// grounded in RFC 2342 + Exchange wire-compatibility, the external-protocol
// mandate.)
const (
	publicNamespaceRoot   = "Public Folders"
	publicNamespacePrefix = "Public Folders/"
)

// isPublicName reports whether an IMAP mailbox name addresses the public folders
// namespace, returning the folder path beneath it ("" for the namespace root
// itself, which is not selectable).
func isPublicName(name string) (sub string, ok bool) {
	if name == publicNamespaceRoot {
		return "", true
	}
	if rest, found := strings.CutPrefix(name, publicNamespacePrefix); found {
		return rest, true
	}
	return "", false
}

// curStore returns the store the currently selected folder lives in: the public
// store when a public folder is selected, otherwise the caller's own mailbox.
// Every operation on the selected folder (FETCH/STORE/SEARCH/COPY source, the
// poll refresh) must route through here so it reaches the right store — folder ids
// are not unique across the two stores, so the selection's store and id only mean
// something together.
func (c *conn) curStore() *objectstore.Store {
	if c.selPublic {
		return c.pubStore
	}
	return c.st
}

// openPub lazily opens the caller's own-domain public store and caches it for the
// connection's lifetime (closed alongside c.st at logout), mirroring how the own
// mailbox handle is held. The domain is always derived from the authenticated
// caller inside the publicfolder service, never supplied by the client, so a
// connection can never reach another tenant's public store. ok is false when the
// feature is unwired or the caller's domain has no public store.
func (c *conn) openPub() (*objectstore.Store, bool) {
	if c.pubStore != nil {
		return c.pubStore, true
	}
	if c.srv.Pub == nil {
		return nil, false
	}
	st, ok, err := c.srv.Pub.OpenForCaller(c.user)
	if err != nil {
		c.event(logging.LevelError, "publicfolder.open.fail", logging.Fields{"error": err.Error()})
		return nil, false
	}
	if !ok {
		return nil, false
	}
	c.pubStore = st
	return st, true
}

// resolvePublicFolder finds a top-level public folder by name in the caller's
// domain store and returns it with the caller's effective rights, but only when
// the caller may at least see it (FrightsVisible). v1 public folders are a flat
// set directly under the IPM subtree, so a name with a further "/" never matches.
func (c *conn) resolvePublicFolder(sub string) (objectstore.FolderInfo, uint32, bool) {
	st, ok := c.openPub()
	if !ok {
		return objectstore.FolderInfo{}, 0, false
	}
	all, err := st.ListFolders()
	if err != nil {
		return objectstore.FolderInfo{}, 0, false
	}
	for _, f := range all {
		if f.ParentID != nil || f.DisplayName != sub {
			continue
		}
		rights, err := st.ResolvePermission(f.ID, c.user)
		if err != nil || rights&mapi.FrightsVisible == 0 {
			return objectstore.FolderInfo{}, 0, false
		}
		return f, rights, true
	}
	return objectstore.FolderInfo{}, 0, false
}

// resolveAppendDest resolves an APPEND/COPY destination mailbox to the store and
// folder id it lives in, gating a public destination on post rights (FrightsCreate).
// On failure it returns the IMAP NO text to report.
func (c *conn) resolveAppendDest(name string) (st *objectstore.Store, fid int64, ok bool, errText string) {
	if sub, isPub := isPublicName(name); isPub {
		if sub == "" {
			return nil, 0, false, "the public folders root is not a mailbox"
		}
		f, rights, found := c.resolvePublicFolder(sub)
		if !found {
			return nil, 0, false, "[TRYCREATE] no such mailbox"
		}
		if rights&mapi.FrightsCreate == 0 {
			return nil, 0, false, "[NOPERM] no post rights on this public folder"
		}
		return c.pubStore, f.ID, true, ""
	}
	tree, err := loadFolderTree(c.st)
	if err != nil {
		return nil, 0, false, "cannot read mailbox list"
	}
	node, found := tree.resolve(name)
	if !found {
		return nil, 0, false, "[TRYCREATE] no such mailbox"
	}
	return c.st, node.info.ID, true, ""
}

// selectPublic opens a public folder for SELECT/EXAMINE: it must be visible and
// readable (FrightsReadAny); the selection is read-only unless the caller also has
// post rights (FrightsCreate). It mirrors the own-mailbox SELECT response.
func (c *conn) selectPublic(tag, name, sub string, examine bool) {
	if sub == "" {
		c.state = stateAuth
		c.sel = nil
		c.no(tag, "the public folders root is not selectable")
		return
	}
	f, rights, found := c.resolvePublicFolder(sub)
	if !found {
		c.state = stateAuth
		c.sel = nil
		c.no(tag, "no such mailbox")
		return
	}
	if rights&mapi.FrightsReadAny == 0 {
		c.no(tag, "[NOPERM] read access denied")
		return
	}
	sel, err := loadMailbox(c.pubStore, f.ID, name)
	if err != nil {
		c.no(tag, "cannot open mailbox")
		return
	}
	c.sel = sel
	c.selPublic = true
	c.state = stateSelected
	// Read-write enables unrestricted STORE and EXPUNGE on the folder's existing
	// items, which the store applies without per-item ownership checks — so it
	// requires the "any" edit/delete rights (Editor level), not merely the create
	// right. A pure poster (Create only) gets a read-only selection but can still
	// APPEND, the post-without-modify semantics public folders want; this is also
	// why FrightsEditOwned/DeleteOwned alone do not grant read-write (the store
	// cannot enforce the owned-only restriction over IMAP).
	c.readOnly = examine || rights&(mapi.FrightsEditAny|mapi.FrightsDeleteAny) == 0
	c.emitSelected(tag, sel, examine)
}

// statusPublic answers STATUS for a public folder. A folder LIST advertises must
// also answer STATUS (clients poll it for unread badges), so the gate is the same
// visibility check LIST uses; the counts come from the public store.
func (c *conn) statusPublic(tag, name, sub string, items []string) {
	if sub == "" {
		c.no(tag, "no such mailbox")
		return
	}
	f, _, found := c.resolvePublicFolder(sub)
	if !found {
		c.no(tag, "no such mailbox")
		return
	}
	st := c.pubStore // resolvePublicFolder opened it
	msgs, err := st.ListMessages(f.ID)
	if err != nil {
		c.no(tag, "cannot read mailbox")
		return
	}
	uidv, _ := st.UIDValidity(f.ID)
	uidn, _ := st.UIDNext(f.ID)
	hms, _ := st.FolderHighestModSeq(f.ID)
	c.untagged("STATUS %s (%s)", quoteString(name), statusParts(items, msgs, uidv, uidn, hms))
	c.ok(tag, "STATUS completed")
}

// emitSelected writes the untagged SELECT/EXAMINE response and the tagged OK,
// keying the read-only status on c.readOnly (a SELECT of a public folder the
// caller cannot post to is downgraded to read-only).
func (c *conn) emitSelected(tag string, sel *selectedMailbox, examine bool) {
	c.untagged("%d EXISTS", sel.maxSeq())
	c.untagged("0 RECENT")
	c.untagged(`FLAGS (%s)`, supportedFlagNames())
	if c.readOnly {
		c.untagged(`OK [PERMANENTFLAGS ()] read-only, no permanent flags`)
	} else {
		c.untagged(`OK [PERMANENTFLAGS (%s)] limited`, supportedFlagNames())
	}
	c.untagged("OK [UIDVALIDITY %d] validity", sel.uidValidity)
	c.untagged("OK [UIDNEXT %d] next uid", sel.uidNext)
	if u := sel.firstUnseen(); u != 0 {
		c.untagged("OK [UNSEEN %d] first unseen", u)
	}
	// CONDSTORE (RFC 7162): report the mailbox HIGHESTMODSEQ once the session has
	// enabled CONDSTORE, so the client has a sync baseline.
	if c.condstore {
		c.untagged("OK [HIGHESTMODSEQ %d] modseq", c.highestModSeq())
	}
	verb := "SELECT"
	if examine {
		verb = "EXAMINE"
	}
	status := "[READ-WRITE]"
	if c.readOnly {
		status = "[READ-ONLY]"
	}
	c.ok(tag, status+" "+verb+" completed")
}

// listPublicFolders appends the caller's ACL-visible public folders to a LIST/LSUB
// response. Public folders carry no per-user subscription state in v1, so they
// appear in LSUB as well (effectively always subscribed). The IPM subtree carries
// no grant, so each child is filtered individually.
func (c *conn) listPublicFolders(verb, full string) {
	st, ok := c.openPub()
	if !ok {
		return
	}
	all, err := st.ListFolders()
	if err != nil {
		return
	}
	var visible []objectstore.FolderInfo
	for _, f := range all {
		if f.ParentID != nil {
			continue
		}
		rights, err := st.ResolvePermission(f.ID, c.user)
		if err != nil || rights&mapi.FrightsVisible == 0 {
			continue
		}
		visible = append(visible, f)
	}
	if len(visible) == 0 {
		return
	}
	if imapMatch(full, publicNamespaceRoot) {
		c.untagged(`%s (\HasChildren \Noselect) "%s" %s`, verb, hierarchySep, quoteString(publicNamespaceRoot))
	}
	for _, f := range visible {
		path := publicNamespacePrefix + f.DisplayName
		if imapMatch(full, path) {
			c.untagged(`%s (\HasNoChildren) "%s" %s`, verb, hierarchySep, quoteString(path))
		}
	}
}

// cmdNamespace answers the NAMESPACE command (RFC 2342): a personal namespace at
// the mailbox root, no other-users namespace, and the public folders shared
// namespace. The delimiter matches the LIST hierarchy delimiter so clients parse
// "Public Folders/" correctly.
func (c *conn) cmdNamespace(tag string) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	c.untagged(`NAMESPACE (("" "%s")) NIL (("%s" "%s"))`, hierarchySep, publicNamespacePrefix, hierarchySep)
	c.ok(tag, "NAMESPACE completed")
}
