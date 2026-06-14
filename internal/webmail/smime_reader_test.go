package webmail

import (
	"crypto"
	"crypto/x509"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/smime"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// identityP12 builds an email identity and returns it as a PKCS#12 (for upload)
// along with the key and certificate (for signing/encrypting test fixtures).
func identityP12(t *testing.T, cn, pass string) ([]byte, crypto.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, cert := genWebmailIdentity(t, cn)
	p12, err := pkcs12.Modern.Encode(key, cert, nil, pass)
	if err != nil {
		t.Fatal(err)
	}
	return p12, key, cert
}

// storeRaw appends a raw message to the mailbox's INBOX and returns its UID.
func storeRaw(t *testing.T, path string, raw []byte) uint32 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}

func readMessage(t *testing.T, c *http.Client, ts string, uid uint32) string {
	t.Helper()
	_, body := get(t, c, ts+"/message?folder=INBOX&uid="+strconv.FormatUint(uint64(uid), 10))
	return body
}

// TestReaderSignedVerified stores a signed message whose signer certificate
// matches the From address and confirms the reader names the signer (an honest
// "Signed by", not an over-claimed "verified") and shows the signed content.
func TestReaderSignedVerified(t *testing.T) {
	path := emptyMailbox(t)
	_, key, cert := identityP12(t, "alice@hermex.test", "pass")
	signed, err := smime.Sign([]byte("Content-Type: text/plain; charset=utf-8\r\n\r\nVerified body text.\r\n"), cert, key)
	if err != nil {
		t.Fatal(err)
	}
	msg := append([]byte("From: alice@hermex.test\r\nTo: alice@hermex.test\r\nSubject: signed note\r\n"), signed...)
	uid := storeRaw(t, path, msg)

	ts := newTestServer(t, path)
	body := readMessage(t, authedClient(t, ts), ts.URL, uid)
	for _, want := range []string{"Signed by alice@hermex.test", "smime-banner ok", "Verified body text."} {
		if !strings.Contains(body, want) {
			t.Errorf("signed reader missing %q", want)
		}
	}
	// The banner must not over-claim trust: no CA chain is checked here.
	if strings.Contains(body, "Signed — verified") {
		t.Error("banner must not claim plain 'verified' without a trust anchor")
	}
}

// TestReaderSignedSenderMismatch is the security-critical case: a cryptographically
// valid signature whose certificate speaks for a DIFFERENT address than From. This
// is the cheap spoof (a self-signed cert minted for the victim's name, sent under
// the victim's From by an attacker who controls neither). The signature is valid,
// so the content is still shown, but the banner must WARN of the mismatch and must
// NOT render the positive (green) state.
func TestReaderSignedSenderMismatch(t *testing.T) {
	path := emptyMailbox(t)
	_, key, cert := identityP12(t, "mallory@evil.test", "pass")
	signed, err := smime.Sign([]byte("Content-Type: text/plain; charset=utf-8\r\n\r\nTrust me, I am alice.\r\n"), cert, key)
	if err != nil {
		t.Fatal(err)
	}
	msg := append([]byte("From: alice@hermex.test\r\nTo: alice@hermex.test\r\nSubject: spoofed\r\n"), signed...)
	uid := storeRaw(t, path, msg)

	ts := newTestServer(t, path)
	body := readMessage(t, authedClient(t, ts), ts.URL, uid)
	for _, want := range []string{"does NOT match the sender", "mallory@evil.test", "alice@hermex.test", "smime-banner warn"} {
		if !strings.Contains(body, want) {
			t.Errorf("mismatch banner missing %q", want)
		}
	}
	if strings.Contains(body, "smime-banner ok") {
		t.Error("a signer/sender mismatch must not render the positive banner")
	}
}

// TestReaderEncryptedLocked confirms that without an unlocked identity the reader
// shows the unlock prompt and not the content.
func TestReaderEncryptedLocked(t *testing.T) {
	path := emptyMailbox(t)
	_, _, cert := identityP12(t, "alice@hermex.test", "pass")
	env, err := smime.Encrypt([]byte("Content-Type: text/plain\r\n\r\ntop secret\r\n"), []*x509.Certificate{cert})
	if err != nil {
		t.Fatal(err)
	}
	msg := append([]byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: secret\r\n"), env...)
	uid := storeRaw(t, path, msg)

	ts := newTestServer(t, path)
	body := readMessage(t, authedClient(t, ts), ts.URL, uid)
	if !strings.Contains(body, "Unlock your certificate") {
		t.Error("locked reader should prompt to unlock")
	}
	if strings.Contains(body, "top secret") {
		t.Error("locked reader must not reveal the encrypted content")
	}
}

// TestReaderEncryptedUnlocked uploads an identity (unlocking the session), stores
// a message encrypted to it, and confirms the reader decrypts and shows it.
func TestReaderEncryptedUnlocked(t *testing.T) {
	path := emptyMailbox(t)
	p12, _, cert := identityP12(t, "alice@hermex.test", "pass")

	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	if code, _ := postMultipart(t, c, ts.URL+"/smime",
		map[string]string{"action": "upload", "passphrase": "pass"},
		map[string][]byte{"p12": p12}); code != http.StatusOK {
		t.Fatalf("identity upload failed: %d", code)
	}

	env, err := smime.Encrypt([]byte("Content-Type: text/plain\r\n\r\ndecrypted body here\r\n"), []*x509.Certificate{cert})
	if err != nil {
		t.Fatal(err)
	}
	msg := append([]byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: secret\r\n"), env...)
	uid := storeRaw(t, path, msg)

	body := readMessage(t, c, ts.URL, uid)
	for _, want := range []string{"Encrypted", "decrypted body here"} {
		if !strings.Contains(body, want) {
			t.Errorf("unlocked reader missing %q", want)
		}
	}
}
