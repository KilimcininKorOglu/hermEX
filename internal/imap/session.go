package imap

import "hermex/internal/objectstore"

// selectedMailbox is a session's view of the currently selected folder: an
// ordered message snapshot plus the identity needed to report UIDVALIDITY and
// UIDNEXT. The snapshot is ordered by ascending UID, so a message's IMAP
// sequence number is its index + 1.
type selectedMailbox struct {
	id          int64
	path        string
	uidValidity uint32
	uidNext     uint32
	msgs        []objectstore.MessageInfo
}

// loadMailbox builds a fresh selected-mailbox view for a folder.
func loadMailbox(st *objectstore.Store, id int64, path string) (*selectedMailbox, error) {
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
// #nosec G115 -- a Go slice length; the buffer it measures is orders of magnitude below the field
func (m *selectedMailbox) maxSeq() uint32 { return uint32(len(m.msgs)) }

// maxUID returns the highest UID in the snapshot, or 0 when empty.
func (m *selectedMailbox) maxUID() uint32 {
	if len(m.msgs) == 0 {
		return 0
	}
	return m.msgs[len(m.msgs)-1].UID
}

// firstUnseen returns the sequence number of the first message without the
// \Seen flag, or 0 when every message has been seen.
func (m *selectedMailbox) firstUnseen() uint32 {
	for i := range m.msgs {
		if m.msgs[i].Flags&objectstore.FlagSeen == 0 {
			return uint32(i + 1)
		}
	}
	return 0
}
