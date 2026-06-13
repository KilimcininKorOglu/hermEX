package objectstore

import (
	"os"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestAppendMessage delivers a raw multipart message and verifies the full
// path: the object is persisted, the wire form is re-synthesized and cached as
// the served eml, the message is indexed with the first UID, the index RFC822
// size equals the served bytes, and the served form re-imports to the same
// semantic content as delivered.
func TestAppendMessage(t *testing.T) {
	s := openSeededStore(t)

	raw := []byte(strings.Join([]string{
		"From: Ali Veli <ali@example.test>",
		"To: Ayse Yilmaz <ayse@example.test>",
		"Subject: deneme konusu",
		"Date: Wed, 15 Nov 2023 10:13:20 +0000",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="b1"`,
		"",
		"--b1",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		"Merhaba, bu bir deneme mesajıdır.",
		"--b1",
		`Content-Type: application/octet-stream; name="ek.bin"`,
		`Content-Disposition: attachment; filename="ek.bin"`,
		"Content-Transfer-Encoding: base64",
		"",
		"aGVsbG8gd29ybGQ=",
		"--b1--",
		"",
	}, "\r\n"))

	delivered := time.Unix(1700043200, 0)

	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, delivered, 0)
	if err != nil {
		t.Fatal(err)
	}
	if info.UID != 1 {
		t.Errorf("uid = %d, want 1", info.UID)
	}
	if info.Size <= 0 {
		t.Fatalf("size = %d, want > 0", info.Size)
	}
	if !info.InternalDate.Equal(delivered.UTC()) {
		t.Errorf("internal date = %v, want %v", info.InternalDate, delivered.UTC())
	}

	// The object was persisted with one recipient and one attachment.
	if n := countRows(t, s, `SELECT COUNT(*) FROM recipients WHERE message_id=?`, info.ID); n != 1 {
		t.Errorf("recipient rows = %d, want 1", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM attachments WHERE message_id=?`, info.ID); n != 1 {
		t.Errorf("attachment rows = %d, want 1", n)
	}

	// The served eml was cached, and its size matches the reported and indexed
	// size (the RFC822.SIZE invariant: index size == served bytes).
	fi, err := os.Stat(s.emlPath(midString(uint64(info.ID))))
	if err != nil {
		t.Fatalf("eml cache missing: %v", err)
	}
	if fi.Size() != info.Size {
		t.Errorf("eml file size %d != reported size %d", fi.Size(), info.Size)
	}
	var idxUID, idxSize int64
	var idxSubject string
	if err := s.idxdb.QueryRow(
		`SELECT uid, size, subject FROM messages WHERE message_id=?`, info.ID).
		Scan(&idxUID, &idxSize, &idxSubject); err != nil {
		t.Fatal(err)
	}
	if idxUID != 1 || idxSize != info.Size {
		t.Errorf("index uid=%d size=%d, want 1/%d", idxUID, idxSize, info.Size)
	}
	if idxSubject != "deneme konusu" {
		t.Errorf("index subject = %q", idxSubject)
	}

	// The served form re-imports to the same semantic content as delivered.
	eml, err := os.ReadFile(s.emlPath(midString(uint64(info.ID))))
	if err != nil {
		t.Fatal(err)
	}
	served, err := oxcmail.Import(eml, oxcmail.Options{Resolver: s.GetNamedPropIDs})
	if err != nil {
		t.Fatalf("re-import served eml: %v", err)
	}
	sm := asMap(served.Props)
	if sm[mapi.PrSubject] != "deneme konusu" {
		t.Errorf("served subject = %v", sm[mapi.PrSubject])
	}
	if sm[mapi.PrSentRepresentingSmtpAddress] != "ali@example.test" {
		t.Errorf("served from = %v", sm[mapi.PrSentRepresentingSmtpAddress])
	}
	body, _ := sm[mapi.PrBody].(string)
	if !strings.Contains(body, "deneme mesajıdır") {
		t.Errorf("served body lost its content: %q", body)
	}
	if len(served.Recipients) != 1 {
		t.Fatalf("served recipients = %d, want 1", len(served.Recipients))
	}
	if a := asMap(served.Recipients[0]); a[mapi.PrSmtpAddress] != "ayse@example.test" {
		t.Errorf("served recipient = %v", a[mapi.PrSmtpAddress])
	}
	if len(served.Attachments) != 1 {
		t.Fatalf("served attachments = %d, want 1", len(served.Attachments))
	}
	att := asMap(served.Attachments[0].Props)
	if att[mapi.PrAttachLongFilename] != "ek.bin" {
		t.Errorf("served attachment filename = %v", att[mapi.PrAttachLongFilename])
	}
	if data, ok := att[mapi.PrAttachDataBin].([]byte); !ok || string(data) != "hello world" {
		t.Errorf("served attachment payload = %q", data)
	}
}

// countRows runs a COUNT(*) query against the object store.
func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.objdb.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
