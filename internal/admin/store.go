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
	GetQuota(maildir string) (objectstore.QuotaLimits, int64, error)
	SetQuota(maildir string, q objectstore.QuotaLimits) error
	GetDelegates(maildir string) ([]string, error)
	SetDelegates(maildir string, list []string) error
	ListFolders(maildir string) ([]objectstore.FolderInfo, error)
	ListFolderPermissions(maildir string, folderID int64) ([]objectstore.PermissionEntry, error)
	SetFolderPermission(maildir string, folderID int64, username string, rights uint32) error
	RemoveFolderPermission(maildir string, folderID, memberID int64) error
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

func (mailboxStore) GetQuota(maildir string) (objectstore.QuotaLimits, int64, error) {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return objectstore.QuotaLimits{}, 0, err
	}
	defer st.Close()
	q, err := st.GetQuota()
	if err != nil {
		return objectstore.QuotaLimits{}, 0, err
	}
	used, err := st.MailboxSize()
	if err != nil {
		return q, 0, err
	}
	return q, used, nil
}

func (mailboxStore) SetQuota(maildir string, q objectstore.QuotaLimits) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetQuota(q) })
}

func (mailboxStore) GetDelegates(maildir string) ([]string, error) {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.GetDelegates()
}

func (mailboxStore) SetDelegates(maildir string, list []string) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetDelegates(list) })
}

func (mailboxStore) ListFolders(maildir string) ([]objectstore.FolderInfo, error) {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.ListFolders()
}

func (mailboxStore) ListFolderPermissions(maildir string, folderID int64) ([]objectstore.PermissionEntry, error) {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.ListPermissions(folderID)
}

// SetFolderPermission grants or updates one member's rights on a folder. PermAdd
// upserts the member's row (replace=false leaves every other member — including the
// seeded default/anonymous free-busy rows — untouched). The rights value is a
// canonical level (mapi.Rights*), so it is persisted as the protocol layer would.
func (mailboxStore) SetFolderPermission(maildir string, folderID int64, username string, rights uint32) error {
	return withStore(maildir, func(st *objectstore.Store) error {
		return st.ModifyPermissions(folderID, false, []objectstore.PermissionChange{
			{Op: objectstore.PermAdd, Username: username, Rights: rights},
		})
	})
}

// RemoveFolderPermission drops one member's row from a folder, addressed by its wire
// member id (0=default, -1=anonymous, else the row id).
func (mailboxStore) RemoveFolderPermission(maildir string, folderID, memberID int64) error {
	return withStore(maildir, func(st *objectstore.Store) error {
		return st.ModifyPermissions(folderID, false, []objectstore.PermissionChange{
			{Op: objectstore.PermRemove, MemberID: memberID},
		})
	})
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
