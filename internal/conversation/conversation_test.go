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
	// Bytes 1-5 are the high 40 bits of the FILETIME, big-endian. A FILETIME
	// counts 100 ns ticks since 1601-01-01, 11644473600 s before the Unix epoch.
	ft := uint64(1718200000+11644473600) * 10_000_000
	want := []byte{byte(ft >> 56), byte(ft >> 48), byte(ft >> 40), byte(ft >> 32), byte(ft >> 24)}
	if !bytes.Equal(idx[1:6], want) {
		t.Errorf("index time bytes = %x, want %x", idx[1:6], want)
	}
}

// TestResolvePrefersAWellFormedStoredID proves a stored PidTagConversationId is
// used only when it has the GUID's 16 bytes. Any other length is a damaged or
// foreign value, and the message falls back to the id its threading headers
// derive rather than grouping by bytes that are not a conversation id.
func TestResolvePrefersAWellFormedStoredID(t *testing.T) {
	stored := bytes.Repeat([]byte{0xAB}, 16)
	derived := IDFromParts("", "", "<root@x>", "Weekly sync")
	if got := Resolve(stored, "", "", "<root@x>", "Weekly sync"); !bytes.Equal(got, stored) {
		t.Errorf("Resolve with a 16-byte stored id = %x, want the stored id", got)
	}
	for _, bad := range [][]byte{nil, {}, stored[:15], append(bytes.Clone(stored), 0)} {
		if got := Resolve(bad, "", "", "<root@x>", "Weekly sync"); !bytes.Equal(got, derived) {
			t.Errorf("Resolve with a %d-byte stored id = %x, want the derived id %x", len(bad), got, derived)
		}
	}
}

// TestIDFromPartsMatchesID proves the two entry points agree for every branch of
// the derivation. A protocol grouping from the stored threading properties must
// reach the same id as one grouping from the wire form, or the same message lands
// in two different conversations depending on the client that asks.
func TestIDFromPartsMatchesID(t *testing.T) {
	for _, c := range []struct {
		name       string
		raw        string
		references string
		inReplyTo  string
		messageID  string
		subject    string
	}{
		{
			name:       "references wins",
			raw:        "Message-Id: <reply@x>\r\nIn-Reply-To: <mid@x>\r\nReferences: <root@x> <mid@x>\r\nSubject: Re: Weekly sync\r\n\r\nbody\r\n",
			references: "<root@x> <mid@x>", inReplyTo: "<mid@x>", messageID: "<reply@x>", subject: "Re: Weekly sync",
		},
		{
			name:      "in-reply-to when there are no references",
			raw:       "Message-Id: <reply@x>\r\nIn-Reply-To: <root@x>\r\nSubject: Re: Weekly sync\r\n\r\nbody\r\n",
			inReplyTo: "<root@x>", messageID: "<reply@x>", subject: "Re: Weekly sync",
		},
		{
			name:      "own message id when the message starts a thread",
			raw:       "Message-Id: <root@x>\r\nSubject: Weekly sync\r\n\r\nbody\r\n",
			messageID: "<root@x>", subject: "Weekly sync",
		},
		{
			name:    "subject fallback when the message carries no threading header",
			raw:     "Subject: Weekly sync\r\n\r\nbody\r\n",
			subject: "Weekly sync",
		},
		{
			// The header is RFC 2047 encoded on the wire and decoded in the stored
			// subject property, so the derivation has to decode before it normalizes.
			name:    "subject fallback with an encoded-word subject",
			raw:     "Subject: =?utf-8?q?Haftal=C4=B1k_toplant=C4=B1?=\r\n\r\nbody\r\n",
			subject: "Haftalık toplantı",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			fromRaw := ID([]byte(c.raw))
			fromParts := IDFromParts(c.references, c.inReplyTo, c.messageID, c.subject)
			if !bytes.Equal(fromRaw, fromParts) {
				t.Errorf("ID(raw) = %x, IDFromParts = %x, want equal", fromRaw, fromParts)
			}
		})
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
