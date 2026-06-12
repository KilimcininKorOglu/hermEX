package imap

import "hermex/internal/store"

// selectedMailbox is a session's view of the currently selected folder: an
// ordered message snapshot plus the identity needed to report UIDVALIDITY and
// UIDNEXT. The snapshot is ordered by ascending UID, so a message's IMAP
// sequence number is its index + 1.
type selectedMailbox struct {
	id          int64
	path        string
	uidValidity uint32
	uidNext     uint32
	msgs        []store.MessageInfo
}

// loadMailbox builds a fresh selected-mailbox view for a folder.
func loadMailbox(st *store.Store, id int64, path string) (*selectedMailbox, error) {
	msgs, err := st.ListMessages(id)
	if err != nil {
		return nil, err
	}
	uidv, err := st.UIDValidity(id)
	if err != nil {
		return nil, err
	}
	uidn, err := st.UIDNext(id)
	if err != nil {
		return nil, err
	}
	return &selectedMailbox{id: id, path: path, uidValidity: uidv, uidNext: uidn, msgs: msgs}, nil
}

// maxSeq returns the highest message sequence number (the message count).
func (m *selectedMailbox) maxSeq() uint32 { return uint32(len(m.msgs)) }

// maxUID returns the highest UID in the snapshot, or 0 when empty.
func (m *selectedMailbox) maxUID() uint32 {
	if len(m.msgs) == 0 {
		return 0
	}
	return m.msgs[len(m.msgs)-1].UID
}

// seqOf returns the 1-based sequence number of a message by UID.
func (m *selectedMailbox) seqOf(uid uint32) (uint32, bool) {
	for i := range m.msgs {
		if m.msgs[i].UID == uid {
			return uint32(i + 1), true
		}
	}
	return 0, false
}

// firstUnseen returns the sequence number of the first message without the
// \Seen flag, or 0 when every message has been seen.
func (m *selectedMailbox) firstUnseen() uint32 {
	for i := range m.msgs {
		if m.msgs[i].Flags&store.FlagSeen == 0 {
			return uint32(i + 1)
		}
	}
	return 0
}

// countUnseen returns how many messages lack the \Seen flag.
func (m *selectedMailbox) countUnseen() int {
	n := 0
	for i := range m.msgs {
		if m.msgs[i].Flags&store.FlagSeen == 0 {
			n++
		}
	}
	return n
}
