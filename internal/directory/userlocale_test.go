package directory

import "testing"

// TestSetUserLocale proves the webmail-facing locale write persists the user's
// timezone + language and, crucially, leaves the rest of the record untouched.
// That no-clobber property is the whole reason webmail uses the narrow
// SetUserLocale instead of the admin UpdateUser (which rewrites maildir,
// homeserver, status and privilege bits): a user changing their timezone during
// onboarding must never wipe their password or mailbox path.
func TestSetUserLocale(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "acme.test")
	mustCreateUser(t, d, root, "u@acme.test", "pw")

	// A fresh user has no locale set; capture the record to diff against later.
	before := mustGetUser(t, d, "u@acme.test")
	wantEq(t, "fresh user timezone", before.Timezone, "")
	wantEq(t, "fresh user lang", before.Lang, "")

	ok, err := d.SetUserLocale("u@acme.test", "America/New_York", "en")
	mustNoErr(t, "set user locale", err)
	wantEq(t, "SetUserLocale found the user", ok, true)

	after := mustGetUser(t, d, "u@acme.test")
	wantEq(t, "timezone after the write", after.Timezone, "America/New_York")
	wantEq(t, "lang after the write", after.Lang, "en")

	// No-clobber: the narrow write must not disturb the rest of the record.
	_, authed := d.Authenticate("u@acme.test", "pw")
	wantEq(t, "the password still authenticates after a locale write", authed, true)
	wantEq(t, "maildir after a locale write", after.Maildir, before.Maildir)
}
