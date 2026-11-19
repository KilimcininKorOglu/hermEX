package dav

import (
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// TestFailedLoginIsRecordedWithoutThePassword proves a rejected login records
// auth.fail with the user it tried, as IMAP and POP3 do, and that no event
// carries the password the client sent.
func TestFailedLoginIsRecordedWithoutThePassword(t *testing.T) {
	const tried = "Wrong-Secret-Value-42"
	sink := &scheduleSink{}
	accs := directory.StaticAccounts{"alice@test": {Password: "right", MailboxPath: t.TempDir()}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.Logger = logging.New(sink)
	r := httptest.NewRequest("PROPFIND", "/dav/", nil)
	r.SetBasicAuth("alice@test", tried)
	if _, _, ok := srv.basicAuth(httptest.NewRecorder(), r); ok {
		t.Fatal("a wrong password was accepted")
	}
	// Basic auth carries the password base64-encoded (RFC 7617), so a logged
	// Authorization header leaks it without the plain text ever appearing.
	encoded := base64.StdEncoding.EncodeToString([]byte("alice@test:" + tried))
	found := false
	for _, e := range sink.events {
		if e.Name == "auth.fail" && e.User == "alice@test" {
			found = true
		}
		if dump := fmt.Sprintf("%+v", e); strings.Contains(dump, tried) || strings.Contains(dump, encoded) {
			t.Errorf("event %q carries the password the client sent", e.Name)
		}
	}
	if !found {
		t.Error("the failed login recorded no auth.fail event for the user it tried")
	}
}
