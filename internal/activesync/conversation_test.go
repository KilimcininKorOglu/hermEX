package activesync

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"hermex/internal/mapi"
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
	// MS-OXOMSG 2.2.1.3: the time field is the high 40 bits of the FILETIME
	// (100ns ticks since 1601-01-01 UTC), most significant byte first.
	ft := uint64(when.Unix()+11644473600) * 10_000_000
	wantTime := []byte{byte(ft >> 56), byte(ft >> 48), byte(ft >> 40), byte(ft >> 32), byte(ft >> 24)}
	if !bytes.Equal(idx[1:6], wantTime) {
		t.Errorf("time bytes = %x, want the high 40 bits of the FILETIME %x", idx[1:6], wantTime)
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
	data := emailAppData(mailRender{}, []byte(convMsgRoot), m, "1", "1")

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

// TestEmailAppDataCarriesTheStoredConversation proves a device receives the
// conversation id and index the store holds, the ones import took from the
// message's Thread-Index and MAPI serves, not ones derived again from the headers.
func TestEmailAppDataCarriesTheStoredConversation(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	guid := bytes.Repeat([]byte{0xcd}, 16)
	idx := append([]byte{0x01, 0xd0, 0x11, 0x22, 0x33, 0x44}, guid...)
	raw := []byte("Message-ID: <ti@hermex.test>\r\nSubject: Plan\r\nThread-Index: " +
		base64.StdEncoding.EncodeToString(idx) + "\r\n\r\nbody\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Unix(1718200000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	data := emailAppData(mailRender{st: st}, raw, info, "1", "1")
	if cid := data.Child(wbxml.EM2ConversationId); cid == nil || !bytes.Equal(cid.Opaque, guid) {
		t.Errorf("ConversationId = %#v, want the stored %x", cid, guid)
	}
	if cidx := data.Child(wbxml.EM2ConversationIndex); cidx == nil || !bytes.Equal(cidx.Opaque, idx) {
		t.Errorf("ConversationIndex = %#v, want the stored %x", cidx, idx)
	}
}

// TestEmailAppDataIgnoresAForgedThreadIndex: Thread-Index is a sender-written
// header. One that is not base64, or decodes shorter than the 22-byte header
// MS-OXOMSG 2.2.1.3 defines, cannot carry a conversation, so the device still
// gets a well-formed 16-byte id and 22-byte index.
func TestEmailAppDataIgnoresAForgedThreadIndex(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	mustNoErr(t, "open the store", err)
	defer mustClose(t, st)
	for _, ti := range []string{"!!not base64!!", base64.StdEncoding.EncodeToString([]byte{0x01, 0x02, 0x03})} {
		raw := []byte("Message-ID: <forged@hermex.test>\r\nSubject: Plan\r\nThread-Index: " + ti + "\r\n\r\nbody\r\n")
		info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Unix(1718200000, 0), 0)
		mustNoErr(t, "append the message", err)
		data := emailAppData(mailRender{st: st}, raw, info, "1", "1")
		if cid := data.Child(wbxml.EM2ConversationId); cid == nil || len(cid.Opaque) != 16 {
			t.Errorf("Thread-Index %q: ConversationId = %#v, want 16 bytes", ti, cid)
		}
		if cidx := data.Child(wbxml.EM2ConversationIndex); cidx == nil || len(cidx.Opaque) != 22 {
			t.Errorf("Thread-Index %q: ConversationIndex = %#v, want 22 bytes", ti, cidx)
		}
	}
}
