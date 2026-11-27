package oxcmail

import (
	"bytes"
	"encoding/base64"
	"testing"

	"hermex/internal/mapi"
)

// importConv imports a message with the given extra header lines and returns its
// conversation topic and id.
func importConv(t *testing.T, headers string) (string, []byte) {
	t.Helper()
	raw := "From: bob@hermex.test\r\nTo: alice@hermex.test\r\n" + headers +
		"Content-Type: text/plain\r\n\r\nbody\r\n"
	msg, err := Import([]byte(raw), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return propString(msg.Props, mapi.PrConversationTopic), mustBytes(t, msg.Props, mapi.PrConversationId)
}

// mustBytes reads a binary property every imported message carries, and stops
// the test when it is missing or of another type.
func mustBytes(t *testing.T, p mapi.PropertyValues, tag mapi.PropTag) []byte {
	t.Helper()
	b, ok := bytesProp(p, tag)
	if !ok {
		t.Fatalf("property %#x is missing or not binary", uint32(tag))
	}
	return b
}

// TestImportDerivesTheConversation proves a message without a Thread-Topic takes
// its conversation topic from the subject, and that a reply and a second delivery
// of the same message resolve to the same conversation id rather than a new one.
func TestImportDerivesTheConversation(t *testing.T) {
	topic, first := importConv(t, "Subject: Budget\r\nMessage-ID: <root@x>\r\n")
	if topic != "Budget" || len(first) != 16 {
		t.Fatalf("topic %q, id %x; want the subject and a 16-byte id", topic, first)
	}
	_, again := importConv(t, "Subject: Budget\r\nMessage-ID: <root@x>\r\n")
	replyTopic, reply := importConv(t, "Subject: RE: Budget\r\nMessage-ID: <r1@x>\r\nIn-Reply-To: <root@x>\r\nReferences: <root@x>\r\n")
	if !bytes.Equal(first, again) || !bytes.Equal(first, reply) || replyTopic != "Budget" {
		t.Errorf("ids %x %x %x, reply topic %q; want one conversation", first, again, reply, replyTopic)
	}
	if topic, _ := importConv(t, "Subject: RE: Budget\r\nThread-Topic: Plan\r\n"); topic != "Plan" {
		t.Errorf("topic %q, want the Thread-Topic header kept", topic)
	}
	// A Thread-Topic that decodes to nothing names no topic, so the subject does.
	for _, h := range []string{"Thread-Topic: =?utf-8?Q??=\r\n", "Thread-Topic:  \r\n"} {
		if topic, _ := importConv(t, "Subject: RE: Budget\r\n"+h); topic != "Budget" {
			t.Errorf("%q: topic %q, want the normalized subject", h, topic)
		}
	}
}

// TestImportReadsTheThreadIndex proves a Thread-Index header is kept as the
// conversation index and names the conversation, a message without one gets a
// root index carrying its conversation id, and export writes the index back.
func TestImportReadsTheThreadIndex(t *testing.T) {
	idx := append([]byte{0x01, 0xd0, 0x11, 0x22, 0x33, 0x44}, bytes.Repeat([]byte{0xab}, 16)...)
	idx = append(idx, 0x10, 0x20, 0x30, 0x40, 0x50)
	enc := base64.StdEncoding.EncodeToString(idx)
	msg, err := Import([]byte("From: bob@hermex.test\r\nSubject: RE: Plan\r\nThread-Index: "+enc+
		"\r\nContent-Type: text/plain\r\n\r\nbody\r\n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	gotIdx := mustBytes(t, msg.Props, mapi.PrConversationIndex)
	gotID := mustBytes(t, msg.Props, mapi.PrConversationId)
	if !bytes.Equal(gotIdx, idx) || !bytes.Equal(gotID, idx[6:22]) {
		t.Errorf("index %x, id %x; want the header's index and its GUID", gotIdx, gotID)
	}
	raw, err := Export(msg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("Thread-Index: "+enc+"\r\n")) {
		t.Errorf("export lacks the Thread-Index:\n%s", raw)
	}

	root, err := Import([]byte("From: bob@hermex.test\r\nSubject: Plan\r\nMessage-ID: <p@x>\r\n"+
		"Content-Type: text/plain\r\n\r\nbody\r\n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	rootIdx := mustBytes(t, root.Props, mapi.PrConversationIndex)
	rootID := mustBytes(t, root.Props, mapi.PrConversationId)
	if len(rootIdx) != 22 || rootIdx[0] != 0x01 || !bytes.Equal(rootIdx[6:], rootID) {
		t.Errorf("root index %x, id %x; want a 22-byte header carrying the id", rootIdx, rootID)
	}
}

// TestImportIgnoresAMalformedThreadIndex feeds Thread-Index values a sender can
// write that are no conversation index: [MS-OXOMSG] 2.2.1.3 makes one a 22-byte
// header followed by 5-byte child blocks. Such a value must not become the
// conversation id; the message gets a root index of its own instead.
func TestImportIgnoresAMalformedThreadIndex(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, 21))
	ragged := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, 25))
	for _, v := range []string{"not base64 !!", short, ragged} {
		msg, err := Import([]byte("From: bob@hermex.test\r\nSubject: Plan\r\nThread-Index: "+v+
			"\r\nContent-Type: text/plain\r\n\r\nbody\r\n"), Options{})
		mustNoErr(t, err, "import with a malformed Thread-Index")
		idx := mustBytes(t, msg.Props, mapi.PrConversationIndex)
		id := mustBytes(t, msg.Props, mapi.PrConversationId)
		if len(idx) != 22 || !bytes.Equal(idx[6:], id) {
			t.Errorf("%q: index %x, id %x; want a fresh 22-byte root index carrying the id", v, idx, id)
		}
		if bytes.Equal(id, bytes.Repeat([]byte{0xab}, 16)) {
			t.Errorf("%q: the malformed header named the conversation", v)
		}
	}
}
