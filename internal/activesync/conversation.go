package activesync

import (
	"bytes"
	"time"

	"hermex/internal/conversation"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// conversationID derives the stable 16-byte conversation id grouping a message's
// thread (MS-ASEMAIL ConversationId) from its wire form, for a message that carries
// no stored id.
func conversationID(raw []byte) []byte { return conversation.ID(raw) }

// conversationIndex builds the 22-byte PidTagConversationIndex header for a message.
func conversationIndex(convID []byte, when time.Time) []byte {
	return conversation.Index(convID, when)
}

// messageConversation returns the conversation id and index a device is sent for a
// stored message. The stored PidTagConversationId and PidTagConversationIndex win,
// so a device groups the message exactly as Outlook and EWS do; a message stored
// without them gets the id derived from its threading headers and a root index.
func messageConversation(st *objectstore.Store, messageID int64, raw []byte, when time.Time) (id, idx []byte) {
	storedID, storedIdx := storedConversation(st, messageID)
	id = storedID
	if len(id) != 16 {
		id = conversationID(raw)
	}
	if len(storedIdx) >= 22 && bytes.Equal(storedIdx[6:22], id) {
		return id, storedIdx
	}
	return id, conversationIndex(id, when)
}

// storedConversation reads a message's stored conversation id and index. A read
// failure is recorded and answered with neither, so the message falls back to the
// derived id rather than going out with none.
func storedConversation(st *objectstore.Store, messageID int64) (id, idx []byte) {
	if st == nil {
		return nil, nil
	}
	pv, err := st.GetMessageProperties(messageID, mapi.PrConversationId, mapi.PrConversationIndex)
	if err != nil {
		st.LogSwallowedError("activesync.conversation_read", err)
		return nil, nil
	}
	if v, ok := pv.Get(mapi.PrConversationId); ok {
		id, _ = v.([]byte)
	}
	if v, ok := pv.Get(mapi.PrConversationIndex); ok {
		idx, _ = v.([]byte)
	}
	return id, idx
}
