package mapi

// Standard property tags (MS-OXPROPS). Constants are added per consumer; this
// set covers the folder property bag the object store writes when seeding and
// creating folders.
const (
	PrDisplayName               = PropTag(0x3001001F) // PtUnicode
	PrComment                   = PropTag(0x3004001F) // PtUnicode
	PrContainerClass            = PropTag(0x3613001F) // PtUnicode
	PrCreationTime              = PropTag(0x30070040) // PtSysTime
	PrLastModificationTime      = PropTag(0x30080040) // PtSysTime
	PrHierRev                   = PropTag(0x40820040) // PtSysTime
	PrLocalCommitTimeMax        = PropTag(0x670A0040) // PtSysTime
	PrChangeKey                 = PropTag(0x65E20102) // PtBinary (XID)
	PrPredecessorChangeList     = PropTag(0x65E30102) // PtBinary (PCL)
	PrAttrHidden                = PropTag(0x10F4000B) // PtBoolean
	PrInternetArticleNumber     = PropTag(0x0E230003) // PtLong
	PrInternetArticleNumberNext = PropTag(0x67510003) // PtLong
	PrDeletedCountTotal         = PropTag(0x670B0003) // PtLong
	PrDeletedFolderCount        = PropTag(0x66410003) // PtLong
	PrHierarchyChangeNum        = PropTag(0x663E0003) // PtLong
	PrParentFolderID            = PropTag(0x67490014) // PtI8 (PidTagParentFolderId)
	PrFolderID                  = PropTag(0x67480014) // PtI8 (PidTagFolderId)
)

// Store-root property tags written when seeding a mailbox.
const (
	PrOOFState                  = PropTag(0x661D000B) // PtBoolean (out-of-office)
	PrMessageSizeExtended       = PropTag(0x0E080014) // PtI8
	PrNormalMessageSizeExtended = PropTag(0x66B30014) // PtI8
	PrAssocMessageSizeExtended  = PropTag(0x66B40014) // PtI8
)

// Common container classes for default folders (PR_CONTAINER_CLASS values).
const (
	ContainerClassNote        = "IPF.Note"        // mail folders
	ContainerClassAppointment = "IPF.Appointment" // calendar
	ContainerClassContact     = "IPF.Contact"     // contacts
	ContainerClassTask        = "IPF.Task"        // tasks
	ContainerClassStickyNote  = "IPF.StickyNote"  // notes
	ContainerClassJournal     = "IPF.Journal"     // journal
)
