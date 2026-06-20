package admin

import "hermex/internal/objectstore"

// MailboxStore reads and writes a mailbox's store-root settings addressed by
// maildir. It backs the admin tabs whose data lives in the per-mailbox object
// store rather than the directory (out-of-office). The concrete mailboxStore
// satisfies it; the server tests substitute a fake. The settings are exchanged as
// the canonical objectstore types so the admin UI shares one representation with
// webmail and the delivery path — never a second encoding of the same format.
type MailboxStore interface {
	GetOOFSettings(maildir string) (objectstore.OOFSettings, error)
	SetOOFSettings(maildir string, cfg objectstore.OOFSettings) error
}

// mailboxStore is the production MailboxStore: it opens the object store at the
// given maildir for each call and closes it before returning, matching how the
// mail daemons access per-mailbox stores (SQLite WAL handles cross-process
// concurrency). It holds no state.
type mailboxStore struct{}

func (mailboxStore) GetOOFSettings(maildir string) (objectstore.OOFSettings, error) {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return objectstore.OOFSettings{}, err
	}
	defer st.Close()
	return st.GetOOFSettings()
}

func (mailboxStore) SetOOFSettings(maildir string, cfg objectstore.OOFSettings) error {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.SetOOFSettings(cfg)
}
