package webmail2api

import (
	"testing"
	"time"

	"hermex/internal/objectstore"
)

// TestNormalizeThreadSubject proves reply/forward prefixes are stripped (matching
// the SPA's normalizeSubject) so a reply groups with its original.
func TestNormalizeThreadSubject(t *testing.T) {
	cases := map[string]string{
		"Re: Hello":        "Hello",
		"Fwd: Re: Project": "Project",
		"  RE: re: Deep ":  "Deep",
		"FW: Quick":        "Quick",
		"No prefix":        "No prefix",
	}
	for in, want := range cases {
		if got := normalizeThreadSubject(in); got != want {
			t.Errorf("normalizeThreadSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGroupThreads proves messages bucket by normalized subject, longest thread
// first, with unique first-seen participants and an unread count.
func TestGroupThreads(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(uid uint32, subj, sender string, seen bool) objectstore.MessageInfo {
		var flags int64
		if seen {
			flags = objectstore.FlagSeen
		}
		return objectstore.MessageInfo{UID: uid, InternalDate: base, Subject: subj, Sender: sender, Flags: flags}
	}
	msgs := []objectstore.MessageInfo{
		mk(1, "Hello", "alice@x", true),
		mk(2, "Re: Hello", "bob@x", false),
		mk(3, "Standalone", "carol@x", false),
		mk(4, "Fwd: Hello", "alice@x", false),
	}
	threads := groupThreads("inbox", msgs)
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2", len(threads))
	}
	// Longest conversation first: the "hello" bucket holds 3 messages.
	hello := threads[0]
	if hello.Key != "hello" || len(hello.Messages) != 3 {
		t.Fatalf("first thread = %q with %d msgs, want hello/3", hello.Key, len(hello.Messages))
	}
	if hello.Subject != "Hello" {
		t.Errorf("subject = %q, want Hello", hello.Subject)
	}
	if hello.Unread != 2 {
		t.Errorf("unread = %d, want 2 (msg1 read, msg2+msg4 unread)", hello.Unread)
	}
	// Participants unique and first-seen ordered: alice (msg1), bob (msg2); alice repeats.
	if len(hello.Participants) != 2 || hello.Participants[0] != "alice@x" || hello.Participants[1] != "bob@x" {
		t.Errorf("participants = %v, want [alice@x bob@x]", hello.Participants)
	}
	if threads[1].Key != "standalone" || len(threads[1].Messages) != 1 {
		t.Errorf("second thread = %q/%d, want standalone/1", threads[1].Key, len(threads[1].Messages))
	}
}
