package objectstore

import (
	"errors"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestHasAttachments checks the real-attachment probe: a by-value attachment with
// no Content-ID counts, an inline cid part (Content-ID set) does not, and a plain
// message has none. This keeps the list's paperclip consistent with the reader,
// which renders cid parts inline rather than listing them.
func TestHasAttachments(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	when := time.Unix(1700000000, 0)

	plain := "From: a@example.test\r\nSubject: plain\r\n\r\nbody"
	realAttach := "From: a@example.test\r\nSubject: att\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--B\r\nContent-Type: application/pdf; name=\"r.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"r.pdf\"\r\n\r\n%PDF data\r\n--B--\r\n"
	inlineOnly := "From: a@example.test\r\nSubject: inline\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/html\r\n\r\n<img src=\"cid:x\">\r\n" +
		"--B\r\nContent-Type: image/png\r\nContent-ID: <x>\r\nContent-Disposition: inline\r\n\r\nPNGDATA\r\n--B--\r\n"

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"plain", plain, false},
		{"real attachment", realAttach, true},
		{"inline cid only", inlineOnly, false},
	}
	for _, c := range cases {
		info, err := s.AppendMessage(inbox, []byte(c.raw), when, 0)
		if err != nil {
			t.Fatalf("%s: append: %v", c.name, err)
		}
		got, err := s.HasAttachments(info.ID)
		if err != nil {
			t.Fatalf("%s: HasAttachments: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: HasAttachments = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCreateDeleteAttachmentStableNumbers is the C.Inc 5b bug fix: an attach
// number a client holds must survive a sibling attachment's deletion. Two
// attachments take numbers 0 and 1; deleting number 0 must leave the survivor at
// number 1 (not renumber it to 0, as a position-based scheme would), a later
// create continues past the high-water number rather than reusing 0, and
// deleting a number that no longer exists reports ErrNotFound.
func TestCreateDeleteAttachmentStableNumbers(t *testing.T) {
	s := openSeededStore(t)
	info, err := s.AppendMessage(int64(mapi.PrivateFIDInbox),
		[]byte("From: a@example.test\r\nSubject: host\r\n\r\nbody"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	mid := info.ID

	_, n0, err := s.CreateAttachment(mid, mapi.PropertyValues{{Tag: mapi.PrAttachLongFilename, Value: "a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	_, n1, err := s.CreateAttachment(mid, mapi.PropertyValues{{Tag: mapi.PrAttachLongFilename, Value: "b.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if n0 != 0 || n1 != 1 {
		t.Fatalf("attach numbers = %d,%d, want 0,1", n0, n1)
	}

	if err := s.DeleteAttachment(mid, n0); err != nil {
		t.Fatal(err)
	}
	msg, err := s.OpenMessage(mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("after delete: %d attachments, want 1", len(msg.Attachments))
	}
	if v, _ := msg.Attachments[0].Props.Get(mapi.PrAttachNum); v != int32(1) {
		t.Errorf("surviving attach number = %v, want 1 (stable across sibling delete)", v)
	}
	if v, _ := msg.Attachments[0].Props.Get(mapi.PrAttachLongFilename); v != "b.txt" {
		t.Errorf("surviving attachment = %v, want b.txt", v)
	}

	if err := s.DeleteAttachment(mid, n0); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-delete of a gone number: err = %v, want ErrNotFound", err)
	}

	_, n2, err := s.CreateAttachment(mid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 2 {
		t.Errorf("next attach number = %d, want 2 (continues past max, no reuse of 0)", n2)
	}
}

// TestImportAssignsAttachNumber confirms the import path stamps a stable attach
// number on each attachment it stores (mail import carries none of its own), so
// the read path resolves an imported attachment by number exactly as it does a
// ROP-created one — the two creation paths share one numbering scheme.
func TestImportAssignsAttachNumber(t *testing.T) {
	s := openSeededStore(t)
	raw := "From: a@example.test\r\nSubject: att\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--B\r\nContent-Type: application/pdf; name=\"r.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"r.pdf\"\r\n\r\n%PDF data\r\n--B--\r\n"
	info, err := s.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.OpenMessage(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("imported message has %d attachments, want 1", len(msg.Attachments))
	}
	if v, _ := msg.Attachments[0].Props.Get(mapi.PrAttachNum); v != int32(0) {
		t.Errorf("imported attach number = %v, want 0", v)
	}
}
