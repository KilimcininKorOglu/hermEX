package directory

import (
	"errors"
	"path/filepath"
	"testing"
)

func policyTestDir(t *testing.T) (*SQLDirectory, string) {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	seedDomain(t, d, root, "acme.test")
	return d, root
}

// TestNewPasswordsMeetTheDefaultMinimum proves an account created, reset or changed
// with a password under 8 characters is refused while no minimum was saved, and
// the refusal names the minimum a caller shows the user.
func TestNewPasswordsMeetTheDefaultMinimum(t *testing.T) {
	d, root := policyTestDir(t)
	badUID, err := d.CreateUser("short@acme.test", "1234567", filepath.Join(root, "short"))
	minLen, ok := MinPasswordLength(err)
	wantEq(t, "MinPasswordLength ok", ok, true)
	wantEq(t, "MinPasswordLength value", minLen, 8)
	wantEq(t, "CreateUser ErrPasswordTooShort", errors.Is(err, ErrPasswordTooShort), true)
	wantEq(t, "refused user id", badUID, int64(0))
	p, ok := d.Authenticate("short@acme.test", "1234567")
	wantEq(t, "refused user login ok", ok, false)
	wantEq(t, "refused user login maildir", p, "")
	seedUser(t, d, root, "u@acme.test", "12345678")
	changed, err := d.SetPassword("u@acme.test", "1234567")
	wantEq(t, "SetPassword ErrPasswordTooShort", errors.Is(err, ErrPasswordTooShort), true)
	wantEq(t, "SetPassword changed", changed, false)
	p, ok = d.Authenticate("u@acme.test", "12345678")
	wantEq(t, "old password preserved ok", ok, true)
	wantEq(t, "old password maildir non-empty", p != "", true)
}

// TestPasswordLengthCountsCharacters proves the minimum counts characters, not
// bytes: "şifre12" is 7 characters in 9 bytes and must be refused, and an
// 8-character password with non-ASCII letters must be accepted.
func TestPasswordLengthCountsCharacters(t *testing.T) {
	d, root := policyTestDir(t)
	seedUser(t, d, root, "u@acme.test", "12345678")
	if changed, err := d.SetPassword("u@acme.test", "şifre12"); !errors.Is(err, ErrPasswordTooShort) || changed {
		t.Errorf("a 7-character, 9-byte password = %v, %v; want false, ErrPasswordTooShort", changed, err)
	}
	if changed, err := d.SetPassword("u@acme.test", "şifre123"); err != nil || !changed {
		t.Errorf("an 8-character password with ş = %v, %v; want true, nil", changed, err)
	}
}

// TestASavedMinimumAppliesToTheNextPassword proves a saved minimum governs the next
// password set with no restart, in both directions, and that a password stored
// under an older, lower minimum still signs in.
func TestASavedMinimumAppliesToTheNextPassword(t *testing.T) {
	d, root := policyTestDir(t)
	if err := d.SetMinPasswordLength(4); err != nil {
		t.Fatal(err)
	}
	seedUser(t, d, root, "old@acme.test", "abcd")
	if err := d.SetMinPasswordLength(12); err != nil {
		t.Fatal(err)
	}
	if got, found, err := d.GetMinPasswordLength(); err != nil || !found || got != 12 {
		t.Fatalf("stored minimum = %d %v %v, want 12 saved", got, found, err)
	}
	if badUID, err := d.CreateUser("new@acme.test", "eleven-char", filepath.Join(root, "new")); !errors.Is(err, ErrPasswordTooShort) || badUID != 0 {
		t.Errorf("an 11-character password under a 12 minimum = id=%d err=%v, want refused", badUID, err)
	}
	seedUser(t, d, root, "new@acme.test", "twelve-chars")
	if p, ok := d.Authenticate("old@acme.test", "abcd"); !ok || p == "" {
		t.Errorf("a password stored under the older minimum no longer signs in: %q %v", p, ok)
	}
	// Lowering the minimum applies to the next password too.
	mustNoErr(t, "lower the minimum", d.SetMinPasswordLength(4))
	changed, err := d.SetPassword("new@acme.test", "wxyz")
	mustNoErr(t, "set a 4-character password under a 4 minimum", err)
	wantEq(t, "SetPassword found new@acme.test", changed, true)
}

// TestTheMinimumIsBounded proves a minimum below 1 or above the cap is refused and
// leaves the stored value, so a typo cannot make every password impossible.
func TestTheMinimumIsBounded(t *testing.T) {
	d, root := policyTestDir(t)
	if err := d.SetMinPasswordLength(10); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int{0, -1, MaxMinPasswordLength + 1} {
		if err := d.SetMinPasswordLength(bad); err == nil {
			t.Errorf("minimum %d accepted", bad)
		}
	}
	got, found, err := d.GetMinPasswordLength()
	mustNoErr(t, "read the minimum after the refusals", err)
	wantEq(t, "minimum found after the refusals", found, true)
	wantEq(t, "stored minimum after the refusals", got, 10)
	// The kept minimum still governs: a 9-character password is refused.
	badUID, err := d.CreateUser("nine@acme.test", "nine-char", filepath.Join(root, "nine"))
	wantEq(t, "a 9-character password under the kept minimum", errors.Is(err, ErrPasswordTooShort), true)
	wantEq(t, "refused 9-character user id", badUID, int64(0))
}
