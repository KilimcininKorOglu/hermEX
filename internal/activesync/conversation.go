package activesync

import (
	"crypto/md5"
	"net/textproto"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// conversationID derives a stable 16-byte conversation id for a message by hashing
// its thread root: the first (oldest) Message-ID in References, else In-Reply-To,
// else the message's own Message-ID, else the normalized subject. Every reply in a
// thread carries the same root in References, so the whole thread resolves to one id
// (the grouping an EAS conversation view needs, MS-ASEMAIL ConversationId). The MD5
// is an id derivation (a fixed 16-byte digest, the GUID width), not a security hash.
func conversationID(raw []byte) []byte {
	root := conversationRootKey(mime.ParseStructure(raw).Header())
	sum := md5.Sum([]byte(root))
	return sum[:]
}

// conversationRootKey resolves a message's thread root from its threading headers,
// falling back to the normalized subject when none are present.
func conversationRootKey(h textproto.MIMEHeader) string {
	if refs := strings.Fields(h.Get("References")); len(refs) > 0 {
		return refs[0]
	}
	if irt := strings.TrimSpace(h.Get("In-Reply-To")); irt != "" {
		return irt
	}
	if mid := strings.TrimSpace(h.Get("Message-Id")); mid != "" {
		return mid
	}
	return "subject:" + normalizeConversationSubject(h.Get("Subject"))
}

// conversationIndex builds the 22-byte PidTagConversationIndex header (MS-OXOMSG
// 2.2.1.3): a reserved 0x01 byte, the delivery time as the high 40 bits of a
// FILETIME written big-endian, and the 16-byte conversation GUID. Reply-chain child
// blocks are not reconstructed (hermEX stores no chain), so a root index is emitted
// per message.
func conversationIndex(convID []byte, when time.Time) []byte {
	ft := mapi.UnixToNTTime(when) >> 24 // the high 40 bits of the FILETIME
	idx := make([]byte, 0, 22)
	idx = append(idx, 0x01)
	idx = append(idx, byte(ft>>32), byte(ft>>24), byte(ft>>16), byte(ft>>8), byte(ft))
	idx = append(idx, convID...)
	return idx
}

// normalizeConversationSubject lowercases the subject and strips leading reply and
// forward prefixes so a thread without References still groups by its base topic.
func normalizeConversationSubject(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for {
		switch {
		case strings.HasPrefix(s, "re:"):
			s = strings.TrimSpace(s[3:])
		case strings.HasPrefix(s, "fwd:"):
			s = strings.TrimSpace(s[4:])
		case strings.HasPrefix(s, "fw:"):
			s = strings.TrimSpace(s[3:])
		default:
			return s
		}
	}
}
