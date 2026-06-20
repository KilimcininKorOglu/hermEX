package objectstore

import (
	"slices"
	"testing"
)

// TestSendAsRoundTrip proves the send-as list persists through the store-root
// property, reads back as none on a fresh mailbox, and clears on an empty set. The
// MTA authorizes an authenticated cross-mailbox sender against this list, so a silent
// drop would let a revoked grantee keep forging the From or block a granted one.
func TestSendAsRoundTrip(t *testing.T) {
	s := openTestStore(t)

	got, err := s.GetSendAs()
	if err != nil {
		t.Fatalf("GetSendAs on a fresh store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("fresh store send-as = %v, want none", got)
	}

	want := []string{"bob@hermex.test", "carol@hermex.test"}
	if err := s.SetSendAs(want); err != nil {
		t.Fatalf("SetSendAs: %v", err)
	}
	got, err = s.GetSendAs()
	if err != nil {
		t.Fatalf("GetSendAs after set: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("send-as = %v, want %v", got, want)
	}

	if err := s.SetSendAs(nil); err != nil {
		t.Fatalf("SetSendAs(nil): %v", err)
	}
	got, err = s.GetSendAs()
	if err != nil {
		t.Fatalf("GetSendAs after clear: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cleared send-as = %v, want none", got)
	}
}
