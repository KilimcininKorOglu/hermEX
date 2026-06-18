package rop

import (
	"errors"
	"strings"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// receiveFolderColumns are the fixed columns of a RopGetReceiveFolderTable row
// ([MS-OXCSTOR] 2.2.1.4): the folder id, the ASCII message class, and the
// mapping's last-modification time.
var receiveFolderColumns = []mapi.PropTag{
	mapi.PrFolderID, mapi.PrMessageClassA, mapi.PrLastModificationTime,
}

// ropGetReceiveFolder handles RopGetReceiveFolder ([MS-OXCSTOR] 2.2.1.2): it
// resolves the request's message class to its delivery folder and returns the
// folder id (as an EID) together with the explicit class that matched (the empty
// string when the default or the Inbox fallback answered).
func (s *Session) ropGetReceiveFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	class, e1 := p.String8()
	if e1 != nil {
		return false
	}
	logon := s.get(handleAt(handles, hindex))
	if logon == nil || logon.kind != kindLogon || logon.store == nil {
		writeErr(out, ropGetReceiveFolder, hindex, ecError)
		return true
	}
	fid, explicit, err := logon.store.GetReceiveFolder(class)
	if err != nil {
		writeErr(out, ropGetReceiveFolder, hindex, ecError)
		return true
	}
	out.Uint8(ropGetReceiveFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint64(uint64(mapi.MakeEIDEx(1, uint64(fid)))) // FolderId (EID, like RopLogon)
	out.String8(explicit)
	return true
}

// ropSetReceiveFolder handles RopSetReceiveFolder ([MS-OXCSTOR] 2.2.1.3): it sets
// the receive folder for a message class, or removes the mapping when the folder
// id is 0. The empty default class cannot be removed with a zero folder
// (ecError), and the IPM / REPORT.IPM classes are not settable (ecAccessDenied).
func (s *Session) ropSetReceiveFolder(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	fidRaw, e1 := p.Uint64()
	class, e2 := p.String8()
	if e1 != nil || e2 != nil {
		return false
	}
	logon := s.get(handleAt(handles, hindex))
	if logon == nil || logon.kind != kindLogon || logon.store == nil {
		writeErr(out, ropSetReceiveFolder, hindex, ecError)
		return true
	}
	fid := int64(mapi.EID(fidRaw).GCValue())
	if class == "" && fid == 0 {
		writeErr(out, ropSetReceiveFolder, hindex, ecError) // cannot remove the default with a zero folder
		return true
	}
	if strings.EqualFold(class, "IPM") || strings.EqualFold(class, "REPORT.IPM") {
		writeErr(out, ropSetReceiveFolder, hindex, ecAccessDenied) // not settable
		return true
	}
	if err := logon.store.SetReceiveFolder(class, fid); err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			writeErr(out, ropSetReceiveFolder, hindex, ecNotFound)
		} else {
			writeErr(out, ropSetReceiveFolder, hindex, ecError)
		}
		return true
	}
	out.Uint8(ropSetReceiveFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// ropGetReceiveFolderTable handles RopGetReceiveFolderTable ([MS-OXCSTOR] 2.2.1.4):
// it returns every receive-folder mapping as a row count followed by one
// PROPERTY_ROW per mapping over receiveFolderColumns. The default empty-class row
// can never be removed, so the table is never empty.
func (s *Session) ropGetReceiveFolderTable(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	logon := s.get(handleAt(handles, hindex))
	if logon == nil || logon.kind != kindLogon || logon.store == nil {
		writeErr(out, ropGetReceiveFolderTable, hindex, ecError)
		return true
	}
	entries, err := logon.store.ReceiveFolderTable()
	if err != nil {
		writeErr(out, ropGetReceiveFolderTable, hindex, ecError)
		return true
	}
	// Build the rows first so a serialization failure does not leave a partial
	// response after the header.
	rows := ext.NewPush(ext.FlagUTF16)
	for _, e := range entries {
		vals := mapi.PropertyValues{
			{Tag: mapi.PrFolderID, Value: int64(mapi.MakeEIDEx(1, uint64(e.FolderID)))},
			{Tag: mapi.PrMessageClassA, Value: e.Class},
			{Tag: mapi.PrLastModificationTime, Value: e.ModifiedTime},
		}
		if err := buildPropertyRow(rows, receiveFolderColumns, vals); err != nil {
			writeErr(out, ropGetReceiveFolderTable, hindex, ecError)
			return true
		}
	}
	out.Uint8(ropGetReceiveFolderTable)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint32(uint32(len(entries))) // RowCount
	out.Raw(rows.Bytes())
	return true
}

// ropGetStoreState handles RopGetStoreState ([MS-OXCSTOR] 2.2.1.5). Exchange 2010
// and later (and the reference) do not implement it and return ecNotImplemented;
// clients tolerate that, so hermEX matches rather than fabricating a store-state
// value.
func (s *Session) ropGetStoreState(_ *ext.Pull, out *ext.Push, _ []uint32, hindex uint8) bool {
	writeErr(out, ropGetStoreState, hindex, ecNotImplemented)
	return true
}
