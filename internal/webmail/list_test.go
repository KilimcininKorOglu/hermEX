package webmail

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// rowCount counts rendered message rows in a mail-page body.
func rowCount(body string) int { return strings.Count(body, `id="msg-`) }

// firstMsgID returns the UID-bearing id of the first rendered row (e.g. "msg-2").
func firstMsgID(body string) string {
	i := strings.Index(body, `id="msg-`)
	if i < 0 {
		return ""
	}
	rest := body[i+len(`id="`):]
	before, _, ok := strings.Cut(rest, "\"")
	if !ok {
		return ""
	}
	return before
}

// TestSortMessages checks the comparator sorts on the real typed field for every
// sort key, in both directions, with a deterministic UID tiebreak.
func TestSortMessages(t *testing.T) {
	mk := func(uid uint32, date, size int64, sender, subject string, flags int64) objectstore.MessageInfo {
		return objectstore.MessageInfo{UID: uid, InternalDate: time.Unix(date, 0), Size: size, Sender: sender, Subject: subject, Flags: flags}
	}
	// m1 oldest/smallest/"carol"/"banana"/plain, m2 newest/largest/"alice"/"apple"/read,
	// m3 middle/"bob"/"cherry"/flagged-but-unread.
	base := []objectstore.MessageInfo{
		mk(1, 100, 100, "carol", "banana", 0),
		mk(2, 110, 300, "alice", "apple", objectstore.FlagSeen),
		mk(3, 105, 200, "bob", "cherry", objectstore.FlagFlagged),
	}
	uids := func(ms []objectstore.MessageInfo) []uint32 {
		out := make([]uint32, len(ms))
		for i, m := range ms {
			out[i] = m.UID
		}
		return out
	}
	cases := []struct {
		key, dir string
		want     []uint32
	}{
		{"date", "asc", []uint32{1, 3, 2}},
		{"date", "desc", []uint32{2, 3, 1}},
		{"from", "asc", []uint32{2, 3, 1}},
		{"from", "desc", []uint32{1, 3, 2}},
		{"subject", "asc", []uint32{2, 1, 3}},
		{"subject", "desc", []uint32{3, 1, 2}},
		{"size", "asc", []uint32{1, 3, 2}},
		{"size", "desc", []uint32{2, 3, 1}},
		{"flag", "asc", []uint32{1, 2, 3}},  // unflagged (uid asc) then flagged
		{"flag", "desc", []uint32{3, 2, 1}}, // flagged then unflagged (uid desc)
		{"read", "asc", []uint32{1, 3, 2}},  // unread (uid asc) then read
		{"read", "desc", []uint32{2, 3, 1}}, // read then unread (uid desc)
	}
	for _, c := range cases {
		ms := slices.Clone(base)
		sortMessages(ms, c.key, c.dir)
		if got := uids(ms); !slices.Equal(got, c.want) {
			t.Errorf("sort %s/%s = %v, want %v", c.key, c.dir, got, c.want)
		}
	}
}

// TestMailSortApplies checks the handler wires the sort/dir params into the
// pipeline: flipping the direction flips which message lists first.
func TestMailSortApplies(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	seedMsg(t, path, inbox, "old", "", "b", 100, 0) // uid 1, older
	seedMsg(t, path, inbox, "new", "", "b", 200, 0) // uid 2, newer
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if _, b := get(t, c, ts.URL+"/mail?folder=INBOX&sort=date&dir=desc"); firstMsgID(b) != "msg-2" {
		t.Errorf("date desc should list the newest (msg-2) first, got %q", firstMsgID(b))
	}
	if _, b := get(t, c, ts.URL+"/mail?folder=INBOX&sort=date&dir=asc"); firstMsgID(b) != "msg-1" {
		t.Errorf("date asc should list the oldest (msg-1) first, got %q", firstMsgID(b))
	}
}

// TestMailIconColumns checks the icon column: an unread dot for unseen rows, a
// flag for flagged rows, a paperclip only for a real (non-inline) attachment, and
// the high/low importance markers. Title attributes are counted, which pins each
// icon to exactly the rows that should carry it.
func TestMailIconColumns(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	app := func(raw string, flags int64) {
		if _, err := st.AppendMessage(inbox, []byte(raw), time.Unix(100, 0), flags); err != nil {
			t.Fatal(err)
		}
	}
	app("From: a@x.test\r\nSubject: plain\r\n\r\nbody", 0)                                                     // unread, no icons but the dot
	app("From: a@x.test\r\nSubject: flagged\r\n\r\nbody", int64(objectstore.FlagSeen|objectstore.FlagFlagged)) // read + flagged
	app("From: a@x.test\r\nSubject: att\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nhi\r\n--B\r\nContent-Type: application/pdf; name=\"r.pdf\"\r\nContent-Disposition: attachment; filename=\"r.pdf\"\r\n\r\nDATA\r\n--B--\r\n", 0)
	app("From: a@x.test\r\nSubject: inline\r\nMIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=B\r\n\r\n--B\r\nContent-Type: text/html\r\n\r\n<img src=\"cid:x\">\r\n--B\r\nContent-Type: image/png\r\nContent-ID: <x>\r\nContent-Disposition: inline\r\n\r\nPNGDATA\r\n--B--\r\n", 0)
	app("From: a@x.test\r\nSubject: high\r\nX-Priority: 1\r\n\r\nbody", 0)  // high importance
	app("From: a@x.test\r\nSubject: low\r\nImportance: low\r\n\r\nbody", 0) // low importance
	st.Close()

	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	_, b := get(t, c, ts.URL+"/mail?folder=INBOX")

	count := func(s string) int { return strings.Count(b, s) }
	// 5 of the 6 messages are unread (only the flagged one is read).
	if n := count(`title="Unread"`); n != 5 {
		t.Errorf("unread dots = %d, want 5", n)
	}
	if n := count(`title="Flagged"`); n != 1 {
		t.Errorf("flag icons = %d, want 1", n)
	}
	// Paperclip only on the real attachment, NOT on the inline-cid-only message.
	if n := count(`title="Has attachment"`); n != 1 {
		t.Errorf("paperclips = %d, want 1 (inline-only must not paperclip)", n)
	}
	if n := count(`title="High importance"`); n != 1 {
		t.Errorf("high-importance markers = %d, want 1", n)
	}
	if n := count(`title="Low importance"`); n != 1 {
		t.Errorf("low-importance markers = %d, want 1", n)
	}
}

// TestMailDensity checks the row-density class is driven by the density param.
func TestMailDensity(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "a", "", "b", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if _, b := get(t, c, ts.URL+"/mail?folder=INBOX"); !strings.Contains(b, "density-compact") {
		t.Errorf("default density should render the compact class")
	}
	if _, b := get(t, c, ts.URL+"/mail?folder=INBOX&density=extended"); !strings.Contains(b, "density-extended") {
		t.Errorf("density=extended should render the extended class")
	}
}

// TestMailSavedDefaults checks saved preferences supply the list defaults when no
// URL parameter is present, and a URL parameter overrides the saved value.
func TestMailSavedDefaults(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "a", "", "b", 100, 0)
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(st, webmailSettings{Density: "extended", DefaultSort: "from", DefaultDir: "asc"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// No params → the saved density and sort apply.
	_, b := get(t, c, ts.URL+"/mail?folder=INBOX")
	if !strings.Contains(b, "density-extended") {
		t.Errorf("saved density (extended) should apply when no density param is given")
	}
	if !strings.Contains(b, "From ▲") {
		t.Errorf("saved default sort (from asc) should make From the active ascending column:\n%s", b)
	}
	// A URL param overrides the saved default.
	if _, b := get(t, c, ts.URL+"/mail?folder=INBOX&density=compact"); !strings.Contains(b, "density-compact") {
		t.Errorf("density=compact param should override the saved extended default")
	}
}

// TestSettingsSaveListPrefs checks the settings form persists the density and
// default sort order.
func TestSettingsSaveListPrefs(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	postForm(t, c, ts.URL+"/settings", url.Values{
		"action": {"save"}, "composeformat": {"html"},
		"density": {"extended"}, "defaultsort": {"from asc"},
	})
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg, err := loadSettings(st)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Density != "extended" {
		t.Errorf("density not persisted: %q", cfg.Density)
	}
	if cfg.DefaultSort != "from" || cfg.DefaultDir != "asc" {
		t.Errorf("default sort not persisted: %q %q", cfg.DefaultSort, cfg.DefaultDir)
	}
}

// TestMailUnreadFilter checks the unread-only filter hides read messages,
// composes with sort, leaves the folder counters unchanged (they are pre-filter),
// and drives the toolbar toggle.
func TestMailUnreadFilter(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	seedMsg(t, path, inbox, "u1", "", "b", 100, 0)                           // uid1 unread, oldest
	seedMsg(t, path, inbox, "r1", "", "b", 101, int64(objectstore.FlagSeen)) // uid2 read
	seedMsg(t, path, inbox, "u2", "", "b", 102, 0)                           // uid3 unread, newest
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if _, b := get(t, c, ts.URL+"/mail?folder=INBOX"); rowCount(b) != 3 {
		t.Errorf("all view rows = %d, want 3", rowCount(b))
	}

	_, b := get(t, c, ts.URL+"/mail?folder=INBOX&filter=unread")
	if rowCount(b) != 2 {
		t.Errorf("unread view rows = %d, want 2", rowCount(b))
	}
	if strings.Contains(b, `id="msg-2"`) {
		t.Errorf("unread view must hide the read message (msg-2)")
	}
	// Counters describe the folder, not the filtered view, so they stay put.
	if !strings.Contains(b, "3 total, 2 unread") {
		t.Errorf("counters must stay pre-filter (3 total, 2 unread):\n%s", b)
	}
	// The toggle is active in the unread view.
	if !strings.Contains(b, "filter-toggle active") {
		t.Errorf("unread view should mark the filter toggle active")
	}

	// Filter composes with sort: oldest unread first under date-asc.
	if _, b := get(t, c, ts.URL+"/mail?folder=INBOX&filter=unread&sort=date&dir=asc"); firstMsgID(b) != "msg-1" {
		t.Errorf("unread + date-asc should list the oldest unread (msg-1) first, got %q", firstMsgID(b))
	}

	// The all view offers a link into the unread filter.
	if _, all := get(t, c, ts.URL+"/mail?folder=INBOX"); !strings.Contains(all, "filter=unread") {
		t.Errorf("all view should offer a link to the unread filter")
	}
}

// TestMailSortHeaders checks the active column shows its direction arrow and the
// header links carry the other params and reset the page.
func TestMailSortHeaders(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "a", "", "b", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, b := get(t, c, ts.URL+"/mail?folder=INBOX&sort=from&dir=asc")
	if !strings.Contains(b, "sortcol active") || !strings.Contains(b, "▲") {
		t.Errorf("sorting by From should mark its header active with an ascending arrow:\n%s", b)
	}
	if !strings.Contains(b, "filter=all") || !strings.Contains(b, "page=1") {
		t.Errorf("sort header links must carry filter and reset to page 1")
	}
}

// TestMailPagination checks the list is sliced into pages of pageSize, newest
// first (default date-descending), with a pager that links to the adjacent page.
func TestMailPagination(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	const total = pageSize + 5 // two pages: 50 + 5
	for i := range total {
		// Increasing received time → message i+1 (uid i+1) is newer than its predecessors.
		seedMsg(t, path, inbox, "m", "", "body", int64(1000+i), 0)
	}
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// Page 1: the newest pageSize messages (uids total..6); the pager shows page 1 of 2.
	_, p1 := get(t, c, ts.URL+"/mail?folder=INBOX")
	if n := rowCount(p1); n != pageSize {
		t.Errorf("page 1 row count = %d, want %d", n, pageSize)
	}
	if !strings.Contains(p1, `id="msg-55"`) || !strings.Contains(p1, `id="msg-6"`) {
		t.Errorf("page 1 missing the newest messages (msg-55 / msg-6)")
	}
	if strings.Contains(p1, `id="msg-5"`) || strings.Contains(p1, `id="msg-1"`) {
		t.Errorf("page 1 leaked an oldest message that belongs on page 2")
	}
	if !strings.Contains(p1, "Page 1 of 2") || !strings.Contains(p1, "page=2") {
		t.Errorf("page 1 pager wrong:\n%s", p1)
	}

	// Page 2: the 5 oldest messages (uids 5..1); the pager links back to page 1.
	_, p2 := get(t, c, ts.URL+"/mail?folder=INBOX&sort=date&dir=desc&filter=all&page=2")
	if n := rowCount(p2); n != 5 {
		t.Errorf("page 2 row count = %d, want 5", n)
	}
	if !strings.Contains(p2, `id="msg-1"`) || !strings.Contains(p2, `id="msg-5"`) {
		t.Errorf("page 2 missing the oldest messages (msg-1 / msg-5)")
	}
	if strings.Contains(p2, `id="msg-6"`) {
		t.Errorf("page 2 leaked a page-1 message (msg-6)")
	}
	if !strings.Contains(p2, "Page 2 of 2") || !strings.Contains(p2, "page=1") {
		t.Errorf("page 2 pager wrong:\n%s", p2)
	}
}

// TestMailPageOutOfRange checks an out-of-range or zero page clamps to the valid
// range, and an empty folder renders the empty state with no pager.
func TestMailPageOutOfRange(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	for i := range pageSize + 5 {
		seedMsg(t, path, inbox, "m", "", "body", int64(1000+i), 0)
	}
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if _, b := get(t, c, ts.URL+"/mail?folder=INBOX&page=0"); rowCount(b) != pageSize || !strings.Contains(b, "Page 1 of 2") {
		t.Errorf("page=0 did not clamp to page 1 (rows=%d)", rowCount(b))
	}
	if _, b := get(t, c, ts.URL+"/mail?folder=INBOX&page=9999"); rowCount(b) != 5 || !strings.Contains(b, "Page 2 of 2") {
		t.Errorf("page=9999 did not clamp to the last page (rows=%d)", rowCount(b))
	}

	// Empty folder: empty state, no pager.
	empty := emptyMailbox(t)
	ts2 := newTestServer(t, empty)
	c2 := authedClient(t, ts2)
	_, b := get(t, c2, ts2.URL+"/mail?folder=INBOX")
	if rowCount(b) != 0 || !strings.Contains(b, "No messages") {
		t.Errorf("empty folder should show the empty state")
	}
	if strings.Contains(b, "Page 1 of") {
		t.Errorf("empty folder should not render a pager")
	}
}

// TestMailCountersMatchSidebar locks the invariant that the toolbar counter (from
// the list pipeline) and the folder's sidebar badge (from CountMessages) agree:
// both must report the same total and unread for the current folder.
func TestMailCountersMatchSidebar(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	seedMsg(t, path, inbox, "a", "", "body", 1000, 0)                           // unread
	seedMsg(t, path, inbox, "b", "", "body", 1001, 0)                           // unread
	seedMsg(t, path, inbox, "c", "", "body", 1002, int64(objectstore.FlagSeen)) // read
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, body := get(t, c, ts.URL+"/mail?folder=INBOX")
	if !strings.Contains(body, "3 total, 2 unread") {
		t.Errorf("toolbar counter wrong; want '3 total, 2 unread':\n%s", body)
	}
	// Sidebar badge renders unread/total when there are unread messages.
	if !strings.Contains(body, `>2/3</span>`) {
		t.Errorf("sidebar INBOX badge wrong; want '2/3':\n%s", body)
	}
}
