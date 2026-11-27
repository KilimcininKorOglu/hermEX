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
// dispatch engine, and the NSPI stub compose end-to-end against a seeded GAL,
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
	disp.Register(nspi.RPCInterfaceUUID, nspi.RPCInterfaceVersion, func(sess *Session, opnum uint16, stub []byte) ([]byte, uint32) {
		user := ""
		if sess != nil {
			user = sess.User
		}
		return nsp.DispatchRPC(opnum, stub, user)
	})
	srv := httptest.NewServer(NewServer(Config{Auth: okAuth, Dispatch: disp.Dispatch}))
	defer srv.Close()
	url := srv.URL + "/rpc/rpcproxy.dll?testhost:6004"

	// RTS handshake: OUT channel opens (CONN/A3), IN channel joins (CONN/C2).
	outResp, err := http.DefaultClient.Do(mustReq(t, "RPC_OUT_DATA", url, bytes.NewReader(connA1())))
	mustNoErr(t, err, "OUT request")
	defer func() {
		if cerr := outResp.Body.Close(); cerr != nil {
			t.Errorf("close OUT body: %v", cerr)
		}
	}()
	wantEq(t, outResp.StatusCode, http.StatusOK, "OUT status")
	_, err = readPDU(outResp.Body) // CONN/A3
	mustNoErr(t, err, "read CONN/A3")

	pr, pw := io.Pipe()
	inDone := make(chan error, 1)
	go func() {
		resp, doErr := http.DefaultClient.Do(mustReq(t, "RPC_IN_DATA", url, pr))
		if resp != nil {
			if cerr := resp.Body.Close(); cerr != nil && doErr == nil {
				doErr = cerr
			}
		}
		inDone <- doErr
	}()
	mustWrite(t, pw, connB1(), "CONN/B1")
	_, err = readPDU(outResp.Body) // CONN/C2
	mustNoErr(t, err, "read CONN/C2")

	// Bind the NSPI interface (UUID f5cc5a18..., version 56).
	mustWrite(t, pw, buildBindPDU(0x30, nspi.RPCInterfaceUUID, nspi.RPCInterfaceVersion, 0), "the bind")
	wantPDUType(t, readReply(t, outResp.Body, "bind_ack"), ndr.PktBindAck, "bind reply")

	// NspiBind (opnum 0): flags + STAT + an [in,out] server-GUID pointer.
	bindStub := ndr.NewPush()
	bindStub.Uint32(0) // flags
	pushNspiStat(bindStub, 1252, 0)
	bindStub.Uint32(0x00020000) // server GUID referent (non-null)
	bindStub.Raw(make([]byte, 16))
	mustWrite(t, pw, buildRequestPDU(0x31, 0, 0, bindStub.Bytes(), ndr.PfcFirstFrag|ndr.PfcLastFrag), "NspiBind")
	bindResp := readReply(t, outResp.Body, "NspiBind response")
	wantPDUType(t, bindResp, ndr.PktResponse, "NspiBind reply")
	bp := ndr.NewPull(responseStub(t, bindResp))
	mustU32(t, bp, "server GUID referent")
	gotGUID, err := bp.GUID()
	mustNoErr(t, err, "read server GUID")
	wantEq(t, gotGUID, mapi.GUID{Data1: 0xABCD1234}, "server GUID")
	handle, err := pullCtxHandle(bp)
	mustNoErr(t, err, "pull the NspiBind handle")
	bindResult := mustU32(t, bp, "NspiBind result")
	wantEq(t, bindResult, uint32(ecSuccess), "NspiBind result")
	wantTrue(t, handle.GUID != (mapi.GUID{}), "the NspiBind reply carries a handle")

	// An unknown NSPI opnum (99) must fault without disturbing the live session.
	mustWrite(t, pw, buildRequestPDU(0x32, 0, 99, nil, ndr.PfcFirstFrag|ndr.PfcLastFrag), "unknown NSPI opnum")
	wantPDUType(t, readReply(t, outResp.Body, "unknown NSPI opnum response"), ndr.PktFault, "unknown NSPI opnum reply")

	// NspiQueryRows (opnum 3) with the live bound handle still succeeds.
	mustWrite(t, pw, buildRequestPDU(0x33, 0, 3, buildNspiQueryRowsStub(handle), ndr.PfcFirstFrag|ndr.PfcLastFrag), "NspiQueryRows")
	qrResp := readReply(t, outResp.Body, "NspiQueryRows response")
	wantPDUType(t, qrResp, ndr.PktResponse, "NspiQueryRows reply")
	qstub := responseStub(t, qrResp)
	wantEq(t, binary.LittleEndian.Uint32(qstub[len(qstub)-4:]), uint32(ecSuccess), "NspiQueryRows result")

	mustNoErr(t, pw.Close(), "close IN pipe")
	if err := <-inDone; err != nil {
		t.Errorf("RPC_IN_DATA request: %v", err)
	}
}

// buildNspiQueryRowsStub builds an NspiQueryRows (opnum 3) request stub for handle.
func buildNspiQueryRowsStub(handle ContextHandle) []byte {
	qrStub := ndr.NewPush()
	pushCtxHandle(qrStub, handle)
	qrStub.Uint32(0) // flags
	pushNspiStat(qrStub, 1252, 0)
	qrStub.Uint32(0)  // inline MID count
	qrStub.Uint32(0)  // null MID referent
	qrStub.Uint32(10) // requested rows
	qrStub.Uint32(0)  // null column referent
	return qrStub.Bytes()
}
