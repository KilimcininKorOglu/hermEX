package imap

import (
	"strings"

	"hermex/internal/directory"
	"hermex/internal/logging"
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

// rightsToACL renders a MAPI rights bitfield as the RFC 4314 right letters. When
// any member of the delete ("et") or create ("kx") right is held, the virtual
// "d" or "c" right is listed too, as RFC 4314 2.1.1 requires for RFC 2086 clients.
func rightsToACL(r uint32) string {
	var b []byte
	for _, m := range aclRights {
		if r&m.fright != 0 {
			b = append(b, m.letter)
		}
	}
	s := string(b)
	if strings.ContainsAny(s, "kx") {
		s += "c"
	}
	if strings.ContainsAny(s, "et") {
		s += "d"
	}
	return s
}

// virtualRights are the obsolete RFC 2086 rights RFC 4314 2.1.1 keeps as
// shorthands: "d" for the delete right ("et", as the RFC example expands it) and
// "c" for the create right ("kx").
var virtualRights = map[byte]string{'d': "et", 'c': "kx"}

// aclToRights parses RFC 4314 right letters into a MAPI rights bitfield. ok is
// false when a letter is no right this server knows, which RFC 4314 3.1 answers
// with BAD rather than ignoring it.
func aclToRights(s string) (uint32, bool) {
	var r uint32
	for i := 0; i < len(s); i++ {
		letters := string(s[i])
		if v, isVirtual := virtualRights[s[i]]; isVirtual {
			letters = v
		}
		for j := 0; j < len(letters); j++ {
			bit, known := rightOf(letters[j])
			if !known {
				return 0, false
			}
			r |= bit
		}
	}
	return r, true
}

// rightOf returns the MAPI right one RFC 4314 letter maps to.
func rightOf(letter byte) (uint32, bool) {
	for _, m := range aclRights {
		if m.letter == letter {
			return m.fright, true
		}
	}
	return 0, false
}

// aclFolder identifies a resolved mailbox and whether the caller owns it as a
// private mailbox.
type aclFolder struct {
	st         *objectstore.Store
	fid        int64
	ownPrivate bool
}

// aclTarget identifies a resolved mailbox that passed the administer check.
type aclTarget struct {
	st   *objectstore.Store
	fid  int64
	name string
}

// resolveACLFolder resolves a mailbox name to the store and folder id its ACL lives
// in, covering both the caller's own folders and public folders. ownPrivate marks a
// folder in the caller's own mailbox, the directory already established the caller
// owns it, so they hold full rights there regardless of any stored permission row.
func (c *conn) resolveACLFolder(name string) *aclFolder {
	if sub, isPub := isPublicName(name); isPub {
		m := c.resolvePublicFolder(sub)
		if m == nil {
			return nil
		}
		return &aclFolder{st: c.pubStore, fid: m.info.ID, ownPrivate: false}
	}
	tree, err := loadFolderTree(c.st)
	if err != nil {
		return nil
	}
	node, found := tree.resolve(name)
	if !found {
		return nil
	}
	return &aclFolder{st: c.st, fid: node.info.ID, ownPrivate: true}
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
	af := c.resolveACLFolder(name)
	if af == nil {
		c.no(tag, "no such mailbox")
		return
	}
	rights := mapi.RightsAll
	if !af.ownPrivate {
		var err error
		rights, err = af.st.ResolvePermission(af.fid, c.user)
		if err != nil {
			c.event(logging.LevelError, "acl.resolve.fail", logging.Fields{"folder": af.fid, "error": err.Error()})
			c.no(tag, "could not read permissions")
			return
		}
	}
	c.untagged("MYRIGHTS %s %s", safeQuoted(name), rightsToACL(rights))
	c.ok(tag, "MYRIGHTS completed")
}

// isPublicOwner checks whether the caller is the owner of a public store,
// logging and returning any store read error.
func (c *conn) isPublicOwner(st *objectstore.Store, fid int64) (bool, error) {
	owner, err := st.IsStoreOwner(c.user)
	if err != nil {
		c.event(logging.LevelError, "acl.owner.fail", logging.Fields{"folder": fid, "error": err.Error()})
		return false, err
	}
	return owner, nil
}

// cmdGetACL handles GETACL <mailbox>: the mailbox's identifier/rights pairs. Like
// SETACL it needs the administer right (RFC 4314 section 4), since the list
// names everyone the folder is shared with. The store owner is always listed with
// full rights, since the resolver elevates them regardless of the stored rows.
func (c *conn) cmdGetACL(tag string, args []token) {
	target := c.aclAdminTarget(tag, args)
	if target == nil {
		return
	}
	ownPrivate := target.st == c.st
	entries, err := target.st.ListPermissions(target.fid)
	if err != nil {
		c.no(tag, "could not read the access list")
		return
	}
	var parts []string
	listed := false
	for _, e := range entries {
		if e.Name == c.user {
			listed = true
		}
		parts = append(parts, safeQuoted(e.Name)+" "+rightsToACL(e.Rights))
	}
	owner := ownPrivate
	if !owner {
		owner, err = c.isPublicOwner(target.st, target.fid)
		if err != nil {
			c.no(tag, "could not verify mailbox owner")
			return
		}
	}
	if owner && !listed {
		parts = append([]string{safeQuoted(c.user) + " " + rightsToACL(mapi.RightsAll)}, parts...)
	}
	c.untagged("ACL %s %s", safeQuoted(target.name), strings.Join(parts, " "))
	c.ok(tag, "GETACL completed")
}

// cmdSetACL handles SETACL <mailbox> <identifier> <rights>: the rights string may
// be replaced outright, or adjusted with a leading + or - (RFC 4314).
func (c *conn) cmdSetACL(tag string, args []token) {
	target := c.aclAdminTarget(tag, args)
	if target == nil {
		return
	}
	id, ok1 := argN(args, 1)
	rightsStr, ok2 := argN(args, 2)
	if !ok1 || !ok2 {
		c.bad(tag, "SETACL requires a mailbox, identifier, and rights")
		return
	}
	mode, requested, known := parseACLChange(rightsStr)
	if !known {
		c.bad(tag, "SETACL names an unrecognized right")
		return
	}
	if !c.mayGrantTo(mode, id) {
		c.no(tag, "[ACL] the identifier is not an account of this organization")
		return
	}
	newRights, err := adjustedRights(target.st, target.fid, id, mode, requested)
	if err != nil {
		c.no(tag, "cannot read the access list")
		return
	}
	change := objectstore.PermissionChange{
		Op:       objectstore.PermAdd,
		Username: id,
		Rights:   mapi.NormalizeRights(newRights&mapi.RightsMaxROP, false),
	}
	if err := target.st.ModifyPermissions(target.fid, false, []objectstore.PermissionChange{change}); err != nil {
		c.no(tag, "cannot set ACL")
		return
	}
	c.ok(tag, "SETACL completed")
}

// parseACLChange splits a SETACL rights argument into its mode ('+', '-' or 0
// for a replacement) and the rights it names; known is false when it names an
// unrecognized right.
func parseACLChange(s string) (mode byte, rights uint32, known bool) {
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		mode, s = s[0], s[1:]
	}
	rights, known = aclToRights(s)
	return mode, rights, known
}

// mayGrantTo reports whether the caller may give id rights. A grant opens the
// mailbox to its identifier, and the mailbox surfaces keep that inside the
// organization, so an identifier outside the caller's organization is refused;
// a removal ("-") is always allowed.
func (c *conn) mayGrantTo(mode byte, id string) bool {
	if mode == '-' {
		return true
	}
	reach, err := directory.MayReach(c.srv.Accounts, c.user, id)
	return err == nil && reach
}

// cmdDeleteACL handles DELETEACL <mailbox> <identifier>: it drops an identifier's
// rights row.
func (c *conn) cmdDeleteACL(tag string, args []token) {
	target := c.aclAdminTarget(tag, args)
	if target == nil {
		return
	}
	id, ok := argN(args, 1)
	if !ok {
		c.bad(tag, "DELETEACL requires a mailbox and identifier")
		return
	}
	memberID, err := aclMemberID(target.st, target.fid, id)
	if err != nil {
		c.no(tag, "cannot read the access list")
		return
	}
	change := objectstore.PermissionChange{Op: objectstore.PermRemove, Username: id, MemberID: memberID}
	if err := target.st.ModifyPermissions(target.fid, false, []objectstore.PermissionChange{change}); err != nil {
		c.no(tag, "cannot delete ACL")
		return
	}
	c.ok(tag, "DELETEACL completed")
}

// cmdListRights handles LISTRIGHTS <mailbox> <identifier>: the rights model. Lookup
// is the always-granted right; the rest are individually grantable.
func (c *conn) cmdListRights(tag string, args []token) {
	id, hasID := argN(args, 1)
	if c.state != stateNotAuth && !hasID {
		c.bad(tag, "LISTRIGHTS requires a mailbox and identifier")
		return
	}
	// RFC 4314 section 4: LISTRIGHTS needs the administer right, like GETACL.
	target := c.aclAdminTarget(tag, args)
	if target == nil {
		return
	}
	c.untagged("LISTRIGHTS %s %s l r s w i p k x t e a c d", safeQuoted(target.name), safeQuoted(id))
	c.ok(tag, "LISTRIGHTS completed")
}

// aclAdminTarget resolves the mailbox for a SETACL/DELETEACL and verifies the caller
// holds the administer right, replying NO when they do not. nil means a reply
// was already sent.
func (c *conn) aclAdminTarget(tag string, args []token) *aclTarget {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return nil
	}
	name, has := arg0(args)
	if !has {
		c.bad(tag, "ACL command requires a mailbox")
		return nil
	}
	af := c.resolveACLFolder(name)
	if af == nil {
		c.no(tag, "no such mailbox")
		return nil
	}
	// The caller owns their own mailbox outright; for a public folder the administer
	// right must be granted explicitly.
	if !af.ownPrivate {
		rights, err := af.st.ResolvePermission(af.fid, c.user)
		if err != nil {
			c.event(logging.LevelError, "acl.resolve.fail", logging.Fields{"folder": af.fid, "error": err.Error()})
			c.no(tag, "could not verify administer rights")
			return nil
		}
		if rights&mapi.FrightsOwner == 0 {
			c.no(tag, "[ACL] insufficient rights to administer this mailbox")
			return nil
		}
	}
	return &aclTarget{st: af.st, fid: af.fid, name: name}
}

// adjustedRights applies a SETACL rights string: mode '+' adds delta to the
// identifier's stored rights, '-' removes it, anything else replaces them. A
// failed read is returned, since treating it as no rights would let "+r"
// overwrite the stored rights with r alone.
func adjustedRights(st *objectstore.Store, fid int64, id string, mode byte, delta uint32) (uint32, error) {
	if mode != '+' && mode != '-' {
		return delta, nil
	}
	e, err := aclEntry(st, fid, id)
	if err != nil {
		return 0, err
	}
	if mode == '+' {
		return e.Rights | delta, nil
	}
	return e.Rights &^ delta, nil
}

// aclMemberID returns the stored member id for an identifier, or 0 (the default
// member) when none is stored, DELETEACL of a real member needs its row id.
func aclMemberID(st *objectstore.Store, fid int64, id string) (int64, error) {
	e, err := aclEntry(st, fid, id)
	return e.MemberID, err
}

// aclEntry returns an identifier's stored permission row, or the zero row when
// it has none.
func aclEntry(st *objectstore.Store, fid int64, id string) (objectstore.PermissionEntry, error) {
	entries, err := st.ListPermissions(fid)
	if err != nil {
		return objectstore.PermissionEntry{}, err
	}
	for _, e := range entries {
		if e.Name == id {
			return e, nil
		}
	}
	return objectstore.PermissionEntry{}, nil
}
