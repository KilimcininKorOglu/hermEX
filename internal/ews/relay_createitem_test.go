package ews

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
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
		mustCloseStore(t, st)
	}
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCloser(t, "relay spool", sp)
	srv.Spool = sp

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// A spoofed From must be refused without spooling anything for relay.
	forgedReq := wrapRequest(`<CreateItem MessageDisposition="SendAndSaveCopy" xmlns="` + nsMessages + `">` +
		`<Items><t:Message xmlns:t="` + nsTypes + `">` +
		`<t:Subject>forged</t:Subject><t:Body BodyType="Text">no</t:Body>` +
		`<t:ToRecipients><t:Mailbox><t:EmailAddress>bob@external.test</t:EmailAddress></t:Mailbox></t:ToRecipients>` +
		`<t:From><t:Mailbox><t:EmailAddress>victim@hermex.test</t:EmailAddress></t:Mailbox></t:From>` +
		`</t:Message></Items></CreateItem>`)
	resp, out := soapPost(t, ts, forgedReq, true)
	wantEq(t, "forged status", resp.StatusCode, http.StatusOK)
	wantContains(t, "forged sender", out, "ErrorSendAsDenied")
	due, err := sp.Claim(time.Now(), 10)
	mustNoErr(t, "claim spool after refusal", err)
	wantEq(t, "spool count after refusal", len(due), 0)

	resp, out = soapPost(t, ts, createItemReq("SendOnly", "bob@external.test", "Out via EWS", "hi bob"), true)
	wantEq(t, "relay SendOnly status", resp.StatusCode, http.StatusOK)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	due, err = sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Recipient != "bob@external.test" {
		t.Fatalf("relay spool = %v, want bob@external.test queued for relay", due)
	}
	if due[0].From != testUser {
		t.Errorf("relay envelope From = %q, want %q", due[0].From, testUser)
	}

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox after SendOnly", err)
	defer mustCloseStore(t, st)
	wantEq(t, "Sent Items count after SendOnly", len(mustListMessages(t, st, int64(mapi.PrivateFIDSentItems))), 0)
}
