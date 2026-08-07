package directory

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// scopedDirectory seeds three domains: two grouped into one organization and a
// third left organizationless, which is the shape a multi-tenant server has. Each
// domain gets a mailbox user, a shared mailbox and a room, so every scoped query
// has something to find in every domain.
//
// It returns the directory and the domain ids in seeding order.
func scopedDirectory(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	orgID, err := d.CreateOrg("acme", "one company, two domains")
	if err != nil {
		t.Fatal(err)
	}
	for _, dom := range []string{"sales.acme.test", "eng.acme.test", "other.test"} {
		domID, err := d.CreateDomain(dom, filepath.Join(root, "domains", dom))
		if err != nil {
			t.Fatal(err)
		}
		if dom != "other.test" {
			if ok, err := d.AssignDomainToOrg(domID, orgID); err != nil || !ok {
				t.Fatalf("assign %s to the org: %v", dom, err)
			}
		}
		for _, local := range []string{"user", "shared", "room"} {
			addr := local + "@" + dom
			if _, err := d.CreateUser(addr, "secret", filepath.Join(root, "users", addr)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`UPDATE users SET address_status = ? WHERE username = ?`,
			afUserSharedMbox, "shared@"+dom); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE users SET display_type = ? WHERE username = ?`,
			dtRoom, "room@"+dom); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// galAddresses runs an unfiltered GAL search as caller and returns the addresses.
func galAddresses(t *testing.T, d *SQLDirectory, caller string) []string {
	t.Helper()
	entries, err := d.SearchGAL(caller, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Address)
	}
	return out
}

// assertNoDomain fails when any address belongs to domain, naming what leaked.
func assertNoDomain(t *testing.T, what string, addrs []string, domain string) {
	t.Helper()
	for _, a := range addrs {
		if strings.HasSuffix(a, "@"+domain) {
			t.Errorf("%s leaked %q from %s", what, a, domain)
		}
	}
}

// TestGALIsScopedToTheCallersOrganization proves one tenant cannot read another
// tenant's staff list. The address book carried no caller at all, so a single
// character typed into autocomplete returned every mailbox address on the server,
// across every domain it hosts.
func TestGALIsScopedToTheCallersOrganization(t *testing.T) {
	d := scopedDirectory(t)

	// The two domains share an organization, so they are one address book.
	got := galAddresses(t, d, "user@sales.acme.test")
	if !slices.Contains(got, "user@eng.acme.test") {
		t.Errorf("a colleague in the same organization is missing from the GAL: %v", got)
	}
	if !slices.Contains(got, "user@sales.acme.test") {
		t.Errorf("the caller's own domain is missing from the GAL: %v", got)
	}
	assertNoDomain(t, "the GAL", got, "other.test")

	// The ungrouped domain sees only itself. org_id 0 is the schema's
	// "organizationless" sentinel, so it must not act as a group.
	got = galAddresses(t, d, "user@other.test")
	if !slices.Contains(got, "user@other.test") {
		t.Errorf("the caller's own domain is missing from the GAL: %v", got)
	}
	assertNoDomain(t, "the GAL", got, "sales.acme.test")
	assertNoDomain(t, "the GAL", got, "eng.acme.test")
}

// TestGALResolvesTheCallerByAliasToo proves the scope follows the same three-key
// resolution the rest of the directory uses, so signing in under an alias does
// not collapse the address book to nothing.
func TestGALResolvesTheCallerByAliasToo(t *testing.T) {
	d := scopedDirectory(t)
	if err := d.CreateAlias("sales@sales.acme.test", "user@sales.acme.test"); err != nil {
		t.Fatal(err)
	}
	if got := galAddresses(t, d, "sales@sales.acme.test"); !slices.Contains(got, "user@eng.acme.test") {
		t.Errorf("an alias caller got %v, want the organization's address book", got)
	}
}

// TestGALRefusesAnUnknownCaller proves the scope fails closed. A surface that
// forgets to pass its authenticated user must show an empty address book, which
// is visible and gets reported, not the whole deployment again.
func TestGALRefusesAnUnknownCaller(t *testing.T) {
	d := scopedDirectory(t)
	for _, caller := range []string{"", "   ", "nobody@nowhere.test"} {
		if got := galAddresses(t, d, caller); len(got) != 0 {
			t.Errorf("caller %q got %v, want nothing", caller, got)
		}
	}
}

// TestRoomsAreScopedToTheCaller proves the room finder follows the GAL. A room is
// an address-book object like any other, and leaving it unscoped would list an
// address the same caller cannot find in the GAL.
func TestRoomsAreScopedToTheCaller(t *testing.T) {
	d := scopedDirectory(t)

	rooms, err := d.ListRooms("user@sales.acme.test")
	if err != nil {
		t.Fatal(err)
	}
	var addrs []string
	for _, r := range rooms {
		addrs = append(addrs, r.Address)
	}
	if !slices.Contains(addrs, "room@eng.acme.test") {
		t.Errorf("a room in the same organization is missing: %v", addrs)
	}
	assertNoDomain(t, "the room list", addrs, "other.test")

	if got, err := d.ListRooms(""); err != nil || len(got) != 0 {
		t.Errorf("ListRooms with no caller = %v (err %v), want nothing", got, err)
	}
}

// TestListAllRoomsStaysUnscoped proves the operator view is deliberately not
// narrowed: the admin panel manages resources across domains and is gated by the
// administrator's own role, not by an address-book scope.
func TestListAllRoomsStaysUnscoped(t *testing.T) {
	d := scopedDirectory(t)
	rooms, err := d.ListAllRooms()
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 3 {
		t.Errorf("ListAllRooms returned %d rooms, want all 3 seeded", len(rooms))
	}
}

// TestSharedMailboxesAreScopedToTheCaller proves the shared-mailbox list follows
// the GAL as well, so webmail cannot enumerate another tenant's shared mailboxes.
func TestSharedMailboxesAreScopedToTheCaller(t *testing.T) {
	d := scopedDirectory(t)

	boxes, err := d.SharedMailboxes("user@sales.acme.test")
	if err != nil {
		t.Fatal(err)
	}
	var addrs []string
	for _, b := range boxes {
		addrs = append(addrs, b.Address)
	}
	if !slices.Contains(addrs, "shared@eng.acme.test") {
		t.Errorf("a shared mailbox in the same organization is missing: %v", addrs)
	}
	assertNoDomain(t, "the shared-mailbox list", addrs, "other.test")

	if got, err := d.SharedMailboxes(""); err != nil || len(got) != 0 {
		t.Errorf("SharedMailboxes with no caller = %v (err %v), want nothing", got, err)
	}
}

// TestScopeSurvivesADisabledOrganizationMember proves the scope narrows and never
// widens: an inactive domain inside the caller's own organization still drops out,
// because the existing domain_status gate runs alongside the scope rather than
// being replaced by it.
func TestScopeSurvivesADisabledOrganizationMember(t *testing.T) {
	d := scopedDirectory(t)
	if _, err := d.db.Exec(`UPDATE domains SET domain_status = 1 WHERE domainname = ?`, "eng.acme.test"); err != nil {
		t.Fatal(err)
	}
	got := galAddresses(t, d, "user@sales.acme.test")
	assertNoDomain(t, "the GAL", got, "eng.acme.test")
	if !slices.Contains(got, "user@sales.acme.test") {
		t.Errorf("the caller's own domain went missing: %v", got)
	}
}
