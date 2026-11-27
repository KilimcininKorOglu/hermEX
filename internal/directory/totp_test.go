package directory

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/totp"
)

// enrolledUser provisions one account and returns the directory. Password hashing
// is deliberately expensive, so these tests seed exactly one.
func enrolledUser(t *testing.T) (*SQLDirectory, string) {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	mustNoErr(t, "ensure the schema", d.EnsureSchema())
	cleanTables(t, db)
	root := t.TempDir()
	did, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test"))
	mustNoErr(t, "create the domain", err)
	wantEq(t, "created domain id > 0", did > 0, true)
	uid, err := d.CreateUser("u@acme.test", "password1", filepath.Join(root, "u"))
	mustNoErr(t, "create the user", err)
	wantEq(t, "created user id > 0", uid > 0, true)
	return d, "u@acme.test"
}

// newSecret mints a TOTP secret and requires the mint to succeed.
func newSecret(t *testing.T) string {
	t.Helper()
	secret, err := totp.NewSecret()
	mustNoErr(t, "mint a secret", err)
	return secret
}

// beginEnrollmentSecret starts an enrollment and returns its secret.
func beginEnrollmentSecret(t *testing.T, d *SQLDirectory, user string) string {
	t.Helper()
	secret := newSecret(t)
	mustNoErr(t, "begin the enrollment", d.BeginTOTPEnrollment(user, secret))
	return secret
}

// beginEnrollment starts an enrollment whose secret the test does not need.
func beginEnrollment(t *testing.T, d *SQLDirectory, user string) {
	t.Helper()
	wantEq(t, "a minted secret is set", beginEnrollmentSecret(t, d, user) != "", true)
}

// activateCodes turns a pending enrollment on and returns its recovery codes.
func activateCodes(t *testing.T, d *SQLDirectory, user string, step int64) []string {
	t.Helper()
	codes, hashes, err := totp.NewRecoveryCodes()
	mustNoErr(t, "mint recovery codes", err)
	mustNoErr(t, "activate the enrollment", d.ActivateTOTP(user, step, hashes))
	wantEq(t, "recovery codes issued", len(codes), totp.RecoveryCodeCount)
	return codes
}

// activate turns a pending enrollment on when the test needs no recovery code.
func activate(t *testing.T, d *SQLDirectory, user string, step int64) {
	t.Helper()
	wantEq(t, "recovery codes issued", len(activateCodes(t, d, user, step)), totp.RecoveryCodeCount)
}

// enrollment reads user's enrollment, requiring the read to succeed.
func enrollment(t *testing.T, d *SQLDirectory, user string) (TOTPEnrollment, bool) {
	t.Helper()
	e, ok, err := d.TOTPEnrollment(user)
	mustNoErr(t, "read the enrollment", err)
	return e, ok
}

// stepAccepted presents step for user and reports whether it was accepted.
func stepAccepted(t *testing.T, d *SQLDirectory, user string, step int64) bool {
	t.Helper()
	ok, err := d.ConsumeTOTPStep(user, step)
	mustNoErr(t, "consume a step", err)
	return ok
}

// codeAccepted presents a recovery code for user and reports whether it was accepted.
func codeAccepted(t *testing.T, d *SQLDirectory, user, code string) bool {
	t.Helper()
	ok, err := d.ConsumeRecoveryCode(user, code)
	mustNoErr(t, "consume a recovery code", err)
	return ok
}

// codesRemaining counts user's unspent recovery codes.
func codesRemaining(t *testing.T, d *SQLDirectory, user string) int {
	t.Helper()
	n, err := d.RecoveryCodesRemaining(user)
	mustNoErr(t, "count the recovery codes", err)
	return n
}

// TestAnEnrollmentIsPendingUntilActivated keeps an abandoned enrollment from
// gating a login. The secret exists from the first step, but the account is not
// enrolled until a code has proved the user can produce one.
func TestAnEnrollmentIsPendingUntilActivated(t *testing.T) {
	d, user := enrolledUser(t)
	empty, ok := enrollment(t, d, user)
	wantEq(t, "a fresh account reports an enrollment", ok, false)
	wantEq(t, "fresh enrollment zero struct", empty, TOTPEnrollment{})

	secret := beginEnrollmentSecret(t, d, user)
	e, ok := enrollment(t, d, user)
	wantEq(t, "the pending enrollment is found", ok, true)
	wantEq(t, "the pending secret", e.Secret, secret)
	wantEq(t, "a pending enrollment reports as enabled", e.Enabled, false)

	activate(t, d, user, 100)
	e, ok = enrollment(t, d, user)
	wantEq(t, "activated enrollment found", ok, true)
	wantEq(t, "enabled after activation", e.Enabled, true)
}

// TestAPendingEnrollmentAcceptsNoCode is why the pending state exists. A secret
// the user never confirmed must open nothing, or a half-finished setup would
// hand a second factor to whoever started it.
func TestAPendingEnrollmentAcceptsNoCode(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	wantEq(t, "a pending enrollment accepted a step", stepAccepted(t, d, user, 100), false)
}

// TestActivationRecordsTheProvingStep is the first half of the replay guard. The
// code that proved the enrollment must not also work as the first login, which
// it would if activation left last_step at zero.
func TestActivationRecordsTheProvingStep(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	step := totp.StepAt(time.Now())
	activate(t, d, user, step)
	wantEq(t, "the enrollment code accepted again as a login", stepAccepted(t, d, user, step), false)
	wantEq(t, "the next step accepted", stepAccepted(t, d, user, step+1), true)
}

// TestOneStepIsSpentOnce is the replay guard itself. A code stays valid for its
// whole time step, so anyone who observes one has the rest of that window to
// present it again; the second presentation has to fail.
func TestOneStepIsSpentOnce(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	activate(t, d, user, 0)
	wantEq(t, "the first use accepted", stepAccepted(t, d, user, 500), true)
	wantEq(t, "the same step accepted twice", stepAccepted(t, d, user, 500), false)
	// An earlier step is refused too: a code captured before this one must not
	// become usable again just because it is older.
	wantEq(t, "an earlier step accepted after a later one", stepAccepted(t, d, user, 499), false)
	wantEq(t, "a later step accepted", stepAccepted(t, d, user, 501), true)
}

// TestRecoveryCodeIsSpentOnce covers the other way in. A recovery code is the
// last resort when the authenticator is gone, and it has to stop working the
// moment it is used, or a written-down list becomes a permanent bypass.
func TestRecoveryCodeIsSpentOnce(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	codes := activateCodes(t, d, user, 0)

	wantEq(t, "codes before any use", codesRemaining(t, d, user), totp.RecoveryCodeCount)
	wantEq(t, "a valid code accepted", codeAccepted(t, d, user, codes[2]), true)
	wantEq(t, "the same code accepted twice", codeAccepted(t, d, user, codes[2]), false)
	wantEq(t, "codes after one use", codesRemaining(t, d, user), totp.RecoveryCodeCount-1)
	// Another code from the same set still works.
	wantEq(t, "a second code from the set accepted", codeAccepted(t, d, user, codes[3]), true)
	wantEq(t, "an unknown code accepted", codeAccepted(t, d, user, "NOTACODE"), false)
}

// TestRecoveryCodeIsBoundToItsAccount spends one account's recovery code on
// another enrolled account. A code is a second factor for the account that was
// issued it, so it must open no other account and stay unspent for its owner.
func TestRecoveryCodeIsBoundToItsAccount(t *testing.T) {
	d, user := enrolledUser(t)
	vID, err := d.CreateUser("v@acme.test", "password1", filepath.Join(t.TempDir(), "v"))
	mustNoErr(t, "create the second user", err)
	wantEq(t, "second user id > 0", vID > 0, true)
	beginEnrollment(t, d, user)
	codes := activateCodes(t, d, user, 0)
	beginEnrollment(t, d, "v@acme.test")
	activate(t, d, "v@acme.test", 0)

	wantEq(t, "one account's code opened another", codeAccepted(t, d, "v@acme.test", codes[0]), false)
	wantEq(t, "the owner's code after the other account's attempt", codeAccepted(t, d, user, codes[0]), true)
}

// TestAnActiveEnrollmentIsNotSilentlyReplaced is the session-theft guard. Anyone
// holding a live session could otherwise start a fresh enrollment, swap the
// second factor for one of their own and keep the account after the password is
// changed. Replacing it requires disabling it first, which is a deliberate act.
func TestAnActiveEnrollmentIsNotSilentlyReplaced(t *testing.T) {
	d, user := enrolledUser(t)
	first := beginEnrollmentSecret(t, d, user)
	activate(t, d, user, 0)

	second := newSecret(t)
	if err := d.BeginTOTPEnrollment(user, second); !errors.Is(err, ErrTOTPAlreadyEnabled) {
		t.Fatalf("BeginTOTPEnrollment over an active one = %v, want ErrTOTPAlreadyEnabled", err)
	}
	e, ok := enrollment(t, d, user)
	wantEq(t, "active enrollment found", ok, true)
	wantEq(t, "the active secret after the refused replace", e.Secret, first)

	// A pending enrollment, by contrast, is replaced freely: a user who reloaded
	// the setup page must get the secret their new QR code shows.
	mustNoErr(t, "disable the second factor", d.DisableTOTP(user))
	mustNoErr(t, "begin a new enrollment", d.BeginTOTPEnrollment(user, second))
	third := newSecret(t)
	mustNoErr(t, "replace the pending enrollment", d.BeginTOTPEnrollment(user, third))
	e, ok = enrollment(t, d, user)
	wantEq(t, "replaced enrollment found", ok, true)
	wantEq(t, "the replaced pending secret", e.Secret, third)
}

// TestDisableRemovesTheRecoveryCodes keeps a later enrollment from silently
// inheriting codes the user was never shown.
func TestDisableRemovesTheRecoveryCodes(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	codes := activateCodes(t, d, user, 0)
	mustNoErr(t, "disable the second factor", d.DisableTOTP(user))
	empty, ok := enrollment(t, d, user)
	wantEq(t, "the enrollment survived disabling", ok, false)
	wantEq(t, "disabled enrollment zero struct", empty, TOTPEnrollment{})
	wantEq(t, "codes after disabling", codesRemaining(t, d, user), 0)
	wantEq(t, "a code from the removed enrollment accepted", codeAccepted(t, d, user, codes[0]), false)
}

// TestAnUnknownUserIsNotAnError keeps the login path simple: an address with no
// account reports no enrollment rather than failing, exactly as an account that
// never enrolled does, so the answer says nothing about whether the account
// exists.
func TestAnUnknownUserIsNotAnError(t *testing.T) {
	d, user := enrolledUser(t)
	// The answer for an unknown address equals the answer for the seeded account
	// that never enrolled.
	never, neverOK := enrollment(t, d, user)
	empty, ok := enrollment(t, d, "nobody@acme.test")
	wantEq(t, "the unknown and never-enrolled answers match", ok == neverOK && empty == never, true)
	wantEq(t, "an unknown user's enrollment", ok, false)
	wantEq(t, "unknown user zero struct", empty, TOTPEnrollment{})
	wantEq(t, "an unknown user's step", stepAccepted(t, d, "nobody@acme.test", 1), false)
	wantEq(t, "an unknown user's recovery code", codeAccepted(t, d, "nobody@acme.test", "X"), false)
	mustNoErr(t, "disable for an unknown user", d.DisableTOTP("nobody@acme.test"))
}

// TestSQLDirectorySatisfiesSecondFactorStore is what the login paths type-assert
// to; a signature drift would otherwise turn into a silent "no second factor".
func TestSQLDirectorySatisfiesSecondFactorStore(t *testing.T) {
	var _ SecondFactorStore = (*SQLDirectory)(nil)
}
