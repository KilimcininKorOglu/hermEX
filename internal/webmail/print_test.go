package webmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// setImportance stamps PR_IMPORTANCE on a seeded message (by uid) for the print
// header test.
func setImportance(t *testing.T, path string, fid int64, uid uint32, value int32) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, err := st.MessageByUID(fid, uid)
	if err != nil {
		t.Fatal(err)
	}
	var pv mapi.PropertyValues
	pv.Set(mapi.PrImportance, value)
	if err := st.SetMessageProperties(m.ID, pv); err != nil {
		t.Fatal(err)
	}
}

// TestPrintNormalMessage checks the print view: a standalone document with the
// formatted header block and the body, auto-printing on load. A normal-priority
// message shows no Importance row.
func TestPrintNormalMessage(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Print me", "rcpt@hermex.test", "the printed body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	code, body := get(t, c, ts.URL+"/print?folder=INBOX&uid="+itoa(uid))
	if code != 200 {
		t.Fatalf("print status = %d", code)
	}
	for _, want := range []string{"<!DOCTYPE html>", "Print me", ">From<", ">Sent<", "the printed body", "window.print()"} {
		if !strings.Contains(body, want) {
			t.Errorf("print view missing %q", want)
		}
	}
	if strings.Contains(body, ">Importance<") {
		t.Error("normal-priority message should not render an Importance row")
	}
}

// TestPrintHighImportance checks that a high-priority message renders the
// Importance header row in the print view.
func TestPrintHighImportance(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Urgent", "", "body", 100, 0)
	setImportance(t, path, int64(mapi.PrivateFIDInbox), uid, int32(mapi.ImportanceHigh))
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, body := get(t, c, ts.URL+"/print?folder=INBOX&uid="+itoa(uid))
	if !strings.Contains(body, ">Importance<") || !strings.Contains(body, "High") {
		t.Error("high-priority message should render Importance: High in the print view")
	}
}

// TestReaderToolbarPopoutPrint checks that the reader toolbar offers pop-out
// (new window) and print links.
func TestReaderToolbarPopoutPrint(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "msg", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, body := get(t, c, ts.URL+"/message?folder=INBOX&uid="+itoa(uid))
	if !strings.Contains(body, `/print?folder=INBOX&uid=`+itoa(uid)) {
		t.Error("reader is missing the Print link")
	}
	if !strings.Contains(body, `target="_blank"`) {
		t.Error("reader is missing a pop-out (new window) link")
	}
}
