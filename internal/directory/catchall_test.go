package directory

import (
	"path/filepath"
	"testing"
)

// catchAllDir builds a directory with two domains and one mailbox user in each, so a test
// can try to point one domain's catch-all at the other domain's account.
func catchAllDir(t *testing.T) *SQLDirectory {
	t.Helper()
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	seedDomain(t, d, root, "other.test")
	seedUser(t, d, root, "alice@hermex.test", "password1")
	seedUser(t, d, root, "bob@other.test", "password1")
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

	if path, ok := d.ResolveCatchAll("nosuchuser@hermex.test"); ok || path != "" {
		t.Errorf("a domain with no catch-all must resolve nothing (%q, %v)", path, ok)
	}
}

// TestCatchAllIsPerDomain proves one domain's catch-all never collects another domain's
// unknown recipients.
func TestCatchAllIsPerDomain(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	if path, ok := d.ResolveCatchAll("nosuchuser@other.test"); ok || path != "" {
		t.Errorf("a domain with no catch-all resolved through another domain's one (%q, %v)", path, ok)
	}
}

// TestCatchAllRefusesAnAccountInAnotherDomain is the tenant-isolation guard: pointing a
// domain's catch-all at another domain's account would pour one tenant's mail into another
// tenant's mailbox.
func TestCatchAllRefusesAnAccountInAnotherDomain(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	if err := d.SetDomainCatchAll("hermex.test", "bob@other.test"); err == nil {
		t.Error("a catch-all in another domain must be refused")
	}
	wantCatchAll(t, d, "hermex.test", "alice@hermex.test")
}

// TestCatchAllRefusesAnUnknownAccount proves a typo is reported rather than stored.
func TestCatchAllRefusesAnUnknownAccount(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	if err := d.SetDomainCatchAll("hermex.test", "ghost@hermex.test"); err == nil {
		t.Error("an unknown account must be refused as a catch-all")
	}
	wantCatchAll(t, d, "hermex.test", "alice@hermex.test")
}

// TestCatchAllRefusesAResourceMailbox proves a room cannot collect a domain's
// unknown recipients: a room's mailbox is read by the booking agent, not by a
// person, so mail poured into it would never be seen.
func TestCatchAllRefusesAResourceMailbox(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")
	rid, err := d.CreateRoom("room@hermex.test", "Room", filepath.Join(t.TempDir(), "room"), 4, false)
	mustNoErr(t, "create the room", err)
	wantEq(t, "room id > 0", rid > 0, true)

	if err := d.SetDomainCatchAll("hermex.test", "room@hermex.test"); err == nil {
		t.Error("a room was accepted as the catch-all")
	}
	wantCatchAll(t, d, "hermex.test", "alice@hermex.test")
}

// wantCatchAll requires a domain's stored catch-all to still be want after a
// refused change.
func wantCatchAll(t *testing.T, d *SQLDirectory, domain, want string) {
	t.Helper()
	got, found, err := d.GetDomainCatchAll(domain)
	mustNoErr(t, "read the catch-all", err)
	wantEq(t, "the catch-all is still set", found, true)
	wantEq(t, "the catch-all after the refusal", got, want)
}

// TestCatchAllClears proves an empty address returns the domain to refusing unknown
// recipients.
func TestCatchAllClears(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	mustSetCatchAll(t, d, "hermex.test", "")

	if path, ok := d.ResolveCatchAll("nosuchuser@hermex.test"); ok || path != "" {
		t.Errorf("the cleared catch-all still resolves (%q, %v)", path, ok)
	}
}

// TestCatchAllGoesWithTheDeletedAccount proves deleting the account clears the setting, so
// the panel never offers an account that no longer exists.
func TestCatchAllGoesWithTheDeletedAccount(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	deleted, err := d.DeleteUser("alice@hermex.test", false)
	mustNoErr(t, "delete user", err)
	wantEq(t, "user deleted", deleted, true)

	cleared, found, err := d.GetDomainCatchAll("hermex.test")
	mustNoErr(t, "read the catch-all", err)
	wantEq(t, "the catch-all went with the account", found, false)
	wantEq(t, "the cleared catch-all address", cleared, "")
}

// TestCatchAllDoesNotAuthenticate is the security case: the catch-all is a delivery-only
// lookup, so an unknown local part must never log in with the catch-all account's password.
func TestCatchAllDoesNotAuthenticate(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	if path, ok := d.Authenticate("nosuchuser@hermex.test", "password1"); ok || path != "" {
		t.Errorf("an unknown address authenticated through the catch-all (%q, %v)", path, ok)
	}
}

// TestCatchAllDoesNotResolveOutsideDelivery proves the exact lookup every other surface
// uses is unchanged, so an unknown address still maps to no mailbox there.
func TestCatchAllDoesNotResolveOutsideDelivery(t *testing.T) {
	d := catchAllDir(t)
	mustSetCatchAll(t, d, "hermex.test", "alice@hermex.test")

	if path, ok := d.Resolve("nosuchuser@hermex.test"); ok || path != "" {
		t.Errorf("Resolve answered for an address no account owns (%q, %v)", path, ok)
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
