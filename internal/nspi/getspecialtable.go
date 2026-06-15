package nspi

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// Address-book container flags (PR_CONTAINER_FLAGS, [MS-OXNSPI] 2.2.1.3).
const (
	abRecipients   int32 = 0x1 // the container holds recipients (browsable)
	abUnmodifiable int32 = 0x8 // read-only (no client modification)
)

// galContainerName is the display name, and galContainerID the address-book
// container id, of the single v1 GAL container (STAT.container_id selects it on
// QueryRows).
const (
	galContainerName       = "Global Address List"
	galContainerID   int32 = 0
)

// getSpecialTableRequest is the decoded GetSpecialTable body: flags, an optional
// STAT, an optional version, then the AuxiliaryBuffer. v1 needs only the flags
// and the STAT code page (echoed in the response).
type getSpecialTableRequest struct {
	flags uint32
	stat  stat
}

func pullGetSpecialTable(body []byte) (getSpecialTableRequest, error) {
	p := ext.NewPull(body, 0)
	var r getSpecialTableRequest
	var err error
	if r.flags, err = p.Uint32(); err != nil {
		return r, err
	}
	hasStat, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasStat != 0 {
		if r.stat, err = pullStat(p); err != nil {
			return r, err
		}
	}
	hasVersion, err := p.Uint8()
	if err != nil {
		return r, err
	}
	if hasVersion != 0 {
		if _, err = p.Uint32(); err != nil { // client's cached version (ignored)
			return r, err
		}
	}
	return r, skipAuxIn(p)
}

// GetSpecialTable handles the NSPI GetSpecialTable request ([MS-OXNSPI] 2.2.4 /
// [MS-OXCMAPIHTTP] 2.2.5.3), which returns the address-book container hierarchy.
// v1 exposes one flat GAL container, so the response carries exactly one
// container row.
func (s *Server) GetSpecialTable(body []byte) []byte {
	req, err := pullGetSpecialTable(body)
	if err != nil {
		return s.encodeGetSpecialTable(ecError, 0, nil)
	}
	row := mapi.PropertyValues{
		{Tag: mapi.PrEntryID, Value: permanentEntryID(dtContainer, "/")},
		{Tag: mapi.PrContainerFlags, Value: abRecipients | abUnmodifiable},
		{Tag: mapi.PrDepth, Value: int32(0)},
		{Tag: mapi.PrEmsAbContainerID, Value: int32(galContainerID)},
		{Tag: mapi.PrDisplayName, Value: galContainerName},
		{Tag: mapi.PrEmsAbIsMaster, Value: false},
	}
	return s.encodeGetSpecialTable(ecSuccess, req.stat.codePage, []mapi.PropertyValues{row})
}

// encodeGetSpecialTable frames a GetSpecialTable response: status + result +
// the echoed code page + the version marker (v1 omits the version, so the
// client always reads the current table) + the row set + an empty
// AuxiliaryBuffer. The rows are serialized under EXT_FLAG_ABK, the address-book
// value encoding. A failure or an empty set writes a single 0 in place of the
// rows.
func (s *Server) encodeGetSpecialTable(result, codePage uint32, rows []mapi.PropertyValues) []byte {
	p := ext.NewPush(ext.FlagABK)
	p.Uint32(0)        // status: MAPI/HTTP-level, always 0
	p.Uint32(result)   // result: the NSPI return code
	p.Uint32(codePage) // CodePage (echoed from the request STAT)
	p.Uint8(0)         // Version: absent
	if result != ecSuccess || len(rows) == 0 {
		p.Uint8(0) // HasRows = false
	} else {
		p.Uint8(0xFF) // HasRows = true
		p.Uint32(uint32(len(rows)))
		for _, row := range rows {
			_ = p.PropertyValuesLong(row) // TPROPVAL_ARRAY per row (ABK)
		}
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}
