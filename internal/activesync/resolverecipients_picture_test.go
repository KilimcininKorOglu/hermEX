package activesync

import (
	"encoding/base64"
	"net/http/httptest"
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
	mustNoErr(t, "close the store", st.Close())

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

// pictureRequestWith asks for portraits with the given Picture option children.
func pictureRequestWith(query string, opts ...*wbxml.Node) *wbxml.Node {
	return wbxml.Elem(wbxml.RRResolveRecipients,
		wbxml.Str(wbxml.RRTo, query),
		wbxml.Elem(wbxml.RROptions, wbxml.Elem(wbxml.RRPicture, opts...)))
}

// photoServer is seededServer with a portrait of n bytes on the test user.
func photoServer(t *testing.T, n int) *httptest.Server {
	t.Helper()
	ts, dir := seededServer(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserPhoto(make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	mustNoErr(t, "close the store", st.Close())
	return ts
}

// pictureStatus returns the Picture Status of the first Recipient for query.
func pictureStatus(t *testing.T, root *wbxml.Node, query string) string {
	t.Helper()
	resp := responseFor(root, query)
	if resp == nil || resp.Child(wbxml.RRRecipient) == nil {
		t.Fatalf("%q did not resolve", query)
	}
	pic := resp.Child(wbxml.RRRecipient).Child(wbxml.RRPicture)
	if pic == nil {
		t.Fatalf("%q: Recipient carried no Picture", query)
	}
	status := pic.ChildText(wbxml.RRStatus)
	// A withheld portrait is withheld: a cap that answered its status but still
	// sent the bytes would cap nothing.
	if status != "1" && pic.Child(wbxml.RRData) != nil {
		t.Errorf("%q: Picture Status %s still carries Data", query, status)
	}
	return status
}

// TestResolveRecipientsPictureCaps proves the two caps a client sets on portraits
// ([MS-ASCMD] Picture Status values): a portrait larger than MaxSize is withheld
// with 174, and once MaxPictures portraits went out the next is withheld with 175.
func TestResolveRecipientsPictureCaps(t *testing.T) {
	ts := photoServer(t, 100)
	_, root := postCommand(t, ts, "ResolveRecipients",
		pictureRequestWith("alice", wbxml.Str(wbxml.RRMaxSize, "99")))
	if s := pictureStatus(t, root, "alice"); s != "174" {
		t.Errorf("MaxSize 99 on a 100-byte portrait: Picture Status = %q, want 174", s)
	}
	_, root = postCommand(t, ts, "ResolveRecipients",
		pictureRequestWith("alice", wbxml.Str(wbxml.RRMaxSize, "100")))
	if s := pictureStatus(t, root, "alice"); s != "1" {
		t.Errorf("MaxSize 100 on a 100-byte portrait: Picture Status = %q, want 1", s)
	}

	req := wbxml.Elem(wbxml.RRResolveRecipients,
		wbxml.Str(wbxml.RRTo, "alice"), wbxml.Str(wbxml.RRTo, testUser),
		wbxml.Elem(wbxml.RROptions, wbxml.Elem(wbxml.RRPicture, wbxml.Str(wbxml.RRMaxPictures, "1"))))
	_, root = postCommand(t, ts, "ResolveRecipients", req)
	if s := pictureStatus(t, root, "alice"); s != "1" {
		t.Errorf("first portrait under MaxPictures 1: Picture Status = %q, want 1", s)
	}
	if s := pictureStatus(t, root, testUser); s != "175" {
		t.Errorf("second portrait under MaxPictures 1: Picture Status = %q, want 175", s)
	}
}

// TestResolveRecipientsRefusesABadPictureCap proves MaxSize and MaxPictures, both
// unsigned integers in [MS-ASCMD], are refused as a protocol error (Status 5) when
// they are not one. Read as zero, a bad value would lift the cap the client meant
// to set.
func TestResolveRecipientsRefusesABadPictureCap(t *testing.T) {
	ts := photoServer(t, 100)
	for _, tc := range []struct{ name, value string }{
		{"not a number", "abc"},
		{"negative", "-5"},
		{"empty", ""},
		{"beyond 32 bits", "99999999999"},
	} {
		for _, tag := range []wbxml.Tag{wbxml.RRMaxSize, wbxml.RRMaxPictures} {
			_, root := postCommand(t, ts, "ResolveRecipients", pictureRequestWith("alice", wbxml.Str(tag, tc.value)))
			if s := root.ChildText(wbxml.RRStatus); s != "5" {
				t.Errorf("%s in tag %v: Status = %q, want 5", tc.name, tag, s)
			}
			if responseFor(root, "alice") != nil {
				t.Errorf("%s in tag %v: a Response was served", tc.name, tag)
			}
		}
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
	if pic.Child(wbxml.RRData) != nil {
		t.Error("a Picture with no portrait carries a Data element")
	}
}
