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
	"hermex/internal/objectstore"
)

// rawMessage builds a minimal RFC 5322 message carrying the given From header
// line verbatim, so a test can assert what the server does with the identity a
// client-built message asserts.
func rawMessage(fromHeader string) string {
	msg := "To: victim@example.org\r\nSubject: hello\r\n\r\nbody\r\n"
	if fromHeader != "" {
		msg = "From: " + fromHeader + "\r\n" + msg
	}
	return base64.StdEncoding.EncodeToString([]byte(msg))
}

// sendRawServer builds a server whose only account is alice, so every other
// address is one she may not send as.
// It also returns the provisioned mailbox of ceo@hermex.test, the local recipient
// postSendRaw addresses, so a test can see whether a message was delivered.
func sendRawServerWithTarget(t *testing.T) (*Server, string, string) {
	t.Helper()
	box := t.TempDir()
	ceoBox := t.TempDir()
	for _, dir := range []string{box, ceoBox} {
		st, err := objectstore.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		mustClose(t, st)
	}
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: box},
		"ceo@hermex.test":   {MailboxPath: ceoBox},
	}
	secret := []byte("send-raw-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: box, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, token, ceoBox
}

// postSendRaw posts a raw message plus its recipient list to the raw send route.
func postSendRaw(t *testing.T, srv *Server, token, raw string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"raw": raw, "to": []string{"ceo@hermex.test"}})
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
	srv, token, ceoBox := sendRawServerWithTarget(t)

	rec := postSendRaw(t, srv, token, rawMessage("ceo@hermex.test"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("forged From answered %d, want %d; the send-as gate did not run", rec.Code, http.StatusForbidden)
	}
	wantEq(t, "messages delivered to the recipient", inboxCount(t, ceoBox), 0)
}

// TestSendRawAllowsOwnFrom is the control: the gate must not break the ordinary
// browser-mode S/MIME flow, which posts back a message whose From is the caller.
func TestSendRawAllowsOwnFrom(t *testing.T) {
	srv, token, ceoBox := sendRawServerWithTarget(t)

	rec := postSendRaw(t, srv, token, rawMessage("alice@hermex.test"))
	if rec.Code != http.StatusOK {
		t.Errorf("the caller's own From answered %d, want 200: %s", rec.Code, rec.Body.String())
	}
	wantEq(t, "messages delivered to the recipient", inboxCount(t, ceoBox), 1)
}

// TestSendRawRefusesUnusableFrom covers the two shapes that carry no single
// authorizable identity: no From header at all, and a From list hiding a second
// address behind the one that would be checked.
func TestSendRawRefusesUnusableFrom(t *testing.T) {
	srv, token, ceoBox := sendRawServerWithTarget(t)

	cases := []struct {
		name string
		from string
	}{
		{"no From header", ""},
		{"From list", "alice@hermex.test, ceo@hermex.test"},
		// RFC 5322 3.6 allows one From field; a second one, in either order, names
		// an address beside the one the gate reads.
		{"second From field after own", "alice@hermex.test\r\nFrom: ceo@hermex.test"},
		{"second From field before own", "ceo@hermex.test\r\nFrom: alice@hermex.test"},
		// The caller's name as display text around another mailbox's address, and
		// the forged address folded onto a continuation line (RFC 5322 2.2.3).
		{"own address as a display name", `"alice@hermex.test" <ceo@hermex.test>`},
		{"folded forged address", "Alice\r\n <ceo@hermex.test>"},
	}
	for _, tc := range cases {
		rec := postSendRaw(t, srv, token, rawMessage(tc.from))
		if rec.Code == http.StatusOK {
			t.Errorf("%s: accepted with %d, want a refusal", tc.name, rec.Code)
		}
	}
	if rec := postSendRaw(t, srv, token, "%%% not base64 %%%"); rec.Code == http.StatusOK {
		t.Errorf("a raw body that is not base64 was accepted")
	}
	wantEq(t, "messages delivered to the recipient", inboxCount(t, ceoBox), 0)
}
