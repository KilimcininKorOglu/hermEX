// Package nspi implements the NSPI address-book protocol ([MS-OXNSPI]) as
// rendered over MAPI/HTTP ([MS-OXCMAPIHTTP] §2.2.5) on the /mapi/nspi endpoint.
// It answers a real Outlook's Global Address List (GAL) browse and recipient
// resolution, backed by the directory GAL.
//
// The transport — HTTP, Basic auth, and the sid/sequence session cookies —
// belongs to internal/mapihttp; this package owns the NSPI request/response wire
// format and the per-op semantics over internal/ext. Every response opens with a
// MAPI/HTTP-level status (always 0: the request was processed) and the op's
// result (the MAPI return code), and closes with an empty AuxiliaryBuffer
// (cb_auxout = 0). See the internal spec for the grounded layouts.
package nspi

import (
	"sync/atomic"

	"hermex/internal/directory"
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// NSPI bind flags ([MS-OXNSPI] 2.2.1.1) and code pages ([MS-OXCDATA] / defs).
const (
	fAnonymousLogin uint32 = 0x20   // anonymous bind — unsupported
	cpWinUnicode    uint32 = 0x04B0 // CP_WINUNICODE (1200): NSPI cannot bind in Unicode
)

// MAPI return codes ([MS-OXCDATA] 2.4) carried in an NSPI response result, or in
// a row's per-column error marker.
const (
	ecSuccess        uint32 = 0x00000000
	ecError          uint32 = 0x80004005
	ecNotSupported   uint32 = 0x80040102
	ecNotFound       uint32 = 0x8004010F // a requested column has no value for the row
	ecInvalidParam   uint32 = 0x80070057 // e.g. QueryRows count == 0
	ecTableTooBig    uint32 = 0x80040403 // more than 100 columns requested
	ecInvalidObject  uint32 = 0x80040108 // GetProps/GetPropList: no such MId
	ecWarnWithErrors uint32 = 0x00040380 // GetProps: row carries PT_ERROR markers
	ecUnknownLcid    uint32 = 0x8004011F // GetTemplateInfo: no display-table for this locale
)

// NspiGetTemplateInfo Flags ([MS-OXNSPI] 2.2.4 / 3.1.4.10): the reference accepts
// only TI_TEMPLATE alone — a request carrying TI_SCRIPT (or neither) is ecNotSupported.
const (
	tiTemplate uint32 = 0x00000001
	tiScript   uint32 = 0x00000004
)

// abkFlags is the EXT flag set every NSPI body is (de)serialized under, matching
// the MAPI/HTTP NSPI wire (EXT_FLAG_UTF16 | EXT_FLAG_WCOUNT | EXT_FLAG_ABK): all
// strings are UTF-16LE, a binary value's length prefix is 32-bit, and address-book
// property values carry the present/absent marker. Fields that touch none of these
// (u32 scalars, STAT, proptag/MId arrays) serialize identically regardless.
const abkFlags = ext.FlagUTF16 | ext.FlagWCount | ext.FlagABK

// Server answers NSPI requests against the directory GAL. serverGUID identifies
// this server instance in a Bind response (the client caches it for the bound
// session). gal may be nil when the directory cannot enumerate users; Bind still
// succeeds and the address book is simply empty.
type Server struct {
	gal        directory.GAL
	serverGUID mapi.GUID
	rpcSeq     atomic.Uint32 // mints distinct NspiBind context handles over RPC/HTTP
}

// NewServer builds an NSPI server over the GAL with a stable server GUID.
func NewServer(gal directory.GAL, serverGUID mapi.GUID) *Server {
	return &Server{gal: gal, serverGUID: serverGUID}
}

// Bind handles the NSPI Bind request ([MS-OXNSPI] 2.2.7 / [MS-OXCMAPIHTTP]
// 2.2.5.1). It rejects an anonymous bind and a Unicode code page (NSPI strings
// are code-page encoded), and otherwise returns the server GUID. ok reports
// whether the bind succeeded, so the transport establishes the session cookie
// only on success.
func (s *Server) Bind(body []byte) (resp []byte, ok bool) {
	req, err := pullBindRequest(body)
	if err != nil {
		return s.encodeBind(ecError), false
	}
	result := s.bindCore(req)
	return s.encodeBind(result), result == ecSuccess
}

// bindCore runs the NSPI Bind admission check on a decoded request,
// transport-neutral: the MAPI/HTTP handler and the RPC/HTTP stub share it. It
// rejects an anonymous bind and a Unicode code page, else admits the session.
func (s *Server) bindCore(req bindRequest) uint32 {
	switch {
	case req.flags&fAnonymousLogin != 0:
		return ecNotSupported
	case req.stat.codePage == cpWinUnicode:
		return ecNotSupported
	}
	return ecSuccess
}

// encodeBind frames a Bind response: status(0) + result + server GUID (zeroed on
// failure) + an empty AuxiliaryBuffer.
func (s *Server) encodeBind(result uint32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)      // status: MAPI/HTTP-level, always 0 (request processed)
	p.Uint32(result) // result: the NSPI return code
	if result == ecSuccess {
		p.GUID(s.serverGUID)
	} else {
		p.GUID(mapi.GUID{})
	}
	p.Uint32(0) // AuxiliaryBufferSize
	return p.Bytes()
}

// Unbind handles the NSPI Unbind request ([MS-OXNSPI] 2.2.8). It reports success
// unconditionally; the transport drops the session keyed by the sid cookie.
func (s *Server) Unbind(body []byte) []byte {
	_ = pullUnbindRequest(body) // body carries no actionable fields
	p := ext.NewPush(abkFlags)
	p.Uint32(0)         // status
	p.Uint32(ecSuccess) // result
	p.Uint32(0)         // AuxiliaryBufferSize
	return p.Bytes()
}
