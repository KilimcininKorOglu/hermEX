package sendas

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

const caller = "alice@hermex.test"

// grantingMailbox provisions a mailbox that extends caller one of the two send
// grants, and returns its directory.
func grantingMailbox(t *testing.T, onBehalf bool) string {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	grant := st.SetSendAs
	if onBehalf {
		grant = st.SetSendOnBehalf
	}
	if err := grant([]string{caller}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// emptyMailbox provisions a mailbox that grants nothing.
func emptyMailbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if st, err := objectstore.Open(dir); err == nil {
		st.Close()
	}
	return dir
}

// testAccounts wires the four mailboxes the cases use.
func testAccounts(t *testing.T) directory.StaticAccounts {
	t.Helper()
	return directory.StaticAccounts{
		caller:             {MailboxPath: t.TempDir()},
		"team@hermex.test": {Shared: true, MailboxPath: grantingMailbox(t, false)},
		"desk@hermex.test": {Shared: true, MailboxPath: grantingMailbox(t, true)},
		"cold@hermex.test": {Shared: true, MailboxPath: emptyMailbox(t)},
	}
}

// TestResolveNamesTheGrant is the load-bearing case: the two grants put different
// things on the message. A send-as grant names only the represented mailbox, so
// Sender must equal From and oxcmail writes no Sender header; an on-behalf grant
// names the caller as well. Answering one for the other either discloses a person
// the grant keeps off the message, or hides one it does not.
func TestResolveNamesTheGrant(t *testing.T) {
	accounts := testAccounts(t)
	for _, c := range []struct {
		name   string
		want   string
		repr   string
		sender string
		grant  Grant
	}{
		{"no chosen identity is the caller's own", "", caller, caller, GrantOwn},
		{"the caller's own address", caller, caller, caller, GrantOwn},
		{"a send-as grant names only the mailbox", "team@hermex.test", "team@hermex.test", "team@hermex.test", GrantSendAs},
		{"an on-behalf grant names the caller too", "desk@hermex.test", "desk@hermex.test", caller, GrantOnBehalf},
		{"a mailbox that granted nothing", "cold@hermex.test", "", "", GrantNone},
		{"an address with no local mailbox", "ghost@hermex.test", "", "", GrantNone},
	} {
		repr, sender, g := Resolve(accounts, caller, c.want)
		if g != c.grant {
			t.Errorf("%s: grant = %v, want %v", c.name, g, c.grant)
			continue
		}
		if repr != c.repr || sender != c.sender {
			t.Errorf("%s: (repr,sender) = (%q,%q), want (%q,%q)", c.name, repr, sender, c.repr, c.sender)
		}
	}
}

// TestSendAsWinsOverOnBehalf pins the order: a caller who holds both grants sends
// under the narrower one, because the send-as grant says the message is the
// mailbox's own and a Sender header would disclose them anyway.
func TestSendAsWinsOverOnBehalf(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSendAs([]string{caller}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSendOnBehalf([]string{caller}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	accounts := directory.StaticAccounts{
		caller:             {MailboxPath: t.TempDir()},
		"both@hermex.test": {Shared: true, MailboxPath: dir},
	}
	repr, sender, g := Resolve(accounts, caller, "both@hermex.test")
	if g != GrantSendAs {
		t.Errorf("grant = %v, want send-as", g)
	}
	if repr != sender {
		t.Errorf("(repr,sender) = (%q,%q), want them equal so no Sender header is written", repr, sender)
	}
}

// TestAllowsRefusesANullSender is the difference between the two entry points.
// Resolve answers which identity the caller chose, and choosing none is their own.
// Allows answers whether an address may be on the message, and an authenticated
// submission with a null sender names no identity to authorize.
func TestAllowsRefusesANullSender(t *testing.T) {
	accounts := testAccounts(t)
	for _, want := range []string{"", "   "} {
		if Allows(accounts, caller, want) {
			t.Errorf("Allows(%q) = true, want false", want)
		}
	}
	if !Allows(accounts, caller, caller) {
		t.Error("the caller's own address must be allowed")
	}
	if Allows(accounts, caller, "cold@hermex.test") {
		t.Error("an ungranted mailbox must be refused")
	}
}

// TestAnUnopenableMailboxDeniesTheGrant keeps the gate failing closed: a mailbox
// whose store will not open grants nothing, rather than falling through to a
// permissive answer.
func TestAnUnopenableMailboxDeniesTheGrant(t *testing.T) {
	accounts := directory.StaticAccounts{
		caller:             {MailboxPath: t.TempDir()},
		"gone@hermex.test": {Shared: true, MailboxPath: "/nonexistent/hermex/mailbox"},
	}
	if _, _, g := Resolve(accounts, caller, "gone@hermex.test"); g != GrantNone {
		t.Errorf("grant = %v, want none: an unopenable store must deny", g)
	}
}
