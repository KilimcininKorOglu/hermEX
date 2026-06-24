package webmail2api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// makeTestCert builds a throwaway self-signed certificate (DER) for the S/MIME
// view test.
func makeTestCert(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
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
	return der
}

// smimeRequest issues an authenticated request whose session points at dir.
func smimeRequest(t *testing.T, srv *Server, secret []byte, method, dir string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	req := httptest.NewRequest(method, "/api/v1/smime/certificate", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestSmimeCertViewDelete proves the model-independent half: a stored identity's
// details are read back, and delete removes it.
func TestSmimeCertViewDelete(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{Cert: makeTestCert(t), P12: []byte("encrypted-p12")}); err != nil {
		t.Fatalf("set identity: %v", err)
	}
	st.Close()

	secret := []byte("smime-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", secret, "", false)

	rec := smimeRequest(t, srv, secret, http.MethodGet, dir)
	var info map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info["subject"] == nil || info["hasPrivateKey"] != true {
		t.Fatalf("view = %v, want subject + hasPrivateKey", info)
	}

	if rec := smimeRequest(t, srv, secret, http.MethodDelete, dir); rec.Code != 200 {
		t.Fatalf("delete = %d", rec.Code)
	}

	rec = smimeRequest(t, srv, secret, http.MethodGet, dir)
	var after map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if after["hasKeys"] != false {
		t.Errorf("after delete = %v, want hasKeys:false", after)
	}
}

// TestSmimeUploadPending proves the upload reports a clear pending state rather
// than silently storing the key under an unchosen at-rest model.
func TestSmimeUploadPending(t *testing.T) {
	secret := []byte("smime-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.test", secret, "", false)
	token, _ := mintToken(secret, sessionClaims{Email: "a@b.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/smime/certificate", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("upload = %d, want 501 (pending)", rec.Code)
	}
}
