package imap

import (
	"strings"

	"hermex/internal/objectstore"
)

// hierarchySep is the IMAP hierarchy delimiter announced in LIST responses and
// used to join folder display names into mailbox paths.
const hierarchySep = "/"

// inboxName is the reserved, case-insensitive mailbox name. The store's inbox
// is a top-level folder whose display name matches "INBOX" case-insensitively;
// its path is normalized to the reserved name here.
const inboxName = "INBOX"

// folderNode is one folder plus its derived IMAP mailbox path.
type folderNode struct {
	info        objectstore.FolderInfo
	path        string
	hasChildren bool
}

// folderTree is a snapshot of a mailbox's folders, indexed for name<->id
// resolution and LIST enumeration. Paths are derived from the parent links.
type folderTree struct {
	nodes  []*folderNode
	byID   map[int64]*folderNode
	byPath map[string]*folderNode // exact path; INBOX handled case-insensitively
}

// loadFolderTree reads every folder from the store and computes mailbox paths.
func loadFolderTree(st *objectstore.Store) (*folderTree, error) {
	folders, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	t := &folderTree{
		byID:   make(map[int64]*folderNode, len(folders)),
		byPath: make(map[string]*folderNode, len(folders)),
	}
	for i := range folders {
		n := &folderNode{info: folders[i]}
		t.nodes = append(t.nodes, n)
		t.byID[n.info.ID] = n
	}
	for _, n := range t.nodes {
		n.path = t.pathOf(n)
		t.byPath[n.path] = n
		if n.info.ParentID != nil {
			if parent, ok := t.byID[*n.info.ParentID]; ok {
				parent.hasChildren = true
			}
		}
	}
	return t, nil
}

// pathOf derives a node's full mailbox path from its ancestry. The root folder
// named INBOX (any case) is normalized to the reserved name "INBOX".
func (t *folderTree) pathOf(n *folderNode) string {
	if n.info.ParentID == nil {
		if strings.EqualFold(n.info.DisplayName, inboxName) {
			return inboxName
		}
		return n.info.DisplayName
	}
	parent, ok := t.byID[*n.info.ParentID]
	if !ok {
		return n.info.DisplayName // orphaned; treat as root-level
	}
	return t.pathOf(parent) + hierarchySep + n.info.DisplayName
}

// resolve looks up a mailbox by its IMAP name. The lookup is case-sensitive
// except for INBOX, which RFC 3501 §5.1 mandates be case-insensitive.
func (t *folderTree) resolve(name string) (*folderNode, bool) {
	if strings.EqualFold(name, inboxName) {
		n, ok := t.byPath[inboxName]
		return n, ok
	}
	n, ok := t.byPath[name]
	return n, ok
}

// imapMatch reports whether an IMAP mailbox name matches a LIST pattern. '*'
// matches across hierarchy separators; '%' matches within a single level only
// (RFC 3501 §6.3.8).
func imapMatch(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	switch pattern[0] {
	case '*':
		for i := 0; i <= len(name); i++ {
			if imapMatch(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	case '%':
		for i := 0; i <= len(name); i++ {
			if imapMatch(pattern[1:], name[i:]) {
				return true
			}
			if i < len(name) && name[i] == hierarchySep[0] {
				break // '%' cannot cross a hierarchy separator
			}
		}
		return false
	default:
		if name == "" || name[0] != pattern[0] {
			return false
		}
		return imapMatch(pattern[1:], name[1:])
	}
}
