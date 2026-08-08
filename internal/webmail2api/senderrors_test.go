package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// TestScheduleFailureDoesNotEchoInternals proves the send surface answers a
// failure with a fixed message. The underlying errors on these paths carry
// mailbox filesystem paths and database driver text, and the response body goes
// to the client whether or not the SPA renders it, so the internal text must stay
// server-side. The unparseable time is the deterministic way in: it fails inside
// the scheduler, past every check that answers with its own message.
func TestScheduleFailureDoesNotEchoInternals(t *testing.T) {
	sink := withSink(t)
	secret := []byte("send-error-test-secret")
	mbox := t.TempDir()
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"to":["bob@hermex.test"],"subject":"later","body":"hi","sendAt":"not-a-timestamp"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/send", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	if got["error"] != "could not schedule the message" {
		t.Errorf("error = %q, want the fixed message", got["error"])
	}
	// The parse error names the layout and the offending value; neither belongs on
	// the wire.
	for _, leak := range []string{"parsing time", "cannot parse", mbox} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("the response leaked internal detail %q:\n%s", leak, rec.Body.String())
		}
	}
	// The operator still gets the real reason.
	e, ok := sink.find("schedule-send")
	if !ok {
		t.Fatal("the failure was not recorded; sanitizing must not mean discarding")
	}
	if e.Err == "" {
		t.Error("the recorded event carries no error text")
	}
}
