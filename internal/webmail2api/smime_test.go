package webmail2api

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/smime"
)

// makeTestIdentity builds a throwaway self-signed identity, returning the cert in
// DER plus the cert and private key in PEM (what the SPA uploads).
func makeTestIdentity(t *testing.T) (certDER []byte, certPEM, keyPEM string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "alice@hermex.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	cp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kp := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return der, string(cp), string(kp)
}

// smimePost issues an authenticated POST whose session points at dir.
func smimePost(t *testing.T, srv *Server, secret []byte, dir string, body any) *httptest.ResponseRecorder {
	t.Helper()
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/smime/certificate", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// smimeGet/smimeDelete issue authenticated read/delete requests.
func smimeReq(t *testing.T, srv *Server, secret []byte, method, dir string) *httptest.ResponseRecorder {
	t.Helper()
	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(method, "/api/v1/smime/certificate", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestSmimeUploadRoundTrip proves an uploaded PEM cert+key is stored encrypted at
// rest and unlocks to a usable signing identity.
func TestSmimeUploadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if st, err := objectstore.Open(dir); err != nil {
		t.Fatalf("open: %v", err)
	} else {
		st.Close()
	}
	_, certPEM, keyPEM := makeTestIdentity(t)
	secret := []byte("smime-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", secret, "", false)

	if rec := smimePost(t, srv, secret, dir, map[string]string{"cert": certPEM, "key": keyPEM}); rec.Code != 200 {
		t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	key, cert, ok := unlockSmimeIdentity(st, secret)
	if !ok {
		t.Fatal("stored identity did not unlock")
	}
	signed, err := smime.Sign([]byte("hello"), cert, key)
	if err != nil {
		t.Fatalf("sign with stored key: %v", err)
	}
	signer, content, err := smime.Verify(signed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if string(content) != "hello" || signer.Subject.CommonName != "alice@hermex.test" {
		t.Errorf("round-trip = %q by %q", content, signer.Subject.CommonName)
	}

	// View reflects the stored identity, delete clears it.
	if rec := smimeReq(t, srv, secret, http.MethodGet, dir); rec.Code != 200 {
		t.Errorf("view = %d", rec.Code)
	}
	if rec := smimeReq(t, srv, secret, http.MethodDelete, dir); rec.Code != 200 {
		t.Errorf("delete = %d", rec.Code)
	}
	if _, _, ok := unlockSmimeIdentity(st, secret); ok {
		t.Error("identity still present after delete")
	}
}

// TestApplySmimeSign proves the send path signs a built message with the stored
// identity, producing a verifiable S/MIME message.
func TestApplySmimeSign(t *testing.T) {
	dir := t.TempDir()
	_, certPEM, keyPEM := makeTestIdentity(t)
	secret := []byte("smime-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", secret, "", false)
	if rec := smimePost(t, srv, secret, dir, map[string]string{"cert": certPEM, "key": keyPEM}); rec.Code != 200 {
		t.Fatalf("upload: %d", rec.Code)
	}
	out, err := srv.applySmime(dir, []byte("From: a@b.test\r\nSubject: hi\r\n\r\nbody\r\n"), []string{"x@y.test"}, true, false)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !smime.IsSigned(out) {
		t.Fatal("send output is not signed")
	}
	if _, _, err := smime.Verify(out); err != nil {
		t.Errorf("signed output does not verify: %v", err)
	}
}

// TestApplySmimeNoIdentity proves signing without an uploaded identity errors
// rather than sending an unsigned message silently.
func TestApplySmimeNoIdentity(t *testing.T) {
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", []byte("s"), "", false)
	if _, err := srv.applySmime(t.TempDir(), []byte("x"), nil, true, false); err == nil {
		t.Error("signing without an identity should error")
	}
}

// TestSmimeReadRoundTrip proves the full chain: a signed+encrypted message is
// decrypted and verified on read, recovering the body and the signer.
func TestSmimeReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	_, certPEM, keyPEM := makeTestIdentity(t)
	secret := []byte("smime-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", secret, "", false)
	if rec := smimePost(t, srv, secret, dir, map[string]string{"cert": certPEM, "key": keyPEM}); rec.Code != 200 {
		t.Fatalf("upload: %d", rec.Code)
	}
	// Store alice's own cert as a recipient cert so she can encrypt to herself.
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id, _, _ := st.GetSmimeIdentity()
	if err := st.PutRecipientCert("alice@hermex.test", id.Cert); err != nil {
		t.Fatalf("put recipient cert: %v", err)
	}
	st.Close()

	msg := []byte("From: bob@x.test\r\nTo: alice@hermex.test\r\nSubject: secret\r\n\r\ntop secret body\r\n")
	out, err := srv.applySmime(dir, msg, []string{"alice@hermex.test"}, true, true)
	if err != nil {
		t.Fatalf("sign+encrypt: %v", err)
	}

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	content, status := srv.smimeOpen(st2, out)
	if !status.Encrypted {
		t.Error("not detected as encrypted")
	}
	if !status.Signed || !status.Verified {
		t.Error("not verified-signed")
	}
	if !bytes.Contains(content, []byte("top secret body")) {
		t.Errorf("decrypted body not recovered: %s", content)
	}
}

// TestSmimeUploadMismatch proves a cert and key that do not pair are rejected.
func TestSmimeUploadMismatch(t *testing.T) {
	_, certPEM, _ := makeTestIdentity(t)
	_, _, otherKeyPEM := makeTestIdentity(t)
	secret := []byte("smime-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", secret, "", false)
	if rec := smimePost(t, srv, secret, t.TempDir(), map[string]string{"cert": certPEM, "key": otherKeyPEM}); rec.Code != http.StatusBadRequest {
		t.Errorf("mismatched pair = %d, want 400", rec.Code)
	}
}
