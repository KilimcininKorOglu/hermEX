package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestDeferredSendTimeRoundTrip checks that PrDeferredSendTime — the property a
// scheduled (send-later) message carries in the Outbox — round-trips through the
// message property layer as a PT_SYSTIME value: an absolute time set as an
// NT-time reads back as the same instant. The send-later worker relies on this
// to decide when a deferred message is due.
func TestDeferredSendTimeRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	mid, err := s.CreateMessage(mapi.PrivateFIDOutbox, &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
			{Tag: mapi.PrSubject, Value: "scheduled"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A whole-second instant round-trips exactly (NT-time has 100 ns resolution).
	want := time.Unix(1893456000, 0) // 2030-01-01T00:00:00Z, a fixed future time
	if err := s.SetMessageProperties(mid, mapi.PropertyValues{
		{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(want)},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetMessageProperties(mid, mapi.PrDeferredSendTime)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := asMap(got)[mapi.PrDeferredSendTime]
	if !ok {
		t.Fatalf("PrDeferredSendTime is absent after the round trip: %v", got)
	}
	nt, ok := raw.(uint64)
	if !ok {
		t.Fatalf("PrDeferredSendTime stored as %T, want uint64 NT-time", raw)
	}
	if back := mapi.NTTimeToUnix(nt); !back.Equal(want) {
		t.Errorf("deferred send time round-trip = %s, want %s", back, want)
	}
}
