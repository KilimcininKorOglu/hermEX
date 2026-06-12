package ext

import "hermex/internal/mapi"

// --- FlatUID and GlobCnt (raw fixed-width byte runs) ---

// FlatUID writes a flat 16-byte GUID verbatim (p_guid for a FLATUID is a raw
// 16-byte copy, not a field-wise encoding).
func (p *Push) FlatUID(f mapi.FlatUID) { p.Raw(f[:]) }

// FlatUID reads a flat 16-byte GUID.
func (p *Pull) FlatUID() (mapi.FlatUID, error) {
	var f mapi.FlatUID
	b, err := p.Raw(16)
	if err != nil {
		return f, err
	}
	copy(f[:], b)
	return f, nil
}

// GlobCnt writes the six raw global-counter bytes.
func (p *Push) GlobCnt(gc mapi.GlobCnt) { p.Raw(gc[:]) }

// GlobCnt reads six raw global-counter bytes.
func (p *Pull) GlobCnt() (mapi.GlobCnt, error) {
	var gc mapi.GlobCnt
	b, err := p.Raw(6)
	if err != nil {
		return gc, err
	}
	copy(gc[:], b)
	return gc, nil
}

// --- XID (variable local id) ---

// XID writes an XID: the replica GUID followed by its local-id bytes. The total
// size (len(LocalID)+16) must fall in 17..24's validation.
func (p *Push) XID(x mapi.XID) error {
	size := 16 + len(x.LocalID)
	if size < 17 || size > 24 {
		return ErrFormat
	}
	p.GUID(x.GUID)
	p.Raw(x.LocalID)
	return nil
}

// XID reads an XID of the given total wire size (17..24): the GUID plus
// size-16 local-id bytes (the size comes from the caller).
func (p *Pull) XID(size int) (mapi.XID, error) {
	var x mapi.XID
	if size < 17 || size > 24 {
		return x, ErrFormat
	}
	g, err := p.GUID()
	if err != nil {
		return x, err
	}
	x.GUID = g
	x.LocalID, err = p.Raw(size - 16)
	return x, err
}

// --- LONG_TERM_ID ---

// LongTermID writes a 24-byte LONG_TERM_ID: GUID, six global-counter bytes, and
// a 16-bit pad.
func (p *Push) LongTermID(l mapi.LongTermID) {
	p.GUID(l.GUID)
	p.GlobCnt(l.GlobalCounter)
	p.Uint16(l.Padding)
}

// LongTermID reads a 24-byte LONG_TERM_ID written by LongTermID.
func (p *Pull) LongTermID() (mapi.LongTermID, error) {
	var l mapi.LongTermID
	var err error
	if l.GUID, err = p.GUID(); err != nil {
		return l, err
	}
	if l.GlobalCounter, err = p.GlobCnt(); err != nil {
		return l, err
	}
	l.Padding, err = p.Uint16()
	return l, err
}

// --- FOLDER_ENTRYID / MESSAGE_ENTRYID ---

// FolderEntryID writes a 46-byte folder entry id.
func (p *Push) FolderEntryID(f mapi.FolderEntryID) {
	p.Uint32(f.Flags)
	p.FlatUID(f.ProviderUID)
	p.Uint16(f.EIDType)
	p.GUID(f.FolderDBGUID)
	p.GlobCnt(f.FolderGC)
	p.Raw(f.Pad1[:])
}

// FolderEntryID reads a 46-byte folder entry id written by FolderEntryID.
func (p *Pull) FolderEntryID() (mapi.FolderEntryID, error) {
	var f mapi.FolderEntryID
	var err error
	if f.Flags, err = p.Uint32(); err != nil {
		return f, err
	}
	if f.ProviderUID, err = p.FlatUID(); err != nil {
		return f, err
	}
	if f.EIDType, err = p.Uint16(); err != nil {
		return f, err
	}
	if f.FolderDBGUID, err = p.GUID(); err != nil {
		return f, err
	}
	if f.FolderGC, err = p.GlobCnt(); err != nil {
		return f, err
	}
	pad, err := p.Raw(2)
	if err != nil {
		return f, err
	}
	copy(f.Pad1[:], pad)
	return f, nil
}

// --- STORE_ENTRYID (wrapped) ---

// wrappedProviderDLL is the 14-byte provider DLL name MS-OXCDATA §2.2.4.3
// embeds in a wrapped store entry id: the literal "emsmdb.dll" plus its NUL
// terminator, zero-padded to 14 bytes. It is a Microsoft wire constant that
// clients expect verbatim, not an internal module name.
var wrappedProviderDLL = [14]byte{'e', 'm', 's', 'm', 'd', 'b', '.', 'd', 'l', 'l'}

// StoreEntryID writes a wrapped store entry id. It always emits
// the full inline-flag form: the wrapper uid is forced to MuidStoreWrap and the
// provider DLL name is the fixed wire constant, regardless of s.IVFlag.
func (p *Push) StoreEntryID(s mapi.StoreEntryID) {
	p.Uint32(s.Flags)
	p.FlatUID(mapi.MuidStoreWrap)
	p.Uint8(s.Version)
	p.Uint8(s.IVFlag)
	p.Raw(wrappedProviderDLL[:])
	p.Uint32(s.WrappedFlags)
	p.FlatUID(s.WrappedProviderUID)
	p.Uint32(s.WrappedType)
	p.String8(s.ServerName)
	p.String8(s.MailboxDN)
}

// StoreEntryID reads a wrapped store entry id. It validates the
// wrapper uid and version, then branches on the inline flag: 0 carries the DLL
// name plus the full wrapped record; 1 carries only the wrapped provider uid
// with the remaining fields defaulted. Any other value is malformed.
func (p *Pull) StoreEntryID() (mapi.StoreEntryID, error) {
	var s mapi.StoreEntryID
	var err error
	if s.Flags, err = p.Uint32(); err != nil {
		return s, err
	}
	wrap, err := p.FlatUID()
	if err != nil {
		return s, err
	}
	if wrap != mapi.MuidStoreWrap {
		return s, ErrFormat
	}
	if s.Version, err = p.Uint8(); err != nil {
		return s, err
	}
	if s.Version != 0 {
		return s, ErrFormat
	}
	if s.IVFlag, err = p.Uint8(); err != nil {
		return s, err
	}
	switch s.IVFlag {
	case 0:
		if _, err = p.Raw(14); err != nil { // DLL name, not retained
			return s, err
		}
		if s.WrappedFlags, err = p.Uint32(); err != nil {
			return s, err
		}
		if s.WrappedProviderUID, err = p.FlatUID(); err != nil {
			return s, err
		}
		if s.WrappedType, err = p.Uint32(); err != nil {
			return s, err
		}
		if s.ServerName, err = p.String8(); err != nil {
			return s, err
		}
		s.MailboxDN, err = p.String8()
		return s, err
	case 1:
		s.WrappedProviderUID, err = p.FlatUID()
		return s, err
	default:
		return s, ErrFormat
	}
}

// MessageEntryID writes a 70-byte message entry id.
func (p *Push) MessageEntryID(m mapi.MessageEntryID) {
	p.Uint32(m.Flags)
	p.FlatUID(m.ProviderUID)
	p.Uint16(m.EIDType)
	p.GUID(m.FolderDBGUID)
	p.GlobCnt(m.FolderGC)
	p.Raw(m.Pad1[:])
	p.GUID(m.MessageDBGUID)
	p.GlobCnt(m.MessageGC)
	p.Raw(m.Pad2[:])
}

// MessageEntryID reads a 70-byte message entry id written by MessageEntryID.
func (p *Pull) MessageEntryID() (mapi.MessageEntryID, error) {
	var m mapi.MessageEntryID
	var err error
	if m.Flags, err = p.Uint32(); err != nil {
		return m, err
	}
	if m.ProviderUID, err = p.FlatUID(); err != nil {
		return m, err
	}
	if m.EIDType, err = p.Uint16(); err != nil {
		return m, err
	}
	if m.FolderDBGUID, err = p.GUID(); err != nil {
		return m, err
	}
	if m.FolderGC, err = p.GlobCnt(); err != nil {
		return m, err
	}
	pad1, err := p.Raw(2)
	if err != nil {
		return m, err
	}
	copy(m.Pad1[:], pad1)
	if m.MessageDBGUID, err = p.GUID(); err != nil {
		return m, err
	}
	if m.MessageGC, err = p.GlobCnt(); err != nil {
		return m, err
	}
	pad2, err := p.Raw(2)
	if err != nil {
		return m, err
	}
	copy(m.Pad2[:], pad2)
	return m, nil
}
