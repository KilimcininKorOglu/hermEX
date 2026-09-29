package oxcmail

import (
	"bytes"
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
