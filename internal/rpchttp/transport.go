// Package rpchttp implements the RPC-over-HTTP ("Outlook Anywhere", [MS-RPCH])
// transport: the RTS tunnelling handshake over the RPC_IN_DATA / RPC_OUT_DATA
// HTTP method pair, and the virtual-connection rendezvous that splices a single
// logical DCE/RPC connection across the two long-lived requests. The IN request
// streams client PDUs in its body; the OUT request streams server PDUs back as a
// chunked response. Connection-oriented PDUs (bind, request) are handed to a
// Dispatch callback (wired by a later increment); this layer owns only the
// channel plumbing.
package rpchttp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"hermex/internal/ndr"
	"hermex/internal/serve"
)

// Authenticator authenticates an HTTP request (Basic), writing the challenge
// itself on failure, and returns the authenticated user and mailbox path.
type Authenticator func(w http.ResponseWriter, r *http.Request) (user, mailbox string, ok bool)

// Dispatch handles one connection-oriented PDU arriving on the IN channel and
// returns the PDUs to stream back on the OUT channel. A nil Dispatch drops CO
// PDUs (the transport-only configuration).
type Dispatch func(sess *Session, pdu []byte) [][]byte

// Config wires the transport to its authentication and PDU dispatch.
type Config struct {
	Auth     Authenticator
	Dispatch Dispatch
}

// Server serves the RPC-over-HTTP endpoint, holding the table of in-flight
// virtual connections.
type Server struct {
	cfg   Config
	mu    sync.Mutex
	conns map[string]*vconn
}

// NewServer returns a Server using the given configuration.
func NewServer(cfg Config) *Server {
	return &Server{cfg: cfg, conns: make(map[string]*vconn)}
}

// errMalformedPDU reports a truncated or inconsistent PDU off the wire.
var errMalformedPDU = errors.New("rpchttp: malformed PDU")

// ServeHTTP authenticates then routes by the RPC-over-HTTP method. It is mounted
// at the rpcproxy paths; the query string carries the proxied "host:port".
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, mailbox, ok := s.cfg.Auth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case "RPC_IN_DATA":
		s.serveIn(w, r, user, mailbox)
	case "RPC_OUT_DATA":
		s.serveOut(w, r, user, mailbox)
	default:
		w.Header().Set("Allow", "RPC_IN_DATA, RPC_OUT_DATA")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveOut handles the long-lived RPC_OUT_DATA request: it consumes the opening
// CONN/A1, replies CONN/A3, joins (or starts) the virtual connection, and then
// streams every queued PDU back as a chunked response until the connection tears
// down.
func (s *Server) serveOut(w http.ResponseWriter, r *http.Request, user, mailbox string) {
	open, ok := s.openChannel(w, r, user, mailbox, "CONN/A1")
	if !ok {
		return
	}
	open.vc.mu.Lock()
	open.vc.windowSize = receiveWindowSize(open.cmds)
	window := open.vc.windowSize
	open.vc.mu.Unlock()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/rpc")
	w.WriteHeader(http.StatusOK)

	// CONN/A3 acknowledges the OUT channel immediately.
	if _, err := w.Write(buildConnA3(open.hdr.CallID)); err != nil {
		s.teardown(open.key)
		return
	}
	flusher.Flush()

	if open.vc.markReady("out") {
		open.vc.send(buildConnC2(open.hdr.CallID, window))
	}
	s.streamOut(w, r, flusher, open)
}

// streamOut writes every queued PDU back on the OUT channel until the request
// context ends or the virtual connection tears down.
func (s *Server) streamOut(w http.ResponseWriter, r *http.Request, flusher http.Flusher, open channelOpen) {
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			s.teardown(open.key)
			return
		case <-open.vc.closed:
			return
		case b := <-open.vc.out:
			if _, err := w.Write(b); err != nil {
				s.teardown(open.key)
				return
			}
			flusher.Flush()
		}
	}
}

// channelOpen is the virtual connection an opening CONN PDU joined, plus what
// that PDU carried.
type channelOpen struct {
	key  string
	vc   *vconn
	hdr  ndr.Header
	cmds []rtsCommand
}

// openChannel consumes the opening CONN PDU a channel begins with (CONN/A1 on the
// OUT channel, CONN/B1 on the IN channel) and joins or starts the virtual
// connection it names. On a refusal the response is already written.
func (s *Server) openChannel(w http.ResponseWriter, r *http.Request, user, mailbox, what string) (channelOpen, bool) {
	var open channelOpen
	host, port, ok := parseProxyURL(r)
	if !ok {
		http.Error(w, "bad rpcproxy url", http.StatusBadRequest)
		return open, false
	}
	pdu, err := readPDU(r.Body)
	if err != nil {
		http.Error(w, "bad "+what, http.StatusBadRequest)
		return open, false
	}
	if open.hdr, err = ndr.ParseHeader(pdu); err != nil {
		http.Error(w, "bad "+what+" header", http.StatusBadRequest)
		return open, false
	}
	if _, open.cmds, err = parseRTS(pdu); err != nil {
		http.Error(w, "bad "+what+" rts", http.StatusBadRequest)
		return open, false
	}
	ck := cookies(open.cmds)
	if len(ck) < 1 {
		http.Error(w, what+" missing cookie", http.StatusBadRequest)
		return open, false
	}
	open.key = vconnKey(ck[0], host, port)
	open.vc = s.getOrCreate(open.key, user, mailbox, serve.ClientAddr(r))
	if open.vc == nil {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return open, false
	}
	return open, true
}

// serveIn handles the long-lived RPC_IN_DATA request: it consumes the opening
// CONN/B1, joins the virtual connection (emitting CONN/C2 once both channels are
// present), then reads PDUs from the request body, forwarding connection-
// oriented PDUs to the dispatch and queueing any replies on the OUT channel.
func (s *Server) serveIn(w http.ResponseWriter, r *http.Request, user, mailbox string) {
	open, ok := s.openChannel(w, r, user, mailbox, "CONN/B1")
	if !ok {
		return
	}
	if open.vc.markReady("in") {
		open.vc.mu.Lock()
		window := open.vc.windowSize
		open.vc.mu.Unlock()
		open.vc.send(buildConnC2(open.hdr.CallID, window))
	}

	s.readIn(r, open.vc)
	s.teardown(open.key)

	w.Header().Set("Content-Type", "application/rpc")
	w.WriteHeader(http.StatusOK)
}

// readIn reads PDUs off the IN channel until the client closes it or the request
// context ends, forwarding connection-oriented PDUs to the dispatch and queueing
// any replies on the OUT channel.
func (s *Server) readIn(r *http.Request, vc *vconn) {
	ctx := r.Context()
	for ctx.Err() == nil {
		pdu, err := readPDU(r.Body)
		if err != nil {
			return // EOF or the client closed the IN channel
		}
		ph, err := ndr.ParseHeader(pdu)
		if err != nil {
			return
		}
		if ph.Type == ndr.PktRTS {
			continue // keep-alive / flow-control: nothing to do for v1
		}
		if s.cfg.Dispatch == nil {
			continue
		}
		for _, reply := range s.cfg.Dispatch(vc.sess, pdu) {
			vc.send(reply)
		}
	}
}

// parseProxyURL extracts the proxied "host:port" from the rpcproxy query string
// (e.g. /rpc/rpcproxy.dll?mail.example.com:6001). The host:port is a virtual-
// connection correlation token, never a socket to dial.
func parseProxyURL(r *http.Request) (host, port string, ok bool) {
	q := r.URL.RawQuery
	i := strings.LastIndex(q, ":")
	if i <= 0 || i == len(q)-1 {
		return "", "", false
	}
	return q[:i], q[i+1:], true
}

// readPDU reads one connection-oriented PDU off a stream: the 16-byte NCACN
// header gives frag_length, then the remaining frag_length-16 bytes follow.
func readPDU(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 16)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	h, err := ndr.ParseHeader(hdr)
	if err != nil {
		return nil, err
	}
	if h.FragLen < 16 {
		return nil, errMalformedPDU
	}
	out := make([]byte, h.FragLen)
	copy(out, hdr)
	if _, err := io.ReadFull(r, out[16:]); err != nil {
		return nil, err
	}
	return out, nil
}
