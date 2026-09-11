package webmail2api

import (
	"slices"
	"testing"

	"hermex/internal/objectstore"
)

// TestADelegationMirrorsItsSendGrants is the load-bearing case: the SPA stores a
// delegation's two send flags, but only the store lists are consulted at send time.
// Writing just the grantee list left both flags decorative, so a delegate the owner
// never granted a send could still be refused and one they did grant could still not
// send. Each flag now reaches its own list, and the delegate list stays the access
// gate that grants no send of its own.
func TestADelegationMirrorsItsSendGrants(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mirrorDelegates(st, []delegationJSON{
		{Grantee: "asonly@hermex.test", CanSendAs: true},
		{Grantee: "behalf@hermex.test", CanSendOnBehalf: true},
		{Grantee: "both@hermex.test", CanSendAs: true, CanSendOnBehalf: true},
		{Grantee: "readonly@hermex.test"},
	})

	dels, err := st.GetDelegates()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"asonly@hermex.test", "behalf@hermex.test", "both@hermex.test", "readonly@hermex.test"} {
		if !slices.Contains(dels, want) {
			t.Errorf("the delegate list is missing %s: every delegation grants access", want)
		}
	}

	sendAs, err := st.GetSendAs()
	if err != nil {
		t.Fatal(err)
	}
	wantSendAs := []string{"asonly@hermex.test", "both@hermex.test"}
	if !slices.Equal(sendAs, wantSendAs) {
		t.Errorf("send-as list = %v, want %v", sendAs, wantSendAs)
	}

	onBehalf, err := st.GetSendOnBehalf()
	if err != nil {
		t.Fatal(err)
	}
	wantOnBehalf := []string{"behalf@hermex.test", "both@hermex.test"}
	if !slices.Equal(onBehalf, wantOnBehalf) {
		t.Errorf("send-on-behalf list = %v, want %v", onBehalf, wantOnBehalf)
	}
}

// TestRemovingADelegationClearsItsGrants proves the mirror is a replacement, not an
// append: a revoked delegation must take its send grants with it.
func TestRemovingADelegationClearsItsGrants(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mirrorDelegates(st, []delegationJSON{{Grantee: "gone@hermex.test", CanSendAs: true, CanSendOnBehalf: true}})
	mirrorDelegates(st, nil)

	for _, c := range []struct {
		name string
		read func() ([]string, error)
	}{
		{"delegates", st.GetDelegates},
		{"send-as", st.GetSendAs},
		{"send-on-behalf", st.GetSendOnBehalf},
	} {
		list, err := c.read()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(list) != 0 {
			t.Errorf("the %s list still holds %v after the delegation was removed", c.name, list)
		}
	}
}
