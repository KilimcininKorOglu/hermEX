package conversation

import (
	"bytes"
	"testing"
	"time"
)

// TestIDThreadGrouping proves a reply that references the root resolves to the
// root's conversation id, and an unrelated message does not.
func TestIDThreadGrouping(t *testing.T) {
	root := ID([]byte("Message-Id: <root@x>\r\nSubject: Weekly sync\r\n\r\nbody\r\n"))
	reply := ID([]byte("Message-Id: <reply@x>\r\nReferences: <root@x>\r\nSubject: Re: Weekly sync\r\n\r\nbody\r\n"))
	other := ID([]byte("Message-Id: <other@x>\r\nSubject: Different\r\n\r\nbody\r\n"))

	if len(root) != 16 {
		t.Fatalf("id length = %d, want 16", len(root))
	}
	if !bytes.Equal(root, reply) {
		t.Error("a reply referencing the root must share its conversation id")
	}
	if bytes.Equal(root, other) {
		t.Error("an unrelated message must not share the conversation id")
	}
}

// TestIDSubjectFallback proves a thread without threading headers groups by its
// normalized base subject (reply/forward prefixes stripped).
func TestIDSubjectFallback(t *testing.T) {
	base := ID([]byte("Subject: Weekly sync\r\n\r\nbody\r\n"))
	reFwd := ID([]byte("Subject: Re: Fwd: Weekly sync\r\n\r\nbody\r\n"))
	if !bytes.Equal(base, reFwd) {
		t.Error("subjects differing only by reply/forward prefixes must group together")
	}
}

// TestIndexFormat proves the conversation index is the 22-byte PidTagConversationIndex
// header: a 0x01 lead byte then the 16-byte conversation guid as its tail.
func TestIndexFormat(t *testing.T) {
	convID := ID([]byte("Message-Id: <root@x>\r\nSubject: Weekly sync\r\n\r\nbody\r\n"))
	idx := Index(convID, time.Unix(1718200000, 0))
	if len(idx) != 22 {
		t.Fatalf("index length = %d, want 22", len(idx))
	}
	if idx[0] != 0x01 {
		t.Errorf("index lead byte = %#x, want 0x01", idx[0])
	}
	if !bytes.Equal(idx[6:], convID) {
		t.Error("index tail must be the 16-byte conversation id")
	}
}

// TestNormalizeSubject proves the reply and forward prefixes are stripped and the
// subject is lowercased.
func TestNormalizeSubject(t *testing.T) {
	for _, in := range []string{"Re: Fwd: Hello", "FW: Hello", "Hello", "re: re: Hello"} {
		if got := NormalizeSubject(in); got != "hello" {
			t.Errorf("NormalizeSubject(%q) = %q, want hello", in, got)
		}
	}
}
