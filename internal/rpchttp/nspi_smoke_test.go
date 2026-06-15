package rpchttp

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/ndr"
	"hermex/internal/nspi"
)

// pushNspiStat writes the NSPI STAT block (9 NDR u32 fields, [MS-OXNSPI] 2.2.8).
// Only the code page and current record are meaningful for this driver; the rest
// start the cursor at the table beginning.
func pushNspiStat(p *ndr.Push, codePage, curRec uint32) {
	p.Uint32(0)        // sort_type (display name)
	p.Uint32(0)        // container_id
	p.Uint32(curRec)   // cur_rec
	p.Uint32(0)        // delta
	p.Uint32(0)        // num_pos
	p.Uint32(0)        // total_rec
	p.Uint32(codePage) // codepage
	p.Uint32(0)        // template_locale
	p.Uint32(0)        // sort_locale
}

// TestEndToEndNSPI drives the full RPC/HTTP vertical against the NSPI address-book
// interface: the RTS handshake, a DCE/RPC bind of the NSPI interface, then
// NspiBind and NspiQueryRows over the live transport, proving the transport, the
// dispatch engine, and the NSPI stub compose end-to-end against a seeded GAL —
// through the same Dispatcher.Register adapter internal/mapihttp wires in
// production.
//
// The byte layers are exercised here against this hand-rolled driver, which is
// NOT an independent oracle (it shares this codebase's framing); a real Outlook
// NSPI session is the only true close and is Outlook-PENDING in this environment.
func TestEndToEndNSPI(t *testing.T) {
	accs := directory.StaticAccounts{
		"alice@hermex.test": {Password: "x", MailboxPath: "/mb/alice"},
		"bob@hermex.test":   {Password: "x", MailboxPath: "/mb/bob"},
	}
	nsp := nspi.NewServer(accs, mapi.GUID{Data1: 0xABCD1234})
	disp := NewDispatcher()
	disp.Register(nspi.RPCInterfaceUUID, nspi.RPCInterfaceVersion, func(_ *Session, opnum uint16, stub []byte) ([]byte, uint32) {
		return nsp.DispatchRPC(opnum, stub)
	})
	srv := httptest.NewServer(NewServer(Config{Auth: okAuth, Dispatch: disp.Dispatch}))
	defer srv.Close()
	url := srv.URL + "/rpc/rpcproxy.dll?testhost:6004"

	// RTS handshake: OUT channel opens (CONN/A3), IN channel joins (CONN/C2).
	outResp, err := http.DefaultClient.Do(mustReq(t, "RPC_OUT_DATA", url, bytes.NewReader(connA1())))
	if err != nil {
		t.Fatalf("OUT request: %v", err)
	}
	defer outResp.Body.Close()
	if _, err := readPDU(outResp.Body); err != nil { // CONN/A3
		t.Fatalf("read CONN/A3: %v", err)
	}
	pr, pw := io.Pipe()
	inDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(mustReq(t, "RPC_IN_DATA", url, pr))
		if resp != nil {
			resp.Body.Close()
		}
		inDone <- err
	}()
	pw.Write(connB1())
	if _, err := readPDU(outResp.Body); err != nil { // CONN/C2
		t.Fatalf("read CONN/C2: %v", err)
	}

	// Bind the NSPI interface (UUID f5cc5a18…, version 56).
	pw.Write(buildBindPDU(0x30, nspi.RPCInterfaceUUID, nspi.RPCInterfaceVersion, 0))
	bindAck, err := readPDU(outResp.Body)
	if err != nil {
		t.Fatalf("read bind_ack: %v", err)
	}
	if h, _ := ndr.ParseHeader(bindAck); h.Type != ndr.PktBindAck {
		t.Fatalf("bind reply type = %#x, want BIND_ACK", h.Type)
	}

	// NspiBind (opnum 0): flags + STAT + an [in,out] server-GUID pointer.
	bindStub := ndr.NewPush()
	bindStub.Uint32(0) // flags
	pushNspiStat(bindStub, 1252, 0)
	bindStub.Uint32(0x00020000) // server GUID referent (non-null)
	bindStub.Raw(make([]byte, 16))
	pw.Write(buildRequestPDU(0x31, 0, 0, bindStub.Bytes(), ndr.PfcFirstFrag|ndr.PfcLastFrag))
	bindResp, err := readPDU(outResp.Body)
	if err != nil {
		t.Fatalf("read NspiBind response: %v", err)
	}
	if h, _ := ndr.ParseHeader(bindResp); h.Type != ndr.PktResponse {
		t.Fatalf("NspiBind reply type = %#x, want RESPONSE", h.Type)
	}
	bp := ndr.NewPull(responseStub(t, bindResp))
	bp.Uint32() // server GUID referent
	bp.Raw(16)  // server GUID flat bytes
	handle, err := pullCtxHandle(bp)
	if err != nil {
		t.Fatalf("NspiBind handle: %v", err)
	}
	bindResult, _ := bp.Uint32()
	if bindResult != ecSuccess || handle.GUID == (mapi.GUID{}) {
		t.Fatalf("NspiBind = (result %#x, handle %v), want (0, non-zero)", bindResult, handle.GUID)
	}

	// NspiQueryRows (opnum 3): the bound handle + flags + STAT + an empty inline
	// MID array (so the cursor walks) + the requested count + a null column set.
	qrStub := ndr.NewPush()
	pushCtxHandle(qrStub, handle)
	qrStub.Uint32(0) // flags
	pushNspiStat(qrStub, 1252, 0)
	qrStub.Uint32(0)  // inline MID count
	qrStub.Uint32(0)  // null MID referent
	qrStub.Uint32(10) // requested rows
	qrStub.Uint32(0)  // null column referent
	pw.Write(buildRequestPDU(0x32, 0, 3, qrStub.Bytes(), ndr.PfcFirstFrag|ndr.PfcLastFrag))
	qrResp, err := readPDU(outResp.Body)
	if err != nil {
		t.Fatalf("read NspiQueryRows response: %v", err)
	}
	if h, _ := ndr.ParseHeader(qrResp); h.Type != ndr.PktResponse {
		t.Fatalf("NspiQueryRows reply type = %#x, want RESPONSE", h.Type)
	}
	qstub := responseStub(t, qrResp)
	if qrResult := binary.LittleEndian.Uint32(qstub[len(qstub)-4:]); qrResult != ecSuccess {
		t.Errorf("NspiQueryRows result = %#x, want ecSuccess", qrResult)
	}

	pw.Close()
	<-inDone
}
