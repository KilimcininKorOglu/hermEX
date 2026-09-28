package objectstore

import "testing"

// TestReadReceiptConfigRoundTrip proves an unconfigured mailbox asks in webmail and
// sends from ActiveSync, that every setting survives the store, and that a response
// outside the three choices is refused rather than stored.
func TestReadReceiptConfigRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg, err := st.GetReadReceiptConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg != (ReadReceiptConfig{Response: ReadReceiptAsk}) {
		t.Errorf("an unset config read as %+v, want ask and ActiveSync sending", cfg)
	}

	for _, want := range []ReadReceiptConfig{
		{Response: ReadReceiptAlways},
		{Response: ReadReceiptNever, SuppressActiveSync: true},
		{Response: ReadReceiptAsk, SuppressActiveSync: true},
		{},
	} {
		if err := st.SetReadReceiptConfig(want); err != nil {
			t.Fatalf("set %+v: %v", want, err)
		}
		got, err := st.GetReadReceiptConfig()
		if err != nil {
			t.Fatalf("get after %+v: %v", want, err)
		}
		if got != want {
			t.Errorf("config = %+v, want %+v", got, want)
		}
	}

	if err := st.SetReadReceiptConfig(ReadReceiptConfig{Response: 3}); err == nil {
		t.Error("a response outside the three choices was stored")
	}
}
