package ews

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

func createItemReq(disposition, to, subject, body string) string {
	return wrapRequest(`<CreateItem MessageDisposition="` + disposition + `" xmlns="` + nsMessages + `">` +
		`<Items><t:Message xmlns:t="` + nsTypes + `">` +
		`<t:Subject>` + subject + `</t:Subject>` +
		`<t:Body BodyType="Text">` + body + `</t:Body>` +
		`<t:ToRecipients><t:Mailbox><t:EmailAddress>` + to + `</t:EmailAddress></t:Mailbox></t:ToRecipients>` +
		`</t:Message></Items></CreateItem>`)
}

// TestCreateItemSendAndSave confirms SendAndSaveCopy delivers (loopback to the
// sender) and files a Sent copy.
func TestCreateItemSendAndSave(t *testing.T) {
	ts, dir := seededWithMessage(t)
	_, out := soapPost(t, ts, createItemReq("SendAndSaveCopy", testUser, "Sent via EWS", "hello there"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	sent, _ := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if len(inbox) != 1 {
		t.Errorf("inbox = %d, want 1 (delivered to self)", len(inbox))
	}
	if len(sent) != 1 {
		t.Errorf("sent = %d, want 1 (saved copy)", len(sent))
	}
}

// TestCreateItemSaveOnly confirms SaveOnly stores a draft and does not deliver.
func TestCreateItemSaveOnly(t *testing.T) {
	ts, dir := seededWithMessage(t)
	_, out := soapPost(t, ts, createItemReq("SaveOnly", testUser, "Draft via EWS", "work in progress"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	drafts, _ := st.ListMessages(int64(mapi.PrivateFIDDraft))
	inbox, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if len(drafts) != 1 {
		t.Errorf("drafts = %d, want 1", len(drafts))
	}
	if len(inbox) != 0 {
		t.Errorf("inbox = %d, want 0 (SaveOnly must not deliver)", len(inbox))
	}
}

// TestCreateItemSendOnly confirms SendOnly delivers (loopback) but files no Sent
// copy, and — the regression guard — that the response still carries an <Items>
// container. A real EWS client rejects a CreateItemResponseMessage with no Items
// element; SendOnly persists nothing, so its container is present but empty. The
// store-only assertions in the sibling tests passed while this wire shape was
// malformed (the container omitted entirely), which is exactly how the defect
// reached the live smoke — so this test gates the shape, not just the effect.
func TestCreateItemSendOnly(t *testing.T) {
	ts, dir := seededWithMessage(t)
	_, out := soapPost(t, ts, createItemReq("SendOnly", testUser, "Send via EWS", "fire and forget"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	if !strings.Contains(out, "<Items") {
		t.Errorf("SendOnly response omits the <Items> container (clients reject its absence): %s", out)
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox, _ := st.ListMessages(int64(mapi.PrivateFIDInbox))
	sent, _ := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if len(inbox) != 1 {
		t.Errorf("inbox = %d, want 1 (delivered to self)", len(inbox))
	}
	if len(sent) != 0 {
		t.Errorf("sent = %d, want 0 (SendOnly files no copy)", len(sent))
	}
}

// multiAccountServer builds an EWS server over a directory with several accounts
// (for ResolveNames).
func multiAccountServer(t *testing.T) *httptest.Server {
	t.Helper()
	accs := directory.StaticAccounts{
		"alice@hermex.test": {Password: testPass, MailboxPath: t.TempDir()},
		"alex@hermex.test":  {Password: testPass, MailboxPath: t.TempDir()},
		"bob@hermex.test":   {Password: testPass, MailboxPath: t.TempDir()},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

func resolveReq(entry string) string {
	return wrapRequest(`<ResolveNames xmlns="` + nsMessages + `"><UnresolvedEntry>` + entry + `</UnresolvedEntry></ResolveNames>`)
}

// TestResolveNames confirms the single/multiple/none outcomes.
func TestResolveNames(t *testing.T) {
	ts := multiAccountServer(t)

	_, single := soapPost(t, ts, resolveReq("bob"), true)
	if !strings.Contains(single, `ResponseClass="Success"`) || !strings.Contains(single, "bob@hermex.test") {
		t.Errorf("single resolve failed: %s", single)
	}

	_, multi := soapPost(t, ts, resolveReq("al"), true)
	if !strings.Contains(multi, "ErrorNameResolutionMultipleResults") {
		t.Errorf("multiple resolve should warn multiple: %s", multi)
	}

	_, none := soapPost(t, ts, resolveReq("zzz-nobody"), true)
	if !strings.Contains(none, "ErrorNameResolutionNoResults") {
		t.Errorf("no-match resolve should warn no results: %s", none)
	}
}
