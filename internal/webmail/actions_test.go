package webmail

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// makeFolder creates a top-level user folder and returns its id.
func makeFolder(t *testing.T, path, name string) int64 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateFolder(nil, name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// fid64 formats a folder id for a form value.
func fid64(id int64) string { return strconv.FormatInt(id, 10) }

func action(t *testing.T, c *http.Client, base string, vals url.Values) int {
	t.Helper()
	code, _ := postForm(t, c, base+"/action", vals)
	return code
}

// TestActionMoveMessage checks move = re-file into dst + remove from src, with
// flags and internal date preserved (fresh uid in dst).
func TestActionMoveMessage(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "to move", "", "body", 100, objectstore.FlagSeen|objectstore.FlagFlagged)
	archive := makeFolder(t, path, "Archive")
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code := action(t, c, ts.URL, url.Values{
		"folder": {"INBOX"}, "uid": {itoa(uid)}, "op": {"move"}, "dst": {fid64(archive)},
	}); code != 200 {
		t.Fatalf("move = %d", code)
	}
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDInbox))); n != 0 {
		t.Errorf("source still has %d messages, want 0", n)
	}
	dst := folderMsgs(t, path, archive)
	if len(dst) != 1 {
		t.Fatalf("destination has %d, want 1", len(dst))
	}
	if dst[0].Flags&objectstore.FlagFlagged == 0 || dst[0].Flags&objectstore.FlagSeen == 0 {
		t.Errorf("flags not preserved on move: %d", dst[0].Flags)
	}
	if !dst[0].InternalDate.Equal(time.Unix(100, 0)) {
		t.Errorf("internal date not preserved: %v", dst[0].InternalDate)
	}
}

// TestActionCopyMessage checks copy leaves the source in place AND files a copy.
func TestActionCopyMessage(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "to copy", "", "body", 100, 0)
	archive := makeFolder(t, path, "Archive")
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code := action(t, c, ts.URL, url.Values{
		"folder": {"INBOX"}, "uid": {itoa(uid)}, "op": {"copy"}, "dst": {fid64(archive)},
	}); code != 200 {
		t.Fatalf("copy = %d", code)
	}
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDInbox))); n != 1 {
		t.Errorf("copy must keep the source, INBOX has %d want 1", n)
	}
	if n := len(folderMsgs(t, path, archive)); n != 1 {
		t.Errorf("copy must file a destination copy, Archive has %d want 1", n)
	}
}

// TestActionMoveSameFolderNoOp checks moving into the same folder does nothing.
func TestActionMoveSameFolderNoOp(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "stay", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	action(t, c, ts.URL, url.Values{
		"folder": {"INBOX"}, "uid": {itoa(uid)}, "op": {"move"}, "dst": {fid64(int64(mapi.PrivateFIDInbox))},
	})
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDInbox))); n != 1 {
		t.Errorf("self-move must be a no-op, INBOX has %d want 1", n)
	}
}

// TestActionJunk checks junk moves to Junk Email, and is a no-op when already there.
func TestActionJunk(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "spam", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	action(t, c, ts.URL, url.Values{"folder": {"INBOX"}, "uid": {itoa(uid)}, "op": {"junk"}})
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDInbox))); n != 0 {
		t.Errorf("junked message still in INBOX (%d)", n)
	}
	junk := folderMsgs(t, path, int64(mapi.PrivateFIDJunk))
	if len(junk) != 1 {
		t.Fatalf("Junk has %d, want 1", len(junk))
	}
	// Junk again while already in Junk → no-op.
	action(t, c, ts.URL, url.Values{"folder": {"Junk Email"}, "uid": {itoa(junk[0].UID)}, "op": {"junk"}})
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDJunk))); n != 1 {
		t.Errorf("junk-while-in-Junk must be a no-op, Junk has %d want 1", n)
	}
}

// TestActionRestore checks restore moves to Inbox from Deleted/Junk, and is
// rejected from anywhere else.
func TestActionRestore(t *testing.T) {
	path := emptyMailbox(t)
	d := seedMsg(t, path, int64(mapi.PrivateFIDDeletedItems), "trashed", "", "body", 100, 0)
	j := seedMsg(t, path, int64(mapi.PrivateFIDJunk), "junked", "", "body", 200, 0)
	keep := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "keeper", "", "body", 300, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	action(t, c, ts.URL, url.Values{"folder": {"Deleted Items"}, "uid": {itoa(d)}, "op": {"restore"}})
	action(t, c, ts.URL, url.Values{"folder": {"Junk Email"}, "uid": {itoa(j)}, "op": {"restore"}})
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDInbox))); n != 3 { // keeper + 2 restored
		t.Errorf("after restoring from Deleted+Junk, Inbox has %d, want 3", n)
	}
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDDeletedItems))); n != 0 {
		t.Errorf("Deleted still has %d after restore", n)
	}

	// Restore from Inbox is rejected and changes nothing.
	if code := action(t, c, ts.URL, url.Values{"folder": {"INBOX"}, "uid": {itoa(keep)}, "op": {"restore"}}); code != http.StatusBadRequest {
		t.Errorf("restore from Inbox = %d, want 400", code)
	}
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDInbox))); n != 3 {
		t.Errorf("rejected restore must not change Inbox, has %d", n)
	}
}

// TestActionMoveToNonMailRejected checks a move into a non-mail folder (Contacts)
// is refused and the message untouched.
func TestActionMoveToNonMailRejected(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "stay put", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code := action(t, c, ts.URL, url.Values{
		"folder": {"INBOX"}, "uid": {itoa(uid)}, "op": {"move"}, "dst": {fid64(int64(mapi.PrivateFIDContacts))},
	}); code != http.StatusBadRequest {
		t.Errorf("move to Contacts = %d, want 400", code)
	}
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDInbox))); n != 1 {
		t.Errorf("rejected move must leave the message, INBOX has %d want 1", n)
	}
}

// TestActionToggleKeepsIconColumns checks that a per-row flag toggle re-renders
// the row with its icon columns intact. The attachment paperclip and the
// importance marker come from a per-row object read (enrichIcons), not from the
// index row messageViewFrom maps, so the htmx single-row swap must run that same
// enrichment. Without it those icons silently vanish from the clicked row until a
// full reload — a regression invisible to the full-page list tests.
func TestActionToggleKeepsIconColumns(t *testing.T) {
	path := emptyMailbox(t)
	// A real attachment (no Content-ID) plus an Importance: High header, so the
	// row carries both per-row-read icons at once.
	raw := "From: sender@example.com\r\nTo: rcpt@example.com\r\n" +
		"Subject: report attached\r\nImportance: High\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--B\r\nContent-Type: application/pdf; name=\"r.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"r.pdf\"\r\n\r\n%PDF data\r\n--B--\r\n"
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(100, 0), 0)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	code, body := postForm(t, c, ts.URL+"/action", url.Values{
		"folder": {"INBOX"}, "uid": {itoa(info.UID)}, "op": {"toggleflag"},
	})
	if code != 200 {
		t.Fatalf("toggleflag = %d", code)
	}
	// The toggle set \Flagged; the re-rendered row reflects current state.
	if !strings.Contains(body, `title="Flagged"`) {
		t.Errorf("toggled row missing flag icon:\n%s", body)
	}
	// Icon columns that require a per-row object read must survive the swap.
	if !strings.Contains(body, `title="Has attachment"`) {
		t.Errorf("toggled row dropped the attachment paperclip (enrichIcons not run):\n%s", body)
	}
	if !strings.Contains(body, `title="High importance"`) {
		t.Errorf("toggled row dropped the importance marker (enrichIcons not run):\n%s", body)
	}
}

// TestActionFollowupFlag checks the six-color follow-up flag actions: applying a
// color renders the colored flag in the swapped row and sets the IMAP \Flagged
// bit; marking complete shows the check and clears \Flagged; clearing removes the
// flag entirely.
func TestActionFollowupFlag(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "flag me", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	flagged := func() int64 {
		return folderMsgs(t, path, int64(mapi.PrivateFIDInbox))[0].Flags & objectstore.FlagFlagged
	}

	// The list's flag menu posts via htmx; the HX-Request header makes /action
	// return the row partial (a plain post redirects to the reader instead).
	hxPost := func(vals url.Values) (int, string) {
		t.Helper()
		req, err := http.NewRequest("POST", ts.URL+"/action", strings.NewReader(vals.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Apply a red flag → colored flag icon in the row + \Flagged set.
	code, body := hxPost(url.Values{"folder": {"INBOX"}, "uid": {itoa(uid)}, "op": {"flag"}, "color": {"6"}})
	if code != 200 {
		t.Fatalf("flag = %d", code)
	}
	if !strings.Contains(body, `title="Flagged"`) || !strings.Contains(body, `class="i-flag flag-c6"`) {
		t.Errorf("flagged row missing the red flag icon:\n%s", body)
	}
	if flagged() == 0 {
		t.Error("applying a flag must set the IMAP \\Flagged bit")
	}

	// Mark complete → check icon, \Flagged cleared.
	_, body = hxPost(url.Values{"folder": {"INBOX"}, "uid": {itoa(uid)}, "op": {"flagcomplete"}})
	if !strings.Contains(body, `title="Flag complete"`) || strings.Contains(body, `title="Flagged"`) {
		t.Errorf("completed row should show the check, not a flag:\n%s", body)
	}
	if flagged() != 0 {
		t.Error("completing a flag must clear the IMAP \\Flagged bit")
	}

	// Clear → no flag and no check.
	_, body = hxPost(url.Values{"folder": {"INBOX"}, "uid": {itoa(uid)}, "op": {"flagnone"}})
	if strings.Contains(body, `title="Flagged"`) || strings.Contains(body, `title="Flag complete"`) {
		t.Errorf("cleared row should show no flag icon:\n%s", body)
	}
	if flagged() != 0 {
		t.Error("a cleared flag must not be \\Flagged")
	}

	// A plain (non-htmx) post — the reader's flag control — redirects to the message.
	noredir := *c
	noredir.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	rcode, _ := postForm(t, &noredir, ts.URL+"/action", url.Values{"folder": {"INBOX"}, "uid": {itoa(uid)}, "op": {"flag"}, "color": {"3"}})
	if rcode != http.StatusSeeOther {
		t.Errorf("reader (non-htmx) flag should redirect to the message (303), got %d", rcode)
	}
}

// TestActionUnauthenticated checks /action requires a session.
func TestActionUnauthenticated(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	code, _ := postForm(t, &http.Client{}, ts.URL+"/action", url.Values{
		"folder": {"INBOX"}, "uid": {"1"}, "op": {"junk"},
	})
	if code != http.StatusUnauthorized {
		t.Errorf("unauthenticated /action = %d, want 401", code)
	}
}
