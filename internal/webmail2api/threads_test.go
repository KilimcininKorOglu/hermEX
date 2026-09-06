package webmail2api

import (
	"testing"
	"time"

	"hermex/internal/objectstore"
)

// TestNormalizeThreadSubject proves reply/forward prefixes are stripped so a reply
// groups with its original. This is the only place the rule lives: the SPA renders
// the threads the endpoint returns and derives nothing from the subject.
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
	wantEq(t, "first thread key", hello.Key, "hello")
	wantEq(t, "first thread message count", len(hello.Messages), 3)
	wantEq(t, "first thread subject", hello.Subject, "Hello")
	wantEq(t, "first thread unread (msg1 read, msg2+msg4 unread)", hello.Unread, 2)
	// Participants unique and first-seen ordered: alice (msg1), bob (msg2); alice repeats.
	if len(hello.Participants) != 2 {
		t.Fatalf("participants = %v, want [alice@x bob@x]", hello.Participants)
	}
	wantEq(t, "first participant", hello.Participants[0], "alice@x")
	wantEq(t, "second participant", hello.Participants[1], "bob@x")
	wantEq(t, "second thread key", threads[1].Key, "standalone")
	wantEq(t, "second thread message count", len(threads[1].Messages), 1)
}
