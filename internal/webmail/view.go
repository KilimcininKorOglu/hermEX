package webmail

import (
	"strings"

	"hermex/internal/mime"
	"hermex/internal/store"
)

// hierarchySep and inboxName mirror the IMAP folder model so webmail folder
// paths line up with what other clients see.
const (
	hierarchySep = "/"
	inboxName    = "INBOX"
)

// folderView is one folder in the sidebar.
type folderView struct {
	Name string // leaf display name
	Path string // full hierarchical path, e.g. "Archive/2026"
}

// messageView is one row in the message list.
type messageView struct {
	UID     uint32
	From    string
	Subject string
	Date    string
	Seen    bool
	Flagged bool
	Deleted bool
}

// mailPage is the data the mail template renders.
type mailPage struct {
	User     string
	Current  string
	Folders  []folderView
	Messages []messageView
}

// buildFolderViews computes each folder's hierarchical path from the parent
// links, ordered as returned by the store.
func buildFolderViews(folders []store.FolderInfo) []folderView {
	byID := make(map[int64]store.FolderInfo, len(folders))
	for _, f := range folders {
		byID[f.ID] = f
	}
	var pathOf func(f store.FolderInfo) string
	pathOf = func(f store.FolderInfo) string {
		if f.ParentID == nil {
			if strings.EqualFold(f.DisplayName, inboxName) {
				return inboxName
			}
			return f.DisplayName
		}
		parent, ok := byID[*f.ParentID]
		if !ok {
			return f.DisplayName
		}
		return pathOf(parent) + hierarchySep + f.DisplayName
	}
	views := make([]folderView, 0, len(folders))
	for _, f := range folders {
		views = append(views, folderView{Name: f.DisplayName, Path: pathOf(f)})
	}
	return views
}

// resolveFolder finds a folder id by its hierarchical path (INBOX is
// case-insensitive), reporting ok=false when no such folder exists.
func resolveFolder(folders []store.FolderInfo, path string) (int64, bool) {
	views := buildFolderViews(folders)
	for i, v := range views {
		if v.Path == path || (strings.EqualFold(path, inboxName) && strings.EqualFold(v.Path, inboxName)) {
			return folders[i].ID, true
		}
	}
	return 0, false
}

// buildMessageViews loads each message's envelope to populate the list, newest
// first.
func buildMessageViews(st *store.Store, folderID int64) ([]messageView, error) {
	msgs, err := st.ListMessages(folderID)
	if err != nil {
		return nil, err
	}
	views := make([]messageView, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- { // newest first
		m := msgs[i]
		v := messageView{
			UID:     m.UID,
			Date:    m.InternalDate.Format("2006-01-02 15:04"),
			Seen:    m.Flags&store.FlagSeen != 0,
			Flagged: m.Flags&store.FlagFlagged != 0,
			Deleted: m.Flags&store.FlagDeleted != 0,
			From:    "(unknown sender)",
			Subject: "(no subject)",
		}
		if raw, err := st.GetMessageRaw(folderID, m.UID); err == nil {
			if env, err := mime.ParseEnvelope(raw); err == nil {
				v.From = formatSender(env.From)
				if env.Subject != "" {
					v.Subject = env.Subject
				}
				if !env.Date.IsZero() {
					v.Date = env.Date.Format("2006-01-02 15:04")
				}
			}
		}
		views = append(views, v)
	}
	return views, nil
}

// formatSender renders the first address of a From list for display.
func formatSender(addrs []mime.Address) string {
	if len(addrs) == 0 {
		return "(unknown sender)"
	}
	a := addrs[0]
	if a.Name != "" {
		return a.Name
	}
	return a.Mailbox + "@" + a.Host
}
