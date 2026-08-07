package directory

import "testing"

// lockoutTestDir opens a clean directory with the settings row cleared.
func lockoutTestDir(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM login_lockout_settings"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestLoginLockoutSettingsDefaultToUnset proves an install that has never opened
// the page reports no row, which is what keeps every daemon on the limiter's
// built-in tuning rather than on a half-populated one.
func TestLoginLockoutSettingsDefaultToUnset(t *testing.T) {
	d := lockoutTestDir(t)
	s, found, err := d.GetLoginLockoutSettings()
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Errorf("an untouched install reports stored settings: %+v", s)
	}
}

// TestLoginLockoutSettingsRoundTrip proves an operator's tuning survives to the
// daemons that poll it. Without this row the thresholds lived in package constants
// and could only be changed by rebuilding the affected daemon.
func TestLoginLockoutSettingsRoundTrip(t *testing.T) {
	d := lockoutTestDir(t)
	want := LoginLockoutSettings{MaxFails: 3, WindowSeconds: 300, LockoutSeconds: 1800}
	if err := d.SetLoginLockoutSettings(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetLoginLockoutSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != want {
		t.Errorf("settings = %+v (found=%v), want %+v", got, found, want)
	}
}

// TestLoginLockoutSettingsUpsert proves a second save replaces the first rather
// than failing on the primary key, so an operator can retune repeatedly during an
// incident.
func TestLoginLockoutSettingsUpsert(t *testing.T) {
	d := lockoutTestDir(t)
	if err := d.SetLoginLockoutSettings(LoginLockoutSettings{MaxFails: 3, WindowSeconds: 300, LockoutSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	loosened := LoginLockoutSettings{MaxFails: 20, WindowSeconds: 60, LockoutSeconds: 60}
	if err := d.SetLoginLockoutSettings(loosened); err != nil {
		t.Fatal(err)
	}
	got, _, err := d.GetLoginLockoutSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != loosened {
		t.Errorf("settings = %+v, want the second save %+v", got, loosened)
	}
}
