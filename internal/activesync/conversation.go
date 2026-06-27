package activesync

import (
	"time"

	"hermex/internal/conversation"
)

// conversationID derives the stable 16-byte conversation id grouping a message's
// thread (MS-ASEMAIL ConversationId), shared with the other protocols' conversation
// views through internal/conversation so the same message always resolves to the
// same id.
func conversationID(raw []byte) []byte { return conversation.ID(raw) }

// conversationIndex builds the 22-byte PidTagConversationIndex header for a message.
func conversationIndex(convID []byte, when time.Time) []byte {
	return conversation.Index(convID, when)
}
