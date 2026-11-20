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
	mustCreateDomain(t, d, root, "acme.test")
	return d, root
}

// TestNewPasswordsMeetTheDefaultMinimum proves an account created, reset or changed
// with a password under 8 characters is refused while no minimum was saved, and
// the refusal names the minimum a caller shows the user.
func TestNewPasswordsMeetTheDefaultMinimum(t *testing.T) {
	d, root := policyTestDir(t)
	_, err := d.CreateUser("short@acme.test", "1234567", filepath.Join(root, "short"))
	if minLen, ok := MinPasswordLength(err); !ok || minLen != 8 || !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("CreateUser with 7 characters = %v, want a too-short refusal naming 8", err)
	}
	if _, ok := d.Authenticate("short@acme.test", "1234567"); ok {
		t.Error("the refused account exists and signs in")
	}
	mustCreateUser(t, d, root, "u@acme.test", "12345678")
	if _, err := d.SetPassword("u@acme.test", "1234567"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("SetPassword with 7 characters = %v, want ErrPasswordTooShort", err)
	}
	if _, ok := d.Authenticate("u@acme.test", "12345678"); !ok {
		t.Error("a refused change replaced the old password")
	}
}

// TestPasswordLengthCountsCharacters proves the minimum counts characters, not
// bytes: "şifre12" is 7 characters in 9 bytes and must be refused, and an
// 8-character password with non-ASCII letters must be accepted.
func TestPasswordLengthCountsCharacters(t *testing.T) {
	d, root := policyTestDir(t)
	mustCreateUser(t, d, root, "u@acme.test", "12345678")
	if _, err := d.SetPassword("u@acme.test", "şifre12"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("a 7-character, 9-byte password = %v, want ErrPasswordTooShort", err)
	}
	if _, err := d.SetPassword("u@acme.test", "şifre123"); err != nil {
		t.Errorf("an 8-character password with ş = %v, want accepted", err)
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
	mustCreateUser(t, d, root, "old@acme.test", "abcd")
	if err := d.SetMinPasswordLength(12); err != nil {
		t.Fatal(err)
	}
	if got, found, err := d.GetMinPasswordLength(); err != nil || !found || got != 12 {
		t.Fatalf("stored minimum = %d %v %v, want 12 saved", got, found, err)
	}
	if _, err := d.CreateUser("new@acme.test", "eleven-char", filepath.Join(root, "new")); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("an 11-character password under a 12 minimum = %v, want refused", err)
	}
	mustCreateUser(t, d, root, "new@acme.test", "twelve-chars")
	if _, ok := d.Authenticate("old@acme.test", "abcd"); !ok {
		t.Error("a password stored under the older minimum no longer signs in")
	}
}

// TestTheMinimumIsBounded proves a minimum below 1 or above the cap is refused and
// leaves the stored value, so a typo cannot make every password impossible.
func TestTheMinimumIsBounded(t *testing.T) {
	d, _ := policyTestDir(t)
	if err := d.SetMinPasswordLength(10); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int{0, -1, MaxMinPasswordLength + 1} {
		if err := d.SetMinPasswordLength(bad); err == nil {
			t.Errorf("minimum %d accepted", bad)
		}
	}
	if got, _, _ := d.GetMinPasswordLength(); got != 10 {
		t.Errorf("stored minimum after the refusals = %d, want 10", got)
	}
}
