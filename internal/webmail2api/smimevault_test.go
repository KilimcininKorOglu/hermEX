package webmail2api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// vaultOf is an envelope with the given work factor and ciphertext size.
func vaultOf(iter, ctLen int) map[string]any {
	b64 := func(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }
	return map[string]any{"v": 1, "kdf": "PBKDF2-SHA256", "iter": iter, "salt": b64(16), "iv": b64(12), "ct": b64(ctLen)}
}

// TestSmimeVaultRoundTrip stores a sealed identity with the certificate, reads it
// back unchanged and uncached, reports it on the certificate, and drops it with
// the certificate. The key used to live in one browser's storage, so it was
// usable nowhere else.
func TestSmimeVaultRoundTrip(t *testing.T) {
	do, _ := apiHarness(t)
	_, certPEM, _ := makeTestIdentity(t)
	vault := vaultOf(600_000, 300)
	body, _ := json.Marshal(map[string]any{"mode": "browser", "cert": certPEM, "vault": vault})
	wantStatus(t, "upload", do(http.MethodPost, "/api/v1/smime/certificate", string(body)), http.StatusOK)

	rec := do(http.MethodGet, "/api/v1/smime/vault", "")
	wantStatus(t, "get vault", rec, http.StatusOK)
	wantEq(t, "cache control", rec.Header().Get("Cache-Control"), "no-store")
	var got map[string]any
	mustNoErr(t, "decode vault", json.Unmarshal(rec.Body.Bytes(), &got))
	wantEq(t, "ciphertext", got["ct"], vault["ct"])
	wantContains(t, "certificate", do(http.MethodGet, "/api/v1/smime/certificate", "").Body.String(), `"hasVault":true`)

	wantStatus(t, "delete", do(http.MethodDelete, "/api/v1/smime/certificate", ""), http.StatusOK)
	wantStatus(t, "vault after delete", do(http.MethodGet, "/api/v1/smime/vault", ""), http.StatusNotFound)
}

// TestSmimeVaultRefusesMalformedEnvelopes refuses an envelope the browser does
// not write: a weaker work factor, the wrong salt or nonce size, an oversized or
// empty ciphertext, or a field this format does not have.
func TestSmimeVaultRefusesMalformedEnvelopes(t *testing.T) {
	do, _ := apiHarness(t)
	_, certPEM, _ := makeTestIdentity(t)
	weak, big, empty, extra, badSalt := vaultOf(1000, 300), vaultOf(600_000, 64<<10+1), vaultOf(600_000, 0), vaultOf(600_000, 300), vaultOf(600_000, 300)
	extra["key"] = "plaintext"
	badSalt["salt"] = base64.StdEncoding.EncodeToString(make([]byte, 8))
	for name, v := range map[string]map[string]any{"weak": weak, "big": big, "empty": empty, "extra": extra, "salt": badSalt} {
		body, _ := json.Marshal(map[string]any{"mode": "browser", "cert": certPEM, "vault": v})
		wantStatus(t, name, do(http.MethodPost, "/api/v1/smime/certificate", string(body)), http.StatusBadRequest)
	}
	wantStatus(t, "nothing stored", do(http.MethodGet, "/api/v1/smime/vault", ""), http.StatusNotFound)
}

// TestSmimeUploadWithoutVaultStillPublishes keeps the certificate-only upload an
// older client sends working, with no vault reported.
func TestSmimeUploadWithoutVaultStillPublishes(t *testing.T) {
	do, _ := apiHarness(t)
	_, certPEM, _ := makeTestIdentity(t)
	body, _ := json.Marshal(map[string]string{"mode": "browser", "cert": certPEM})
	wantStatus(t, "upload", do(http.MethodPost, "/api/v1/smime/certificate", string(body)), http.StatusOK)
	cert := do(http.MethodGet, "/api/v1/smime/certificate", "").Body.String()
	if !strings.Contains(cert, `"hasVault":false`) || !strings.Contains(cert, `"mode":"browser"`) {
		t.Errorf("certificate = %s, want browser mode without a vault", cert)
	}
}
