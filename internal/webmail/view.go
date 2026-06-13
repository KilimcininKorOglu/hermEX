package webmail

import (
	"strings"

	"hermex/internal/objectstore"
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
	Folder  string // the containing folder path, for action links
	From    string
	Subject string
	Date    string
	Seen    bool
	Flagged bool
	Deleted bool
	Draft   bool // unsent draft: the row opens the compose editor, not the reader
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
func buildFolderViews(folders []objectstore.FolderInfo) []folderView {
	byID := make(map[int64]objectstore.FolderInfo, len(folders))
	for _, f := range folders {
		byID[f.ID] = f
	}
	var pathOf func(f objectstore.FolderInfo) string
	pathOf = func(f objectstore.FolderInfo) string {
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
func resolveFolder(folders []objectstore.FolderInfo, path string) (int64, bool) {
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
func buildMessageViews(st *objectstore.Store, folderID int64, folder string) ([]messageView, error) {
	msgs, err := st.ListMessages(folderID)
	if err != nil {
		return nil, err
	}
	views := make([]messageView, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- { // newest first
		views = append(views, messageViewFrom(folder, msgs[i]))
	}
	return views, nil
}

// messageViewFrom builds a single list-row view from the index's denormalized
// envelope projections, so listing a folder needs no per-message wire-form read.
// The date shown is the message's received (internal) date, as the index carries
// it; the sender is the originator's display name from the formatted projection.
func messageViewFrom(folder string, m objectstore.MessageInfo) messageView {
	subject := m.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	from := senderDisplay(m.Sender)
	if from == "" {
		from = "(unknown sender)"
	}
	return messageView{
		UID:     m.UID,
		Folder:  folder,
		From:    from,
		Subject: subject,
		Date:    m.InternalDate.Format("2006-01-02 15:04"),
		Seen:    m.Flags&objectstore.FlagSeen != 0,
		Flagged: m.Flags&objectstore.FlagFlagged != 0,
		Deleted: m.Flags&objectstore.FlagDeleted != 0,
		Draft:   m.Flags&objectstore.FlagDraft != 0,
	}
}

// senderDisplay reduces a formatted originator ("Name <addr>") to its display
// name for the list, falling back to the bare address when there is no name.
func senderDisplay(sender string) string {
	if i := strings.Index(sender, " <"); i >= 0 {
		return sender[:i]
	}
	return sender
}
