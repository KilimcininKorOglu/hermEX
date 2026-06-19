package oxews

import "encoding/xml"

// Folder is the EWS <t:Folder> element (the BaseFolderType subset v1 emits). The
// element declares the types namespace once on itself; the child elements inherit
// it as the default namespace, so they need no per-field namespace boilerplate.
type Folder struct {
	XMLName          xml.Name  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
	FolderID         FolderID  `xml:"FolderId"`
	ParentFolderID   *FolderID `xml:"ParentFolderId,omitempty"`
	DisplayName      string    `xml:"DisplayName"`
	TotalCount       int       `xml:"TotalCount"`
	ChildFolderCount int       `xml:"ChildFolderCount"`
	// PermissionSet precedes UnreadCount per the FolderType schema sequence; it is
	// emitted only when GetFolder requested folder:PermissionSet (otherwise nil).
	PermissionSet *PermissionSet `xml:"PermissionSet,omitempty"`
	UnreadCount   int            `xml:"UnreadCount"`
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
}

// BuildFolder renders a <t:Folder> element from store folder data.
func BuildFolder(in FolderInput) Folder {
	f := Folder{
		FolderID:         FolderID{ID: EncodeFolderID(in.FolderID), ChangeKey: ChangeKey(in.ChangeNumber)},
		DisplayName:      in.DisplayName,
		TotalCount:       in.Total,
		ChildFolderCount: in.Children,
		UnreadCount:      in.Unread,
	}
	if in.ParentID != nil {
		f.ParentFolderID = &FolderID{ID: EncodeFolderID(*in.ParentID)}
	}
	return f
}
