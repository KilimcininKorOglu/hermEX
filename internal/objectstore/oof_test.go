package objectstore

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"hermex/internal/mapi"
)

// TestUpdateOOFSettingsLosesNoEdit proves concurrent merges through
// UpdateOOFSettings, from two handles as two daemons (EWS and ActiveSync) would,
// each land: every writer appends one mark to the reply, so a merge applied to a
// copy read before another's commit would drop marks. It also proves
// PR_OOF_STATE follows the merged Enabled.
func TestUpdateOOFSettingsLosesNoEdit(t *testing.T) {
	dir := t.TempDir()
	const writers, rounds = 2, 20
	mergeConcurrently(t, dir, writers, rounds)
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg, err := st.GetOOFSettings()
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("x", writers*rounds); cfg.InternalReply != want {
		t.Errorf("reply has %d marks after %d merges", len(cfg.InternalReply), writers*rounds)
	}
	props, err := st.GetStoreProperties(mapi.PrOOFState)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := props.Get(mapi.PrOOFState); v != true {
		t.Errorf("PR_OOF_STATE = %v after enabling, want true", v)
	}
}

// mergeConcurrently runs rounds OOF merges from each of writers handles on dir at
// once; each merge appends one mark to the internal reply and enables the reply.
func mergeConcurrently(t *testing.T, dir string, writers, rounds int) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for range writers {
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		wg.Go(func() {
			for range rounds {
				errs <- st.UpdateOOFSettings(func(c *OOFSettings) error {
					c.InternalReply += "x"
					c.Enabled = true
					return nil
				})
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

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
	props, err = s.GetStoreProperties(mapi.PrOOFState)
	mustNoErr(t, "read PR_OOF_STATE", err)
	if v, _ := props.Get(mapi.PrOOFState); v != false {
		t.Errorf("PR_OOF_STATE = %v, want false after disabling", v)
	}
}

// TestOOFSettingsLegacySubject proves a config stored before the subject was split
// per audience, carrying the single "subject" key, still surfaces its reply
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

// TestOOFSettingsRefusesACorruptBlob: a stored settings blob that is not JSON is
// reported as an error rather than read as "out of office is off", which would
// silently stop the replies the user set up.
func TestOOFSettingsRefusesACorruptBlob(t *testing.T) {
	s := openTestStore(t)
	mustNoErr(t, "store a corrupt blob", s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrOOFSettings, Value: `{"enabled":true,"internalReply":`},
	}))
	if _, err := s.GetOOFSettings(); err == nil {
		t.Error("a corrupt OOF settings blob read back without an error")
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

// TestOOFWindowIsChecked writes schedules through both store writers: a window
// that ends before it starts, or a bound that is not a time (-1, what a surface
// reads from an unparseable date), is refused and the stored settings stay; a
// single unset bound is the open-ended schedule OOFActive defines and is kept.
func TestOOFWindowIsChecked(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	kept := OOFSettings{Enabled: true, InternalReply: "kept", Start: 1000, End: 2000}
	mustNoErr(t, "store a valid schedule", st.SetOOFSettings(kept))
	bad := []OOFSettings{
		{Enabled: true, Start: 2000, End: 1000},
		{Enabled: true, Start: 1000, End: 1000},
		{Enabled: true, Start: -1, End: 2000},
		{Enabled: true, Start: 1000, End: -1},
	}
	for _, cfg := range bad {
		if err := st.SetOOFSettings(cfg); !errors.Is(err, ErrInvalidOOFWindow) {
			t.Errorf("SetOOFSettings(%d..%d) = %v, want ErrInvalidOOFWindow", cfg.Start, cfg.End, err)
		}
		err := st.UpdateOOFSettings(func(c *OOFSettings) error { *c = cfg; return nil })
		if !errors.Is(err, ErrInvalidOOFWindow) {
			t.Errorf("UpdateOOFSettings(%d..%d) = %v, want ErrInvalidOOFWindow", cfg.Start, cfg.End, err)
		}
	}
	got, err := st.GetOOFSettings()
	mustNoErr(t, "read the OOF settings", err)
	if got != kept {
		t.Errorf("settings after the refused windows = %+v, want the stored %+v", got, kept)
	}
	for _, cfg := range []OOFSettings{{Enabled: true, Start: 1000}, {Enabled: true, End: 2000}, {Enabled: true, Start: 1000, End: 2000}} {
		if err := st.SetOOFSettings(cfg); err != nil {
			t.Errorf("SetOOFSettings(%d..%d) = %v, want stored", cfg.Start, cfg.End, err)
		}
	}
}
