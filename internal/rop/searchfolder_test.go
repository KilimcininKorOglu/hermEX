package rop

import (
	"slices"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// openFinder opens the Finder folder off logonH and returns its handle.
func openFinder(t *testing.T, sess *Session, logonH uint32) uint32 {
	t.Helper()
	h := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDFinder))), ropOpenFolder, "OpenFolder(Finder)", logonH, 0xFFFFFFFF)
	requireHandles(t, "OpenFolder(Finder)", h[1])
	return h[1]
}

// ownerFinder logs on as the owner of a fresh mailbox and opens its Finder.
func ownerFinder(t *testing.T, dir string) (*Session, uint32) {
	t.Helper()
	sess := NewSession(dir, nil, "")
	t.Cleanup(sess.Close)
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	return sess, openFinder(t, sess, logonH)
}

// createSearchFolderROP creates a search folder named name under parentH and returns its id and handle.
func createSearchFolderROP(t *testing.T, sess *Session, parentH uint32, name string) (int64, uint32) {
	t.Helper()
	resp, h := sess.Dispatch(toROPRequest(ropCreateFolder, 0, createFolderBody(uint8(folderTypeSearch), name, false)), []uint32{parentH, 0xFFFFFFFF})
	p := ropOK(t, resp, ropCreateFolder, "CreateFolder(search)")
	requireHandles(t, "CreateFolder(search)", h[1])
	return int64(mapi.EID(mustU64(t, p, "FolderId")).GCValue()), h[1]
}

// setCriteriaBody is a RopSetSearchCriteria request body.
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

// getCriteria sends RopGetSearchCriteria with its first ROP header carrying logonID.
func getCriteria(t *testing.T, sess *Session, folderH uint32, logonID, useUnicode uint8) searchCriteriaReplyROP {
	t.Helper()
	b := ext.NewPush(ext.FlagUTF16)
	b.Raw([]byte{ropGetSearchCriteria, logonID, 0, useUnicode, 1, 1})
	resp := dispatchOn(sess, b.Bytes(), folderH)
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
// Finder, give it criteria, read them back, and see matching messages.
func TestSearchFolderThroughROP(t *testing.T) {
	dir := provisionedDir(t)
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
// name taken by a folder of the other type is a duplicate even with OpenExisting.
func TestCreateSearchFolderRefusals(t *testing.T) {
	sess, finderH := ownerFinder(t, provisionedDir(t))
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

// TestSetSearchCriteriaRefusals proves refusal codes and unchanged state across
// invalid targets and scopes.
func TestSetSearchCriteriaRefusals(t *testing.T) {
	sess, finderH := ownerFinder(t, provisionedDir(t))
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

// TestGetSearchCriteriaIn8BitStrings proves 8-bit string restriction tags.
func TestGetSearchCriteriaIn8BitStrings(t *testing.T) {
	sess, finderH := ownerFinder(t, provisionedDir(t))
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

// TestDelegateSearchCriteriaRights proves delegate rights required on search and target folders.
func TestDelegateSearchCriteriaRights(t *testing.T) {
	dir := provisionedDir(t)
	const delegate = "delegate@hermex.test"
	owner, finderH := ownerFinder(t, dir)
	fid, searchOwnerH := createSearchFolderROP(t, owner, finderH, "Shared")
	requireHandles(t, "CreateFolder(owner search)", searchOwnerH)
	owner.Close()
	grantFolderPermission(t, dir, int64(mapi.PrivateFIDFinder), delegate, mapi.FrightsVisible)
	grantFolderPermission(t, dir, fid, delegate, mapi.FrightsVisible)

	sess, logonH := delegateLogon(t, dir, delegate)
	defer sess.Close()
	searchH := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(fid)))), ropOpenFolder, "OpenFolder(search)", logonH, 0xFFFFFFFF)[1]
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
	if c, state, err := st.GetSearchCriteria(fid); err != nil || c.Restriction != nil || state != 0 {
		t.Errorf("criteria after the refused sets = %+v state %#x (err %v), want none", c, state, err)
	}
	if err := st.ModifyPermissions(int64(mapi.PrivateFIDInbox), false, []objectstore.PermissionChange{{Op: objectstore.PermAdd, Username: delegate, Rights: mapi.RightsReviewer}}); err != nil {
		t.Fatal(err)
	}
	if ec := ropResultEC(t, mustDispatch(sess, toROPRequest(ropSetSearchCriteria, 0, body), searchH, 0xFFFFFFFF)); ec != ecSuccess {
		t.Errorf("with both rights ec = %#x, want success", ec)
	}
}
