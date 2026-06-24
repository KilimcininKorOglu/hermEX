package webmail2api

import (
	"encoding/json"
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
	st.Close()

	other := t.TempDir() // a shared mailbox alice does NOT own
	st2, err := objectstore.Open(other)
	if err != nil {
		t.Fatalf("open other store: %v", err)
	}
	st2.Close()

	accounts := directory.StaticAccounts{
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
}
