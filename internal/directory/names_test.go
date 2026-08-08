package directory

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if _, err := d.CreateUser(address, "pw", escaped); err == nil {
		t.Fatal("CreateUser accepted an address that escapes the data root")
	}
	if _, err := os.Stat(escaped); err == nil {
		t.Errorf("a directory was created at %q, outside the data root", escaped)
	}
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
}
