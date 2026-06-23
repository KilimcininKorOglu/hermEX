package webmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestBuildFolderViewsTree proves the sidebar list is emitted in tree order —
// each parent immediately followed by its descendants — with the right nesting
// Depth, while top-level folders keep the store's own order (so the well-known
// folders are not re-sorted alphabetically). The store order here is deliberately
// NOT pre-grouped (the children trail unrelated roots) to prove the regrouping.
func TestBuildFolderViewsTree(t *testing.T) {
	us := int64(mapi.PrivateFIDUnassignedStart)
	archive, projects, y2025, y2026 := us+1, us+2, us+3, us+4
	p := func(id int64) *int64 { return &id }

	folders := []objectstore.FolderInfo{
		{ID: int64(mapi.PrivateFIDInbox), DisplayName: "Inbox"},
		{ID: archive, DisplayName: "Archive"},
		{ID: projects, DisplayName: "Projects"},
		{ID: y2025, ParentID: p(archive), DisplayName: "2025"},
		{ID: y2026, ParentID: p(archive), DisplayName: "2026"},
	}

	views := buildFolderViews(folders)

	want := []struct {
		path  string
		depth int
	}{
		{"INBOX", 0}, // a root "Inbox" normalizes to the IMAP name
		{"Archive", 0},
		{"Archive/2025", 1},
		{"Archive/2026", 1},
		{"Projects", 0},
	}
	if len(views) != len(want) {
		t.Fatalf("got %d views, want %d", len(views), len(want))
	}
	for i, w := range want {
		if views[i].Path != w.path || views[i].Depth != w.depth {
			t.Errorf("view[%d] = {path %q, depth %d}, want {path %q, depth %d}",
				i, views[i].Path, views[i].Depth, w.path, w.depth)
		}
	}

	// IsUser gates rename/delete: a built-in folder is off-limits, a user folder is not.
	if views[0].IsUser {
		t.Errorf("built-in Inbox marked as a user folder")
	}
	if !views[1].IsUser {
		t.Errorf("user folder Archive not marked IsUser")
	}

	// A nested folder must be resolvable by its full path (so a sidebar click opens it).
	if id, ok := resolveFolder(folders, "Archive/2026"); !ok || id != y2026 {
		t.Errorf("resolveFolder(Archive/2026) = %d, %v; want %d, true", id, ok, y2026)
	}
}

// TestBuildFolderViewsOrphan proves a folder whose parent is absent from the set
// is surfaced as a root (depth 0) rather than dropped, so a corrupt parent link
// can never hide a folder from the sidebar.
func TestBuildFolderViewsOrphan(t *testing.T) {
	us := int64(mapi.PrivateFIDUnassignedStart)
	missing := us + 99
	orphan := us + 1
	p := func(id int64) *int64 { return &id }

	views := buildFolderViews([]objectstore.FolderInfo{
		{ID: orphan, ParentID: p(missing), DisplayName: "Lost"},
	})
	if len(views) != 1 {
		t.Fatalf("got %d views, want 1", len(views))
	}
	if views[0].Path != "Lost" || views[0].Depth != 0 {
		t.Errorf("orphan view = {path %q, depth %d}, want {Lost, 0}", views[0].Path, views[0].Depth)
	}
}

// TestSidebarIndentsNestedFolders proves the rendered sidebar carries the depth
// marker the CSS indents on, so a nested folder reads as a child and not a peer.
// It guards the html/template CSS-attribute context, which would silently blank
// the value (ZgotmplZ) if the marker were not a plain integer.
func TestSidebarIndentsNestedFolders(t *testing.T) {
	path := emptyMailbox(t)
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.CreateFolder(nil, "Parent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFolder(&pid, "Child"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	code, body := get(t, c, ts.URL+"/mail")
	if code != 200 {
		t.Fatalf("/mail = %d", code)
	}
	if !strings.Contains(body, "Child") {
		t.Errorf("nested folder Child missing from the sidebar:\n%s", body)
	}
	if !strings.Contains(body, "--depth:1") {
		t.Errorf("child folder not indented (no --depth:1 marker) — html/template may have blanked it")
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Errorf("html/template rejected the depth marker (ZgotmplZ in output)")
	}
}
