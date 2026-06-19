package ews

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// createAttachmentReq builds a CreateAttachment request adding one FileAttachment
// to a parent item.
func createAttachmentReq(parentID, name, contentType, contentB64 string) string {
	return wrapRequest(`<CreateAttachment xmlns="` + nsMessages + `">` +
		`<ParentItemId Id="` + parentID + `"/>` +
		`<Attachments><t:FileAttachment xmlns:t="` + nsTypes + `">` +
		`<t:Name>` + name + `</t:Name>` +
		`<t:ContentType>` + contentType + `</t:ContentType>` +
		`<t:Content>` + contentB64 + `</t:Content>` +
		`</t:FileAttachment></Attachments></CreateAttachment>`)
}

// parentItemID returns the item id of the single message FindItem lists in the
// Inbox.
func parentItemID(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, out := soapPost(t, ts, findItemReq("inbox"), true)
	m := itemIDRE.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("FindItem returned no ItemId: %s", out)
	}
	return m[1]
}

// TestCreateAttachmentRoundTrip confirms an attachment added to a message is
// returned by a follow-up GetAttachment with its name, content type, and content
// intact (the content type round-trip is why CreateAttachment sets the MIME tag
// the reference drops).
func TestCreateAttachmentRoundTrip(t *testing.T) {
	ts, _ := seededWithMessage(t, plainMessage)
	parent := parentItemID(t, ts)
	payload := base64.StdEncoding.EncodeToString([]byte("hello attach"))

	resp, out := soapPost(t, ts, createAttachmentReq(parent, "note.txt", "text/plain", payload), true)
	if resp.StatusCode != 200 {
		t.Fatalf("CreateAttachment status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("CreateAttachment not success: %s", out)
	}
	m := attachIDRE.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("CreateAttachment returned no AttachmentId: %s", out)
	}

	_, got := soapPost(t, ts, getAttachmentReq(m[1]), true)
	if !strings.Contains(got, "note.txt") {
		t.Errorf("GetAttachment missing name: %s", got)
	}
	if !strings.Contains(got, "text/plain") {
		t.Errorf("GetAttachment missing content type (round-trip lost): %s", got)
	}
	if !strings.Contains(got, payload) {
		t.Errorf("GetAttachment missing content: %s", got)
	}
}

// TestCreateAttachmentBaseCountOffset confirms an attachment added to a message
// that ALREADY has one resolves, via its returned id, to the new content and not
// the pre-existing attachment — the positional-index offset (baseCount) a
// clean-message test cannot exercise.
func TestCreateAttachmentBaseCountOffset(t *testing.T) {
	ts, _ := seededWithMessage(t, attachMessage) // already carries doc.txt = "file data"
	parent := parentItemID(t, ts)
	payload := base64.StdEncoding.EncodeToString([]byte("SECOND"))

	_, out := soapPost(t, ts, createAttachmentReq(parent, "second.txt", "text/plain", payload), true)
	m := attachIDRE.FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("CreateAttachment returned no AttachmentId: %s", out)
	}

	_, got := soapPost(t, ts, getAttachmentReq(m[1]), true)
	if !strings.Contains(got, payload) {
		t.Errorf("the new attachment's id must resolve to the new content: %s", got)
	}
	if strings.Contains(got, base64.StdEncoding.EncodeToString([]byte("file data"))) {
		t.Errorf("the new attachment's id wrongly resolved to the pre-existing attachment: %s", got)
	}
}

// TestCreateAttachmentBumpsChangeNumber confirms adding an attachment advances the
// parent message's change number, so ICS content sync observes it.
func TestCreateAttachmentBumpsChangeNumber(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cns, err := st.FolderMessageChangeNumbers(int64(mapi.PrivateFIDInbox))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()
	var mid int64
	var cn0 uint64
	for k, v := range cns {
		mid, cn0 = k, v
	}

	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDInbox), MessageID: mid})
	payload := base64.StdEncoding.EncodeToString([]byte("x"))
	if _, out := soapPost(t, ts, createAttachmentReq(itemID, "a.txt", "text/plain", payload), true); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("CreateAttachment not success: %s", out)
	}

	st, err = objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cns, err = st.FolderMessageChangeNumbers(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if cn1 := cns[mid]; cn1 <= cn0 {
		t.Errorf("change number did not advance: before=%d after=%d", cn0, cn1)
	}
}

// TestCreateAttachmentErrors confirms the per-attachment error codes: a malformed
// parent id, a nonexistent parent, and an unsupported ItemAttachment.
func TestCreateAttachmentErrors(t *testing.T) {
	ts, _ := seededWithMessage(t, plainMessage)
	payload := base64.StdEncoding.EncodeToString([]byte("x"))

	if _, out := soapPost(t, ts, createAttachmentReq("not a valid id", "a.txt", "text/plain", payload), true); !strings.Contains(out, "ErrorInvalidId") {
		t.Errorf("malformed parent id should be ErrorInvalidId: %s", out)
	}

	bogus := oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDInbox), MessageID: 999999})
	if _, out := soapPost(t, ts, createAttachmentReq(bogus, "a.txt", "text/plain", payload), true); !strings.Contains(out, "ErrorItemNotFound") {
		t.Errorf("nonexistent parent should be ErrorItemNotFound: %s", out)
	}

	parent := parentItemID(t, ts)
	itemAtt := wrapRequest(`<CreateAttachment xmlns="` + nsMessages + `">` +
		`<ParentItemId Id="` + parent + `"/>` +
		`<Attachments><t:ItemAttachment xmlns:t="` + nsTypes + `"><t:Name>nested</t:Name></t:ItemAttachment></Attachments>` +
		`</CreateAttachment>`)
	if _, out := soapPost(t, ts, itemAtt, true); !strings.Contains(out, "ErrorInvalidRequest") {
		t.Errorf("ItemAttachment input should be ErrorInvalidRequest: %s", out)
	}
}
