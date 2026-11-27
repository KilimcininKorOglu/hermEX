package directory

import (
	"errors"
	"testing"
)

// TestUserPrefsRoundTrip proves the interface preferences start at their defaults,
// that a write changes only the fields it names, that repeating a stored value is
// not reported as an unknown user, and that a value outside the accepted set is
// refused without touching the record.
func TestUserPrefsRoundTrip(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "acme.test")
	seedUser(t, d, root, "u@acme.test", "password1")
	prefs := func(what string) UserPrefs {
		t.Helper()
		p, found, err := d.GetUserPrefs("u@acme.test")
		mustNoErr(t, what, err)
		wantEq(t, what+": the user exists", found, true)
		return p
	}
	set := func(what string, u UserPrefsUpdate) {
		t.Helper()
		found, err := d.SetUserPrefs("U@acme.test", u)
		mustNoErr(t, what, err)
		wantEq(t, what+": the user exists", found, true)
	}
	str := func(s string) *string { return &s }
	no := false

	wantEq(t, "the defaults", prefs("read the defaults"), UserPrefs{ShowWelcome: true})

	set("set the theme", UserPrefsUpdate{Theme: str("dark")})
	set("set the language and dismiss the banner", UserPrefsUpdate{Lang: str("tr"), ShowWelcome: &no})
	wantEq(t, "the merged record", prefs("read the merged record"), UserPrefs{Theme: "dark", Lang: "tr"})

	set("repeat the stored theme", UserPrefsUpdate{Theme: str("dark")})
	set("follow the browser language", UserPrefsUpdate{Lang: str("")})
	wantEq(t, "the cleared language", prefs("read the cleared language").Lang, "")

	set("choose a time zone", UserPrefsUpdate{Timezone: str("Europe/Istanbul")})
	wantEq(t, "the stored time zone", prefs("read the time zone").Timezone, "Europe/Istanbul")
	set("clear the time zone", UserPrefsUpdate{Timezone: str("")})
	wantEq(t, "the cleared time zone", prefs("read the cleared time zone").Timezone, "")

	for _, u := range []UserPrefsUpdate{{Theme: str("blue")}, {Lang: str("de")}, {Timezone: str("Mars/Olympus")}, {Timezone: str("Local")}} {
		if ok, err := d.SetUserPrefs("u@acme.test", u); !errors.Is(err, ErrInvalidPref) || ok {
			t.Errorf("SetUserPrefs(%+v) = %v, %v; want false, ErrInvalidPref", u, ok, err)
		}
	}
	wantEq(t, "the record after the refused writes", prefs("read after refused writes"), UserPrefs{Theme: "dark"})

	found, err := d.SetUserPrefs("ghost@acme.test", UserPrefsUpdate{Theme: str("light")})
	mustNoErr(t, "write an unknown user", err)
	wantEq(t, "an unknown user is reported", found, false)
	emptyP, found, err := d.GetUserPrefs("ghost@acme.test")
	mustNoErr(t, "read an unknown user", err)
	wantEq(t, "an unknown user reads as absent", found, false)
	wantEq(t, "absent prefs", emptyP, UserPrefs{})
}
