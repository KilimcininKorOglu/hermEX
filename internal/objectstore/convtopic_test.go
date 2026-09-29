package objectstore

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// storedTopic reads a message's conversation topic, "" when it has none.
func storedTopic(t *testing.T, s *Store, mid int64) string {
	t.Helper()
	got, err := s.GetMessageProperties(mid, mapi.PrConversationTopic)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := asMap(got)[mapi.PrConversationTopic].(string)
	return v
}

// TestEmptyTopicIsNoTopic proves the store treats an empty conversation topic like
// an absent one on every write path: a message created over MAPI or EWS without a
// topic, or with an empty one, takes its normalized subject, and a later write of
// an empty topic leaves the stored one in place instead of clearing it.
func TestEmptyTopicIsNoTopic(t *testing.T) {
	s := openSeededStore(t)
	for _, topic := range []mapi.PropertyValues{nil, {{Tag: mapi.PrConversationTopic, Value: ""}}, {{Tag: topicANSI, Value: " "}}} {
		props := append(mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
			{Tag: mapi.PrSubjectPrefix, Value: "RE: "},
			{Tag: mapi.PrSubject, Value: "RE: Budget"},
		}, topic...)
		mid, err := s.CreateMessage(mapi.PrivateFIDOutbox, &oxcmail.Message{Props: props})
		if err != nil {
			t.Fatal(err)
		}
		if got := storedTopic(t, s, mid); got != "Budget" {
			t.Errorf("create with %v: topic %q, want the normalized subject", topic, got)
		}
		if err := s.SetMessageProperties(mid, mapi.PropertyValues{{Tag: mapi.PrConversationTopic, Value: ""}}); err != nil {
			t.Fatal(err)
		}
		if got := storedTopic(t, s, mid); got != "Budget" {
			t.Errorf("an empty topic write left %q, want the stored topic kept", got)
		}
	}

	mid, err := s.CreateMessage(mapi.PrivateFIDOutbox, &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Budget"},
		{Tag: mapi.PrConversationTopic, Value: "Plan"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := storedTopic(t, s, mid); got != "Plan" {
		t.Errorf("topic %q, want the client's own topic kept", got)
	}
}
