package webmail

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// testP12 builds a self-signed email-protection certificate for cn and returns it
// as a password-protected PKCS#12 plus its PEM certificate (for recipient tests).
func testP12(t *testing.T, cn, pass string) (p12, certPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:   big.NewInt(1),
		Subject:        pkix.Name{CommonName: cn},
		NotBefore:      time.Now().Add(-time.Hour),
		NotAfter:       time.Now().Add(24 * time.Hour),
		KeyUsage:       x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		EmailAddresses: []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	p12, err = pkcs12.Modern.Encode(key, cert, nil, pass)
	if err != nil {
		t.Fatal(err)
	}
	return p12, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// postMultipart submits a multipart/form-data POST (fields plus optional file
// parts) and returns the final status and body, following redirects.
func postMultipart(t *testing.T, c *http.Client, url string, fields map[string]string, files map[string][]byte) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for field, data := range files {
		fw, err := mw.CreateFormFile(field, field+".bin")
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(data)
	}
	mw.Close()
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestSmimeRequiresSession locks the auth gate on the certificate page.
func TestSmimeRequiresSession(t *testing.T) {
	ts := newTestServer(t, emptyMailbox(t))
	c := &http.Client{}
	if _, body := get(t, c, ts.URL+"/smime"); !strings.Contains(body, "Sign in") {
		t.Error("unauthenticated /smime did not land on login")
	}
}

// TestSmimeIdentityFlow uploads a PKCS#12 (which unlocks the session), confirms
// the certificate details render, and removes it. A second session starts locked
// and unlocks with the passphrase (and rejects a wrong one).
func TestSmimeIdentityFlow(t *testing.T) {
	ts := newTestServer(t, emptyMailbox(t))
	c := authedClient(t, ts)
	p12, _ := testP12(t, "alice@hermex.test", "pass")

	code, body := postMultipart(t, c, ts.URL+"/smime",
		map[string]string{"action": "upload", "passphrase": "pass"},
		map[string][]byte{"p12": p12})
	if code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200 after redirect", code)
	}
	for _, want := range []string{"alice@hermex.test", "Unlocked for this session"} {
		if !strings.Contains(body, want) {
			t.Errorf("after upload, page missing %q", want)
		}
	}

	// A second session sees the stored identity but locked.
	c2 := authedClient(t, ts)
	if _, body := get(t, c2, ts.URL+"/smime"); !strings.Contains(body, "Locked") {
		t.Error("second session should start locked")
	}
	// Wrong passphrase is rejected.
	if code, _ := postMultipart(t, c2, ts.URL+"/smime", map[string]string{"action": "unlock", "passphrase": "nope"}, nil); code != http.StatusBadRequest {
		t.Errorf("unlock with wrong passphrase = %d, want 400", code)
	}
	// Correct passphrase unlocks.
	if _, body := postMultipart(t, c2, ts.URL+"/smime", map[string]string{"action": "unlock", "passphrase": "pass"}, nil); !strings.Contains(body, "Unlocked for this session") {
		t.Error("unlock with correct passphrase did not unlock")
	}

	// Remove clears it.
	if _, body := postMultipart(t, c, ts.URL+"/smime", map[string]string{"action": "remove"}, nil); !strings.Contains(body, "No certificate uploaded yet") {
		t.Error("after remove, page should show no certificate")
	}
}

// TestSmimeRecipientFlow adds a recipient certificate, lists it, and removes it.
func TestSmimeRecipientFlow(t *testing.T) {
	ts := newTestServer(t, emptyMailbox(t))
	c := authedClient(t, ts)
	_, certPEM := testP12(t, "bob@hermex.test", "x")

	_, body := postMultipart(t, c, ts.URL+"/smime",
		map[string]string{"action": "addrecipient", "address": "bob@hermex.test"},
		map[string][]byte{"cert": certPEM})
	if !strings.Contains(body, "bob@hermex.test") {
		t.Error("recipient address not listed after add")
	}

	_, body = postMultipart(t, c, ts.URL+"/smime",
		map[string]string{"action": "removerecipient", "address": "bob@hermex.test"}, nil)
	if !strings.Contains(body, "No recipient certificates added") {
		t.Error("recipient still listed after remove")
	}
}
