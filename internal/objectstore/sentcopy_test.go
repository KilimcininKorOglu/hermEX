package objectstore

import "testing"

// TestSentCopyConfigRoundTrip proves the two flags survive the store and are two
// answers, not one: a mailbox that wants a copy of what is sent AS it need not want
// one of every on-behalf send.
func TestSentCopyConfigRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// An unconfigured mailbox keeps no copy, so an upgrade changes no behaviour.
	cfg, err := st.GetSentCopyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ForSendAs || cfg.ForSendOnBehalf {
		t.Errorf("an unset config read as %+v, want both false", cfg)
	}

	for _, want := range []SentCopyConfig{
		{ForSendAs: true},
		{ForSendOnBehalf: true},
		{ForSendAs: true, ForSendOnBehalf: true},
		{},
	} {
		if err := st.SetSentCopyConfig(want); err != nil {
			t.Fatalf("set %+v: %v", want, err)
		}
		got, err := st.GetSentCopyConfig()
		if err != nil {
			t.Fatalf("get after %+v: %v", want, err)
		}
		if got != want {
			t.Errorf("config = %+v, want %+v", got, want)
		}
	}
}
