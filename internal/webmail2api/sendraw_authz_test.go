package webmail2api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// rawMessage builds a minimal RFC 5322 message carrying the given From header
// line verbatim, so a test can assert what the server does with the identity a
// client-built message asserts.
func rawMessage(fromHeader string) string {
	var b strings.Builder
	if fromHeader != "" {
		b.WriteString("From: ")
		b.WriteString(fromHeader)
		b.WriteString("\r\n")
	}
	b.WriteString("To: victim@example.org\r\n")
	b.WriteString("Subject: hello\r\n")
	b.WriteString("\r\n")
	b.WriteString("body\r\n")
	return base64.StdEncoding.EncodeToString([]byte(b.String()))
}

// sendRawServer builds a server whose only account is alice, so every other
// address is one she may not send as.
func sendRawServer(t *testing.T) (*Server, string) {
	t.Helper()
	box := t.TempDir()
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: box},
		"ceo@hermex.test":   {MailboxPath: t.TempDir()},
	}
	secret := []byte("send-raw-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: box, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, token
}

// postSendRaw posts a raw message plus its recipient list to the raw send route.
func postSendRaw(t *testing.T, srv *Server, token, raw string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"raw": raw, "to": []string{"victim@example.org"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/send-raw", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestSendRawRefusesForgedFrom is the defect. The raw send path relays bytes the
// client built, and it used to relay them without ever looking at the From header
// they assert. The outbound signer keys DKIM off that header, so an unauthorized
// From left the server DMARC-aligned for a domain the caller does not own.
func TestSendRawRefusesForgedFrom(t *testing.T) {
	srv, token := sendRawServer(t)

	rec := postSendRaw(t, srv, token, rawMessage("ceo@hermex.test"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("forged From answered %d, want %d; the send-as gate did not run", rec.Code, http.StatusForbidden)
	}
}

// TestSendRawAllowsOwnFrom is the control: the gate must not break the ordinary
// browser-mode S/MIME flow, which posts back a message whose From is the caller.
func TestSendRawAllowsOwnFrom(t *testing.T) {
	srv, token := sendRawServer(t)

	rec := postSendRaw(t, srv, token, rawMessage("alice@hermex.test"))
	if rec.Code == http.StatusForbidden {
		t.Errorf("the caller's own From was refused with %d", rec.Code)
	}
}

// TestSendRawRefusesUnusableFrom covers the two shapes that carry no single
// authorizable identity: no From header at all, and a From list hiding a second
// address behind the one that would be checked.
func TestSendRawRefusesUnusableFrom(t *testing.T) {
	srv, token := sendRawServer(t)

	cases := []struct {
		name string
		from string
	}{
		{"no From header", ""},
		{"From list", "alice@hermex.test, ceo@hermex.test"},
	}
	for _, tc := range cases {
		rec := postSendRaw(t, srv, token, rawMessage(tc.from))
		if rec.Code == http.StatusOK {
			t.Errorf("%s: accepted with %d, want a refusal", tc.name, rec.Code)
		}
	}
}
