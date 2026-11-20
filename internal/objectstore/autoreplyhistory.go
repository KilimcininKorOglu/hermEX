package objectstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"hermex/internal/mapi"
)

// AutoReplyListOOF is the history list of the out-of-office reply.
const AutoReplyListOOF = "oof"

// The history is bounded so a stream of distinct senders (bounce addresses that
// pass every suppression test, say) cannot grow it without end. A full list
// forgets its oldest sender, which then gets one more reply: never the other way
// round, where a full list would block a first reply.
const (
	maxAutoReplySenders = 1000
	maxAutoReplyLists   = 32
)

// replyList is one automatic reply's answered senders, oldest first.
type replyList struct {
	Key     string   `json:"k"`
	Senders []string `json:"s"`
}

// RuleAutoReplyList is the history list of one vacation rule's reply. The text is
// part of the key, so a rule whose reply changed starts a new list: its senders
// have not seen the new reply.
func RuleAutoReplyList(ruleID int64, message string) string {
	sum := sha256.Sum256([]byte(message))
	return fmt.Sprintf("rule:%d:%s", ruleID, hex.EncodeToString(sum[:8]))
}

// ClaimAutoReply records that list is about to answer sender, and reports whether
// this is the first answer, the only case in which the caller sends. The check and
// the record are one write transaction, so two deliveries from the same sender
// racing on different handles send one reply between them.
func (s *Store) ClaimAutoReply(list, sender string) (bool, error) {
	sender = strings.ToLower(strings.TrimSpace(sender))
	if sender == "" {
		return false, nil
	}
	first := false
	err := s.updateStoreString(mapi.PrAutoReplyHistory, func(cur string) (string, bool, error) {
		lists, err := decodeReplyHistory(cur)
		if err != nil {
			return "", false, err
		}
		lists, first = claimSender(lists, list, sender)
		if !first {
			return "", false, nil
		}
		b, err := json.Marshal(lists)
		return string(b), err == nil, err
	})
	return first, err
}

func decodeReplyHistory(raw string) ([]replyList, error) {
	if raw == "" {
		return nil, nil
	}
	var lists []replyList
	if err := json.Unmarshal([]byte(raw), &lists); err != nil {
		return nil, fmt.Errorf("objectstore: decode the automatic reply history: %w", err)
	}
	return lists, nil
}

// claimSender adds sender to the list named key and reports whether it was absent.
func claimSender(lists []replyList, key, sender string) ([]replyList, bool) {
	i := slices.IndexFunc(lists, func(l replyList) bool { return l.Key == key })
	if i < 0 {
		lists = append(lists, replyList{Key: key})
		if len(lists) > maxAutoReplyLists {
			lists = lists[len(lists)-maxAutoReplyLists:]
		}
		i = len(lists) - 1
	}
	if slices.Contains(lists[i].Senders, sender) {
		return lists, false
	}
	l := &lists[i]
	l.Senders = append(l.Senders, sender)
	if len(l.Senders) > maxAutoReplySenders {
		l.Senders = l.Senders[len(l.Senders)-maxAutoReplySenders:]
	}
	return lists, true
}

// txDropAutoReplyList forgets every sender list key answered, inside tx, so the
// next message from each draws a reply again. A history that no longer decodes is
// removed whole: it is the record dropping a list resets anyway.
func (s *Store) txDropAutoReplyList(tx *sql.Tx, key string) error {
	cur, err := s.txStoreString(tx, mapi.PrAutoReplyHistory)
	if err != nil || cur == "" {
		return err
	}
	tag := int64(uint32(mapi.PrAutoReplyHistory))
	lists, err := decodeReplyHistory(cur)
	if err != nil {
		_, err = tx.Exec(`DELETE FROM store_properties WHERE proptag = ?`, tag)
		return err
	}
	kept := slices.DeleteFunc(lists, func(l replyList) bool { return l.Key == key })
	b, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	enc, err := s.storedPropval(mapi.PrAutoReplyHistory, string(b))
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE store_properties SET propval = ? WHERE proptag = ?`, enc, tag)
	return err
}
