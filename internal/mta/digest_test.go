package mta

import (
	"io"
	"mime/quotedprintable"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/quarantine"
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
	defer st.Close()
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
	defer st.Close()
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
	_, body, ok := strings.Cut(string(raw), "\r\n\r\n")
	if !ok {
		t.Fatal("digest has no body separator")
	}
	dec, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	return string(dec)
}

// firstToken extracts the token from the first "Release: ...?t=<token>" line.
func firstToken(t *testing.T, body string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, "?t=")
	if !ok {
		t.Fatal("digest body has no release link")
	}
	tok, _, _ := strings.Cut(after, "\r")
	tok, _, _ = strings.Cut(tok, "\n")
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
	// mailbox — verifying the token is the load-bearing check, not just the URL text.
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
	inboxRaw(t, maildir, 1) // still exactly the one digest
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
	r.Run() // first digest covers the two old messages

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
	inboxRaw(t, maildir, 0)
}
