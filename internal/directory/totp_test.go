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
	return d, "u@acme.test"
}

// beginEnrollment starts an enrollment and returns its secret.
func beginEnrollment(t *testing.T, d *SQLDirectory, user string) string {
	t.Helper()
	secret, err := totp.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.BeginTOTPEnrollment(user, secret); err != nil {
		t.Fatal(err)
	}
	return secret
}

// activate turns a pending enrollment on and returns its recovery codes.
func activate(t *testing.T, d *SQLDirectory, user string, step int64) []string {
	t.Helper()
	codes, hashes, err := totp.NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ActivateTOTP(user, step, hashes); err != nil {
		t.Fatal(err)
	}
	return codes
}

// TestAnEnrollmentIsPendingUntilActivated keeps an abandoned enrollment from
// gating a login. The secret exists from the first step, but the account is not
// enrolled until a code has proved the user can produce one.
func TestAnEnrollmentIsPendingUntilActivated(t *testing.T) {
	d, user := enrolledUser(t)

	if _, ok, err := d.TOTPEnrollment(user); err != nil || ok {
		t.Fatalf("a fresh account reports an enrollment: ok %v err %v", ok, err)
	}

	secret := beginEnrollment(t, d, user)
	e, ok, err := d.TOTPEnrollment(user)
	if err != nil || !ok {
		t.Fatalf("TOTPEnrollment = ok %v err %v", ok, err)
	}
	if e.Secret != secret {
		t.Errorf("secret = %q, want %q", e.Secret, secret)
	}
	if e.Enabled {
		t.Error("a pending enrollment reports as enabled")
	}

	activate(t, d, user, 100)
	if e, _, _ := d.TOTPEnrollment(user); !e.Enabled {
		t.Error("the enrollment is still pending after activation")
	}
}

// TestAPendingEnrollmentAcceptsNoCode is why the pending state exists. A secret
// the user never confirmed must open nothing, or a half-finished setup would
// hand a second factor to whoever started it.
func TestAPendingEnrollmentAcceptsNoCode(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	if ok, err := d.ConsumeTOTPStep(user, 100); err != nil || ok {
		t.Errorf("a pending enrollment accepted a step: ok %v err %v", ok, err)
	}
}

// TestActivationRecordsTheProvingStep is the first half of the replay guard. The
// code that proved the enrollment must not also work as the first login, which
// it would if activation left last_step at zero.
func TestActivationRecordsTheProvingStep(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	step := totp.StepAt(time.Now())
	activate(t, d, user, step)
	if ok, err := d.ConsumeTOTPStep(user, step); err != nil || ok {
		t.Errorf("the enrollment code was accepted again as a login: ok %v err %v", ok, err)
	}
	if ok, err := d.ConsumeTOTPStep(user, step+1); err != nil || !ok {
		t.Errorf("the next step was refused: ok %v err %v", ok, err)
	}
}

// TestOneStepIsSpentOnce is the replay guard itself. A code stays valid for its
// whole time step, so anyone who observes one has the rest of that window to
// present it again; the second presentation has to fail.
func TestOneStepIsSpentOnce(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	activate(t, d, user, 0)
	if ok, _ := d.ConsumeTOTPStep(user, 500); !ok {
		t.Fatal("the first use was refused")
	}
	if ok, _ := d.ConsumeTOTPStep(user, 500); ok {
		t.Error("the same step was accepted twice")
	}
	// An earlier step is refused too: a code captured before this one must not
	// become usable again just because it is older.
	if ok, _ := d.ConsumeTOTPStep(user, 499); ok {
		t.Error("an earlier step was accepted after a later one")
	}
	if ok, _ := d.ConsumeTOTPStep(user, 501); !ok {
		t.Error("a later step was refused")
	}
}

// TestRecoveryCodeIsSpentOnce covers the other way in. A recovery code is the
// last resort when the authenticator is gone, and it has to stop working the
// moment it is used, or a written-down list becomes a permanent bypass.
func TestRecoveryCodeIsSpentOnce(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	codes := activate(t, d, user, 0)

	if n, err := d.RecoveryCodesRemaining(user); err != nil || n != totp.RecoveryCodeCount {
		t.Fatalf("remaining = %d err %v, want %d", n, err, totp.RecoveryCodeCount)
	}
	if ok, err := d.ConsumeRecoveryCode(user, codes[2]); err != nil || !ok {
		t.Fatalf("a valid code was refused: ok %v err %v", ok, err)
	}
	if ok, _ := d.ConsumeRecoveryCode(user, codes[2]); ok {
		t.Error("the same recovery code was accepted twice")
	}
	if n, _ := d.RecoveryCodesRemaining(user); n != totp.RecoveryCodeCount-1 {
		t.Errorf("remaining = %d, want %d", n, totp.RecoveryCodeCount-1)
	}
	// Another code from the same set still works.
	if ok, _ := d.ConsumeRecoveryCode(user, codes[3]); !ok {
		t.Error("a second code from the set was refused")
	}
	if ok, _ := d.ConsumeRecoveryCode(user, "NOTACODE"); ok {
		t.Error("an unknown recovery code was accepted")
	}
}

// TestAnActiveEnrollmentIsNotSilentlyReplaced is the session-theft guard. Anyone
// holding a live session could otherwise start a fresh enrollment, swap the
// second factor for one of their own and keep the account after the password is
// changed. Replacing it requires disabling it first, which is a deliberate act.
func TestAnActiveEnrollmentIsNotSilentlyReplaced(t *testing.T) {
	d, user := enrolledUser(t)
	first := beginEnrollment(t, d, user)
	activate(t, d, user, 0)

	second, _ := totp.NewSecret()
	if err := d.BeginTOTPEnrollment(user, second); !errors.Is(err, ErrTOTPAlreadyEnabled) {
		t.Fatalf("BeginTOTPEnrollment over an active one = %v, want ErrTOTPAlreadyEnabled", err)
	}
	if e, _, _ := d.TOTPEnrollment(user); e.Secret != first {
		t.Error("the active secret was replaced")
	}

	// A pending enrollment, by contrast, is replaced freely: a user who reloaded
	// the setup page must get the secret their new QR code shows.
	if err := d.DisableTOTP(user); err != nil {
		t.Fatal(err)
	}
	if err := d.BeginTOTPEnrollment(user, second); err != nil {
		t.Fatal(err)
	}
	third, _ := totp.NewSecret()
	if err := d.BeginTOTPEnrollment(user, third); err != nil {
		t.Fatal(err)
	}
	if e, _, _ := d.TOTPEnrollment(user); e.Secret != third {
		t.Error("a pending secret was not replaced")
	}
}

// TestDisableRemovesTheRecoveryCodes keeps a later enrollment from silently
// inheriting codes the user was never shown.
func TestDisableRemovesTheRecoveryCodes(t *testing.T) {
	d, user := enrolledUser(t)
	beginEnrollment(t, d, user)
	codes := activate(t, d, user, 0)
	if err := d.DisableTOTP(user); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.TOTPEnrollment(user); err != nil || ok {
		t.Errorf("the enrollment survived disabling: ok %v err %v", ok, err)
	}
	if n, err := d.RecoveryCodesRemaining(user); err != nil || n != 0 {
		t.Errorf("remaining = %d err %v, want 0", n, err)
	}
	if ok, _ := d.ConsumeRecoveryCode(user, codes[0]); ok {
		t.Error("a recovery code from the removed enrollment still works")
	}
}

// TestAnUnknownUserIsNotAnError keeps the login path simple: an address with no
// account reports no enrollment rather than failing, exactly as an account that
// never enrolled does, so the answer says nothing about whether the account
// exists.
func TestAnUnknownUserIsNotAnError(t *testing.T) {
	d, _ := enrolledUser(t)
	if _, ok, err := d.TOTPEnrollment("nobody@acme.test"); err != nil || ok {
		t.Errorf("TOTPEnrollment(unknown) = ok %v err %v", ok, err)
	}
	if ok, err := d.ConsumeTOTPStep("nobody@acme.test", 1); err != nil || ok {
		t.Errorf("ConsumeTOTPStep(unknown) = ok %v err %v", ok, err)
	}
	if ok, err := d.ConsumeRecoveryCode("nobody@acme.test", "X"); err != nil || ok {
		t.Errorf("ConsumeRecoveryCode(unknown) = ok %v err %v", ok, err)
	}
	if err := d.DisableTOTP("nobody@acme.test"); err != nil {
		t.Errorf("DisableTOTP(unknown) = %v", err)
	}
}

// TestSQLDirectorySatisfiesSecondFactorStore is what the login paths type-assert
// to; a signature drift would otherwise turn into a silent "no second factor".
func TestSQLDirectorySatisfiesSecondFactorStore(t *testing.T) {
	var _ SecondFactorStore = (*SQLDirectory)(nil)
}
