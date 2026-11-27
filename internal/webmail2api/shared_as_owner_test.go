package webmail2api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestSharedAsOwnerListsOwnedMailboxes proves GET /mailboxes/shared-as-owner
// returns, under the shared_as_owner key, exactly the shared mailboxes the caller
// is a store owner of, and never the mailboxes it merely has access to. It guards
// the fix for the defect where /shared and /shared-as-owner shared one handler
// that always returned the shared-to-me list under the shared_mailboxes key.
func TestSharedAsOwnerListsOwnedMailboxes(t *testing.T) {
	owned := t.TempDir() // a shared mailbox alice owns
	st, err := objectstore.Open(owned)
	if err != nil {
		t.Fatalf("open owned store: %v", err)
	}
	if err := st.SetStoreOwners([]string{"alice@hermex.test"}); err != nil {
		t.Fatalf("set store owner: %v", err)
	}
	mustClose(t, st)

	other := t.TempDir() // a shared mailbox alice does NOT own
	st2, err := objectstore.Open(other)
	if err != nil {
		t.Fatalf("open other store: %v", err)
	}
	mustClose(t, st2)

	accounts := directory.StaticAccounts{
		"alice@hermex.test": {Password: "password1", MailboxPath: t.TempDir()},
		"team@hermex.test":  {Shared: true, MailboxPath: owned},
		"other@hermex.test": {Shared: true, MailboxPath: other},
	}
	secret := []byte("shared-as-owner-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)

	rec := authedGet(t, srv, secret, "alice@hermex.test", "/api/v1/mailboxes/shared-as-owner")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		SharedAsOwner   []string `json:"shared_as_owner"`
		SharedMailboxes any      `json:"shared_mailboxes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	// The defect returned the shared_mailboxes key; it must be absent here.
	if got.SharedMailboxes != nil {
		t.Errorf("response must not carry the shared_mailboxes key: %s", rec.Body.String())
	}
	if len(got.SharedAsOwner) != 1 || got.SharedAsOwner[0] != "team@hermex.test" {
		t.Errorf("shared_as_owner = %v, want [team@hermex.test]", got.SharedAsOwner)
	}

	// Another account sees none of alice's ownerships, and no session sees nothing.
	bob := authedGet(t, srv, secret, "bob@hermex.test", "/api/v1/mailboxes/shared-as-owner")
	var bobGot struct {
		SharedAsOwner []string `json:"shared_as_owner"`
	}
	mustNoErr(t, "decode bob", json.Unmarshal(bob.Body.Bytes(), &bobGot))
	if len(bobGot.SharedAsOwner) != 0 {
		t.Errorf("bob's shared_as_owner = %v, want none", bobGot.SharedAsOwner)
	}
	anon := httptest.NewRecorder()
	srv.Handler().ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/api/v1/mailboxes/shared-as-owner", nil))
	wantStatus(t, "no session", anon, http.StatusUnauthorized)
}

// failingLister is a directory whose shared-mailbox listing fails.
type failingLister struct{ directory.StaticAccounts }

func (failingLister) SharedMailboxes(string) ([]directory.SharedMailbox, error) {
	return nil, errors.New("directory unreachable")
}

// TestSharedAsOwnerReportsFailures: a listing failure is a recorded 500, not an
// empty list, and a mailbox that cannot be opened is recorded, not skipped
// silently.
func TestSharedAsOwnerReportsFailures(t *testing.T) {
	secret := []byte("shared-as-owner-test-secret")
	accs := directory.StaticAccounts{"alice@hermex.test": {Password: "password1", MailboxPath: t.TempDir()}}
	sink := withSink(t)
	srv := NewServer(failingLister{accs}, accs, nil, "mail.hermex.test", secret, "", false)
	wantStatus(t, "listing failure", authedGet(t, srv, secret, "alice@hermex.test", "/api/v1/mailboxes/shared-as-owner"), http.StatusInternalServerError)
	if _, ok := sink.find("shared-as-owner"); !ok {
		t.Error("the listing failure was not recorded")
	}

	missing := directory.StaticAccounts{
		"alice@hermex.test": {Password: "password1", MailboxPath: t.TempDir()},
		"team@hermex.test":  {Shared: true, MailboxPath: filepath.Join(t.TempDir(), "never-provisioned")},
	}
	sink2 := withSink(t)
	srv2 := NewServer(missing, missing, nil, "mail.hermex.test", secret, "", false)
	wantStatus(t, "unopenable mailbox", authedGet(t, srv2, secret, "alice@hermex.test", "/api/v1/mailboxes/shared-as-owner"), http.StatusOK)
	if _, ok := sink2.find("shared-as-owner"); !ok {
		t.Error("the unopenable shared mailbox was skipped without a record")
	}
}
