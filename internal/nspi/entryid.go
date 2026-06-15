package nspi

import "hermex/internal/ext"

// abProviderGUID is the NSPI address-book provider GUID
// ({c840a7dc-42c0-1a10-b4b9-08002b2fe182}) in flat wire order — the provider
// stamp embedded in every PermanentEntryID ([MS-OXNSPI] 2.2.9.3).
var abProviderGUID = [16]byte{
	0xDC, 0xA7, 0x40, 0xC8, 0xC0, 0x42, 0x10, 0x1A,
	0xB4, 0xB9, 0x08, 0x00, 0x2B, 0x2F, 0xE1, 0x82,
}

// Address-book display types ([MS-OXNSPI] / mapidefs).
const (
	dtMailuser  uint32 = 0x00000000 // DT_MAILUSER
	dtContainer uint32 = 0x00000100 // DT_CONTAINER
)

// permanentEntryID builds a PermanentEntryID ([MS-OXNSPI] 2.2.9.3): a 28-byte
// header — flags=0 (ENTRYID_TYPE_PERMANENT), the address-book provider GUID, a
// constant version=1, then the display type — followed by the X500 DN as a
// NUL-terminated ASCII string (total length 28 + len(dn) + 1). It is the
// on-the-wire PR_ENTRYID of every address-book row: a mailuser carries its
// reversible DN, the GAL container carries "/". DNToMId reverses the mailuser
// DN, so the round-trip a client performs (QueryRows row -> OpenEntry by its
// PR_ENTRYID) closes.
func permanentEntryID(displayType uint32, x500dn string) []byte {
	p := ext.NewPush(0)
	p.Uint32(0) // flags: ENTRYID_TYPE_PERMANENT
	p.Raw(abProviderGUID[:])
	p.Uint32(1) // version (constant)
	p.Uint32(displayType)
	p.Raw([]byte(x500dn))
	p.Uint8(0) // NUL terminator
	return p.Bytes()
}
