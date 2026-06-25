package nspi

import (
	"testing"

	"hermex/internal/mapi"
)

// TestGalUserPropsRoomCapacity proves the address book advertises a resource
// mailbox's seating capacity as PR_EMS_AB_ROOM_CAPACITY (a PtLong), so Outlook shows
// it when booking, and that an ordinary entry carries no such property.
func TestGalUserPropsRoomCapacity(t *testing.T) {
	room := galUser{smtp: "boardroom@hermex.test", display: "Boardroom", dt: dtMailuser, dispType: 7, capacity: 12}
	v, ok := galUserProps(room).Get(mapi.PrEmsAbRoomCapacity)
	if !ok {
		t.Fatal("room entry is missing PR_EMS_AB_ROOM_CAPACITY")
	}
	if v != int32(12) {
		t.Errorf("PR_EMS_AB_ROOM_CAPACITY = %#v, want int32(12)", v)
	}

	user := galUser{smtp: "alice@hermex.test", display: "Alice", dt: dtMailuser, dispType: 0, capacity: 0}
	if _, ok := galUserProps(user).Get(mapi.PrEmsAbRoomCapacity); ok {
		t.Error("a non-resource entry must not carry PR_EMS_AB_ROOM_CAPACITY")
	}
}
