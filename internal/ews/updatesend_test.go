package ews

import (
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

// spooledServer returns a server whose external mail is queued in a relay spool
// the test can read back.
func spooledServer(t *testing.T) (*httptest.Server, string, *relay.Spool) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sp.Close() })
	srv.Spool = sp
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, dir, sp
}

// updateSendReq builds an UpdateItem that rewrites the recipients and sends.
func updateSendReq(disposition, itemID, to string) string {
	return wrapRequest(`<UpdateItem ConflictResolution="AutoResolve" MessageDisposition="` + disposition +
		`" xmlns="` + nsMessages + `">` +
		`<ItemChanges><t:ItemChange xmlns:t="` + nsTypes + `">` +
		`<t:ItemId Id="` + itemID + `"/>` +
		`<t:Updates><t:SetItemField><t:FieldURI FieldURI="message:ToRecipients"/>` +
		`<t:Message><t:ToRecipients><t:Mailbox><t:EmailAddress>` + to + `</t:EmailAddress></t:Mailbox></t:ToRecipients></t:Message>` +
		`</t:SetItemField></t:Updates>` +
		`</t:ItemChange></ItemChanges></UpdateItem>`)
}

// TestUpdateItemSendAndSaveCopySends is the second half of the defect: a client
// that composes by saving a draft and then updating it with a send disposition
// used to be told Success while nothing was transmitted and the draft stayed
// where it was.
func TestUpdateItemSendAndSaveCopySends(t *testing.T) {
	ts, dir, sp := spooledServer(t)
	itemID := createDraft(t, ts, "old@external.test")

	_, out := soapPost(t, ts, updateSendReq("SendAndSaveCopy", itemID, "new@external.test"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem not success: %s", out)
	}

	due, err := sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	// The recipient the same request wrote is the one that receives it, not the
	// one the draft was saved with.
	if len(due) != 1 || due[0].Recipient != "new@external.test" {
		t.Fatalf("relay spool = %v, want new@external.test", due)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDDraft)); n != 0 {
		t.Errorf("drafts = %d, want 0 (the sent draft is consumed)", n)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDSentItems)); n != 1 {
		t.Errorf("sent items = %d, want the filed copy", n)
	}
}

// TestUpdateItemSendOnlyFilesNoCopy covers the other sending disposition: the
// message goes out and nothing is kept.
func TestUpdateItemSendOnlyFilesNoCopy(t *testing.T) {
	ts, dir, sp := spooledServer(t)
	itemID := createDraft(t, ts, "old@external.test")

	_, out := soapPost(t, ts, updateSendReq("SendOnly", itemID, "new@external.test"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem not success: %s", out)
	}
	if due, _ := sp.Claim(time.Now(), 10); len(due) != 1 {
		t.Fatalf("relay spool = %v, want one queued message", due)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDDraft)); n != 0 {
		t.Errorf("drafts = %d, want 0", n)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("sent items = %d, want 0 for SendOnly", n)
	}
}

// TestUpdateItemSaveOnlyDoesNotSend keeps the default disposition where it was:
// an edit is an edit, and the message stays a draft.
func TestUpdateItemSaveOnlyDoesNotSend(t *testing.T) {
	ts, dir, sp := spooledServer(t)
	itemID := createDraft(t, ts, "old@external.test")

	_, out := soapPost(t, ts, updateSendReq("SaveOnly", itemID, "new@external.test"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem not success: %s", out)
	}
	if due, _ := sp.Claim(time.Now(), 10); len(due) != 0 {
		t.Errorf("SaveOnly queued %v", due)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDDraft)); n != 1 {
		t.Errorf("drafts = %d, want the edited draft still there", n)
	}
}

// TestUpdateItemRefusesAnUnknownDisposition keeps an unrecognised value from
// being read as SaveOnly, which would swallow a send the client asked for.
func TestUpdateItemRefusesAnUnknownDisposition(t *testing.T) {
	ts, dir, sp := spooledServer(t)
	itemID := createDraft(t, ts, "old@external.test")

	_, out := soapPost(t, ts, updateSendReq("SendLater", itemID, "new@external.test"), true)
	if !strings.Contains(out, "ErrorInvalidRequest") {
		t.Fatalf("an unknown disposition answered %s", out)
	}
	if due, _ := sp.Claim(time.Now(), 10); len(due) != 0 {
		t.Errorf("the refused request queued %v", due)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDDraft)); n != 1 {
		t.Errorf("drafts = %d, want the untouched draft", n)
	}
}

// TestUpdateItemWithoutRecipientsIsNotSent keeps a draft that names nobody from
// being consumed by a send that could not happen.
func TestUpdateItemWithoutRecipientsIsNotSent(t *testing.T) {
	ts, dir, _ := spooledServer(t)
	itemID := createDraft(t, ts, "old@external.test")

	req := wrapRequest(`<UpdateItem ConflictResolution="AutoResolve" MessageDisposition="SendAndSaveCopy" xmlns="` + nsMessages + `">` +
		`<ItemChanges><t:ItemChange xmlns:t="` + nsTypes + `">` +
		`<t:ItemId Id="` + itemID + `"/>` +
		`<t:Updates><t:SetItemField><t:FieldURI FieldURI="message:ToRecipients"/>` +
		`<t:Message><t:ToRecipients/></t:Message></t:SetItemField></t:Updates>` +
		`</t:ItemChange></ItemChanges></UpdateItem>`)
	_, out := soapPost(t, ts, req, true)
	if !strings.Contains(out, "ErrorInvalidRecipients") {
		t.Fatalf("a recipientless send answered %s", out)
	}
	if n := folderCount(t, dir, int64(mapi.PrivateFIDDraft)); n != 1 {
		t.Errorf("drafts = %d, want the draft kept after a refused send", n)
	}
}
