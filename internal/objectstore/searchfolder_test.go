package objectstore

import (
	"errors"
	"slices"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// searchStore opens a fresh mailbox and creates a search folder under Finder.
func searchStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(t.TempDir())
	mustNoErr(t, "open", err)
	t.Cleanup(func() { s.Close() })
	fid, err := s.CreateSearchFolder(int64(mapi.PrivateFIDFinder), "Invoices")
	mustNoErr(t, "create search folder", err)
	return s, fid
}

// deliver appends a message with subject to folder and returns its store id.
func deliver(t *testing.T, s *Store, folder int64, subject string) int64 {
	t.Helper()
	raw := []byte("From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: " + subject +
		"\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nbody\r\n")
	return mustAppendMessage(t, s, folder, raw, time.Now(), 0).ID
}

func searchIDs(t *testing.T, s *Store, fid int64) []int64 {
	t.Helper()
	ids, err := s.SearchFolderMessageIDs(fid)
	mustNoErr(t, "search folder ids", err)
	return ids
}

// TestRunningSearchFollowsItsScope proves a running search holds exactly the
// matching messages of its scope, sees a message delivered after it was set,
// and drops a message deleted after it was set.
func TestRunningSearchFollowsItsScope(t *testing.T) {
	s, fid := searchStore(t)
	inbox, sent := int64(mapi.PrivateFIDInbox), int64(mapi.PrivateFIDSentItems)
	hit := deliver(t, s, inbox, "Invoice 12")
	deliver(t, s, inbox, "Lunch")
	deliver(t, s, sent, "Invoice sent elsewhere")
	res := RuleSubjectContains("invoice")
	mustNoErr(t, "set criteria", s.SetSearchCriteria(fid, SearchCriteria{Restriction: &res, Scope: []int64{inbox}, Flags: mapi.SearchRestart}))

	if got := searchIDs(t, s, fid); !slices.Equal(got, []int64{hit}) {
		t.Fatalf("results = %v, want [%d]", got, hit)
	}
	later := deliver(t, s, inbox, "Invoice 13")
	if got := searchIDs(t, s, fid); !slices.Equal(got, []int64{hit, later}) {
		t.Fatalf("after delivery results = %v, want [%d %d]", got, hit, later)
	}
	mustNoErr(t, "delete", s.DeleteObject(hit))
	if got := searchIDs(t, s, fid); !slices.Equal(got, []int64{later}) {
		t.Fatalf("after delete results = %v, want [%d]", got, later)
	}
	props, err := s.FolderComputedProps(fid)
	mustNoErr(t, "computed props", err)
	if v, _ := props.Get(mapi.PrContentCount); v != int32(1) {
		t.Errorf("PidTagContentCount = %v, want 1", v)
	}
}

// TestRecursiveSearchLooksBelowItsScope proves only a recursive search finds a
// match in a subfolder of its scope folder.
func TestRecursiveSearchLooksBelowItsScope(t *testing.T) {
	s, fid := searchStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	sub := mustCreateFolder(t, s, &inbox, "Clients")
	deep := deliver(t, s, sub, "Invoice deep")
	res := RuleSubjectContains("invoice")
	mustNoErr(t, "set shallow", s.SetSearchCriteria(fid, SearchCriteria{Restriction: &res, Scope: []int64{inbox}, Flags: mapi.SearchRestart}))
	if got := searchIDs(t, s, fid); len(got) != 0 {
		t.Fatalf("shallow results = %v, want none", got)
	}
	mustNoErr(t, "set recursive", s.SetSearchCriteria(fid, SearchCriteria{Flags: mapi.SearchRestart | mapi.SearchRecursive}))
	if got := searchIDs(t, s, fid); !slices.Equal(got, []int64{deep}) {
		t.Fatalf("recursive results = %v, want [%d]", got, deep)
	}
	c, state, err := s.GetSearchCriteria(fid)
	mustNoErr(t, "get criteria", err)
	if !slices.Equal(c.Scope, []int64{inbox}) || c.Restriction == nil {
		t.Errorf("criteria = %+v, want the kept restriction and scope", c)
	}
	wantEq(t, "state", state, mapi.SearchStateRunning|mapi.SearchStateRecursive)
}

// TestStaticSearchKeepsItsSnapshot proves a static search lists what matched
// when it was restarted, not what arrives later, and a stopped search lists
// nothing.
func TestStaticSearchKeepsItsSnapshot(t *testing.T) {
	s, fid := searchStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	first := deliver(t, s, inbox, "Invoice 1")
	res := RuleSubjectContains("invoice")
	mustNoErr(t, "set static", s.SetSearchCriteria(fid, SearchCriteria{Restriction: &res, Scope: []int64{inbox}, Flags: mapi.SearchRestart | mapi.SearchStatic}))
	deliver(t, s, inbox, "Invoice 2")
	if got := searchIDs(t, s, fid); !slices.Equal(got, []int64{first}) {
		t.Fatalf("static results = %v, want [%d]", got, first)
	}
	mustNoErr(t, "stop", s.SetSearchCriteria(fid, SearchCriteria{Flags: mapi.SearchStop}))
	if got := searchIDs(t, s, fid); len(got) != 0 {
		t.Fatalf("stopped results = %v, want none", got)
	}
}

// TestSearchCriteriaRefusals proves the refusals a client can meet: criteria on
// a folder that is not a search folder, a partial change before any criteria
// exist, and a scope that holds the search folder itself.
func TestSearchCriteriaRefusals(t *testing.T) {
	s, fid := searchStore(t)
	res := RuleSubjectContains("x")
	inbox := int64(mapi.PrivateFIDInbox)
	if err := s.SetSearchCriteria(inbox, SearchCriteria{Restriction: &res, Scope: []int64{inbox}}); !errors.Is(err, ErrNotSearchFolder) {
		t.Errorf("criteria on the Inbox: %v, want ErrNotSearchFolder", err)
	}
	if err := s.SetSearchCriteria(fid, SearchCriteria{Scope: []int64{inbox}, Flags: mapi.SearchRestart}); !errors.Is(err, ErrSearchNotInitialized) {
		t.Errorf("scope without a restriction: %v, want ErrSearchNotInitialized", err)
	}
	if c, state, err := s.GetSearchCriteria(fid); err != nil || c.Restriction != nil || state != 0 {
		t.Errorf("uninitialized criteria = %+v, %#x, %v", c, state, err)
	}
	if err := s.SetSearchCriteria(fid, SearchCriteria{Restriction: &res, Scope: []int64{int64(mapi.PrivateFIDRoot)}}); !errors.Is(err, ErrSearchScope) {
		t.Errorf("scope over the search folder: %v, want ErrSearchScope", err)
	}
}

// TestSearchFolderChildrenListsSearchFolders proves a search folder is listed
// under its parent, where the user folder tree does not show it.
func TestSearchFolderChildrenListsSearchFolders(t *testing.T) {
	s, fid := searchStore(t)
	kids, err := s.SearchFolderChildren(int64(mapi.PrivateFIDFinder))
	mustNoErr(t, "children", err)
	if len(kids) != 1 || kids[0].ID != fid || kids[0].DisplayName != "Invoices" {
		t.Fatalf("children = %+v, want the Invoices search folder", kids)
	}
}
