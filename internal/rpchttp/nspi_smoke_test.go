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
	defer outResp.Body.Close()
	_, err = readPDU(outResp.Body) // CONN/A3
	mustNoErr(t, err, "read CONN/A3")

	pr, pw := io.Pipe()
	inDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(mustReq(t, "RPC_IN_DATA", url, pr))
		if resp != nil {
			resp.Body.Close()
		}
		inDone <- err
	}()
	_, _ = pw.Write(connB1())
	_, err = readPDU(outResp.Body) // CONN/C2
	mustNoErr(t, err, "read CONN/C2")

	// Bind the NSPI interface (UUID f5cc5a18…, version 56).
	_, _ = pw.Write(buildBindPDU(0x30, nspi.RPCInterfaceUUID, nspi.RPCInterfaceVersion, 0))
	wantPDUType(t, readReply(t, outResp.Body, "bind_ack"), ndr.PktBindAck, "bind reply")

	// NspiBind (opnum 0): flags + STAT + an [in,out] server-GUID pointer.
	bindStub := ndr.NewPush()
	bindStub.Uint32(0) // flags
	pushNspiStat(bindStub, 1252, 0)
	bindStub.Uint32(0x00020000) // server GUID referent (non-null)
	bindStub.Raw(make([]byte, 16))
	_, _ = pw.Write(buildRequestPDU(0x31, 0, 0, bindStub.Bytes(), ndr.PfcFirstFrag|ndr.PfcLastFrag))
	bindResp := readReply(t, outResp.Body, "NspiBind response")
	wantPDUType(t, bindResp, ndr.PktResponse, "NspiBind reply")
	bp := ndr.NewPull(responseStub(t, bindResp))
	_, _ = bp.Uint32() // server GUID referent
	_, _ = bp.Raw(16)  // server GUID flat bytes
	handle, err := pullCtxHandle(bp)
	mustNoErr(t, err, "pull the NspiBind handle")
	bindResult, _ := bp.Uint32()
	wantEq(t, bindResult, uint32(ecSuccess), "NspiBind result")
	wantTrue(t, handle.GUID != (mapi.GUID{}), "the NspiBind reply carries a handle")

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
	_, _ = pw.Write(buildRequestPDU(0x32, 0, 3, qrStub.Bytes(), ndr.PfcFirstFrag|ndr.PfcLastFrag))
	qrResp := readReply(t, outResp.Body, "NspiQueryRows response")
	wantPDUType(t, qrResp, ndr.PktResponse, "NspiQueryRows reply")
	qstub := responseStub(t, qrResp)
	wantEq(t, binary.LittleEndian.Uint32(qstub[len(qstub)-4:]), uint32(ecSuccess), "NspiQueryRows result")

	_ = pw.Close()
	<-inDone
}
