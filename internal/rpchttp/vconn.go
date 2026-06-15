package rpchttp

import (
	"strings"
	"sync"

	"hermex/internal/mapi"
)

// Session is the per-virtual-connection state shared across the connection-
// oriented PDUs that arrive on the IN channel. The transport fills User and
// Mailbox from the HTTP authentication; the bind/dispatch engine attaches the
// presentation-context bindings and request-reassembly state. A session's PDUs
// are processed sequentially by the single IN-channel goroutine, so this state
// needs no locking.
type Session struct {
	User    string
	Mailbox string

	contexts   map[uint16]*registeredIface // context id -> bound interface
	assocGroup uint32
	maxFrag    int               // negotiated client max_recv_frag (response chunk bound)
	reasm      map[uint32][]byte // call id -> accumulated request stub (fragmented requests)
}

// vconn is one RPC-over-HTTP virtual connection: the rendezvous between the
// long-lived RPC_IN_DATA request (client→server PDUs) and the RPC_OUT_DATA
// request (server→client PDUs). The two HTTP requests find each other through
// the server's table by the connection cookie; out carries the PDUs the OUT
// channel streams back.
type vconn struct {
	key  string
	sess *Session

	out    chan []byte   // PDUs queued for the OUT channel to stream
	closed chan struct{} // closed once on teardown; unblocks both channels
	once   sync.Once

	mu         sync.Mutex
	inReady    bool
	outReady   bool
	c2Sent     bool
	windowSize uint32 // the OUT channel's receive window (from CONN/A1)
}

// markReady records that the named channel ("in" or "out") has attached and
// reports whether this call completed the pair (and CONN/C2 has not yet been
// queued) — i.e. whether the caller should now emit CONN/C2.
func (vc *vconn) markReady(kind string) bool {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if kind == "in" {
		vc.inReady = true
	} else {
		vc.outReady = true
	}
	if vc.inReady && vc.outReady && !vc.c2Sent {
		vc.c2Sent = true
		return true
	}
	return false
}

// send queues a PDU for the OUT channel, abandoning it if the connection has
// already torn down (so a producer never blocks on a dead channel).
func (vc *vconn) send(pdu []byte) {
	select {
	case vc.out <- pdu:
	case <-vc.closed:
	}
}

// close tears the virtual connection down exactly once, unblocking both
// channels' loops.
func (vc *vconn) close() {
	vc.once.Do(func() { close(vc.closed) })
}

// vconnKey is the table key both channels rendezvous on: the connection cookie,
// the proxied port, and the host, lowercased (matching the reference's join key).
func vconnKey(connCookie mapi.GUID, host, port string) string {
	return strings.ToLower(connCookie.String() + ":" + port + ":" + host)
}

// getOrCreate returns the virtual connection for key, creating it (with a fresh
// Session seeded from the authenticated identity) on first use.
func (s *Server) getOrCreate(key, user, mailbox string) *vconn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if vc, ok := s.conns[key]; ok {
		return vc
	}
	vc := &vconn{
		key:    key,
		sess:   &Session{User: user, Mailbox: mailbox},
		out:    make(chan []byte, 16),
		closed: make(chan struct{}),
	}
	s.conns[key] = vc
	return vc
}

// teardown closes the virtual connection and removes it from the table.
func (s *Server) teardown(key string) {
	s.mu.Lock()
	vc, ok := s.conns[key]
	if ok {
		delete(s.conns, key)
	}
	s.mu.Unlock()
	if ok {
		vc.close()
	}
}
