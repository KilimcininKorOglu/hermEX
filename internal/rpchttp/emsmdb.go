package rpchttp

import (
	"sync"
	"sync/atomic"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/ndr"
	"hermex/internal/oxmapihttp"
	"hermex/internal/rop"
)

// EMSMDB interface identity ([MS-OXCRPC] 1.9): the store/ROP RPC interface.
var (
	// EMSMDBUUID is the EMSMDB interface UUID a4f1db00-ca47-1067-b31f-00dd010662da.
	EMSMDBUUID = mapi.GUID{
		Data1: 0xA4F1DB00, Data2: 0xCA47, Data3: 0x1067,
		Data4: [8]byte{0xB3, 0x1F, 0x00, 0xDD, 0x01, 0x06, 0x62, 0xDA},
	}
	// EMSMDBVersion is the interface version 0.81 (0x00510000).
	EMSMDBVersion uint32 = 0x00510000
)

// EMSMDB opnums dispatched by the interface ([MS-OXCRPC] 3.1.4).
const (
	opEcDoDisconnect uint16 = 1
	opEcDoConnectEx  uint16 = 10
	opEcDoRpcExt2    uint16 = 11
)

// MAPI result codes returned in the RPC result field.
const (
	ecSuccess        uint32 = 0
	ecResponseTooBig uint32 = 0x0000047D // the ROP response exceeds the offered buffer
)

// maxROPBuffer bounds the ROP response buffer the EcDoRpcExt2 codec can frame; it
// matches the oxmapihttp envelope's 32 KiB cap (its SizeActual is a uint16). A
// larger response is reported as ecResponseTooBig rather than silently truncated.
const maxROPBuffer = 0x8000

// serverVersion is the 3×uint16 server version reported in EcDoConnectEx
// ([MS-OXCRPC] 2.2.2.2.2). A plausible modern value; a real client only acts on
// it for feature negotiation, which a real-Outlook session would exercise.
var serverVersion = [3]uint16{0x000F, 0x0000, 0x0000}

// ContextHandle is the 20-byte DCE/RPC context handle ([C706]): a 4-byte
// attributes word plus a 16-byte GUID. EcDoConnectEx returns one to key the
// session; EcDoRpcExt2 and EcDoDisconnect carry it back.
type ContextHandle struct {
	HandleType uint32
	GUID       mapi.GUID
}

// emsmdbSession is the per-logon state keyed by the context handle: the
// authenticated identity, the connect code page, and the ROP object/handle table
// (the same session type the MAPI/HTTP Execute path drives). The ROP store opens
// lazily when the client's first EcDoRpcExt2 carries a RopLogon.
type emsmdbSession struct {
	user    string
	mailbox string
	cpid    uint32
	rop     *rop.Session
}

// EMSMDB is the EMSMDB RPC interface stub: it manages logon sessions keyed by
// the context handle and routes the connect/disconnect opnums. Its Handle method
// is registered on a Dispatcher.
type EMSMDB struct {
	accounts directory.Accounts

	mu       sync.Mutex
	sessions map[mapi.GUID]*emsmdbSession
	seq      atomic.Uint32
}

// NewEMSMDB returns an EMSMDB stub backed by the directory (used to open the ROP
// store once EcDoRpcExt2 lands).
func NewEMSMDB(accounts directory.Accounts) *EMSMDB {
	return &EMSMDB{accounts: accounts, sessions: make(map[mapi.GUID]*emsmdbSession)}
}

// Handle is the IfaceHandler the dispatcher calls for an EMSMDB request.
func (e *EMSMDB) Handle(sess *Session, opnum uint16, stub []byte) ([]byte, uint32) {
	switch opnum {
	case opEcDoConnectEx:
		return e.connectEx(sess, stub)
	case opEcDoRpcExt2:
		return e.rpcExt2(stub)
	case opEcDoDisconnect:
		return e.disconnect(stub)
	default:
		return nil, ndr.FaultOpRngError
	}
}

// rpcExt2 handles EcDoRpcExt2 (opnum 11): it unwraps the NDR parameters, runs
// the carried ROP buffer through the same decode/dispatch/encode path the
// MAPI/HTTP Execute uses (byte-for-byte the same buffer), and re-wraps the
// response. A response that overflows the codec or the client's buffer is
// reported as ecResponseTooBig rather than truncated.
func (e *EMSMDB) rpcExt2(stub []byte) ([]byte, uint32) {
	in, err := pullRpcExt2(stub)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	s, ok := e.lookup(in.cxh.GUID)
	if !ok {
		return nil, ndr.FaultContextMismatch
	}
	if len(in.pin) == 0 {
		return pushRpcExt2Out(in.cxh, oxmapihttp.EncodeExecute(nil, nil), ecSuccess), 0
	}
	reqRops, reqHandles, derr := oxmapihttp.DecodeExecute(in.pin)
	if derr != nil {
		return nil, ndr.FaultNdr
	}
	respRops, respHandles := s.rop.Dispatch(reqRops, reqHandles)
	if ropBufferSize(respRops, respHandles) > maxROPBuffer || ropBufferSize(respRops, respHandles)+8 > int(in.cbOut) {
		return pushRpcExt2Out(in.cxh, nil, ecResponseTooBig), 0
	}
	return pushRpcExt2Out(in.cxh, oxmapihttp.EncodeExecute(respRops, respHandles), ecSuccess), 0
}

// ropBufferSize is the wire size of the ROP region: RopSize + the ROP commands +
// the server-object handle table.
func ropBufferSize(rops []byte, handles []uint32) int {
	return 2 + len(rops) + 4*len(handles)
}

// mintHandle allocates a fresh context handle. The GUID is a per-session counter
// (unique for the server's lifetime); the client treats it as opaque.
func (e *EMSMDB) mintHandle() ContextHandle {
	n := e.seq.Add(1)
	return ContextHandle{GUID: mapi.GUID{Data1: n, Data4: [8]byte{'h', 'e', 'r', 'm', 'e', 'x', 0, 0}}}
}

// connectEx handles EcDoConnectEx (opnum 10): it validates the request, mints a
// context handle, records the session, and returns the connect response carrying
// the handle and the user's display name.
func (e *EMSMDB) connectEx(sess *Session, stub []byte) ([]byte, uint32) {
	in, err := pullConnectEx(stub)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	cxh := e.mintHandle()
	e.mu.Lock()
	e.sessions[cxh.GUID] = &emsmdbSession{
		user:    sess.User,
		mailbox: sess.Mailbox,
		cpid:    in.cpid,
		rop:     rop.NewSession(sess.Mailbox, e.accounts, sess.User),
	}
	e.mu.Unlock()
	return pushConnectExOut(cxh, sess.User, ecSuccess), 0
}

// disconnect handles EcDoDisconnect (opnum 1): it frees the session and returns
// a zeroed context handle.
func (e *EMSMDB) disconnect(stub []byte) ([]byte, uint32) {
	h, err := pullCtxHandle(ndr.NewPull(stub))
	if err != nil {
		return nil, ndr.FaultNdr
	}
	e.mu.Lock()
	if s, ok := e.sessions[h.GUID]; ok {
		s.rop.Close()
		delete(e.sessions, h.GUID)
	}
	e.mu.Unlock()
	out := ndr.NewPush()
	pushCtxHandle(out, ContextHandle{}) // zeroed on disconnect
	out.Uint32(ecSuccess)
	return out.Bytes(), 0
}

// lookup returns the session for a context handle, if live.
func (e *EMSMDB) lookup(g mapi.GUID) (*emsmdbSession, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[g]
	return s, ok
}

// pushCtxHandle marshals a context handle (attributes word + GUID, 4-aligned).
func pushCtxHandle(p *ndr.Push, h ContextHandle) {
	p.Uint32(h.HandleType)
	p.GUID(h.GUID)
}

// pullCtxHandle reads a context handle.
func pullCtxHandle(p *ndr.Pull) (ContextHandle, error) {
	var h ContextHandle
	var err error
	if h.HandleType, err = p.Uint32(); err != nil {
		return h, err
	}
	h.GUID, err = p.GUID()
	return h, err
}

// connectExIn is the decoded EcDoConnectEx request ([MS-OXCRPC] 2.2.2.2.1).
type connectExIn struct {
	userDN     string
	flags      uint32
	cpid       uint32
	clientVers [3]uint16
	timestamp  uint32
}

// pullConnectEx decodes the EcDoConnectEx request stub: the conformant-varying
// szUserDN string, the scalar parameters, and the (discarded) AUX-in buffer.
func pullConnectEx(stub []byte) (*connectExIn, error) {
	p := ndr.NewPull(stub)
	size, err := p.Uint32() // userdn max_count
	if err != nil {
		return nil, err
	}
	offset, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	length, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if offset != 0 || length > size || length > 1024 {
		return nil, ndr.ErrFormat
	}
	dn, err := p.Raw(int(length))
	if err != nil {
		return nil, err
	}
	r := &connectExIn{userDN: trimNUL(dn)}
	if r.flags, err = p.Uint32(); err != nil {
		return nil, err
	}
	for _, f := range []*uint32{new(uint32), new(uint32)} { // conmod, limit
		if *f, err = p.Uint32(); err != nil {
			return nil, err
		}
	}
	if r.cpid, err = p.Uint32(); err != nil {
		return nil, err
	}
	for range 3 { // lcid_string, lcid_sort, cxr_link
		if _, err = p.Uint32(); err != nil {
			return nil, err
		}
	}
	if _, err = p.Uint16(); err != nil { // cnvt_cps
		return nil, err
	}
	for i := range r.clientVers {
		if r.clientVers[i], err = p.Uint16(); err != nil {
			return nil, err
		}
	}
	if r.timestamp, err = p.Uint32(); err != nil {
		return nil, err
	}
	// AUX-in: conformant byte array + redundant length (discarded for v1).
	auxSize, err := p.Uint32()
	if err != nil {
		return nil, err
	}
	if _, err = p.Raw(int(auxSize)); err != nil {
		return nil, err
	}
	return r, nil // cb_auxin / cb_auxout follow but are not needed
}

// pushConnectExOut marshals the EcDoConnectEx response ([MS-OXCRPC] 2.2.2.2.2):
// the context handle, the polling parameters, the DN-prefix and display-name
// strings, the server/best versions, an empty AUX buffer, and the result.
func pushConnectExOut(cxh ContextHandle, displayName string, result uint32) []byte {
	p := ndr.NewPush()
	pushCtxHandle(p, cxh)
	p.Uint32(60000) // max_polls (ms)
	p.Uint32(60)    // max_retry
	p.Uint32(10)    // retry_delay
	p.Uint16(0)     // cxr
	pushConfString(p, "")
	pushConfString(p, displayName)
	for _, v := range serverVersion {
		p.Uint16(v)
	}
	for _, v := range serverVersion {
		p.Uint16(v)
	}
	p.Uint32(0)      // timestamp
	p.Uint32(0)      // AUX max_count
	p.Uint32(0)      // AUX offset
	p.Uint32(0)      // AUX actual_count
	p.Uint32(0)      // cb_auxout (redundant)
	p.Uint32(result) // err32
	return p.Bytes()
}

// pushConfString marshals a non-null unique-pointer conformant-varying string:
// the referent id, then max_count/offset/actual_count and the NUL-terminated
// bytes ([MS-OXCRPC] uses this for the DN-prefix and display-name fields).
func pushConfString(p *ndr.Push, s string) {
	p.UniquePtr(true)
	b := append([]byte(s), 0)
	n := uint32(len(b))
	p.Uint32(n) // max_count
	p.Uint32(0) // offset
	p.Uint32(n) // actual_count
	p.Raw(b)
}

// trimNUL returns the bytes up to the first NUL as a string.
func trimNUL(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// rpcExt2In is the decoded EcDoRpcExt2 request ([MS-OXCRPC] 2.2.2.3): the context
// handle, flags, the opaque ROP buffer, and the client's output-buffer size.
type rpcExt2In struct {
	cxh   ContextHandle
	flags uint32
	pin   []byte
	cbOut uint32
}

// pullRpcExt2 decodes the EcDoRpcExt2 request stub. The ROP buffer (pin) is a
// conformant byte array carried verbatim; the trailing AUX-in buffer and the
// redundant length words are read past but unused.
func pullRpcExt2(stub []byte) (*rpcExt2In, error) {
	p := ndr.NewPull(stub)
	r := &rpcExt2In{}
	var err error
	if r.cxh, err = pullCtxHandle(p); err != nil {
		return nil, err
	}
	if r.flags, err = p.Uint32(); err != nil {
		return nil, err
	}
	size, err := p.Uint32() // pin max_count
	if err != nil {
		return nil, err
	}
	if r.pin, err = p.Raw(int(size)); err != nil {
		return nil, err
	}
	cbIn, err := p.Uint32() // cb_in
	if err != nil {
		return nil, err
	}
	if cbIn != size {
		return nil, ndr.ErrFormat
	}
	if r.cbOut, err = p.Uint32(); err != nil {
		return nil, err
	}
	auxSize, err := p.Uint32() // AUX-in max_count
	if err != nil {
		return nil, err
	}
	if _, err = p.Raw(int(auxSize)); err != nil {
		return nil, err
	}
	return r, nil // cb_auxin / cb_auxout follow but are not needed
}

// pushRpcExt2Out marshals the EcDoRpcExt2 response ([MS-OXCRPC] 2.2.2.3): the
// echoed context handle, flags, the conformant ROP response buffer, an empty
// AUX-out buffer, the transfer time, and the result.
func pushRpcExt2Out(cxh ContextHandle, pout []byte, result uint32) []byte {
	p := ndr.NewPush()
	pushCtxHandle(p, cxh)
	p.Uint32(0) // flags
	n := uint32(len(pout))
	p.Uint32(n) // cb_out max_count
	p.Uint32(0) // offset
	p.Uint32(n) // actual_count
	p.Raw(pout)
	p.Uint32(n)      // cb_out (redundant)
	p.Uint32(0)      // AUX max_count
	p.Uint32(0)      // AUX offset
	p.Uint32(0)      // AUX actual_count
	p.Uint32(0)      // cb_auxout (redundant)
	p.Uint32(0)      // trans_time
	p.Uint32(result) // err32
	return p.Bytes()
}
