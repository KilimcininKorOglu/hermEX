package objectstore

import (
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
