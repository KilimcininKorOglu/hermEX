package admin

import (
	"hermex/internal/activesync"
	"hermex/internal/objectstore"
)

// MailboxStore reads and writes a mailbox's store-root settings addressed by
// maildir. It backs the admin tabs whose data lives in the per-mailbox object
// store rather than the directory (out-of-office, ActiveSync devices). The
// concrete mailboxStore satisfies it; the server tests substitute a fake. The
// settings are exchanged as the canonical objectstore/activesync types so the
// admin UI shares one representation with webmail and the protocol handlers —
// never a second encoding of the same format.
type MailboxStore interface {
	GetOOFSettings(maildir string) (objectstore.OOFSettings, error)
	SetOOFSettings(maildir string, cfg objectstore.OOFSettings) error
	ListDevices(maildir string) ([]activesync.DeviceInfo, error)
	ResyncDevice(maildir, deviceID string) error
	DeleteDevice(maildir, deviceID string) error
	WipeDevice(maildir, deviceID string, accountOnly bool) error
	CancelDeviceWipe(maildir, deviceID string) error
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

func (mailboxStore) ListDevices(maildir string) ([]activesync.DeviceInfo, error) {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return activesync.Devices(st)
}

func (mailboxStore) ResyncDevice(maildir, deviceID string) error {
	return withStore(maildir, func(st *objectstore.Store) error { return activesync.ResyncDevice(st, deviceID) })
}

func (mailboxStore) DeleteDevice(maildir, deviceID string) error {
	return withStore(maildir, func(st *objectstore.Store) error { return activesync.DeleteDevice(st, deviceID) })
}

func (mailboxStore) WipeDevice(maildir, deviceID string, accountOnly bool) error {
	return withStore(maildir, func(st *objectstore.Store) error { return activesync.RequestWipe(st, deviceID, accountOnly) })
}

func (mailboxStore) CancelDeviceWipe(maildir, deviceID string) error {
	return withStore(maildir, func(st *objectstore.Store) error { return activesync.CancelWipe(st, deviceID) })
}

// withStore opens the object store at maildir, runs fn, and closes it — the
// open/close boilerplate the per-device action methods share.
func withStore(maildir string, fn func(*objectstore.Store) error) error {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(st)
}
