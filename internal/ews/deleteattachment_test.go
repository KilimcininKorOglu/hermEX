package ews

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// inboxAttachmentCount opens the mailbox and returns the attachment count of the
// most recently listed Inbox message, plus that message's id.
func inboxMessageWithAttachments(t *testing.T, dir string) (mid int64, attachments int) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil || len(msgs) == 0 {
		t.Fatalf("no inbox messages: %v", err)
	}
	mid = msgs[len(msgs)-1].ID
	m, err := st.OpenMessage(mid)
	if err != nil {
		t.Fatal(err)
	}
	return mid, len(m.Attachments)
}

func deleteAttachmentBody(id string) string {
	return `<DeleteAttachment xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<AttachmentIds><t:AttachmentId Id="` + id + `"/></AttachmentIds>` +
		`</DeleteAttachment>`
}

// TestDeleteAttachment proves an attachment is removed from its parent message and
// the parent item id is returned.
func TestDeleteAttachment(t *testing.T) {
	ts, dir := seededEWS(t)
	seedInboxMessageWithAttachment(t, dir, "has attachment")

	mid, before := inboxMessageWithAttachments(t, dir)
	if before == 0 {
		t.Fatal("seeded message carried no attachment")
	}

	id := oxews.EncodeAttachmentID(int64(mapi.PrivateFIDInbox), mid, 0, "")
	_, resp := soapPost(t, ts, wrapRequest(deleteAttachmentBody(id)), true)
	if !strings.Contains(resp, `ResponseClass="Success"`) {
		t.Fatalf("DeleteAttachment did not succeed: %s", resp)
	}
	if !strings.Contains(resp, "RootItemId") {
		t.Errorf("response missing RootItemId: %s", resp)
	}

	_, after := inboxMessageWithAttachments(t, dir)
	if after != before-1 {
		t.Errorf("attachment count = %d, want %d (one removed)", after, before-1)
	}
}

// TestDeleteAttachmentInvalidId proves a malformed attachment id is reported as an
// invalid request, not a crash.
func TestDeleteAttachmentInvalidId(t *testing.T) {
	ts, _ := seededEWS(t)
	_, resp := soapPost(t, ts, wrapRequest(deleteAttachmentBody("not-a-real-id")), true)
	if !strings.Contains(resp, "ErrorInvalidRequest") {
		t.Errorf("malformed attachment id not rejected: %s", resp)
	}
}
