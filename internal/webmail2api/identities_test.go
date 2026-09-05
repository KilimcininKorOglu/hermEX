package webmail2api

import (
	"encoding/json"
	"testing"

	"hermex/internal/directory"
)

// aliasAccounts is a directory whose Identities returns the primary plus fixed
// aliases, so the identities endpoint can be exercised beyond StaticAccounts
// (which reports only the account itself).
type aliasAccounts struct {
	directory.StaticAccounts
	aliases map[string][]string
}

func (a aliasAccounts) Identities(user string) ([]string, error) {
	return a.aliases[user], nil
}

// TestGetIdentitiesReturnsAliases proves GET /identities returns the caller's
// primary address plus every directory alias, with the primary first and no
// duplicate of it, so the compose picker can offer them as send-as identities.
func TestGetIdentitiesReturnsAliases(t *testing.T) {
	base := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: t.TempDir()}}
	accounts := aliasAccounts{
		StaticAccounts: base,
		aliases: map[string][]string{
			"alice@hermex.test": {"alice@hermex.test", "sales@hermex.test", "a.example@hermex.test"},
		},
	}
	secret := []byte("identities-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)

	rec := authedGet(t, srv, secret, "alice@hermex.test", "/api/v1/identities")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Identities []string `json:"identities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	want := []string{"alice@hermex.test", "sales@hermex.test", "a.example@hermex.test"}
	if len(got.Identities) != len(want) {
		t.Fatalf("identities = %v, want %v", got.Identities, want)
	}
	for i := range want {
		if got.Identities[i] != want[i] {
			t.Errorf("identities[%d] = %q, want %q", i, got.Identities[i], want[i])
		}
	}
}

// TestGetIdentitiesFallsBackToSelf proves a directory that cannot enumerate
// aliases still yields the caller alone rather than an empty list.
func TestGetIdentitiesFallsBackToSelf(t *testing.T) {
	accounts := directory.StaticAccounts{"bob@hermex.test": {MailboxPath: t.TempDir()}}
	secret := []byte("identities-fallback-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)

	rec := authedGet(t, srv, secret, "bob@hermex.test", "/api/v1/identities")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Identities []string `json:"identities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Identities) != 1 || got.Identities[0] != "bob@hermex.test" {
		t.Errorf("identities = %v, want [bob@hermex.test]", got.Identities)
	}
}

// TestSendingFromAnOwnAliasIsNotADelegateSend is the distinction the sent copy
// depends on. One person can be spelled several ways, and comparing the chosen
// From with the login address alone reads an alias of the sender as somebody
// else. That verdict is what decides whether the message is represented on
// behalf of another mailbox.
func TestSendingFromAnOwnAliasIsNotADelegateSend(t *testing.T) {
	accs := aliasAccounts{
		StaticAccounts: directory.StaticAccounts{
			"alice@hermex.test": {MailboxPath: t.TempDir()},
		},
		aliases: map[string][]string{
			"alice@hermex.test": {"alice@hermex.test", "a.smith@hermex.test", "sales@hermex.test"},
		},
	}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", []byte("identity-secret"), "", false)

	for _, want := range []string{"", "alice@hermex.test", "ALICE@HERMEX.TEST", "a.smith@hermex.test", "sales@hermex.test"} {
		representing, sender, ok := srv.resolveSender("alice@hermex.test", want)
		if !ok {
			t.Errorf("%q was refused as the sender's own identity", want)
			continue
		}
		// Own identity: nothing is represented on behalf of anyone, so
		// representing and sender name the same person.
		if representing != sender {
			t.Errorf("%q resolved to representing %q, sender %q; an own identity is not a delegate send",
				want, representing, sender)
		}
	}

	// A stranger, with no send-as grant, is still refused.
	if _, _, ok := srv.resolveSender("alice@hermex.test", "bob@hermex.test"); ok {
		t.Error("a mailbox the caller holds no grant on was accepted as the sender")
	}
}
