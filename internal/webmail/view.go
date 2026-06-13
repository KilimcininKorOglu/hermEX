package webmail

import (
	"strings"

	"hermex/internal/mapi"
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
	ID     int64  // fixed folder id, used as the move/copy/CRUD target value
	Name   string // leaf display name
	Path   string // full hierarchical path, e.g. "Archive/2026"
	IsUser bool   // user-created (id >= unassigned-start): rename/delete allowed
	Total  int    // messages in the folder (sidebar badge; populated by the mail handler)
	Unread int    // unread messages in the folder (sidebar badge)
}

// messageView is one row in the message list.
type messageView struct {
	UID       uint32
	Folder    string // the containing folder path, for action links
	From      string
	Subject   string
	Date      string
	Seen      bool
	Flagged   bool
	Deleted   bool
	Draft     bool // unsent draft: the row opens the compose editor, not the reader
	Scheduled bool // a deferred send awaiting release: the row offers a cancel action
	InTrash   bool // in Deleted Items: the row offers Restore instead of Junk
	InJunk    bool // in Junk Email: the row offers Restore instead of Junk
}

// mailPage is the data the mail template renders. Query/Field/Scope back the
// shared search form rendered in the toolbar (empty/zero on the mail page; the
// search page fills them in), so the form's defaults select correctly.
type mailPage struct {
	User     string
	Current  string
	Folders  []folderView
	Messages []messageView
	Query    string
	Field    string
	Scope    string
	// Message-list state (#31). Sort/Dir/Filter are carried on every list link so
	// pagination, sorting, and filtering compose; Page..NextPage drive the pager;
	// Total/Unread are the current folder's counts shown in the toolbar.
	Sort     string
	Dir      string
	Filter   string
	Page     int
	MaxPage  int
	PrevPage int
	NextPage int
	Total    int
	Unread   int
	Columns  []columnHeader // sortable column headers with precomputed link state
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
		views = append(views, folderView{
			ID:     f.ID,
			Name:   f.DisplayName,
			Path:   pathOf(f),
			IsUser: f.ID >= int64(mapi.PrivateFIDUnassignedStart),
		})
	}
	return views
}

// folderParent returns a folder's parent id (nil for a top-level folder), used to
// rename a folder in place without reparenting it.
func folderParent(folders []objectstore.FolderInfo, id int64) (*int64, bool) {
	for _, f := range folders {
		if f.ID == id {
			return f.ParentID, true
		}
	}
	return nil, false
}

// moveTargets returns the folders a message may be moved or copied into: mail
// folders (per isMailFolder) other than the one it is currently in.
func moveTargets(folders []objectstore.FolderInfo, currentID int64) []folderView {
	var out []folderView
	for _, v := range buildFolderViews(folders) {
		if v.ID != currentID && isMailFolder(v.ID) {
			out = append(out, v)
		}
	}
	return out
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
		views = append(views, messageViewFrom(folderID, folder, msgs[i]))
	}
	return views, nil
}

// messageViewFrom builds a single list-row view from the index's denormalized
// envelope projections, so listing a folder needs no per-message wire-form read.
// The date shown is the message's received (internal) date, as the index carries
// it; the sender is the originator's display name from the formatted projection.
// folderID is the containing folder's fixed id, used to decide the Trash/Junk
// row actions from the id (not the mutable display path).
func messageViewFrom(folderID int64, folder string, m objectstore.MessageInfo) messageView {
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
		// The Outbox holds only deferred sends in this server, so an Outbox row is
		// a scheduled message the user can cancel.
		Scheduled: folder == outboxName,
		InTrash:   folderID == int64(mapi.PrivateFIDDeletedItems),
		InJunk:    folderID == int64(mapi.PrivateFIDJunk),
	}
}

// isMailFolder reports whether a folder is a valid message move/copy/junk/restore
// target: a user-created folder (id >= the unassigned-start id) or one of the
// well-known mail folders. Non-mail built-ins (calendar, contacts, tasks, notes,
// journal) and the special Drafts/Outbox are excluded.
func isMailFolder(id int64) bool {
	switch id {
	case int64(mapi.PrivateFIDInbox), int64(mapi.PrivateFIDSentItems),
		int64(mapi.PrivateFIDDeletedItems), int64(mapi.PrivateFIDJunk):
		return true
	}
	return id >= int64(mapi.PrivateFIDUnassignedStart)
}

// senderDisplay reduces a formatted originator ("Name <addr>") to its display
// name for the list, falling back to the bare address when there is no name.
func senderDisplay(sender string) string {
	if i := strings.Index(sender, " <"); i >= 0 {
		return sender[:i]
	}
	return sender
}
