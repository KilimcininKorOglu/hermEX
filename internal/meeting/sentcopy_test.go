package meeting

import (
	"bytes"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// localMeeting opens an attendee mailbox holding a request from a local organizer
// and returns it with the request's id and the directory that holds both.
func localMeeting(t *testing.T) (*objectstore.Store, int64, directory.StaticAccounts) {
	t.Helper()
	organizerDir := t.TempDir()
	org, err := objectstore.Open(organizerDir)
	if err != nil {
		t.Fatal(err)
	}
	org.Close()
	attendeeDir := t.TempDir()
	st, err := objectstore.Open(attendeeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reqID, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: requestClass},
		{Tag: mapi.PrSubject, Value: "Sync"},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "bob@hermex.test"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	accs := directory.StaticAccounts{
		"bob@hermex.test":   {MailboxPath: organizerDir},
		"alice@hermex.test": {MailboxPath: attendeeDir},
	}
	return st, reqID, accs
}

// TestSentResponseIsKept proves the response the server sends the organizer comes
// back to the caller as sent, so it is kept among what the attendee sent, as every
// other send is. A response that is only recorded sends nothing and keeps nothing.
func TestSentResponseIsKept(t *testing.T) {
	st, reqID, accs := localMeeting(t)
	var kept [][]byte
	reply := Reply{Send: true, SentCopy: func(raw []byte) { kept = append(kept, raw) }}

	if _, err := RespondWith(st, accs, nil, "alice@hermex.test", reqID, ResponseAccepted, reply); err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatalf("the caller was handed %d copies of the sent response, want 1", len(kept))
	}
	for _, want := range []string{"Subject: Accepted: Sync", "METHOD:REPLY"} {
		if !bytes.Contains(kept[0], []byte(want)) {
			t.Errorf("the kept copy lacks %q:\n%s", want, kept[0])
		}
	}

	reply.Send = false
	if _, err := RespondWith(st, accs, nil, "alice@hermex.test", reqID, ResponseTentative, reply); err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Errorf("a response that was not sent handed the caller a copy")
	}
}

// TestSentResponseLeavesTheOnlyCopyToTheMailbox proves a response a delegate sends
// on behalf of a mailbox that keeps the only copy of such mail is not kept a second
// time by the delegate.
func TestSentResponseLeavesTheOnlyCopyToTheMailbox(t *testing.T) {
	st, reqID, accs := localMeeting(t)
	delegateDir := t.TempDir()
	delegate, err := objectstore.Open(delegateDir)
	if err != nil {
		t.Fatal(err)
	}
	delegate.Close()
	accs["erin@hermex.test"] = directory.Account{MailboxPath: delegateDir}
	if err := st.SetSentCopyConfig(objectstore.SentCopyConfig{ForSendOnBehalf: true, Exclusive: true}); err != nil {
		t.Fatal(err)
	}
	kept := 0
	reply := Reply{Send: true, SentCopy: func([]byte) { kept++ }}

	if _, err := RespondOnBehalfWith(st, accs, nil, "alice@hermex.test", "erin@hermex.test", reqID, ResponseAccepted, reply); err != nil {
		t.Fatal(err)
	}
	if kept != 0 {
		t.Errorf("the delegate kept %d copies of a response the mailbox keeps the only copy of", kept)
	}
	sent, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Errorf("the mailbox's Sent Items holds %d responses, want its one copy", len(sent))
	}
}
