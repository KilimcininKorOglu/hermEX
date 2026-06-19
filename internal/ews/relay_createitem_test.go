package ews

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
)

// TestCreateItemSendRelaysExternal proves a SendOnly to a foreign-domain
// recipient is queued for outbound relay rather than dropped: with a spool set,
// the external recipient leaves through the relay carrying the sender's address.
func TestCreateItemSendRelaysExternal(t *testing.T) {
	dir := t.TempDir()
	if st, err := objectstore.Open(dir); err != nil {
		t.Fatal(err)
	} else {
		st.Close()
	}
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	srv.Spool = sp

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	_, out := soapPost(t, ts, createItemReq("SendOnly", "bob@external.test", "Out via EWS", "hi bob"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	due, err := sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Recipient != "bob@external.test" {
		t.Fatalf("relay spool = %v, want bob@external.test queued for relay", due)
	}
	if due[0].From != testUser {
		t.Errorf("relay envelope From = %q, want %q", due[0].From, testUser)
	}
}
