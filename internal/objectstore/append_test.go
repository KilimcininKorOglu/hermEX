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

	info := mustAppendMessage(t, s, mapi.PrivateFIDInbox, raw, delivered, 0)
	wantEq(t, "uid", info.UID, uint32(1))
	if info.Size <= 0 {
		t.Fatalf("size = %d, want > 0", info.Size)
	}
	if !info.InternalDate.Equal(delivered.UTC()) {
		t.Errorf("internal date = %v, want %v", info.InternalDate, delivered.UTC())
	}

	// The object was persisted with one recipient and one attachment.
	wantRows(t, s, "recipient rows", 1, `SELECT COUNT(*) FROM recipients WHERE message_id=?`, info.ID)
	wantRows(t, s, "attachment rows", 1, `SELECT COUNT(*) FROM attachments WHERE message_id=?`, info.ID)

	wantCachedEMLSize(t, s, info)
	wantServedContent(t, s, info)
}

// wantCachedEMLSize proves the served eml was cached and its length matches
// both the reported and the indexed size (the RFC822.SIZE invariant: index size
// == served bytes).
func wantCachedEMLSize(t *testing.T, s *Store, info MessageInfo) {
	t.Helper()
	fi, err := os.Stat(s.emlPath(midString(uint64(info.ID))))
	if err != nil {
		t.Fatalf("eml cache missing: %v", err)
	}
	wantEq(t, "eml file size", fi.Size(), info.Size)
	var idxUID, idxSize int64
	var idxSubject string
	mustScan(t, s.idxdb.QueryRow(`SELECT uid, size, subject FROM messages WHERE message_id=?`, info.ID),
		&idxUID, &idxSize, &idxSubject)
	wantEq(t, "index uid", idxUID, int64(1))
	wantEq(t, "index size", idxSize, info.Size)
	wantEq(t, "index subject", idxSubject, "deneme konusu")
}

// wantServedContent re-imports the served wire form and checks it carries the
// same semantic content as delivered.
func wantServedContent(t *testing.T, s *Store, info MessageInfo) {
	t.Helper()
	eml, err := os.ReadFile(s.emlPath(midString(uint64(info.ID))))
	mustNoErr(t, "read served eml", err)
	served, err := oxcmail.Import(eml, oxcmail.Options{Resolver: s.GetNamedPropIDs})
	mustNoErr(t, "re-import served eml", err)

	sm := asMap(served.Props)
	wantEq(t, "served subject", sm[mapi.PrSubject], any("deneme konusu"))
	wantEq(t, "served from", sm[mapi.PrSentRepresentingSmtpAddress], any("ali@example.test"))
	body, _ := sm[mapi.PrBody].(string)
	wantContains(t, "served body", body, "deneme mesajıdır")

	if len(served.Recipients) != 1 {
		t.Fatalf("served recipients = %d, want 1", len(served.Recipients))
	}
	wantEq(t, "served recipient", asMap(served.Recipients[0])[mapi.PrSmtpAddress], any("ayse@example.test"))

	if len(served.Attachments) != 1 {
		t.Fatalf("served attachments = %d, want 1", len(served.Attachments))
	}
	att := asMap(served.Attachments[0].Props)
	wantEq(t, "served attachment filename", att[mapi.PrAttachLongFilename], any("ek.bin"))
	data, _ := att[mapi.PrAttachDataBin].([]byte)
	wantEq(t, "served attachment payload", string(data), "hello world")
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
