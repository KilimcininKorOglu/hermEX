package directory

import (
	"path/filepath"
	"testing"
)

// TestSetUserLocale proves the webmail-facing locale write persists the user's
// timezone + language and, crucially, leaves the rest of the record untouched.
// That no-clobber property is the whole reason webmail uses the narrow
// SetUserLocale instead of the admin UpdateUser (which rewrites maildir,
// homeserver, status and privilege bits): a user changing their timezone during
// onboarding must never wipe their password or mailbox path.
func TestSetUserLocale(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()

	if _, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("u@acme.test", "pw", filepath.Join(root, "u")); err != nil {
		t.Fatal(err)
	}

	// A fresh user has no locale set; capture the record to diff against later.
	before, ok, err := d.GetUser("u@acme.test")
	if err != nil || !ok {
		t.Fatalf("GetUser fresh = ok %v, err %v", ok, err)
	}
	if before.Timezone != "" || before.Lang != "" {
		t.Fatalf("fresh user locale = (%q,%q), want empty", before.Timezone, before.Lang)
	}

	if ok, err := d.SetUserLocale("u@acme.test", "America/New_York", "en"); err != nil || !ok {
		t.Fatalf("SetUserLocale = ok %v, err %v", ok, err)
	}

	after, ok, err := d.GetUser("u@acme.test")
	if err != nil || !ok {
		t.Fatalf("GetUser after set = ok %v, err %v", ok, err)
	}
	if after.Timezone != "America/New_York" || after.Lang != "en" {
		t.Errorf("locale round-trip = (%q,%q), want (America/New_York, en)", after.Timezone, after.Lang)
	}

	// No-clobber: the narrow write must not disturb the rest of the record.
	if _, ok := d.Authenticate("u@acme.test", "pw"); !ok {
		t.Error("password no longer authenticates after a locale write")
	}
	if after.Maildir != before.Maildir {
		t.Errorf("maildir changed after a locale write = %q, want %q", after.Maildir, before.Maildir)
	}
}
