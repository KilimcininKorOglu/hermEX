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
	topic, _ := msg.Props.Get(mapi.PrConversationTopic)
	id, _ := msg.Props.Get(mapi.PrConversationId)
	s, _ := topic.(string)
	b, _ := id.([]byte)
	return s, b
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
	gotIdx, _ := msg.Props.Get(mapi.PrConversationIndex)
	gotID, _ := msg.Props.Get(mapi.PrConversationId)
	if !bytes.Equal(gotIdx.([]byte), idx) || !bytes.Equal(gotID.([]byte), idx[6:22]) {
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
	rootIdx, _ := root.Props.Get(mapi.PrConversationIndex)
	rootID, _ := root.Props.Get(mapi.PrConversationId)
	if b := rootIdx.([]byte); len(b) != 22 || b[0] != 0x01 || !bytes.Equal(b[6:], rootID.([]byte)) {
		t.Errorf("root index %x, id %x; want a 22-byte header carrying the id", rootIdx, rootID)
	}
}
