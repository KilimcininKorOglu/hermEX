package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
)

// TestDiagnosticsSurfacesOwnFailedDeliveries proves the diagnostics view reports the
// caller's own stuck outbound deliveries from the relay queue, never another user's,
// and that naming a different mailbox returns nothing rather than exposing its queue.
func TestDiagnosticsSurfacesOwnFailedDeliveries(t *testing.T) {
	spool, err := relay.Open(filepath.Join(t.TempDir(), "spool.sqlite3"))
	mustNoErr(t, "open spool", err)
	now := time.Now()
	mustNoErr(t, "enqueue alice",
		spool.Enqueue("alice@hermex.test", []string{"nobody@external.test"}, []byte("Subject: x\r\n\r\nhi"), now))
	mustNoErr(t, "enqueue bob",
		spool.Enqueue("bob@hermex.test", []string{"other@external.test"}, []byte("Subject: y\r\n\r\nhi"), now))
	entries, err := spool.List()
	mustNoErr(t, "list spool", err)
	for _, e := range entries {
		mustNoErr(t, "record a failed attempt", spool.Retry(e.RecipientID, now.Add(time.Hour), "550 mailbox unavailable"))
	}

	secret := []byte("diagnostics-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, spool, "mail.hermex.test", secret, "", false)
	do := func(target string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: "/tmp/alice", Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	type diagnostics struct {
		Errors []diagnosticJSON `json:"errors"`
	}
	resp := okBody[diagnostics](t, "diagnostics", do("/api/v1/mail/diagnostics"))
	if len(resp.Errors) != 1 {
		t.Fatalf("got %d diagnostics, want 1 (alice's own, not bob's)", len(resp.Errors))
	}
	d := resp.Errors[0]
	wantEq(t, "category", d.Category, "delivery")
	wantEq(t, "retryable", d.Retryable, true)
	wantContains(t, "message", d.Message, "nobody@external.test")
	wantContains(t, "message", d.Message, "550")

	// A request naming another mailbox returns nothing (no cross-mailbox exposure).
	other := decodeBody[diagnostics](t, "cross-mailbox diagnostics", do("/api/v1/mail/diagnostics?mailbox=bob@hermex.test"))
	wantEq(t, "cross-mailbox diagnostic entries", len(other.Errors), 0)
}
