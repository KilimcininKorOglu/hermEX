package rpchttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSessionCarriesRemoteAddr proves the virtual-connection session is seeded with
// the originating client address, the value the NSPI activity log relies on so a
// per-operation event records the real client (MS-RPCH "always set RemoteAddr").
// It also pins the create-once semantics: the address is fixed when the session is
// first created and a later request on the same connection does not re-seed it.
func TestSessionCarriesRemoteAddr(t *testing.T) {
	s := NewServer(Config{})

	vc := s.getOrCreate("vkey", "alice@hermex.test", "/mb/alice", "203.0.113.7:50000")
	if vc.sess.RemoteAddr != "203.0.113.7:50000" {
		t.Errorf("session RemoteAddr = %q, want the originating client address", vc.sess.RemoteAddr)
	}
	if vc.sess.User != "alice@hermex.test" {
		t.Errorf("session User = %q, want alice@hermex.test", vc.sess.User)
	}
	wantEq(t, vc.sess.Mailbox, "/mb/alice", "session Mailbox")

	// A later request on the same virtual connection returns the same session; the
	// identity and address set at creation stick rather than being overwritten.
	vc2 := s.getOrCreate("vkey", "bob@hermex.test", "/mb/bob", "198.51.100.9:1")
	if vc2 != vc || vc2.sess.RemoteAddr != "203.0.113.7:50000" {
		t.Errorf("re-lookup RemoteAddr = %q, want the original creation address", vc2.sess.RemoteAddr)
	}
	wantEq(t, vc2.sess.User, "alice@hermex.test", "re-lookup User")
	wantEq(t, vc2.sess.Mailbox, "/mb/alice", "re-lookup Mailbox")
}

// TestOpenChannelSeedsRemoteAddrAndRefusesHostile proves openChannel populates
// Session.RemoteAddr from the HTTP request, preserves it when the peer channel
// joins from a different address, and leaves the connection table untouched when
// a malformed request is refused.
func TestOpenChannelSeedsRemoteAddrAndRefusesHostile(t *testing.T) {
	s := NewServer(Config{})

	req := httptest.NewRequest("RPC_OUT_DATA", "/rpc/rpcproxy.dll?mail.hermex.test:6001", bytes.NewReader(connA1()))
	req.RemoteAddr = "203.0.113.7:50000"
	open, ok := s.openChannel(httptest.NewRecorder(), req, "alice@hermex.test", "/mb/alice", "CONN/A1")
	wantTrue(t, ok, "valid openChannel")
	wantEq(t, open.vc.sess.RemoteAddr, "203.0.113.7:50000", "seeded RemoteAddr")

	// Peer channel joining from another address must not overwrite RemoteAddr.
	req2 := httptest.NewRequest("RPC_IN_DATA", "/rpc/rpcproxy.dll?mail.hermex.test:6001", bytes.NewReader(connB1()))
	req2.RemoteAddr = "198.51.100.9:44321"
	open2, ok := s.openChannel(httptest.NewRecorder(), req2, "alice@hermex.test", "/mb/alice", "CONN/B1")
	wantTrue(t, ok, "joining openChannel")
	wantEq(t, open2.vc, open.vc, "joined vconn")
	wantEq(t, open2.vc.sess.RemoteAddr, "203.0.113.7:50000", "preserved RemoteAddr")

	// Hostile request with missing proxy host:port is refused without adding to s.conns.
	badRec := httptest.NewRecorder()
	badReq := httptest.NewRequest("RPC_OUT_DATA", "/rpc/rpcproxy.dll", bytes.NewReader(connA1()))
	badReq.RemoteAddr = "192.0.2.55:9999"
	_, ok = s.openChannel(badRec, badReq, "alice@hermex.test", "/mb/alice", "CONN/A1")
	wantFalse(t, ok, "bad URL openChannel")
	wantEq(t, badRec.Code, http.StatusBadRequest, "bad URL status")

	s.mu.Lock()
	count := len(s.conns)
	s.mu.Unlock()
	wantEq(t, count, 1, "connection table size after refusal")
	wantEq(t, open.vc.sess.RemoteAddr, "203.0.113.7:50000", "existing RemoteAddr after refusal")
}
