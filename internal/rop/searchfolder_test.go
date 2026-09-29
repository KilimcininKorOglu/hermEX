package rop

import (
	"slices"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// openFinder opens the Finder folder, where a client keeps its search folders,
// off the logon at logonH and returns its handle.
func openFinder(t *testing.T, sess *Session, logonH uint32) uint32 {
	t.Helper()
	_, h := sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDFinder))), []uint32{logonH, 0xFFFFFFFF})
	if h[1] == 0xFFFFFFFF {
		t.Fatal("OpenFolder(Finder) left the handle unset")
	}
	return h[1]
}

// ownerFinder logs on as the owner of a fresh mailbox and opens its Finder.
func ownerFinder(t *testing.T, dir string) (*Session, uint32) {
	t.Helper()
	sess := NewSession(dir, nil, "")
	t.Cleanup(sess.Close)
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	return sess, openFinder(t, sess, h[0])
}

// createSearchFolderROP creates a search folder named name under the folder at
// parentH and returns its store id and the handle it was opened at.
func createSearchFolderROP(t *testing.T, sess *Session, parentH uint32, name string) (int64, uint32) {
	t.Helper()
	resp, h := sess.Dispatch(toROPRequest(ropCreateFolder, 0, createFolderBody(uint8(folderTypeSearch), name, false)), []uint32{parentH, 0xFFFFFFFF})
	p := ropOK(t, resp, ropCreateFolder, "CreateFolder(search)")
	return int64(mapi.EID(mustU64(t, p, "FolderId")).GCValue()), h[1]
}

// setCriteriaBody is a RopSetSearchCriteria request body as a client sends it:
// the restriction with its 16-bit size, a 16-bit count of folder ids, and the
// search flags.
func setCriteriaBody(t *testing.T, res *mapi.Restriction, scope []uint64, flags uint32) []byte {
	t.Helper()
	b := ext.NewPush(ext.FlagUTF16)
	if res == nil {
		b.Uint16(0)
	} else {
		r := ext.NewPush(ext.FlagUTF16)
		if err := r.Restriction(*res); err != nil {
			t.Fatal(err)
		}
		b.Uint16(uint16(len(r.Bytes())))
		b.Raw(r.Bytes())
	}
	if err := b.Uint64ArrayShort(scope); err != nil {
		t.Fatal(err)
	}
	b.Uint32(flags)
	return b.Bytes()
}

// folderEIDs is the wire form of store folder ids.
func folderEIDs(fids ...int64) []uint64 {
	out := make([]uint64, len(fids))
	for i, f := range fids {
		out[i] = uint64(mapi.MakeEIDEx(1, uint64(f)))
	}
	return out
}

// searchCriteriaReplyROP is a parsed RopGetSearchCriteria response.
type searchCriteriaReplyROP struct {
	restriction *mapi.Restriction
	logonID     uint8
	scope       []uint64
	state       uint32
}

// getCriteria sends RopGetSearchCriteria with its first ROP header carrying
// logonID and parses the response.
func getCriteria(t *testing.T, sess *Session, folderH uint32, logonID, useUnicode uint8) searchCriteriaReplyROP {
	t.Helper()
	b := ext.NewPush(ext.FlagUTF16)
	b.Raw([]byte{ropGetSearchCriteria, logonID, 0, useUnicode, 1, 1})
	resp, _ := sess.Dispatch(b.Bytes(), []uint32{folderH})
	p := ropOK(t, resp, ropGetSearchCriteria, "GetSearchCriteria")
	var out searchCriteriaReplyROP
	size, err := p.Uint16()
	if err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		raw, err := p.Raw(int(size))
		if err != nil {
			t.Fatal(err)
		}
		r, err := ext.NewPull(raw, ext.FlagUTF16).Restriction()
		if err != nil {
			t.Fatalf("restriction: %v", err)
		}
		out.restriction = &r
	}
	out.logonID = mustU8(t, p, "LogonId")
	if out.scope, err = p.Uint64ArrayShort(); err != nil {
		t.Fatal(err)
	}
	out.state = mustU32(t, p, "SearchFlags")
	if p.Remaining() != 0 {
		t.Fatalf("GetSearchCriteria response has %d trailing bytes", p.Remaining())
	}
	return out
}

// rowCount opens a table with request and returns the RowCount it reports.
func rowCount(t *testing.T, sess *Session, request []byte, folderH uint32, what string) uint32 {
	t.Helper()
	p := ropOK(t, mustDispatch(sess, request, folderH, 0xFFFFFFFF), request[0], what)
	return mustU32(t, p, "RowCount")
}

// TestSearchFolderThroughROP proves a client can create a search folder under
// Finder, give it criteria, read them back, and see the matching messages in its
// contents table, and that Finder's hierarchy table lists the search folder.
func TestSearchFolderThroughROP(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Invoice 12")
	seedInboxMessage(t, dir, "Lunch")
	seedInboxMessage(t, dir, "invoice reminder")
	sess, finderH := ownerFinder(t, dir)
	_, searchH := createSearchFolderROP(t, sess, finderH, "Invoices")

	res := objectstore.RuleSubjectContains("invoice")
	body := setCriteriaBody(t, &res, folderEIDs(int64(mapi.PrivateFIDInbox)), mapi.SearchRestart)
	ropOK(t, mustDispatch(sess, toROPRequest(ropSetSearchCriteria, 0, body), searchH, 0xFFFFFFFF), ropSetSearchCriteria, "SetSearchCriteria")

	got := getCriteria(t, sess, searchH, 3, 1)
	if got.restriction == nil || got.restriction.Type != res.Type {
		t.Errorf("restriction = %+v, want %+v", got.restriction, res)
	}
	if got.logonID != 3 {
		t.Errorf("LogonId = %d, want the request's 3", got.logonID)
	}
	if !slices.Equal(got.scope, folderEIDs(int64(mapi.PrivateFIDInbox))) {
		t.Errorf("scope = %x, want the Inbox", got.scope)
	}
	if got.state != mapi.SearchStateRunning {
		t.Errorf("state = %#x, want SEARCH_RUNNING", got.state)
	}
	if n := rowCount(t, sess, buildGetContentsTable(0, 1), searchH, "GetContentsTable(search)"); n != 2 {
		t.Errorf("search folder contents = %d rows, want the 2 invoices", n)
	}
	if n := rowCount(t, sess, buildGetHierarchyTable(0, 1), finderH, "GetHierarchyTable(Finder)"); n != 1 {
		t.Errorf("Finder hierarchy = %d rows, want the search folder", n)
	}
}

// TestCreateSearchFolderRefusals proves a search folder holds no subfolder, and a
// name taken by a folder of the other type is a duplicate even with OpenExisting,
// while the same type opens the existing folder.
func TestCreateSearchFolderRefusals(t *testing.T) {
	sess, finderH := ownerFinder(t, t.TempDir())
	fid, searchH := createSearchFolderROP(t, sess, finderH, "Saved")
	if ok, err := sess.get(searchH).store.IsSearchFolder(fid); err != nil || !ok {
		t.Fatalf("the created folder is a search folder: %v, %v", ok, err)
	}
	under := mustDispatch(sess, toROPRequest(ropCreateFolder, 0, createFolderBody(1, "Child", false)), searchH, 0xFFFFFFFF)
	if ec := readEC(t, under, ropCreateFolder); ec != ecNotSupported {
		t.Errorf("CreateFolder under a search folder ec = %#x, want ecNotSupported", ec)
	}
	generic := mustDispatch(sess, toROPRequest(ropCreateFolder, 0, createFolderBody(1, "Saved", true)), finderH, 0xFFFFFFFF)
	if ec := readEC(t, generic, ropCreateFolder); ec != ecDuplicateName {
		t.Errorf("generic OpenExisting over a search folder ec = %#x, want ecDuplicateName", ec)
	}
	same := mustDispatch(sess, toROPRequest(ropCreateFolder, 0, createFolderBody(uint8(folderTypeSearch), "Saved", true)), finderH, 0xFFFFFFFF)
	p := ropOK(t, same, ropCreateFolder, "search OpenExisting")
	if got := int64(mapi.EID(mustU64(t, p, "FolderId")).GCValue()); got != fid {
		t.Errorf("OpenExisting opened %d, want %d", got, fid)
	}
}

// TestSetSearchCriteriaRefusals proves the return codes a client meets: criteria
// on a folder that is not a search folder, a partial change before any criteria,
// a scope in another store, and a scope that holds the search folder. It also
// proves a request that names nothing and does not restart changes nothing.
func TestSetSearchCriteriaRefusals(t *testing.T) {
	sess, finderH := ownerFinder(t, t.TempDir())
	_, searchH := createSearchFolderROP(t, sess, finderH, "S")
	res := objectstore.RuleSubjectContains("x")
	inbox := folderEIDs(int64(mapi.PrivateFIDInbox))
	set := func(h uint32, r *mapi.Restriction, scope []uint64, flags uint32) uint32 {
		return ropResultEC(t, mustDispatch(sess, toROPRequest(ropSetSearchCriteria, 0, setCriteriaBody(t, r, scope, flags)), h, 0xFFFFFFFF))
	}
	if ec := set(finderH, &res, inbox, mapi.SearchRestart); ec != ecNotSearchFolder {
		t.Errorf("criteria on Finder ec = %#x, want ecNotSearchFolder", ec)
	}
	if ec := set(searchH, nil, inbox, mapi.SearchRestart); ec != ecNotInitialized {
		t.Errorf("scope without criteria ec = %#x, want ecNotInitialized", ec)
	}
	if ec := set(searchH, &res, []uint64{uint64(mapi.MakeEIDEx(2, mapi.PrivateFIDInbox))}, mapi.SearchRestart); ec != ecSearchFolderScopeViolation {
		t.Errorf("scope in another replica ec = %#x, want ecSearchFolderScopeViolation", ec)
	}
	if ec := set(searchH, &res, folderEIDs(int64(mapi.PrivateFIDRoot)), mapi.SearchRestart); ec != ecSearchFolderScopeViolation {
		t.Errorf("scope over the search folder ec = %#x, want ecSearchFolderScopeViolation", ec)
	}
	if ec := set(searchH, &res, inbox, mapi.SearchRestart); ec != ecSuccess {
		t.Fatalf("valid criteria ec = %#x", ec)
	}
	if ec := set(searchH, nil, nil, 0); ec != ecSuccess {
		t.Errorf("empty change ec = %#x, want success", ec)
	}
	if got := getCriteria(t, sess, searchH, 0, 1); got.state != mapi.SearchStateRunning {
		t.Errorf("state after an empty change = %#x, want the search still running", got.state)
	}
}

// TestGetSearchCriteriaIn8BitStrings proves a client that does not use Unicode
// gets the restriction's string property as an 8-bit string.
func TestGetSearchCriteriaIn8BitStrings(t *testing.T) {
	sess, finderH := ownerFinder(t, t.TempDir())
	_, searchH := createSearchFolderROP(t, sess, finderH, "S")
	res := objectstore.RuleSubjectContains("x")
	ropOK(t, mustDispatch(sess, toROPRequest(ropSetSearchCriteria, 0, setCriteriaBody(t, &res, folderEIDs(int64(mapi.PrivateFIDInbox)), mapi.SearchRestart)), searchH, 0xFFFFFFFF), ropSetSearchCriteria, "SetSearchCriteria")
	got := getCriteria(t, sess, searchH, 0, 0)
	c, ok := got.restriction.Value.(mapi.ContentRestriction)
	if !ok {
		t.Fatalf("restriction = %+v, want a content restriction", got.restriction)
	}
	if c.PropTag.Type() != mapi.PtString8 || c.PropVal.Tag.Type() != mapi.PtString8 {
		t.Errorf("tags = %v / %v, want 8-bit string tags", c.PropTag, c.PropVal.Tag)
	}
}

// TestDelegateSearchCriteriaRights proves a delegate needs owner rights on the
// search folder and read rights on every folder it searches.
func TestDelegateSearchCriteriaRights(t *testing.T) {
	dir := t.TempDir()
	const delegate = "delegate@hermex.test"
	owner, finderH := ownerFinder(t, dir)
	fid, _ := createSearchFolderROP(t, owner, finderH, "Shared")
	owner.Close()
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDFinder), delegate, mapi.FrightsVisible)
	grantFolderPermission(t, dir, fid, delegate, mapi.FrightsVisible)

	sess, logonH := delegateLogon(t, dir, delegate)
	defer sess.Close()
	_, h := sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(fid)))), []uint32{logonH, 0xFFFFFFFF})
	searchH := h[1]
	res := objectstore.RuleSubjectContains("x")
	body := setCriteriaBody(t, &res, folderEIDs(int64(mapi.PrivateFIDInbox)), mapi.SearchRestart)
	if ec := ropResultEC(t, mustDispatch(sess, toROPRequest(ropSetSearchCriteria, 0, body), searchH, 0xFFFFFFFF)); ec != ecAccessDenied {
		t.Errorf("without owner rights ec = %#x, want ecAccessDenied", ec)
	}
	st := sess.get(logonH).store
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{{Op: objectstore.PermAdd, Username: delegate, Rights: mapi.RightsAll}}); err != nil {
		t.Fatal(err)
	}
	if ec := ropResultEC(t, mustDispatch(sess, toROPRequest(ropSetSearchCriteria, 0, body), searchH, 0xFFFFFFFF)); ec != ecAccessDenied {
		t.Errorf("without read rights on the Inbox ec = %#x, want ecAccessDenied", ec)
	}
	if err := st.ModifyPermissions(int64(mapi.PrivateFIDInbox), false, []objectstore.PermissionChange{{Op: objectstore.PermAdd, Username: delegate, Rights: mapi.RightsReviewer}}); err != nil {
		t.Fatal(err)
	}
	if ec := ropResultEC(t, mustDispatch(sess, toROPRequest(ropSetSearchCriteria, 0, body), searchH, 0xFFFFFFFF)); ec != ecSuccess {
		t.Errorf("with both rights ec = %#x, want success", ec)
	}
}
