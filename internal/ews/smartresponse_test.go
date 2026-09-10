package ews

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// nestedSmartRequest is the shape Outlook for Mac sends: a smart-response element carrying
// a reference id and a full nested Message.
func nestedSmartRequest(element, subject string) string {
	return wrapRequest(`<CreateItem MessageDisposition="SaveOnly" xmlns="` + nsMessages + `">` +
		`<Items><t:` + element + ` xmlns:t="` + nsTypes + `">` +
		`<t:ReferenceItemId Id="AAA" ChangeKey="BBB"/>` +
		`<t:Message>` +
		`<t:Subject>` + subject + `</t:Subject>` +
		`<t:Body BodyType="Text">reply body</t:Body>` +
		`<t:ToRecipients><t:Mailbox><t:EmailAddress>` + testUser + `</t:EmailAddress></t:Mailbox></t:ToRecipients>` +
		`</t:Message>` +
		`</t:` + element + `></Items></CreateItem>`)
}

// flatSmartRequest is the published schema's shape: the message fields sit on the
// smart-response element itself.
func flatSmartRequest(element, subject string) string {
	return wrapRequest(`<CreateItem MessageDisposition="SaveOnly" xmlns="` + nsMessages + `">` +
		`<Items><t:` + element + ` xmlns:t="` + nsTypes + `">` +
		`<t:ReferenceItemId Id="AAA" ChangeKey="BBB"/>` +
		`<t:Subject>` + subject + `</t:Subject>` +
		`<t:Body BodyType="Text">reply body</t:Body>` +
		`<t:ToRecipients><t:Mailbox><t:EmailAddress>` + testUser + `</t:EmailAddress></t:Mailbox></t:ToRecipients>` +
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

// TestReplyToItemSavesADraft is the load-bearing case: Outlook for Mac saves a reply as a
// draft with a ReplyToItem carrying a nested Message, and the draft must be stored.
func TestReplyToItemSavesADraft(t *testing.T) {
	ts, dir := seededWithMessage(t)

	_, out := soapPost(t, ts, nestedSmartRequest("ReplyToItem", "Re: hello"), true)

	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("not success: %s", out)
	}
	if got := draftSubjects(t, dir); len(got) != 1 || got[0] != "Re: hello" {
		t.Errorf("drafts = %v, want [Re: hello]", got)
	}
}

// TestReplyAllToItemSavesADraft proves the reply-all element takes the same path.
func TestReplyAllToItemSavesADraft(t *testing.T) {
	ts, dir := seededWithMessage(t)

	soapPost(t, ts, nestedSmartRequest("ReplyAllToItem", "Re: all"), true)

	if got := draftSubjects(t, dir); len(got) != 1 || got[0] != "Re: all" {
		t.Errorf("drafts = %v, want [Re: all]", got)
	}
}

// TestForwardItemSavesADraft proves the forward element takes the same path.
func TestForwardItemSavesADraft(t *testing.T) {
	ts, dir := seededWithMessage(t)

	soapPost(t, ts, nestedSmartRequest("ForwardItem", "Fw: hello"), true)

	if got := draftSubjects(t, dir); len(got) != 1 || got[0] != "Fw: hello" {
		t.Errorf("drafts = %v, want [Fw: hello]", got)
	}
}

// TestFlatSmartResponseSavesADraft proves the published schema's flat shape is read too, so
// a client that follows Types.xsd is served as well as one that nests a Message.
func TestFlatSmartResponseSavesADraft(t *testing.T) {
	ts, dir := seededWithMessage(t)

	soapPost(t, ts, flatSmartRequest("ReplyToItem", "Re: flat"), true)

	if got := draftSubjects(t, dir); len(got) != 1 || got[0] != "Re: flat" {
		t.Errorf("drafts = %v, want [Re: flat]", got)
	}
}

// TestSmartResponseAnswersWithAResponseMessage proves the request is answered per item. An
// unread element would leave the response with no CreateItemResponseMessage at all, which
// is what made the lost draft silent.
func TestSmartResponseAnswersWithAResponseMessage(t *testing.T) {
	ts, _ := seededWithMessage(t)

	_, out := soapPost(t, ts, nestedSmartRequest("ForwardItem", "Fw: counted"), true)

	if !strings.Contains(out, "CreateItemResponseMessage") {
		t.Errorf("the response carries no response message:\n%s", out)
	}
}
