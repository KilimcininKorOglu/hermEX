package directory

import "testing"

// TestRequirePasswordChange proves the must-change-password flag set by an admin
// reset round-trips through GetUser, and that the user clears it by changing their
// own password. A fresh account does not require a change; this is what gates the
// webmail forced-change screen.
func TestRequirePasswordChange(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "acme.test")
	mustCreateUser(t, d, root, "u@acme.test", "pw")

	// A fresh account does not require a password change.
	wantEq(t, "a fresh account requires a password change",
		mustGetUser(t, d, "u@acme.test").MustChangePassword, false)

	// An admin reset sets the flag.
	setMustChange(t, d, true)
	wantEq(t, "must_change_password after an admin reset",
		mustGetUser(t, d, "u@acme.test").MustChangePassword, true)

	// The user changing their own password clears it.
	setMustChange(t, d, false)
	wantEq(t, "must_change_password after the user changes it",
		mustGetUser(t, d, "u@acme.test").MustChangePassword, false)
}

// setMustChange writes the forced-change flag, requiring the user to exist.
func setMustChange(t *testing.T, d *SQLDirectory, required bool) {
	t.Helper()
	ok, err := d.RequirePasswordChange("u@acme.test", required)
	mustNoErr(t, "set the forced-change flag", err)
	wantEq(t, "RequirePasswordChange found the user", ok, true)
}

// TestAuthenticateDeniesMustChange proves the fail-closed flip: once an account is
// flagged for a forced password change, the strict Authenticate (the path every
// client protocol calls) denies the correct password, while the lenient
// AuthenticateAllowingPasswordChange (the webmail2 remediation channel) still
// admits it so the user can reach the change screen. Clearing the flag restores
// normal authentication. This is what stops a temporary admin-set password from
// working on IMAP/POP3/SMTP/EWS/ActiveSync/MAPI/DAV.
func TestAuthenticateDeniesMustChange(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "acme.test")
	mustCreateUser(t, d, root, "u@acme.test", "pw")
	strict := func(password string) bool {
		t.Helper()
		_, ok := d.Authenticate("u@acme.test", password)
		return ok
	}
	remediation := func(password string) bool {
		t.Helper()
		_, ok := d.AuthenticateAllowingPasswordChange("u@acme.test", password)
		return ok
	}

	// Unflagged: both paths admit the correct password.
	wantEq(t, "a fresh account authenticates on the strict path", strict("pw"), true)

	setMustChange(t, d, true)

	// Flagged: the strict path denies even the correct password...
	wantEq(t, "a flagged account on the strict path", strict("pw"), false)
	// ...but the remediation path still admits it so the user can change it.
	wantEq(t, "a flagged account on the remediation path", remediation("pw"), true)
	// A wrong password is denied by both paths regardless of the flag.
	wantEq(t, "a wrong password on the remediation path", remediation("nope"), false)

	// Clearing the flag restores normal strict authentication.
	setMustChange(t, d, false)
	wantEq(t, "the strict path after clearing the flag", strict("pw"), true)
}
