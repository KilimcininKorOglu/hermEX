package nspi

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// testGAL builds a Server over a static directory of the given addresses (each
// with a distinct mailbox path so SearchGAL enumerates them all).
func testGAL(addrs ...string) *Server {
	accs := directory.StaticAccounts{}
	for i, a := range addrs {
		accs[a] = directory.Account{Password: "x", MailboxPath: "/mb/" + a + string(rune('a'+i))}
	}
	return NewServer(accs, testGUID)
}

// TestDNRoundTrip proves userDN and dnToSMTP are inverses — the reversible DN
// scheme that lets DNToMId recover an MId from a PR_ENTRYID without a snapshot.
func TestDNRoundTrip(t *testing.T) {
	for _, smtp := range []string{"alice@hermex.test", "a.b+tag@sub.example.org"} {
		dn := userDN(smtp)
		got, ok := dnToSMTP(dn)
		if !ok || got != smtp {
			t.Errorf("dnToSMTP(userDN(%q)) = (%q, %v), want (%q, true)", smtp, got, ok, smtp)
		}
	}
	if _, ok := dnToSMTP("garbage-without-cn"); ok {
		t.Error("dnToSMTP accepted a non-DN string")
	}
}

// TestSnapshotMIDs proves the GAL is address-sorted and MIds are assigned from
// 0x10 by position (dodging the reserved 0x0–0xF range).
func TestSnapshotMIDs(t *testing.T) {
	g := testGAL("carol@hermex.test", "alice@hermex.test", "bob@hermex.test").snapshot()
	if len(g.users) != 3 {
		t.Fatalf("snapshot has %d users, want 3", len(g.users))
	}
	wantOrder := []string{"alice@hermex.test", "bob@hermex.test", "carol@hermex.test"}
	for i, w := range wantOrder {
		if g.users[i].smtp != w {
			t.Errorf("user[%d] = %q, want %q (address-sorted)", i, g.users[i].smtp, w)
		}
		if want := midBase + uint32(i); g.users[i].mid != want {
			t.Errorf("user[%d] MId = %#x, want %#x", i, g.users[i].mid, want)
		}
	}
	// byMID round-trips; the reserved range and out-of-range miss.
	if u, ok := g.byMID(midBase + 1); !ok || u.smtp != "bob@hermex.test" {
		t.Errorf("byMID(0x11) = (%v, %v), want bob", u, ok)
	}
	if _, ok := g.byMID(0x5); ok {
		t.Error("byMID resolved a reserved MId")
	}
	if _, ok := g.byMID(midBase + 99); ok {
		t.Error("byMID resolved an out-of-range MId")
	}
}

// TestGalUserProps proves the AB-user bag carries the SMTP address under the
// standard tags, the reversible-DN permanent EntryID, and the mailuser types.
func TestGalUserProps(t *testing.T) {
	u := galUser{mid: midBase, display: "Alice", smtp: "alice@hermex.test"}
	bag := galUserProps(u)
	checks := map[mapi.PropTag]any{
		mapi.PrDisplayName:  "Alice",
		mapi.PrAddrType:     "SMTP",
		mapi.PrEmailAddress: "alice@hermex.test",
		mapi.PrSmtpAddress:  "alice@hermex.test",
		mapi.PrObjectType:   int32(mapi.ObjectTypeMailUser),
		mapi.PrDisplayType:  int32(mapi.DisplayTypeMailUser),
	}
	for tag, want := range checks {
		got, ok := bag.Get(tag)
		if !ok || got != want {
			t.Errorf("%#x = %v (present %v), want %v", uint32(tag), got, ok, want)
		}
	}
	eid, ok := bag.Get(mapi.PrEntryID)
	if !ok {
		t.Fatal("bag missing PR_ENTRYID")
	}
	// The EntryID is the mailuser PermanentEntryID with the reversible DN.
	if string(eid.([]byte)) != string(permanentEntryID(dtMailuser, userDN(u.smtp))) {
		t.Error("PR_ENTRYID is not the mailuser PermanentEntryID")
	}
}
