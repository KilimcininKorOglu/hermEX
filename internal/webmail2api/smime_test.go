package webmail2api

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
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

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// TestSmimeServerMode proves a .p12 uploaded in server mode is stored encrypted at
// rest, unlocks server-side, and signs — the server-held-key path.
func TestSmimeServerMode(t *testing.T) {
	dir := t.TempDir()
	if st, err := objectstore.Open(dir); err != nil {
		t.Fatalf("open: %v", err)
	} else {
		st.Close()
	}
	_, certPEM, keyPEM := makeTestIdentity(t)
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	cert, _ := x509.ParseCertificate(pair.Certificate[0])
	p12, err := pkcs12.Modern.Encode(pair.PrivateKey, cert, nil, "userpass")
	if err != nil {
		t.Fatalf("encode p12: %v", err)
	}
	secret := []byte("smime-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", secret, "", false)

	rec := smimePost(t, srv, secret, dir, map[string]string{
		"mode": "server", "p12": base64.StdEncoding.EncodeToString(p12), "passphrase": "userpass",
	})
	if rec.Code != 200 {
		t.Fatalf("server upload = %d: %s", rec.Code, rec.Body.String())
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	id, ok, _ := st.GetSmimeIdentity()
	if !ok || id.Mode != "server" || len(id.P12) == 0 {
		t.Fatalf("not stored as server mode: ok=%v mode=%q p12=%d", ok, id.Mode, len(id.P12))
	}
	if _, _, ok := unlockSmimeIdentity(st, secret); !ok {
		t.Fatal("server identity did not unlock")
	}
	out, err := srv.applySmime(dir, []byte("From: a@b.test\r\nSubject: hi\r\n\r\nbody\r\n"), nil, true, false)
	if err != nil {
		t.Fatalf("server-mode sign: %v", err)
	}
	if !smime.IsSigned(out) {
		t.Error("server-mode output is not signed")
	}
}

// makeTestIdentity builds a throwaway self-signed identity, returning the cert in
// DER plus the cert and private key in PEM.
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

// smimePost issues an authenticated POST to the certificate endpoint.
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

// smimeReq issues an authenticated GET/DELETE to the certificate endpoint.
func smimeReq(t *testing.T, srv *Server, secret []byte, method, dir string) *httptest.ResponseRecorder {
	t.Helper()
	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(method, "/api/v1/smime/certificate", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestSmimePublishViewDelete proves a public certificate publishes, reads back,
// and clears — the only server-side role in the client-side S/MIME model.
func TestSmimePublishViewDelete(t *testing.T) {
	dir := t.TempDir()
	if st, err := objectstore.Open(dir); err != nil {
		t.Fatalf("open: %v", err)
	} else {
		st.Close()
	}
	_, certPEM, _ := makeTestIdentity(t)
	secret := []byte("smime-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", secret, "", false)

	if rec := smimePost(t, srv, secret, dir, map[string]string{"cert": certPEM}); rec.Code != 200 {
		t.Fatalf("publish = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := smimeReq(t, srv, secret, http.MethodGet, dir); rec.Code != 200 {
		t.Errorf("view = %d", rec.Code)
	}
	if rec := smimeReq(t, srv, secret, http.MethodDelete, dir); rec.Code != 200 {
		t.Errorf("delete = %d", rec.Code)
	}
	rec := smimeReq(t, srv, secret, http.MethodGet, dir)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["hasKeys"] != false {
		t.Errorf("after delete, view = %v, want hasKeys:false", got)
	}
}

// TestSmimeRejectPrivateKey proves the server refuses a request carrying a private
// key — the key must stay in the browser.
func TestSmimeRejectPrivateKey(t *testing.T) {
	_, certPEM, keyPEM := makeTestIdentity(t)
	secret := []byte("smime-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", secret, "", false)
	if rec := smimePost(t, srv, secret, t.TempDir(), map[string]string{"cert": certPEM, "key": keyPEM}); rec.Code != http.StatusBadRequest {
		t.Errorf("request with a private key = %d, want 400", rec.Code)
	}
}
