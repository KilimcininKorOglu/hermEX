package rpchttp

import (
	"slices"
	"sync/atomic"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// IfaceHandler runs one connection-oriented RPC call: given the bound session,
// the opnum, and the NDR-marshalled request stub, it returns the response stub.
// A non-zero fault status makes the dispatcher emit a FAULT instead of a
// RESPONSE.
type IfaceHandler func(sess *Session, opnum uint16, stub []byte) (out []byte, fault uint32)

// registeredIface binds an abstract syntax (interface UUID + version) to its
// opnum handler.
type registeredIface struct {
	syntax  ndr.SyntaxID
	handler IfaceHandler
}

// ourMaxFrag is the largest PDU fragment this server emits/accepts; the
// negotiated value is min(client, ourMaxFrag). It bounds the response chunk size.
const ourMaxFrag = 0x16D0 // 5840, the classic DCE/RPC default

// Dispatcher is the connection-oriented DCE/RPC engine: it accepts binds against
// a registry of interfaces and routes requests to the bound interface's handler,
// reassembling fragmented requests and fragmenting large responses. Its Dispatch
// method is the transport's per-PDU callback.
type Dispatcher struct {
	ifaces   []registeredIface
	assocSeq atomic.Uint32
}

// NewDispatcher returns an empty Dispatcher; register interfaces before use.
func NewDispatcher() *Dispatcher {
	d := &Dispatcher{}
	d.assocSeq.Store(0x10000)
	return d
}

// Register adds an interface (matched at bind by UUID and version exactly) and
// its opnum handler.
func (d *Dispatcher) Register(uuid mapi.GUID, version uint32, h IfaceHandler) {
	d.ifaces = append(d.ifaces, registeredIface{syntax: ndr.SyntaxID{UUID: uuid, Version: version}, handler: h})
}

// find returns the interface whose abstract syntax matches s exactly, or nil.
func (d *Dispatcher) find(s ndr.SyntaxID) *registeredIface {
	for i := range d.ifaces {
		if d.ifaces[i].syntax == s {
			return &d.ifaces[i]
		}
	}
	return nil
}

// Dispatch handles one inbound connection-oriented PDU and returns the PDUs to
// stream back on the OUT channel. It is wired to the transport's Config.Dispatch.
func (d *Dispatcher) Dispatch(sess *Session, pdu []byte) [][]byte {
	parsed, err := ndr.ParsePDU(pdu)
	if err != nil {
		return nil // unparseable: drop (a malformed peer)
	}
	switch parsed.Header.Type {
	case ndr.PktBind, ndr.PktAlter:
		return d.handleBind(sess, parsed)
	case ndr.PktRequest:
		return d.handleRequest(sess, parsed.Header, parsed.Request)
	default:
		return nil // co_cancel, orphaned, auth3, etc.: no-op for v1
	}
}

// handleBind matches each presentation context against the registry, binds the
// accepted ones to the session, and returns a BIND_ACK (or BIND_NAK when no
// context is acceptable). v1 accepts auth-none binds and ignores any RPC-level
// verifier (HTTP Basic already authenticated; NTLMSSP-in-RPC is deferred).
func (d *Dispatcher) handleBind(sess *Session, pdu *ndr.PDU) [][]byte {
	b := pdu.Bind
	if sess.contexts == nil {
		sess.contexts = make(map[uint16]*registeredIface)
	}
	results := make([]ndr.AckCtx, 0, len(b.Contexts))
	accepted := false
	for _, ctx := range b.Contexts {
		ri := d.find(ctx.AbstractSyntax)
		if ri != nil && offersNDR(ctx.TransferSyntaxes) {
			sess.contexts[ctx.ContextID] = ri
			results = append(results, ndr.AckCtx{Result: ndr.AckResultAccept, Reason: ndr.AckReasonNotSpecified, Syntax: ndr.TransferSyntaxNDR})
			accepted = true
		} else {
			// Reject this context but keep its slot so the client's context ids
			// still line up with the results array.
			results = append(results, ndr.AckCtx{Result: ndr.AckResultProviderReject})
		}
	}
	if !accepted {
		return [][]byte{ndr.FrameBindNak(pdu.Header.CallID, ndr.BindNakReasonNotSpecified)}
	}

	if sess.assocGroup == 0 {
		if b.AssocGroupID != 0 {
			sess.assocGroup = b.AssocGroupID
		} else {
			sess.assocGroup = d.assocSeq.Add(1)
		}
	}
	frag := negotiateFrag(b.MaxXmitFrag, b.MaxRecvFrag)
	sess.maxFrag = frag
	ba := &ndr.BindAck{
		MaxXmitFrag:      uint16(frag),
		MaxRecvFrag:      uint16(frag),
		AssocGroupID:     sess.assocGroup,
		SecondaryAddress: "6001", // the EMSMDB endpoint; vestigial over RPC/HTTP
		Results:          results,
	}
	return [][]byte{ndr.FrameBindAck(pdu.Header.CallID, ba)}
}

// handleRequest reassembles a (possibly fragmented) request, dispatches it to
// the bound interface, and fragments the response to the negotiated frag size.
func (d *Dispatcher) handleRequest(sess *Session, h ndr.Header, req *ndr.Request) [][]byte {
	stub := req.Stub
	first := h.Flags&ndr.PfcFirstFrag != 0
	last := h.Flags&ndr.PfcLastFrag != 0
	if !first || !last {
		if sess.reasm == nil {
			sess.reasm = make(map[uint32][]byte)
		}
		if first {
			sess.reasm[h.CallID] = append([]byte(nil), stub...)
		} else {
			sess.reasm[h.CallID] = append(sess.reasm[h.CallID], stub...)
		}
		if !last {
			return nil // await the remaining fragments
		}
		stub = sess.reasm[h.CallID]
		delete(sess.reasm, h.CallID)
	}

	ri := sess.contexts[req.ContextID]
	if ri == nil {
		return [][]byte{ndr.FrameFault(h.CallID, req.ContextID, ndr.FaultUnkIf)}
	}
	// Stamp the in-flight request's framing so a handler that parks a long-poll can
	// frame its deferred reply to match this call; the IN goroutine is the only writer.
	sess.curCallID = h.CallID
	sess.curContextID = req.ContextID
	out, fault := ri.handler(sess, req.Opnum, stub)
	if fault == faultParked {
		return nil // the handler parked; its reply is delivered later on the OUT channel
	}
	if fault != 0 {
		return [][]byte{ndr.FrameFault(h.CallID, req.ContextID, fault)}
	}
	return fragmentResponse(h.CallID, req.ContextID, out, sess.maxFrag)
}

// faultParked is the sentinel an interface handler returns to signal that it has
// parked the call (e.g. EcDoAsyncWaitEx's long-poll): no reply is framed now; the
// handler delivers its response on the OUT channel when it completes. It is not a
// DCE/RPC NCA fault code — handleRequest intercepts it before any fault framing.
const faultParked uint32 = 0xFFFFFFFF

// offersNDR reports whether the client offered the NDR (v2) transfer syntax.
func offersNDR(syntaxes []ndr.SyntaxID) bool {
	return slices.Contains(syntaxes, ndr.TransferSyntaxNDR)
}

// negotiateFrag picks the fragment size: the smaller of the client's offered
// sizes and our maximum, floored at a safe minimum.
func negotiateFrag(clientXmit, clientRecv uint16) int {
	frag := ourMaxFrag
	if clientXmit != 0 {
		frag = min(frag, int(clientXmit))
	}
	if clientRecv != 0 {
		frag = min(frag, int(clientRecv))
	}
	return max(frag, 256)
}

// fragmentResponse splits the response stub into RESPONSE PDUs no larger than
// the negotiated fragment size, marking the first and last fragments. alloc_hint
// carries the full stub size so the client can pre-size its reassembly buffer.
func fragmentResponse(callID uint32, contextID uint16, stub []byte, maxFrag int) [][]byte {
	total := uint32(len(stub))
	chunk := max(maxFrag-24, 16) // minus the response PDU header overhead
	chunk &^= 15                 // a fragment's stub is a multiple of 16

	if len(stub) == 0 {
		return [][]byte{ndr.FrameResponse(callID, ndr.PfcFirstFrag|ndr.PfcLastFrag, contextID, 0, nil)}
	}
	var pdus [][]byte
	for off := 0; off < len(stub); off += chunk {
		end := min(off+chunk, len(stub))
		var flags uint8
		if off == 0 {
			flags |= ndr.PfcFirstFrag
		}
		if end == len(stub) {
			flags |= ndr.PfcLastFrag
		}
		pdus = append(pdus, ndr.FrameResponse(callID, flags, contextID, total, stub[off:end]))
	}
	return pdus
}
