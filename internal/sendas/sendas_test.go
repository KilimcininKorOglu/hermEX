package sendas

import (
	"errors"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
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
	grant := st.SetSendAs
	if onBehalf {
		grant = st.SetSendOnBehalf
	}
	return closeMailbox(t, st, dir, grant([]string{caller}))
}

// listMailbox provisions a mailbox whose send-as list names exactly list.
func listMailbox(t *testing.T, list []string) string {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return closeMailbox(t, st, dir, st.SetSendAs(list))
}

// closeMailbox fails the test on the setup error err or on the store's Close,
// and returns dir.
func closeMailbox(t *testing.T, st *objectstore.Store, dir string, err error) string {
	t.Helper()
	if cerr := st.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// emptyMailbox provisions a mailbox that grants nothing.
func emptyMailbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return closeMailbox(t, st, dir, nil)
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

// aliasAccounts is a directory whose caller owns an alias besides the login, or
// whose identity lookup fails, the two shapes Identities has to handle.
type aliasAccounts struct {
	directory.StaticAccounts
	ids []string
	err error
}

func (a aliasAccounts) Identities(string) ([]string, error) { return a.ids, a.err }

// TestAnAliasIsTheCallersOwnAndCarriesItsGrants proves a caller's alias is their
// own identity (From names the alias, no Sender), and a mailbox that granted
// send-as to the alias grants it to the caller who owns it. When the directory
// cannot list the identities, the caller keeps the login alone: the alias and the
// grant made to it are refused rather than assumed.
func TestAnAliasIsTheCallersOwnAndCarriesItsGrants(t *testing.T) {
	const alias = "sales@hermex.test"
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	closeMailbox(t, st, dir, st.SetSendAs([]string{" SALES@hermex.test "}))
	base := directory.StaticAccounts{
		caller:               {MailboxPath: t.TempDir()},
		"shared@hermex.test": {Shared: true, MailboxPath: dir},
	}

	withAlias := aliasAccounts{StaticAccounts: base, ids: []string{caller, alias}}
	if repr, sender, g := Resolve(withAlias, caller, "Sales@Hermex.test"); g != GrantOwn || repr != alias || sender != alias {
		t.Errorf("alias: (%q,%q,%v), want (%q,%q,own)", repr, sender, g, alias, alias)
	}
	if _, _, g := Resolve(withAlias, caller, "shared@hermex.test"); g != GrantSendAs {
		t.Errorf("grant to the caller's alias = %v, want send-as", g)
	}

	broken := aliasAccounts{StaticAccounts: base, err: errors.New("directory down")}
	for _, want := range []string{alias, "shared@hermex.test"} {
		if _, _, g := Resolve(broken, caller, want); g != GrantNone {
			t.Errorf("identity lookup failed, %s: grant = %v, want none", want, g)
		}
	}
	if _, _, g := Resolve(broken, caller, caller); g != GrantOwn {
		t.Errorf("identity lookup failed, own login: grant = %v, want own", g)
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
	closeMailbox(t, st, dir, st.SetSendOnBehalf([]string{caller}))

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

// TestResolveLeavesAnUnprovisionedMailboxAlone asks for the identity of an account
// whose mailbox was never created. It grants nothing, and asking must not create
// the mailbox, because only delivery provisions one.
func TestResolveLeavesAnUnprovisionedMailboxAlone(t *testing.T) {
	dir := t.TempDir()
	accounts := directory.StaticAccounts{
		caller:             {MailboxPath: t.TempDir()},
		"team@hermex.test": {MailboxPath: dir},
	}
	if _, _, g := Resolve(accounts, caller, "team@hermex.test"); g != GrantNone {
		t.Errorf("grant = %v, want none", g)
	}
	if _, err := objectstore.OpenExisting(dir); err == nil {
		t.Error("resolving the identity created the mailbox")
	}
}

// TestAGrantListNamesExactAddresses: a grant list entry is one whole address, so
// an entry that only contains the caller's address, or extends it, grants
// nothing. A substring or prefix match would let a mailbox's grant to
// xalice@hermex.test or alice@hermex.test.rival.test reach alice.
func TestAGrantListNamesExactAddresses(t *testing.T) {
	accounts := testAccounts(t)
	accounts["lure@hermex.test"] = directory.Account{Shared: true, MailboxPath: listMailbox(t,
		[]string{"x" + caller, caller + ".rival.test", "alice", "*@hermex.test", ""})}
	if rep, snd, g := Resolve(accounts, caller, "lure@hermex.test"); g != GrantNone || rep != "" || snd != "" {
		t.Errorf("Resolve = %q, %q, %v; want a refusal", rep, snd, g)
	}
}

// TestAnUnreadableSendAsListDeniesTheGrant keeps the documented fail-closed
// rule: a send-as list that will not read denies the grant. Falling through to
// the on-behalf list would answer for a mailbox whose grants were never read.
func TestAnUnreadableSendAsListDeniesTheGrant(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSendOnBehalf([]string{caller}); err != nil {
		closeMailbox(t, st, dir, err)
	}
	closeMailbox(t, st, dir, st.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrAbSendAs, Value: "not a json list"},
	}))
	accounts := directory.StaticAccounts{
		caller:             {MailboxPath: t.TempDir()},
		"torn@hermex.test": {Shared: true, MailboxPath: dir},
	}
	if rep, snd, g := Resolve(accounts, caller, "torn@hermex.test"); g != GrantNone || rep != "" || snd != "" {
		t.Errorf("Resolve = %q, %q, %v; want a refusal", rep, snd, g)
	}
}

// TestAGrantOfAnotherOrganizationIsNotHonored: a mailbox of another organization
// that lists the caller on its send-as or send-on-behalf list (a grant written
// before the grant surfaces refused one across organizations) gives the caller
// no identity, since every mailbox grant speaks inside its own organization.
func TestAGrantOfAnotherOrganizationIsNotHonored(t *testing.T) {
	accounts := testAccounts(t)
	accounts["boss@rival.test"] = directory.Account{MailboxPath: grantingMailbox(t, false)}
	accounts["desk@rival.test"] = directory.Account{MailboxPath: grantingMailbox(t, true)}
	for _, want := range []string{"boss@rival.test", "desk@rival.test"} {
		if rep, snd, g := Resolve(accounts, caller, want); g != GrantNone || rep != "" || snd != "" {
			t.Errorf("Resolve(%s) = %q, %q, %v; want a refusal", want, rep, snd, g)
		}
		if Allows(accounts, caller, want) {
			t.Errorf("Allows(%s) = true across organizations", want)
		}
	}
}
