package objectstore

import (
	"errors"
	"testing"

	"hermex/internal/mapi"
)

// TestMessageChangeNumberMovesOnEachEdit reads the stored version of a message:
// it advances when the message is edited and names no message for an unknown id.
func TestMessageChangeNumberMovesOnEachEdit(t *testing.T) {
	s := openSeededStore(t)
	info := appendForEdit(t, s, "before")
	before, err := s.MessageChangeNumber(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before != msgCN(t, s, info.ID) {
		t.Errorf("change number = %d, stored %d", before, msgCN(t, s, info.ID))
	}
	if err := s.ModifyMessageProperties(info.ID, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "after"}}); err != nil {
		t.Fatal(err)
	}
	after, err := s.MessageChangeNumber(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Errorf("change number after an edit = %d, want above %d", after, before)
	}
	if _, err := s.MessageChangeNumber(1 << 40); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
}
