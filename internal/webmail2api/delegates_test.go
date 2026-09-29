package webmail2api

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// delegationHarness signs alice in over a mailbox whose store lists already hold
// grants made outside webmail: carol is a delegate with send-on-behalf, as Outlook
// or the admin panel writes her, and dave may send as the mailbox.
func delegationHarness(t *testing.T) (requestFunc, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range []func([]string) error{st.SetDelegates, st.SetSendOnBehalf} {
		if err := set([]string{"carol@hermex.test"}); err != nil {
			st.Close()
			t.Fatal(err)
		}
	}
	if err := st.SetSendAs([]string{"dave@hermex.test"}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: dir}}
	return loginAs(t, accounts, "alice@hermex.test"), dir
}

type delegationRow struct {
	ID              string `json:"id"`
	Grantee         string `json:"grantee"`
	CanSendAs       bool   `json:"canSendAs"`
	CanSendOnBehalf bool   `json:"canSendOnBehalf"`
}

// listDelegations reads the delegation list keyed by grantee.
func listDelegations(t *testing.T, do requestFunc) map[string]delegationRow {
	t.Helper()
	rec := do(http.MethodGet, "/api/v1/delegations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET delegations = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Delegations []delegationRow `json:"delegations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	rows := map[string]delegationRow{}
	for _, d := range out.Delegations {
		rows[d.Grantee] = d
	}
	return rows
}

// storeLists reads the three store lists every protocol consults.
func storeLists(t *testing.T, dir string) (delegates, sendAs, onBehalf []string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var e1, e2, e3 error
	delegates, e1 = st.GetDelegates()
	sendAs, e2 = st.GetSendAs()
	onBehalf, e3 = st.GetSendOnBehalf()
	for _, err := range []error{e1, e2, e3} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return delegates, sendAs, onBehalf
}

// TestDelegationsShowGrantsMadeElsewhere proves webmail lists the store's delegate
// and send grants, the ones Outlook, EWS and the admin panel write, and not only
// the grants webmail itself made.
func TestDelegationsShowGrantsMadeElsewhere(t *testing.T) {
	do, _ := delegationHarness(t)
	rows := listDelegations(t, do)
	carol, ok := rows["carol@hermex.test"]
	if !ok || !carol.CanSendOnBehalf || carol.CanSendAs {
		t.Errorf("carol = %+v (listed %v), want a send-on-behalf delegate", carol, ok)
	}
	dave, ok := rows["dave@hermex.test"]
	if !ok || !dave.CanSendAs || dave.CanSendOnBehalf {
		t.Errorf("dave = %+v (listed %v), want a send-as grant", dave, ok)
	}
	if carol.ID == "" || dave.ID == "" || carol.ID == dave.ID {
		t.Errorf("ids %q and %q must be distinct and non-empty", carol.ID, dave.ID)
	}
}

// TestAddingADelegationKeepsOtherGrants proves a delegation added in webmail joins
// the store lists instead of replacing them, so grants made elsewhere survive.
func TestAddingADelegationKeepsOtherGrants(t *testing.T) {
	do, dir := delegationHarness(t)
	if rec := do(http.MethodPost, "/api/v1/delegations",
		`{"grantee":"bob@hermex.test","rights":["read"],"canSendAs":true}`); rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	delegates, sendAs, onBehalf := storeLists(t, dir)
	for name, got := range map[string][]string{"delegates": delegates, "send-on-behalf": onBehalf} {
		if !slices.Contains(got, "carol@hermex.test") {
			t.Errorf("%s list %v lost carol", name, got)
		}
	}
	if !slices.Contains(delegates, "bob@hermex.test") {
		t.Errorf("delegates %v lack bob", delegates)
	}
	if !slices.Contains(sendAs, "dave@hermex.test") || !slices.Contains(sendAs, "bob@hermex.test") {
		t.Errorf("send-as %v, want dave and bob", sendAs)
	}
}

// TestRemovingADelegationRemovesOnlyThatGrantee proves a delete by the listed id
// takes that grantee off every store list and leaves the others, including a grant
// webmail never made.
func TestRemovingADelegationRemovesOnlyThatGrantee(t *testing.T) {
	do, dir := delegationHarness(t)
	carol := listDelegations(t, do)["carol@hermex.test"]
	if rec := do(http.MethodDelete, "/api/v1/delegations/"+carol.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	delegates, sendAs, onBehalf := storeLists(t, dir)
	if slices.Contains(delegates, "carol@hermex.test") || slices.Contains(onBehalf, "carol@hermex.test") {
		t.Errorf("carol remains: delegates %v, send-on-behalf %v", delegates, onBehalf)
	}
	if !slices.Contains(sendAs, "dave@hermex.test") {
		t.Errorf("send-as %v lost dave", sendAs)
	}
	if _, ok := listDelegations(t, do)["carol@hermex.test"]; ok {
		t.Error("carol is still listed")
	}
}
