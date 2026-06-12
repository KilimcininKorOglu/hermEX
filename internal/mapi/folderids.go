package mapi

// Built-in folder identifiers for a private mailbox store: the fixed small
// global-counter values assigned to the default folders of a private mailbox
// (MS-OXOSFLD). They are stored bare (unwrapped) as folders.folder_id and are
// wrapped with the replica id only at the wire layer via MakeEID.
// PrivateFIDUnassignedStart is the first id handed out to user-created folders.
const (
	PrivateFIDRoot                       = 0x01
	PrivateFIDDeferredAction             = 0x02
	PrivateFIDSpoolerQueue               = 0x03
	PrivateFIDShortcuts                  = 0x04
	PrivateFIDFinder                     = 0x05
	PrivateFIDViews                      = 0x06
	PrivateFIDCommonViews                = 0x07
	PrivateFIDSchedule                   = 0x08
	PrivateFIDIPMSubtree                 = 0x09
	PrivateFIDSentItems                  = 0x0a
	PrivateFIDDeletedItems               = 0x0b
	PrivateFIDOutbox                     = 0x0c
	PrivateFIDInbox                      = 0x0d
	PrivateFIDDraft                      = 0x0e
	PrivateFIDCalendar                   = 0x0f
	PrivateFIDJournal                    = 0x10
	PrivateFIDNotes                      = 0x11
	PrivateFIDTasks                      = 0x12
	PrivateFIDContacts                   = 0x13
	PrivateFIDQuickContacts              = 0x14
	PrivateFIDIMContactList              = 0x15
	PrivateFIDGALContacts                = 0x16
	PrivateFIDJunk                       = 0x17
	PrivateFIDLocalFreebusy              = 0x18
	PrivateFIDSyncIssues                 = 0x19
	PrivateFIDConflicts                  = 0x1a
	PrivateFIDLocalFailures              = 0x1b
	PrivateFIDServerFailures             = 0x1c
	PrivateFIDConversationActionSettings = 0x1d
	PrivateFIDUnassignedStart            = 0x1e
)

// Built-in folder identifiers for a public store (MS-OXOSFLD).
const (
	PublicFIDRoot            = 0x01
	PublicFIDIPMSubtree      = 0x02
	PublicFIDNonIPMSubtree   = 0x03
	PublicFIDEFormsRegistry  = 0x04
	PublicFIDUnassignedStart = 0x05
)
