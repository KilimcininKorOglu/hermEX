package ews

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// originalRaw is the message the smart responses below reply to or forward.
const originalRaw = "From: bob@hermex.test\r\nTo: " + testUser + "\r\nSubject: Original\r\n" +
	"Message-ID: <orig@hermex.test>\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
	"--b\r\nContent-Type: text/plain\r\n\r\nhi alice\r\n" +
	"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=plan.pdf\r\n\r\nPDFDATA\r\n--b--\r\n"

// seededOriginal starts a server whose Inbox holds originalRaw and returns the
// ItemId a client would name it by.
func seededOriginal(t *testing.T) (ts *httptest.Server, dir, ref string, uid uint32, msgID int64) {
	t.Helper()
	srv, d := seededWithMessage(t)
	st, err := objectstore.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(originalRaw), time.Now(), 0)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	ref = oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDInbox), MessageID: info.ID, UID: info.UID})
	return srv, d, ref, info.UID, info.ID
}

// nestedSmartRequest is the shape Outlook for Mac sends: a smart-response element carrying
// a reference id and a full nested Message.
func nestedSmartRequest(element, ref, subject string) string {
	return wrapRequest(`<CreateItem MessageDisposition="SaveOnly" xmlns="` + nsMessages + `">` +
		`<Items><t:` + element + ` xmlns:t="` + nsTypes + `">` +
		`<t:ReferenceItemId Id="` + ref + `"/>` +
		`<t:Message>` +
		`<t:Subject>` + subject + `</t:Subject>` +
		`<t:Body BodyType="Text">reply body</t:Body>` +
		`<t:ToRecipients><t:Mailbox><t:EmailAddress>` + testUser + `</t:EmailAddress></t:Mailbox></t:ToRecipients>` +
		`</t:Message>` +
		`</t:` + element + `></Items></CreateItem>`)
}

// flatSmartRequest is the published schema's shape: the message fields sit on the
// smart-response element itself.
func flatSmartRequest(element, ref, subject string) string {
	return wrapRequest(`<CreateItem MessageDisposition="SaveOnly" xmlns="` + nsMessages + `">` +
		`<Items><t:` + element + ` xmlns:t="` + nsTypes + `">` +
		`<t:ReferenceItemId Id="` + ref + `"/>` +
		`<t:Subject>` + subject + `</t:Subject>` +
		`<t:Body BodyType="Text">reply body</t:Body>` +
		`<t:ToRecipients><t:Mailbox><t:EmailAddress>` + testUser + `</t:EmailAddress></t:Mailbox></t:ToRecipients>` +
		`</t:` + element + `></Items></CreateItem>`)
}

// newBodyRequest sends only new text (NewBodyContent) for the server to complete.
func newBodyRequest(element, disp, ref, subject string) string {
	return wrapRequest(`<CreateItem MessageDisposition="` + disp + `" xmlns="` + nsMessages + `">` +
		`<Items><t:` + element + ` xmlns:t="` + nsTypes + `">` +
		`<t:Subject>` + subject + `</t:Subject>` +
		`<t:ToRecipients><t:Mailbox><t:EmailAddress>` + testUser + `</t:EmailAddress></t:Mailbox></t:ToRecipients>` +
		`<t:ReferenceItemId Id="` + ref + `"/>` +
		`<t:NewBodyContent BodyType="Text">sounds good</t:NewBodyContent>` +
		`</t:` + element + `></Items></CreateItem>`)
}

// draftSubjects returns the subjects of the drafts in a mailbox.
func draftSubjects(t *testing.T, dir string) []string {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDDraft))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Subject)
	}
	return out
}

// TestSmartResponsesSaveADraft covers both request shapes (Outlook for Mac's nested
// Message and the published flat one) and all three elements: each stores its draft.
func TestSmartResponsesSaveADraft(t *testing.T) {
	for _, c := range []struct {
		element string
		build   func(element, ref, subject string) string
	}{
		{"ReplyToItem", nestedSmartRequest}, {"ReplyAllToItem", nestedSmartRequest},
		{"ForwardItem", nestedSmartRequest}, {"ReplyToItem", flatSmartRequest},
	} {
		ts, dir, ref, _, _ := seededOriginal(t)
		_, out := soapPost(t, ts, c.build(c.element, ref, "Re: x"), true)
		if !strings.Contains(out, `ResponseClass="Success"`) || !strings.Contains(out, "CreateItemResponseMessage") {
			t.Fatalf("%s: not success: %s", c.element, out)
		}
		if got := draftSubjects(t, dir); len(got) != 1 || got[0] != "Re: x" {
			t.Errorf("%s: drafts = %v, want [Re: x]", c.element, got)
		}
	}
}

// TestSmartResponseToAMissingItemSavesNothing names references that are not an item
// of the caller's: each is refused (ErrorItemNotFound for a well-formed id) and no
// draft is stored, so no reply to nothing leaves.
func TestSmartResponseToAMissingItemSavesNothing(t *testing.T) {
	ts, dir, _, uid, msgID := seededOriginal(t)
	for name, ref := range map[string]string{
		"unknown message":    oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDInbox), MessageID: msgID + 999}),
		"other folder":       oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDSentItems), MessageID: msgID, UID: uid}),
		"no message, no uid": oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDInbox)}),
		"not an id":          "AAA",
	} {
		_, out := soapPost(t, ts, newBodyRequest("ForwardItem", "SaveOnly", ref, "Fw: x"), true)
		if strings.Contains(out, `ResponseClass="Success"`) {
			t.Errorf("%s: a reference to no item succeeded: %s", name, out)
		}
	}
	if got := draftSubjects(t, dir); len(got) != 0 {
		t.Errorf("drafts = %v after refused references, want none", got)
	}
}

// TestNewBodyContentGetsTheOriginal sends only new text: the stored message holds
// it followed by the original's text, and a forward also its attachment.
func TestNewBodyContentGetsTheOriginal(t *testing.T) {
	for _, c := range []struct {
		element    string
		attachment bool
	}{{"ReplyToItem", false}, {"ForwardItem", true}} {
		ts, dir, ref, _, _ := seededOriginal(t)
		_, out := soapPost(t, ts, newBodyRequest(c.element, "SaveOnly", ref, "Re: x"), true)
		if !strings.Contains(out, `ResponseClass="Success"`) {
			t.Fatalf("%s: %s", c.element, out)
		}
		got := draftRaw(t, dir)
		if !strings.Contains(got, "sounds good") || !strings.Contains(got, "hi alice") {
			t.Errorf("%s: draft lacks the new text or the original:\n%s", c.element, got)
		}
		if strings.Contains(got, "plan.pdf") != c.attachment {
			t.Errorf("%s: original attachment present = %v, want %v", c.element, !c.attachment, c.attachment)
		}
	}
}

// TestSentReplyMarksTheOriginal sends a reply: the original carries the reply verb
// and \Answered, as the EAS SmartReply leaves it.
func TestSentReplyMarksTheOriginal(t *testing.T) {
	ts, dir, ref, uid, msgID := seededOriginal(t)
	_, out := soapPost(t, ts, newBodyRequest("ReplyToItem", "SendAndSaveCopy", ref, "Re: x"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("%s", out)
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	props, err := st.GetMessageProperties(msgID, mapi.PrLastVerbExecuted)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := props.Get(mapi.PrLastVerbExecuted); v != mapi.NoteVerbReplyToSender {
		t.Errorf("PidTagLastVerbExecuted = %v, want %d", v, mapi.NoteVerbReplyToSender)
	}
	if flags, _ := st.MessageFlags(int64(mapi.PrivateFIDInbox), uid); flags&objectstore.FlagAnswered == 0 {
		t.Error("the original is not marked \\Answered")
	}
}
