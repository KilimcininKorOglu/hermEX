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
