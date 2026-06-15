package ndr

import (
	"errors"

	"hermex/internal/mapi"
)

// Connection-oriented DCE/RPC PDU types ([MS-RPCE] 2.2.2.3 / C706).
const (
	PktRequest  uint8 = 0
	PktResponse uint8 = 2
	PktFault    uint8 = 3
	PktBind     uint8 = 11
	PktBindAck  uint8 = 12
	PktBindNak  uint8 = 13
	PktAlter    uint8 = 14
	PktAlterAck uint8 = 15
	PktAuth3    uint8 = 16
	PktCoCancel uint8 = 18
	PktOrphaned uint8 = 19
	PktRTS      uint8 = 20
)

// PFC (packet flag) bits ([MS-RPCE] 2.2.2.3).
const (
	PfcFirstFrag  uint8 = 0x01
	PfcLastFrag   uint8 = 0x02
	PfcConcMpx    uint8 = 0x10
	PfcDidNotExec uint8 = 0x20
	PfcMaybe      uint8 = 0x40
	PfcObjectUUID uint8 = 0x80
)

// drepLE is the little-endian data-representation byte 0 (integer rep nibble
// 1, ASCII chars, IEEE floats). v1 supports little-endian peers only.
const drepLE = 0x10

// DCE/RPC authentication types and levels (the verifier trailer).
const (
	AuthTypeNone        uint8 = 0
	AuthTypeGSSNegot    uint8 = 9
	AuthTypeNTLMSSP     uint8 = 10
	AuthLevelNone       uint8 = 1
	AuthLevelConnect    uint8 = 2
	AuthLevelPktIntg    uint8 = 5
	AuthLevelPktPrivacy uint8 = 6
)

// bind_ack presentation-context result + reason ([MS-RPCE] 2.2.2.4).
const (
	AckResultAccept         uint16 = 0
	AckResultProviderReject uint16 = 2
	AckReasonNotSpecified   uint16 = 0
)

// bind_nak reject reason ([MS-RPCE] 2.2.2.5).
const BindNakReasonNotSpecified uint16 = 0

// NCA fault status codes ([MS-RPCE] / C706) used in FAULT PDUs.
const (
	FaultOpRngError   uint32 = 0x1C010002
	FaultUnkIf        uint32 = 0x1C010003
	FaultProtoError   uint32 = 0x1C01000B
	FaultNdr          uint32 = 0x000006F7
	FaultAccessDenied uint32 = 0x00000005
)

// TransferSyntaxNDR is the well-known NDR (v2) transfer syntax abstract id a
// connection-oriented bind negotiates ([C706] / [MS-RPCE] §B): UUID
// 8a885d04-1ceb-11c9-9fe8-08002b104860, version 2.0.
var TransferSyntaxNDR = SyntaxID{
	UUID: mapi.GUID{
		Data1: 0x8A885D04, Data2: 0x1CEB, Data3: 0x11C9,
		Data4: [8]byte{0x9F, 0xE8, 0x08, 0x00, 0x2B, 0x10, 0x48, 0x60},
	},
	Version: 2,
}

// ErrBigEndian reports a PDU whose data representation is big-endian; v1 only
// supports little-endian peers (every Windows RPC client sends little-endian).
var ErrBigEndian = errors.New("ndr: big-endian data representation not supported")

// Header is the 16-byte common NCACN PDU header. rpc_vers (5), rpc_vers_minor
// (0), and the little-endian drep are fixed on the wire and not stored.
type Header struct {
	Type    uint8  // pkt_type (wire offset 2)
	Flags   uint8  // pfc_flags (wire offset 3)
	FragLen uint16 // total PDU size including this header and trailing padding
	AuthLen uint16 // length of the auth-verifier token (0 = no verifier)
	CallID  uint32
}

// SyntaxID is a DCE/RPC presentation syntax: an interface/transfer UUID plus a
// 32-bit version (low 16 bits major, high 16 bits minor).
type SyntaxID struct {
	UUID    mapi.GUID
	Version uint32
}

// CtxList is one presentation context offered in a bind: a context id, the
// abstract (interface) syntax, and the transfer syntaxes the client supports.
type CtxList struct {
	ContextID        uint16
	AbstractSyntax   SyntaxID
	TransferSyntaxes []SyntaxID
}

// Bind is a decoded BIND or ALTER_CONTEXT PDU body.
type Bind struct {
	MaxXmitFrag  uint16
	MaxRecvFrag  uint16
	AssocGroupID uint32
	Contexts     []CtxList
	AuthInfo     []byte // the auth verifier (8-byte header + token), empty = auth-none
}

// AckCtx is one presentation-context result in a bind_ack.
type AckCtx struct {
	Result uint16
	Reason uint16
	Syntax SyntaxID
}

// BindAck is a BIND_ACK / ALTER_CONTEXT_RESP PDU body to emit.
type BindAck struct {
	MaxXmitFrag      uint16
	MaxRecvFrag      uint16
	AssocGroupID     uint32
	SecondaryAddress string // the named-pipe/endpoint address ("" emits a zero-length field)
	Results          []AckCtx
}

// Request is a decoded REQUEST PDU body. Stub is the opaque interface stub
// (NDR-marshalled call parameters); for v1 (auth-none) it runs to the PDU end.
type Request struct {
	AllocHint uint32
	ContextID uint16
	Opnum     uint16
	Object    *mapi.GUID
	Stub      []byte
}

// PDU is a parsed inbound PDU: the header plus the decoded body for the types
// the v1 dispatch consumes (BIND/ALTER and REQUEST). Other types carry only the
// header.
type PDU struct {
	Header  Header
	Bind    *Bind
	Request *Request
}

// pushHeader writes the 16-byte NCACN header (rpc 5.0, little-endian drep).
func pushHeader(p *Push, h Header) {
	p.Uint8(5)
	p.Uint8(0)
	p.Uint8(h.Type)
	p.Uint8(h.Flags)
	p.Raw([]byte{drepLE, 0, 0, 0})
	p.Uint16(h.FragLen)
	p.Uint16(h.AuthLen)
	p.Uint32(h.CallID)
}

// pullHeader reads the 16-byte NCACN header, rejecting non-RPC-5 and big-endian
// peers (the NDR primitives are little-endian only).
func pullHeader(p *Pull) (Header, error) {
	var h Header
	vers, err := p.Uint8()
	if err != nil {
		return h, err
	}
	if vers != 5 {
		return h, ErrFormat
	}
	if _, err = p.Uint8(); err != nil { // rpc_vers_minor
		return h, err
	}
	if h.Type, err = p.Uint8(); err != nil {
		return h, err
	}
	if h.Flags, err = p.Uint8(); err != nil {
		return h, err
	}
	drep, err := p.Raw(4)
	if err != nil {
		return h, err
	}
	if drep[0]&0x10 == 0 {
		return h, ErrBigEndian
	}
	if h.FragLen, err = p.Uint16(); err != nil {
		return h, err
	}
	if h.AuthLen, err = p.Uint16(); err != nil {
		return h, err
	}
	if h.CallID, err = p.Uint32(); err != nil {
		return h, err
	}
	return h, nil
}

// pushSyntax writes a SyntaxID (GUID + version, 4-byte aligned via the GUID).
func pushSyntax(p *Push, s SyntaxID) {
	p.GUID(s.UUID)
	p.Uint32(s.Version)
}

// pullSyntax reads a SyntaxID.
func pullSyntax(p *Pull) (SyntaxID, error) {
	var s SyntaxID
	var err error
	if s.UUID, err = p.GUID(); err != nil {
		return s, err
	}
	s.Version, err = p.Uint32()
	return s, err
}

// pullCtxList reads one presentation context (align(4); context_id; n_xfer;
// abstract syntax; transfer syntaxes).
func pullCtxList(p *Pull) (CtxList, error) {
	var c CtxList
	p.Align(4)
	var err error
	if c.ContextID, err = p.Uint16(); err != nil {
		return c, err
	}
	n, err := p.Uint8()
	if err != nil {
		return c, err
	}
	if c.AbstractSyntax, err = pullSyntax(p); err != nil {
		return c, err
	}
	for range n {
		s, err := pullSyntax(p)
		if err != nil {
			return c, err
		}
		c.TransferSyntaxes = append(c.TransferSyntaxes, s)
	}
	return c, nil
}

// pullBind reads a BIND/ALTER body (the cursor is positioned just past the
// 16-byte header).
func pullBind(p *Pull) (*Bind, error) {
	b := &Bind{}
	var err error
	if b.MaxXmitFrag, err = p.Uint16(); err != nil {
		return nil, err
	}
	if b.MaxRecvFrag, err = p.Uint16(); err != nil {
		return nil, err
	}
	if b.AssocGroupID, err = p.Uint32(); err != nil {
		return nil, err
	}
	n, err := p.Uint8()
	if err != nil {
		return nil, err
	}
	for range n {
		c, err := pullCtxList(p)
		if err != nil {
			return nil, err
		}
		b.Contexts = append(b.Contexts, c)
	}
	b.AuthInfo = p.Rest()
	return b, nil
}

// pullRequest reads a REQUEST body. The object GUID is present only when the
// header carries PfcObjectUUID; the stub runs to the PDU end (v1 = auth-none, so
// there is no auth verifier to carve off).
func pullRequest(p *Pull, h Header) (*Request, error) {
	r := &Request{}
	var err error
	if r.AllocHint, err = p.Uint32(); err != nil {
		return nil, err
	}
	if r.ContextID, err = p.Uint16(); err != nil {
		return nil, err
	}
	if r.Opnum, err = p.Uint16(); err != nil {
		return nil, err
	}
	if h.Flags&PfcObjectUUID != 0 {
		g, err := p.GUID()
		if err != nil {
			return nil, err
		}
		r.Object = &g
	}
	p.Align(8) // pad before the stub
	r.Stub = p.Rest()
	return r, nil
}

// ParsePDU decodes an inbound connection-oriented PDU: always the header, plus
// the body for BIND/ALTER and REQUEST (the types the v1 dispatch consumes). RTS
// PDUs are handled by the transport layer before this is called.
func ParsePDU(buf []byte) (*PDU, error) {
	p := NewPull(buf)
	h, err := pullHeader(p)
	if err != nil {
		return nil, err
	}
	pdu := &PDU{Header: h}
	switch h.Type {
	case PktBind, PktAlter:
		if pdu.Bind, err = pullBind(p); err != nil {
			return nil, err
		}
	case PktRequest:
		if pdu.Request, err = pullRequest(p, h); err != nil {
			return nil, err
		}
	}
	return pdu, nil
}

// Frame wraps a pre-built body in the 16-byte NCACN header, computing
// frag_length. The body must already be 4-byte aligned (the body builders end
// with a trailer alignment); v1 emits no auth verifier (auth_length 0).
func Frame(typ, flags uint8, callID uint32, body []byte) []byte {
	p := NewPush()
	pushHeader(p, Header{Type: typ, Flags: flags, FragLen: uint16(16 + len(body)), CallID: callID})
	p.Raw(body)
	return p.Bytes()
}

// buildBindAckBody marshals a BIND_ACK/ALTER_ACK body.
func buildBindAckBody(ba *BindAck) []byte {
	p := NewPush()
	p.Uint16(ba.MaxXmitFrag)
	p.Uint16(ba.MaxRecvFrag)
	p.Uint32(ba.AssocGroupID)
	if ba.SecondaryAddress == "" {
		p.Uint16(0)
	} else {
		sa := append([]byte(ba.SecondaryAddress), 0)
		p.Uint16(uint16(len(sa)))
		p.Raw(sa)
	}
	p.Align(4) // the pad blob between the secondary address and the result count
	p.Uint8(uint8(len(ba.Results)))
	for _, c := range ba.Results {
		p.Align(4)
		p.Uint16(c.Result)
		p.Uint16(c.Reason)
		pushSyntax(p, c.Syntax)
	}
	p.Align(4) // trailer alignment
	return p.Bytes()
}

// FrameBindAck builds a complete BIND_ACK PDU (FIRST|LAST, single fragment).
func FrameBindAck(callID uint32, ba *BindAck) []byte {
	return Frame(PktBindAck, PfcFirstFrag|PfcLastFrag, callID, buildBindAckBody(ba))
}

// FrameBindNak builds a complete BIND_NAK PDU.
func FrameBindNak(callID uint32, reason uint16) []byte {
	p := NewPush()
	p.Uint16(reason)
	p.Align(4) // the reject body aligns to 4; no version list for a generic nak
	return Frame(PktBindNak, PfcFirstFrag|PfcLastFrag, callID, p.Bytes())
}

// buildResponseBody marshals a RESPONSE body: alloc_hint, context_id,
// cancel_count, an 8-byte alignment pad, then the stub fragment, trailer-aligned.
func buildResponseBody(allocHint uint32, contextID uint16, stub []byte) []byte {
	p := NewPush()
	p.Uint32(allocHint)
	p.Uint16(contextID)
	p.Uint8(0) // cancel_count
	p.Align(8) // pad before the stub
	p.Raw(stub)
	p.Align(4) // trailer alignment
	return p.Bytes()
}

// FrameResponse builds one RESPONSE PDU carrying a stub fragment. The caller
// sets flags (PfcFirstFrag on the first fragment, PfcLastFrag on the last) and
// passes allocHint = the full multi-fragment stub size.
func FrameResponse(callID uint32, flags uint8, contextID uint16, allocHint uint32, stub []byte) []byte {
	return Frame(PktResponse, flags, callID, buildResponseBody(allocHint, contextID, stub))
}

// buildFaultBody marshals a FAULT body: alloc_hint, context_id, cancel_count,
// then the NCA status (4-byte aligned), trailer-aligned.
func buildFaultBody(allocHint uint32, contextID uint16, status uint32) []byte {
	p := NewPush()
	p.Uint32(allocHint)
	p.Uint16(contextID)
	p.Uint8(0) // cancel_count
	p.Uint32(status)
	p.Align(4)
	return p.Bytes()
}

// FrameFault builds a complete FAULT PDU.
func FrameFault(callID uint32, contextID uint16, status uint32) []byte {
	return Frame(PktFault, PfcFirstFrag|PfcLastFrag, callID, buildFaultBody(0, contextID, status))
}
