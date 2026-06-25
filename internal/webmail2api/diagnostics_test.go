package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	now := time.Now()
	if err := spool.Enqueue("alice@hermex.test", []string{"nobody@external.test"}, []byte("Subject: x\r\n\r\nhi"), now); err != nil {
		t.Fatal(err)
	}
	if err := spool.Enqueue("bob@hermex.test", []string{"other@external.test"}, []byte("Subject: y\r\n\r\nhi"), now); err != nil {
		t.Fatal(err)
	}
	entries, err := spool.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := spool.Retry(e.RecipientID, now.Add(time.Hour), "550 mailbox unavailable"); err != nil {
			t.Fatal(err)
		}
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

	var resp struct {
		Errors []diagnosticJSON `json:"errors"`
	}
	rec := do("/api/v1/mail/diagnostics")
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics: status %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Errors) != 1 {
		t.Fatalf("got %d diagnostics, want 1 (alice's own, not bob's)", len(resp.Errors))
	}
	d := resp.Errors[0]
	if d.Category != "delivery" || !d.Retryable || !strings.Contains(d.Message, "nobody@external.test") || !strings.Contains(d.Message, "550") {
		t.Fatalf("diagnostic = %+v, want a retryable delivery error naming nobody@external.test and 550", d)
	}

	// A request naming another mailbox returns nothing (no cross-mailbox exposure).
	resp.Errors = nil
	rec = do("/api/v1/mail/diagnostics?mailbox=bob@hermex.test")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode cross-mailbox: %v", err)
	}
	if len(resp.Errors) != 0 {
		t.Fatalf("cross-mailbox diagnostics leaked %d entries", len(resp.Errors))
	}
}
