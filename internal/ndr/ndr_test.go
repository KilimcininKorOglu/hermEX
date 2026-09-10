package ndr

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
)

// emsmdbUUID is the EMSMDB interface id ([MS-OXCRPC]); used as a realistic
// abstract syntax in the hand-built bind vector.
var emsmdbUUID = mapi.GUID{
	Data1: 0xA4F1DB00, Data2: 0xCA47, Data3: 0x1067,
	Data4: [8]byte{0xB3, 0x1F, 0x00, 0xDD, 0x01, 0x06, 0x62, 0xDA},
}

// TestPushAlignment proves a scalar self-aligns to its width: a uint32 written
// after a single byte is padded to the 4-byte boundary, so it lands at offset 4.
func TestPushAlignment(t *testing.T) {
	p := NewPush()
	p.Uint8(0xAA)
	p.Uint32(0xDEADBEEF)
	want := []byte{0xAA, 0, 0, 0, 0xEF, 0xBE, 0xAD, 0xDE}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("aligned bytes = % x, want % x", p.Bytes(), want)
	}
	// Pull mirrors the alignment.
	q := NewPull(p.Bytes())
	if b, _ := q.Uint8(); b != 0xAA {
		t.Errorf("u8 = %#x, want 0xAA", b)
	}
	if v, _ := q.Uint32(); v != 0xDEADBEEF {
		t.Errorf("u32 = %#x, want 0xDEADBEEF (alignment skipped the pad)", v)
	}
}

// TestUniquePtr pins the referent-id allocation: the first non-null pointer is
// 0x00020000, each subsequent one increments by 4, and a null pointer is 0.
func TestUniquePtr(t *testing.T) {
	p := NewPush()
	if id := p.UniquePtr(true); id != 0x00020000 {
		t.Errorf("first referent id = %#x, want 0x20000", id)
	}
	if id := p.UniquePtr(true); id != 0x00020004 {
		t.Errorf("second referent id = %#x, want 0x20004", id)
	}
	if id := p.UniquePtr(false); id != 0 {
		t.Errorf("null pointer id = %#x, want 0", id)
	}
	want := []byte{0x00, 0x00, 0x02, 0x00, 0x04, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("referent bytes = % x, want % x", p.Bytes(), want)
	}
}

// TestGUIDRoundTrip proves the field-wise GUID codec is its own inverse.
func TestGUIDRoundTrip(t *testing.T) {
	p := NewPush()
	p.GUID(emsmdbUUID)
	g, err := NewPull(p.Bytes()).GUID()
	if err != nil || g != emsmdbUUID {
		t.Errorf("GUID round-trip = (%v, %v), want %v", g, err, emsmdbUUID)
	}
}

// TestFrameHeaderBytes pins the 16-byte NCACN header layout: rpc 5.0, the
// pkt_type at offset 2, pfc_flags at 3, little-endian drep at 4, frag_length at
// 8, auth_length at 10, call_id at 12.
func TestFrameHeaderBytes(t *testing.T) {
	pdu := Frame(PktResponse, PfcFirstFrag|PfcLastFrag, 0x01020304, []byte{0xDE, 0xAD, 0xBE, 0xEF})
	want := []byte{
		5, 0, PktResponse, 0x03, // vers, minor, type, flags
		0x10, 0, 0, 0, // drep (little-endian)
		20, 0, // frag_length = 16 + 4
		0, 0, // auth_length = 0
		0x04, 0x03, 0x02, 0x01, // call_id LE
		0xDE, 0xAD, 0xBE, 0xEF, // body
	}
	if !bytes.Equal(pdu, want) {
		t.Errorf("framed PDU = % x, want % x", pdu, want)
	}
}

// bindVector is a hand-assembled BIND PDU (independent of the encoder): one
// presentation context offering the EMSMDB abstract syntax (v0.81) over the NDR
// transfer syntax (v2), no auth verifier. It exists to prove ParsePDU decodes a
// real client bind byte-for-byte.
func bindVector() []byte {
	return []byte{
		// --- 16-byte header ---
		5, 0, PktBind, 0x03, // vers, minor, BIND, FIRST|LAST
		0x10, 0, 0, 0, // drep LE
		72, 0, // frag_length
		0, 0, // auth_length
		0x04, 0x03, 0x02, 0x01, // call_id
		// --- bind body ---
		0xD0, 0x16, // max_xmit_frag = 0x16D0
		0xD0, 0x16, // max_recv_frag
		0, 0, 0, 0, // assoc_group_id
		1,       // num_contexts
		0, 0, 0, // pad to 4 (offset 25->28)
		0, 0, // context_id = 0
		1, // num_transfer_syntaxes
		0, // pad to 4 before the abstract syntax (offset 31->32)
		// abstract syntax: EMSMDB UUID (Data1-3 LE, Data4 verbatim) + version 0x00510000
		0x00, 0xDB, 0xF1, 0xA4, 0x47, 0xCA, 0x67, 0x10,
		0xB3, 0x1F, 0x00, 0xDD, 0x01, 0x06, 0x62, 0xDA,
		0x00, 0x00, 0x51, 0x00,
		// transfer syntax: NDR UUID + version 2
		0x04, 0x5D, 0x88, 0x8A, 0xEB, 0x1C, 0xC9, 0x11,
		0x9F, 0xE8, 0x08, 0x00, 0x2B, 0x10, 0x48, 0x60,
		0x02, 0x00, 0x00, 0x00,
	}
}

// TestParseBindVector proves ParsePDU decodes a hand-built client bind: the
// header, the negotiated frame sizes, the abstract syntax (EMSMDB v0.81), the
// transfer syntax (NDR v2), and an empty (auth-none) verifier.
func TestParseBindVector(t *testing.T) {
	pdu, err := ParsePDU(bindVector())
	mustNoErr(t, err, "ParsePDU")

	wantEq(t, pdu.Header.Type, uint8(PktBind), "the PDU type")
	wantEq(t, pdu.Header.CallID, uint32(0x01020304), "the call id")
	if pdu.Bind == nil {
		t.Fatal("no bind body decoded")
	}
	wantEq(t, pdu.Bind.MaxXmitFrag, uint16(0x16D0), "the max transmit frame size")
	wantEq(t, pdu.Bind.MaxRecvFrag, uint16(0x16D0), "the max receive frame size")
	mustCount(t, len(pdu.Bind.Contexts), 1, "the presentation contexts")

	c := pdu.Bind.Contexts[0]
	wantEq(t, c.AbstractSyntax.UUID, emsmdbUUID, "the abstract syntax UUID")
	wantEq(t, c.AbstractSyntax.Version, uint32(0x00510000), "the abstract syntax version")
	mustCount(t, len(c.TransferSyntaxes), 1, "the transfer syntaxes")
	wantEq(t, c.TransferSyntaxes[0], TransferSyntaxNDR, "the transfer syntax")
	wantEq(t, len(pdu.Bind.AuthInfo), 0, "the auth info length (auth-none)")
}

// TestBigEndianRejected proves a big-endian peer is rejected (the NDR
// primitives are little-endian only).
func TestBigEndianRejected(t *testing.T) {
	v := bindVector()
	v[4] = 0x00 // clear the little-endian drep bit
	if _, err := ParsePDU(v); err != ErrBigEndian {
		t.Errorf("ParsePDU(big-endian) err = %v, want ErrBigEndian", err)
	}
}

// TestBindAckRoundTrip builds a BIND_ACK and reads it back through the header +
// body codec, proving the accepted context result and negotiated syntax survive.
func TestBindAckRoundTrip(t *testing.T) {
	ba := &BindAck{
		MaxXmitFrag:      0x16D0,
		MaxRecvFrag:      0x16D0,
		AssocGroupID:     0x1234,
		SecondaryAddress: "6001",
		Results:          []AckCtx{{Result: AckResultAccept, Reason: AckReasonNotSpecified, Syntax: TransferSyntaxNDR}},
	}
	pduBytes := FrameBindAck(0x55, ba)

	p := NewPull(pduBytes)
	h, err := pullHeader(p)
	mustNoErr(t, err, "pullHeader")
	wantEq(t, h.Type, uint8(PktBindAck), "the PDU type")
	wantEq(t, h.Flags, uint8(PfcFirstFrag|PfcLastFrag), "the fragment flags")
	wantEq(t, int(h.FragLen), len(pduBytes), "the fragment length (the whole PDU)")

	maxXmit, _ := p.Uint16()
	maxRecv, _ := p.Uint16()
	assoc, _ := p.Uint32()
	wantEq(t, maxXmit, uint16(0x16D0), "the max transmit frame size")
	wantEq(t, maxRecv, uint16(0x16D0), "the max receive frame size")
	wantEq(t, assoc, uint32(0x1234), "the association group id")

	saLen, _ := p.Uint16()
	wantEq(t, saLen, uint16(5), `the secondary address length ("6001" plus its terminator)`)
	_, err = p.Raw(int(saLen))
	mustNoErr(t, err, "the secondary address")

	p.Align(4)
	n, _ := p.Uint8()
	mustCount(t, int(n), 1, "the ack result count")
	p.Align(4)
	result, _ := p.Uint16()
	reason, _ := p.Uint16()
	syn, _ := pullSyntax(p)
	wantEq(t, result, uint16(AckResultAccept), "the context result")
	wantEq(t, reason, uint16(AckReasonNotSpecified), "the rejection reason")
	wantEq(t, syn, TransferSyntaxNDR, "the negotiated transfer syntax")
}

// TestParseRequest proves a REQUEST PDU yields the opnum, context id, and the
// stub bytes that follow the 8-byte-aligned header.
func TestParseRequest(t *testing.T) {
	stub := []byte{0x11, 0x22, 0x33, 0x44, 0x55}
	// Build the request body directly: alloc_hint, context_id, opnum, pad8, stub.
	body := NewPush()
	body.Uint32(uint32(len(stub))) // alloc_hint
	body.Uint16(7)                 // context_id
	body.Uint16(11)                // opnum (EcDoRpcExt2)
	body.Align(8)
	body.Raw(stub)
	pduBytes := Frame(PktRequest, PfcFirstFrag|PfcLastFrag, 0x99, body.Bytes())

	pdu, err := ParsePDU(pduBytes)
	if err != nil {
		t.Fatalf("ParsePDU: %v", err)
	}
	if pdu.Request == nil {
		t.Fatal("no request body decoded")
	}
	if pdu.Request.ContextID != 7 || pdu.Request.Opnum != 11 {
		t.Errorf("request = {ctx %d, opnum %d}, want {7, 11}", pdu.Request.ContextID, pdu.Request.Opnum)
	}
	if !bytes.Equal(pdu.Request.Stub, stub) {
		t.Errorf("stub = % x, want % x", pdu.Request.Stub, stub)
	}
}

// TestFrameFault proves a FAULT PDU carries the NCA status at the documented
// offset and frames to a complete, header-consistent PDU.
func TestFrameFault(t *testing.T) {
	pdu := FrameFault(0x42, 0, FaultOpRngError)
	p := NewPull(pdu)
	h, err := pullHeader(p)
	if err != nil || h.Type != PktFault {
		t.Fatalf("fault header = (%v, %#x), want fault", err, h.Type)
	}
	if _, err := p.Uint32(); err != nil { // alloc_hint
		t.Fatal(err)
	}
	if _, err := p.Uint16(); err != nil { // context_id
		t.Fatal(err)
	}
	if _, err := p.Uint8(); err != nil { // cancel_count
		t.Fatal(err)
	}
	status, _ := p.Uint32()
	if status != FaultOpRngError {
		t.Errorf("fault status = %#x, want %#x", status, FaultOpRngError)
	}
}

// TestPullCheckCount rejects an element count the remaining bytes cannot satisfy,
// the guard that stops an NSPI NDR array from allocating gigabytes off a short
// buffer.
func TestPullCheckCount(t *testing.T) {
	p := NewPull([]byte{1, 2, 3, 4}) // 4 bytes remaining: one 4-byte element fits
	if err := p.CheckCount(1); err != nil {
		t.Errorf("count 1 over 4 bytes = %v, want nil", err)
	}
	if err := p.CheckCount(2); err != ErrUnderflow {
		t.Errorf("count 2 over 4 bytes = %v, want ErrUnderflow", err)
	}
	if err := p.CheckCount(^uint32(0)); err != ErrUnderflow {
		t.Errorf("max count over 4 bytes = %v, want ErrUnderflow", err)
	}
}
