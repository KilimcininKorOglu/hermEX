package rpchttp

import (
	"strconv"
	"testing"
)

// tableEntry reads the virtual connection the table currently holds for a key.
func tableEntry(s *Server, key string) *vconn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[key]
}

// TestTeardownLeavesAReplacementConnectionAlone is the load-bearing case. The IN and
// OUT channels of one virtual connection tear down independently, so a client can
// reconnect under the same cookie between the two teardowns. The late teardown must
// not remove the replacement.
func TestTeardownLeavesAReplacementConnectionAlone(t *testing.T) {
	s := NewServer(Config{})
	const key = "vkey"

	first := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50000")
	s.teardown(key, first)

	second := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50001")
	if second == first {
		t.Fatal("the replacement reused the torn-down connection")
	}

	// The other channel of the first connection now ends and tears down.
	s.teardown(key, first)

	wantEq(t, tableEntry(s, key), second, "the table entry after the late teardown")
	wantFalse(t, second.isClosed(), "the replacement connection is closed")
}

// TestTeardownClosesItsOwnConnection proves the caller's connection still goes away:
// the identity check narrows which table entry is removed, not whether the channel
// loops are unblocked.
func TestTeardownClosesItsOwnConnection(t *testing.T) {
	s := NewServer(Config{})
	const key = "vkey"

	vc := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50000")
	s.teardown(key, vc)

	wantTrue(t, vc.isClosed(), "the torn-down connection is closed")
	if tableEntry(s, key) != nil {
		t.Error("the torn-down connection is still in the table")
	}
}

// TestGetOrCreateReplacesAClosedConnection proves a joining channel never takes a
// connection whose loops have already stopped, which would drop every PDU it queues.
func TestGetOrCreateReplacesAClosedConnection(t *testing.T) {
	s := NewServer(Config{})
	const key = "vkey"

	first := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50000")
	first.close() // the peer channel closed it, but its teardown has not run yet

	second := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50001")
	if second == first {
		t.Fatal("a joining channel was handed the closed connection")
	}
	wantFalse(t, second.isClosed(), "the replacement connection is closed")
	wantEq(t, tableEntry(s, key), second, "the table entry after the replacement")
}

// TestTableCapRefusesANewKeyButAllowsAReplacement proves the ceiling still bounds the
// table, and that replacing a closed entry is not refused by it, because such a
// replacement does not grow the table.
func TestTableCapRefusesANewKeyButAllowsAReplacement(t *testing.T) {
	s := NewServer(Config{})
	for i := range maxVirtualConnections {
		s.getOrCreate("key"+strconv.Itoa(i), "alice@hermex.test", "/mb/alice", "203.0.113.7:1")
	}

	if vc := s.getOrCreate("overflow", "alice@hermex.test", "/mb/alice", "203.0.113.7:1"); vc != nil {
		t.Error("a new key was admitted past the connection ceiling")
	}

	const key = "key0"
	tableEntry(s, key).close()
	replacement := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:2")
	if replacement == nil {
		t.Fatal("replacing a closed entry was refused at the ceiling")
	}
	wantFalse(t, replacement.isClosed(), "the replacement connection is closed")
}
