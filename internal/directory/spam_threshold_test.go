package directory

import (
	"path/filepath"
	"testing"
)

func setupSpamThreshold(t *testing.T) (*SQLDirectory, string) {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	maildir := filepath.Join(root, "users", "alice")
	if _, err := d.CreateUser("alice@hermex.test", "secret-pw1", maildir); err != nil {
		t.Fatal(err)
	}
	return d, maildir
}

// TestSpamThresholdResolution proves the per-recipient threshold resolves user
// override → domain override → none (so the caller inherits the global threshold),
// keyed by maildir.
func TestSpamThresholdResolution(t *testing.T) {
	d, maildir := setupSpamThreshold(t)
	resolve := func(what string) (int, bool) {
		t.Helper()
		th, ok, err := d.SpamThresholdForMaildir(maildir)
		mustNoErr(t, "resolve the threshold "+what, err)
		return th, ok
	}

	// No overrides → not found (the caller uses the global threshold).
	_, ok := resolve("with no override")
	wantEq(t, "a fresh user resolves to an override", ok, false)

	// A domain override applies when the user has none.
	dom := 12
	mustNoErr(t, "set the domain override", d.SetDomainSpamThreshold("hermex.test", &dom))
	th, ok := resolve("with a domain override")
	wantEq(t, "the domain override was found", ok, true)
	wantEq(t, "the resolved threshold", th, 12)

	// A user override beats the domain override.
	usr := 4
	mustNoErr(t, "set the user override", d.SetUserSpamThreshold("alice@hermex.test", &usr))
	th, ok = resolve("with a user override")
	wantEq(t, "the user override was found", ok, true)
	wantEq(t, "the resolved threshold", th, 4)

	// Clearing the user override falls back to the domain override.
	mustNoErr(t, "clear the user override", d.SetUserSpamThreshold("alice@hermex.test", nil))
	th, ok = resolve("after clearing the user override")
	wantEq(t, "the domain override was found again", ok, true)
	wantEq(t, "the resolved threshold", th, 12)
}

// TestSpamThresholdRefusesBelowOne proves a threshold below 1, which flags every
// message as spam, is refused at both scopes and the stored override kept.
func TestSpamThresholdRefusesBelowOne(t *testing.T) {
	d, _ := setupSpamThreshold(t)
	keep := 6
	mustNoErr(t, "set the user threshold", d.SetUserSpamThreshold("alice@hermex.test", &keep))
	mustNoErr(t, "set the domain threshold", d.SetDomainSpamThreshold("hermex.test", &keep))
	for _, bad := range []int{0, -3} {
		wantErr(t, "a user threshold below 1 was accepted", d.SetUserSpamThreshold("alice@hermex.test", &bad))
		wantErr(t, "a domain threshold below 1 was accepted", d.SetDomainSpamThreshold("hermex.test", &bad))
	}
	u, err := d.GetUserSpamThreshold("alice@hermex.test")
	mustNoErr(t, "read the user threshold", err)
	wantSet(t, "the user threshold after the refusals", u, 6)
	dom, err := d.GetDomainSpamThreshold("hermex.test")
	mustNoErr(t, "read the domain threshold", err)
	wantSet(t, "the domain threshold after the refusals", dom, 6)
}

// TestSpamThresholdRefusesAnUnknownAccount proves a threshold for an account that
// does not exist is reported as an error, so the panel cannot show "Saved" for a
// change that stored nothing; re-saving an existing account's same value is not.
func TestSpamThresholdRefusesAnUnknownAccount(t *testing.T) {
	d, _ := setupSpamThreshold(t)
	n := 6
	wantErr(t, "a threshold for an unknown account was accepted", d.SetUserSpamThreshold("nobody@hermex.test", &n))
	mustNoErr(t, "set the user threshold", d.SetUserSpamThreshold("alice@hermex.test", &n))
	mustNoErr(t, "re-save the same threshold", d.SetUserSpamThreshold("alice@hermex.test", &n))
}

// TestSpamThresholdGetReflectsSet proves the per-scope getters read back what was set
// and report nil (inherit) once cleared.
func TestSpamThresholdGetReflectsSet(t *testing.T) {
	d, _ := setupSpamThreshold(t)
	userThreshold := func() *int {
		t.Helper()
		v, err := d.GetUserSpamThreshold("alice@hermex.test")
		mustNoErr(t, "get the user threshold", err)
		return v
	}

	if v := userThreshold(); v != nil {
		t.Fatalf("a fresh user threshold = %d, want unset", *v)
	}
	n := 7
	mustNoErr(t, "set the user threshold", d.SetUserSpamThreshold("alice@hermex.test", &n))
	wantSet(t, "the user threshold after the set", userThreshold(), 7)
	mustNoErr(t, "clear the user threshold", d.SetUserSpamThreshold("alice@hermex.test", nil))
	if v := userThreshold(); v != nil {
		t.Errorf("the user threshold after clearing = %d, want unset", *v)
	}

	dn := 15
	mustNoErr(t, "set the domain threshold", d.SetDomainSpamThreshold("hermex.test", &dn))
	v, err := d.GetDomainSpamThreshold("hermex.test")
	mustNoErr(t, "get the domain threshold", err)
	wantSet(t, "the domain threshold after the set", v, 15)
}
