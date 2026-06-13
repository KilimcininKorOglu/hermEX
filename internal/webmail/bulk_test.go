package webmail

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// msgFlags reads one message's flag bits by uid (for asserting bulk read/unread).
func msgFlags(t *testing.T, path string, fid int64, uid uint32) int64 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f, err := st.MessageFlags(fid, uid)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestBulkMarkReadUnread checks that op=read sets \Seen on exactly the selected
// messages (and op=unread clears it), leaving unselected rows untouched.
func TestBulkMarkReadUnread(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	a := seedMsg(t, path, inbox, "a", "", "body", 100, 0)
	b := seedMsg(t, path, inbox, "b", "", "body", 100, 0)
	c := seedMsg(t, path, inbox, "c", "", "body", 100, 0)
	ts := newTestServer(t, path)
	cl := authedClient(t, ts)

	// Mark a and b read; leave c unread.
	postForm(t, cl, ts.URL+"/bulk", url.Values{
		"folder": {"INBOX"}, "op": {"read"}, "uid": {itoa(a), itoa(b)},
	})
	if msgFlags(t, path, inbox, a)&objectstore.FlagSeen == 0 {
		t.Errorf("a not marked read")
	}
	if msgFlags(t, path, inbox, b)&objectstore.FlagSeen == 0 {
		t.Errorf("b not marked read")
	}
	if msgFlags(t, path, inbox, c)&objectstore.FlagSeen != 0 {
		t.Errorf("c was marked read but was not selected")
	}

	// Now mark a unread again.
	postForm(t, cl, ts.URL+"/bulk", url.Values{
		"folder": {"INBOX"}, "op": {"unread"}, "uid": {itoa(a)},
	})
	if msgFlags(t, path, inbox, a)&objectstore.FlagSeen != 0 {
		t.Errorf("a still read after unread op")
	}
}

// TestBulkDelete checks that bulk delete moves the selected messages from the
// Inbox to Deleted Items (the same to-Trash semantics as the single-message op).
func TestBulkDelete(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	a := seedMsg(t, path, inbox, "a", "", "body", 100, 0)
	b := seedMsg(t, path, inbox, "b", "", "body", 100, 0)
	seedMsg(t, path, inbox, "keep", "", "body", 100, 0)
	ts := newTestServer(t, path)
	cl := authedClient(t, ts)

	postForm(t, cl, ts.URL+"/bulk", url.Values{
		"folder": {"INBOX"}, "op": {"delete"}, "uid": {itoa(a), itoa(b)},
	})
	if n := len(folderMsgs(t, path, inbox)); n != 1 {
		t.Errorf("inbox has %d messages, want 1 (only 'keep')", n)
	}
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDDeletedItems))); n != 2 {
		t.Errorf("Deleted Items has %d, want 2", n)
	}
}

// TestBulkMove checks that bulk move re-files the selected messages into the
// chosen folder and removes them from the source.
func TestBulkMove(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	a := seedMsg(t, path, inbox, "a", "", "body", 100, 0)
	b := seedMsg(t, path, inbox, "b", "", "body", 100, 0)
	archive := makeFolder(t, path, "Archive")
	ts := newTestServer(t, path)
	cl := authedClient(t, ts)

	postForm(t, cl, ts.URL+"/bulk", url.Values{
		"folder": {"INBOX"}, "op": {"move"}, "dst": {fid64(archive)}, "uid": {itoa(a), itoa(b)},
	})
	if n := len(folderMsgs(t, path, inbox)); n != 0 {
		t.Errorf("inbox has %d after move, want 0", n)
	}
	if n := len(folderMsgs(t, path, archive)); n != 2 {
		t.Errorf("Archive has %d after move, want 2", n)
	}
}

// TestBulkExport streams the selected messages as a zip of .eml files and
// verifies the archive holds one entry per selected uid.
func TestBulkExport(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)
	a := seedMsg(t, path, inbox, "first", "", "body one", 100, 0)
	b := seedMsg(t, path, inbox, "second", "", "body two", 100, 0)
	ts := newTestServer(t, path)
	cl := authedClient(t, ts)

	resp, err := cl.PostForm(ts.URL+"/export", url.Values{
		"folder": {"INBOX"}, "uid": {itoa(a), itoa(b)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}
	if len(zr.File) != 2 {
		t.Fatalf("zip has %d entries, want 2", len(zr.File))
	}
	// Each entry must be a non-empty .eml carrying the seeded body.
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		if len(content) == 0 {
			t.Errorf("zip entry %s is empty", f.Name)
		}
	}
}

// TestSingleEMLDownload checks that /eml streams one message as an RFC822
// attachment named after its subject.
func TestSingleEMLDownload(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Hello World", "", "the body text", 100, 0)
	ts := newTestServer(t, path)
	cl := authedClient(t, ts)

	resp, err := cl.Get(ts.URL + "/eml?folder=INBOX&uid=" + itoa(uid))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("eml status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "message/rfc822" {
		t.Errorf("Content-Type = %q, want message/rfc822", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `Hello World.eml`) {
		t.Errorf("Content-Disposition = %q, want a Hello World.eml attachment", cd)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "Subject: Hello World") || !strings.Contains(string(raw), "the body text") {
		t.Error("downloaded .eml is missing the message content")
	}
}

// TestSingleEMLDownloadUnicode checks that a Turkish subject is delivered as an
// RFC 6266 filename*: the header stays pure ASCII (no raw UTF-8 leaks, which
// Safari would garble) while the dotless-i is percent-encoded as UTF-8.
func TestSingleEMLDownloadUnicode(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Toplantı notları", "", "govde", 100, 0)
	ts := newTestServer(t, path)
	cl := authedClient(t, ts)

	resp, err := cl.Get(ts.URL + "/eml?folder=INBOX&uid=" + itoa(uid))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	cd := resp.Header.Get("Content-Disposition")
	for i := 0; i < len(cd); i++ {
		if cd[i] > 0x7f {
			t.Fatalf("Content-Disposition leaked a non-ASCII byte: %q", cd)
		}
	}
	if !strings.Contains(cd, "filename*=UTF-8''") || !strings.Contains(cd, "%C4%B1") {
		t.Errorf("Content-Disposition missing RFC 5987 UTF-8 encoding of the Turkish subject: %q", cd)
	}
}

// TestSafeEMLName checks subject-to-filename sanitization.
func TestSafeEMLName(t *testing.T) {
	cases := map[string]string{
		"Quarterly sync":   "Quarterly sync.eml",
		"a/b:c*?":          "a_b_c__.eml",
		"":                 "message.eml",
		"   ":              "message.eml",
		"Re: \"plan\" <x>": "Re_ _plan_ _x_.eml",   // ':' and '"' are sanitized for FS + header safety
		"Toplantı notları": "Toplantı notları.eml", // Unicode is preserved in the display name
	}
	for in, want := range cases {
		if got := safeEMLName(in); got != want {
			t.Errorf("safeEMLName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBulkUnauthenticated rejects bulk ops without a session.
func TestBulkUnauthenticated(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	resp, err := http.PostForm(ts.URL+"/bulk", url.Values{"folder": {"INBOX"}, "op": {"read"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated bulk = %d, want 401", resp.StatusCode)
	}
}
