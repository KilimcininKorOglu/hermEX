package ews

import (
	"strings"
	"testing"
)

// copyItemReq is a CopyItem of one item into a distinguished folder.
func copyItemReq(itemID, destDistinguished string) string {
	return strings.ReplaceAll(moveItemReq(itemID, destDistinguished, ""), "MoveItem", "CopyItem")
}

// TestMoveAndCopyCalendarItems proves MoveItem and CopyItem act on a calendar
// item, which has no IMAP uid: a copy adds a second series to the calendar, a
// move takes the item out of it under the same id, and an occurrence is refused.
// Both answered ErrorItemNotFound before, because they copied by IMAP uid.
func TestMoveAndCopyCalendarItems(t *testing.T) {
	ts, paths := availabilityServer(t)
	master := createdMeeting(t, ts)
	alice := paths["alice@hermex.test"]

	_, out := soapPost(t, ts, copyItemReq(master, "calendar"), true)
	wantContains(t, "the copy", out, `ResponseClass="Success"`)
	wantContains(t, "the copy's element", out, "<CalendarItem")
	if n := octoberBusy(t, alice); n != 6 {
		t.Errorf("the calendar holds %d busy blocks after the copy, want 6", n)
	}

	_, out = soapPost(t, ts, moveItemReq(master, "deleteditems", ""), true)
	wantContains(t, "the move", out, `ResponseClass="Success"`)
	if n := octoberBusy(t, alice); n != 3 {
		t.Errorf("the calendar holds %d busy blocks after the move, want the copy's 3", n)
	}

	occurrence := wrapRequest(`<MoveItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<ToFolderId><t:DistinguishedFolderId Id="deleteditems"/></ToFolderId>` +
		`<ItemIds><t:OccurrenceItemId RecurringMasterId="` + master + `" InstanceIndex="1"/></ItemIds></MoveItem>`)
	_, out = soapPost(t, ts, occurrence, true)
	wantContains(t, "an occurrence", out, "ErrorCalendarCannotMoveOrCopyOccurrence")
}
