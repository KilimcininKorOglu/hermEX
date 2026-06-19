package nspi

import (
	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// RPC interface identity ([MS-OXNSPI] / [MS-OXABREF] 2.1): the NSPI address-book
// RPC interface, the connection-oriented form of the same GAL semantics the
// MAPI/HTTP endpoint serves. Its parameters are NDR-marshalled ([MS-OXNSPI]
// §2.2), distinct from the flat-EXT bodies the func([]byte)[]byte handlers
// (de)serialize; both wire frontends share the unexported typed cores.
var (
	// RPCInterfaceUUID is the NSPI interface UUID f5cc5a18-4264-101a-8c59-08002b2f8426.
	RPCInterfaceUUID = mapi.GUID{
		Data1: 0xF5CC5A18, Data2: 0x4264, Data3: 0x101A,
		Data4: [8]byte{0x8C, 0x59, 0x08, 0x00, 0x2B, 0x2F, 0x84, 0x26},
	}
	// RPCInterfaceVersion is the interface version 56.0. The DCE/RPC syntax
	// version packs major in the low 16 bits and minor in the high 16: 56|(0<<16)
	// == 56 (cf. EMSMDB 0.81 == 0x00510000).
	RPCInterfaceVersion uint32 = 56
)

// NSPI opnums ([MS-OXNSPI] 3.1.4). The write/template ops — ModProps (11),
// GetTemplateInfo (13), ModLinkAtt (14) — answer with a faithful MAPI error rather
// than an op-range fault: the GAL is read-only (ModProps/ModLinkAtt → ecNotSupported)
// and there is no display-table archive (GetTemplateInfo → ecUnknownLcid).
const (
	opNspiBind              uint16 = 0
	opNspiUnbind            uint16 = 1
	opNspiUpdateStat        uint16 = 2
	opNspiQueryRows         uint16 = 3
	opNspiSeekEntries       uint16 = 4
	opNspiGetMatches        uint16 = 5
	opNspiResortRestriction uint16 = 6
	opNspiDNToMId           uint16 = 7
	opNspiGetPropList       uint16 = 8
	opNspiGetProps          uint16 = 9
	opNspiCompareMIds       uint16 = 10
	opNspiModProps          uint16 = 11
	opNspiGetSpecialTable   uint16 = 12
	opNspiGetTemplateInfo   uint16 = 13
	opNspiModLinkAtt        uint16 = 14
	opNspiQueryColumns      uint16 = 16
	opNspiResolveNames      uint16 = 19
	opNspiResolveNamesW     uint16 = 20
)

// nspiUnbindSuccess is MAPI_E_UNBINDSUCCESS (1), the NSPI-specific success code
// NspiUnbind returns ([MS-OXNSPI] 3.1.4.1.2) — not ecSuccess.
const nspiUnbindSuccess uint32 = 1

// DispatchRPC routes one NSPI connection-oriented RPC call: it decodes the
// opnum's NDR IN parameters, runs the shared typed core, and encodes the NDR OUT
// parameters. It is transport-neutral — internal/mapihttp registers it on the
// RPC/HTTP DCE/RPC dispatcher through an adapter, so this package does not import
// the transport. A decode failure returns an NDR fault; an unserved opnum returns
// an op-range fault.
//
// The GAL is global to every authenticated user, so no per-bind session state is
// kept: NspiBind mints a fresh context handle the client echoes back on each
// call, and the data ops are stateless (the STAT cursor is client-carried). The
// HTTP transport has already authenticated the caller via Basic auth.
func (s *Server) DispatchRPC(opnum uint16, stub []byte) (out []byte, fault uint32) {
	switch opnum {
	case opNspiBind:
		return s.rpcBind(stub)
	case opNspiUnbind:
		return s.rpcUnbind(stub)
	case opNspiUpdateStat:
		return s.rpcUpdateStat(stub)
	case opNspiQueryRows:
		return s.rpcQueryRows(stub)
	case opNspiSeekEntries:
		return s.rpcSeekEntries(stub)
	case opNspiGetMatches:
		return s.rpcGetMatches(stub)
	case opNspiResortRestriction:
		return s.rpcResortRestriction(stub)
	case opNspiDNToMId:
		return s.rpcDNToMid(stub)
	case opNspiGetPropList:
		return s.rpcGetPropList(stub)
	case opNspiGetProps:
		return s.rpcGetProps(stub)
	case opNspiCompareMIds:
		return s.rpcCompareMids(stub)
	case opNspiModProps:
		return s.rpcModProps(stub)
	case opNspiGetSpecialTable:
		return s.rpcGetSpecialTable(stub)
	case opNspiGetTemplateInfo:
		return s.rpcGetTemplateInfo(stub)
	case opNspiModLinkAtt:
		return s.rpcModLinkAtt(stub)
	case opNspiQueryColumns:
		return s.rpcQueryColumns(stub)
	case opNspiResolveNames:
		return s.rpcResolveNames(stub, false)
	case opNspiResolveNamesW:
		return s.rpcResolveNames(stub, true)
	default:
		// Opnums 15/17/18 are reserved/unused in the NSPI interface (the reference
		// enum omits them too), and anything past 20 is out of range; none is a
		// defined operation, so all fault.
		return nil, ndr.FaultOpRngError
	}
}

// mintRPCHandle allocates a fresh NSPI context-handle GUID from a per-server
// counter. The client treats the handle's 20 bytes as opaque and echoes them on
// each subsequent call; the server keeps no table, so the value only needs to be
// distinct per bind.
func (s *Server) mintRPCHandle() mapi.GUID {
	n := s.rpcSeq.Add(1)
	return mapi.GUID{Data1: n, Data4: [8]byte{'h', 'e', 'r', 'm', 'e', 'x', 0, 0}}
}

// rpcBind handles NspiBind (opnum 0): flags, the STAT, and an [in,out]
// unique-pointer server GUID (the client's cached value, ignored). It runs the
// shared admission check (bindCore); on success it mints a context handle and
// returns this server's GUID, and on failure the handle is zeroed. The server-GUID
// OUT slot mirrors the client's pointer presence, as an [in,out] unique pointer
// requires.
func (s *Server) rpcBind(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	flags, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	st, err := pullStatNDR(p)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	ref, err := p.Uint32() // server GUID [in,out] referent
	if err != nil {
		return nil, ndr.FaultNdr
	}
	hadGUID := ref != 0
	if hadGUID {
		if _, err := p.Raw(16); err != nil { // the client's cached GUID, ignored
			return nil, ndr.FaultNdr
		}
	}

	result := s.bindCore(bindRequest{flags: flags, stat: st})

	out := ndr.NewPush()
	out.UniquePtr(hadGUID)
	if hadGUID {
		if result == ecSuccess {
			f := s.serverGUID.Flat()
			out.Raw(f[:])
		} else {
			out.Raw(make([]byte, 16))
		}
	}
	if result == ecSuccess {
		pushCtxHandleNDR(out, 0, s.mintRPCHandle())
	} else {
		pushCtxHandleNDR(out, 0, mapi.GUID{})
	}
	out.Uint32(result)
	return out.Bytes(), 0
}

// rpcUnbind handles NspiUnbind (opnum 1): it consumes the context handle, returns
// a zeroed handle, and reports MAPI_E_UNBINDSUCCESS — the NSPI-specific success
// code, not ecSuccess. No session is dropped because none is kept; the trailing
// reserved word carries nothing actionable.
func (s *Server) rpcUnbind(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if _, _, err := pullCtxHandleNDR(p); err != nil {
		return nil, ndr.FaultNdr
	}
	out := ndr.NewPush()
	pushCtxHandleNDR(out, 0, mapi.GUID{}) // zeroed on unbind
	out.Uint32(nspiUnbindSuccess)
	return out.Bytes(), 0
}
