package dav

import (
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
	found := false
	for _, e := range sink.events {
		if e.Name == "auth.fail" && e.User == "alice@test" {
			found = true
		}
		if strings.Contains(fmt.Sprintf("%+v", e), tried) {
			t.Errorf("event %q carries the password the client sent", e.Name)
		}
	}
	if !found {
		t.Error("the failed login recorded no auth.fail event for the user it tried")
	}
}
