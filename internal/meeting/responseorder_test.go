package meeting

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// TestOrganizerIsToldOnlyAfterTheMeetingIsStored pins the order of Respond's two
// outward effects. The organizer's reply must go out only once the appointment is on
// the attendee's calendar: sending first leaves the organizer holding an acceptance
// for a meeting the attendee does not have, and that is not recoverable by retrying,
// because the second attempt sends a second reply.
//
// The attendee store here is a public store, which has no Calendar folder, so the
// filing step fails for real rather than through a seam.
func TestOrganizerIsToldOnlyAfterTheMeetingIsStored(t *testing.T) {
	organizerDir := t.TempDir()
	if st, err := objectstore.Open(organizerDir); err != nil {
		t.Fatal(err)
	} else {
		st.Close()
	}
	accs := directory.StaticAccounts{"bob@hermex.test": {MailboxPath: organizerDir}}

	st, err := objectstore.OpenPublic(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fid, err := st.CreateFolder(nil, "Requests")
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := st.CreateMessage(fid, &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: requestClass},
		{Tag: mapi.PrSubject, Value: "Sync"},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "bob@hermex.test"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Respond(st, accs, nil, "alice@hermex.test", reqID, ResponseAccepted, true); err == nil {
		t.Fatal("filing the appointment was expected to fail on a store with no Calendar folder")
	}
	if n := organizerInbox(t, organizerDir); n != 0 {
		t.Errorf("the organizer holds %d responses for a meeting that was never stored", n)
	}
}

// organizerInbox counts the messages in the organizer's Inbox.
func organizerInbox(t *testing.T, dir string) int {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}
