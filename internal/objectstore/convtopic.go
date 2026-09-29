package objectstore

import (
	"strings"

	"hermex/internal/mapi"
)

// topicANSI is the PtString8 form of PidTagConversationTopic, which a MAPI client
// may write instead of the Unicode one.
var topicANSI = mapi.PrConversationTopic.WithType(mapi.PtString8)

// rectifyTopic prepares the conversation topic a message write stores. An empty
// topic is no topic, so it is dropped rather than stored: a client that clears it
// (an empty EWS extended property, a MAPI SetProperties) would otherwise put the
// message in an unnamed conversation. On a create a message left without a topic
// takes the normalized subject, as a delivered copy of it does on import, so a
// message saved over MAPI or EWS joins the same topic. The caller's slice is never
// modified.
func rectifyTopic(props mapi.PropertyValues, create bool) mapi.PropertyValues {
	out := make(mapi.PropertyValues, 0, len(props)+1)
	hasTopic := false
	for _, p := range props {
		if p.Tag == mapi.PrConversationTopic || p.Tag == topicANSI {
			if strings.TrimSpace(stringOf(p.Value)) == "" {
				continue
			}
			hasTopic = true
		}
		out = append(out, p)
	}
	if create && !hasTopic {
		if s := normalizedSubject(props); s != "" {
			out = append(out, mapi.TaggedPropVal{Tag: mapi.PrConversationTopic, Value: s})
		}
	}
	return out
}

// normalizedSubject is the subject without its prefix: the stored normalized
// subject, else the subject with the stored prefix cut off.
func normalizedSubject(props mapi.PropertyValues) string {
	if v, ok := props.GetAnyCharset(mapi.PrNormalizedSubject); ok && stringOf(v) != "" {
		return stringOf(v)
	}
	subject, _ := props.GetAnyCharset(mapi.PrSubject)
	prefix, _ := props.GetAnyCharset(mapi.PrSubjectPrefix)
	return strings.TrimPrefix(stringOf(subject), stringOf(prefix))
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
