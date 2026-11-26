package directory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAddressDomainMatchesValidateAddress pins the split to the account's own
// domain, the part after the last '@' (RFC 5321 4.1.2 allows an '@' in a quoted
// local part), and checks that ValidateAddress accepts such an address so the
// two agree on which domain owns it.
func TestAddressDomainMatchesValidateAddress(t *testing.T) {
	cases := map[string]string{
		"alice@Hermex.test":        "hermex.test",
		"x@other.test@hermex.test": "hermex.test",
		"no-domain":                "",
		"trailing@":                "",
	}
	for addr, want := range cases {
		if got := AddressDomain(addr); got != want {
			t.Errorf("AddressDomain(%q) = %q, want %q", addr, got, want)
		}
	}
	if err := ValidateAddress("x@other.test@hermex.test"); err != nil {
		t.Fatalf("ValidateAddress refused the address the split is tested on: %v", err)
	}
}

// TestValidateAddressRejectsTraversal covers the payloads that would walk the
// derived maildir out of the data root. filepath.Join resolves "..", so the join
// itself is no defense and the name has to be refused before it reaches disk.
func TestValidateAddressRejectsTraversal(t *testing.T) {
	bad := []string{
		"../../../../tmp/pwned@example.com",
		"alice/../../etc@example.com",
		`alice\..\..\tmp@example.com`,
		"..@example.com",
		".@example.com",
		"alice@../../tmp",
		"alice@..",
		"alice@exa/mple.com",
		"alice@",
		"@example.com",
		"alice",
		"al ice@example.com",
		"alice\x00@example.com",
		"alice@example.com\r\nBcc: x@evil.example",
		"ali\nce@example.com",
	}
	for _, address := range bad {
		if err := ValidateAddress(address); err == nil {
			t.Errorf("ValidateAddress(%q) = nil, want an error", address)
		}
	}
}

// TestValidateAddressAcceptsOrdinaryAddresses keeps provisioning working for the
// shapes real deployments use.
func TestValidateAddressAcceptsOrdinaryAddresses(t *testing.T) {
	good := []string{
		"alice@example.com",
		"Alice.Smith@Example.Com",
		"alice+tag@mail.example.co.uk",
		"a_b-c@sub-domain.example.com",
		"alice@xn--nda.example",
	}
	for _, address := range good {
		if err := ValidateAddress(address); err != nil {
			t.Errorf("ValidateAddress(%q) = %v, want nil", address, err)
		}
	}
}

// TestValidateDomainRejectsTraversal covers the domain names that would move a
// public store, whose directory is the domain name, outside the data root.
func TestValidateDomainRejectsTraversal(t *testing.T) {
	for _, domain := range []string{"..", ".", "../tmp", `..\tmp`, "", "exam ple.com", ".example.com", "example..com", "example.com.", "exam;ple.com"} {
		if err := ValidateDomain(domain); err == nil {
			t.Errorf("ValidateDomain(%q) = nil, want an error", domain)
		}
	}
}

// TestValidateAccessPattern keeps allow/block patterns to what the exact-lookup
// matcher can hit: one address or one bare domain. Anything else would be a rule
// listed as active that matches no sender.
func TestValidateAccessPattern(t *testing.T) {
	for _, p := range []string{"partner@example.com", "Spammy.Example", "mail.example.co.uk"} {
		if err := ValidateAccessPattern(p); err != nil {
			t.Errorf("ValidateAccessPattern(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range []string{"*", "*.example.com", "@example.com", "a@b@c.com", "a@example.com, b@example.com",
		"Name <a@example.com>", "exam ple.com", "example..com", ""} {
		if err := ValidateAccessPattern(p); err == nil {
			t.Errorf("ValidateAccessPattern(%q) = nil, want an error", p)
		}
	}
}

// TestValidateContactAddress keeps a contact to one routable RFC 5322 addr-spec:
// it is offered to every client as a recipient, so anything a relay cannot route
// would bounce on use.
func TestValidateContactAddress(t *testing.T) {
	for _, a := range []string{"john@partner.example", "j.o-hn+x@mail.partner.co.uk"} {
		if err := ValidateContactAddress(a); err != nil {
			t.Errorf("ValidateContactAddress(%q) = %v, want nil", a, err)
		}
	}
	for _, a := range []string{"john@", "@partner.example", "john", "jo hn@partner.example",
		"John <john@partner.example>", "a@example.com, b@example.com", "john@exam ple.com",
		"john@example..com", "john@partner.example\r\nBcc: x@evil.example", ""} {
		if err := ValidateContactAddress(a); err == nil {
			t.Errorf("ValidateContactAddress(%q) = nil, want an error", a)
		}
	}
}

// TestValidatedAddressStaysUnderRoot states the property the validation exists
// for: every accepted name joins to a path inside the root it was joined to.
func TestValidatedAddressStaysUnderRoot(t *testing.T) {
	const root = "/data/user"
	for _, local := range []string{"alice", "a.b", "a+b", "..a", "a..b"} {
		if err := ValidateAddress(local + "@example.com"); err != nil {
			continue
		}
		joined := filepath.Join(root, "example.com", local)
		rel, err := filepath.Rel(root, joined)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || len(rel) > 2 && rel[:3] == "../" {
			t.Errorf("local part %q joins to %q, outside %q", local, joined, root)
		}
	}
}

// TestCreateUserRefusesEscapingMaildir proves the check reaches the one place
// that puts a mailbox on disk: no row, and nothing created outside the root the
// caller derived the path from.
func TestCreateUserRefusesEscapingMaildir(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	// The data root sits inside the temp dir so the escape target is still cleaned
	// up, and a leftover from an earlier run cannot decide the assertion.
	base := t.TempDir()
	root := filepath.Join(base, "data")
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "domain", "hermex.test")); err != nil {
		t.Fatal(err)
	}

	// What an admin API caller can post: the domain is one they administer, the
	// local part walks out of the data root.
	address := "../../../escaped@hermex.test"
	escaped := filepath.Join(root, "user", "hermex.test", "../../../escaped")
	if _, err := d.CreateUser(address, "password1", escaped); err == nil {
		t.Fatal("CreateUser accepted an address that escapes the data root")
	}
	if _, err := os.Stat(escaped); err == nil {
		t.Errorf("a directory was created at %q, outside the data root", escaped)
	}
	wantRows(t, db, "user rows after the refused create", 0, `SELECT COUNT(*) FROM users`)
}

// TestCreateDomainRefusesEscapingHomedir is the same property for a domain's
// public store, whose directory is the domain name itself.
func TestCreateDomainRefusesEscapingHomedir(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	base := t.TempDir()
	escaped := filepath.Join(base, "data", "domain", "../../escaped-domain")
	if _, err := d.CreateDomain("../../escaped-domain", escaped); err == nil {
		t.Fatal("CreateDomain accepted a name that escapes the data root")
	}
	if _, err := os.Stat(escaped); err == nil {
		t.Errorf("a directory was created at %q, outside the data root", escaped)
	}
	wantRows(t, db, "domain rows after the refused create", 0, `SELECT COUNT(*) FROM domains`)
}
