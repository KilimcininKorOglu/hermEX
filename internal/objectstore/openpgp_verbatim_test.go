package objectstore

import (
	"bytes"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestOpenPGPEncryptedIsServedVerbatim proves an OpenPGP/MIME encrypted message is
// served as it arrived. Re-synthesized, it lost its multipart/encrypted type and
// protocol, and no OpenPGP client could open it.
func TestOpenPGPEncryptedIsServedVerbatim(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := []byte("From: sender@example.org\r\nTo: recipient@example.org\r\nSubject: sealed\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/encrypted; protocol=\"application/pgp-encrypted\"; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: application/pgp-encrypted\r\n\r\nVersion: 1\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\n\r\n-----BEGIN PGP MESSAGE-----\r\n\r\nx\r\n-----END PGP MESSAGE-----\r\n--b--\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("served form differs from the arrival bytes:\n%s", got)
	}
}
