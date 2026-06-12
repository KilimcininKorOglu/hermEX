package mapi

// XID is an external identifier (MS-OXCFXICS §2.2.2.2): a replica GUID followed
// by a local id of 1..8 bytes. Its total wire size (17..24) is carried out of
// band, so the size byte is not part of the serialized form.
type XID struct {
	GUID    GUID
	LocalID []byte // 1..8 bytes; the wire size is len(LocalID)+16
}

// LongTermID is a specific form of XID (MS-OXCDATA §2.2.1.3.1): a database GUID,
// a 6-byte global counter, and two padding bytes — a fixed 24-byte structure.
type LongTermID struct {
	GUID          GUID
	GlobalCounter GlobCnt
	Padding       uint16
}

// FolderEntryID identifies a folder within a store (MS-OXCDATA §2.2.4.1). The
// provider id is carried as a flat 16-byte uid, whereas the database id is a
// structured GUID.
type FolderEntryID struct {
	Flags        uint32
	ProviderUID  FlatUID
	EIDType      uint16
	FolderDBGUID GUID
	FolderGC     GlobCnt
	Pad1         [2]byte
}

// StoreEntryID is a wrapped store entry id (MS-OXCDATA §2.2.4.3). The outer
// wrapper uid is always MuidStoreWrap; the wrapped provider uid selects a
// private (MuidStorePrivate) or public (MuidStorePublic) store. ServerName and
// MailboxDN are NUL-terminated 8-bit strings on the wire.
type StoreEntryID struct {
	Flags              uint32
	Version            uint8
	IVFlag             uint8
	WrappedFlags       uint32
	WrappedProviderUID FlatUID
	WrappedType        uint32
	ServerName         string
	MailboxDN          string
}

// MessageEntryID identifies a message (MS-OXCDATA §2.2.4.2). It carries the
// folder entry-id fields inline and appends the message's database GUID and
// global counter.
type MessageEntryID struct {
	Flags         uint32
	ProviderUID   FlatUID
	EIDType       uint16
	FolderDBGUID  GUID
	FolderGC      GlobCnt
	Pad1          [2]byte
	MessageDBGUID GUID
	MessageGC     GlobCnt
	Pad2          [2]byte
}
