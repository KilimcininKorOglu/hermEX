package oxews

import (
	"encoding/xml"
	"strings"
)

// typesNS is the EWS types namespace every folder element is in.
const typesNS = "http://schemas.microsoft.com/exchange/services/2006/types"

// Folder is the EWS <t:Folder> element (the BaseFolderType subset v1 emits), or
// <t:SearchFolder> for a search folder: the element name is how a client tells
// the two apart. The element declares the types namespace once on itself; the
// child elements inherit it as the default namespace, so they need no per-field
// namespace boilerplate. BuildFolder sets the name.
type Folder struct {
	XMLName          xml.Name
	FolderID         FolderID  `xml:"FolderId"`
	ParentFolderID   *FolderID `xml:"ParentFolderId,omitempty"`
	FolderClass      *string   `xml:"FolderClass,omitempty"`
	DisplayName      string    `xml:"DisplayName"`
	TotalCount       int       `xml:"TotalCount"`
	ChildFolderCount int       `xml:"ChildFolderCount"`
	// PermissionSet precedes UnreadCount per the FolderType schema sequence; it is
	// emitted only when GetFolder requested folder:PermissionSet (otherwise nil).
	PermissionSet *PermissionSet `xml:"PermissionSet,omitempty"`
	// UnreadCount is nil for a calendar or a contacts folder, whose types have
	// none.
	UnreadCount *int `xml:"UnreadCount,omitempty"`
	// SearchParameters closes a SearchFolderType; it is emitted only when GetFolder
	// asked for it and the folder is a search folder (otherwise nil).
	SearchParameters *SearchParameters `xml:"SearchParameters,omitempty"`
}

// FolderID is the EWS <t:FolderId> element: an opaque id plus a change key, both
// carried as attributes.
type FolderID struct {
	ID        string `xml:"Id,attr"`
	ChangeKey string `xml:"ChangeKey,attr,omitempty"`
}

// FolderInput is the store data a folder element is built from. ParentID is the
// EWS parent folder id (nil for the mailbox root, whose parent is not emitted);
// clients build the folder tree from it, so a folder without it cannot be placed
// under its parent during an enumeration.
type FolderInput struct {
	FolderID     int64
	ParentID     *int64
	ChangeNumber uint64
	DisplayName  string
	Total        int
	Unread       int
	Children     int
	// Mailbox is the target mailbox SMTP when the folder lives in another mailbox the
	// caller was granted access to; empty for the caller's own. It is encoded into the
	// folder and parent ids so a later request reopens the same mailbox.
	Mailbox string
	// Search marks a search folder, rendered as <t:SearchFolder>.
	Search bool
	// Class is the folder's container class (PidTagContainerClass), empty when
	// it has none.
	Class string
}

// typedFolders are the container classes that make a folder a typed element,
// and whether that element carries an UnreadCount: CalendarFolderType and
// ContactsFolderType derive from BaseFolderType, which has none, while
// TasksFolderType derives from FolderType, which has one.
var typedFolders = []struct {
	class, element string
	unread         bool
}{
	{"IPF.Appointment", "CalendarFolder", false},
	{"IPF.Contact", "ContactsFolder", false},
	{"IPF.Task", "TasksFolder", true},
}

// folderElement names the element a folder renders as, and whether it carries
// an UnreadCount. A class names its kind with its subclasses
// ("IPF.Contact.Custom" is a contacts folder).
func folderElement(in FolderInput) (string, bool) {
	if in.Search {
		return "SearchFolder", true
	}
	for _, t := range typedFolders {
		if in.Class == t.class || strings.HasPrefix(in.Class, t.class+".") {
			return t.element, t.unread
		}
	}
	return "Folder", true
}

// BuildFolder renders a folder element from store folder data.
func BuildFolder(in FolderInput) Folder {
	name, hasUnread := folderElement(in)
	f := Folder{
		XMLName:          xml.Name{Space: typesNS, Local: name},
		FolderID:         FolderID{ID: EncodeFolderIDFor(in.FolderID, in.Mailbox), ChangeKey: ChangeKey(in.ChangeNumber)},
		DisplayName:      in.DisplayName,
		TotalCount:       in.Total,
		ChildFolderCount: in.Children,
	}
	if in.Class != "" {
		class := in.Class
		f.FolderClass = &class
	}
	if hasUnread {
		unread := in.Unread
		f.UnreadCount = &unread
	}
	if in.ParentID != nil {
		f.ParentFolderID = &FolderID{ID: EncodeFolderIDFor(*in.ParentID, in.Mailbox)}
	}
	return f
}
