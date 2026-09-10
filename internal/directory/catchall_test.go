package directory

import "testing"

// catchAllDir builds a directory with two domains and one mailbox user in each, so a test
// can try to point one domain's catch-all at the other domain's account.
func catchAllDir(t *testing.T) *SQLDirectory {
	t.Helper()
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateDomain(t, d, root, "other.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "pw")
	mustCreateUser(t, d, root, "bob@other.test", "pw")
	return d
}

// mustSetCatchAll points a domain's catch-all at an account and requires it to be accepted.
func mustSetCatchAll(t *testing.T, d *SQLDirectory, domain, address string) {
	t.Helper()
	mustNoErr(t, "set the catch-all of "+domain, d.SetDomainCatchAll(domain, address))
}

// TestCatchAllRoundTrips proves a stored catch-all reads back for the admin panel.
func TestCatchAllRoundTrips(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	got, found, err := d.GetDomainCatchAll("hermex.test")
	mustNoErr(t, "read the catch-all", err)

	wantEq(t, "the catch-all was found", found, true)
	wantEq(t, "the catch-all address", got, "alice@hermex.test")
}

// TestCatchAllResolvesAnUnknownLocalPart is the load-bearing case: an address no account
// owns resolves to the domain's catch-all mailbox for delivery.
func TestCatchAllResolvesAnUnknownLocalPart(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	got, ok := d.ResolveCatchAll("nosuchuser@hermex.test")

	wantEq(t, "the unknown address resolved", ok, true)
	wantEq(t, "it resolved to the catch-all mailbox", got, mailboxOf(t, d, "alice@hermex.test"))
}

// TestCatchAllResolvesNothingWithoutOne proves a domain with no catch-all still refuses an
// unknown local part.
func TestCatchAllResolvesNothingWithoutOne(t *testing.T) {
	d := catchAllDir(t)

	if _, ok := d.ResolveCatchAll("nosuchuser@hermex.test"); ok {
		t.Error("a domain with no catch-all must resolve nothing")
	}
}

// TestCatchAllIsPerDomain proves one domain's catch-all never collects another domain's
// unknown recipients.
func TestCatchAllIsPerDomain(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	if _, ok := d.ResolveCatchAll("nosuchuser@other.test"); ok {
		t.Error("a domain with no catch-all resolved through another domain's one")
	}
}

// TestCatchAllRefusesAnAccountInAnotherDomain is the tenant-isolation guard: pointing a
// domain's catch-all at another domain's account would pour one tenant's mail into another
// tenant's mailbox.
func TestCatchAllRefusesAnAccountInAnotherDomain(t *testing.T) {
	d := catchAllDir(t)

	if err := d.SetDomainCatchAll("hermex.test", "bob@other.test"); err == nil {
		t.Error("a catch-all in another domain must be refused")
	}
}

// TestCatchAllRefusesAnUnknownAccount proves a typo is reported rather than stored.
func TestCatchAllRefusesAnUnknownAccount(t *testing.T) {
	d := catchAllDir(t)

	if err := d.SetDomainCatchAll("hermex.test", "ghost@hermex.test"); err == nil {
		t.Error("an unknown account must be refused as a catch-all")
	}
}

// TestCatchAllClears proves an empty address returns the domain to refusing unknown
// recipients.
func TestCatchAllClears(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	mustSetCatchAll(t, d, "hermex.test", "")

	if _, ok := d.ResolveCatchAll("nosuchuser@hermex.test"); ok {
		t.Error("the cleared catch-all still resolves")
	}
}

// TestCatchAllGoesWithTheDeletedAccount proves deleting the account clears the setting, so
// the panel never offers an account that no longer exists.
func TestCatchAllGoesWithTheDeletedAccount(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	if _, err := d.DeleteUser("alice@hermex.test", false); err != nil {
		t.Fatal(err)
	}

	_, found, err := d.GetDomainCatchAll("hermex.test")
	mustNoErr(t, "read the catch-all", err)
	wantEq(t, "the catch-all went with the account", found, false)
}

// TestCatchAllDoesNotAuthenticate is the security case: the catch-all is a delivery-only
// lookup, so an unknown local part must never log in with the catch-all account's password.
func TestCatchAllDoesNotAuthenticate(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	if _, ok := d.Authenticate("nosuchuser@hermex.test", "pw"); ok {
		t.Error("an unknown address authenticated through the catch-all")
	}
}

// TestCatchAllDoesNotResolveOutsideDelivery proves the exact lookup every other surface
// uses is unchanged, so an unknown address still maps to no mailbox there.
func TestCatchAllDoesNotResolveOutsideDelivery(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	if _, ok := d.Resolve("nosuchuser@hermex.test"); ok {
		t.Error("Resolve answered for an address no account owns")
	}
}

// mailboxOf returns the store path of an account, for comparing against a resolved one.
func mailboxOf(t *testing.T, d *SQLDirectory, address string) string {
	t.Helper()
	path, ok := d.Resolve(address)
	if !ok {
		t.Fatalf("%s does not resolve", address)
	}
	return path
}
