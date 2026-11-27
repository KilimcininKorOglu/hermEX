package mta

import (
	"database/sql"
	"errors"
	"io"
	"mime/quotedprintable"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/quarantine"

	_ "modernc.org/sqlite"
)

// fakeDigestDir is an in-memory DigestDirectory for the worker tests.
type fakeDigestDir struct {
	users     []directory.UserInfo
	maildirs  map[string]string
	watermark map[string]uint32
}

func (f *fakeDigestDir) ListUsers() ([]directory.UserInfo, error) { return f.users, nil }
func (f *fakeDigestDir) Resolve(addr string) (string, bool)       { m, ok := f.maildirs[addr]; return m, ok }
func (f *fakeDigestDir) GetDigestWatermark(maildir string) (uint32, error) {
	return f.watermark[maildir], nil
}
func (f *fakeDigestDir) SetDigestWatermark(maildir string, uid uint32) error {
	f.watermark[maildir] = uid
	return nil
}

var digestSecret = []byte("a-32-byte-or-longer-digest-secret!!!")

// appendJunk seeds a Junk message and returns its UID.
func appendJunk(t *testing.T, maildir, subject string) uint32 {
	t.Helper()
	st, err := objectstore.Open(maildir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	info, err := st.AppendMessage(int64(mapi.PrivateFIDJunk),
		[]byte("Subject: "+subject+"\r\nFrom: spam@bad.example\r\n\r\nbuy now"), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}

func newDigestRunner(dir DigestDirectory) *DigestRunner {
	return &DigestRunner{
		Dir:      dir,
		Secret:   digestSecret,
		BaseURL:  "https://mail.test",
		Hostname: "mail.test",
		TokenTTL: 8 * 24 * time.Hour,
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
}

// inboxRaw returns the newest inbox message's raw bytes, failing unless exactly want
// messages are present.
func inboxRaw(t *testing.T, maildir string, want int) []byte {
	t.Helper()
	st, err := objectstore.Open(maildir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != want {
		t.Fatalf("inbox has %d messages, want %d", len(msgs), want)
	}
	if want == 0 {
		return nil
	}
	raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), msgs[len(msgs)-1].UID)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// decodeBody returns the quoted-printable-decoded body of a digest message.
func decodeBody(t *testing.T, raw []byte) string {
	t.Helper()
	s := string(raw)
	i := strings.Index(s, "\r\n\r\n")
	if i < 0 {
		t.Fatal("digest has no body separator")
	}
	body := s[i+len("\r\n\r\n"):]
	dec, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	return string(dec)
}

// firstToken extracts the token from the first "Release: ...?t=<token>" line.
func firstToken(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "?t=")
	if i < 0 {
		t.Fatal("digest body has no release link")
	}
	tok := body[i+len("?t="):]
	if j := strings.IndexAny(tok, "\r\n"); j >= 0 {
		tok = tok[:j]
	}
	return strings.TrimSpace(tok)
}

// TestDigestRunDeliversAndWatermarks proves a mailbox with quarantined mail gets one
// digest in its inbox whose release link verifies to the right mailbox and message,
// the watermark advances to the newest message, and a second run with nothing new
// sends nothing.
func TestDigestRunDeliversAndWatermarks(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "alice")
	uid1 := appendJunk(t, maildir, "cheap pills")
	uid2 := appendJunk(t, maildir, "you have won")

	dir := &fakeDigestDir{
		users:     []directory.UserInfo{{Username: "alice@hermex.test"}},
		maildirs:  map[string]string{"alice@hermex.test": maildir},
		watermark: map[string]uint32{},
	}
	r := newDigestRunner(dir)

	if sent := r.Run(); sent != 1 {
		t.Fatalf("first run sent %d digests, want 1", sent)
	}

	body := decodeBody(t, inboxRaw(t, maildir, 1))
	if !strings.Contains(body, "cheap pills") || !strings.Contains(body, "you have won") {
		t.Errorf("digest body missing quarantined subjects:\n%s", body)
	}
	// The release link actually authorizes releasing one of these messages from this
	// mailbox, verifying the token is the load-bearing check, not just the URL text.
	claims, err := quarantine.Verify(digestSecret, firstToken(t, body), r.Now())
	if err != nil {
		t.Fatalf("release token does not verify: %v", err)
	}
	if claims.Mailbox != "alice@hermex.test" || (claims.UID != uid1 && claims.UID != uid2) {
		t.Errorf("token claims = %+v, want this mailbox and one of UIDs %d/%d", claims, uid1, uid2)
	}
	if got := dir.watermark[maildir]; got != uid2 {
		t.Errorf("watermark = %d, want the newest UID %d", got, uid2)
	}

	// Nothing new since the watermark → no second digest.
	if sent := r.Run(); sent != 0 {
		t.Errorf("second run with nothing new sent %d digests, want 0", sent)
	}
	wantEq(t, "inbox count after no-op run", inboxCount(t, maildir), 1)
}

// TestDigestOnlyNewSinceWatermark proves a later run summarizes only messages newer
// than the watermark, not the whole Junk folder again.
func TestDigestOnlyNewSinceWatermark(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "bob")
	appendJunk(t, maildir, "old spam one")
	appendJunk(t, maildir, "old spam two")

	dir := &fakeDigestDir{
		users:     []directory.UserInfo{{Username: "bob@hermex.test"}},
		maildirs:  map[string]string{"bob@hermex.test": maildir},
		watermark: map[string]uint32{},
	}
	r := newDigestRunner(dir)
	wantEq(t, "first digest run count", r.Run(), 1) // first digest covers the two old messages

	uid3 := appendJunk(t, maildir, "fresh spam three")
	if sent := r.Run(); sent != 1 {
		t.Fatalf("run after a new message sent %d, want 1", sent)
	}
	body := decodeBody(t, inboxRaw(t, maildir, 2)) // first + second digest
	if !strings.Contains(body, "fresh spam three") {
		t.Errorf("second digest missing the new message:\n%s", body)
	}
	if strings.Contains(body, "old spam one") || strings.Contains(body, "old spam two") {
		t.Errorf("second digest re-listed already-summarized messages:\n%s", body)
	}
	if got := dir.watermark[maildir]; got != uid3 {
		t.Errorf("watermark = %d, want %d", got, uid3)
	}
}

// TestDigestDisabledWithoutSecret proves the run is a no-op without a signing secret
// or base URL, so no unsigned or unreachable links are ever emitted.
func TestDigestDisabledWithoutSecret(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "carol")
	appendJunk(t, maildir, "spam")
	dir := &fakeDigestDir{
		users:     []directory.UserInfo{{Username: "carol@hermex.test"}},
		maildirs:  map[string]string{"carol@hermex.test": maildir},
		watermark: map[string]uint32{},
	}
	r := newDigestRunner(dir)
	r.Secret = nil
	if sent := r.Run(); sent != 0 {
		t.Errorf("run without a secret sent %d digests, want 0", sent)
	}
	wantEq(t, "inbox count without secret", inboxCount(t, maildir), 0)

	r = newDigestRunner(dir)
	r.BaseURL = "   "
	if sent := r.Run(); sent != 0 {
		t.Errorf("run with blank BaseURL sent %d digests, want 0", sent)
	}
	wantEq(t, "inbox count with blank BaseURL", inboxCount(t, maildir), 0)
}

// TestDigestSkipsAMissingMailboxWithoutCreatingIt proves the digest run never
// creates a mailbox. A mailbox is made when its account is created; a missing
// one (a deleted directory, an unmounted volume) has no quarantine to report, and
// writing a digest into a fresh empty mailbox there would hide the loss.
func TestDigestSkipsAMissingMailboxWithoutCreatingIt(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "gone")
	r := newDigestRunner(&fakeDigestDir{
		users:     []directory.UserInfo{{Username: "dave@hermex.test"}},
		maildirs:  map[string]string{"dave@hermex.test": maildir},
		watermark: map[string]uint32{},
	})
	if sent := r.Run(); sent != 0 {
		t.Errorf("run over a missing mailbox sent %d digests, want 0", sent)
	}
	if _, err := os.Stat(maildir); !os.IsNotExist(err) {
		t.Errorf("the digest run created the missing mailbox: %v", err)
	}
}

// TestDigestLogsJunkListReadFailure proves a Junk read fault on an opened mailbox
// store is recorded, not silently swallowed. The store opens fine but ListMessages
// fails (a transient lock or a damaged index); the run must skip the mailbox without
// failing, and the operator must see a digest.list event rather than a clean run.
func TestDigestLogsJunkListReadFailure(t *testing.T) {
	maildir := t.TempDir()
	appendJunk(t, maildir, "spammy") // provisions the store and seeds a junk message

	// Break the index so Open still succeeds but ListMessages fails: drop one
	// column the listing scans. Open's index check reads each table and rebuilds an
	// index it cannot read, so dropping the whole table would be repaired instead of
	// observed; a missing column leaves every probe readable and still faults the
	// listing.
	db, err := sql.Open("sqlite", filepath.Join(maildir, "imapindex.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`ALTER TABLE messages DROP COLUMN preview`)
	if err != nil {
		mustClose(t, db)
		t.Fatal(err)
	}
	n, err := res.RowsAffected()
	mustNoErr(t, "drop column RowsAffected", err)
	wantEq(t, "drop column rows affected", n, int64(0))
	mustClose(t, db)

	sink := &captureSink{}
	r := newDigestRunner(&fakeDigestDir{
		maildirs:  map[string]string{"alice@test": maildir},
		watermark: map[string]uint32{},
	})
	r.Logger = logging.New(sink)

	delivered, err := r.digestMailbox("alice@test", maildir)
	if err != nil {
		t.Fatalf("a read fault must not fail the whole run: %v", err)
	}
	if delivered {
		t.Error("no digest should be delivered when the Junk read failed")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	found := false
	for _, e := range sink.events {
		if e.Name == "digest.list" {
			found = true
		}
	}
	if !found {
		t.Error("the Junk read failure was not logged; the operator would see a clean run")
	}
}

// TestDigestFlattensUntrustedSubjectAndSurfacesCloseError verifies hostile CRLF
// in Junk subjects is flattened and a store Close failure is surfaced on delivery.
func TestDigestFlattensUntrustedSubjectAndSurfacesCloseError(t *testing.T) {
	maildir := filepath.Join(t.TempDir(), "alice")
	appendJunk(t, maildir, "offer\r\nInjected: bad")
	dir := &fakeDigestDir{
		users:     []directory.UserInfo{{Username: "alice@hermex.test"}},
		maildirs:  map[string]string{"alice@hermex.test": maildir},
		watermark: map[string]uint32{},
	}
	r := newDigestRunner(dir)
	raw, err := r.buildDigest("alice@hermex.test", []objectstore.MessageInfo{{
		UID:          1,
		Subject:      "offer\r\nInjected: bad",
		Sender:       "bad@x.test\r\nFrom: evil@x.test",
		InternalDate: time.Unix(1_700_000_000, 0),
	}}, r.Now())
	mustNoErr(t, "build digest", err)
	body := decodeBody(t, raw)
	wantContains(t, "flattened subject", body, "offer Injected: bad")
	wantContains(t, "flattened sender", body, "bad@x.test From: evil@x.test")

	prevClose := closeMailbox
	closeMailbox = func(st *objectstore.Store) error {
		mustClose(t, st)
		return errors.New("close failed")
	}
	t.Cleanup(func() { closeMailbox = prevClose })
	sink := &captureSink{}
	r.Logger = logging.New(sink)

	r.Run()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if _, ok := findEvent(sink.events, "digest.deliver"); !ok {
		t.Error("digest close failure emitted no digest.deliver event")
	}
}
