package activesync

import (
	"encoding/base64"
	"testing"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// pictureRequest is a ResolveRecipients request that asks for the recipient's
// portrait (an empty Picture option means unbounded).
func pictureRequest(query string) *wbxml.Node {
	return wbxml.Elem(wbxml.RRResolveRecipients,
		wbxml.Str(wbxml.RRTo, query),
		wbxml.Elem(wbxml.RROptions, wbxml.Elem(wbxml.RRPicture)))
}

// TestResolveRecipientsPicture proves a recipient's portrait is returned as
// base64 Picture>Data from the cross-protocol photo property.
func TestResolveRecipientsPicture(t *testing.T) {
	ts, dir := seededServer(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	photo := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3, 4}
	if err := st.SetUserPhoto(photo); err != nil {
		t.Fatalf("set photo: %v", err)
	}
	st.Close()

	_, root := postCommand(t, ts, "ResolveRecipients", pictureRequest("alice"))
	resp := responseFor(root, "alice")
	if resp == nil {
		t.Fatal("no Response echoed for the query")
	}
	rec := resp.Child(wbxml.RRRecipient)
	if rec == nil {
		t.Fatal("resolved response carried no Recipient")
	}
	pic := rec.Child(wbxml.RRPicture)
	if pic == nil {
		t.Fatal("Recipient carried no Picture")
	}
	if s := pic.ChildText(wbxml.RRStatus); s != "1" {
		t.Errorf("Picture Status = %q, want 1", s)
	}
	if want := base64.StdEncoding.EncodeToString(photo); pic.ChildText(wbxml.RRData) != want {
		t.Errorf("Picture Data = %q, want %q", pic.ChildText(wbxml.RRData), want)
	}
}

// TestResolveRecipientsPictureNone proves a recipient with no portrait answers
// Picture Status 173 (no picture), not a data element.
func TestResolveRecipientsPictureNone(t *testing.T) {
	ts, _ := seededServer(t)
	_, root := postCommand(t, ts, "ResolveRecipients", pictureRequest("alice"))
	resp := responseFor(root, "alice")
	if resp == nil {
		t.Fatal("no Response echoed for the query")
	}
	rec := resp.Child(wbxml.RRRecipient)
	if rec == nil {
		t.Fatal("resolved response carried no Recipient")
	}
	pic := rec.Child(wbxml.RRPicture)
	if pic == nil {
		t.Fatal("Recipient carried no Picture")
	}
	if s := pic.ChildText(wbxml.RRStatus); s != "173" {
		t.Errorf("Picture Status = %q, want 173 (no picture)", s)
	}
}
