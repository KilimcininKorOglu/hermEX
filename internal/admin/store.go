package admin

import (
	"errors"

	"hermex/internal/activesync"
	"hermex/internal/easpolicy"
	"hermex/internal/objectstore"
)

// MailboxStore reads and writes a mailbox's store-root settings addressed by
// maildir. It backs the admin tabs whose data lives in the per-mailbox object
// store rather than the directory (out-of-office, ActiveSync devices). The
// concrete mailboxStore satisfies it; the server tests substitute a fake. The
// settings are exchanged as the canonical objectstore/activesync types so the
// admin UI shares one representation with webmail and the protocol handlers,
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
	GetSendAs(maildir string) ([]string, error)
	SetSendAs(maildir string, list []string) error
	GetSendOnBehalf(maildir string) ([]string, error)
	SetSendOnBehalf(maildir string, list []string) error
	GetSentCopyConfig(maildir string) (objectstore.SentCopyConfig, error)
	SetSentCopyConfig(maildir string, cfg objectstore.SentCopyConfig) error
	GetMeetingConfig(maildir string) (objectstore.MeetingConfig, error)
	SetMeetingConfig(maildir string, cfg objectstore.MeetingConfig) error
	GetStoreOwners(maildir string) ([]string, error)
	SetStoreOwners(maildir string, list []string) error
	GetSyncPolicy(maildir string) (easpolicy.Policy, error)
	SetSyncPolicy(maildir string, p easpolicy.Policy) error
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

func (mailboxStore) GetOOFSettings(maildir string) (cfg objectstore.OOFSettings, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		cfg, e = st.GetOOFSettings()
		return e
	})
	return cfg, err
}

func (mailboxStore) SetOOFSettings(maildir string, cfg objectstore.OOFSettings) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetOOFSettings(cfg) })
}

func (mailboxStore) ListDevices(maildir string) (devs []activesync.DeviceInfo, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		devs, e = activesync.Devices(st)
		return e
	})
	return devs, err
}

func (mailboxStore) ResyncDevice(maildir, deviceID string) error {
	return withExistingStore(maildir, func(st *objectstore.Store) error { return activesync.ResyncDevice(st, deviceID) })
}

func (mailboxStore) DeleteDevice(maildir, deviceID string) error {
	return withExistingStore(maildir, func(st *objectstore.Store) error { return activesync.DeleteDevice(st, deviceID) })
}

func (mailboxStore) WipeDevice(maildir, deviceID string, accountOnly bool) error {
	return withExistingStore(maildir, func(st *objectstore.Store) error { return activesync.RequestWipe(st, deviceID, accountOnly) })
}

func (mailboxStore) CancelDeviceWipe(maildir, deviceID string) error {
	return withExistingStore(maildir, func(st *objectstore.Store) error { return activesync.CancelWipe(st, deviceID) })
}

func (mailboxStore) GetQuota(maildir string) (q objectstore.QuotaLimits, used int64, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		if q, e = st.GetQuota(); e != nil {
			return e
		}
		used, e = st.MailboxSize()
		return e
	})
	return q, used, err
}

func (mailboxStore) SetQuota(maildir string, q objectstore.QuotaLimits) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetQuota(q) })
}

func (mailboxStore) GetDelegates(maildir string) (list []string, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		list, e = st.GetDelegates()
		return e
	})
	return list, err
}

func (mailboxStore) SetDelegates(maildir string, list []string) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetDelegates(list) })
}

func (mailboxStore) GetSendAs(maildir string) (list []string, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		list, e = st.GetSendAs()
		return e
	})
	return list, err
}

func (mailboxStore) SetSendAs(maildir string, list []string) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetSendAs(list) })
}

func (mailboxStore) GetSendOnBehalf(maildir string) (list []string, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		list, e = st.GetSendOnBehalf()
		return e
	})
	return list, err
}

func (mailboxStore) SetSendOnBehalf(maildir string, list []string) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetSendOnBehalf(list) })
}

func (mailboxStore) GetSentCopyConfig(maildir string) (cfg objectstore.SentCopyConfig, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		cfg, e = st.GetSentCopyConfig()
		return e
	})
	return cfg, err
}

func (mailboxStore) SetSentCopyConfig(maildir string, cfg objectstore.SentCopyConfig) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetSentCopyConfig(cfg) })
}

func (mailboxStore) GetMeetingConfig(maildir string) (cfg objectstore.MeetingConfig, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		cfg, e = st.GetMeetingConfig()
		return e
	})
	return cfg, err
}

func (mailboxStore) SetMeetingConfig(maildir string, cfg objectstore.MeetingConfig) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetMeetingConfig(cfg) })
}

func (mailboxStore) GetStoreOwners(maildir string) (list []string, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		list, e = st.GetStoreOwners()
		return e
	})
	return list, err
}

func (mailboxStore) SetStoreOwners(maildir string, list []string) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetStoreOwners(list) })
}

func (mailboxStore) GetSyncPolicy(maildir string) (p easpolicy.Policy, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		p, e = st.GetSyncPolicy()
		return e
	})
	return p, err
}

func (mailboxStore) SetSyncPolicy(maildir string, p easpolicy.Policy) error {
	return withStore(maildir, func(st *objectstore.Store) error { return st.SetSyncPolicy(p) })
}

func (mailboxStore) ListFolders(maildir string) (list []objectstore.FolderInfo, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		list, e = st.ListFolders()
		return e
	})
	return list, err
}

func (mailboxStore) ListFolderPermissions(maildir string, folderID int64) (list []objectstore.PermissionEntry, err error) {
	err = readStore(maildir, func(st *objectstore.Store) (e error) {
		list, e = st.ListPermissions(folderID)
		return e
	})
	return list, err
}

// SetFolderPermission grants or updates one member's rights on a folder. PermAdd
// upserts the member's row (replace=false leaves every other member, including the
// seeded default/anonymous free-busy rows, untouched). The rights value is a
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

// readStore runs fn against the existing store at maildir. A mailbox that was
// never provisioned holds no settings, so fn does not run and the caller keeps
// its zero values; the store is never created by a read.
func readStore(maildir string, fn func(*objectstore.Store) error) error {
	err := withExistingStore(maildir, fn)
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		return nil
	}
	return err
}

// withExistingStore runs fn against the existing store at maildir and answers
// objectstore.ErrNotProvisioned for a mailbox that was never provisioned. A device
// action uses it: an unprovisioned mailbox has no device to act on.
func withExistingStore(maildir string, fn func(*objectstore.Store) error) error {
	st, err := objectstore.OpenExisting(maildir)
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(st)
}

// withStore opens the object store at maildir, creating it when the mailbox was
// never provisioned, runs fn, and closes it. Only a settings write uses it: an
// operator may configure a new mailbox before its first delivery.
func withStore(maildir string, fn func(*objectstore.Store) error) error {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(st)
}
