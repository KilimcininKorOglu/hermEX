package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

// TestOOFSettingsRoundTrip stores out-of-office settings and reads them back,
// and checks the standard PR_OOF_STATE boolean is kept in sync with Enabled so
// the delivery path and a MAPI client see the same on/off state.
func TestOOFSettingsRoundTrip(t *testing.T) {
	s := openTestStore(t)

	// Default (nothing stored) is disabled.
	got, err := s.GetOOFSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Errorf("a fresh mailbox should have OOF disabled")
	}

	want := OOFSettings{
		Enabled:          true,
		InternalSubject:  "Away until Monday",
		InternalReply:    "I am out of the office, back Monday.",
		ExternalSubject:  "Out of office",
		ExternalReply:    "Thanks for your message; I will reply when I return.",
		ExternalEnabled:  true,
		ExternalAudience: OOFExternalKnown,
		Start:            1700000000,
		End:              1700600000,
	}
	if err := s.SetOOFSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetOOFSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	// PR_OOF_STATE mirrors Enabled.
	props, err := s.GetStoreProperties(mapi.PrOOFState)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := props.Get(mapi.PrOOFState); v != true {
		t.Errorf("PR_OOF_STATE = %v, want true to mirror Enabled", v)
	}

	// Disabling clears the standard flag too.
	want.Enabled = false
	if err := s.SetOOFSettings(want); err != nil {
		t.Fatal(err)
	}
	props, _ = s.GetStoreProperties(mapi.PrOOFState)
	if v, _ := props.Get(mapi.PrOOFState); v != false {
		t.Errorf("PR_OOF_STATE = %v, want false after disabling", v)
	}
}

// TestOOFSettingsLegacySubject proves a config stored before the subject was split
// per audience — carrying the single "subject" key — still surfaces its reply
// subject after the upgrade, folded into InternalSubject rather than silently lost.
func TestOOFSettingsLegacySubject(t *testing.T) {
	s := openTestStore(t)
	legacy := `{"enabled":true,"subject":"Away until Monday","internalReply":"Back Monday."}`
	if err := s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrOOFSettings, Value: legacy},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOOFSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.InternalSubject != "Away until Monday" {
		t.Errorf("legacy subject = %q, want it folded into InternalSubject", got.InternalSubject)
	}
	if !got.Enabled || got.InternalReply != "Back Monday." {
		t.Errorf("legacy blob mis-decoded: %+v", got)
	}
}

// TestOOFActive covers the on/off and schedule-window gate that decides whether
// an auto-reply fires for a given delivery time.
func TestOOFActive(t *testing.T) {
	cases := []struct {
		name string
		cfg  OOFSettings
		now  int64
		want bool
	}{
		{"disabled", OOFSettings{Enabled: false}, 1700000500, false},
		{"enabled no window", OOFSettings{Enabled: true}, 1700000500, true},
		{"before start", OOFSettings{Enabled: true, Start: 1700000000}, 1699999999, false},
		{"at start", OOFSettings{Enabled: true, Start: 1700000000}, 1700000000, true},
		{"after end", OOFSettings{Enabled: true, End: 1700000000}, 1700000001, false},
		{"at end", OOFSettings{Enabled: true, End: 1700000000}, 1700000000, true},
		{"within window", OOFSettings{Enabled: true, Start: 1700000000, End: 1700600000}, 1700300000, true},
	}
	for _, c := range cases {
		if got := c.cfg.OOFActive(c.now); got != c.want {
			t.Errorf("%s: OOFActive(%d) = %v, want %v", c.name, c.now, got, c.want)
		}
	}
}
