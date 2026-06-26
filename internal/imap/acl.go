package imap

import (
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ACL (RFC 4314). The IMAP right letters map onto the MAPI folder-permission bits
// the object store already enforces, so a shared-folder grant set over one protocol
// is honored by the other. Exchange's permission model is coarser than IMAP's, so
// several letters share one bit.
var aclRights = []struct {
	letter byte
	fright uint32
}{
	{'l', mapi.FrightsVisible},         // lookup
	{'r', mapi.FrightsReadAny},         // read
	{'s', mapi.FrightsReadAny},         // keep seen state
	{'w', mapi.FrightsEditAny},         // write flags
	{'i', mapi.FrightsCreate},          // insert (APPEND/COPY in)
	{'p', mapi.FrightsCreate},          // post
	{'k', mapi.FrightsCreateSubfolder}, // create mailbox
	{'x', mapi.FrightsOwner},           // delete mailbox
	{'t', mapi.FrightsDeleteAny},       // delete messages (\Deleted)
	{'e', mapi.FrightsDeleteAny},       // expunge
	{'a', mapi.FrightsOwner},           // administer
}

// rightsToACL renders a MAPI rights bitfield as the RFC 4314 right letters.
func rightsToACL(r uint32) string {
	var b []byte
	for _, m := range aclRights {
		if r&m.fright != 0 {
			b = append(b, m.letter)
		}
	}
	return string(b)
}

// aclToRights parses RFC 4314 right letters into a MAPI rights bitfield.
func aclToRights(s string) uint32 {
	var r uint32
	for i := 0; i < len(s); i++ {
		for _, m := range aclRights {
			if s[i] == m.letter {
				r |= m.fright
			}
		}
	}
	return r
}

// resolveACLFolder resolves a mailbox name to the store and folder id its ACL lives
// in, covering both the caller's own folders and public folders. ownPrivate marks a
// folder in the caller's own mailbox — the directory already established the caller
// owns it, so they hold full rights there regardless of any stored permission row.
func (c *conn) resolveACLFolder(name string) (st *objectstore.Store, fid int64, ownPrivate, ok bool) {
	if sub, isPub := isPublicName(name); isPub {
		f, _, found := c.resolvePublicFolder(sub)
		if !found {
			return nil, 0, false, false
		}
		return c.pubStore, f.ID, false, true
	}
	tree, err := loadFolderTree(c.st)
	if err != nil {
		return nil, 0, false, false
	}
	node, found := tree.resolve(name)
	if !found {
		return nil, 0, false, false
	}
	return c.st, node.info.ID, true, true
}

// cmdMyRights handles MYRIGHTS <mailbox>: the caller's effective rights on it.
func (c *conn) cmdMyRights(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	name, ok := arg0(args)
	if !ok {
		c.bad(tag, "MYRIGHTS requires a mailbox")
		return
	}
	st, fid, ownPrivate, ok := c.resolveACLFolder(name)
	if !ok {
		c.no(tag, "no such mailbox")
		return
	}
	rights := mapi.RightsAll
	if !ownPrivate {
		rights, _ = st.ResolvePermission(fid, c.user)
	}
	c.untagged("MYRIGHTS %s %s", quoteString(name), rightsToACL(rights))
	c.ok(tag, "MYRIGHTS completed")
}

// cmdGetACL handles GETACL <mailbox>: the mailbox's identifier/rights pairs. The
// store owner is always listed with full rights, since the resolver elevates them
// regardless of the stored rows.
func (c *conn) cmdGetACL(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	name, ok := arg0(args)
	if !ok {
		c.bad(tag, "GETACL requires a mailbox")
		return
	}
	st, fid, ownPrivate, ok := c.resolveACLFolder(name)
	if !ok {
		c.no(tag, "no such mailbox")
		return
	}
	entries, _ := st.ListPermissions(fid)
	var parts []string
	listed := false
	for _, e := range entries {
		if e.Name == c.user {
			listed = true
		}
		parts = append(parts, quoteString(e.Name)+" "+rightsToACL(e.Rights))
	}
	owner := ownPrivate
	if !owner {
		owner, _ = st.IsStoreOwner(c.user)
	}
	if owner && !listed {
		parts = append([]string{quoteString(c.user) + " " + rightsToACL(mapi.RightsAll)}, parts...)
	}
	c.untagged("ACL %s %s", quoteString(name), strings.Join(parts, " "))
	c.ok(tag, "GETACL completed")
}

// cmdSetACL handles SETACL <mailbox> <identifier> <rights>: the rights string may
// be replaced outright, or adjusted with a leading + or - (RFC 4314).
func (c *conn) cmdSetACL(tag string, args []token) {
	st, fid, _, ok := c.aclAdminTarget(tag, args)
	if !ok {
		return
	}
	id, ok1 := argN(args, 1)
	rightsStr, ok2 := argN(args, 2)
	if !ok1 || !ok2 {
		c.bad(tag, "SETACL requires a mailbox, identifier, and rights")
		return
	}
	mode := byte(0)
	if strings.HasPrefix(rightsStr, "+") || strings.HasPrefix(rightsStr, "-") {
		mode, rightsStr = rightsStr[0], rightsStr[1:]
	}
	delta := aclToRights(rightsStr)
	var newRights uint32
	switch mode {
	case '+':
		newRights = currentACLRights(st, fid, id) | delta
	case '-':
		newRights = currentACLRights(st, fid, id) &^ delta
	default:
		newRights = delta
	}
	change := objectstore.PermissionChange{
		Op:       objectstore.PermAdd,
		Username: id,
		Rights:   mapi.NormalizeRights(newRights&mapi.RightsMaxROP, false),
	}
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{change}); err != nil {
		c.no(tag, "cannot set ACL")
		return
	}
	c.ok(tag, "SETACL completed")
}

// cmdDeleteACL handles DELETEACL <mailbox> <identifier>: it drops an identifier's
// rights row.
func (c *conn) cmdDeleteACL(tag string, args []token) {
	st, fid, _, ok := c.aclAdminTarget(tag, args)
	if !ok {
		return
	}
	id, ok := argN(args, 1)
	if !ok {
		c.bad(tag, "DELETEACL requires a mailbox and identifier")
		return
	}
	change := objectstore.PermissionChange{Op: objectstore.PermRemove, Username: id, MemberID: aclMemberID(st, fid, id)}
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{change}); err != nil {
		c.no(tag, "cannot delete ACL")
		return
	}
	c.ok(tag, "DELETEACL completed")
}

// cmdListRights handles LISTRIGHTS <mailbox> <identifier>: the rights model. Lookup
// is the always-granted right; the rest are individually grantable.
func (c *conn) cmdListRights(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	name, ok1 := arg0(args)
	id, ok2 := argN(args, 1)
	if !ok1 || !ok2 {
		c.bad(tag, "LISTRIGHTS requires a mailbox and identifier")
		return
	}
	if _, _, _, ok := c.resolveACLFolder(name); !ok {
		c.no(tag, "no such mailbox")
		return
	}
	c.untagged("LISTRIGHTS %s %s l r s w i p k x t e a", quoteString(name), quoteString(id))
	c.ok(tag, "LISTRIGHTS completed")
}

// aclAdminTarget resolves the mailbox for a SETACL/DELETEACL and verifies the caller
// holds the administer right, replying NO when they do not. ok=false means a reply
// was already sent.
func (c *conn) aclAdminTarget(tag string, args []token) (st *objectstore.Store, fid int64, name string, ok bool) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return nil, 0, "", false
	}
	name, has := arg0(args)
	if !has {
		c.bad(tag, "ACL command requires a mailbox")
		return nil, 0, "", false
	}
	st, fid, ownPrivate, found := c.resolveACLFolder(name)
	if !found {
		c.no(tag, "no such mailbox")
		return nil, 0, "", false
	}
	// The caller owns their own mailbox outright; for a public folder the administer
	// right must be granted explicitly.
	if !ownPrivate {
		if rights, _ := st.ResolvePermission(fid, c.user); rights&mapi.FrightsOwner == 0 {
			c.no(tag, "[ACL] insufficient rights to administer this mailbox")
			return nil, 0, "", false
		}
	}
	return st, fid, name, true
}

// currentACLRights returns an identifier's stored rights on a folder, or 0.
func currentACLRights(st *objectstore.Store, fid int64, id string) uint32 {
	entries, _ := st.ListPermissions(fid)
	for _, e := range entries {
		if e.Name == id {
			return e.Rights
		}
	}
	return 0
}

// aclMemberID returns the stored member id for an identifier, or 0 (the default
// member) when none is stored — DELETEACL of a real member needs its row id.
func aclMemberID(st *objectstore.Store, fid int64, id string) int64 {
	entries, _ := st.ListPermissions(fid)
	for _, e := range entries {
		if e.Name == id {
			return e.MemberID
		}
	}
	return 0
}
