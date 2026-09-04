package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
)

// TestUnopenableStoreIsRecorded is the diagnosability defect. A mailbox that will
// not open is an infrastructure fault: a corrupt database, a failing disk, a held
// lock. The client is told only that the mailbox is unavailable, and the request
// logger records just the status, so without this line the operator sees a 500
// with no cause for exactly the class of failure that cannot be guessed.
func TestUnopenableStoreIsRecorded(t *testing.T) {
	sink := withSink(t)

	// A regular file where a mailbox directory belongs: opening it fails.
	notAMailbox := filepath.Join(t.TempDir(), "mailbox")
	if err := os.WriteFile(notAMailbox, []byte("not a store"), 0o600); err != nil {
		t.Fatal(err)
	}

	accounts := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: notAMailbox}}
	secret := []byte("store-fault-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: notAMailbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/contacts", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	e, ok := sink.find("open-store")
	if !ok {
		t.Fatal("the store-open failure reached the client as a 500 but was never recorded")
	}
	if e.Err == "" {
		t.Error("the recorded event carries no error text, so the cause is still unknown")
	}
}
