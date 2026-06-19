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

// createDraft saves a draft (CreateItem SaveOnly) addressed to one recipient and
// returns its opaque ItemId, the handle a later SendItem addresses.
func createDraft(t *testing.T, ts *httptest.Server, to string) string {
	t.Helper()
	_, out := soapPost(t, ts, createItemReq("SaveOnly", to, "Draft via EWS", "queued for later"), true)
	m := itemIDRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no ItemId in CreateItem response: %s", out)
	}
	return m[1]
}

func sendItemReq(saveToFolder, itemID string) string {
	return wrapRequest(`<SendItem SaveItemToFolder="` + saveToFolder + `" xmlns="` + nsMessages + `">` +
		`<ItemIds><t:ItemId Id="` + itemID + `" xmlns:t="` + nsTypes + `"/></ItemIds>` +
		`</SendItem>`)
}

func sendItemReqSaved(saveToFolder, itemID, savedFolder string) string {
	return wrapRequest(`<SendItem SaveItemToFolder="` + saveToFolder + `" xmlns="` + nsMessages + `">` +
		`<ItemIds><t:ItemId Id="` + itemID + `" xmlns:t="` + nsTypes + `"/></ItemIds>` +
		`<SavedItemFolderId><t:DistinguishedFolderId Id="` + savedFolder + `" xmlns:t="` + nsTypes + `"/></SavedItemFolderId>` +
		`</SendItem>`)
}

// TestSendItemDeliversAndConsumesDraft proves the create-then-send flow: a saved
// draft is transmitted (loopback to the sender lands in the Inbox), a copy is
// filed to Sent Items (SaveItemToFolder=true), and the source draft is consumed —
// a sent message must not linger in Drafts looking unsent.
func TestSendItemDeliversAndConsumesDraft(t *testing.T) {
	ts, dir := seededWithMessage(t)
	itemID := createDraft(t, ts, testUser)

	_, out := soapPost(t, ts, sendItemReq("true", itemID), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("SendItem not success: %s", out)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	sent, _ := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	drafts, _ := st.ListMessages(int64(mapi.PrivateFIDDraft))
	if len(inbox) != 1 {
		t.Errorf("inbox = %d, want 1 (delivered to self)", len(inbox))
	}
	if len(sent) != 1 {
		t.Errorf("sent = %d, want 1 (SaveItemToFolder=true files a copy)", len(sent))
	}
	if len(drafts) != 0 {
		t.Errorf("drafts = %d, want 0 (sent draft consumed)", len(drafts))
	}
}

// TestSendItemNoSaveConsumesDraft confirms SaveItemToFolder=false transmits the
// draft, files no copy, and still consumes the source.
func TestSendItemNoSaveConsumesDraft(t *testing.T) {
	ts, dir := seededWithMessage(t)
	itemID := createDraft(t, ts, testUser)

	_, out := soapPost(t, ts, sendItemReq("false", itemID), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("SendItem not success: %s", out)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	sent, _ := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	drafts, _ := st.ListMessages(int64(mapi.PrivateFIDDraft))
	if len(inbox) != 1 {
		t.Errorf("inbox = %d, want 1 (delivered to self)", len(inbox))
	}
	if len(sent) != 0 {
		t.Errorf("sent = %d, want 0 (SaveItemToFolder=false files no copy)", len(sent))
	}
	if len(drafts) != 0 {
		t.Errorf("drafts = %d, want 0 (sent draft consumed)", len(drafts))
	}
}

// TestSendItemInvalidSaveSettings confirms the documented contradiction —
// SaveItemToFolder=false with a SavedItemFolderId — is rejected and the draft is
// left untouched.
func TestSendItemInvalidSaveSettings(t *testing.T) {
	ts, dir := seededWithMessage(t)
	itemID := createDraft(t, ts, testUser)

	_, out := soapPost(t, ts, sendItemReqSaved("false", itemID, "sentitems"), true)
	if !strings.Contains(out, "ErrorInvalidSendItemSaveSettings") {
		t.Errorf("want ErrorInvalidSendItemSaveSettings, got: %s", out)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if drafts, _ := st.ListMessages(int64(mapi.PrivateFIDDraft)); len(drafts) != 1 {
		t.Errorf("drafts = %d, want 1 (rejected send must not consume the draft)", len(drafts))
	}
}

// TestSendItemRelaysExternal proves a saved draft to a foreign-domain recipient is
// queued for outbound relay (carrying the sender's address as the envelope From)
// and the source draft is consumed.
func TestSendItemRelaysExternal(t *testing.T) {
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

	itemID := createDraft(t, ts, "carol@external.test")
	_, out := soapPost(t, ts, sendItemReq("false", itemID), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("SendItem not success: %s", out)
	}

	due, err := sp.Claim(time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Recipient != "carol@external.test" {
		t.Fatalf("relay spool = %v, want carol@external.test queued for relay", due)
	}
	if due[0].From != testUser {
		t.Errorf("relay envelope From = %q, want %q", due[0].From, testUser)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if drafts, _ := st.ListMessages(int64(mapi.PrivateFIDDraft)); len(drafts) != 0 {
		t.Errorf("drafts = %d, want 0 (sent draft consumed)", len(drafts))
	}
}
