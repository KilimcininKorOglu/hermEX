package objectstore

import "testing"

// TestReadReceiptConfigRoundTrip proves an unconfigured mailbox asks in webmail and
// sends from ActiveSync, that every setting survives the store, and that a response
// outside the three choices is refused rather than stored.
func TestReadReceiptConfigRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir())
	mustNoErr(t, "open", err)
	defer mustClose(t, st)

	wantReadReceiptConfig(t, st, ReadReceiptConfig{Response: ReadReceiptAsk})

	for _, want := range []ReadReceiptConfig{
		{Response: ReadReceiptAlways},
		{Response: ReadReceiptNever, SuppressActiveSync: true},
		{Response: ReadReceiptAsk, SuppressActiveSync: true},
		{},
	} {
		mustNoErr(t, "set the config", st.SetReadReceiptConfig(want))
		wantReadReceiptConfig(t, st, want)
	}

	kept := ReadReceiptConfig{Response: ReadReceiptNever, SuppressActiveSync: true}
	mustNoErr(t, "set the kept config", st.SetReadReceiptConfig(kept))
	if err := st.SetReadReceiptConfig(ReadReceiptConfig{Response: 3}); err == nil {
		t.Error("a response outside the three choices was stored")
	}
	wantReadReceiptConfig(t, st, kept)
}

// wantReadReceiptConfig reads the stored config and compares it with want.
func wantReadReceiptConfig(t *testing.T, st *Store, want ReadReceiptConfig) {
	t.Helper()
	got, err := st.GetReadReceiptConfig()
	mustNoErr(t, "get the config", err)
	if got != want {
		t.Errorf("config = %+v, want %+v", got, want)
	}
}
