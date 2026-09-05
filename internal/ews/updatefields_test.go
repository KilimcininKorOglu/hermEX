package ews

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// draftMessage is a saved draft with one To recipient, a Cc and a Bcc, which is
// what an edit has to preserve when it names only one of the three.
const draftMessage = "From: user@hermex.test\r\nTo: first@example.test\r\n" +
	"Cc: copied@example.test\r\nBcc: blind@example.test\r\n" +
	"Subject: Draft subject\r\nMessage-ID: <draft-1@hermex.test>\r\n" +
	"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nOriginal body\r\n"

// updateReq builds an UpdateItem naming one field.
func updateReq(itemID, fieldURI, inner string) string {
	return wrapRequest(`<UpdateItem ConflictResolution="AutoResolve" xmlns="` + nsMessages + `">` +
		`<ItemChanges><t:ItemChange xmlns:t="` + nsTypes + `">` +
		`<t:ItemId Id="` + itemID + `"/>` +
		`<t:Updates><t:SetItemField><t:FieldURI FieldURI="` + fieldURI + `"/>` +
		`<t:Message>` + inner + `</t:Message></t:SetItemField></t:Updates>` +
		`</t:ItemChange></ItemChanges></UpdateItem>`)
}

// firstInboxItemID returns the ItemId of the single seeded message.
func firstInboxItemID(t *testing.T, out string) string {
	t.Helper()
	m := itemIDRE.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("no ItemId in %s", out)
	}
	return m[1]
}

// storedMessage reads the one message in the Inbox back as a stored object,
// which is what every other protocol reads, rather than through the API that
// wrote it.
func storedMessage(t *testing.T, dir string) (string, []string, []string, []string, string) {
	t.Helper()
	st, msgs := inboxUIDs(t, dir)
	defer st.Close()
	if len(msgs) != 1 {
		t.Fatalf("inbox holds %d messages, want 1", len(msgs))
	}
	msg, err := st.OpenMessage(msgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := msg.Props.Get(mapi.PrSubject)
	subj, _ := subject.(string)
	body := ""
	if v, ok := msg.Props.Get(mapi.PrBody); ok {
		body, _ = v.(string)
	} else if v, ok := msg.Props.Get(mapi.PrHTML); ok {
		b, _ := v.([]byte)
		body = string(b)
	}
	var to, cc, bcc []string
	for _, bag := range msg.Recipients {
		addr := recipientSMTP(bag)
		rt, _ := bag.Get(mapi.PrRecipientType)
		switch rt {
		case int32(mapi.RecipTo):
			to = append(to, addr)
		case int32(mapi.RecipCc):
			cc = append(cc, addr)
		case int32(mapi.RecipBcc):
			bcc = append(bcc, addr)
		}
	}
	return subj, to, cc, bcc, body
}

// TestUpdateItemWritesTheSubject is the defect the whole slice is about: the
// handler used to answer Success and keep the old value, so a client had no way
// to learn its edit was discarded.
func TestUpdateItemWritesTheSubject(t *testing.T) {
	ts, dir := seededWithMessage(t, draftMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	id := firstInboxItemID(t, fi)

	_, out := soapPost(t, ts, updateReq(id, "item:Subject", `<t:Subject>Edited subject</t:Subject>`), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem not success: %s", out)
	}
	subj, _, _, _, _ := storedMessage(t, dir)
	if subj != "Edited subject" {
		t.Errorf("stored subject = %q, want the edited one", subj)
	}
}

// TestUpdateItemWritesTheBody covers the other content field, and that the old
// representation is gone rather than left beside the new one.
func TestUpdateItemWritesTheBody(t *testing.T) {
	ts, dir := seededWithMessage(t, draftMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	id := firstInboxItemID(t, fi)

	_, out := soapPost(t, ts, updateReq(id, "item:Body",
		`<t:Body BodyType="Text">Edited body</t:Body>`), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem not success: %s", out)
	}
	_, _, _, _, body := storedMessage(t, dir)
	if !strings.Contains(body, "Edited body") {
		t.Errorf("stored body = %q, want the edited one", body)
	}
	if strings.Contains(body, "Original body") {
		t.Errorf("the old body survived: %q", body)
	}
}

// TestUpdateItemReplacesOneRecipientClass is what a client editing a draft does
// most: it names ToRecipients only, and the Cc and Bcc lists must survive.
func TestUpdateItemReplacesOneRecipientClass(t *testing.T) {
	ts, dir := seededWithMessage(t, draftMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	id := firstInboxItemID(t, fi)

	_, out := soapPost(t, ts, updateReq(id, "message:ToRecipients",
		`<t:ToRecipients><t:Mailbox><t:EmailAddress>second@example.test</t:EmailAddress></t:Mailbox>`+
			`<t:Mailbox><t:EmailAddress>third@example.test</t:EmailAddress></t:Mailbox></t:ToRecipients>`), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem not success: %s", out)
	}
	_, to, cc, bcc, _ := storedMessage(t, dir)
	if len(to) != 2 || to[0] != "second@example.test" || to[1] != "third@example.test" {
		t.Errorf("To = %v, want the two new recipients", to)
	}
	if len(cc) != 1 || cc[0] != "copied@example.test" {
		t.Errorf("Cc = %v, want the untouched one", cc)
	}
	if len(bcc) != 1 || bcc[0] != "blind@example.test" {
		t.Errorf("Bcc = %v, want the untouched one", bcc)
	}
}

// TestUpdateItemWritesBcc keeps the blind list editable. It is the one class a
// client cannot check by reading the sent copy, so a silent drop would go
// unnoticed until the message reached nobody.
func TestUpdateItemWritesBcc(t *testing.T) {
	ts, dir := seededWithMessage(t, draftMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	id := firstInboxItemID(t, fi)

	_, out := soapPost(t, ts, updateReq(id, "message:BccRecipients",
		`<t:BccRecipients><t:Mailbox><t:EmailAddress>hidden@example.test</t:EmailAddress></t:Mailbox></t:BccRecipients>`), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem not success: %s", out)
	}
	_, to, _, bcc, _ := storedMessage(t, dir)
	if len(bcc) != 1 || bcc[0] != "hidden@example.test" {
		t.Errorf("Bcc = %v, want the edited one", bcc)
	}
	if len(to) != 1 || to[0] != "first@example.test" {
		t.Errorf("To = %v, want the untouched one", to)
	}
}

// TestUpdateItemRefusesAFieldItDoesNotWrite is the honesty guard. Answering
// Success for a field the server drops is worse than refusing it, because the
// client then believes the message holds a value it does not.
func TestUpdateItemRefusesAFieldItDoesNotWrite(t *testing.T) {
	ts, dir := seededWithMessage(t, draftMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	id := firstInboxItemID(t, fi)

	_, out := soapPost(t, ts, updateReq(id, "item:Categories",
		`<t:Categories><t:String>Red</t:String></t:Categories>`), true)
	if !strings.Contains(out, "ErrorInvalidPropertySet") {
		t.Fatalf("an unwritten field answered %s", out)
	}
	// And nothing was changed by the refused request.
	subj, _, _, _, _ := storedMessage(t, dir)
	if subj != "Draft subject" {
		t.Errorf("the refused request still edited the message: subject = %q", subj)
	}
}

// TestUpdateItemStillMarksRead keeps the one field the handler already wrote
// working, and proves it survives a content rewrite in the same request: the
// rewrite gives the message a new uid, so a flag written against the old one
// would land nowhere.
func TestUpdateItemStillMarksRead(t *testing.T) {
	ts, dir := seededWithMessage(t, draftMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	id := firstInboxItemID(t, fi)

	req := wrapRequest(`<UpdateItem ConflictResolution="AutoResolve" xmlns="` + nsMessages + `">` +
		`<ItemChanges><t:ItemChange xmlns:t="` + nsTypes + `">` +
		`<t:ItemId Id="` + id + `"/>` +
		`<t:Updates>` +
		`<t:SetItemField><t:FieldURI FieldURI="item:Subject"/><t:Message><t:Subject>Both</t:Subject></t:Message></t:SetItemField>` +
		`<t:SetItemField><t:FieldURI FieldURI="message:IsRead"/><t:Message><t:IsRead>true</t:IsRead></t:Message></t:SetItemField>` +
		`</t:Updates></t:ItemChange></ItemChanges></UpdateItem>`)
	_, out := soapPost(t, ts, req, true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateItem not success: %s", out)
	}
	subj, _, _, _, _ := storedMessage(t, dir)
	if subj != "Both" {
		t.Errorf("subject = %q", subj)
	}
	st, msgs := inboxUIDs(t, dir)
	defer st.Close()
	flags, _ := st.MessageFlags(int64(mapi.PrivateFIDInbox), msgs[0].UID)
	if flags&objectstore.FlagSeen == 0 {
		t.Error("the read flag did not survive the content rewrite")
	}
}

// TestUpdateItemReturnsTheNewItemID keeps the client able to follow the message.
// The rewrite stores a new message, so the old id no longer resolves and the
// response has to carry the one that does.
func TestUpdateItemReturnsTheNewItemID(t *testing.T) {
	ts, _ := seededWithMessage(t, draftMessage)
	_, fi := soapPost(t, ts, findItemReq("inbox"), true)
	id := firstInboxItemID(t, fi)

	_, out := soapPost(t, ts, updateReq(id, "item:Subject", `<t:Subject>Edited</t:Subject>`), true)
	newID := itemIDRE.FindStringSubmatch(out)
	if len(newID) != 2 {
		t.Fatalf("the response carries no ItemId: %s", out)
	}
	if newID[1] == id {
		t.Error("the response repeated the old id, which no longer resolves")
	}
	// The returned id resolves: GetItem on it finds the edited message.
	req := wrapRequest(`<GetItem xmlns="` + nsMessages + `">` +
		`<ItemShape><t:BaseShape xmlns:t="` + nsTypes + `">AllProperties</t:BaseShape></ItemShape>` +
		`<ItemIds><t:ItemId Id="` + newID[1] + `" xmlns:t="` + nsTypes + `"/></ItemIds></GetItem>`)
	_, got := soapPost(t, ts, req, true)
	if !strings.Contains(got, "Edited") {
		t.Errorf("GetItem on the returned id did not find the edited message: %s", got)
	}
}
