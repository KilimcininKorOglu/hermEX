package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestConversationThreading checks the batch read returns each message's stored
// RFC 5322 threading headers verbatim (the full References chain, not a
// truncation) and its stored conversation id.
func TestConversationThreading(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	when := time.Unix(1700000000, 0)

	root, err := s.AppendMessage(inbox, []byte("From: a@b.test\r\nMessage-ID: <a@x>\r\nSubject: root\r\n\r\nbody\r\n"), when, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A reply two levels deep: its References chain holds both ancestors.
	reply, err := s.AppendMessage(inbox, []byte("From: c@b.test\r\nMessage-ID: <c@x>\r\nIn-Reply-To: <b@x>\r\nReferences: <a@x> <b@x>\r\nSubject: re\r\n\r\nbody\r\n"), when, 0)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.AppendMessage(inbox, []byte("From: d@b.test\r\nSubject: standalone\r\n\r\nbody\r\n"), when, 0)
	if err != nil {
		t.Fatal(err)
	}

	th, err := s.ConversationThreading([]int64{root.ID, reply.ID, plain.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := th[root.ID].MessageID; got != "<a@x>" {
		t.Errorf("root Message-ID = %q, want <a@x>", got)
	}
	if got := th[reply.ID].References; got != "<a@x> <b@x>" {
		t.Errorf("reply References = %q, want the full two-id chain", got)
	}
	if got := th[reply.ID].InReplyTo; got != "<b@x>" {
		t.Errorf("reply In-Reply-To = %q, want <b@x>", got)
	}
	checkConversationIDs(t, th[root.ID], th[plain.ID])
}

// checkConversationIDs asserts both messages carry the conversation id import
// derived, and the one without threading headers carries no header.
func checkConversationIDs(t *testing.T, root, plain ThreadHeaders) {
	t.Helper()
	if plain.MessageID != "" || plain.References != "" || plain.InReplyTo != "" || len(plain.ConversationID) != 16 {
		t.Errorf("standalone message = %+v, want only its conversation id", plain)
	}
	if len(root.ConversationID) != 16 {
		t.Errorf("root carries no conversation id: %+v", root)
	}
}

// TestConversationThreadingEmpty is the empty-input guard: no ids yields an
// empty map and no query.
func TestConversationThreadingEmpty(t *testing.T) {
	s := openTestStore(t)
	th, err := s.ConversationThreading(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(th) != 0 {
		t.Errorf("empty input returned %d entries, want 0", len(th))
	}
}
