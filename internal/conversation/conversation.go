// Package conversation derives the stable conversation identity that groups a
// mail thread, shared by every protocol that exposes a conversation view
// (ActiveSync ConversationId/Index, EWS FindConversation/GetConversationItems).
// Keeping one derivation means the same message always resolves to the same
// conversation id regardless of the protocol a client uses.
package conversation

import (
	// #nosec G501 -- MD5 derives a fixed 16-byte conversation id, the GUID width; it authenticates nothing
	"crypto/md5"
	stdmime "mime"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// ID derives a stable 16-byte conversation id for a message from its wire form.
// The MD5 is an id derivation (a fixed 16-byte digest, the GUID width), not a
// security hash.
func ID(raw []byte) []byte {
	h := mime.ParseStructure(raw).Header()
	return IDFromParts(h.Get("References"), h.Get("In-Reply-To"), h.Get("Message-Id"), h.Get("Subject"))
}

// IDFromParts derives the same id as ID from a message's threading fields once
// they are already in hand, so a caller holding the stored PR_INTERNET_REFERENCES,
// PR_IN_REPLY_TO_ID and PR_INTERNET_MESSAGE_ID properties can group a mailbox
// without reading every message's wire form back. Both entry points run the one
// derivation below, so a message resolves to the same id whichever of them the
// protocol reaches it through.
func IDFromParts(references, inReplyTo, messageID, subject string) []byte {
	// #nosec G401 -- MD5 derives a fixed 16-byte conversation id, the GUID width; it authenticates nothing
	sum := md5.Sum([]byte(rootKey(references, inReplyTo, messageID, subject)))
	return sum[:]
}

// Index builds the 22-byte PidTagConversationIndex header (MS-OXOMSG 2.2.1.3): a
// reserved 0x01 byte, the delivery time as the high 40 bits of a FILETIME written
// big-endian, and the 16-byte conversation GUID. Reply-chain child blocks are not
// reconstructed (hermEX stores no chain), so a root index is emitted per message.
func Index(convID []byte, when time.Time) []byte {
	ft := mapi.UnixToNTTime(when) >> 24 // the high 40 bits of the FILETIME
	idx := make([]byte, 0, 22)
	idx = append(idx, 0x01)
	// #nosec G115 -- a deliberate little-endian split of the wider value
	idx = append(idx, byte(ft>>32), byte(ft>>24), byte(ft>>16), byte(ft>>8), byte(ft))
	idx = append(idx, convID...)
	return idx
}

// rootKey resolves a message's thread root: the first (oldest) Message-ID in
// References, else In-Reply-To, else the message's own Message-ID, else the
// normalized subject. Every reply in a thread carries the same root in References,
// so the whole thread resolves to one key.
//
// The subject is RFC 2047 decoded before it is normalized. A caller reading the
// wire form holds the header verbatim while a caller reading the stored subject
// property holds it already decoded, and both must reach the same key for the same
// message.
func rootKey(references, inReplyTo, messageID, subject string) string {
	if refs := strings.Fields(references); len(refs) > 0 {
		return refs[0]
	}
	if irt := strings.TrimSpace(inReplyTo); irt != "" {
		return irt
	}
	if mid := strings.TrimSpace(messageID); mid != "" {
		return mid
	}
	return "subject:" + NormalizeSubject(decodeHeaderWord(subject))
}

// decodeHeaderWord decodes RFC 2047 encoded-words in a header value, leaving
// plain text unchanged.
func decodeHeaderWord(s string) string {
	if d, err := (&stdmime.WordDecoder{}).DecodeHeader(s); err == nil {
		return d
	}
	return s
}

// NormalizeSubject lowercases a subject and strips leading reply and forward
// prefixes so a thread without References still groups by its base topic.
func NormalizeSubject(s string) string {
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
