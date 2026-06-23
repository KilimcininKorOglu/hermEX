package webmail

import (
	"slices"
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
	Depth  int    // nesting level (0 = top-level), for sidebar indentation
	IsUser bool   // user-created (id >= unassigned-start): rename/delete allowed
	Total  int    // messages in the folder (sidebar badge; populated by the mail handler)
	Unread int    // unread messages in the folder (sidebar badge)
}

// messageView is one row in the message list.
type messageView struct {
	UID       uint32
	Folder    string // the containing folder path, for action links
	Mbox      string // shared mailbox address when the row is in one: read/action links carry &mbox; empty for the own mailbox
	ReadOnly  bool   // hide write controls (a shared folder the caller may see but not modify); false for the own mailbox
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
	// Icon-column flags, filled per visible row from the message object (not from
	// the index projection), so they live outside messageViewFrom.
	HasAttachment  bool           // a real, non-inline attachment is present (paperclip)
	ImportanceHigh bool           // PR_IMPORTANCE high
	ImportanceLow  bool           // PR_IMPORTANCE low
	FlagColor      int32          // follow-up flag color 1-6 (PR_FOLLOWUP_ICON), 0 = none; a legacy \Flagged with no color shows red
	FlagComplete   bool           // follow-up flag marked complete (shows a check instead of a flag)
	Categories     []categoryView // assigned categories (PidNameKeywords) with their master-list colors
}

// categoryView is one assigned category resolved to its display color.
type categoryView struct {
	Name  string
	Color string
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
	Density  string // message-list row density: "compact" | "extended"
	Page     int
	MaxPage  int
	PrevPage int
	NextPage int
	Total    int
	Unread   int
	Columns  []columnHeader // sortable column headers with precomputed link state
	// Categories is the mailbox's master category list, offered in the
	// multi-select bulk-categorize control (#33).
	Categories []category
	// PreviewPane is the reading-pane location: "none" | "right" | "bottom" (#34).
	PreviewPane string
	// Conversation switches the list to the threaded (conversation) view (#39).
	// When true the template renders Threads instead of the flat Messages, and
	// the sortable column headers and bulk toolbar (both flat-view features) are
	// not shown.
	Conversation bool
	Threads      []threadView
	// PublicFolders are the public folders the user may see, shown as a labeled
	// sidebar section (empty when none are visible or public folders are off).
	PublicFolders []publicFolderLink
	// SharedMailboxes are the shared mailboxes the user may open, each with its
	// visible folders, shown as a labeled sidebar section (empty when none).
	SharedMailboxes []sharedMailboxGroup
	// MoveTargets are the folders of the store whose messages are listed (the own
	// mailbox, or the open shared mailbox), offered as the bulk move destinations.
	// Kept distinct from Folders, which is always the own sidebar tree.
	MoveTargets []folderView
	// Mbox is the shared mailbox address when the open folder belongs to one, else
	// empty. When set, every read link carries &mbox={{.Mbox}} so navigation stays
	// in the shared store. The template escapes the value, so it is held raw (never
	// pre-escaped) here.
	Mbox string
	// ReadOnly hides the folder-level write controls (the bulk toolbar and the
	// select-all checkbox) for a shared folder the caller may see but not modify;
	// false for the own mailbox, so its list is unaffected.
	ReadOnly bool
	// CanSendAs reports that the caller may compose as the open shared mailbox
	// (owner or delegate), so the Compose button opens a send-as composer; false
	// for the own mailbox and for a shared mailbox the caller cannot send as.
	CanSendAs bool
}

// threadView is one conversation thread rendered as a collapsible group: a
// summary (root subject, member count, latest-message date, unread state) over
// the member rows, which are ordinary messageViews so they keep their per-row
// actions. Open requests the group start expanded (threads with unread mail).
type threadView struct {
	Subject   string
	Count     int
	Date      string // the latest member's date
	AnyUnread bool
	Open      bool
	Messages  []messageView // members, oldest-first
}

// buildFolderViews computes each folder's hierarchical path and nesting depth
// from the parent links, emitted in tree order: each folder is immediately
// followed by its descendants, with siblings kept in the store's own order. This
// lets the sidebar indent children under their parent (Depth) instead of listing
// every folder flat, while preserving the well-known folders' canonical order
// (Inbox first, etc.) rather than re-sorting alphabetically.
func buildFolderViews(folders []objectstore.FolderInfo) []folderView {
	byID := make(map[int64]objectstore.FolderInfo, len(folders))
	for _, f := range folders {
		byID[f.ID] = f
	}
	// children[parentID] holds a parent's folders in store order; a folder is a
	// root when it has no parent, or an orphan whose parent is absent from this set
	// (surfaced as a root so it is never dropped).
	children := make(map[int64][]objectstore.FolderInfo)
	var roots []objectstore.FolderInfo
	for _, f := range folders {
		if f.ParentID == nil {
			roots = append(roots, f)
			continue
		}
		if _, ok := byID[*f.ParentID]; ok {
			children[*f.ParentID] = append(children[*f.ParentID], f)
		} else {
			roots = append(roots, f)
		}
	}
	views := make([]folderView, 0, len(folders))
	var walk func(f objectstore.FolderInfo, parentPath string, depth int)
	walk = func(f objectstore.FolderInfo, parentPath string, depth int) {
		name := f.DisplayName
		if depth == 0 && strings.EqualFold(name, inboxName) {
			name = inboxName
		}
		path := name
		if parentPath != "" {
			path = parentPath + hierarchySep + f.DisplayName
		}
		views = append(views, folderView{
			ID:     f.ID,
			Name:   f.DisplayName,
			Path:   path,
			Depth:  depth,
			IsUser: f.ID >= int64(mapi.PrivateFIDUnassignedStart),
		})
		for _, c := range children[f.ID] {
			walk(c, path, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, "", 0)
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
	for _, v := range buildFolderViews(folders) {
		if v.Path == path || (strings.EqualFold(path, inboxName) && strings.EqualFold(v.Path, inboxName)) {
			return v.ID, true
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
	for _, m := range slices.Backward(msgs) { // newest first
		views = append(views, messageViewFrom(folderID, folder, m))
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
	if before, _, ok := strings.Cut(sender, " <"); ok {
		return before
	}
	return sender
}
