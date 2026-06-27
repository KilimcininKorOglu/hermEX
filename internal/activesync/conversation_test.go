package activesync

import (
	"bytes"
	"testing"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

const (
	convMsgRoot  = "Message-ID: <root@hermex.test>\r\nSubject: Project plan\r\n\r\nthe plan\r\n"
	convMsgReply = "Message-ID: <reply@hermex.test>\r\nIn-Reply-To: <root@hermex.test>\r\n" +
		"References: <root@hermex.test>\r\nSubject: Re: Project plan\r\n\r\nack\r\n"
	convMsgOther = "Message-ID: <other@hermex.test>\r\nSubject: Lunch\r\n\r\nfood\r\n"
)

// TestConversationIDThreadGrouping proves a reply carrying the thread root in its
// References shares the root message's ConversationId, while an unrelated message
// gets a different one (the grouping that makes a conversation view cohere).
func TestConversationIDThreadGrouping(t *testing.T) {
	root := conversationID([]byte(convMsgRoot))
	reply := conversationID([]byte(convMsgReply))
	other := conversationID([]byte(convMsgOther))

	if len(root) != 16 {
		t.Fatalf("ConversationId length = %d, want 16", len(root))
	}
	if !bytes.Equal(root, reply) {
		t.Errorf("reply ConversationId %x != root %x; a thread must share one id", reply, root)
	}
	if bytes.Equal(root, other) {
		t.Error("an unrelated message must not share the thread's ConversationId")
	}
}

// TestConversationIDSubjectFallback proves a message with no threading headers
// groups by its normalized subject, so "Re: X" and "X" still share a conversation
// when neither carries References.
func TestConversationIDSubjectFallback(t *testing.T) {
	base := conversationID([]byte("Subject: Weekly sync\r\n\r\nbody\r\n"))
	reFwd := conversationID([]byte("Subject: Re: Fwd: Weekly sync\r\n\r\nbody\r\n"))
	if !bytes.Equal(base, reFwd) {
		t.Errorf("subject-fallback ids differ: %x vs %x (Re:/Fwd: must be stripped)", base, reFwd)
	}
}

// TestConversationIndexFormat proves the index is the 22-byte MS-OXOMSG header: the
// reserved 0x01 byte, 5 time bytes, then the 16-byte ConversationId GUID.
func TestConversationIndexFormat(t *testing.T) {
	convID := conversationID([]byte(convMsgRoot))
	when := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	idx := conversationIndex(convID, when)

	if len(idx) != 22 {
		t.Fatalf("ConversationIndex length = %d, want 22", len(idx))
	}
	if idx[0] != 0x01 {
		t.Errorf("reserved byte = %#x, want 0x01", idx[0])
	}
	if !bytes.Equal(idx[6:], convID) {
		t.Errorf("trailing GUID %x != ConversationId %x", idx[6:], convID)
	}
}

// TestEmailAppDataCarriesConversation proves the email render emits both the
// ConversationId (16 bytes) and the ConversationIndex (22 bytes) a client groups by.
func TestEmailAppDataCarriesConversation(t *testing.T) {
	m := objectstore.MessageInfo{UID: 1, Subject: "Project plan", Sender: "a@hermex.test",
		InternalDate: time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)}
	data := emailAppData([]byte(convMsgRoot), m, "1", "1", bodyPref{})

	cid := data.Child(wbxml.EM2ConversationId)
	if cid == nil || len(cid.Opaque) != 16 {
		t.Fatalf("ConversationId node missing or not 16 bytes: %#v", cid)
	}
	cidx := data.Child(wbxml.EM2ConversationIndex)
	if cidx == nil || len(cidx.Opaque) != 22 {
		t.Fatalf("ConversationIndex node missing or not 22 bytes: %#v", cidx)
	}
	if !bytes.Equal(cidx.Opaque[6:], cid.Opaque) {
		t.Error("the rendered index GUID must equal the rendered ConversationId")
	}
}
