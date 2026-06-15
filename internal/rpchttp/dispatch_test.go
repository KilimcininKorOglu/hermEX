package rpchttp

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// dummyUUID is an arbitrary interface id used to exercise the dispatch engine
// without depending on the real EMSMDB/NSPI interfaces.
var dummyUUID = mapi.GUID{Data1: 0x12345678, Data2: 0x9ABC, Data3: 0xDEF0, Data4: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}

// echoDispatcher returns a Dispatcher with dummyUUID registered to a handler
// that echoes the request stub prefixed with the opnum (so routing is testable).
func echoDispatcher() *Dispatcher {
	d := NewDispatcher()
	d.Register(dummyUUID, 1, func(sess *Session, opnum uint16, stub []byte) ([]byte, uint32) {
		out := []byte{byte(opnum), byte(opnum >> 8)}
		return append(out, stub...), 0
	})
	return d
}

// buildBindPDU hand-assembles a BIND PDU offering one presentation context
// (abstract syntax uuid/version over the NDR transfer syntax). The explicit
// Align(4) before the context matches the ctx-list wire layout.
func buildBindPDU(callID uint32, uuid mapi.GUID, version uint32, ctxID uint16) []byte {
	body := ndr.NewPush()
	body.Uint16(0x16D0) // max_xmit_frag
	body.Uint16(0x16D0) // max_recv_frag
	body.Uint32(0)      // assoc_group_id
	body.Uint8(1)       // num_contexts
	body.Align(4)       // pad before the context list
	body.Uint16(ctxID)  // context_id
	body.Uint8(1)       // num_transfer_syntaxes
	body.GUID(uuid)     // abstract syntax (GUID aligns to 4)
	body.Uint32(version)
	body.GUID(ndr.TransferSyntaxNDR.UUID)
	body.Uint32(ndr.TransferSyntaxNDR.Version)
	return ndr.Frame(ndr.PktBind, ndr.PfcFirstFrag|ndr.PfcLastFrag, callID, body.Bytes())
}

// buildRequestPDU assembles a REQUEST PDU with the given fragment flags.
func buildRequestPDU(callID uint32, ctxID, opnum uint16, stub []byte, flags uint8) []byte {
	body := ndr.NewPush()
	body.Uint32(uint32(len(stub))) // alloc_hint
	body.Uint16(ctxID)
	body.Uint16(opnum)
	body.Align(8) // pad before the stub
	body.Raw(stub)
	return ndr.Frame(ndr.PktRequest, flags, callID, body.Bytes())
}

// assertPayload checks that got is want followed only by up to 3 bytes of NDR
// zero padding (the trailer alignment a RESPONSE PDU carries; real EMSMDB/NSPI
// stubs are 4-aligned so this is usually empty, and the payload is length-
// delimited at a higher layer, so trailing alignment bytes are benign).
func assertPayload(t *testing.T, got, want []byte) {
	t.Helper()
	if !bytes.HasPrefix(got, want) {
		t.Errorf("payload = % x, want prefix % x", got, want)
		return
	}
	for _, b := range got[len(want):] {
		if b != 0 {
			t.Errorf("payload = % x, want % x + zero padding", got, want)
			return
		}
	}
	if len(got)-len(want) > 3 {
		t.Errorf("payload has %d trailing pad bytes, want <=3", len(got)-len(want))
	}
}

// responseStub extracts the stub fragment from a RESPONSE PDU.
func responseStub(t *testing.T, pdu []byte) []byte {
	t.Helper()
	p := ndr.NewPull(pdu)
	if _, err := p.Raw(16); err != nil { // header
		t.Fatal(err)
	}
	p.Uint32() // alloc_hint
	p.Uint16() // context_id
	p.Uint8()  // cancel_count
	p.Align(8)
	return p.Rest()
}

// TestBindAccept proves a bind for a registered interface is accepted: a single
// BIND_ACK with the context accepted over the NDR transfer syntax, a non-zero
// association group, and the context bound on the session.
func TestBindAccept(t *testing.T) {
	d := echoDispatcher()
	sess := &Session{User: "alice@hermex.test"}
	out := d.Dispatch(sess, buildBindPDU(0x10, dummyUUID, 1, 0))
	if len(out) != 1 {
		t.Fatalf("bind produced %d PDUs, want 1", len(out))
	}
	h, err := ndr.ParseHeader(out[0])
	if err != nil || h.Type != ndr.PktBindAck {
		t.Fatalf("bind reply = (%v, type %#x), want BIND_ACK", err, h.Type)
	}
	if sess.contexts[0] == nil {
		t.Error("context 0 was not bound on the session")
	}
	if sess.assocGroup == 0 {
		t.Error("association group not allocated")
	}
}

// TestBindNakUnknownInterface proves a bind for an unregistered interface is
// refused with a BIND_NAK.
func TestBindNakUnknownInterface(t *testing.T) {
	d := echoDispatcher()
	other := mapi.GUID{Data1: 0xDEADBEEF}
	out := d.Dispatch(&Session{}, buildBindPDU(0x10, other, 1, 0))
	h, err := ndr.ParseHeader(out[0])
	if err != nil || h.Type != ndr.PktBindNak {
		t.Errorf("unknown-interface bind reply = (%v, type %#x), want BIND_NAK", err, h.Type)
	}
}

// TestRequestDispatch proves a request after a successful bind reaches the bound
// handler and the handler's output returns as a single RESPONSE.
func TestRequestDispatch(t *testing.T) {
	d := echoDispatcher()
	sess := &Session{}
	d.Dispatch(sess, buildBindPDU(0x10, dummyUUID, 1, 0))

	stub := []byte("hello rpc")
	out := d.Dispatch(sess, buildRequestPDU(0x11, 0, 7, stub, ndr.PfcFirstFrag|ndr.PfcLastFrag))
	if len(out) != 1 {
		t.Fatalf("request produced %d PDUs, want 1", len(out))
	}
	h, _ := ndr.ParseHeader(out[0])
	if h.Type != ndr.PktResponse {
		t.Fatalf("reply type = %#x, want RESPONSE", h.Type)
	}
	got := responseStub(t, out[0])
	want := append([]byte{7, 0}, stub...) // opnum 7 prefix + echoed stub
	assertPayload(t, got, want)
}

// TestRequestUnboundContextFaults proves a request to a context that was never
// bound yields a FAULT.
func TestRequestUnboundContextFaults(t *testing.T) {
	d := echoDispatcher()
	out := d.Dispatch(&Session{}, buildRequestPDU(0x11, 9, 0, nil, ndr.PfcFirstFrag|ndr.PfcLastFrag))
	h, _ := ndr.ParseHeader(out[0])
	if h.Type != ndr.PktFault {
		t.Errorf("unbound-context reply type = %#x, want FAULT", h.Type)
	}
}

// TestResponseFragmentation proves a response larger than the negotiated frag
// size splits into multiple RESPONSE PDUs — FIRST on the first, LAST on the
// last — that reassemble to the full stub.
func TestResponseFragmentation(t *testing.T) {
	d := echoDispatcher()
	sess := &Session{}
	d.Dispatch(sess, buildBindPDU(0x10, dummyUUID, 1, 0))

	// A 12 KiB request echoes back to a >frag (5840) response.
	big := bytes.Repeat([]byte{0xAB}, 12000)
	out := d.Dispatch(sess, buildRequestPDU(0x11, 0, 0, big, ndr.PfcFirstFrag|ndr.PfcLastFrag))
	if len(out) < 2 {
		t.Fatalf("response fragmented into %d PDUs, want >1", len(out))
	}

	var reassembled []byte
	for i, pdu := range out {
		h, _ := ndr.ParseHeader(pdu)
		if h.Type != ndr.PktResponse {
			t.Fatalf("fragment %d type = %#x, want RESPONSE", i, h.Type)
		}
		wantFirst := i == 0
		wantLast := i == len(out)-1
		if (h.Flags&ndr.PfcFirstFrag != 0) != wantFirst {
			t.Errorf("fragment %d FIRST flag = %v, want %v", i, h.Flags&ndr.PfcFirstFrag != 0, wantFirst)
		}
		if (h.Flags&ndr.PfcLastFrag != 0) != wantLast {
			t.Errorf("fragment %d LAST flag = %v, want %v", i, h.Flags&ndr.PfcLastFrag != 0, wantLast)
		}
		if int(h.FragLen) != len(pdu) {
			t.Errorf("fragment %d frag_length = %d, want %d", i, h.FragLen, len(pdu))
		}
		reassembled = append(reassembled, responseStub(t, pdu)...)
	}
	want := append([]byte{0, 0}, big...) // opnum 0 prefix + echo
	assertPayload(t, reassembled, want)
}

// TestRequestReassembly proves a fragmented request (FIRST then LAST) is held
// until the last fragment, then dispatched once with the concatenated stub.
func TestRequestReassembly(t *testing.T) {
	d := echoDispatcher()
	sess := &Session{}
	d.Dispatch(sess, buildBindPDU(0x10, dummyUUID, 1, 0))

	if out := d.Dispatch(sess, buildRequestPDU(0x22, 0, 3, []byte("AAAA"), ndr.PfcFirstFrag)); out != nil {
		t.Fatalf("first fragment produced %d PDUs, want 0 (awaiting more)", len(out))
	}
	out := d.Dispatch(sess, buildRequestPDU(0x22, 0, 3, []byte("BBBB"), ndr.PfcLastFrag))
	if len(out) != 1 {
		t.Fatalf("last fragment produced %d PDUs, want 1", len(out))
	}
	got := responseStub(t, out[0])
	want := append([]byte{3, 0}, []byte("AAAABBBB")...)
	assertPayload(t, got, want)
}
